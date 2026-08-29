package marketdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// serveAVFixture stands in for AlphaVantage's /query endpoint, returning the
// captured 50-item NEWS_SENTIMENT response. It also records the query so the
// test can assert we ask for as many articles as we intend to render.
func serveAVFixture(t *testing.T, body []byte) (*httptest.Server, *string) {
	t.Helper()
	var lastQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &lastQuery
}

func loadAVFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/av_news_sentiment_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The old parser reduced this whole payload to one averaged float. Everything
// that makes a citation checkable — headline, publisher, link — was discarded,
// which is what left the news specialist inventing sources.
func TestNewsSentimentKeepsHeadlines(t *testing.T) {
	srv, query := serveAVFixture(t, loadAVFixture(t))
	t.Setenv("CFR_AV_BASE", srv.URL)

	p := NewAlphaVantageProvider("testkey", t.TempDir())
	td, err := p.Fetch(context.Background(), "news", "NVDA")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// We render newsHeadlines articles, so we must request at least that many.
	if !strings.Contains(*query, "limit=50") {
		t.Errorf("request should ask for %d articles, query = %q", newsFeedLimit, *query)
	}
	if !strings.Contains(*query, "function=NEWS_SENTIMENT") {
		t.Errorf("wrong AlphaVantage function: %q", *query)
	}

	if len(td.Facts) != 1+newsHeadlines {
		t.Fatalf("want 1 aggregate + %d headlines, got %d facts", newsHeadlines, len(td.Facts))
	}

	agg := td.Facts[0]
	if agg.Label != "News Sentiment Score" {
		t.Errorf("first fact should be the aggregate, got %q", agg.Label)
	}
	// The AMD-only article and the title-less one are excluded; 50 remain.
	if !strings.Contains(agg.Value, "from 50 articles") {
		t.Errorf("aggregate should count only NVDA-relevant articles, got %q", agg.Value)
	}

	for _, f := range td.Facts[1:] {
		if f.URL == "" {
			t.Errorf("headline %q has no URL — it cannot be cited", f.Value)
		}
		if !strings.HasPrefix(f.URL, "https://") {
			t.Errorf("headline URL %q is not a link", f.URL)
		}
		if f.Source == "" || !strings.Contains(f.Source, ".") {
			t.Errorf("headline source %q is not a publisher domain", f.Source)
		}
		if f.AsOf.IsZero() {
			t.Errorf("headline %q has no publication time", f.Value)
		}
		if !strings.Contains(f.Value, " — ") {
			t.Errorf("headline value %q should carry title and publisher", f.Value)
		}
	}

	// Highest relevance first: the fixture's relevance descends with the index.
	top := td.Facts[1]
	if !strings.Contains(top.Value, "Nvidia headline number 1 — Reuters") {
		t.Errorf("headlines not ordered by relevance, top = %q", top.Value)
	}

	// Articles that never mention NVDA, and articles with nothing citable,
	// must not appear at all.
	for _, f := range td.Facts {
		if strings.Contains(f.Value, "AMD-only") {
			t.Error("an article with no NVDA ticker_sentiment leaked into the pack")
		}
		if strings.HasPrefix(f.Value, " — ") {
			t.Error("a title-less article was emitted as a headline")
		}
	}
}

// news and sentiment are one upstream call; charging the daily budget twice for
// it puts a 12-name shortlist over the free tier's 25 requests.
func TestNewsAndSentimentShareOneCacheKey(t *testing.T) {
	p := NewAlphaVantageProvider("testkey", t.TempDir())
	cd, ok := p.(CacheDomainer)
	if !ok {
		t.Fatal("AlphaVantage should implement CacheDomainer")
	}
	if cd.CacheDomain("news") != cd.CacheDomain("sentiment") {
		t.Error("news and sentiment must share a cache key")
	}
	if cd.CacheDomain("technicals") == cd.CacheDomain("news") {
		t.Error("technicals is a separate upstream call and needs its own key")
	}
}

// Quota errors arrive as HTTP 200 with an Information/Note body. Treating one
// as a successful empty response would cache the failure for the rest of the day.
func TestNewsSentimentQuotaMessageIsAnError(t *testing.T) {
	srv, _ := serveAVFixture(t, []byte(`{"Information":"Thank you for using Alpha Vantage! Our standard API rate limit is 25 requests per day."}`))
	t.Setenv("CFR_AV_BASE", srv.URL)

	p := NewAlphaVantageProvider("testkey", t.TempDir())
	if _, err := p.Fetch(context.Background(), "news", "NVDA"); err == nil {
		t.Fatal("a quota Information message must surface as an error")
	}
}

// A dotted foreign symbol is rejected by AlphaVantage ("Invalid ticker format").
// Learning that costs one of only 25 daily requests, so half a shortlist's
// budget went on questions that could never be answered.
func TestNewsSentimentSkipsForeignListingsWithoutSpendingBudget(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(`{"feed":[]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_AV_BASE", srv.URL)

	p := NewAlphaVantageProvider("testkey", t.TempDir())
	for _, ticker := range []string{"2330.TW", "DTE.DE", "AIR.PA", "000660.KS", "HDFCBANK.NS"} {
		if _, err := p.Fetch(context.Background(), "news", ticker); err == nil {
			t.Errorf("%s: expected a skip", ticker)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("%d requests spent on symbols AlphaVantage cannot parse", n)
	}
}

// US share classes are dotted in our universe files and hyphenated upstream.
func TestNewsSentimentHyphenatesShareClasses(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("tickers")
		w.Write([]byte(`{"feed":[{"title":"Berkshire news","url":"https://reuters.com/x",
			"time_published":"20260828T120000","source":"Reuters","source_domain":"reuters.com",
			"ticker_sentiment":[{"ticker":"BRK-B","relevance_score":"0.9",
			"ticker_sentiment_score":"0.2","ticker_sentiment_label":"Somewhat-Bullish"}]}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_AV_BASE", srv.URL)

	td, err := NewAlphaVantageProvider("testkey", t.TempDir()).
		Fetch(context.Background(), "news", "BRK.B")
	if err != nil {
		t.Fatal(err)
	}
	if query != "BRK-B" {
		t.Errorf("requested tickers=%q, want BRK-B", query)
	}
	if len(td.Facts) == 0 {
		t.Error("the response echoes BRK-B; matching on BRK.B would drop every article")
	}
}

// The tier polices roughly one request per second on top of its daily cap.
func TestAlphaVantageDoesNotBurst(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(`{"feed":[]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_AV_BASE", srv.URL)

	p := NewAlphaVantageProvider("testkey", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	for i := 0; i < 3; i++ {
		p.Fetch(ctx, "news", "AAPL")
	}
	if n := atomic.LoadInt32(&hits); n > 1 {
		t.Errorf("%d requests fired inside 300ms; AlphaVantage answers a burst with a rate-limit notice, not data", n)
	}
}
