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

// yahooNewsCount is how many items to ask the search endpoint for; newsMaxAge
// and feedHeadlines are shared with the Alpaca feed and live in newsfilter.go,
// along with isSubjectRelevant — the relevance rule this comment used to claim
// was applied here before it actually was. A symbol in relatedTickers is not
// the same claim as the item being about that company; see isSubjectRelevant.
// Asking for more than we print costs nothing on a keyless endpoint.
const yahooNewsCount = 20

// yahooNewsProvider reads headlines from Yahoo Finance's keyless search
// endpoint, for the ticker's *own* symbol.
//
// It exists because the news domain — a quarter of the whole score — rested
// entirely on AlphaVantage, and AlphaVantage is two things this pipeline cannot
// build a domain on. It is US-only, so it reaches a foreign listing only through
// an ADR mapping that covers 26 of the 114 foreign names in the universe files:
// 9 of eu50's top-15 pre-screen ranks and 11 of asia100's were structurally
// unreachable. And its free tier allows 25 requests a day shared across news and
// the earnings calendar, which one twelve-name run very nearly exhausts on its
// own — on 2026-09-01 the counter read 24 of 25 and the run lost every headline
// for two names.
//
// Yahoo's search endpoint takes the local symbol (BMW.DE, 8035.T) and needs no
// key, so it covers both holes at once. What it does not carry is a per-article
// sentiment score; the news persona treats that as enrichment AlphaVantage may
// add, not as evidence it requires.
type yahooNewsProvider struct {
	auth    *yahooAuth
	baseURL string
	limiter *Limiter
}

func NewYahooNewsProvider() Provider {
	base := "https://query1.finance.yahoo.com"
	rerouted := false
	if v := os.Getenv("CFR_YAHOO_BASE"); v != "" {
		base, rerouted = v, true
	}
	limiter := NewLimiter(2000, 240, 5)
	if rerouted {
		limiter = NewLimiter(math.MaxInt32, 1e6, 1e6)
	}
	return &yahooNewsProvider{
		auth:    newYahooAuth(base, rerouted),
		baseURL: base,
		limiter: limiter,
	}
}

func (p *yahooNewsProvider) Name() string      { return "Yahoo News" }
func (p *yahooNewsProvider) Source() string    { return p.baseURL }
func (p *yahooNewsProvider) Domains() []string { return []string{"news"} }
func (p *yahooNewsProvider) Available() bool   { return true }
func (p *yahooNewsProvider) MacroFetch(ctx context.Context) ([]Fact, error) {
	return nil, ErrNotApplicable
}

