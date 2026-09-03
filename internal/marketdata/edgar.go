package marketdata

import (
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed data/cik_map.csv
var cikMapRaw string

// secTickersPath is SEC's full ticker->CIK directory (~10k filers). The embedded
// CSV is a 160-row stub covering roughly two thirds of the S&P names we screen
// and none of the rest; without this fetch, "fundamentals" silently misses most
// of any shortlist (AMD, SNOW and MELI are all SEC filers and all absent).
const secTickersPath = "/files/company_tickers.json"

// foreignExchanges holds the Yahoo-style suffixes of listings that US-market
// data sources do not cover — SEC has no filings for them and AlphaVantage
// rejects the symbol outright. A foreign primary listing is not a lookup
// failure, and reporting it as one buried the genuine gaps under one error per
// European and Asian name. US share classes (BRK.B, BF.B, HEI.A) use .A/.B and
// are deliberately absent here.
var foreignExchanges = map[string]bool{
	"AS": true, "AT": true, "AX": true, "BA": true, "BE": true, "BK": true,
	"BO": true, "BR": true, "CN": true, "CO": true, "DE": true, "DU": true,
	"F": true, "HE": true, "HK": true, "HM": true, "IC": true, "IR": true,
	"IS": true, "JK": true, "JO": true, "KL": true, "KQ": true, "KS": true,
	"L": true, "LS": true, "MC": true, "ME": true, "MI": true, "MU": true,
	"MX": true, "NE": true, "NS": true, "NZ": true, "OL": true, "PA": true,
	"PR": true, "SA": true, "SG": true, "SI": true, "SN": true, "SS": true,
	"ST": true, "SW": true, "SZ": true, "T": true, "TA": true, "TL": true,
	"TO": true, "TW": true, "TWO": true, "V": true, "VI": true, "VN": true,
	"VS": true, "WA": true,
}

// edgarRequestsPerSecond mirrors SEC's own stated guidance for automated
// access: "10 requests/second at most" (see the form4MaxDocs comment in
// edgarform4.go). SEC imposes no daily quota, unlike AlphaVantage — only the
// per-second rate needs throttling.
const edgarRequestsPerSecond = 10

type edgarProvider struct {
	client       *http.Client
	contactEmail string
	cache        *Cache
	factsBase    string // companyfacts host
	tickersBase  string // ticker directory host (a different SEC host)
	limiter      *Limiter

	mu        sync.Mutex
	cikMap    map[string]string
	refreshed bool
	// submissions memoises one issuer's filing index for the life of the run.
	// Three legs now read it — Form 4, Form 144 and the 13D/G stakes — and it is
	// one document answering all three, so fetching it three times per ticker
	// spends SEC's rate limit on the same bytes.
	submissions map[string]*submissionsResp
	// The tracked-manager 13F index is built at most once per process and kept
	// on disk for a quarter; thirteenFTried stops a failed build being retried
	// for every ticker in the shortlist.
	thirteenFIdx   *thirteenFIndex
	thirteenFTried bool
}

// NewEdgarProvider builds the SEC fundamentals provider. cache may be nil; it is
// used to keep the ~10k-row ticker directory on disk for a day so only the first
// run of the day pays for it.
func NewEdgarProvider(contactEmail string, cache *Cache) Provider {
	p := &edgarProvider{
		client:       &http.Client{Timeout: 20 * time.Second},
		contactEmail: contactEmail,
		cache:        cache,
		cikMap:       make(map[string]string),
		submissions:  make(map[string]*submissionsResp),
		factsBase:    "https://data.sec.gov",
		tickersBase:  "https://www.sec.gov",
		// No daily budget to enforce (SEC doesn't quote one) — dailyLimit is
		// set high enough to never bind, only the per-second rate matters.
		limiter: NewLimiter(1<<30, edgarRequestsPerSecond*60, edgarRequestsPerSecond),
	}
	// CFR_SEC_BASE reroutes both SEC hosts at once (tests, proxies/mirrors).
	if v := os.Getenv("CFR_SEC_BASE"); v != "" {
		p.factsBase, p.tickersBase = v, v
	}
	p.loadEmbeddedCIKMap()
	return p
}

func (p *edgarProvider) Name() string   { return "EDGAR" }
func (p *edgarProvider) Source() string { return p.factsBase }

// Domains: fundamentals from companyfacts, and sentiment from Form 4 insider
// filings. Both are keyless SEC data behind one CIK map and one identified
// client, which is why insider activity lives on this provider rather than a
// second one that would duplicate all of it.
func (p *edgarProvider) Domains() []string {
	return []string{"fundamentals", "sentiment"}
}
func (p *edgarProvider) Available() bool { return p.contactEmail != "" }

// userAgent satisfies SEC's requirement that automated clients identify
// themselves with contact information.
func (p *edgarProvider) userAgent() string {
	return fmt.Sprintf("ClaudeFinancialResearcher/1.0 (%s)", p.contactEmail)
}

func (p *edgarProvider) loadEmbeddedCIKMap() {
	records, err := csv.NewReader(strings.NewReader(cikMapRaw)).ReadAll()
	if err != nil {
		return
	}
	for _, rec := range records {
		if len(rec) >= 2 {
			p.cikMap[secTicker(rec[0])] = padCIK(rec[1])
		}
	}
}

// ensureCIKMap layers SEC's full directory over the embedded stub. It runs at
// most once per process: on failure the stub is a working fallback, and
// retrying a dead endpoint once per shortlisted ticker helps nobody.
func (p *edgarProvider) ensureCIKMap(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refreshed {
		return
	}
	p.refreshed = true

	if p.cache != nil {
		var cached map[string]string
		if ok, _ := p.cache.Get(p.tickersBase, p.Name(), "cik_map", "ALL", &cached); ok && len(cached) > 0 {
			p.mergeCIKMap(cached)
			return
		}
	}

	fetched, err := p.fetchCIKMap(ctx)
	if err != nil || len(fetched) == 0 {
		return // embedded stub stands
	}
	p.mergeCIKMap(fetched)
	if p.cache != nil {
		p.cache.Set(p.tickersBase, p.Name(), "cik_map", "ALL", fetched)
	}
}

func (p *edgarProvider) mergeCIKMap(m map[string]string) {
	for t, cik := range m {
		p.cikMap[secTicker(t)] = padCIK(cik)
	}
}

// secTickerEntry is one row of SEC's company_tickers.json, which is a JSON
// object keyed by row index rather than an array.
type secTickerEntry struct {
	CIK    json.Number `json:"cik_str"`
	Ticker string      `json:"ticker"`
	Title  string      `json:"title"`
}

func (p *edgarProvider) fetchCIKMap(ctx context.Context) (map[string]string, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.tickersBase+secTickersPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", p.userAgent())

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: SEC ticker directory returned %d", ErrUnavailable, resp.StatusCode)
	}

	var raw map[string]secTickerEntry
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	out := make(map[string]string, len(raw))
	for _, e := range raw {
		t := secTicker(e.Ticker)
		cik := strings.TrimSpace(e.CIK.String())
		if t == "" || cik == "" || cik == "0" {
			continue
		}
		out[t] = padCIK(cik)
	}
	return out, nil
}

