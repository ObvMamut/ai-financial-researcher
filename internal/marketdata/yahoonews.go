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
// Yahoo's search endpoint needs no key. It was added for the local symbol
// (BMW.DE, 8035.T), but by 2026-10-07 it returned nothing for any foreign local
// symbol, so a foreign listing is now reached through its ADR line where one is
// mapped and through a company-name search where none is (byCompanyName). What it does not carry is a per-article
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

	td, unresolved := p.newsData(ctx, resp, ticker, "", false)
	// An empty local feed counts as unresolved too: Yahoo indexed no news
	// under HDFCBANK.NS, PHIA.AS or BBVA.MC on 2026-10-07 while their ADR lines
	// carried the issuer stories, and treating that as a quiet name left all
	// three without news coverage.
	if !unresolved && len(resp.News) > 0 {
		return td, nil
	}
	adr := adrMap[strings.ToUpper(ticker)]
	if adr == "" || strings.EqualFold(adr, ticker) {
		if len(resp.News) == 0 && isForeignListing(ticker) {
			return p.byCompanyName(ctx, ticker, td)
		}
		return td, nil
	}
	// One mapped major-exchange query only, after an unresolved local feed.
	// Transport errors and stale-only responses never trigger it; an empty
	// local feed does (see above).
	fallback, err := p.search(ctx, adr)
	if err != nil {
		return td, err
	}
	next, _ := p.newsData(ctx, fallback, ticker, " (issuer news via mapped ADR "+adr+"; local listing "+ticker+")", false)
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

// byCompanyName is the last query for a foreign listing with no US line whose
// local feed came back empty. On 2026-10-07 Yahoo's search returned nothing
// for the local symbol of all 89 unmapped foreign listings in the universe
// (0700.HK, SIE.DE, 7203.T, …), while a search for the company name returned
// fresh items tagged with that same local symbol. Yahoo's tags on a name
// search are loose ("Kroger and Costco help shoppers…" tagged BMW.DE), and a
// root can collide with a US ticker (CSL.AX's "CSL" is Carlisle), so here an
// item counts as about the company only when it is tagged with the listing
// *and* its headline names the company. One request, only after an empty
// local feed.
func (p *yahooNewsProvider) byCompanyName(ctx context.Context, ticker string, local TickerData) (TickerData, error) {
	names := companyNamesFor(ctx, ticker)
	if len(names) == 0 {
		return local, nil
	}
	query, alias := normalizeCompanyName(names[0])
	if alias != "" {
		query = alias
	}
	if query == "" {
		return local, nil
	}
	resp, err := p.searchQuery(ctx, query)
	if err != nil {
		return local, err
	}
	next, _ := p.newsData(ctx, resp, ticker, fmt.Sprintf(" (issuer news via company-name search %q; local listing %s)", query, ticker), true)
	if len(next.Facts) == 0 {
		next.Diagnostics = append(local.Diagnostics, next.Diagnostics...)
		next.Warnings = append(local.Warnings, next.Warnings...)
	}
	return next, nil
}

// newsData turns one search response into facts for ticker. note is appended
// to every headline to say how the feed was reached when it was not the
// listing's own symbol. byName additionally requires the headline to name the
// company for the item to count as about it (see byCompanyName).
func (p *yahooNewsProvider) newsData(ctx context.Context, resp yahooSearchResp, ticker, note string, byName bool) (TickerData, bool) {
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
			Related:   isSubjectRelevant(ctx, n.RelatedTickers, title, "", ticker) && (!byName || headlineNamesCompany(ctx, title, ticker)),
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
	return p.searchQuery(ctx, yahooSymbol(ticker))
}

// searchQuery runs the search endpoint on a raw query string.
func (p *yahooNewsProvider) searchQuery(ctx context.Context, query string) (yahooSearchResp, error) {
	var out yahooSearchResp
	if err := p.limiter.Wait(ctx); err != nil {
		return out, fmt.Errorf("%w: Yahoo news: %v", ErrUnavailable, err)
	}
	q := url.Values{}
	q.Set("q", query)
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
		return out, fmt.Errorf("%w: Yahoo news HTTP %d for %s", ErrUnavailable, resp.StatusCode, query)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("%w: Yahoo news: %v", ErrUnavailable, err)
	}
	if out.Finance.Error != nil {
		return out, fmt.Errorf("%w: Yahoo news: %s", ErrUnavailable, out.Finance.Error.Description)
	}
	return out, nil
}

// headlineNamesCompany reports whether a headline names the company itself,
// by its universe name or an alias, ignoring ticker roots.
func headlineNamesCompany(ctx context.Context, headline, ticker string) bool {
	for _, name := range companyNamesFor(ctx, ticker) {
		if mentionsCompanyName(headline, name) {
			return true
		}
	}
	return false
}
