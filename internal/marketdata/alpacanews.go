package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// alpacaNewsCount is how many items to ask for. Alpaca caps a page at 50, which
// is well above what survives the relevance and age filters for one ticker.
const alpacaNewsCount = 50

// alpacaNewsProvider reads Benzinga-sourced headlines from Alpaca's news
// endpoint, keyed by the ticker's own symbol.
//
// It sits beside yahoonews.go rather than replacing it. Yahoo is the only
// source that reaches a foreign listing under its local symbol, and Yahoo is
// also the source that began answering 429 to this host on every endpoint — so
// the news domain, a quarter of the whole score, had exactly one provider for
// its global coverage and that provider is unreliable. Alpaca is US-only, so it
// covers the 155 US names and Yahoo keeps the other 117.
//
// It draws on the same 200-a-minute account budget as the price client, which
// is generous next to AlphaVantage's 25 a day.
type alpacaNewsProvider struct {
	client  *http.Client
	keyID   string
	secret  string
	baseURL string
	limiter *Limiter
}

func NewAlpacaNewsProvider(keyID, secret string) Provider {
	base := "https://data.alpaca.markets"
	rerouted := false
	if v := os.Getenv("CFR_ALPACA_BASE"); v != "" {
		base, rerouted = v, true
	}
	limiter := NewLimiter(math.MaxInt32, 180, 10)
	if rerouted {
		limiter = NewLimiter(math.MaxInt32, 1e6, 1e6)
	}
	return &alpacaNewsProvider{
		client:  &http.Client{Timeout: 30 * time.Second},
		keyID:   keyID,
		secret:  secret,
		baseURL: base,
		limiter: limiter,
	}
}

func (p *alpacaNewsProvider) Name() string      { return "Alpaca News" }
func (p *alpacaNewsProvider) Source() string    { return p.baseURL }
func (p *alpacaNewsProvider) Domains() []string { return []string{"news"} }
func (p *alpacaNewsProvider) Available() bool   { return p.keyID != "" && p.secret != "" }
func (p *alpacaNewsProvider) MacroFetch(ctx context.Context) ([]Fact, error) {
	return nil, ErrNotApplicable
}

type alpacaNewsResp struct {
	News []struct {
		ID        int64    `json:"id"`
		Headline  string   `json:"headline"`
		Summary   string   `json:"summary"`
		Content   string   `json:"content"`
		Author    string   `json:"author"`
		Source    string   `json:"source"`
		URL       string   `json:"url"`
		Symbols   []string `json:"symbols"`
		CreatedAt string   `json:"created_at"`
		UpdatedAt string   `json:"updated_at"`
	} `json:"news"`
	NextPageToken string `json:"next_page_token"`
}

func (p *alpacaNewsProvider) Fetch(ctx context.Context, domain string, ticker string) (TickerData, error) {
	if domain != "news" {
		return TickerData{}, ErrNotApplicable
	}
	// Alpaca lists US securities only, so a foreign listing is reachable only
	// through its US line where one exists. This is a structural limit, not a
	// fetch failure — BuildPack filters ErrNotApplicable out of the run's
	// errors, and yahoonews.go answers for the local symbol regardless.
	symbol, ok := providerSymbol(ticker)
	if !ok {
		return TickerData{}, fmt.Errorf("%w: %s has no US listing Alpaca covers", ErrNotApplicable, ticker)
	}

	resp, err := p.search(ctx, symbol)
	if err != nil {
		return TickerData{}, err
	}

	cutoff := time.Now().Add(-newsMaxAge)
	var arts []newsArticle
	var drops newsDrops
	seen := map[string]bool{}
	for _, n := range resp.News {
		headline := strings.TrimSpace(n.Headline)
		if headline == "" {
			drops.noTitle++
			continue
		}
		if seen[headline] {
			drops.duplicate++
			continue
		}
		published, err := time.Parse(time.RFC3339, n.CreatedAt)
		if err != nil || published.IsZero() {
			drops.noTimestamp++
			continue
		}
		if published.Before(cutoff) {
			drops.stale++
			continue
		}
		seen[headline] = true
		arts = append(arts, newsArticle{
			Title:     headline,
			Publisher: strings.TrimSpace(n.Source),
			Link:      n.URL,
			Published: published.UTC(),
			Related:   relatesTo(n.Symbols, symbol) || relatesTo(n.Symbols, ticker),
		})
	}

	td := TickerData{Ticker: ticker}
	if len(arts) == 0 {
		// A feed that delivered items and lost every one of them is not a quiet
		// name, and it produces byte-identical output. An empty news array is
		// silent — that one really is quiet.
		if len(resp.News) > 0 {
			td.Warnings = append(td.Warnings, drops.warning(len(resp.News), "created_at"))
		}
		return td, nil
	}

	sort.SliceStable(arts, func(i, j int) bool { return arts[i].Published.After(arts[j].Published) })
	note := ""
	if symbol != ticker {
		note = fmt.Sprintf(" (US line: %s)", symbol)
	}
	facts, warn := headlineFacts(arts, note)
	if warn != "" {
		td.Warnings = append(td.Warnings, warn)
		return td, nil
	}
	for i := range facts {
		for _, n := range resp.News {
			if n.URL == facts[i].URL {
				facts[i].Summary = cleanDocument(n.Summary)
				facts[i].Content = cleanDocument(n.Content)
				break
			}
		}
	}
	td.Facts = facts
	return td, nil
}

func (p *alpacaNewsProvider) search(ctx context.Context, symbol string) (alpacaNewsResp, error) {
	var out alpacaNewsResp
	if err := p.limiter.Wait(ctx); err != nil {
		return out, fmt.Errorf("%w: Alpaca news: %v", ErrUnavailable, err)
	}
	q := url.Values{}
	q.Set("symbols", symbol)
	q.Set("start", time.Now().Add(-newsMaxAge).UTC().Format(time.RFC3339))
	q.Set("limit", fmt.Sprint(alpacaNewsCount))
	q.Set("sort", "desc")
	// An item with no body is a wire-service stub; the headline is all this
	// pipeline reads, but a contentless one is usually a duplicate of a real
	// story that also arrives.
	q.Set("exclude_contentless", "true")
	q.Set("include_content", "true")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1beta1/news?"+q.Encode(), nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("APCA-API-KEY-ID", p.keyID)
	req.Header.Set("APCA-API-SECRET-KEY", p.secret)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return out, fmt.Errorf("%w: Alpaca news: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body := strings.TrimSpace(string(mustReadLimited(resp.Body, 256)))
		return out, fmt.Errorf("%w: Alpaca news HTTP %d for %s: %s", ErrUnavailable, resp.StatusCode, symbol, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("%w: Alpaca news: %v", ErrUnavailable, err)
	}
	return out, nil
}
