package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// alpacaBar renders one bar in the wire shape Alpaca uses: single-letter keys,
// an RFC-3339 timestamp.
func alpacaBar(date string, close, vol float64) string {
	return fmt.Sprintf(`{"t":"%sT05:00:00Z","o":%.2f,"h":%.2f,"l":%.2f,"c":%.2f,"v":%.0f,"n":10,"vw":%.2f}`,
		date, close, close*1.01, close*0.99, close, vol, close)
}

// alpacaPage renders one page of the bars response. bars maps symbol to its
// rendered bar list; token is the next_page_token ("" serialises as null).
func alpacaPage(bars map[string]string, token string) string {
	parts := make([]string, 0, len(bars))
	for sym, bs := range bars {
		parts = append(parts, fmt.Sprintf(`%q:[%s]`, sym, bs))
	}
	tok := "null"
	if token != "" {
		tok = fmt.Sprintf("%q", token)
	}
	return fmt.Sprintf(`{"bars":{%s},"next_page_token":%s}`, strings.Join(parts, ","), tok)
}

func alpacaServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("CFR_ALPACA_BASE", srv.URL)
}

func TestAlpacaHistoryParsesBarsAndSendsCredentials(t *testing.T) {
	alpacaServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("APCA-API-KEY-ID"); got != "key" {
			t.Errorf("APCA-API-KEY-ID = %q, want the configured key id", got)
		}
		if got := r.Header.Get("APCA-API-SECRET-KEY"); got != "secret" {
			t.Errorf("APCA-API-SECRET-KEY = %q, want the configured secret", got)
		}
		if r.URL.Path != "/v2/stocks/bars" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("timeframe"); got != "1Day" {
			t.Errorf("timeframe = %q, want 1Day", got)
		}
		fmt.Fprint(w, alpacaPage(map[string]string{
			"AAPL": alpacaBar("2026-08-31", 100, 1e6) + "," + alpacaBar("2026-09-01", 102, 2e6),
		}, ""))
	})

	s, err := NewAlpacaPrices("key", "secret", nil).History(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(s.Bars) != 2 {
		t.Fatalf("got %d bars, want 2: %+v", len(s.Bars), s.Bars)
	}
	if s.Bars[0].Date != "2026-08-31" || s.Bars[1].Date != "2026-09-01" {
		t.Errorf("dates not parsed to YYYY-MM-DD ascending: %+v", s.Bars)
	}
	if s.Bars[1].Close != 102 || s.Bars[1].Volume != 2e6 {
		t.Errorf("close/volume wrong: %+v", s.Bars[1])
	}
}

// The single most dangerous default in this integration. On the free tier the
// feed resolves to "best available", which is IEX — about 2.5% of consolidated
// volume. Every US name's average dollar volume would come back ~40x too small
// and the liquidity floor would reject the whole universe as illiquid, with no
// error anywhere.
func TestAlpacaAlwaysAsksForTheSIPFeed(t *testing.T) {
	var feeds []string
	alpacaServer(t, func(w http.ResponseWriter, r *http.Request) {
		feeds = append(feeds, r.URL.Query().Get("feed"))
		if got := r.URL.Query().Get("end"); got == "" {
			t.Error("no end parameter: SIP needs an end at least 15 minutes old")
		}
		fmt.Fprint(w, alpacaPage(map[string]string{"AAPL": alpacaBar("2026-09-01", 100, 1e6)}, ""))
	})

	if _, err := NewAlpacaPrices("k", "s", nil).History(context.Background(), "AAPL"); err != nil {
		t.Fatalf("History: %v", err)
	}
	for _, f := range feeds {
		if f != "sip" {
			t.Errorf("feed = %q, want an explicit \"sip\" on every request", f)
		}
	}
}

