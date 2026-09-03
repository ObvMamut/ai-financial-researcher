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

func newsItem(title, publisher string, ageHours int, related ...string) string {
	rel := make([]string, 0, len(related))
	for _, r := range related {
		rel = append(rel, `"`+r+`"`)
	}
	return fmt.Sprintf(`{"uuid":"%s","title":%q,"publisher":%q,"link":"https://example.test/%s",
		"providerPublishTime":%d,"type":"STORY","relatedTickers":[%s]}`,
		title, title, publisher, publisher,
		time.Now().Add(-time.Duration(ageHours)*time.Hour).Unix(), strings.Join(rel, ","))
}

func newsServer(t *testing.T, items ...string) *[]string {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Query().Get("q"))
		fmt.Fprintf(w, `{"news":[%s],"quotes":[]}`, strings.Join(items, ","))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)
	return &asked
}

// The news domain is a quarter of the whole score and rested entirely on
// AlphaVantage, which is US-only. A foreign listing reached it through an ADR
// map covering 26 of the universe's 114 foreign names, so 9 of eu50's top-15
// pre-screen ranks and 11 of asia100's could never be researched at all.
func TestYahooNewsReachesAForeignListingUnderItsOwnSymbol(t *testing.T) {
	asked := newsServer(t,
		newsItem("BMW lifts guidance", "Reuters", 6, "BMW.DE"),
		newsItem("German autos rally", "Handelsblatt", 20),
	)

	td, err := NewYahooNewsProvider().Fetch(context.Background(), "news", "BMW.DE")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(*asked) != 1 || (*asked)[0] != "BMW.DE" {
		t.Fatalf("asked for %v, want the listing's own symbol — an ADR would be a different company's coverage", *asked)
	}
	if len(td.Facts) != 2 {
		t.Fatalf("got %d facts, want 2: %+v", len(td.Facts), td.Facts)
	}
	joined := fmt.Sprintf("%+v", td.Facts)
	if !strings.Contains(joined, "BMW lifts guidance") {
		t.Errorf("headline missing:\n%s", joined)
	}
	// An untagged item is context, not coverage of the company, and the agent
	// has to be able to tell them apart rather than have one filtered silently.
	if !strings.Contains(joined, "tagged to this ticker") || !strings.Contains(joined, "not tagged to this ticker") {
		t.Errorf("relevance is not distinguishable:\n%s", joined)
	}
	// And this is what makes it count as news coverage at all.
	if !HasDomainEvidence("news", td) {
		t.Error("headlines must ground the news domain")
	}
}

func TestYahooNewsOrdersNewestFirstAndDropsStaleItems(t *testing.T) {
	newsServer(t,
		newsItem("older but listed first", "Reuters", 48, "AAPL"),
		newsItem("newest", "Bloomberg", 2, "AAPL"),
		newsItem("ancient history", "Reuters", 24*60, "AAPL"),
	)

	td, err := NewYahooNewsProvider().Fetch(context.Background(), "news", "AAPL")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(td.Facts) != 2 {
		t.Fatalf("got %d facts, want 2 (the 60-day item is background, not flow): %+v", len(td.Facts), td.Facts)
	}
	// A search endpoint orders by its own relevance score. A catalyst read wants
	// the tape's order.
	if !strings.Contains(td.Facts[0].Value, "newest") {
		t.Errorf("headlines are not newest-first: %+v", td.Facts)
	}
}

// A quiet name is not a failed fetch. Returning an error here would put a name
// with genuinely no news into the run's data_errors alongside the ones whose
// provider broke.
func TestYahooNewsTreatsAQuietNameAsQuietNotBroken(t *testing.T) {
	newsServer(t)

	td, err := NewYahooNewsProvider().Fetch(context.Background(), "news", "AAPL")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(td.Facts) != 0 {
		t.Errorf("expected no facts, got %+v", td.Facts)
	}
	if HasDomainEvidence("news", td) {
		t.Error("no headlines must not count as news coverage")
	}
}

