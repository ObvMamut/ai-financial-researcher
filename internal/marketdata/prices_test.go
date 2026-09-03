package marketdata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// routedTest stands up both back ends and reports which one each symbol
// reached. The Yahoo side serves the chart fixture shape; the Alpaca side
// serves the bars shape.
type routedTest struct {
	prices     *RoutedPrices
	yahooAsked *[]string
	alpacaAsk  *[]string
}

func newRoutedTest(t *testing.T, alpacaConfigured bool, alpacaFails bool) routedTest {
	t.Helper()
	var yahooAsked, alpacaAsked []string

	ysrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sym := strings.TrimPrefix(r.URL.Path, "/v8/finance/chart/")
		yahooAsked = append(yahooAsked, sym)
		fmt.Fprint(w, `{"chart":{"result":[{"timestamp":[1756684800],`+
			`"indicators":{"quote":[{"open":[10],"high":[11],"low":[9],"close":[10],"volume":[1000]}],`+
			`"adjclose":[{"adjclose":[10]}]}}],"error":null}}`)
	}))
	t.Cleanup(ysrv.Close)
	t.Setenv("CFR_YAHOO_BASE", ysrv.URL)

	asrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		syms := r.URL.Query().Get("symbols")
		alpacaAsked = append(alpacaAsked, syms)
		if alpacaFails {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"message":"boom"}`)
			return
		}
		bars := map[string]string{}
		for _, s := range strings.Split(syms, ",") {
			bars[s] = alpacaBar("2026-09-01", 10, 1000)
		}
		fmt.Fprint(w, alpacaPage(bars, ""))
	}))
	t.Cleanup(asrv.Close)
	t.Setenv("CFR_ALPACA_BASE", asrv.URL)

	keyID, secret := "k", "s"
	if !alpacaConfigured {
		keyID, secret = "", ""
	}
	return routedTest{
		prices:     NewRoutedPrices(NewAlpacaPrices(keyID, secret, nil), NewYahooClient(nil)),
		yahooAsked: &yahooAsked,
		alpacaAsk:  &alpacaAsked,
	}
}

// The naive predicate — "route by IsUSListing" — is wrong twice, and both
// failures are silent. isForeignListing looks for a dot, so ^GSPC and EURUSD=X
// both read as US listings; Alpaca has neither index data nor FX, so the
// benchmark column and macro's regime block would quietly go empty.
func TestRoutingSendsIndicesAndFXToYahoo(t *testing.T) {
	rt := newRoutedTest(t, true, false)

	for _, sym := range []string{"^GSPC", "^NDX", "^STOXX50E", "^N225", "EURUSD=X", "JPYUSD=X"} {
		if _, err := rt.prices.History(context.Background(), sym); err != nil {
			t.Fatalf("History(%s): %v", sym, err)
		}
	}
	if len(*rt.alpacaAsk) != 0 {
		t.Errorf("Alpaca was asked for %v — it serves neither indices nor FX", *rt.alpacaAsk)
	}
	if len(*rt.yahooAsked) != 6 {
		t.Errorf("Yahoo saw %v, want all six", *rt.yahooAsked)
	}
}

func TestRoutingSendsUSEquitiesToAlpacaAndForeignToYahoo(t *testing.T) {
	rt := newRoutedTest(t, true, false)

	for _, sym := range []string{"AAPL", "BRK.B", "BMW.DE", "7203.T"} {
		if _, err := rt.prices.History(context.Background(), sym); err != nil {
			t.Fatalf("History(%s): %v", sym, err)
		}
	}
	alpaca := strings.Join(*rt.alpacaAsk, " ")
	if !strings.Contains(alpaca, "AAPL") {
		t.Errorf("AAPL did not reach Alpaca: %v", *rt.alpacaAsk)
	}
	// Yahoo spells Berkshire BRK-B; Alpaca wants the dotted form. Routing must
	// not borrow yahooSymbol on its way out.
	if !strings.Contains(alpaca, "BRK.B") {
		t.Errorf("BRK.B reached Alpaca in the wrong spelling: %v", *rt.alpacaAsk)
	}
	yahoo := strings.Join(*rt.yahooAsked, " ")
	if !strings.Contains(yahoo, "BMW.DE") || !strings.Contains(yahoo, "7203.T") {
		t.Errorf("a foreign listing did not reach Yahoo: %v", *rt.yahooAsked)
	}
	if strings.Contains(alpaca, "BMW.DE") || strings.Contains(alpaca, "7203.T") {
		t.Errorf("a foreign listing was sent to Alpaca: %v", *rt.alpacaAsk)
	}
}

func TestRoutingFallsBackToYahooWhenAlpacaFails(t *testing.T) {
	rt := newRoutedTest(t, true, true)

	s, err := rt.prices.History(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("a failing Alpaca must fall back, not fail the name: %v", err)
	}
	if len(s.Bars) == 0 {
		t.Fatal("fallback returned no bars")
	}
	if !strings.Contains(strings.Join(*rt.yahooAsked, " "), "AAPL") {
		t.Errorf("no fallback request reached Yahoo: %v", *rt.yahooAsked)
	}
}

func TestRoutingUsesYahooEntirelyWhenAlpacaIsUnconfigured(t *testing.T) {
	rt := newRoutedTest(t, false, false)

	if _, err := rt.prices.History(context.Background(), "AAPL"); err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(*rt.alpacaAsk) != 0 {
		t.Errorf("an unconfigured Alpaca was called anyway: %v", *rt.alpacaAsk)
	}
	if !strings.Contains(strings.Join(*rt.yahooAsked, " "), "AAPL") {
		t.Errorf("Yahoo did not serve the name: %v", *rt.yahooAsked)
	}
}

// Prefetch is the whole point of the Alpaca path: one batched call warms the
// cache so runPrescreen's existing per-ticker loop makes no requests.
func TestRoutingPrefetchesOnlyTheEligibleSymbols(t *testing.T) {
	var alpacaHits atomic.Int64
	var asked string
	asrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		alpacaHits.Add(1)
		asked = r.URL.Query().Get("symbols")
		bars := map[string]string{}
		for _, s := range strings.Split(asked, ",") {
			bars[s] = alpacaBar("2026-09-01", 10, 1000)
		}
		fmt.Fprint(w, alpacaPage(bars, ""))
	}))
	t.Cleanup(asrv.Close)
	t.Setenv("CFR_ALPACA_BASE", asrv.URL)

	cache := NewCache(t.TempDir())
	prices := NewRoutedPrices(NewAlpacaPrices("k", "s", cache), NewYahooClient(cache))

	n := prices.Prefetch(context.Background(), []string{"AAPL", "MSFT", "^GSPC", "BMW.DE", "EURUSD=X"})
	if n != 2 {
		t.Errorf("prefetched %d series, want 2 (only AAPL and MSFT are eligible)", n)
	}
	if alpacaHits.Load() != 1 {
		t.Errorf("made %d Alpaca requests, want 1 batched call", alpacaHits.Load())
	}
	for _, bad := range []string{"^GSPC", "BMW.DE", "EURUSD=X"} {
		if strings.Contains(asked, bad) {
			t.Errorf("%s was included in the batch: %q", bad, asked)
		}
	}
}

func TestAlpacaEligibility(t *testing.T) {
	for _, tc := range []struct {
		sym  string
		want bool
	}{
		{"AAPL", true},
		{"BRK.B", true},
		{"MSFT", true},
		{"^GSPC", false},    // index: Alpaca has no index data
		{"^N225", false},    // index
		{"EURUSD=X", false}, /* FX: a synthetic Yahoo symbol */
		{"BMW.DE", false},   // foreign listing
		{"7203.T", false},   // foreign listing
	} {
		if got := alpacaEligible(tc.sym); got != tc.want {
			t.Errorf("alpacaEligible(%q) = %v, want %v", tc.sym, got, tc.want)
		}
	}
}

// The Yahoo client has to satisfy the seam on its own so a run with no Alpaca
// key behaves exactly as it did before.
func TestYahooClientSatisfiesPriceSource(t *testing.T) {
	var _ PriceSource = (*YahooClient)(nil)
	var _ PriceSource = (*RoutedPrices)(nil)

	if n := NewYahooClient(nil).Prefetch(context.Background(), []string{"AAPL"}); n != 0 {
		t.Errorf("Yahoo Prefetch cached %d; it has no batch endpoint and must be a no-op", n)
	}
}
