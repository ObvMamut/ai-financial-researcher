package marketdata

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/quant"
)

func newFixtureServer(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	fixture, err := os.ReadFile("testdata/yahoo_chart_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "Mozilla") {
			t.Errorf("missing browser User-Agent, got %q", ua)
		}
		if !strings.HasPrefix(r.URL.Path, "/v8/finance/chart/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
}

func TestYahooHistoryParsesFixture(t *testing.T) {
	var hits atomic.Int64
	srv := newFixtureServer(t, &hits)
	defer srv.Close()

	y := NewYahooClient(nil)
	y.baseURL = srv.URL

	s, err := y.History(context.Background(), "TEST")
	if err != nil {
		t.Fatal(err)
	}
	// 6 rows, 1 all-null holiday row skipped.
	if len(s.Bars) != 5 {
		t.Fatalf("got %d bars, want 5", len(s.Bars))
	}
	for i := 1; i < len(s.Bars); i++ {
		if s.Bars[i].Date <= s.Bars[i-1].Date {
			t.Errorf("bars not ascending: %s then %s", s.Bars[i-1].Date, s.Bars[i].Date)
		}
	}
	// Adjusted close preferred: raw close 101.0 → adj 50.5, and OHLC rescaled
	// by the same factor (open 100.0 → 50.0).
	b0 := s.Bars[0]
	if math.Abs(b0.Close-50.5) > 1e-9 {
		t.Errorf("Close = %v, want adjclose 50.5", b0.Close)
	}
	if math.Abs(b0.Open-50.0) > 1e-9 {
		t.Errorf("Open = %v, want rescaled 50.0", b0.Open)
	}
	// Volume is rescaled by the same factor as OHLC (1/0.5 = 2×) so that
	// Close×Volume still reconstructs the actual dollars traded that day:
	// raw 101.0×1,000,000 == adjusted 50.5×2,000,000.
	if b0.Volume != 2000000 {
		t.Errorf("Volume = %v, want 2000000 (rescaled with OHLC)", b0.Volume)
	}
	if got := s.LastClose(); math.Abs(got-53.0) > 1e-9 {
		t.Errorf("LastClose = %v, want 53.0", got)
	}
}

func TestYahooHistoryUsesDailyCache(t *testing.T) {
	var hits atomic.Int64
	srv := newFixtureServer(t, &hits)
	defer srv.Close()

	y := NewYahooClient(NewCache(t.TempDir()))
	y.baseURL = srv.URL

	if _, err := y.History(context.Background(), "TEST"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.History(context.Background(), "TEST"); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("server hits = %d, want 1 (second call must come from cache)", got)
	}
}

func TestYahooSymbolEscaping(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		fixture, _ := os.ReadFile("testdata/yahoo_chart_sample.json")
		w.Write(fixture)
	}))
	defer srv.Close()

	y := NewYahooClient(nil)
	y.baseURL = srv.URL
	if _, err := y.History(context.Background(), "^GSPC"); err != nil {
		t.Fatal(err)
	}
	want := "/v8/finance/chart/" + url.PathEscape("^GSPC")
	if gotPath != want {
		t.Errorf("request path = %q, want %q", gotPath, want)
	}
}

