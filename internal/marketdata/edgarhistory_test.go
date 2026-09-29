package marketdata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// filingRow is one row of a fabricated filings page, in the parallel-array
// shape SEC serves under both filings.recent and a filings.files page.
type filingRow struct {
	form  string
	date  time.Time
	items string // "" for anything that is not an 8-K
}

func filingsPageJSON(rows []filingRow) string {
	var acc, filed, form, doc, items []string
	for i, r := range rows {
		acc = append(acc, fmt.Sprintf(`"0000320193-26-%06d"`, i))
		filed = append(filed, `"`+r.date.Format("2006-01-02")+`"`)
		form = append(form, `"`+r.form+`"`)
		doc = append(doc, fmt.Sprintf(`"doc%d.htm"`, i))
		items = append(items, `"`+r.items+`"`)
	}
	return fmt.Sprintf(`{"accessionNumber":[%s],"filingDate":[%s],"form":[%s],"primaryDocument":[%s],"items":[%s]}`,
		strings.Join(acc, ","), strings.Join(filed, ","), strings.Join(form, ","),
		strings.Join(doc, ","), strings.Join(items, ","))
}

// filingHistoryServer serves one issuer's submissions document (filings.recent
// plus a filings.files pointer to two older pages) and the ticker directory.
// It counts requests per path so a test can assert on cache reuse and on a
// page outside the window never being fetched at all.
func filingHistoryServer(t *testing.T, recent []filingRow, files map[string][]filingRow, filesRange map[string][2]time.Time) (*httptest.Server, func(path string) int) {
	t.Helper()
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		switch {
		case strings.Contains(r.URL.Path, "company_tickers.json"):
			fmt.Fprint(w, `{"0":{"cik_str":320193,"ticker":"AAPL","title":"Apple Inc."}}`)
		case strings.HasSuffix(r.URL.Path, "/submissions/CIK0000320193.json"):
			var filesArr []string
			for name, rng := range filesRange {
				filesArr = append(filesArr, fmt.Sprintf(`{"name":"%s","filingFrom":"%s","filingTo":"%s"}`,
					name, rng[0].Format("2006-01-02"), rng[1].Format("2006-01-02")))
			}
			fmt.Fprintf(w, `{"cik":"320193","name":"Apple Inc.","filings":{"recent":%s,"files":[%s]}}`,
				filingsPageJSON(recent), strings.Join(filesArr, ","))
		default:
			for name, rows := range files {
				if strings.HasSuffix(r.URL.Path, "/submissions/"+name) {
					fmt.Fprint(w, filingsPageJSON(rows))
					return
				}
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[path]
	}
}

func TestFilingHistoryParsesItem202FromRecentAndFiles(t *testing.T) {
	now := time.Now().UTC().Truncate(24 * time.Hour)
	recent := []filingRow{
		{"8-K", now.AddDate(0, 0, -5), "2.02,9.01"},  // earnings release, items in one order
		{"8-K", now.AddDate(0, 0, -10), "9.01,2.02"}, // earnings release, items reversed
		{"8-K", now.AddDate(0, 0, -12), "5.02"},      // not an earnings release
		{"8-K/A", now.AddDate(0, 0, -3), "2.02"},     // amendment: must not count as a new release
		{"10-Q", now.AddDate(0, 0, -40), ""},
		{"10-Q/A", now.AddDate(0, 0, -35), ""}, // restatement: must not count
	}
	olderEarnings := now.AddDate(-2, 0, 0)
	olderReport := now.AddDate(-2, 0, -10)
	filesPages := map[string][]filingRow{
		"CIK0000320193-submissions-001.json": {
			{"8-K", olderEarnings, "2.02"},
			{"10-K", olderReport, ""},
		},
	}
	since := now.AddDate(-3, 0, 0)
	filesRange := map[string][2]time.Time{
		"CIK0000320193-submissions-001.json": {olderEarnings.AddDate(0, -1, 0), olderReport.AddDate(0, 1, 0)},
	}
	srv, _ := filingHistoryServer(t, recent, filesPages, filesRange)
	t.Setenv("CFR_SEC_BASE", srv.URL)

	src := NewFilingHistorySource("test@example.com", NewCache(t.TempDir()))
	got, warnings := src.FilingHistory(context.Background(), []string{"AAPL"}, since)
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	h, ok := got["AAPL"]
	if !ok {
		t.Fatalf("no filing history returned for AAPL")
	}
	if len(h.Earnings) != 3 {
		t.Errorf("Earnings = %v, want 3 Item 2.02 releases (both orderings from recent, plus the one from files; the 8-K/A must be excluded)", h.Earnings)
	}
	wantEarliest := olderEarnings
	if len(h.Earnings) > 0 && !h.Earnings[0].Equal(wantEarliest) {
		t.Errorf("Earnings[0] = %v, want %v (sorted oldest first, including the files-page date)", h.Earnings[0], wantEarliest)
	}
	if len(h.Reports) != 2 {
		t.Errorf("Reports = %v, want 2 (the 10-Q and the older files-page 10-K; 10-Q/A excluded)", h.Reports)
	}
}

func TestFilingHistorySkipsAFilesPageEntirelyBeforeSince(t *testing.T) {
	now := time.Now().UTC().Truncate(24 * time.Hour)
	recent := []filingRow{{"8-K", now.AddDate(0, 0, -5), "2.02"}}
	tooOld := now.AddDate(-6, 0, 0)
	filesPages := map[string][]filingRow{
		"CIK0000320193-submissions-001.json": {{"8-K", tooOld, "2.02"}},
	}
	since := now.AddDate(-3, 0, 0)
	filesRange := map[string][2]time.Time{
		// Both bounds fall before `since`: the page cannot contain anything in
		// the window, so it must never be requested at all.
		"CIK0000320193-submissions-001.json": {tooOld.AddDate(0, -1, 0), tooOld.AddDate(0, 1, 0)},
	}
	srv, hitsFor := filingHistoryServer(t, recent, filesPages, filesRange)
	t.Setenv("CFR_SEC_BASE", srv.URL)

	src := NewFilingHistorySource("test@example.com", NewCache(t.TempDir()))
	got, warnings := src.FilingHistory(context.Background(), []string{"AAPL"}, since)
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if n := hitsFor("/submissions/CIK0000320193-submissions-001.json"); n != 0 {
		t.Errorf("fetched the out-of-window files page %d time(s), want 0", n)
	}
	if len(got["AAPL"].Earnings) != 1 {
		t.Errorf("Earnings = %v, want just the one recent release", got["AAPL"].Earnings)
	}
}

// The bug this guards: filings.files pages are immutable once published, and
// re-walking one on every backtest run defeats the point of A2's permanent
// cache. Two edgarProviders sharing one cache directory stand in for two
// separate runs; the second must make zero requests for a page the first
// already fetched.
func TestFilingsPageIsServedFromPermanentCacheOnASecondProvider(t *testing.T) {
	now := time.Now().UTC().Truncate(24 * time.Hour)
	recent := []filingRow{{"8-K", now.AddDate(0, 0, -5), "2.02"}}
	older := now.AddDate(-2, 0, 0)
	filesPages := map[string][]filingRow{
		"CIK0000320193-submissions-001.json": {{"8-K", older, "2.02"}},
	}
	since := now.AddDate(-3, 0, 0)
	filesRange := map[string][2]time.Time{
		"CIK0000320193-submissions-001.json": {older.AddDate(0, -1, 0), older.AddDate(0, 1, 0)},
	}
	srv, hitsFor := filingHistoryServer(t, recent, filesPages, filesRange)
	t.Setenv("CFR_SEC_BASE", srv.URL)

	dir := t.TempDir()
	cache := NewCache(dir)

	p1 := NewFilingHistorySource("test@example.com", cache)
	got1, _ := p1.FilingHistory(context.Background(), []string{"AAPL"}, since)
	if len(got1["AAPL"].Earnings) != 2 {
		t.Fatalf("first run: Earnings = %v, want 2", got1["AAPL"].Earnings)
	}
	firstHits := hitsFor("/submissions/CIK0000320193-submissions-001.json")
	if firstHits == 0 {
		t.Fatalf("first run never fetched the files page")
	}

	// A fresh provider (a separate process/run in practice) sharing the same
	// on-disk cache directory.
	p2 := NewFilingHistorySource("test@example.com", cache)
	got2, _ := p2.FilingHistory(context.Background(), []string{"AAPL"}, since)
	if len(got2["AAPL"].Earnings) != 2 {
		t.Fatalf("second run: Earnings = %v, want 2", got2["AAPL"].Earnings)
	}
	if n := hitsFor("/submissions/CIK0000320193-submissions-001.json") - firstHits; n != 0 {
		t.Errorf("second run made %d more request(s) for the files page, want 0 (permanent cache)", n)
	}
}

func TestFilingHistoryWarnsPerTickerWithoutFailingTheCall(t *testing.T) {
	now := time.Now().UTC().Truncate(24 * time.Hour)
	recent := []filingRow{{"8-K", now.AddDate(0, 0, -5), "2.02"}}
	srv, _ := filingHistoryServer(t, recent, nil, nil)
	t.Setenv("CFR_SEC_BASE", srv.URL)

	src := NewFilingHistorySource("test@example.com", NewCache(t.TempDir()))
	got, warnings := src.FilingHistory(context.Background(), []string{"AAPL", "ZZZZ"}, now.AddDate(-1, 0, 0))
	if _, ok := got["AAPL"]; !ok {
		t.Errorf("AAPL should still resolve when ZZZZ fails: got %v", got)
	}
	if _, ok := got["ZZZZ"]; ok {
		t.Errorf("ZZZZ has no CIK and must be absent, not zero-valued")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "ZZZZ") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings %v do not mention the failed ticker ZZZZ", warnings)
	}
}

func TestFilingHistoryIsSilentWithoutAContactEmail(t *testing.T) {
	src := NewFilingHistorySource("", NewCache(t.TempDir()))
	got, warnings := src.FilingHistory(context.Background(), []string{"AAPL"}, time.Now().AddDate(-1, 0, 0))
	if len(got) != 0 {
		t.Errorf("got %v, want nothing without a contact email", got)
	}
	if len(warnings) == 0 {
		t.Error("want a warning explaining why nothing was fetched")
	}
}