func TestYahooNewsServesOnlyItsOwnDomain(t *testing.T) {
	newsServer(t)
	if _, err := NewYahooNewsProvider().Fetch(context.Background(), "sentiment", "AAPL"); err == nil {
		t.Error("the news provider answered for the sentiment domain")
	}
}

// newsItemNoTimestamp is an item with the providerPublishTime key absent
// entirely — what a renamed, retyped or relocated timestamp field looks like
// from here. Every such item is dropped, so a whole feed of them is
// indistinguishable from a quiet name unless the provider says so.
func newsItemNoTimestamp(title, publisher string) string {
	return fmt.Sprintf(`{"uuid":"%s","title":%q,"publisher":%q,"link":"https://example.test/%s",
		"type":"STORY","relatedTickers":["AAPL"]}`, title, title, publisher, publisher)
}

func TestYahooNewsWarnsWhenEveryItemLacksATimestamp(t *testing.T) {
	newsServer(t,
		newsItemNoTimestamp("Apple ships something", "Reuters"),
		newsItemNoTimestamp("Analysts react", "Bloomberg"),
		newsItemNoTimestamp("Supply chain note", "WSJ"),
	)

	td, err := NewYahooNewsProvider().Fetch(context.Background(), "news", "AAPL")
	if err != nil {
		t.Fatalf("Fetch: %v — a shape change must not become a fetch error", err)
	}
	if len(td.Facts) != 0 {
		t.Fatalf("no item is usable, so none may be published: %+v", td.Facts)
	}
	if HasDomainEvidence("news", td) {
		t.Error("coverage must stay false")
	}
	w := strings.Join(td.Warnings, " | ")
	if w == "" {
		t.Fatalf("a feed of three items that all vanished reported itself as a quiet name")
	}
	if !strings.Contains(w, "3 items") {
		t.Errorf("the warning does not say how many items arrived: %s", w)
	}
	if !strings.Contains(w, "providerPublishTime") {
		t.Errorf("the warning does not name the timestamp field, which is the whole diagnosis: %s", w)
	}
	if strings.Contains(w, "older than") {
		t.Errorf("the warning blames staleness for a timestamp problem: %s", w)
	}
}

func TestYahooNewsWarnsWithTheAgeReasonWhenTheNameIsMerelyStale(t *testing.T) {
	// Same visible outcome — no facts, no coverage — but a completely different
	// cause, and the message has to separate them without further digging.
	newsServer(t,
		newsItem("last quarter's news", "Reuters", 24*40, "AAPL"),
		newsItem("older still", "Bloomberg", 24*90, "AAPL"),
	)

	td, err := NewYahooNewsProvider().Fetch(context.Background(), "news", "AAPL")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	w := strings.Join(td.Warnings, " | ")
	if !strings.Contains(w, "2 items") {
		t.Errorf("the warning does not say how many items arrived: %s", w)
	}
	if !strings.Contains(w, "older than") {
		t.Errorf("the warning does not name the age cutoff as the reason: %s", w)
	}
	if strings.Contains(w, "providerPublishTime") {
		t.Errorf("the warning blames the timestamp field for a stale name: %s", w)
	}
}

func TestYahooNewsDoesNotWarnOnAnEmptyFeedOrAHealthyOne(t *testing.T) {
	// An empty news array is a quiet name and that is not news.
	newsServer(t)
	td, err := NewYahooNewsProvider().Fetch(context.Background(), "news", "AAPL")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(td.Warnings) != 0 {
		t.Errorf("an empty feed must stay silent, got: %v", td.Warnings)
	}

	newsServer(t, newsItem("real headline", "Reuters", 3, "AAPL"))
	td, err = NewYahooNewsProvider().Fetch(context.Background(), "news", "AAPL")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(td.Warnings) != 0 {
		t.Errorf("a healthy feed must stay silent, got: %v", td.Warnings)
	}
}

