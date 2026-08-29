package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type alphaVantageProvider struct {
	client  *http.Client
	apiKey  string
	baseURL string // overridable in tests
	limiter *Limiter
}

// NewAlphaVantageProvider builds the provider for the free tier (25 requests per
// day, 5 per minute). dataDir persists the daily count so the budget is real
// across processes; pass "" for an in-memory limiter (tests).
func NewAlphaVantageProvider(apiKey, dataDir string) Provider {
	base := "https://www.alphavantage.co"
	// CFR_AV_BASE reroutes query requests (tests, proxies/mirrors).
	if v := os.Getenv("CFR_AV_BASE"); v != "" {
		base = v
	}
	return &alphaVantageProvider{
		client:  &http.Client{Timeout: 10 * time.Second},
		apiKey:  apiKey,
		baseURL: base,
		// 25/day is the tier's hard cap; the burst of 1 is the important part.
		// AlphaVantage also polices roughly one request per second, and firing a
		// whole minute's allowance at once had it answering "please consider
		// spreading out your free API requests" instead of serving news — 8 of
		// 12 tickers came back empty in a full run. One every two seconds costs
		// half a minute across a shortlist and is answered every time.
		limiter: NewPersistentLimiter(25, 30, 1, dataDir, "alphavantage"),
	}
}

func (p *alphaVantageProvider) Name() string   { return "AlphaVantage" }
func (p *alphaVantageProvider) Source() string { return p.baseURL }
func (p *alphaVantageProvider) Domains() []string {
	return []string{"technicals", "news", "sentiment"}
}
func (p *alphaVantageProvider) Available() bool { return p.apiKey != "" }

func (p *alphaVantageProvider) Fetch(ctx context.Context, domain string, ticker string) (TickerData, error) {
	if !p.Available() {
		return TickerData{}, ErrUnavailable
	}

	switch domain {
	case "technicals":
		return p.fetchGlobalQuote(ctx, ticker)
	case "news", "sentiment":
		return p.fetchNewsSentiment(ctx, ticker)
	default:
		return TickerData{}, ErrNotApplicable
	}
}

// CacheDomain collapses "news" and "sentiment" onto one key: both are served by
// a single NEWS_SENTIMENT request, so caching them separately would double the
// per-run API spend (24 calls instead of 12 — over the free 25/day tier).
func (p *alphaVantageProvider) CacheDomain(domain string) string {
	switch domain {
	case "news", "sentiment":
		return newsSentimentCacheDomain
	default:
		return domain
	}
}

// newsSentimentCacheDomain is the shared cache key segment for the news and
// sentiment domains (see CacheDomain).
const newsSentimentCacheDomain = "news_sentiment"

