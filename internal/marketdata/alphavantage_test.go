package marketdata

import (
	"context"
	"errors"
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
		// The news domain also fetches the bulk earnings calendar; serve it an
		// empty one so the assertions below stay about the NEWS_SENTIMENT call.
		if r.URL.Query().Get("function") == "EARNINGS_CALENDAR" {
			w.Write([]byte("symbol,name,reportDate,fiscalDateEnding,estimate,currency\n"))
			return
		}
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

	// AV no longer gets isSubjectRelevant's "≤3 symbols" free pass (fix round
	// 1: a real AV item's own tag list is almost always that short, which
	// would make the text rule a near no-op for this provider), so the
	// fixture's "Nvidia headline number N" titles need the company-name
	// lookup a live run always wires (orchestrator.go's WithCompanyNames) to
	// count as coverage rather than relying on relevance alone.
	ctx := WithCompanyNames(context.Background(), func(string) []string { return []string{"Nvidia"} })
	p := NewAlphaVantageProvider("testkey", t.TempDir())
	td, err := p.Fetch(ctx, "news", "NVDA")
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

	// Names with no major-exchange US line. 2330.TW and HDFCBANK.NS are absent
	// deliberately: they now resolve to TSM and HDB and are fetched.
	p := NewAlphaVantageProvider("testkey", t.TempDir())
	for _, ticker := range []string{"DTE.DE", "AIR.PA", "000660.KS", "005930.KS", "2317.TW"} {
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
		if r.URL.Query().Get("function") == "EARNINGS_CALENDAR" {
			w.Write([]byte("symbol,name,reportDate,fiscalDateEnding,estimate,currency\n"))
			return
		}
		query = r.URL.Query().Get("tickers")
		// This test is about the hyphenation match, not the subject-relevance
		// rule, so the relevance score clears AVRelevanceFloor directly
		// rather than wiring a company-name lookup the ticker-matching logic
		// under test has no part in.
		w.Write([]byte(`{"feed":[{"title":"Berkshire news","url":"https://reuters.com/x",
			"time_published":"20260828T120000","source":"Reuters","source_domain":"reuters.com",
			"ticker_sentiment":[{"ticker":"BRK-B","relevance_score":"0.99",
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

// avWrapItem is the shape of a real low-relevance AlphaVantage item that
// tags this ticker without being about it, taken from
// runs/2026-08-31T09-39-06/data/news.json (TTD's saved news.json, "Headline 6
// (relevance 0.65, ...)"): a MarketBeat 13F-holdings alert whose own headline
// names Amazon and $AMZN, not Trade Desk. The real captured feed
// (testdata/av_news_sentiment_sample.json) tags every item with only one or
// two tickers, never a dozen, so this reconstructs the same two-ticker shape
// (AMZN, the real subject, plus TTD) rather than an invented multi-name wrap —
// isSubjectRelevant's own "≤3 symbols" fallback would wave a short tag list
// like this through, which is exactly why AV does not use that fallback (see
// articlesFor's comment); this fixture is what pins that.
const avWrapItem = `{"title":"AFG Fiduciary Services Limited Partnership Has $3.87 Million Holdings in Amazon.com, Inc. $AMZN — MarketBeat",
	 "url":"https://www.marketbeat.com/instant-alerts/x/","source":"MarketBeat","source_domain":"marketbeat.com",
	 "time_published":"20260822T070916",
	 "ticker_sentiment":[
		{"ticker":"AMZN","relevance_score":"0.95","ticker_sentiment_score":"0.10","ticker_sentiment_label":"Neutral"},
		{"ticker":"TTD","relevance_score":"0.65","ticker_sentiment_score":"0.11","ticker_sentiment_label":"Neutral"}
	 ]}`

// A low-relevance wrap tagged to this ticker, mixed in with a genuine headline
// for it: the wrap must not count as coverage or be weighted into the
// aggregate sentiment, but must still be printed as context; the genuine
// headline still must count.
func TestNewsSentimentExcludesAWrapTaggedAtLowRelevance(t *testing.T) {
	srv, _ := serveAVFixture(t, []byte(`{"feed":[`+avWrapItem+`,
		{"title":"The Trade Desk (TTD) beats Q2 earnings estimates",
		 "url":"https://reuters.com/ttd-earnings","source":"Reuters","source_domain":"reuters.com",
		 "time_published":"20260828T120000",
		 "ticker_sentiment":[{"ticker":"TTD","relevance_score":"0.90","ticker_sentiment_score":"0.40","ticker_sentiment_label":"Bullish"}]}
	]}`))
	t.Setenv("CFR_AV_BASE", srv.URL)

	td, err := NewAlphaVantageProvider("testkey", t.TempDir()).Fetch(context.Background(), "news", "TTD")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	var agg, wrap, genuine *Fact
	for i := range td.Facts {
		f := &td.Facts[i]
		switch {
		case f.Label == "News Sentiment Score":
			agg = f
		case strings.Contains(f.Value, "AFG Fiduciary"):
			wrap = f
		case strings.Contains(f.Value, "beats Q2 earnings"):
			genuine = f
		}
	}
	if wrap == nil || genuine == nil {
		t.Fatalf("expected both headlines rendered as facts, got %+v", td.Facts)
	}
	if !strings.HasSuffix(wrap.Label, "context, not about this company)") {
		t.Errorf("wrap item label = %q, want it marked context, not coverage", wrap.Label)
	}
	if !strings.HasSuffix(genuine.Label, "tagged to this ticker)") {
		t.Errorf("genuine item label = %q, want it marked as coverage", genuine.Label)
	}
	if agg == nil {
		t.Fatal("expected an aggregate News Sentiment Score fact from the one genuine article")
	}
	if !strings.Contains(agg.Value, "from 1 articles") {
		t.Errorf("aggregate should weight only the subject-relevant article, got %q", agg.Value)
	}
}

// When *every* item tagged to this ticker is a low-relevance wrap (the same
// shape as above, alone this time), the feed must produce no Facts at all —
// only a warning — exactly as newsfilter.go's headlineFacts does for
// Alpaca/Yahoo. HasDomainEvidence (pack.go) treats any non-empty Facts list
// as the news domain having something to say about this ticker other than the
// earnings-date/US-line notes, so leaving the wrap's headline in as a Fact
// would make this ticker "news covered" on an item that is not about it — the
// SAP.DE defect one provider over.
func TestNewsSentimentEmitsNoFactsWhenEveryItemIsNonSubject(t *testing.T) {
	srv, _ := serveAVFixture(t, []byte(`{"feed":[`+avWrapItem+`]}`))
	t.Setenv("CFR_AV_BASE", srv.URL)

	td, err := NewAlphaVantageProvider("testkey", t.TempDir()).Fetch(context.Background(), "news", "TTD")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(td.Facts) != 0 {
		t.Errorf("expected no Facts when every AV item is non-subject, got %+v", td.Facts)
	}
	if len(td.Warnings) == 0 {
		t.Error("expected a warning explaining the feed has no coverage")
	}
	if HasDomainEvidence("news", td) {
		t.Error("HasDomainEvidence should be false when every item is a non-subject wrap")
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

// A key without the premium plan gets the same refusal for every ticker. The
// first one marks NEWS_SENTIMENT not applicable for the process; no later call
// may spend a request or wait on the limiter.
func TestNewsSentimentPremiumEndpointStopsAsking(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("function") != "NEWS_SENTIMENT" {
			return // the calendar is a different endpoint; this test counts news asks
		}
		hits.Add(1)
		w.Write([]byte(`{"Information":"Thank you for using Alpha Vantage! This is a premium endpoint. You may subscribe to any of the premium plans at https://www.alphavantage.co/premium/ to instantly unlock all premium endpoints"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_AV_BASE", srv.URL)

	p := NewAlphaVantageProvider("testkey", t.TempDir())
	_, err := p.Fetch(context.Background(), "sentiment", "NVDA")
	if !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("first premium refusal = %v, want ErrNotApplicable", err)
	}
	if !strings.Contains(err.Error(), "premium endpoint on this key") {
		t.Fatalf("error should name the cause: %v", err)
	}
	// An already-cancelled context proves the second call never reaches the limiter.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Fetch(ctx, "sentiment", "AAPL"); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("second ticker = %v, want ErrNotApplicable", err)
	}
	if _, err := p.Fetch(context.Background(), "news", "MSFT"); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("news domain = %v, want ErrNotApplicable", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("server saw %d NEWS_SENTIMENT requests, want 1", n)
	}
}

// serveAVNewsAndCalendar answers NEWS_SENTIMENT with newsBody and the calendar
// with one upcoming NVDA report date.
func serveAVNewsAndCalendar(t *testing.T, newsBody string) *httptest.Server {
	t.Helper()
	soon := time.Now().AddDate(0, 0, 9).Format("2006-01-02")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("function") == "EARNINGS_CALENDAR" {
			w.Write([]byte(calendarCSV("NVDA,NVIDIA Corp," + soon + ",2026-10-31,1.20,USD")))
			return
		}
		w.Write([]byte(newsBody))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func hasNewsUnavailableWarning(td TickerData) bool {
	for _, w := range td.Warnings {
		if strings.Contains(w, "news feed unavailable") {
			return true
		}
	}
	return false
}

// A premium-gated news feed is an expected gap: the calendar fact ships alone
// and no per-ticker warning is raised. A genuine failure still warns.
func TestNewsDomainWarnsOnlyOnGenuineNewsFailure(t *testing.T) {
	premium := serveAVNewsAndCalendar(t, `{"Information":"This is a premium endpoint."}`)
	t.Setenv("CFR_AV_BASE", premium.URL)
	td, err := NewAlphaVantageProvider("testkey", t.TempDir()).Fetch(context.Background(), "news", "NVDA")
	if err != nil {
		t.Fatal(err)
	}
	var haveDate bool
	for _, f := range td.Facts {
		if f.Label == EarningsFactLabel {
			haveDate = true
		}
	}
	if !haveDate {
		t.Fatal("calendar fact must still ship")
	}
	if hasNewsUnavailableWarning(td) {
		t.Fatalf("premium news must not warn per ticker: %v", td.Warnings)
	}

	failing := serveAVNewsAndCalendar(t, `{"Error Message":"Invalid API call."}`)
	t.Setenv("CFR_AV_BASE", failing.URL)
	td, err = NewAlphaVantageProvider("testkey", t.TempDir()).Fetch(context.Background(), "news", "NVDA")
	if err != nil {
		t.Fatal(err)
	}
	if !hasNewsUnavailableWarning(td) {
		t.Fatalf("a genuine news failure must still warn: %v", td.Warnings)
	}
}
