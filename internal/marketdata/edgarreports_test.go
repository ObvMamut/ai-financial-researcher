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

const dailyIndexHeader = `Description:           Daily Index of EDGAR Dissemination Feed by Form Type
Last Data Received:    Sep 3, 2026
Comments:              webmaster@sec.gov

Form Type   Company Name                                                  CIK         Date Filed  File Name
---------------------------------------------------------------------------------------------------------
`

func idxRow(form, company, cik, date string) string {
	return fmt.Sprintf("%-16s %-60s %-11s %-11s edgar/data/%s/0000-26-1.txt\n", form, company, cik, date, cik)
}

func TestParseReportFilersReadsColumnsFromTheEnd(t *testing.T) {
	// The file is fixed-width and company names contain spaces, so indexing
	// forward past the form type puts a two-word company's filing under a
	// fragment of its own name instead of under its CIK.
	body := dailyIndexHeader +
		idxRow("10-Q", "American Outdoor Brands, Inc.", "1808997", "20260903") +
		idxRow("10-K", "CIENA CORP", "936395", "20260903")

	got := parseReportFilers(body)
	want := []string{"0001808997", "0000936395"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseReportFilersRejectsAmendmentsAndLateNotices(t *testing.T) {
	// A 10-Q/A restates a report already counted, and moving the date to the
	// restatement would put the drift window on the wrong day. "NT 10-Q" is
	// notice that the report is *late* — the opposite of a report — and its
	// first field is "NT", which is why an exact match on the whole form type
	// is the test rather than a prefix.
	body := dailyIndexHeader +
		idxRow("10-Q/A", "Restater Inc.", "111", "20260903") +
		idxRow("NT 10-Q", "Late Filer Corp", "222", "20260903") +
		idxRow("10-KT", "Transition Period Co", "333", "20260903") +
		idxRow("10-Q", "Real Filer Inc.", "444", "20260903")

	got := parseReportFilers(body)
	if len(got) != 1 || got[0] != "0000000444" {
		t.Fatalf("got %v, want only the plain 10-Q filer 0000000444", got)
	}
}

func TestParseReportFilersDeduplicatesOneFiler(t *testing.T) {
	body := dailyIndexHeader +
		idxRow("10-Q", "Twice Inc.", "555", "20260903") +
		idxRow("10-K", "Twice Inc.", "555", "20260903")
	if got := parseReportFilers(body); len(got) != 1 {
		t.Fatalf("got %v, want one entry for one filer", got)
	}
}

// secReportServer serves daily indexes keyed by date, plus the ticker
// directory the CIK map is built from.
func secReportServer(t *testing.T, byDate map[string]string) (*int, func()) {
	t.Helper()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "company_tickers.json") {
			fmt.Fprint(w, `{"0":{"cik_str":1808997,"ticker":"AOUT","title":"American Outdoor"},
				"1":{"cik_str":936395,"ticker":"CIEN","title":"Ciena"}}`)
			return
		}
		hits++
		for date, body := range byDate {
			if strings.Contains(r.URL.Path, "form."+date+".idx") {
				fmt.Fprint(w, body)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Setenv("CFR_SEC_BASE", srv.URL)
	return &hits, srv.Close
}

func TestReportDatesKeepsTheMostRecentFiling(t *testing.T) {
	// Walking forward means a later filing overwrites an earlier one, so a
	// filer that reported twice inside the window ends on the newer date — the
	// one the drift clock has to run from.
	now := time.Now().UTC().Truncate(24 * time.Hour)
	older := lastWeekday(now.AddDate(0, 0, -6))
	newer := lastWeekday(now.AddDate(0, 0, -2))
	hits, closeSrv := secReportServer(t, map[string]string{
		older.Format("20060102"): dailyIndexHeader + idxRow("10-Q", "American Outdoor", "1808997", older.Format("20060102")),
		newer.Format("20060102"): dailyIndexHeader + idxRow("10-Q", "American Outdoor", "1808997", newer.Format("20060102")),
	})
	defer closeSrv()

	src := NewEdgarReportDates("test@example.com", NewCache(t.TempDir()))
	got := src.ReportDates(context.Background(), []string{"AOUT", "CIEN"}, now.AddDate(0, 0, -10))

	if d, ok := got["AOUT"]; !ok || !d.Equal(newer) {
		t.Errorf("AOUT = %v (present %v), want the later filing %v", d, ok, newer)
	}
	if _, ok := got["CIEN"]; ok {
		t.Error("CIEN filed nothing in the window but carries a date")
	}
	if *hits == 0 {
		t.Error("no daily index was fetched")
	}
}

func TestReportDatesIsSilentWithoutAContactEmail(t *testing.T) {
	// Every EDGAR leg follows the same rule: SEC requires an identified caller,
	// so an unconfigured contact disables the source rather than fetching
	// anonymously.
	src := NewEdgarReportDates("", NewCache(t.TempDir()))
	if got := src.ReportDates(context.Background(), []string{"AOUT"}, time.Now().AddDate(0, 0, -10)); len(got) != 0 {
		t.Errorf("got %v, want nothing without a contact email", got)
	}
}

func TestReportDatesSurvivesAFailedDay(t *testing.T) {
	// The drift leg is additive. A session whose index cannot be fetched costs
	// that session's filers and nothing else — it must never take the
	// pre-screen down with it.
	now := time.Now().UTC().Truncate(24 * time.Hour)
	good := lastWeekday(now.AddDate(0, 0, -2))
	_, closeSrv := secReportServer(t, map[string]string{
		good.Format("20060102"): dailyIndexHeader + idxRow("10-Q", "American Outdoor", "1808997", good.Format("20060102")),
	})
	defer closeSrv()

	src := NewEdgarReportDates("test@example.com", NewCache(t.TempDir()))
	got := src.ReportDates(context.Background(), []string{"AOUT"}, now.AddDate(0, 0, -10))
	if d, ok := got["AOUT"]; !ok || !d.Equal(good) {
		t.Errorf("AOUT = %v (present %v), want %v — every other day 404s", d, ok, good)
	}
}

func TestReportDatesOnceWalksTheIndexOnce(t *testing.T) {
	now := time.Now().UTC().Truncate(24 * time.Hour)
	day := lastWeekday(now.AddDate(0, 0, -2))
	hits, closeSrv := secReportServer(t, map[string]string{
		day.Format("20060102"): dailyIndexHeader + idxRow("10-Q", "American Outdoor", "1808997", day.Format("20060102")),
	})
	defer closeSrv()

	src := NewReportDatesOnce(NewEdgarReportDates("test@example.com", NewCache(t.TempDir())))
	first := src.ReportDates(context.Background(), []string{"AOUT"}, now.AddDate(0, 0, -10))
	after := *hits
	second := src.ReportDates(context.Background(), []string{"AOUT"}, now.AddDate(0, 0, -10))

	if len(first) != len(second) {
		t.Errorf("second call returned %d entries, first returned %d", len(second), len(first))
	}
	if *hits != after {
		t.Errorf("the second call fetched %d more index files, want 0", *hits-after)
	}
}

// The bug this test guards: Cache.Get/Set hash today's UTC date into the key,
// so an entry written for a closed day is a miss again the moment the
// calendar date changes — every new day re-walks every daily index the run
// before it already parsed, at ~1MB apiece. reportFilersOn now goes through
// Cache.GetPermanent/SetPermanent instead, which carries no date. Two
// edgarProviders sharing one cache directory but built with Caches on
// different injected clocks stand in for two runs on two different real
// days; the second one must make zero HTTP requests for a day the first one
// already fetched.
func TestReportFilersOnCachesPermanentlyAcrossADayRollover(t *testing.T) {
	day := lastWeekday(time.Now().UTC().AddDate(0, 0, -2))
	hits, closeSrv := secReportServer(t, map[string]string{
		day.Format("20060102"): dailyIndexHeader + idxRow("10-Q", "American Outdoor", "1808997", day.Format("20060102")),
	})
	defer closeSrv()

	dir := t.TempDir()
	cache1 := NewCache(dir)
	cache1.now = func() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) }
	p1 := NewEdgarProvider("test@example.com", cache1).(*edgarProvider)
	got1 := p1.reportFilersOn(context.Background(), day)
	if len(got1) != 1 || got1[0] != "0001808997" {
		t.Fatalf("first run: got %v, want [0001808997]", got1)
	}
	if *hits != 1 {
		t.Fatalf("first run made %d HTTP requests, want 1", *hits)
	}

	cache2 := NewCache(dir)
	cache2.now = func() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) }
	p2 := NewEdgarProvider("test@example.com", cache2).(*edgarProvider)
	got2 := p2.reportFilersOn(context.Background(), day)
	if len(got2) != 1 || got2[0] != "0001808997" {
		t.Fatalf("second run: got %v, want [0001808997]", got2)
	}
	if *hits != 1 {
		t.Errorf("second run (a different day) made %d more HTTP requests, want 0", *hits-1)
	}
}

// lastWeekday walks back to the nearest weekday, since the index has no weekend
// files and the walk skips them.
func lastWeekday(d time.Time) time.Time {
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}