func TestYahooErrorResponses(t *testing.T) {
	t.Run("http error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		}))
		defer srv.Close()
		y := NewYahooClient(nil)
		y.baseURL = srv.URL
		if _, err := y.History(context.Background(), "TEST"); err == nil {
			t.Error("want error on HTTP 429")
		}
	})

	t.Run("chart error payload", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"chart":{"result":null,"error":{"code":"Not Found","description":"No data found"}}}`))
		}))
		defer srv.Close()
		y := NewYahooClient(nil)
		y.baseURL = srv.URL
		_, err := y.History(context.Background(), "NOPE")
		if err == nil || !strings.Contains(err.Error(), "No data found") {
			t.Errorf("want chart error surfaced, got %v", err)
		}
	})
}

// Yahoo spells US class shares with a hyphen: BRK.B is BRK-B, and the dotted
// form 404s. The universe carries the dotted form (it is what every other
// source uses), and the universe-wide pre-screen made the gap visible — every
// run silently lost Berkshire.
func TestYahooSymbolMapsClassShares(t *testing.T) {
	cases := map[string]string{
		"BRK.B":   "BRK-B",
		"BF.B":    "BF-B",
		"AAPL":    "AAPL",
		"^GSPC":   "^GSPC",
		"ASML.AS": "ASML.AS", // a real exchange suffix stays dotted
		"7203.T":  "7203.T",
		"2330.TW": "2330.TW",
	}
	for in, want := range cases {
		if got := yahooSymbol(in); got != want {
			t.Errorf("yahooSymbol(%q) = %q, want %q", in, got, want)
		}
	}
}

// chartJSON renders a Yahoo chart response ending on the nth of six sessions,
// so a test can serve a series that regresses by one bar.
func chartJSON(bars int) string {
	ts := []int64{1751895000, 1751981400, 1752067800, 1752154200, 1752240600, 1752499800}
	var t, o, h, l, c, v, a []string
	for i := 0; i < bars; i++ {
		t = append(t, fmt.Sprint(ts[i]))
		o = append(o, fmt.Sprintf("%.1f", 100+float64(i)))
		h = append(h, fmt.Sprintf("%.1f", 102+float64(i)))
		l = append(l, fmt.Sprintf("%.1f", 99+float64(i)))
		c = append(c, fmt.Sprintf("%.1f", 101+float64(i)))
		v = append(v, "1000000")
		a = append(a, fmt.Sprintf("%.1f", 101+float64(i)))
	}
	j := func(s []string) string { return strings.Join(s, ",") }
	return fmt.Sprintf(`{"chart":{"result":[{"meta":{"currency":"USD","symbol":"TEST"},`+
		`"timestamp":[%s],"indicators":{"quote":[{"open":[%s],"high":[%s],"low":[%s],`+
		`"close":[%s],"volume":[%s]}],"adjclose":[{"adjclose":[%s]}]}}],"error":null}}`,
		j(t), j(o), j(h), j(l), j(c), j(v), j(a))
}

// A response that trails what we already hold is a regression upstream, not
// news. On 2026-09-05 Yahoo served every eu50 constituent one session short of
// what the cache had held since Friday evening; the cache was overwritten, the
// forced refetch got the short series again, and the run priced STLAM.MI off a
// superseded close — which cost the idea and left the run shipping two.
func TestARegressingResponseDoesNotOverwriteABetterCachedSeries(t *testing.T) {
	var bars atomic.Int64
	bars.Store(6)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(chartJSON(int(bars.Load()))))
	}))
	defer srv.Close()

	y := NewYahooClient(NewCache(t.TempDir()))
	y.baseURL = srv.URL

	full, err := y.History(context.Background(), "TEST")
	if err != nil {
		t.Fatal(err)
	}
	newest := full.AsOf()
	if len(full.Bars) != 6 {
		t.Fatalf("first fetch got %d bars, want 6", len(full.Bars))
	}

	// Upstream now drops the last session. Both paths must hold the line: the
	// TTL path (an ordinary later run) and the forced refetch, which is exactly
	// when a regression does the most damage because the caller asked for it
	// precisely because the bar looked old.
	bars.Store(5)
	for _, tc := range []struct {
		name string
		get  func() (*quant.Series, error)
	}{
		{"HistoryFresh", func() (*quant.Series, error) { return y.HistoryFresh(context.Background(), "TEST") }},
		{"History past TTL", func() (*quant.Series, error) {
			y.SetPriceTTL(time.Nanosecond)
			defer y.SetPriceTTL(DefaultPriceTTL)
			return y.History(context.Background(), "TEST")
		}},
	} {
		got, err := tc.get()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got.AsOf() != newest {
			t.Errorf("%s returned a series ending %s, want the cached %s", tc.name, got.AsOf(), newest)
		}
		if len(got.Bars) != 6 {
			t.Errorf("%s returned %d bars, want the cached 6", tc.name, len(got.Bars))
		}
	}

	// And the moment upstream catches up, the fetch wins again — the guard
	// must not pin the cache to a series that has genuinely been superseded.
	bars.Store(6)
	y.SetPriceTTL(time.Nanosecond)
	defer y.SetPriceTTL(DefaultPriceTTL)
	back, err := y.History(context.Background(), "TEST")
	if err != nil {
		t.Fatal(err)
	}
	if back.AsOf() != newest || len(back.Bars) != 6 {
		t.Errorf("after upstream recovered, got %d bars ending %s, want 6 ending %s",
			len(back.Bars), back.AsOf(), newest)
	}
}

// The regression that mattered crossed a day boundary: the good series was
// cached on Friday evening and the regressed fetch came on Saturday morning.
// Cache keys carry the UTC calendar date, so those are two different files, and
// a guard that reads only today's key cannot see the one worth keeping.
func TestTheBetterCachedSeriesIsFoundAcrossADayBoundary(t *testing.T) {
	var bars atomic.Int64
	bars.Store(6)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(chartJSON(int(bars.Load()))))
	}))
	defer srv.Close()

	dir := t.TempDir()
	friday := time.Date(2026, 9, 4, 19, 19, 0, 0, time.UTC)
	cache := NewCache(dir)
	cache.now = func() time.Time { return friday }
	y := NewYahooClient(cache)
	y.baseURL = srv.URL

	full, err := y.History(context.Background(), "TEST")
	if err != nil {
		t.Fatal(err)
	}
	newest := full.AsOf()

	// Saturday morning. Upstream has lost the last session, and today's key
	// holds nothing at all — the only better series is under Friday's.
	cache.now = func() time.Time { return friday.AddDate(0, 0, 1).Add(-5 * time.Hour) }
	bars.Store(5)
	got, err := y.HistoryFresh(context.Background(), "TEST")
	if err != nil {
		t.Fatal(err)
	}
	if got.AsOf() != newest || len(got.Bars) != 6 {
		t.Errorf("Saturday's fetch returned %d bars ending %s, want Friday's cached 6 ending %s",
			len(got.Bars), got.AsOf(), newest)
	}

	// Beyond the lookback window the guard lets go: a series nobody has
	// refreshed in a week is not evidence about today.
	cache.now = func() time.Time { return friday.AddDate(0, 0, priceCacheLookbackDays+2) }
	stale, err := y.HistoryFresh(context.Background(), "TEST")
	if err != nil {
		t.Fatal(err)
	}
	if len(stale.Bars) != 5 {
		t.Errorf("a week later the guard still pinned the old series: got %d bars", len(stale.Bars))
	}
}
