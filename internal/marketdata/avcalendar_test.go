package marketdata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// writeLimiterState pre-seeds the AlphaVantage limiter's persisted daily count,
// exactly as .data/limiter-alphavantage.json stood on the morning of an
// incident — the fixture NewAlphaVantageProvider's NewPersistentLimiter loads
// on construction. limiterState is defined in limiter.go; reused rather than
// duplicated so the fixture cannot drift from the real on-disk shape.
func writeLimiterState(t *testing.T, dir string, dailyCount int) {
	t.Helper()
	data, err := json.Marshal(limiterState{DailyCount: dailyCount, LastReset: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "limiter-alphavantage.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readLimiterDailyCount(t *testing.T, dir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "limiter-alphavantage.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st limiterState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	return st.DailyCount
}

// TestCalendarStillLoadsWhenNewsHasAlreadySpentTheOrdinaryBudget is the fix for
// F1 (2026-09-24): every one of that day's 8 runs spent the AlphaVantage key's
// whole 25-request daily budget on per-ticker news before the calendar's one
// bulk request ever got a turn, so all 8 shipped with no verified earnings
// calendar at all. NewAlphaVantageProvider now reserves one of the 25 for the
// calendar (Limiter.Reserve + WaitReserved), so it must still load here with
// only that one slot left.
func TestCalendarStillLoadsWhenNewsHasAlreadySpentTheOrdinaryBudget(t *testing.T) {
	soon := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	srv, _, calHits := serveAVMixed(t, calendarCSV("AAPL,Apple Inc,"+soon+",2026-10-31,1.20,USD"))
	t.Setenv("CFR_AV_BASE", srv.URL)

	dataDir := t.TempDir()
	// 24 of today's 25 already spent on per-ticker news, one shy of the daily
	// cap — the exact shape of the 2026-09-24 incident.
	writeLimiterState(t, dataDir, 24)

	p := NewAlphaVantageProvider("testkey", dataDir)
	svc := NewService(nil, p)
	pack := svc.BuildPack(context.Background(), "news", []string{"AAPL"})

	want, _ := time.Parse("2006-01-02", soon)
	got, ok := pack.EventDates["AAPL"]
	if !ok {
		t.Fatalf("the calendar did not load with 24 of 25 requests already spent; EventDates=%v errors=%v",
			pack.EventDates, pack.Errors)
	}
	if !got.Equal(want) {
		t.Errorf("AAPL next earnings = %s, want %s", got.Format("2006-01-02"), soon)
	}
	if gotHits := atomic.LoadInt32(calHits); gotHits != 1 {
		t.Errorf("fetched the calendar %d times, want exactly 1", gotHits)
	}
	// The reservation is exactly one slot: this fetch must spend the day's
	// last request, not find one spare.
	if used := readLimiterDailyCount(t, dataDir); used != 25 {
		t.Errorf("daily count after the calendar's fetch = %d, want the full 25 (the reserved slot spent)", used)
	}
}

// TestCalendarFailsHonestlyWhenTheReservedSlotIsAlsoGone proves the reservation
// is a priority, not a 26th free request: once the whole day's budget — the
// reserved slot included — is genuinely spent, the calendar must still report
// the same honest failure it always did, not silently produce no date and no
// complaint.
func TestCalendarFailsHonestlyWhenTheReservedSlotIsAlsoGone(t *testing.T) {
	soon := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	srv, _, calHits := serveAVMixed(t, calendarCSV("AAPL,Apple Inc,"+soon+",2026-10-31,1.20,USD"))
	t.Setenv("CFR_AV_BASE", srv.URL)

	dataDir := t.TempDir()
	writeLimiterState(t, dataDir, 25) // the whole day, reserve included, already gone

	p := NewAlphaVantageProvider("testkey", dataDir)
	svc := NewService(nil, p)
	pack := svc.BuildPack(context.Background(), "news", []string{"AAPL"})

	if _, ok := pack.EventDates["AAPL"]; ok {
		t.Errorf("a fully exhausted budget must not produce a date: %v", pack.EventDates)
	}
	if len(pack.Errors) == 0 {
		t.Error("a fully exhausted daily budget must still be reported, not swallowed")
	}
	if gotHits := atomic.LoadInt32(calHits); gotHits != 0 {
		t.Errorf("fetched the calendar %d times against an exhausted budget, want 0", gotHits)
	}
}

// TestCalendarSurvivesForASecondRunTheSameDay is the acceptance test from the
// brief: two runs back to back on one UTC day must both carry EventDates. The
// second run starts from a persisted budget the first run fully spent, and
// must get its date from the per-day cache rather than needing a request of
// its own — proving the reservation and the cache protect each other's runs.
func TestCalendarSurvivesForASecondRunTheSameDay(t *testing.T) {
	soon := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	srv, _, calHits := serveAVMixed(t, calendarCSV("AAPL,Apple Inc,"+soon+",2026-10-31,1.20,USD"))
	t.Setenv("CFR_AV_BASE", srv.URL)

	dataDir := t.TempDir()
	writeLimiterState(t, dataDir, 24)
	want, _ := time.Parse("2006-01-02", soon)

	first := NewAlphaVantageProvider("testkey", dataDir)
	firstPack := NewService(nil, first).BuildPack(context.Background(), "news", []string{"AAPL"})
	if got, ok := firstPack.EventDates["AAPL"]; !ok || !got.Equal(want) {
		t.Fatalf("setup: first run did not carry EventDates: %v errors=%v", firstPack.EventDates, firstPack.Errors)
	}

	// A brand new provider — a second process — over the same data dir on the
	// same UTC day: the persisted budget is now 25/25, fully spent.
	second := NewAlphaVantageProvider("testkey", dataDir)
	secondPack := NewService(nil, second).BuildPack(context.Background(), "news", []string{"AAPL"})
	got, ok := secondPack.EventDates["AAPL"]
	if !ok {
		t.Fatalf("second run on the same day did not carry EventDates: %v errors=%v", secondPack.EventDates, secondPack.Errors)
	}
	if !got.Equal(want) {
		t.Errorf("second run's AAPL next earnings = %s, want %s", got.Format("2006-01-02"), soon)
	}
	if gotHits := atomic.LoadInt32(calHits); gotHits != 1 {
		t.Errorf("fetched the calendar %d times across two runs, want exactly 1 — the second must hit the cache and spend nothing", gotHits)
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