func (p *alphaVantageProvider) fetchGlobalQuote(ctx context.Context, ticker string) (TickerData, error) {
	if isForeignListing(ticker) {
		return TickerData{}, fmt.Errorf("%w: %s is not a US listing", ErrNotApplicable, ticker)
	}
	if err := p.limiter.Wait(ctx); err != nil {
		return TickerData{}, fmt.Errorf("%w: AlphaVantage: %v", ErrUnavailable, err)
	}

	v := url.Values{}
	v.Set("function", "GLOBAL_QUOTE")
	v.Set("symbol", avSymbol(ticker))
	v.Set("apikey", p.apiKey)

	u := p.baseURL + "/query?" + v.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return TickerData{}, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return TickerData{}, err
	}
	defer resp.Body.Close()

	var data struct {
		Quote map[string]string `json:"Global Quote"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return TickerData{}, err
	}

	td := TickerData{Ticker: ticker}
	if price, ok := data.Quote["05. price"]; ok {
		td.Facts = append(td.Facts, Fact{
			Label:  "Last Price",
			Value:  price,
			AsOf:   time.Now(),
			Source: "AlphaVantage",
		})
	}
	if change, ok := data.Quote["10. change percent"]; ok {
		td.Facts = append(td.Facts, Fact{
			Label:  "Change %",
			Value:  change,
			AsOf:   time.Now(),
			Source: "AlphaVantage",
		})
	}

	return td, nil
}

// newsFeedLimit is what we ask NEWS_SENTIMENT for. The endpoint returns 50
// items regardless of a smaller limit, so ask for what we actually read rather
// than pretending we only want 5.
const newsFeedLimit = 50

// newsHeadlines is how many articles per ticker are surfaced as facts. Enough
// for an analyst to form a narrative; few enough to keep the prompt compact.
const newsHeadlines = 6

// avNewsFeed mirrors the NEWS_SENTIMENT response. Alpha Vantage encodes the
// per-ticker scores as JSON strings, hence the string-typed score fields.
type avNewsFeed struct {
	Items string `json:"items"`
	Feed  []struct {
		Title           string `json:"title"`
		URL             string `json:"url"`
		TimePublished   string `json:"time_published"` // 20260828T143000
		Source          string `json:"source"`
		SourceDomain    string `json:"source_domain"`
		Summary         string `json:"summary"`
		TickerSentiment []struct {
			Ticker         string `json:"ticker"`
			RelevanceScore string `json:"relevance_score"`
			SentimentScore string `json:"ticker_sentiment_score"`
			SentimentLabel string `json:"ticker_sentiment_label"`
		} `json:"ticker_sentiment"`
	} `json:"feed"`

	// Alpha Vantage reports quota exhaustion and bad requests as a 200 with one
	// of these fields set. Surfacing them as errors is what keeps an exhausted
	// quota from looking like "no news exists".
	Information  string `json:"Information"`
	Note         string `json:"Note"`
	ErrorMessage string `json:"Error Message"`
}

// article is one feed item scored for the requested ticker.
type article struct {
	title     string
	url       string
	publisher string
	domain    string
	published time.Time
	relevance float64
	sentiment float64
	label     string
}

func (p *alphaVantageProvider) fetchNewsSentiment(ctx context.Context, ticker string) (TickerData, error) {
	// AlphaVantage rejects a dotted foreign symbol ("Invalid ticker format:
	// 2330.TW"). Spending one of 25 daily requests to be told so wastes half the
	// budget on a shortlist with European and Asian names in it. But most of
	// those names trade a US line, and TSM's news is TSMC's news — so ask under
	// the US symbol when there is one, and skip only when there is not.
	symbol, ok := providerSymbol(ticker)
	if !ok {
		return TickerData{}, fmt.Errorf("%w: %s is not a US listing and has no US line", ErrNotApplicable, ticker)
	}
	if err := p.limiter.Wait(ctx); err != nil {
		return TickerData{}, fmt.Errorf("%w: AlphaVantage: %v", ErrUnavailable, err)
	}

	v := url.Values{}
	v.Set("function", "NEWS_SENTIMENT")
	v.Set("tickers", avSymbol(symbol))
	v.Set("limit", strconv.Itoa(newsFeedLimit))
	v.Set("apikey", p.apiKey)

	u := p.baseURL + "/query?" + v.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return TickerData{}, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return TickerData{}, err
	}
	defer resp.Body.Close()

	var data avNewsFeed
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return TickerData{}, err
	}
	if msg := firstNonEmpty(data.ErrorMessage, data.Information, data.Note); msg != "" {
		return TickerData{}, fmt.Errorf("%w: AlphaVantage: %s", ErrUnavailable, msg)
	}

	arts := articlesFor(&data, symbol)
	if len(arts) == 0 {
		return TickerData{Ticker: ticker}, nil
	}

	// Most relevant first; ties broken by recency so a stale article never
	// outranks today's on an equal relevance score.
	sort.SliceStable(arts, func(i, j int) bool {
		if arts[i].relevance != arts[j].relevance {
			return arts[i].relevance > arts[j].relevance
		}
		return arts[i].published.After(arts[j].published)
	})

	td := TickerData{Ticker: ticker}

	// When the facts describe a different listing from the one asked about, say
	// so in the pack. An agent reading "Headline 1 … TSMC" under 2330.TW must
	// know it is reading US-line coverage, not Taipei coverage.
	if symbol != ticker {
		td.Facts = append(td.Facts, Fact{
			Label:  "US line",
			Value:  fmt.Sprintf("%s — news below is coverage of %s, the US listing of this company (%s)", symbol, symbol, USLineNote(ticker)),
			AsOf:   arts[0].published,
			Source: "AlphaVantage",
		})
	}

	// Aggregate first: a relevance-weighted mean of the per-ticker sentiment
	// scores, not the article-level "overall" score, which mixes in every other
	// name mentioned in the same piece.
	var wsum, w float64
	for _, a := range arts {
		wsum += a.relevance * a.sentiment
		w += a.relevance
	}
	agg := 0.0
	if w > 0 {
		agg = wsum / w
	}
	td.Facts = append(td.Facts, Fact{
		Label:  "News Sentiment Score",
		Value:  fmt.Sprintf("%+.3f (%s) from %d articles", agg, sentimentLabel(agg), len(arts)),
		AsOf:   arts[0].published,
		Source: "AlphaVantage",
	})

	// Then the headlines themselves — the citable part.
	for i, a := range arts {
		if i >= newsHeadlines {
			break
		}
		td.Facts = append(td.Facts, Fact{
			Label: fmt.Sprintf("Headline %d (relevance %.2f, sentiment %+.2f %s)",
				i+1, a.relevance, a.sentiment, a.label),
			Value:  fmt.Sprintf("%s — %s", a.title, a.publisher),
			AsOf:   a.published,
			Source: a.domain,
			URL:    a.url,
		})
	}

	return td, nil
}

// avSymbol renders a symbol the way AlphaVantage spells it: upper case, with US
// share classes hyphenated (BRK.B here is BRK-B there). Foreign listings are
// filtered out before this point — AlphaVantage has no spelling for them.
func avSymbol(ticker string) string {
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(ticker)), ".", "-")
}

// articlesFor keeps only feed items that carry a ticker_sentiment entry for the
// requested ticker, and scores them from that entry. An article that merely
// mentions the name in passing has a low relevance_score and sorts itself out.
func articlesFor(data *avNewsFeed, ticker string) []article {
	want := avSymbol(ticker)
	var out []article
	for _, item := range data.Feed {
		for _, ts := range item.TickerSentiment {
			if strings.ToUpper(strings.TrimSpace(ts.Ticker)) != want {
				continue
			}
			rel, err := strconv.ParseFloat(ts.RelevanceScore, 64)
			if err != nil {
				continue
			}
			score, _ := strconv.ParseFloat(ts.SentimentScore, 64)
			a := article{
				title:     strings.TrimSpace(item.Title),
				url:       strings.TrimSpace(item.URL),
				publisher: strings.TrimSpace(item.Source),
				domain:    strings.TrimSpace(item.SourceDomain),
				published: parseAVTime(item.TimePublished),
				relevance: rel,
				sentiment: score,
				label:     strings.TrimSpace(ts.SentimentLabel),
			}
			if a.title == "" || a.url == "" {
				break // nothing citable; skip this item
			}
			if a.domain == "" {
				a.domain = "AlphaVantage"
			}
			if a.publisher == "" {
				a.publisher = a.domain
			}
			out = append(out, a)
			break
		}
	}
	return out
}

// parseAVTime decodes Alpha Vantage's compact timestamp (20260828T143000).
// An unparseable value yields the zero time rather than a fabricated "now".
func parseAVTime(s string) time.Time {
	t, err := time.Parse("20060102T150405", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

// sentimentLabel maps an aggregate score onto Alpha Vantage's own label bands.
func sentimentLabel(score float64) string {
	switch {
	case score <= -0.35:
		return "Bearish"
	case score <= -0.15:
		return "Somewhat-Bearish"
	case score < 0.15:
		return "Neutral"
	case score < 0.35:
		return "Somewhat-Bullish"
	default:
		return "Bullish"
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (p *alphaVantageProvider) MacroFetch(ctx context.Context) ([]Fact, error) {
	return nil, ErrNotApplicable // AlphaVantage is per-ticker only here
}