func TestYahooNewsRefusesAFeedThatTagsNothingToTheTicker(t *testing.T) {
	// 2026-09-03: the search endpoint answered 035720.KS, 2454.TW, BMW.DE,
	// NESTE.HE and O39.SI with the same eight oil, Namibia and photonics
	// stories, none tagged to any of them. The provider's own comment claimed a
	// relevance filter it did not have, so all eight were printed as facts for
	// each name and the news domain spent a quarter of the score on them.
	now := time.Now().Unix()
	var items []string
	for i := 0; i < 8; i++ {
		items = append(items, fmt.Sprintf(
			`{"uuid":"u%d","title":"Oil edges down as investors weigh uncertainty %d","publisher":"Reuters","link":"https://example.com/%d","providerPublishTime":%d,"relatedTickers":["XOM","CVX"]}`,
			i, i, i, now))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"news":[%s],"finance":{"error":null}}`, strings.Join(items, ","))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)

	td, err := NewYahooNewsProvider().Fetch(context.Background(), "news", "O39.SI")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(td.Facts) != 0 {
		t.Errorf("%d untagged headlines were printed as facts:\n%+v", len(td.Facts), td.Facts)
	}
	joined := strings.Join(td.Warnings, " | ")
	if !strings.Contains(joined, "tagged none of them") {
		t.Errorf("no warning naming the failure: %q", joined)
	}
}

func TestYahooNewsKeepsAMixedFeed(t *testing.T) {
	// STLAM.MI got 21 items with 13 tagged, BBVA.MC 14 with 6. A mix is normal
	// and the untagged half is real sector context — it is only *none* tagged
	// that says the feed never resolved the symbol.
	now := time.Now().Unix()
	items := []string{
		fmt.Sprintf(`{"uuid":"a","title":"Stellantis cuts guidance","publisher":"Reuters","link":"https://example.com/a","providerPublishTime":%d,"relatedTickers":["STLAM.MI"]}`, now),
		fmt.Sprintf(`{"uuid":"b","title":"European car sales slip","publisher":"Reuters","link":"https://example.com/b","providerPublishTime":%d,"relatedTickers":["VOW3.DE"]}`, now),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"news":[%s],"finance":{"error":null}}`, strings.Join(items, ","))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)

	td, err := NewYahooNewsProvider().Fetch(context.Background(), "news", "STLAM.MI")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(td.Facts) != 2 {
		t.Fatalf("got %d facts, want both the tagged story and its sector context", len(td.Facts))
	}
	if len(td.Warnings) != 0 {
		t.Errorf("a mixed feed warned: %v", td.Warnings)
	}
}

func TestPackFlagsTwoTickersServedTheSameHeadlines(t *testing.T) {
	// The check a single ticker cannot make. Both sets look like coverage on
	// their own; together they are one fallback payload answering two queries.
	p := NewDataPack("news")
	same := []Fact{
		{Label: "Headline 1 (surfaced by search, not tagged to this ticker)", Value: "Oil edges down — Reuters"},
		{Label: "Headline 2 (surfaced by search, not tagged to this ticker)", Value: "TotalEnergies enters PEL83 — GlobeNewswire"},
	}
	p.ByTicker["O39.SI"] = TickerData{Ticker: "O39.SI", Facts: same}
	p.ByTicker["035720.KS"] = TickerData{Ticker: "035720.KS", Facts: same}
	p.ByTicker["INTC"] = TickerData{Ticker: "INTC", Facts: []Fact{
		{Label: "Headline 1 (tagged to this ticker)", Value: "Intel lands a foundry customer — Reuters"},
	}}

	flagSharedHeadlineSets(p)

	joined := strings.Join(p.Errors, " | ")
	if !strings.Contains(joined, "035720.KS, O39.SI") {
		t.Errorf("the two tickers sharing a headline set were not named: %q", joined)
	}
	if strings.Contains(joined, "INTC") {
		t.Errorf("a ticker with its own headlines was flagged: %q", joined)
	}
}
