package marketdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// calendarCSV is the shape AlphaVantage's EARNINGS_CALENDAR returns: one bulk
// CSV covering every symbol it knows, not a per-ticker query.
func calendarCSV(rows ...string) string {
	head := "symbol,name,reportDate,fiscalDateEnding,estimate,currency\n"
	return head + strings.Join(rows, "\n") + "\n"
}

// serveAVMixed answers NEWS_SENTIMENT with the JSON fixture and
// EARNINGS_CALENDAR with the CSV, counting how many of each it served.
func serveAVMixed(t *testing.T, csv string) (*httptest.Server, *int32, *int32) {
	t.Helper()
	var newsHits, calHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("function") == "EARNINGS_CALENDAR" {
			atomic.AddInt32(&calHits, 1)
			w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
			w.Write([]byte(csv))
			return
		}
		atomic.AddInt32(&newsHits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write(loadAVFixture(t))
	}))
	t.Cleanup(srv.Close)
	return srv, &newsHits, &calHits
}

func TestEarningsCalendarIsOneBulkRequestForTheWholeShortlist(t *testing.T) {
	// The date matters more than almost anything else the news domain can
	// supply, and a per-ticker endpoint would cost the whole 25/day budget. One
	// CSV covers every name, so it must be fetched once and reused.
	soon := time.Now().AddDate(0, 0, 9).Format("2006-01-02")
	later := time.Now().AddDate(0, 0, 40).Format("2006-01-02")
	past := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	srv, _, calHits := serveAVMixed(t, calendarCSV(
		"NVDA,NVIDIA Corp,"+past+",2026-07-31,1.01,USD",
		"NVDA,NVIDIA Corp,"+soon+",2026-10-31,1.20,USD",
		"NVDA,NVIDIA Corp,"+later+",2027-01-31,1.35,USD",
		"JPM,JPMorgan Chase,"+later+",2026-12-31,4.10,USD",
	))
	t.Setenv("CFR_AV_BASE", srv.URL)

	p := NewAlphaVantageProvider("testkey", t.TempDir())
	svc := NewService(nil, p)
	pack := svc.BuildPack(context.Background(), "news", []string{"NVDA", "JPM"})

	if got := atomic.LoadInt32(calHits); got != 1 {
		t.Errorf("fetched the calendar %d times for 2 tickers, want 1 bulk request", got)
	}

	want, _ := time.Parse("2006-01-02", soon)
	got, ok := pack.EventDates["NVDA"]
	if !ok {
		t.Fatalf("NVDA has no typed event date; pack has %v", pack.EventDates)
	}
	if !got.Equal(want) {
		// The next *future* report, not the first row and not the stalest.
		t.Errorf("NVDA next earnings = %s, want %s", got.Format("2006-01-02"), soon)
	}

	md := pack.Markdown()
	if !strings.Contains(md, "Next earnings") || !strings.Contains(md, soon) {
		t.Errorf("the earnings date must reach the prompt:\n%s", md)
	}
}

func TestEarningsCalendarSkipsNamesItDoesNotCover(t *testing.T) {
	srv, _, _ := serveAVMixed(t, calendarCSV("NVDA,NVIDIA Corp,"+
		time.Now().AddDate(0, 0, 5).Format("2006-01-02")+",2026-10-31,1.20,USD"))
	t.Setenv("CFR_AV_BASE", srv.URL)

	p := NewAlphaVantageProvider("testkey", t.TempDir())
	svc := NewService(nil, p)
	pack := svc.BuildPack(context.Background(), "news", []string{"NVDA", "JPM"})

	if _, ok := pack.EventDates["JPM"]; ok {
		t.Errorf("a name absent from the calendar must have no date, got %v", pack.EventDates)
	}
	if strings.Contains(pack.Markdown(), "JPM\n- **Next earnings**") {
		t.Errorf("invented an earnings date for an uncovered name:\n%s", pack.Markdown())
	}
}

func TestEarningsCalendarReachesForeignNamesThroughTheirUSLine(t *testing.T) {
	// TSMC files as TSM. Without the ADR hop a foreign listing has no earnings
	// date at all, which is exactly the name whose date matters most.
	soon := time.Now().AddDate(0, 0, 6).Format("2006-01-02")
	srv, _, _ := serveAVMixed(t, calendarCSV("TSM,Taiwan Semiconductor,"+soon+",2026-09-30,2.10,USD"))
	t.Setenv("CFR_AV_BASE", srv.URL)

	p := NewAlphaVantageProvider("testkey", t.TempDir())
	svc := NewService(nil, p)
	pack := svc.BuildPack(context.Background(), "news", []string{"2330.TW"})

	if _, ok := pack.EventDates["2330.TW"]; !ok {
		t.Errorf("2330.TW should get TSM's calendar date, pack has %v", pack.EventDates)
	}
}

func TestEarningsCalendarSurvivesAQuotaMessage(t *testing.T) {
	// AlphaVantage reports an exhausted quota as a 200 with a JSON note where
	// the CSV should be. Parsing that as a calendar would silently produce a
	// pipeline with no earnings dates and no complaint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("function") == "EARNINGS_CALENDAR" {
			w.Write([]byte(`{"Information": "rate limit reached"}`))
			return
		}
		w.Write(loadAVFixture(t))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_AV_BASE", srv.URL)

	p := NewAlphaVantageProvider("testkey", t.TempDir())
	svc := NewService(nil, p)
	pack := svc.BuildPack(context.Background(), "news", []string{"NVDA"})

	if len(pack.EventDates) != 0 {
		t.Errorf("a quota message must not become an earnings date: %v", pack.EventDates)
	}
	if len(pack.Errors) == 0 {
		t.Errorf("a failed calendar fetch must be recorded in the pack's errors")
	}
	// The news facts still arrive: one failure does not take the other with it.
	if len(pack.ByTicker["NVDA"].Facts) == 0 {
		t.Errorf("news headlines should survive a calendar failure")
	}
}
