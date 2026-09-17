package scoreboard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

func TestThesisLateFillCannotExtendPastAbsoluteExpiry(t *testing.T) {
	idea := buy(100, 80, 140)
	idea.ResearchMode = "thesis"
	idea.TimeframeDays = 15
	idea.PriceAtGeneration = 102
	idea.Thesis = &model.ThesisPlan{EntryExpiresOn: "2026-09-09", ExpiresOn: "2026-09-11"}
	s := &quant.Series{Symbol: "AAA", Bars: []quant.Bar{
		bar("2026-09-07", 103, 104, 102, 103), bar("2026-09-08", 103, 104, 102, 103),
		bar("2026-09-09", 101, 103, 99, 102), bar("2026-09-10", 102, 104, 101, 103),
		bar("2026-09-11", 103, 106, 102, 105), bar("2026-09-14", 105, 150, 104, 145),
	}}
	cache := &seriesCache{bySymbol: map[string]*quant.Series{"AAA": s}, byRun: map[string]*quant.Series{}}
	e := replayIdea(context.Background(), runSummary("test"), "2026-09-06T12:00:00Z", idea, cache, 3)
	if e.Outcome != OutcomeExpired || e.ExitDate != "2026-09-11" || e.ExitPrice != 105 {
		t.Fatalf("late fill extended thesis: %+v", e)
	}
	if !e.TradeDone || e.TradePnLPct != 5 {
		t.Fatalf("trade horizon ignored absolute expiry: %+v", e)
	}
}

func TestResearchComparisonRetainsEmptyRunsAndRequiresBenchmark(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 8, 3, 22, 0, 0, 0, time.UTC)
	var selected *store.Run
	for i := 0; i < 2; i++ {
		ts := start.AddDate(0, 0, i)
		run := &store.Run{Dir: filepath.Join(dir, ts.Format("2006-01-02T15-04-05")), TS: ts}
		if err := os.MkdirAll(run.Dir, 0700); err != nil {
			t.Fatal(err)
		}
		ideas := []model.TradeIdea{}
		if i == 0 {
			selected = run
			ideas = append(ideas, model.TradeIdea{Ticker: "AAA", Index: "sp500", Direction: model.DirectionBuy, PriceAtGeneration: 100, Status: "conditional"})
		}
		if err := run.WriteIdeas(&model.IdeasResult{GeneratedAt: ts.Format(time.RFC3339), ResearchMode: "thesis", SchemaVersion: 2, Ideas: ideas}); err != nil {
			t.Fatal(err)
		}
	}
	series := &quant.Series{Symbol: "AAA"}
	for i := 0; i < 24; i++ {
		series.Bars = append(series.Bars, bar(start.AddDate(0, 0, i).Format("2006-01-02"), 100, 101, 99, 100))
	}
	if err := selected.WritePrices("AAA", series); err != nil {
		t.Fatal(err)
	}
	for _, haveBenchmark := range []bool{false, true} {
		if haveBenchmark {
			if err := selected.WritePrices("^GSPC", series); err != nil {
				t.Fatal(err)
			}
		}
		report, err := CompareResearchWithOptions(context.Background(), dir, nil, ResearchComparisonOptions{AsOf: start.AddDate(0, 1, 0)})
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Cohorts) != 1 || report.Cohorts[0].Runs != 2 || report.Cohorts[0].EmptyRuns != 1 || report.Cohorts[0].ConditionalPlans != 1 {
			t.Fatalf("lost no-trade run: %+v", report)
		}
		for _, arm := range []string{"shipped/10", "shipped/15"} {
			a := report.Cohorts[0].Arms[arm]
			if (!haveBenchmark && (a.Unmeasurable != 1 || a.Record.N != 0)) || (haveBenchmark && (a.Unmeasurable != 0 || a.Record.N != 1)) {
				t.Fatalf("benchmark present=%v arm=%+v", haveBenchmark, a)
			}
		}
	}
}
func TestThesisEntryExpirySurvivesMissingHolidayBar(t *testing.T) {
	idea := buy(100, 80, 140)
	idea.ResearchMode = "thesis"
	idea.PriceAtGeneration = 103
	idea.Thesis = &model.ThesisPlan{EntryExpiresOn: "2026-09-08", ExpiresOn: "2026-09-20"}
	s := &quant.Series{Symbol: "AAA", Bars: []quant.Bar{bar("2026-09-07", 103, 104, 102, 103), bar("2026-09-09", 101, 103, 99, 102)}}
	cache := &seriesCache{bySymbol: map[string]*quant.Series{"AAA": s}, byRun: map[string]*quant.Series{}}
	e := replayIdea(context.Background(), runSummary("test"), "2026-09-06T12:00:00Z", idea, cache, 3)
	if e.Outcome != OutcomeUnfilled {
		t.Fatalf("filled after expiry: %+v", e)
	}
}
func TestThesisCalibrationDoesNotContaminateLegacy(t *testing.T) {
	legacy := Entry{Ticker: "A", Direction: "BUY", GeneratedAt: "2026-08-01T12:00:00Z", Outcome: OutcomeExpired, PnLPct: 1, RiskAdjPnL: 1}
	thesis := legacy
	thesis.ResearchMode = "thesis"
	thesis.PnLPct = 90
	thesis.RiskAdjPnL = 90
	kept, n := Dedupe([]Entry{legacy, thesis}, 7)
	if len(kept) != 2 || n != 0 {
		t.Fatal("cross-engine observations deduplicated")
	}
	sum := &Summary{Replay: true, Entries: []Entry{legacy, thesis}}
	sum.aggregate()
	cal := Calibrate(sum)
	if cal.NClosed != 1 || cal.AvgPnLPct != 1 {
		t.Fatalf("mixed calibration: %+v", cal)
	}
	if sum.ByConfidence["thesis (unscored)"].N != 1 {
		t.Fatal("thesis reported as legacy confidence")
	}
}

func TestResearchTextDoesNotPresentUnmeasuredReturnsAsZero(t *testing.T) {
	for _, arm := range []ControlArm{{Pending: 1}, {Unmeasurable: 1}, {}} {
		report := ResearchComparison{Cohorts: []ResearchCohort{{Arms: map[string]ControlArm{"shipped/10": arm}}}}
		text := report.FormatText()
		if strings.Contains(text, "return +0.00%") || !strings.Contains(text, "return unavailable") {
			t.Fatalf("unmeasured return displayed as a result: %s", text)
		}
	}
	arm := ControlArm{}
	arm.Record.N = 1
	report := ResearchComparison{Cohorts: []ResearchCohort{{Arms: map[string]ControlArm{"shipped/10": arm}}}}
	if !strings.Contains(report.FormatText(), "return +0.00%") {
		t.Fatal("measured zero return was hidden")
	}
}
