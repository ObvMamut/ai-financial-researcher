package marketdata

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func optionsJSON(expiry int64, expiries []int64, spot float64, calls, puts string) string {
	list := make([]string, 0, len(expiries))
	for _, e := range expiries {
		list = append(list, fmt.Sprintf("%d", e))
	}
	return fmt.Sprintf(`{"optionChain":{"result":[{
		"underlyingSymbol":"NVDA",
		"expirationDates":[%s],
		"quote":{"regularMarketPrice":%.2f},
		"options":[{"expirationDate":%d,"calls":[%s],"puts":[%s]}]
	}],"error":null}}`, strings.Join(list, ","), spot, expiry, calls, puts)
}

func leg(strike, oi, iv float64) string {
	return fmt.Sprintf(`{"strike":%.2f,"openInterest":%.0f,"impliedVolatility":%.4f}`, strike, oi, iv)
}

func TestOptionsReadsPositioningAcrossTheFrontTwoExpiries(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RawQuery)
		if r.URL.Query().Get("date") == "2000" {
			// Second expiry: 300 puts, 100 calls.
			fmt.Fprint(w, optionsJSON(2000, []int64{1000, 2000}, 178.40,
				leg(180, 100, 0.40), leg(180, 300, 0.44)))
			return
		}
		fmt.Fprint(w, optionsJSON(1000, []int64{1000, 2000}, 178.40,
			leg(175, 500, 0.38)+","+leg(180, 500, 0.42),
			leg(175, 700, 0.46)+","+leg(180, 300, 0.44)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)

	td, err := NewYahooOptionsProvider().Fetch(context.Background(), "sentiment", "NVDA")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(asked) != 2 {
		t.Fatalf("asked for %d chains (%v), want the front two expiries", len(asked), asked)
	}

	joined := fmt.Sprintf("%+v", td.Facts)
	// puts 700+300+300 = 1300; calls 500+500+100 = 1100; ratio 1.18.
	if !strings.Contains(joined, "put/call open interest 1.18") {
		t.Errorf("open-interest ratio wrong:\n%s", joined)
	}
	// Spot 178.40: nearest strike is 180 on both sides, IV (0.42+0.44)/2 = 43%.
	if !strings.Contains(joined, "43.0%") {
		t.Errorf("ATM implied vol wrong — it must average both sides at the nearest strike:\n%s", joined)
	}
	for _, f := range td.Facts {
		if f.URL == "" {
			t.Errorf("fact %q carries no link", f.Label)
		}
	}
}

func TestOptionsDegradesGracefullyWhenGated(t *testing.T) {
	// Yahoo's options endpoint is intermittently crumb-gated. A 401 must be an
	// unavailable provider, not a run-level failure: sentiment falls back to
	// insider filings and everything else proceeds.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"finance":{"error":{"code":"Unauthorized","description":"Invalid Cookie"}}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)

	_, err := NewYahooOptionsProvider().Fetch(context.Background(), "sentiment", "NVDA")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("gated endpoint should report ErrUnavailable, got %v", err)
	}
	if errors.Is(err, ErrNotApplicable) {
		t.Error("a gated endpoint is a failure to record, not a name out of scope")
	}
}

func TestOptionsSkipsListingsWithNoUSChain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, optionsJSON(1000, []int64{1000}, 100, leg(100, 1, 0.3), leg(100, 1, 0.3)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)

	_, err := NewYahooOptionsProvider().Fetch(context.Background(), "sentiment", "MC.PA")
	if !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("a Paris listing has no US chain; want ErrNotApplicable, got %v", err)
	}
}

// legWithVolume is the same leg shape with the traded-volume key present. The
// bare leg() above deliberately omits it, because that is the exact shape the
// warning below exists to catch.
func legWithVolume(strike, oi, iv, vol float64) string {
	return fmt.Sprintf(`{"strike":%.2f,"openInterest":%.0f,"impliedVolatility":%.4f,"volume":%.0f}`,
		strike, oi, iv, vol)
}

