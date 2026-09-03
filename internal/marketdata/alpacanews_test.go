package marketdata

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// alpacaNewsItem renders one article in Alpaca's wire shape: RFC-3339
// created_at, a symbols array, headline/source rather than title/publisher.
func alpacaNewsItem(headline, source string, ageHours int, symbols ...string) string {
	syms := make([]string, 0, len(symbols))
	for _, s := range symbols {
		syms = append(syms, `"`+s+`"`)
	}
	return fmt.Sprintf(`{"id":1,"headline":%q,"summary":"s","author":"a","source":%q,
		"url":"https://example.test/%s","symbols":[%s],"created_at":%q,"updated_at":%q}`,
		headline, source, source, strings.Join(syms, ","),
		time.Now().Add(-time.Duration(ageHours)*time.Hour).UTC().Format(time.RFC3339),
		time.Now().UTC().Format(time.RFC3339))
}

func alpacaNewsServer(t *testing.T, items ...string) *[]string {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Query().Get("symbols"))
		if r.Header.Get("APCA-API-KEY-ID") == "" {
			t.Error("news request carried no credentials")
		}
		fmt.Fprintf(w, `{"news":[%s],"next_page_token":null}`, strings.Join(items, ","))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_ALPACA_BASE", srv.URL)
	return &asked
}

func TestAlpacaNewsGroundsTheDomain(t *testing.T) {
	asked := alpacaNewsServer(t,
		alpacaNewsItem("Apple raises guidance", "Benzinga", 5, "AAPL"),
		alpacaNewsItem("Sector note", "Benzinga", 20),
	)

	td, err := NewAlpacaNewsProvider("k", "s").Fetch(context.Background(), "news", "AAPL")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(*asked) != 1 || (*asked)[0] != "AAPL" {
		t.Fatalf("asked for %v, want AAPL", *asked)
	}
	if len(td.Facts) != 2 {
		t.Fatalf("got %d facts, want 2: %+v", len(td.Facts), td.Facts)
	}
	joined := fmt.Sprintf("%+v", td.Facts)
	if !strings.Contains(joined, "Apple raises guidance") {
		t.Errorf("headline missing:\n%s", joined)
	}
	// The symbols array is the only relevance signal, and it is printed rather
	// than filtered on — same contract as the Yahoo provider.
	if !strings.Contains(joined, "tagged to this ticker") || !strings.Contains(joined, "not tagged to this ticker") {
		t.Errorf("relevance is not distinguishable:\n%s", joined)
	}
	if !HasDomainEvidence("news", td) {
		t.Error("headlines must ground the news domain")
	}
	for _, f := range td.Facts {
		if f.URL == "" {
			t.Errorf("fact %q carries no link", f.Label)
		}
	}
}

// Same failure as the Yahoo feed and the same remedy: a feed that returned
// items and kept none of them must say why, and the timestamp field it names
// has to be Alpaca's, not Yahoo's.
func TestAlpacaNewsWarnsWhenEveryItemIsUnusable(t *testing.T) {
	alpacaNewsServer(t,
		`{"id":1,"headline":"no timestamp","source":"Benzinga","url":"https://x.test/1","symbols":["AAPL"]}`,
		`{"id":2,"headline":"also none","source":"Benzinga","url":"https://x.test/2","symbols":["AAPL"]}`,
	)

	td, err := NewAlpacaNewsProvider("k", "s").Fetch(context.Background(), "news", "AAPL")
	if err != nil {
		t.Fatalf("a shape change must not become a fetch error: %v", err)
	}
	if len(td.Facts) != 0 {
		t.Fatalf("unusable items were published: %+v", td.Facts)
	}
	w := strings.Join(td.Warnings, " | ")
	if !strings.Contains(w, "2 items") {
		t.Errorf("the warning does not say how many arrived: %s", w)
	}
	if !strings.Contains(w, "created_at") {
		t.Errorf("the warning names the wrong timestamp field: %s", w)
	}
	if strings.Contains(w, "providerPublishTime") {
		t.Errorf("the warning names Yahoo's field on an Alpaca feed: %s", w)
	}
}

func TestAlpacaNewsStaleNameReadsAsStale(t *testing.T) {
	alpacaNewsServer(t,
		alpacaNewsItem("last quarter", "Benzinga", 24*40, "AAPL"),
		alpacaNewsItem("older still", "Benzinga", 24*90, "AAPL"),
	)

	td, _ := NewAlpacaNewsProvider("k", "s").Fetch(context.Background(), "news", "AAPL")
	w := strings.Join(td.Warnings, " | ")
	if !strings.Contains(w, "older than") {
		t.Errorf("the warning does not name the age cutoff: %s", w)
	}
	if strings.Contains(w, "created_at") {
		t.Errorf("the warning blames the timestamp field for a stale name: %s", w)
	}
}

func TestAlpacaNewsStaysSilentOnAnEmptyOrHealthyFeed(t *testing.T) {
	alpacaNewsServer(t)
	td, err := NewAlpacaNewsProvider("k", "s").Fetch(context.Background(), "news", "AAPL")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(td.Warnings) != 0 {
		t.Errorf("an empty feed must stay silent: %v", td.Warnings)
	}

	alpacaNewsServer(t, alpacaNewsItem("real one", "Benzinga", 3, "AAPL"))
	td, _ = NewAlpacaNewsProvider("k", "s").Fetch(context.Background(), "news", "AAPL")
	if len(td.Warnings) != 0 {
		t.Errorf("a healthy feed must stay silent: %v", td.Warnings)
	}
}

func TestAlpacaNewsIsUSOnlyAndOptional(t *testing.T) {
	alpacaNewsServer(t)
	_, err := NewAlpacaNewsProvider("k", "s").Fetch(context.Background(), "news", "BMW.DE")
	if !errors.Is(err, ErrNotApplicable) {
		t.Errorf("a foreign listing has no Alpaca coverage; want ErrNotApplicable, got %v", err)
	}

	if NewAlpacaNewsProvider("", "").Available() {
		t.Error("an unkeyed news provider reported itself available")
	}
	if _, err := NewAlpacaNewsProvider("k", "s").Fetch(context.Background(), "sentiment", "AAPL"); err == nil {
		t.Error("the news provider answered for the sentiment domain")
	}
}

// Alpaca and Yahoo both serve the news domain for a US name, and BuildPack
// merges every provider that answers. Without a dedupe the reader — and the
// specialist — sees each story twice and reads the repetition as corroboration.
func TestPackDedupesHeadlinesAcrossNewsProviders(t *testing.T) {
	shared := "Apple raises guidance"
	asrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"news":[%s],"next_page_token":null}`,
			alpacaNewsItem(shared, "Benzinga", 4, "AAPL"))
	}))
	t.Cleanup(asrv.Close)
	t.Setenv("CFR_ALPACA_BASE", asrv.URL)

	ysrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"news":[%s],"quotes":[]}`, newsItem(shared, "Reuters", 4, "AAPL"))
	}))
	t.Cleanup(ysrv.Close)
	t.Setenv("CFR_YAHOO_BASE", ysrv.URL)

	svc := NewService(nil, NewAlpacaNewsProvider("k", "s"), NewYahooNewsProvider())
	pack := svc.BuildPack(context.Background(), "news", []string{"AAPL"})

	seen := 0
	for _, f := range pack.ByTicker["AAPL"].Facts {
		if strings.Contains(f.Value, shared) {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("the same headline appears %d times; two providers covering one US name must not double-count it", seen)
	}
}
