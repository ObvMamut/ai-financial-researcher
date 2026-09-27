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

	"github.com/mamut/claude-financial-researcher/internal/redact"
)

type alphaVantageProvider struct {
	client   *http.Client
	apiKey   string
	baseURL  string // overridable in tests
	limiter  *Limiter
	calendar *earningsCalendar
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
	client := &http.Client{Timeout: 30 * time.Second}
	// 25/day is the tier's hard cap; the burst of 1 is the important part.
	// AlphaVantage also polices roughly one request per second, and firing a
	// whole minute's allowance at once had it answering "please consider
	// spreading out your free API requests" instead of serving news — 8 of
	// 12 tickers came back empty in a full run. One every two seconds costs
	// half a minute across a shortlist and is answered every time.
	//
	// One limiter, shared: the daily budget belongs to the key, so the news
	// requests and the calendar request must draw from the same count.
	limiter := NewPersistentLimiter(25, 30, 1, dataDir, "alphavantage")
	// Hold back one of the 25 for the calendar (WaitReserved in
	// avcalendar.go). Specialists fetch news per ticker concurrently, and
	// without a reservation a shortlist of enough names spends the whole
	// budget on news before the calendar's single bulk request ever gets
	// scheduled — the cause of all 8 runs on 2026-09-24 shipping with no
	// verified earnings calendar at all.
	limiter.Reserve(calendarReservedSlots)
	var cache *Cache
	if dataDir != "" {
		cache = NewCache(dataDir)
	}
	return &alphaVantageProvider{
		client:  client,
		apiKey:  apiKey,
		baseURL: base,
		limiter: limiter,
		calendar: &earningsCalendar{
			client:  client,
			apiKey:  apiKey,
			baseURL: base,
			limiter: limiter,
			cache:   cache,
		},
	}
}

func (p *alphaVantageProvider) Name() string   { return "AlphaVantage" }
func (p *alphaVantageProvider) Source() string { return p.baseURL }

// Domains: news only.
//
// "sentiment" used to be served here too, from the same NEWS_SENTIMENT call the
// news domain makes — so two of the five nominally independent domains were
// reading one source and agreeing with each other by construction. Sentiment now
// comes from insider filings and option positioning (edgarform4.go,
// yahoooptions.go); headline tone stays a news fact.
//
// "technicals" is gone with the specialist of that name: the quant stage
// computes everything a GLOBAL_QUOTE call carried, from a full 2-year series
// rather than a single snapshot, and for every listing rather than US ones. The
// call was still being made and its facts never reached a prompt.
func (p *alphaVantageProvider) Domains() []string {
	return []string{"news"}
}
func (p *alphaVantageProvider) Available() bool { return p.apiKey != "" }

// DailyBudget reports this key's spend against the free tier's 25-a-day cap.
//
// It is deliberately not on the Provider interface: no other provider has a
// daily budget, and widening Provider to carry one would put a meaningless
// method on five implementations. The orchestrator type-asserts for it instead.
func (p *alphaVantageProvider) DailyBudget() (used, limit int) {
	return p.limiter.DailyBudget()
}

func (p *alphaVantageProvider) Fetch(ctx context.Context, domain string, ticker string) (TickerData, error) {
	if !p.Available() {
		return TickerData{}, ErrUnavailable
	}

	switch domain {
	case "news":
		return p.fetchNews(ctx, ticker)
	case "sentiment":
		return p.fetchNewsSentiment(ctx, ticker)
	default:
		return TickerData{}, ErrNotApplicable
	}
}

