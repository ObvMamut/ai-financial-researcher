package marketdata

import (
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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

type edgarProvider struct {
	client       *http.Client
	contactEmail string
	cache        *Cache
	factsBase    string // companyfacts host
	tickersBase  string // ticker directory host (a different SEC host)

	mu        sync.Mutex
	cikMap    map[string]string
	refreshed bool
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
		factsBase:    "https://data.sec.gov",
		tickersBase:  "https://www.sec.gov",
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
func (p *edgarProvider) Domains() []string {
	return []string{"fundamentals"}
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
	if domain != "fundamentals" {
		return TickerData{}, ErrNotApplicable
	}
	if isForeignListing(ticker) {
		return TickerData{}, fmt.Errorf("%w: %s is not a US listing", ErrNotApplicable, ticker)
	}

	p.ensureCIKMap(ctx)
	cik, ok := p.lookupCIK(ticker)
	if !ok {
		return TickerData{}, fmt.Errorf("%w: ticker %s not in SEC CIK map", ErrUnavailable, ticker)
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
		Facts map[string]map[string]struct {
			Units map[string][]struct {
				Val  float64 `json:"val"`
				Accn string  `json:"accn"`
				FY   int     `json:"fy"`
				FP   string  `json:"fp"`
				End  string  `json:"end"`
			} `json:"units"`
		} `json:"facts"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return TickerData{}, err
	}

	td := TickerData{Ticker: ticker}
	filingURL := fmt.Sprintf("https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=%s&type=10-K", cik)

	// extract emits one fact per label, choosing the freshest full-year USD
	// observation across all the GAAP tags that could carry it.
	//
	// Two traps live here. Filers migrate between tags — NVDA's ASC 606 revenue
	// tag stops in FY2022 and continues under another — so taking the first tag
	// that has *any* data pins a figure four years stale. And one tag's slice
	// mixes annual with quarterly frames in no particular order, so a quarterly
	// revenue printed beside an annual net income reads as a company earning
	// four times its sales.
	extract := func(label string, gaapKeys ...string) {
		gaap, ok := data.Facts["us-gaap"]
		if !ok {
			return
		}
		var (
			bestVal    float64
			bestEnd    time.Time
			bestAnnual bool
			found      bool
		)
		for _, key := range gaapKeys {
			fact, ok := gaap[key]
			if !ok {
				continue
			}
			for _, obs := range fact.Units["USD"] {
				end, err := time.Parse("2006-01-02", obs.End)
				if err != nil {
					continue
				}
				annual := obs.FP == "FY"
				// An annual frame always beats a quarterly one; within the same
				// kind, the later period wins.
				better := !found ||
					(annual && !bestAnnual) ||
					(annual == bestAnnual && end.After(bestEnd))
				if better {
					bestVal, bestEnd, bestAnnual, found = obs.Val, end, annual, true
				}
			}
		}
		if !found {
			return
		}
		td.Facts = append(td.Facts, Fact{
			Label:  label,
			Value:  fmt.Sprintf("%.2f", bestVal),
			AsOf:   bestEnd,
			Source: "SEC EDGAR",
			URL:    filingURL,
		})
	}

	extract("Total Assets", "Assets")
	extract("Net Income", "NetIncomeLoss", "ProfitLoss")
	extract("Revenue",
		"RevenueFromContractWithCustomerExcludingAssessedTax",
		"RevenueFromContractWithCustomerIncludingAssessedTax",
		"Revenues",
		"SalesRevenueNet")
	extract("Stockholders Equity", "StockholdersEquity")

	return td, nil
}

func (p *edgarProvider) MacroFetch(ctx context.Context) ([]Fact, error) {
	return nil, ErrNotApplicable // EDGAR has no macro series
}
