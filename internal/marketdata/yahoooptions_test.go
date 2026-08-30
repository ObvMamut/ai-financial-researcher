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