func TestAlpacaRefusesToDegradeToTheIEXFeed(t *testing.T) {
	alpacaServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"subscription does not permit querying recent SIP data"}`)
	})

	s, err := NewAlpacaPrices("k", "s", nil).History(context.Background(), "AAPL")
	if err == nil {
		t.Fatal("a refused SIP feed must be an error, not a quiet fallback to IEX")
	}
	if s != nil {
		t.Errorf("bars returned alongside the error: %+v", s)
	}
	if !strings.Contains(err.Error(), "iex") && !strings.Contains(err.Error(), "IEX") {
		t.Errorf("the error does not name the feed that would have been used: %v", err)
	}
	if !strings.Contains(err.Error(), "volume") {
		t.Errorf("the error does not say what IEX volume would do to the run: %v", err)
	}
}

// limit counts bars, not symbols, and results are ordered by symbol then time —
// so one symbol's history straddles a page boundary. Dropping the continuation
// silently truncates a series, and a truncated series is a wrong momentum
// number rather than a missing one.
func TestAlpacaReassemblesASymbolSplitAcrossPages(t *testing.T) {
	var hits atomic.Int64
	alpacaServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page_token") {
		case "":
			hits.Add(1)
			fmt.Fprint(w, alpacaPage(map[string]string{
				"AAA": alpacaBar("2026-08-28", 10, 1e6) + "," + alpacaBar("2026-08-31", 11, 1e6),
			}, "page2"))
		case "page2":
			hits.Add(1)
			fmt.Fprint(w, alpacaPage(map[string]string{
				"AAA": alpacaBar("2026-09-01", 12, 1e6),
				"BBB": alpacaBar("2026-09-01", 50, 2e6),
			}, ""))
		default:
			t.Errorf("unexpected page_token %q", r.URL.Query().Get("page_token"))
		}
	})

	cache := NewCache(t.TempDir())
	a := NewAlpacaPrices("k", "s", cache)
	if n := a.Prefetch(context.Background(), []string{"AAA", "BBB"}); n != 2 {
		t.Fatalf("Prefetch cached %d series, want 2", n)
	}
	if hits.Load() != 2 {
		t.Errorf("made %d requests, want 2 (one per page)", hits.Load())
	}

	s, err := a.History(context.Background(), "AAA")
	if err != nil {
		t.Fatalf("History(AAA): %v", err)
	}
	if len(s.Bars) != 3 {
		t.Fatalf("AAA has %d bars, want 3 — the page boundary truncated it: %+v", len(s.Bars), s.Bars)
	}
	if s.Bars[2].Date != "2026-09-01" {
		t.Errorf("the continuation page's bar is missing: %+v", s.Bars)
	}
}

// The point of batching: one multi-symbol call warms the cache so the
// pre-screen's existing per-ticker loop makes no requests at all.
func TestAlpacaPrefetchWarmsTheCacheSoHistoryDoesNotRefetch(t *testing.T) {
	var hits atomic.Int64
	alpacaServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, alpacaPage(map[string]string{
			"AAA": alpacaBar("2026-09-01", 10, 1e6),
			"BBB": alpacaBar("2026-09-01", 20, 1e6),
		}, ""))
	})

	a := NewAlpacaPrices("k", "s", NewCache(t.TempDir()))
	a.Prefetch(context.Background(), []string{"AAA", "BBB"})
	before := hits.Load()

	for _, sym := range []string{"AAA", "BBB"} {
		if _, err := a.History(context.Background(), sym); err != nil {
			t.Fatalf("History(%s): %v", sym, err)
		}
	}
	if got := hits.Load(); got != before {
		t.Errorf("History made %d further request(s); the prefetch did not populate the cache", got-before)
	}
}

// Yahoo spells Berkshire BRK-B and 404s the dotted form; Alpaca is the other
// way round. The Alpaca client must not borrow yahooSymbol.
func TestAlpacaKeepsTheDottedShareClassSymbol(t *testing.T) {
	var asked string
	alpacaServer(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("symbols")
		fmt.Fprint(w, alpacaPage(map[string]string{"BRK.B": alpacaBar("2026-09-01", 700, 1e5)}, ""))
	})

	if _, err := NewAlpacaPrices("k", "s", nil).History(context.Background(), "BRK.B"); err != nil {
		t.Fatalf("History: %v", err)
	}
	if asked != "BRK.B" {
		t.Errorf("asked for %q, want BRK.B — Alpaca uses the dotted form", asked)
	}
}

func TestAlpacaWithoutCredentialsIsUnavailable(t *testing.T) {
	if NewAlpacaPrices("", "", nil).Available() {
		t.Error("a client with no credentials reported itself available")
	}
	if NewAlpacaPrices("k", "", nil).Available() {
		t.Error("a key id with no secret reported itself available")
	}
	if !NewAlpacaPrices("k", "s", nil).Available() {
		t.Error("a fully configured client reported itself unavailable")
	}
}

// A symbol Alpaca simply has no bars for is not an error at the transport
// level, but it must not come back as an empty series either — the caller
// cannot tell an empty series from a quiet one.
func TestAlpacaReportsASymbolWithNoBars(t *testing.T) {
	alpacaServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"bars":{},"next_page_token":null}`)
	})

	if _, err := NewAlpacaPrices("k", "s", nil).History(context.Background(), "NOPE"); err == nil {
		t.Error("a symbol with no bars must be an error, not an empty series")
	}
}