func TestOptionsWarnsWhenNoStrikeCarriesTradedVolume(t *testing.T) {
	// Open interest and volume arrive in the same object. A chain that parsed
	// strikes and open interest but reported volume on none of them is the
	// signature of a renamed or dropped field, not of a quiet name — and
	// without this warning the whole flow leg vanishes from every ticker with
	// no error, no data_error and no log line.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, optionsJSON(1000, []int64{1000}, 178.40,
			leg(175, 500, 0.38)+","+leg(180, 500, 0.42),
			leg(175, 700, 0.46)+","+leg(180, 300, 0.44)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)

	td, err := NewYahooOptionsProvider().Fetch(context.Background(), "sentiment", "NVDA")
	if err != nil {
		t.Fatalf("Fetch: %v — a missing field must not discard the whole chain", err)
	}
	joinedWarn := strings.Join(td.Warnings, " | ")
	if joinedWarn == "" {
		t.Fatalf("no warning: the flow leg disappeared silently")
	}
	if !strings.Contains(joinedWarn, "volume") {
		t.Errorf("the warning does not name the volume field: %s", joinedWarn)
	}
	if !strings.Contains(joinedWarn, "open interest") {
		t.Errorf("the warning does not say open interest arrived, which is what makes it a schema signal: %s", joinedWarn)
	}

	// The open-interest positioning leg is unaffected and must still land.
	joined := fmt.Sprintf("%+v", td.Facts)
	if !strings.Contains(joined, "put/call open interest") {
		t.Errorf("the open-interest fact was lost:\n%s", joined)
	}
	// And the flow leg still correctly emits nothing — the absence is right,
	// only the silence was wrong.
	for _, f := range td.Facts {
		if f.Label == UnusualOptionsLabel || f.Label == OptionsFlowSignalLabel {
			t.Errorf("flow fact %q emitted with no volume behind it: %+v", f.Label, f)
		}
	}
}

func TestOptionsDoesNotWarnWhenTheChainIsMerelyQuiet(t *testing.T) {
	// Real volume, just not much of it: classifyUnusualOptions abstains and
	// that is a plain abstention, not a schema problem. No warning.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, optionsJSON(1000, []int64{1000}, 178.40,
			legWithVolume(175, 500, 0.38, 40)+","+legWithVolume(180, 500, 0.42, 12),
			legWithVolume(175, 700, 0.46, 25)+","+legWithVolume(180, 300, 0.44, 8)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)

	td, err := NewYahooOptionsProvider().Fetch(context.Background(), "sentiment", "NVDA")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(td.Warnings) != 0 {
		t.Errorf("a quiet chain must stay a plain abstention, got warnings: %v", td.Warnings)
	}
}

func TestOptionsWithholdsAnImplausibleImpliedVolatility(t *testing.T) {
	// Yahoo publishes a placeholder rather than an absence on a contract it has
	// no quote for. atmIV accepted anything above zero, so on 2026-09-03 six of
	// seven names carried "0.1%–0.8% annualized" into the sentiment prompt, and
	// the agent scored the run's strongest verdict on an option that looked free.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, optionsJSON(1000, []int64{1000}, 90.05,
			legWithVolume(90, 4000, 0.00001, 13424),
			legWithVolume(90, 4000, 0.002, 4835)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)

	td, err := NewYahooOptionsProvider().Fetch(context.Background(), "sentiment", "INTC")
	if err != nil {
		t.Fatalf("Fetch: %v — a broken quote must not discard the whole chain", err)
	}
	for _, f := range td.Facts {
		if strings.HasPrefix(f.Label, "Implied volatility") {
			t.Errorf("a %v implied volatility was written as a fact: %s", f.Value, f.Label)
		}
	}
	joined := strings.Join(td.Warnings, " | ")
	if !strings.Contains(joined, "implied volatility withheld") {
		t.Errorf("no warning naming the withheld quote: %q", joined)
	}
	// The rest of the chain is unaffected — this is one leg, not the provider.
	if len(td.Facts) == 0 {
		t.Error("withholding the IV fact took the whole chain with it")
	}
}

func TestOptionsKeepsAPlausibleImpliedVolatility(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, optionsJSON(1000, []int64{1000}, 178.40,
			legWithVolume(180, 3000, 0.42, 900),
			legWithVolume(180, 3000, 0.44, 900)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)

	td, err := NewYahooOptionsProvider().Fetch(context.Background(), "sentiment", "NVDA")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(fmt.Sprintf("%+v", td.Facts), "43.0% annualized") {
		t.Errorf("a 43%% ATM IV was not written:\n%+v", td.Facts)
	}
}