// fetchNews is the headline flow plus the one fact that outranks all of it: the
// next scheduled earnings date. The calendar is a single bulk request for the
// whole run, so a name with no headlines still gets its date, and a name whose
// headlines fail to arrive does not lose it.
func (p *alphaVantageProvider) fetchNews(ctx context.Context, ticker string) (TickerData, error) {
	td, newsErr := p.fetchNewsSentiment(ctx, ticker)
	td.Ticker = ticker

	date, ok, calErr := p.calendar.next(ctx, ticker)
	switch {
	case calErr != nil:
		td.Warnings = append(td.Warnings, "earnings calendar unavailable: "+calErr.Error())
	case ok:
		// First in the list: it is the fact that decides whether the trade can
		// be held through the window at all.
		td.Facts = append([]Fact{{
			Label:  EarningsFactLabel,
			Value:  date.Format("2006-01-02"),
			AsOf:   time.Now(),
			Source: "AlphaVantage earnings calendar",
		}}, td.Facts...)
	}

	// A news failure with a date in hand is a partial result, not a failure:
	// returning the error would discard the date with it.
	if newsErr != nil {
		if len(td.Facts) == 0 {
			return td, newsErr
		}
		td.Warnings = append(td.Warnings, "news feed unavailable: "+newsErr.Error())
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

// AVRelevanceFloor is the second route to counting an AlphaVantage item as
// coverage of this company, alongside the shared isSubjectRelevant text rule
// (see articlesFor). Pinned from the saved data rather than guessed, and the
// data says something less tidy than "high relevance means on-subject": of
// 628 AlphaVantage headline facts across runs/*/data/news.json (17 runs, 33
// tickers, as of 2026-09-27), 550 pass the text rule (min relevance 0.98) and
// 78 fail it
// — but 72 of those 78 fails still carry the *same* relevance_score, 1.00, as
// the bulk of the passes. AlphaVantage's own score does not, on this evidence,
// separate a subject-relevant item from one it scores highly for some other
// reason (an ISRG story about robotic surgery that never says "Intuitive
// Surgical"; "Micron Announces Leadership Appointments..." and "Marvell
// Falls 4%..." — both real mentions the text rule still misses only because
// each drops the multi-word normalized name's corporate half, the same
// residual class already named for Regeneron/Disney/Infineon/Daikin in
// docs/research/2026-09-25-news-relevance.md) — no floor between 0.90 and
// 1.00 changes how many of the 78 fails it would still admit (72, every time,
// because that is where they already sit). So this floor is not a
// discriminator at the high end; it is pinned at 0.98, the weakest score any
// text-confirmed article in this pipeline's history has carried, because that
// is the most conservative value the data actually supports — anything
// stricter would exclude coverage the pipeline already correctly counts, and
// it still correctly excludes the sample's only real low-relevance fails (six
// items at 0.65-0.71, a wrap-style "AFG Fiduciary Services Limited Partnership
// Has $3.87 Million Holdings" mention among them). See
// docs/research/2026-09-25-news-relevance.md's 2026-09-27 addendum for the
// full distribution and the acceptance audit this produced.
const AVRelevanceFloor = 0.98

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
	summary   string
	title     string
	url       string
	publisher string
	domain    string
	published time.Time
	relevance float64
	sentiment float64
	label     string
	// relevant is isSubjectRelevant on this item's title+summary, or AV's own
	// relevance score clearing AVRelevanceFloor — the same subject rule
	// Alpaca/Yahoo apply (newsfilter.go), plus AV's own second route. An item
	// that clears neither is context, not coverage: see the call site.
	relevant bool
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
		// A transport failure comes back as *url.Error carrying the whole
		// request URL — apikey and all. Left alone it reaches metadata.json.
		return TickerData{}, redact.Error(err)
	}
	defer resp.Body.Close()

	var data avNewsFeed
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return TickerData{}, redact.Error(err)
	}
	if msg := firstNonEmpty(data.ErrorMessage, data.Information, data.Note); msg != "" {
		rememberDailyQuota(p.limiter, msg)
		// AlphaVantage answers a rejected call by quoting the query string it
		// was sent. That prose is the credential-echo path this redaction was
		// added for; the diagnostic stays, the key does not.
		return TickerData{}, fmt.Errorf("%w: AlphaVantage: %s", ErrUnavailable, redact.String(msg))
	}

	arts := articlesFor(ctx, &data, symbol, ticker)
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
			Label:  USLineFactLabel,
			Value:  fmt.Sprintf("%s — news below is coverage of %s, the US listing of this company (%s)", symbol, symbol, USLineNote(ticker)),
			AsOf:   arts[0].published,
			Source: "AlphaVantage",
		})
	}

	// Aggregate first: a relevance-weighted mean of the per-ticker sentiment
	// scores, not the article-level "overall" score, which mixes in every other
	// name mentioned in the same piece. Only subject-relevant articles are
	// weighted in — a market wrap's sentiment is a read on the market, not on
	// this company, and folding it in is the same F3 defect as counting it for
	// coverage.
	var wsum, w float64
	var relevantCount int
	for _, a := range arts {
		if !a.relevant {
			continue
		}
		wsum += a.relevance * a.sentiment
		w += a.relevance
		relevantCount++
	}
	if w > 0 {
		agg := wsum / w
		td.Facts = append(td.Facts, Fact{
			Label:  "News Sentiment Score",
			Value:  fmt.Sprintf("%+.3f (%s) from %d articles", agg, sentimentLabel(agg), relevantCount),
			AsOf:   arts[0].published,
			Source: "AlphaVantage",
		})
	}

	// Then the headlines themselves — the citable part. All of them are
	// printed, subject and non-subject alike, exactly as the global feed
	// prints a mix of `tagged to this ticker` and `context, not about this
	// company` items (newsfilter.go's headlineFacts): a market wrap is real
	// information, just not evidence of what this company is doing.
	for i, a := range arts {
		if i >= newsHeadlines {
			break
		}
		rel := "tagged to this ticker"
		if !a.relevant {
			rel = "context, not about this company"
		}
		td.Facts = append(td.Facts, Fact{
			Label: fmt.Sprintf("Headline %d (relevance %.2f, sentiment %+.2f %s, %s)",
				i+1, a.relevance, a.sentiment, a.label, rel),
			Value:   fmt.Sprintf("%s — %s", a.title, a.publisher),
			AsOf:    a.published,
			Source:  a.domain,
			URL:     a.url,
			Summary: a.summary,
		})
	}

	return td, nil
}

