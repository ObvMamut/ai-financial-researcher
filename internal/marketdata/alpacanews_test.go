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

// alpacaWrapItem is alpacaNewsItem with a real summary, needed to reproduce
// the F3 fixture below (alpacaNewsItem hardcodes summary to the placeholder
// "s", which every other test in this file treats as fine to ignore).
func alpacaWrapItem(headline, summary string, symbols ...string) string {
	syms := make([]string, 0, len(symbols))
	for _, s := range symbols {
		syms = append(syms, `"`+s+`"`)
	}
	return fmt.Sprintf(`{"id":1,"headline":%q,"summary":%q,"author":"a","source":"benzinga",
		"url":"https://benzinga.test/%s","symbols":[%s],"created_at":%q,"updated_at":%q}`,
		headline, summary, headline, strings.Join(syms, ","),
		time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339),
		time.Now().UTC().Format(time.RFC3339))
}

// TestAlpacaNewsWrapItemsAreNotCoverage reproduces plan finding F3
// (2026-09-24, runs/2026-09-24T12-58-48/data/news.json ByTicker["SAP.DE"]):
// four real Benzinga headlines — three premarket notes about AMD, NVIDIA and
// Micron, and one European-market-close wrap — that all named SAP.DE's
// resolved US line (SAP) in Alpaca's per-symbol query, so relatesTo alone
// called all four "tagged to this ticker" and the domain scored SAP.DE on
// them. Not one names SAP in its headline or summary; SAP appears only deep
// in the body, as one of a handful of software names an AI pullback could
// rotate into (or, for the wrap, one line in a market-summary table).
//
// news.json only persists the rendered Fact, not Alpaca's raw symbols array,
// so the lists below are reconstructed from each article's own body — every
// ticker its content actually names — rather than replayed real data that no
// longer exists. Only their length matters for this test: each easily clears
// the >3 threshold that would otherwise make a short tag list "relevant
// enough on its own" regardless of content.
func TestAlpacaNewsWrapItemsAreNotCoverage(t *testing.T) {
	alpacaNewsServer(t,
		alpacaWrapItem(
			"What Is Going on With AMD Stock on Tuesday?",
			"AMD stock surges over 2% premarket as investors shake off AI safety concerns and refocus on long-term data-center hardware demand.",
			"AMD", "INTC", "NVDA", "AVGO", "MSFT", "INTU", "SAP"),
		alpacaWrapItem(
			"NVIDIA Isn't 'so Expensive,' but Crowded AI Trade Could Unwind Quickly, Fund Manager Warns",
			"NVIDIA edges higher premarket as Wall Street weighs AI safety debates, long-term data center spending, and software stock rotation.",
			"NVDA", "MSFT", "INTU", "SAP"),
		alpacaWrapItem(
			"What's Going On With Micron Technology Stock Tuesday?",
			"Micron (MU) stock dips in premarket trading amid tech sell-off. Key support levels, technical analysis & price action breakdown.",
			"MU", "MSFT", "INTU", "SAP", "SPMO", "XNTK", "SGRT"),
		alpacaWrapItem(
			"Brent Jumps to $97, German AfD Party Wins Big: Stock Market Today",
			"Europe closed mixed as the AfD's Saxony-Anhalt landslide and $97 Brent lifted bond yields, and Wall Street sat out Labor Day.",
			"IFNNY", "ASML", "STM", "SBGSY", "LGRDY", "TTE", "SHEL", "BP", "NVS", "SAP"),
	)

	td, err := NewAlpacaNewsProvider("k", "s").Fetch(context.Background(), "news", "SAP.DE")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(td.Facts) != 0 {
		t.Fatalf("wrap items that never name SAP were published as coverage: %+v", td.Facts)
	}
	if HasDomainEvidence("news", td) {
		t.Error("four items that merely tag SAP must not ground the news domain — this is exactly what carried SAP.DE past the evidence floor in F3")
	}
	if w := strings.Join(td.Warnings, " | "); !strings.Contains(w, "tagged none of them") {
		t.Errorf("no warning explaining why the feed produced no facts: %q", w)
	}
}

// TestAlpacaNewsWrapItemsStayUncoveredEvenWithCompanyNameWired checks the
// same fixture through the path a real run actually takes: the orchestrator
// wires WithCompanyNames from the universe's own name for every ticker. SAP's
// company name normalizes to "SAP" — the same string as its root — so this
// must reach the identical answer, not a different one that happens to also
// be correct only because the lookup was absent.
func TestAlpacaNewsWrapItemsStayUncoveredEvenWithCompanyNameWired(t *testing.T) {
	alpacaNewsServer(t,
		alpacaWrapItem("What Is Going on With AMD Stock on Tuesday?",
			"AMD stock surges over 2% premarket as investors shake off AI safety concerns.",
			"AMD", "INTC", "NVDA", "AVGO", "MSFT", "INTU", "SAP"),
		alpacaWrapItem("Brent Jumps to $97, German AfD Party Wins Big: Stock Market Today",
			"Europe closed mixed as the AfD's Saxony-Anhalt landslide and $97 Brent lifted bond yields.",
			"IFNNY", "ASML", "STM", "SBGSY", "LGRDY", "TTE", "SHEL", "BP", "NVS", "SAP"),
	)
	ctx := WithCompanyNames(context.Background(), func(ticker string) []string {
		if ticker == "SAP.DE" {
			return []string{"SAP SE"}
		}
		return nil
	})
	td, err := NewAlpacaNewsProvider("k", "s").Fetch(ctx, "news", "SAP.DE")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(td.Facts) != 0 {
		t.Fatalf("wired company name let wrap items through as coverage: %+v", td.Facts)
	}
	if HasDomainEvidence("news", td) {
		t.Error("must stay uncovered with the real orchestrator wiring in place")
	}
}

// TestAlpacaNewsGenuineCompanyStoryIsCoverage is the positive control: the
// same rule that excludes the wrap items above must still admit a story that
// actually is about the company.
func TestAlpacaNewsGenuineCompanyStoryIsCoverage(t *testing.T) {
	alpacaNewsServer(t, alpacaWrapItem(
		"SAP SE Raises Full-Year Cloud Revenue Guidance",
		"SAP lifted its outlook after strong cloud bookings in the latest quarter.",
		"SAP"))

	td, err := NewAlpacaNewsProvider("k", "s").Fetch(context.Background(), "news", "SAP.DE")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(td.Facts) != 1 {
		t.Fatalf("got %d facts, want 1: %+v", len(td.Facts), td.Facts)
	}
	if !HasDomainEvidence("news", td) {
		t.Error("a headline naming the company must ground the news domain")
	}
}
