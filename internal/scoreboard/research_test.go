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

// TestScoreboardCohortSeparatesChiefEngines pins the consequence Task 6's own
// context note calls out: the cohort key (research.go) already folds in
// meta.SynthesisModel, so fixing that value (Task 6's main fix) separates a
// claude-Chief run from an api-Chief run "for free" whenever their model
// names differ, which they do in this fixture (opus vs chief-model).
//
// The case the model name alone CANNOT resolve is the third run here: a
// historical run predating chief_engine (no chief_engine field at all) that
// happens to record the same "opus" SynthesisModel as an explicit,
// post-fix claude run. The historical value is not trustworthy provenance —
// the sep15 fixture (testdata/research-sep15.json) is a run where "opus" was
// recorded despite the DeepSeek fallback producing the accepted output, which
// is exactly the bug Task 6 fixes going forward. Pooling that fixture's kind
// of run with a confidently-labeled new "claude" run would silently attribute
// possibly-DeepSeek-answered results to a Claude cohort, so the cohort key
// must carry the engine label (falling back to "unknown" when the
// field is absent) rather than relying on SynthesisModel string equality
// alone.
func TestScoreboardCohortSeparatesChiefEngines(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 8, 3, 22, 0, 0, 0, time.UTC)
	mk := func(i int, meta model.RunMeta) *store.Run {
		ts := start.AddDate(0, 0, i)
		run := &store.Run{Dir: filepath.Join(dir, ts.Format("2006-01-02T15-04-05")), TS: ts}
		if err := os.MkdirAll(run.Dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := run.WriteIdeas(&model.IdeasResult{GeneratedAt: ts.Format(time.RFC3339), Ideas: []model.TradeIdea{}}); err != nil {
			t.Fatal(err)
		}
		if err := run.WriteMeta(meta); err != nil {
			t.Fatal(err)
		}
		return run
	}
	// Run 0: explicit claude primary, correctly recorded post-fix.
	mk(0, model.RunMeta{ChiefEngine: "claude", ChiefModel: "opus", ChiefAttempted: "claude", ChiefAccepted: "claude", SynthesisModel: "opus"})
	// Run 1: explicit api primary, a genuinely different engine and model.
	mk(1, model.RunMeta{ChiefEngine: "api", ChiefModel: "chief-model", ChiefAttempted: "api", ChiefAccepted: "api", SynthesisModel: "chief-model"})
	// Run 2: a historical run predating chief_engine (field entirely absent),
	// recording the same "opus" SynthesisModel value as run 0 by coincidence
	// (or, as in the real sep15 fixture, because a fallback silently answered
	// instead). Model-name equality with run 0 must not merge them.
	mk(2, model.RunMeta{SynthesisModel: "opus"})

	report, err := CompareResearchWithOptions(context.Background(), dir, nil, ResearchComparisonOptions{AsOf: start.AddDate(0, 1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Cohorts) != 3 {
		t.Fatalf("want 3 separate cohorts (claude, api, claude-unrecorded), got %d: %+v", len(report.Cohorts), report.Cohorts)
	}
	sawUnrecorded := false
	sawExplicitClaude := false
	for _, c := range report.Cohorts {
		if strings.Contains(c.Key, "chief:unknown") {
			sawUnrecorded = true
		}
		if strings.Contains(c.Key, "chief:claude/") {
			sawExplicitClaude = true
		}
	}
	if !sawUnrecorded {
		t.Fatalf("a run with no chief_engine field must read as \"claude (unrecorded)\", never silently merged into either new cohort: %+v", report.Cohorts)
	}
	if !sawExplicitClaude {
		t.Fatalf("an explicit chief_engine=claude run must keep its own distinct label: %+v", report.Cohorts)
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

func TestResearchDiagnosticsKeepDossierAndReviewProgressSeparate(t *testing.T) {
	meta := &model.RunMeta{ResearchOutcomes: []model.ResearchOutcome{{Transport: model.OutcomeOK, Parsing: model.OutcomeOK, Evidence: "full", Review: model.ReviewUnavailable}}, SourceDiagnostics: []model.SourceDiagnostic{{ID: "one", Reason: "unresolved_symbol"}, {ID: "one", Reason: "unresolved_symbol"}, {ID: "two", Reason: "not_applicable"}}}
	d := researchDiagnostics(store.RunSummary{Dir: t.TempDir()}, nil, nil, meta, nil)
	if d.StageProgress == nil || d.StageProgress.Researched != 1 || d.StageProgress.Reviewed != 0 || d.StageProgress.Failed != 1 {
		t.Fatalf("stage progress: %+v", d.StageProgress)
	}
	if d.SourceReasons["unresolved_symbol"] != 1 || d.SourceReasons["not_applicable"] != 1 {
		t.Fatalf("source counts duplicated: %v", d.SourceReasons)
	}
	if contractKey(meta) != "prompt:unknown/response:unknown" {
		t.Fatal("historical contract was inferred")
	}
	meta.Domains = []model.DomainStatus{{Prompt: &model.PromptProfile{Version: 2, ResponseContractVersion: 2}}}
	if contractKey(meta) != "prompt:2/response:2" {
		t.Fatal(contractKey(meta))
	}
	meta.Domains = append(meta.Domains, model.DomainStatus{})
	if contractKey(meta) != "prompt:2,unknown/response:2,unknown" {
		t.Fatal("mixed contract versions erased")
	}
}
