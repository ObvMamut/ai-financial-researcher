package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// secServer stands in for both SEC hosts: the ticker directory and companyfacts.
func secServer(t *testing.T, directory map[string]secTickerEntry) (*httptest.Server, *int32) {
	t.Helper()
	var dirHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" || !strings.Contains(r.Header.Get("User-Agent"), "@") {
			// SEC rejects clients that do not identify themselves.
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch {
		case r.URL.Path == secTickersPath:
			atomic.AddInt32(&dirHits, 1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(directory)
		case strings.HasPrefix(r.URL.Path, "/api/xbrl/companyfacts/"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"facts":{"us-gaap":{
				"Assets":{"units":{"USD":[
					{"val":100,"fp":"FY","end":"2024-12-31"},
					{"val":999,"fp":"Q2","end":"2026-06-30"},
					{"val":400,"fp":"FY","end":"2025-12-31"},
					{"val":200,"fp":"FY","end":"2023-12-31"}]}},
				"RevenueFromContractWithCustomerExcludingAssessedTax":{"units":{"USD":[
					{"val":50,"fp":"FY","end":"2021-12-31"}]}},
				"Revenues":{"units":{"USD":[
					{"val":700,"fp":"FY","end":"2025-12-31"}]}},
				"NetIncomeLoss":{"units":{"USD":[{"val":9,"fp":"Q1","end":"2026-03-31"}]}}
			}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &dirHits
}

// The embedded map is a 160-row stub: AMD, SNOW and MELI are all SEC filers and
// all absent from it, which is why fundamentals covered 3 of 12 names.
func TestEdgarUsesFullSECDirectory(t *testing.T) {
	srv, hits := secServer(t, map[string]secTickerEntry{
		"0": {CIK: "1730168", Ticker: "AMD", Title: "Advanced Micro Devices"},
	})
	t.Setenv("CFR_SEC_BASE", srv.URL)

	p := NewEdgarProvider("cfr@example.com", nil)
	if _, ok := p.(*edgarProvider).lookupCIK("AMD"); ok {
		t.Fatal("setup: AMD should be missing from the embedded stub")
	}

	td, err := p.Fetch(context.Background(), "fundamentals", "AMD")
	if err != nil {
		t.Fatalf("Fetch AMD: %v", err)
	}
	if len(td.Facts) == 0 {
		t.Fatal("AMD returned no facts")
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Errorf("ticker directory fetched %d times, want 1 per process", got)
	}

	// A second lookup must reuse the directory rather than re-fetching it.
	if _, err := p.Fetch(context.Background(), "fundamentals", "AMD"); err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Errorf("ticker directory re-fetched (%d hits) — it should be loaded once", got)
	}
}

func TestEdgarCachesDirectoryAcrossProviders(t *testing.T) {
	srv, hits := secServer(t, map[string]secTickerEntry{
		"0": {CIK: "1730168", Ticker: "AMD"},
	})
	t.Setenv("CFR_SEC_BASE", srv.URL)
	cache := NewCache(t.TempDir())

	first := NewEdgarProvider("cfr@example.com", cache)
	if _, err := first.Fetch(context.Background(), "fundamentals", "AMD"); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}

	// A fresh process the same day should read the map off disk.
	second := NewEdgarProvider("cfr@example.com", cache)
	if _, err := second.Fetch(context.Background(), "fundamentals", "AMD"); err != nil {
		t.Fatalf("second provider Fetch: %v", err)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Errorf("directory fetched %d times; the daily cache should cover the second run", got)
	}
}

// The embedded stub has to keep working when SEC is unreachable, or a network
// blip costs the run every fundamental it would otherwise have had.
func TestEdgarFallsBackToEmbeddedStub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_SEC_BASE", srv.URL)

	p := NewEdgarProvider("cfr@example.com", nil).(*edgarProvider)
	p.ensureCIKMap(context.Background())
	if _, ok := p.lookupCIK("AAPL"); !ok {
		t.Error("AAPL is in the embedded stub and must survive an SEC outage")
	}
}

// Reporting "not in CIK map" once per European and Asian name was 8 of the 11
// fundamentals errors in the baseline run, and told the reader nothing.
func TestEdgarSkipsForeignListingsQuietly(t *testing.T) {
	// Names with no US line. SAP.DE, 2330.TW, 7203.T and ASML.AS are absent
	// deliberately: they resolve to a US symbol and are looked up under it
	// (LIN.DE is the one that yields real us-gaap facts; the IFRS filers
	// resolve but report nothing, which is honest).
	p := NewEdgarProvider("cfr@example.com", nil)
	for _, ticker := range []string{"MC.PA", "005930.KS", "0700.HK", "2317.TW", "PTT.BK"} {
		_, err := p.Fetch(context.Background(), "fundamentals", ticker)
		if err == nil {
			t.Errorf("%s: expected a skip, got data", ticker)
			continue
		}
		if !isNotApplicable(err) {
			t.Errorf("%s: %v — a foreign listing is not a lookup failure", ticker, err)
		}
	}
}

// US share classes spell out with a dot in our universe files and a dash at SEC.
func TestEdgarNormalisesShareClasses(t *testing.T) {
	srv, _ := secServer(t, map[string]secTickerEntry{
		"0": {CIK: "1067983", Ticker: "BRK-B", Title: "Berkshire Hathaway"},
	})
	t.Setenv("CFR_SEC_BASE", srv.URL)

	p := NewEdgarProvider("cfr@example.com", nil)
	if isForeignListing("BRK.B") {
		t.Fatal("BRK.B is a US share class, not a foreign listing")
	}
	td, err := p.Fetch(context.Background(), "fundamentals", "BRK.B")
	if err != nil {
		t.Fatalf("Fetch BRK.B: %v", err)
	}
	if len(td.Facts) == 0 {
		t.Error("BRK.B returned no facts")
	}
}

// The pack prints an "as of" date next to every figure, so it has to be the
// date of the value we actually took.
func TestEdgarPicksLatestObservationAndModernRevenueTag(t *testing.T) {
	srv, _ := secServer(t, map[string]secTickerEntry{
		"0": {CIK: "320193", Ticker: "AAPL"},
	})
	t.Setenv("CFR_SEC_BASE", srv.URL)

	td, err := NewEdgarProvider("cfr@example.com", nil).
		Fetch(context.Background(), "fundamentals", "AAPL")
	if err != nil {
		t.Fatal(err)
	}

	byLabel := factsByLabel(td)

	assets, ok := byLabel["Total Assets"]
	if !ok {
		t.Fatal("no Total Assets fact")
	}
	// The latest observation overall is a Q2 frame; the latest *annual* one is
	// 2025-12-31. Mixing frames prints a quarterly revenue beside an annual net
	// income, which reads as a company earning four times its sales.
	if assets.Value != "400.00" {
		t.Errorf("Total Assets = %s, want the latest FY observation (400.00)", assets.Value)
	}
	if got := assets.AsOf.Format("2006-01-02"); got != "2025-12-31" {
		t.Errorf("Total Assets as-of = %s, want 2025-12-31", got)
	}

	// A tag with no annual frame at all still reports its latest figure rather
	// than dropping out of the pack entirely.
	if ni, ok := byLabel["Net Income"]; !ok {
		t.Error("no Net Income fact — a quarterly-only tag should still be reported")
	} else if ni.Value != "9.00" {
		t.Errorf("Net Income = %s, want the quarterly fallback (9.00)", ni.Value)
	}
	if assets.URL == "" {
		t.Error("a filing fact needs a link for the agent to cite")
	}

	// Filers migrate between revenue tags mid-history: NVDA's ASC 606 tag stops
	// in FY2022 and continues elsewhere. Taking the first tag that has any data
	// pinned a figure four years stale, so the freshest annual figure across
	// every candidate tag wins.
	rev, ok := byLabel["Revenue"]
	if !ok {
		t.Fatal("no Revenue fact")
	}
	if rev.Value != "700.00" {
		t.Errorf("Revenue = %s, want the freshest annual figure (700.00), not the stale ASC 606 tag", rev.Value)
	}
	if got := rev.AsOf.Format("2006-01-02"); got != "2025-12-31" {
		t.Errorf("Revenue as-of = %s, want 2025-12-31", got)
	}
}

func isNotApplicable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not covered by this provider")
}

