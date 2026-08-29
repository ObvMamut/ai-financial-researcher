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
	p := NewEdgarProvider("cfr@example.com", nil)
	for _, ticker := range []string{"MC.PA", "SAP.DE", "2330.TW", "005930.KS", "7203.T", "ASML.AS", "0700.HK"} {
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

	byLabel := map[string]Fact{}
	for _, f := range td.Facts {
		byLabel[f.Label] = f
	}

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