func rememberDailyQuota(limiter *Limiter, message string) {
	m := strings.ToLower(message)
	if limiter != nil && (strings.Contains(m, "per day") || strings.Contains(m, "daily")) &&
		(strings.Contains(m, "limit") || strings.Contains(m, "quota") || strings.Contains(m, "rate")) {
		limiter.ExhaustForDay()
	}
}

// avSymbol renders a symbol the way AlphaVantage spells it: upper case, with US
// share classes hyphenated (BRK.B here is BRK-B there). Foreign listings are
// filtered out before this point — AlphaVantage has no spelling for them.
func avSymbol(ticker string) string {
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(ticker)), ".", "-")
}

// articlesFor keeps only feed items that carry a ticker_sentiment entry for the
// requested ticker (symbol — the AV-spelled query symbol, itself or the ADR
// line providerSymbol resolved), and scores them from that entry.
//
// Carrying a ticker_sentiment entry is AV's own membership test, not a subject
// test — the F3 defect (newsfilter.go) again, one provider over: a market wrap
// or a peer's note that tags this ticker in passing carries one too. So an item
// additionally counts as coverage only if isSubjectRelevant reads it or its own
// relevance_score clears AVRelevanceFloor. isSubjectRelevant is given the
// item's actual ticker_sentiment tag list (every ticker AV associated with
// this one story, not just the one being asked about) so its own "≤3 symbols"
// branch means what it means for Alpaca/Yahoo: a story tagging a small
// handful of names is informative on its own, one tagging a dozen (a market
// wrap) is not. ticker — the original CFR ticker, never symbol, AV's own
// spelling — is what carries the company name/alias lookup through ctx.
func articlesFor(ctx context.Context, data *avNewsFeed, symbol, ticker string) []article {
	want := avSymbol(symbol)
	var out []article
	for _, item := range data.Feed {
		var tags []string
		for _, ts := range item.TickerSentiment {
			tags = append(tags, ts.Ticker)
		}
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
				summary:   cleanDocument(item.Summary),
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
			a.relevant = isSubjectRelevant(ctx, tags, a.title, a.summary, ticker) ||
				a.relevance >= AVRelevanceFloor
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