// secTicker normalises a symbol to SEC's spelling: upper case, and share
// classes joined with a dash (BRK.B in our universe files is BRK-B at SEC).
func secTicker(raw string) string {
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(raw)), ".", "-")
}

func padCIK(cik string) string {
	cik = strings.TrimSpace(cik)
	if len(cik) < 10 {
		cik = strings.Repeat("0", 10-len(cik)) + cik
	}
	return cik
}

// isForeignListing reports whether a symbol carries a foreign exchange suffix.
func isForeignListing(ticker string) bool {
	i := strings.LastIndex(ticker, ".")
	if i < 0 {
		return false
	}
	return foreignExchanges[strings.ToUpper(ticker[i+1:])]
}

func (p *edgarProvider) lookupCIK(ticker string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cik, ok := p.cikMap[secTicker(ticker)]
	return cik, ok
}

func (p *edgarProvider) Fetch(ctx context.Context, domain string, ticker string) (TickerData, error) {
	switch domain {
	case "fundamentals":
	case "sentiment":
		return p.fetchInsiderActivity(ctx, ticker)
	default:
		return TickerData{}, ErrNotApplicable
	}
	// Some foreign listings file with SEC under a US line — Linde plc trades as
	// LIN and files a full 10-K. Ask under that symbol rather than skipping the
	// name outright. IFRS filers (ASML, TSM) resolve too but carry no us-gaap
	// facts, so they return empty and stay honestly uncovered.
	symbol, ok := providerSymbol(ticker)
	if !ok {
		return TickerData{}, fmt.Errorf("%w: %s is not a US listing and has no US line", ErrNotApplicable, ticker)
	}

	p.ensureCIKMap(ctx)
	cik, ok := p.lookupCIK(symbol)
	if !ok {
		return TickerData{}, fmt.Errorf("%w: ticker %s not in SEC CIK map", ErrUnavailable, symbol)
	}

	if err := p.limiter.Wait(ctx); err != nil {
		return TickerData{}, err
	}
	url := fmt.Sprintf("%s/api/xbrl/companyfacts/CIK%s.json", p.factsBase, cik)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return TickerData{}, err
	}
	req.Header.Set("User-Agent", p.userAgent())

	resp, err := p.client.Do(req)
	if err != nil {
		return TickerData{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return TickerData{}, fmt.Errorf("%w: SEC API returned %d", ErrUnavailable, resp.StatusCode)
	}

	var data struct {
		Facts map[string]map[string]xbrlFact `json:"facts"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return TickerData{}, err
	}

	td := TickerData{Ticker: ticker}
	filingURL := fmt.Sprintf("https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=%s&type=10-K", cik)

	// extract emits one fact per label, choosing the best USD observation across
	// all the GAAP tags that could carry it.
	//
	// Three traps live here. Filers migrate between tags — NVDA's ASC 606
	// revenue tag stops in FY2022 and continues under another — so taking the
	// first tag that has *any* data pins a figure four years stale. One tag's
	// slice mixes annual with quarterly frames in no particular order, so a
	// quarterly revenue printed beside an annual net income reads as a company
	// earning four times its sales. And preferring annual frames unconditionally
	// — the previous rule — reintroduces the first trap through the back door:
	// on 2026-08-29 it printed NVDA's FY2022 revenue ($26.9B) beside its FY2026
	// net income ($118.0B), and the specialist reported a "37% net margin" whose
	// actual quotient is 438%.
	//
	// The rule is freshness first, tidiness second: prefer the latest annual
	// frame, but only while it is within annualGracePeriod of the latest
	// observation of any kind. Past that, take the freshest figure and say in
	// the label that the frame is quarterly.
	// extractUnit is extract generalised over namespace and unit: share counts
	// live in the `dei` namespace under a "shares" unit, and diluted EPS under
	// "USD/shares". Without those two, no multiple is computable from SEC data
	// at all, and the fundamentals domain could only ever report raw dollars.
	extractUnit := func(namespace, unit, label string, keys ...string) {
		ns, ok := data.Facts[namespace]
		if !ok {
			return
		}
		var (
			latest, latestAnnual         xbrlObs
			haveLatest, haveLatestAnnual bool
		)
		for _, key := range keys {
			fact, ok := ns[key]
			if !ok {
				continue
			}
			for _, o := range fact.Units[unit] {
				end, err := time.Parse("2006-01-02", o.End)
				if err != nil {
					continue
				}
				cur := xbrlObs{val: o.Val, end: end, annual: o.FP == "FY"}
				if !haveLatest || cur.end.After(latest.end) {
					latest, haveLatest = cur, true
				}
				if cur.annual && (!haveLatestAnnual || cur.end.After(latestAnnual.end)) {
					latestAnnual, haveLatestAnnual = cur, true
				}
			}
		}
		if !haveLatest {
			return
		}

		chosen := latest
		if haveLatestAnnual && !latestAnnual.end.Before(latest.end.Add(-annualGracePeriod)) {
			chosen = latestAnnual
		}
		shown := label
		if !chosen.annual {
			shown = label + " (quarterly)"
		}
		td.Facts = append(td.Facts, Fact{
			Label:  shown,
			Value:  fmt.Sprintf("%.2f", chosen.val),
			AsOf:   chosen.end,
			Source: "SEC EDGAR",
			URL:    filingURL,
		})
	}
	extract := func(label string, gaapKeys ...string) {
		extractUnit("us-gaap", "USD", label, gaapKeys...)
	}

	extract("Total Assets", "Assets")
	extract(FactNetIncome, "NetIncomeLoss", "ProfitLoss")
	extract(FactRevenue, revenueTags...)
	extract("Stockholders Equity", "StockholdersEquity")
	// The two inputs a multiple needs. Shares outstanding is a cover-page fact
	// in the `dei` namespace, not a GAAP one, which is why it was never found.
	extractUnit("dei", "shares", FactShares, "EntityCommonStockSharesOutstanding")
	extractUnit("us-gaap", "USD/shares", FactEPSDiluted, "EarningsPerShareDiluted", "EarningsPerShareBasicAndDiluted")

	if g, ok := revenueGrowth(data.Facts["us-gaap"]); ok {
		td.Facts = append(td.Facts, g)
	}

	td.Facts, td.Warnings = dropInconsistentDates(td.Facts)
	return td, nil
}

// Labels the orchestrator keys off when it computes multiples. They are
// constants because a rename here would silently stop every multiple being
// computed rather than fail anything.
const (
	FactRevenue    = "Revenue"
	FactNetIncome  = "Net Income"
	FactShares     = "Shares outstanding"
	FactEPSDiluted = "EPS (diluted)"
	FactRevenueYoY = "Revenue growth YoY"
)

// revenueTags are the GAAP tags a filer may carry revenue under. Filers migrate
// between them mid-history, which is why all of them are read together.
var revenueTags = []string{
	"RevenueFromContractWithCustomerExcludingAssessedTax",
	"RevenueFromContractWithCustomerIncludingAssessedTax",
	"Revenues",
	"SalesRevenueNet",
}

// revenueGrowth computes year-over-year revenue growth from the two freshest
// *annual* frames, and only when they are actually a year apart.
//
// Growth is what makes a multiple mean anything — a P/E of 40 is expensive or
// cheap depending entirely on it — and the fundamentals domain had no growth
// figure at all, so it supplied one from recollection.
func revenueGrowth(gaap map[string]xbrlFact) (Fact, bool) {
	byEnd := map[string]xbrlObs{}
	for _, tag := range revenueTags {
		fact, ok := gaap[tag]
		if !ok {
			continue
		}
		for _, o := range fact.Units["USD"] {
			if o.FP != "FY" {
				continue
			}
			end, err := time.Parse("2006-01-02", o.End)
			if err != nil {
				continue
			}
			// A later tag restating the same period wins; filers migrate tags
			// and the newer one is the one they now report under.
			byEnd[o.End] = xbrlObs{val: o.Val, end: end, annual: true}
		}
	}
	if len(byEnd) < 2 {
		return Fact{}, false
	}
	obs := make([]xbrlObs, 0, len(byEnd))
	for _, o := range byEnd {
		obs = append(obs, o)
	}
	sort.Slice(obs, func(i, j int) bool { return obs[i].end.After(obs[j].end) })

	newest, prior := obs[0], obs[1]
	gap := newest.end.Sub(prior.end)
	// A fiscal year is 12 months give or take a 52/53-week calendar; anything
	// outside this is two frames that are not a year apart, and dividing them
	// produces a growth rate for a period nobody named.
	if gap < 300*24*time.Hour || gap > 430*24*time.Hour || prior.val <= 0 {
		return Fact{}, false
	}
	return Fact{
		Label: FactRevenueYoY,
		Value: fmt.Sprintf("%+.1f%% (FY to %s vs FY to %s)",
			(newest.val/prior.val-1)*100,
			newest.end.Format("2006-01-02"), prior.end.Format("2006-01-02")),
		AsOf:   newest.end,
		Source: "SEC EDGAR",
	}, true
}

// xbrlFact is one XBRL tag's observations, keyed by unit ("USD", "shares",
// "USD/shares").
type xbrlFact struct {
	Units map[string][]struct {
		Val  float64 `json:"val"`
		Accn string  `json:"accn"`
		FY   int     `json:"fy"`
		FP   string  `json:"fp"`
		End  string  `json:"end"`
	} `json:"units"`
}

// xbrlObs is one XBRL observation reduced to what the choice depends on.
type xbrlObs struct {
	val    float64
	end    time.Time
	annual bool
}

const (
	// annualGracePeriod is how stale the latest annual frame may be, relative to
	// the freshest observation of any kind, before a quarterly figure is
	// preferred instead. Fifteen months clears a normal annual reporting gap
	// (12 months plus filing lag) without tolerating a migrated-away tag.
	annualGracePeriod = 15 * 30 * 24 * time.Hour
	// dateSpreadLimit is how far a figure may trail the freshest figure for the
	// same company and still be printed beside it. Figures more than a year
	// apart cannot be divided into a margin, a ratio or a growth rate, and
	// shipping them side by side without saying so is what invites the
	// invention.
	dateSpreadLimit = 370 * 24 * time.Hour
)

// dropInconsistentDates keeps the facts clustered around the freshest one and
// withholds the stragglers, returning the survivors and a note naming what went.
func dropInconsistentDates(facts []Fact) ([]Fact, []string) {
	if len(facts) < 2 {
		return facts, nil
	}
	var newest time.Time
	for _, f := range facts {
		if f.AsOf.After(newest) {
			newest = f.AsOf
		}
	}
	cutoff := newest.Add(-dateSpreadLimit)

	kept := make([]Fact, 0, len(facts))
	var dropped []string
	for _, f := range facts {
		if f.AsOf.Before(cutoff) {
			dropped = append(dropped, fmt.Sprintf("%s (%s)", f.Label, f.AsOf.Format("2006-01-02")))
			continue
		}
		kept = append(kept, f)
	}
	if len(dropped) == 0 {
		return facts, nil
	}
	return kept, []string{fmt.Sprintf(
		"withheld %s: more than a year older than the freshest figure (%s), so no ratio across them is meaningful",
		strings.Join(dropped, ", "), newest.Format("2006-01-02"))}
}

func (p *edgarProvider) MacroFetch(ctx context.Context) ([]Fact, error) {
	return nil, ErrNotApplicable // EDGAR has no macro series
}
