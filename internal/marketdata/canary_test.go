package marketdata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeYahoo serves the three Yahoo endpoints the canary reads. options and
// search are the bodies for the option chain and for a name search; a search
// for a local symbol always comes back empty, as it did on 2026-10-07.
func fakeYahoo(t *testing.T, options, search string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/finance/chart/"):
			fmt.Fprint(w, chartJSON(6))
		case strings.Contains(r.URL.Path, "/finance/options/"):
			fmt.Fprint(w, options)
		case strings.Contains(r.URL.Path, "/finance/search"):
			if strings.Contains(r.URL.Query().Get("q"), ".") {
				fmt.Fprint(w, `{"news":[]}`)
				return
			}
			fmt.Fprint(w, search)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)
}

func byName(results []CanaryResult) map[string]CanaryResult {
	out := map[string]CanaryResult{}
	for _, r := range results {
		out[r.Name] = r
	}
	return out
}

// chartJSON's newest bar is 2025-07-14.
var canaryNow = time.Date(2025, 7, 15, 15, 0, 0, 0, time.UTC)

// The option-chain and name-search bodies of a healthy Yahoo.
var (
	healthyOptions = optionsJSONInState("REGULAR", 1000, 178.40, legWithVolume(180, 3000, 0.42, 900), legWithVolume(180, 3000, 0.44, 900))
	healthySearch  = fmt.Sprintf(`{"news":[%s]}`, newsItem("Tencent lifts its buyback", "Reuters", 3, "0700.HK"))
)

func TestCanaryPassesOnHealthySources(t *testing.T) {
	fakeYahoo(t, healthyOptions, healthySearch)
	res := RunCanary(context.Background(), CanaryConfig{Cache: NewCache(t.TempDir()), Now: canaryNow})
	got := byName(res)

	for _, name := range []string{"yahoo-chart AAPL", "yahoo-chart SAP.DE", "yahoo-options AAPL", "yahoo-news 0700.HK by name"} {
		if got[name].Status != CanaryPass {
			t.Errorf("%s = %+v, want pass", name, got[name])
		}
	}
	// Unconfigured sources are skipped, not failed.
	for _, name := range []string{"alpaca-bars AAPL", "alpaca-news AAPL", "sec-submissions AAPL"} {
		if got[name].Status != CanarySkip {
			t.Errorf("%s = %+v, want skip without credentials", name, got[name])
		}
	}
	if !CanaryHealthy(res) {
		t.Error("a run with only passes and skips was reported unhealthy")
	}
}

func TestCanaryFailsOnTheSilentFailures(t *testing.T) {
	// In session, a chain with volume and no open interest; and a name search
	// whose only item is a market wrap that never names Tencent.
	fakeYahoo(t,
		optionsJSONInState("REGULAR", 1000, 72.56, legWithVolume(73, 0, 0.0078, 13424), legWithVolume(73, 0, 0.0078, 4835)),
		fmt.Sprintf(`{"news":[%s]}`, newsItem("Asian equities traded in the US edge lower", "MT Newswires", 3, "0700.HK", "BABA", "JD", "PDD")))
	res := RunCanary(context.Background(), CanaryConfig{Cache: NewCache(t.TempDir()), Now: canaryNow.AddDate(0, 0, 10)})
	got := byName(res)

	if r := got["yahoo-chart AAPL"]; r.Status != CanaryFail || !strings.Contains(r.Detail, "stale") {
		t.Errorf("a ten-day-old chart = %+v, want a stale failure", r)
	}
	if r := got["yahoo-options AAPL"]; r.Status != CanaryFail || !strings.Contains(r.Detail, "openInterest") {
		t.Errorf("a blind in-session chain = %+v, want a failure naming openInterest", r)
	}
	if r := got["yahoo-news 0700.HK by name"]; r.Status != CanaryFail {
		t.Errorf("a name search with no headline about the company = %+v, want fail", r)
	}
	if CanaryHealthy(res) {
		t.Error("a run with failures was reported healthy")
	}
}

func TestCanaryTreatsAPreOpenChainAsExpected(t *testing.T) {
	fakeYahoo(t,
		optionsJSONInState("PREPRE", 1000, 72.56, legWithVolume(73, 0, 0.0078, 13424), legWithVolume(73, 0, 0.0078, 4835)),
		`{"news":[]}`)
	r := probeOptions(context.Background())
	if r.Status != CanaryPass || !strings.Contains(r.Detail, "before the US session") {
		t.Errorf("a pre-open chain = %+v, want pass with the reason", r)
	}
}

// An Alpaca answer with no bars fails the probe rather than panicking it.
// AlpacaPrices.history turns that answer into an error today; the probe's own
// guard keeps it a failure if that contract ever loosens (removing the check in
// history made this test panic at the probe's s.Bars index).
func TestCanaryFailsOnAnEmptyAlpacaAnswer(t *testing.T) {
	fakeYahoo(t, healthyOptions, healthySearch)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"bars":{},"next_page_token":null}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_ALPACA_BASE", srv.URL)
	res := RunCanary(context.Background(), CanaryConfig{AlpacaKeyID: "k", AlpacaSecret: "s", Cache: NewCache(t.TempDir()), Now: canaryNow})
	if r := byName(res)["alpaca-bars AAPL"]; r.Status != CanaryFail || !strings.Contains(r.Detail, "no bars") {
		t.Errorf("an empty Alpaca answer = %+v, want a failure saying no bars", r)
	}
}