// nvdaLikeServer reproduces the shape that drove the rank-1 idea on
// 2026-08-29: the modern revenue tag stops in FY2022 while net income carries a
// current annual frame, so "annual always beats quarterly" pinned revenue four
// years stale beside a fresh net income. The specialist noticed the staleness,
// divided anyway, and reported a "37% net margin" whose actual quotient is 438%.
func nvdaLikeServer(t *testing.T, facts string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == secTickersPath:
			json.NewEncoder(w).Encode(map[string]secTickerEntry{
				"0": {CIK: "1045810", Ticker: "NVDA", Title: "NVIDIA"},
			})
		case strings.HasPrefix(r.URL.Path, "/api/xbrl/companyfacts/"):
			fmt.Fprint(w, facts)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// factsByLabel keys on the base label; the frame suffix EDGAR appends
// ("Revenue (quarterly)") stays on the Fact for assertions about it.
func factsByLabel(td TickerData) map[string]Fact {
	m := map[string]Fact{}
	for _, f := range td.Facts {
		m[strings.TrimSuffix(f.Label, " (quarterly)")] = f
	}
	return m
}

// A stale annual frame must lose to a current quarterly one. Four years of
// staleness is not worth the tidiness of a matching period.
func TestEdgarPrefersFreshQuarterlyOverStaleAnnual(t *testing.T) {
	srv := nvdaLikeServer(t, `{"facts":{"us-gaap":{
		"NetIncomeLoss":{"units":{"USD":[{"val":118000,"fp":"FY","end":"2026-01-25"}]}},
		"RevenueFromContractWithCustomerExcludingAssessedTax":{"units":{"USD":[
			{"val":26900,"fp":"FY","end":"2022-01-30"},
			{"val":130500,"fp":"Q3","end":"2025-10-26"}]}}
	}}}`)
	t.Setenv("CFR_SEC_BASE", srv.URL)

	td, err := NewEdgarProvider("cfr@example.com", nil).
		Fetch(context.Background(), "fundamentals", "NVDA")
	if err != nil {
		t.Fatal(err)
	}
	by := factsByLabel(td)

	rev, ok := by["Revenue"]
	if !ok {
		t.Fatal("no Revenue fact")
	}
	if got := rev.AsOf.Format("2006-01-02"); got != "2025-10-26" {
		t.Errorf("Revenue as-of = %s, want the current quarterly frame 2025-10-26 "+
			"— a FY2022 figure beside a FY2026 net income is the mixed-date bug", got)
	}
	if !strings.Contains(rev.Label, "quarterly") {
		t.Errorf("Revenue label = %q, want it to say the frame is quarterly", rev.Label)
	}
}

// A recent annual frame still wins: the point is freshness, not frame type.
func TestEdgarKeepsAnnualWhenItIsRecent(t *testing.T) {
	srv := nvdaLikeServer(t, `{"facts":{"us-gaap":{
		"Assets":{"units":{"USD":[
			{"val":100,"fp":"FY","end":"2026-01-25"},
			{"val":110,"fp":"Q1","end":"2026-04-26"}]}}
	}}}`)
	t.Setenv("CFR_SEC_BASE", srv.URL)

	td, err := NewEdgarProvider("cfr@example.com", nil).
		Fetch(context.Background(), "fundamentals", "NVDA")
	if err != nil {
		t.Fatal(err)
	}
	assets := factsByLabel(td)["Total Assets"]
	if assets.Value != "100.00" {
		t.Errorf("Total Assets = %s, want the annual frame (100.00) — it is only a quarter old", assets.Value)
	}
	if strings.Contains(assets.Label, "quarterly") {
		t.Errorf("Total Assets label = %q, an annual frame must not be labelled quarterly", assets.Label)
	}
}

// Figures more than a year apart cannot be divided into a margin or a ratio.
// Shipping them side by side without saying so is what invited the invention.
func TestEdgarDropsFactsInconsistentWithTheFreshest(t *testing.T) {
	srv := nvdaLikeServer(t, `{"facts":{"us-gaap":{
		"NetIncomeLoss":{"units":{"USD":[{"val":118000,"fp":"FY","end":"2026-01-25"}]}},
		"Assets":{"units":{"USD":[{"val":111600,"fp":"FY","end":"2026-01-25"}]}},
		"RevenueFromContractWithCustomerExcludingAssessedTax":{"units":{"USD":[
			{"val":26900,"fp":"FY","end":"2022-01-30"}]}}
	}}}`)
	t.Setenv("CFR_SEC_BASE", srv.URL)

	td, err := NewEdgarProvider("cfr@example.com", nil).
		Fetch(context.Background(), "fundamentals", "NVDA")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := factsByLabel(td)["Revenue"]; ok {
		t.Error("a FY2022 revenue must not ship beside FY2026 net income — it reads as a 438% margin")
	}
	if len(td.Facts) != 2 {
		t.Errorf("kept %d facts, want the two consistent ones", len(td.Facts))
	}
	if len(td.Warnings) == 0 {
		t.Fatal("dropping a stale figure must be recorded, not silent")
	}
	if !strings.Contains(strings.Join(td.Warnings, " "), "Revenue") {
		t.Errorf("warnings = %v, want the dropped label named", td.Warnings)
	}
}

// The pack carries the provider's warnings into the run's error list, so a
// dropped figure shows up in metadata.json rather than only in a log line.
func TestBuildPackSurfacesTickerWarnings(t *testing.T) {
	srv := nvdaLikeServer(t, `{"facts":{"us-gaap":{
		"NetIncomeLoss":{"units":{"USD":[{"val":118000,"fp":"FY","end":"2026-01-25"}]}},
		"Assets":{"units":{"USD":[{"val":111600,"fp":"FY","end":"2026-01-25"}]}},
		"RevenueFromContractWithCustomerExcludingAssessedTax":{"units":{"USD":[
			{"val":26900,"fp":"FY","end":"2022-01-30"}]}}
	}}}`)
	t.Setenv("CFR_SEC_BASE", srv.URL)

	pack := NewService(nil, NewEdgarProvider("cfr@example.com", nil)).
		BuildPack(context.Background(), "fundamentals", []string{"NVDA"})
	if !strings.Contains(strings.Join(pack.Errors, " "), "Revenue") {
		t.Errorf("pack.Errors = %v, want the dropped stale figure named", pack.Errors)
	}
}

// multiplesServer carries the three facts a multiple actually needs: a share
// count (a cover-page `dei` fact, not a GAAP one), diluted EPS (quoted in
// USD/shares, not USD), and two consecutive annual revenue frames.
func multiplesServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == secTickersPath:
			json.NewEncoder(w).Encode(map[string]secTickerEntry{"0": {CIK: "320193", Ticker: "AAPL"}})
		case strings.HasPrefix(r.URL.Path, "/api/xbrl/companyfacts/"):
			fmt.Fprint(w, `{"facts":{
				"dei":{"EntityCommonStockSharesOutstanding":{"units":{"shares":[
					{"val":15000000000,"fp":"FY","end":"2025-12-31"}]}}},
				"us-gaap":{
					"EarningsPerShareDiluted":{"units":{"USD/shares":[
						{"val":6.50,"fp":"FY","end":"2025-12-31"},
						{"val":5.90,"fp":"FY","end":"2024-12-31"}]}},
					"Revenues":{"units":{"USD":[
						{"val":400000000000,"fp":"FY","end":"2025-12-31"},
						{"val":100,"fp":"Q1","end":"2026-03-31"},
						{"val":350000000000,"fp":"FY","end":"2024-12-31"},
						{"val":300000000000,"fp":"FY","end":"2023-12-31"}]}},
					"NetIncomeLoss":{"units":{"USD":[{"val":99000000000,"fp":"FY","end":"2025-12-31"}]}}
				}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEdgarExtractsTheInputsAMultipleNeeds(t *testing.T) {
	t.Setenv("CFR_SEC_BASE", multiplesServer(t).URL)
	td, err := NewEdgarProvider("cfr@example.com", nil).
		Fetch(context.Background(), "fundamentals", "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	byLabel := factsByLabel(td)

	// Shares outstanding is a `dei` cover-page fact under a "shares" unit, so a
	// us-gaap/USD-only extractor could never find it — and without it no market
	// cap, and therefore no multiple, is computable from SEC data at all.
	if f, ok := byLabel[FactShares]; !ok {
		t.Errorf("no share count; facts = %v", byLabel)
	} else if f.Value != "15000000000.00" {
		t.Errorf("shares outstanding = %s", f.Value)
	}
	// EPS is quoted in USD/shares, not USD.
	if f, ok := byLabel[FactEPSDiluted]; !ok {
		t.Errorf("no diluted EPS; facts = %v", byLabel)
	} else if f.Value != "6.50" {
		t.Errorf("diluted EPS = %s, want the freshest annual (6.50)", f.Value)
	}
	// 400B vs 350B = +14.3%, from the two freshest *annual* frames — the Q1
	// figure sitting between them is not a year of revenue.
	if f, ok := byLabel[FactRevenueYoY]; !ok {
		t.Errorf("no revenue growth; facts = %v", byLabel)
	} else if !strings.Contains(f.Value, "+14.3%") {
		t.Errorf("revenue growth = %s, want +14.3%%", f.Value)
	}
}

func TestEdgarWithholdsGrowthAcrossANonAnnualGap(t *testing.T) {
	// The default fixture's two annual revenue frames are four years apart.
	// Dividing them yields a growth rate for a period nobody named.
	srv, _ := secServer(t, map[string]secTickerEntry{"0": {CIK: "320193", Ticker: "AAPL"}})
	t.Setenv("CFR_SEC_BASE", srv.URL)
	td, err := NewEdgarProvider("cfr@example.com", nil).
		Fetch(context.Background(), "fundamentals", "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := factsByLabel(td)[FactRevenueYoY]; ok {
		t.Error("frames four years apart must not produce a YoY growth rate")
	}
}