type yahooSearchResp struct {
	News []struct {
		UUID                string   `json:"uuid"`
		Title               string   `json:"title"`
		Publisher           string   `json:"publisher"`
		Link                string   `json:"link"`
		ProviderPublishTime int64    `json:"providerPublishTime"`
		Type                string   `json:"type"`
		RelatedTickers      []string `json:"relatedTickers"`
	} `json:"news"`
	Finance struct {
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"finance"`
}

func (p *yahooNewsProvider) Fetch(ctx context.Context, domain string, ticker string) (TickerData, error) {
	if domain != "news" {
		return TickerData{}, ErrNotApplicable
	}
	// Deliberately not providerSymbol: the whole point of this source is that it
	// answers for the listing itself, so a name with no US line still has news.
	resp, err := p.search(ctx, ticker)
	if err != nil {
		return TickerData{}, err
	}

	td, unresolved := p.newsData(ctx, resp, ticker, ticker)
	// An empty local feed counts as unresolved too: Yahoo indexed no news
	// under HDFCBANK.NS, PHIA.AS or BBVA.MC on 2026-10-07 while their ADR lines
	// carried the issuer stories, and treating that as a quiet name left all
	// three without news coverage.
	if !unresolved && len(resp.News) > 0 {
		return td, nil
	}
	adr := adrMap[strings.ToUpper(ticker)]
	if adr == "" || strings.EqualFold(adr, ticker) {
		return td, nil
	}
	// One mapped major-exchange query only, after an unresolved local feed.
	// Transport errors and stale-only responses never trigger it; an empty
	// local feed does (see above).
	fallback, err := p.search(ctx, adr)
	if err != nil {
		return td, err
	}
	next, _ := p.newsData(ctx, fallback, ticker, adr)
	// The first attempt's own diagnostics (e.g. "unresolved_symbol") describe a
	// gap that no longer exists once the ADR-mapped fallback found facts; carry
	// them forward only when the fallback is itself empty, exactly as Warnings
	// already does, so a ticker that fully resolved via the mapping does not
	// still report a stale withheld/unresolved entry.
	if len(next.Facts) == 0 {
		next.Diagnostics = append(td.Diagnostics, next.Diagnostics...)
		next.Warnings = append(td.Warnings, next.Warnings...)
	}
	return next, nil
}

func (p *yahooNewsProvider) newsData(ctx context.Context, resp yahooSearchResp, ticker, query string) (TickerData, bool) {
	cutoff := time.Now().Add(-newsMaxAge)
	var arts []newsArticle
	// Every filter below is correct on its own, and every one of them ends in
	// the same empty result as a name with genuinely no news. Counting why each
	// item went is what separates "nothing happened" from "the feed changed
	// shape", so the counts are kept whether or not they are ever printed.
	var drops newsDrops
	seen := map[string]bool{}
	for _, n := range resp.News {
		title := strings.TrimSpace(n.Title)
		if title == "" {
			drops.noTitle++
			continue
		}
		if seen[title] {
			drops.duplicate++
			continue
		}
		published := time.Unix(n.ProviderPublishTime, 0).UTC()
		if n.ProviderPublishTime <= 0 {
			drops.noTimestamp++
			continue
		}
		if published.Before(cutoff) {
			drops.stale++
			continue
		}
		seen[title] = true
		arts = append(arts, newsArticle{
			Title:     title,
			Publisher: strings.TrimSpace(n.Publisher),
			Link:      n.Link,
			Published: published,
			Related:   isSubjectRelevant(ctx, n.RelatedTickers, title, "", ticker),
		})
	}
	if len(arts) == 0 {
		// Not an error: a quiet name has no news, and saying so is different
		// from a fetch that failed. Coverage stays false either way.
		td := TickerData{Ticker: ticker}
		// But a feed that delivered items and lost every one of them is not a
		// quiet name, and it produces byte-identical output. This source is
		// meant to cover the whole shortlist for a domain carrying 0.25 of the
		// score, so that failure would take news to zero everywhere while the
		// run reported itself complete. An empty news array stays silent —
		// that one really is a quiet name.
		if len(resp.News) > 0 {
			message := drops.warning(len(resp.News), "providerPublishTime")
			td.Warnings = append(td.Warnings, message)
			reason := "filtered_items"
			if drops.stale == len(resp.News) {
				reason = "stale"
			}
			td.Diagnostics = append(td.Diagnostics, sourceDiagnostic(p.Name(), ticker, "news", reason, "withheld", message))
		}
		return td, false
	}
	// Newest first. A search endpoint orders by its own relevance score, which
	// is not the order a catalyst read wants.
	sort.SliceStable(arts, func(i, j int) bool { return arts[i].Published.After(arts[j].Published) })

	td := TickerData{Ticker: ticker}
	note := ""
	if query != ticker {
		note = " (issuer news via mapped ADR " + query + "; local listing " + ticker + ")"
	}
	facts, warn := headlineFacts(arts, note)
	if warn != "" {
		td.Warnings = append(td.Warnings, warn)
		td.Diagnostics = append(td.Diagnostics, sourceDiagnostic(p.Name(), ticker, "news", "unresolved_symbol", "withheld", warn))
		return td, true
	}
	td.Facts = facts
	return td, false
}

func (p *yahooNewsProvider) search(ctx context.Context, ticker string) (yahooSearchResp, error) {
	var out yahooSearchResp
	if err := p.limiter.Wait(ctx); err != nil {
		return out, fmt.Errorf("%w: Yahoo news: %v", ErrUnavailable, err)
	}
	q := url.Values{}
	q.Set("q", yahooSymbol(ticker))
	q.Set("newsCount", fmt.Sprint(yahooNewsCount))
	q.Set("quotesCount", "0")
	q.Set("enableFuzzyQuery", "false")
	u := p.baseURL + "/v1/finance/search?" + q.Encode()

	// The search endpoint is not always crumb-gated, but Do degrades to an
	// uncrumbed request when the handshake fails, so routing through it costs
	// nothing and survives Yahoo turning the gate on.
	resp, err := p.auth.Do(ctx, u)
	if err != nil {
		return out, fmt.Errorf("%w: Yahoo news: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("%w: Yahoo news HTTP %d for %s", ErrUnavailable, resp.StatusCode, ticker)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("%w: Yahoo news: %v", ErrUnavailable, err)
	}
	if out.Finance.Error != nil {
		return out, fmt.Errorf("%w: Yahoo news: %s", ErrUnavailable, out.Finance.Error.Description)
	}
	return out, nil
}
