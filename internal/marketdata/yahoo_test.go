package marketdata

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
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
	if b0.Volume != 1000000 {
		t.Errorf("Volume = %v, want 1000000", b0.Volume)
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