func TestOptionsWarnsWhenNoStrikeCarriesOpenInterest(t *testing.T) {
	// The mirror of TestOptionsWarnsWhenNoStrikeCarriesTradedVolume, and the one
	// that actually happened. That warning is guarded on open interest being
	// present, so it could never fire here: on 2026-09-03 INTC traded 90,677
	// call contracts against no reported standing position at all, both
	// open-interest legs went dark, and nothing said so.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, optionsJSON(1000, []int64{1000}, 90.05,
			legWithVolume(90, 0, 0.42, 13424),
			legWithVolume(88, 0, 0.44, 4835)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)

	td, err := NewYahooOptionsProvider().Fetch(context.Background(), "sentiment", "INTC")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	joined := strings.Join(td.Warnings, " | ")
	if !strings.Contains(joined, "openInterest") {
		t.Errorf("the warning does not name the openInterest field: %q", joined)
	}
	if strings.Contains(fmt.Sprintf("%+v", td.Facts), "put/call open interest") {
		t.Error("a put/call ratio was computed with no open interest to divide")
	}
}

func TestOptionsRefusesAPutCallRatioOnAThinBook(t *testing.T) {
	// BBVA's ADR line shipped "put/call open interest 8.14 (676 puts vs 83
	// calls)" — 759 contracts, where one order moves the ratio further than the
	// signal it carries. The volume leg has always had this floor.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, optionsJSON(1000, []int64{1000}, 29.16,
			legWithVolume(30, 83, 0.35, 10),
			legWithVolume(30, 676, 0.36, 15)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)

	td, err := NewYahooOptionsProvider().Fetch(context.Background(), "sentiment", "BBVA")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if strings.Contains(fmt.Sprintf("%+v", td.Facts), "put/call open interest") {
		t.Errorf("759 contracts produced a positioning ratio:\n%+v", td.Facts)
	}
}

// optionsJSONInState is optionsJSON with the quote's marketState set, which is
// how Yahoo says which session the chain belongs to.
func optionsJSONInState(state string, expiry int64, spot float64, calls, puts string) string {
	return strings.Replace(optionsJSON(expiry, []int64{expiry}, spot, calls, puts),
		`"quote":{`, fmt.Sprintf(`"quote":{"marketState":%q,`, state), 1)
}

func TestOptionsAbstainsQuietlyOnAPreOpenChain(t *testing.T) {
	// Every weekday run started before the US open since the IV check existed
	// (2026-09-04 06:11Z, 09-10 06:49Z, eight runs on 09-24 before 13:30Z,
	// 10-07 05:13Z) withheld placeholder IVs, and 10-07 also lost open interest
	// on six names. Runs inside the session and on weekends did not. That is
	// the chain not yet being republished for the day, not a renamed field, and
	// it is an expected gap rather than a data error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, optionsJSONInState("PREPRE", 1000, 72.56,
			legWithVolume(73, 0, 0.0078, 13424),
			legWithVolume(73, 0, 0.0078, 4835)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)

	td, err := NewYahooOptionsProvider().Fetch(context.Background(), "sentiment", "FCX")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(td.Warnings) != 0 {
		t.Errorf("a pre-open chain was reported as a data failure: %q", td.Warnings)
	}
	for _, f := range td.Facts {
		if strings.HasPrefix(f.Label, "Implied volatility") || strings.Contains(f.Value, "put/call open interest") {
			t.Errorf("a pre-open placeholder was written as a fact: %s = %s", f.Label, f.Value)
		}
	}
	if len(td.Diagnostics) != 1 || td.Diagnostics[0].Disposition != "expected" || td.Diagnostics[0].Reason != "off_session" {
		t.Fatalf("want one expected off_session diagnostic, got %+v", td.Diagnostics)
	}
	if !strings.Contains(td.Diagnostics[0].Message, "before the US session") {
		t.Errorf("the diagnostic does not say why: %q", td.Diagnostics[0].Message)
	}
}

func TestOptionsStillWarnsOnABlindChainInsideTheSession(t *testing.T) {
	// 2026-09-04 lost open interest at 14:26Z and 18:55Z, inside the session.
	// That one really is unexplained, and it keeps its warning.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, optionsJSONInState("REGULAR", 1000, 72.56,
			legWithVolume(73, 0, 0.0078, 13424),
			legWithVolume(73, 0, 0.0078, 4835)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)

	td, err := NewYahooOptionsProvider().Fetch(context.Background(), "sentiment", "FCX")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	joined := strings.Join(td.Warnings, " | ")
	if !strings.Contains(joined, "openInterest") || !strings.Contains(joined, "implied volatility withheld") {
		t.Errorf("an in-session blind chain lost its warnings: %q", joined)
	}
	for _, d := range td.Diagnostics {
		if d.Reason == "off_session" {
			t.Errorf("an in-session chain was excused as off-session: %+v", d)
		}
	}
}