func TestAlpacaSurfacesAnErrorBody(t *testing.T) {
	alpacaServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"message":"invalid symbol"}`)
	})

	_, err := NewAlpacaPrices("k", "s", nil).History(context.Background(), "!!")
	if err == nil {
		t.Fatal("a 422 must be reported")
	}
	if !strings.Contains(err.Error(), "invalid symbol") {
		t.Errorf("the error drops the server's own explanation: %v", err)
	}
}

// Guard the wire contract the parser depends on: single-letter bar keys.
func TestAlpacaBarWireShapeIsStable(t *testing.T) {
	var b alpacaWireBar
	if err := json.Unmarshal([]byte(alpacaBar("2026-09-01", 10, 5)), &b); err != nil {
		t.Fatal(err)
	}
	if b.Close != 10 || b.Volume != 5 || !strings.HasPrefix(b.Time, "2026-09-01") {
		t.Errorf("wire bar did not decode: %+v", b)
	}
}

// The lab prices a membership interval with Alpaca's asof set inside it, which
// resolves a symbol to the company that held it then: FB as of 2020 is Meta's
// whole history, FI as of 2018 is Frank's International, not Fiserv.
func TestAlpacaHistoryAsOfSendsTheWindowAndAsof(t *testing.T) {
	var got []url.Values
	alpacaServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Query())
		fmt.Fprint(w, alpacaPage(map[string]string{
			"FB": alpacaBar("2016-01-04", 102.22, 1e7) + "," + alpacaBar("2022-06-08", 190.0, 1e7),
		}, ""))
	})
	a := NewAlpacaPrices("k", "s", NewCache(t.TempDir()))
	start, end, asof := asofDate("2016-01-01"), asofDate("2022-06-30"), asofDate("2022-06-08")
	out, err := a.HistoryAsOf(context.Background(), []string{"FB"}, start, end, asof)
	if err != nil {
		t.Fatal(err)
	}
	if s := out["FB"]; s == nil || len(s.Bars) != 2 || s.Bars[1].Close != 190.0 {
		t.Fatalf("series = %+v", out["FB"])
	}
	q := got[0]
	if q.Get("asof") != "2022-06-08" || q.Get("start") != "2016-01-01" || !strings.HasPrefix(q.Get("end"), "2022-06-30") || q.Get("feed") != "sip" {
		t.Errorf("request = %v", q)
	}

	// A window that closed in the past never changes, so it is served from
	// the permanent cache without a second request.
	if _, err := a.HistoryAsOf(context.Background(), []string{"FB"}, start, end, asof); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("a finished window was fetched %d times, want once", len(got))
	}
}

func TestAlpacaHistoryAsOfReportsNamesItCouldNotResolve(t *testing.T) {
	alpacaServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, alpacaPage(map[string]string{"AAPL": alpacaBar("2016-01-04", 26.3, 1e8)}, ""))
	})
	a := NewAlpacaPrices("k", "s", nil)
	out, err := a.HistoryAsOf(context.Background(), []string{"AAPL", "MWV"}, asofDate("2016-01-01"), asofDate("2016-02-01"), asofDate("2016-01-29"))
	if err != nil {
		t.Fatal(err)
	}
	if out["AAPL"] == nil {
		t.Error("AAPL missing")
	}
	if _, ok := out["MWV"]; ok {
		t.Error("a symbol with no bars was returned as an empty series")
	}
}

func asofDate(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}
