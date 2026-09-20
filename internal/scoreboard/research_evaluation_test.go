package scoreboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

func evaluationRun(t *testing.T, root, name, mode string, at time.Time, ideas []model.TradeIdea) *store.Run {
	t.Helper()
	r := &store.Run{Dir: filepath.Join(root, name), TS: at}
	if err := os.MkdirAll(r.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteIdeas(&model.IdeasResult{ResearchMode: mode, Mode: "independent", GeneratedAt: at.Format(time.RFC3339), Ideas: ideas}); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteMeta(model.RunMeta{ResearchMode: mode, Mode: "independent", GeneratedAt: at.Format(time.RFC3339), Outcome: "complete", Indices: []string{"sp500"}}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResearchEvaluationRetainsFailedAndMalformedRuns(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	good := evaluationRun(t, dir, "empty", "thesis", now, []model.TradeIdea{})
	missing := evaluationRun(t, dir, "missing", "thesis", now, []model.TradeIdea{})
	bad := evaluationRun(t, dir, "bad", "legacy", now, []model.TradeIdea{})
	if err := os.Remove(filepath.Join(missing.Dir, "ideas.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad.Dir, "ideas.json"), []byte(`{"ideas":"wrong type"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := good.WriteMeta(model.RunMeta{ResearchMode: "thesis", Outcome: "degraded", Domains: []model.DomainStatus{{Status: model.StatusFailed, Attempts: 2, Tokens: 20, Payload: "invalid"}}, DataErrors: []string{"one", "one", "two"}}); err != nil {
		t.Fatal(err)
	}
	rep, err := CompareResearch(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Runs) != 3 {
		t.Fatalf("lost failed run: %+v", rep)
	}
	results := map[string]ResearchRunDiagnostics{}
	for _, r := range rep.Runs {
		results[r.Run] = r
	}
	if results["missing"].Result != "missing" || results["bad"].Result != "invalid" || !results["empty"].Empty {
		t.Fatalf("result accounting: %+v", results)
	}
	d := results["empty"]
	if d.FailedCalls != 1 || d.InvalidPayloads != 1 || d.DistinctDataErrors != 2 || d.Usage.CompletionTokens != 20 || d.Usage.CompleteAttempts != 0 || d.Usage.Attempts != 2 {
		t.Fatalf("diagnostics: %+v", d)
	}
}

func TestResearchUsageDoesNotAddReasoningOrCacheSubsets(t *testing.T) {
	prompt, completion, total, hit, reasoning := 10, 20, 30, 8, 15
	u := ResearchUsage{}
	a := model.TokenUsage{PromptTokens: &prompt, CompletionTokens: &completion, TotalTokens: &total, CacheHitTokens: &hit}
	a.CompletionDetails = &struct {
		ReasoningTokens *int `json:"reasoning_tokens,omitempty"`
	}{&reasoning}
	u.add(model.DomainStatus{Attempts: 2, Usage: []model.TokenUsage{a, {}}})
	if u.TotalTokens != 30 || u.PromptTokens != 10 || u.CompletionTokens != 20 || u.CompleteAttempts != 1 || u.Attempts != 2 {
		t.Fatalf("double-counted subsets or unknown attempt: %+v", u)
	}
}

// TestIncompleteUsageStaysALowerBoundNeverAMeasuredZero mixes a Claude CLI
// report whose usage could not be parsed (plain-text stdout outside the
// print-mode JSON envelope leaves every count unknown, decodeClaudeOutput's
// zero-value TokenUsage) with one API report carrying real, complete usage.
// The aggregate must: (1) let the unparsed call contribute exactly 0 to every
// token total, never an estimate; (2) still mark itself incomplete — a
// lower bound, not a measured total — via CompleteAttempts trailing Attempts;
// and (3) never derive a dollar figure anywhere.
func TestIncompleteUsageStaysALowerBoundNeverAMeasuredZero(t *testing.T) {
	u := ResearchUsage{}
	// The Claude CLI call: one attempt, usage entirely unknown.
	u.add(model.DomainStatus{Status: model.StatusDone, Attempts: 1, Usage: []model.TokenUsage{{}}})
	// The API call: one attempt, real usage.
	prompt, completion, total := 100, 50, 150
	u.add(model.DomainStatus{Status: model.StatusDone, Attempts: 1, Usage: []model.TokenUsage{{PromptTokens: &prompt, CompletionTokens: &completion, TotalTokens: &total}}})

	if u.Attempts != 2 {
		t.Fatalf("Attempts = %d, want 2", u.Attempts)
	}
	if u.CompleteAttempts != 1 {
		t.Fatalf("CompleteAttempts = %d, want 1 — the unparsed attempt must not count as complete", u.CompleteAttempts)
	}
	if u.IncompleteAttempts != 1 {
		t.Fatalf("IncompleteAttempts = %d, want 1 — the aggregate must mark itself a lower bound, never a measured total", u.IncompleteAttempts)
	}
	if u.TotalTokens != total {
		t.Fatalf("TotalTokens = %d, want %d — the unparsed call must contribute 0, never an estimate", u.TotalTokens, total)
	}
	if u.PromptTokens != prompt || u.CompletionTokens != completion {
		t.Fatalf("the unparsed call polluted prompt/completion totals: %+v", u)
	}

	// No dollar figure is derived anywhere: the aggregate carries no cost
	// field, and nothing here turns a token count into a price.
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(b))
	for _, forbidden := range []string{"cost", "dollar", "usd", "price", "$"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("usage aggregate must never derive a dollar figure: %s", b)
		}
	}
}

// TestNotAttemptedOutcomeCountsAsFailureNeverAsDeferred pins ruling R27: a
// zero-attempt capacity refusal (model.OutcomeNotAttempted) is a company that
// failed, not one whose research was deferred by policy. model.OutcomeNotRun
// is a different fact — an event-window block, where research was never
// *intended* — and both consumers already special-case it into Deferred.
// Reusing OutcomeNotRun for a capacity refusal was measured against the real
// September 15 run to move 3 failures out of the failure count and into
// deferrals, silently making an overflowing run look healthy. Verify by
// mutation: swapping OutcomeNotAttempted for OutcomeNotRun in the fixture
// below must fail this test, and only this test.
func TestNotAttemptedOutcomeCountsAsFailureNeverAsDeferred(t *testing.T) {
	outcomes := []model.ResearchOutcome{
		{Ticker: "OKTA", Transport: model.OutcomeNotAttempted, Parsing: model.OutcomeNotAttempted, Evidence: model.EvidenceDocuments, Review: model.ReviewUnavailable},
	}

	summary := model.SummarizeResearch(outcomes, nil)
	if summary.Deferred != 0 {
		t.Errorf("Deferred = %d, want 0 — a capacity refusal is not a policy deferral", summary.Deferred)
	}
	if summary.Failed != 1 {
		t.Errorf("Failed = %d, want 1 — the company still failed", summary.Failed)
	}
	if summary.Researched != 0 {
		t.Errorf("Researched = %d, want 0 — no readable dossier was produced", summary.Researched)
	}

	dir := t.TempDir()
	now := time.Date(2026, 9, 15, 17, 0, 0, 0, time.UTC)
	run := evaluationRun(t, dir, "capacity", "thesis", now, nil)
	if err := run.WriteMeta(model.RunMeta{ResearchMode: "thesis", Outcome: "degraded", ResearchOutcomes: outcomes}); err != nil {
		t.Fatal(err)
	}
	rep, err := CompareResearch(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Runs) != 1 {
		t.Fatalf("lost run: %+v", rep)
	}
	d := rep.Runs[0]
	if d.DeferredCompanies != 0 {
		t.Errorf("DeferredCompanies = %d, want 0", d.DeferredCompanies)
	}
	if d.FailedResearch != 1 {
		t.Errorf("FailedResearch = %d, want 1", d.FailedResearch)
	}
	if d.AttemptedCompanies != 1 {
		t.Errorf("AttemptedCompanies = %d, want 1 — research was intended, not deferred", d.AttemptedCompanies)
	}
}

func pairFixture(t *testing.T) (string, string, *store.Run, *store.Run) {
	t.Helper()
	dir := t.TempDir()
	at := time.Date(2026, 8, 3, 22, 0, 0, 0, time.UTC)
	idea := model.TradeIdea{Ticker: "AAA", Index: "sp500", Direction: model.DirectionBuy, PriceAtGeneration: 100}
	l := evaluationRun(t, dir, "legacy", "legacy", at, []model.TradeIdea{idea})
	r := evaluationRun(t, dir, "thesis", "thesis", at.Add(time.Minute), []model.TradeIdea{idea})
	stock, bench := &quant.Series{Symbol: "AAA"}, &quant.Series{Symbol: "^GSPC"}
	for day, n := at, 0; n < 18; day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		date := day.Format("2006-01-02")
		stock.Bars = append(stock.Bars, bar(date, 100, 102, 99, 100+float64(n)))
		bench.Bars = append(bench.Bars, bar(date, 100, 100, 100, 100))
		n++
	}
	for _, run := range []*store.Run{l, r} {
		if err := run.WritePrices("AAA", stock); err != nil {
			t.Fatal(err)
		}
		if err := run.WritePrices("^GSPC", bench); err != nil {
			t.Fatal(err)
		}
		if err := run.WriteDataPack("macro", map[string]string{"source": "shared fixture evidence"}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(l.Dir, "data", "macro.json"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	spec := ResearchPairSpec{ID: "pair-1", RegisteredAt: at.Add(-time.Hour).Format(time.RFC3339), LegacyRun: "legacy", ThesisRun: "thesis", MaxSkewSeconds: 60, SnapshotSHA256: map[string]string{"data/macro.json": hex.EncodeToString(hash[:])}}
	b, err := json.Marshal(map[string]any{"pairs": []ResearchPairSpec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(t.TempDir(), "pairs.json")
	if err := os.WriteFile(manifest, b, 0600); err != nil {
		t.Fatal(err)
	}
	return dir, manifest, l, r
}

func TestResearchPairAuditChecksSnapshotsAndMaturity(t *testing.T) {
	dir, manifest, _, r := pairFixture(t)
	opts := ResearchComparisonOptions{PairManifest: manifest, AsOf: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	rep, err := CompareResearchWithOptions(context.Background(), dir, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Pairs) != 1 || rep.Pairs[0].Status != "matched_declared_inputs" {
		t.Fatalf("pair not matched: %+v", rep.Pairs)
	}
	for _, h := range []string{"shipped/10", "shipped/15"} {
		d := rep.Pairs[0].Deltas[h]
		if d.State != "measured" || d.ExcessDifference == nil || *d.ExcessDifference != 0 {
			t.Fatalf("invalid delta: %+v", d)
		}
	}
	if err := r.WriteDataPack("macro", map[string]string{"source": "different evidence"}); err != nil {
		t.Fatal(err)
	}
	rep, err = CompareResearchWithOptions(context.Background(), dir, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pairs[0].Status != "mismatch" || len(rep.Pairs[0].Deltas) != 0 {
		t.Fatalf("mismatched evidence compared: %+v", rep.Pairs[0])
	}
}

func TestResearchPairKeepsEmptyAndUnavailableOutcomes(t *testing.T) {
	for _, empty := range []bool{true, false} {
		dir, manifest, _, r := pairFixture(t)
		if empty {
			if err := r.WriteIdeas(&model.IdeasResult{ResearchMode: "thesis", Mode: "independent", GeneratedAt: r.TS.Format(time.RFC3339), Ideas: []model.TradeIdea{}}); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := r.WritePrices("AAA", &quant.Series{Bars: []quant.Bar{bar("2026-08-03", 100, 100, 100, 100)}}); err != nil {
				t.Fatal(err)
			}
		}
		rep, err := CompareResearchWithOptions(context.Background(), dir, nil, ResearchComparisonOptions{PairManifest: manifest, AsOf: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		want := "unavailable"
		if empty {
			want = "empty_arm"
		}
		d := rep.Pairs[0].Deltas["shipped/15"]
		if d.State != want || d.ExcessDifference != nil {
			t.Fatalf("empty=%v: %+v", empty, d)
		}
	}
}

func TestResearchPairManifestRejectsReusedRunsAndTraversal(t *testing.T) {
	_, manifest, _, _ := pairFixture(t)
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(string) string{
		func(s string) string { return strings.Replace(s, `"thesis_run":"thesis"`, `"thesis_run":"legacy"`, 1) },
		func(s string) string { return strings.Replace(s, `data/macro.json`, `../metadata.json`, 1) },
	} {
		if err := os.WriteFile(manifest, []byte(mutate(string(data))), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadResearchPairs(manifest); err == nil {
			t.Fatal("invalid pairing accepted")
		}
	}
}

func TestResearchExecutionCostUsesClosedBarrierReturn(t *testing.T) {
	idea := buy(100, 95, 110)
	idea.TimeframeDays = 15
	idea.Status = "conditional"
	s := &quant.Series{Bars: []quant.Bar{bar("2026-09-08", 100, 100, 100, 100), bar("2026-09-09", 100, 111, 99, 105)}}
	cache := &seriesCache{bySymbol: map[string]*quant.Series{"AAA": s}, byRun: map[string]*quant.Series{}}
	rows := researchExecutionAt(context.Background(), cache, runSummary("r"), &model.IdeasResult{GeneratedAt: "2026-09-08T21:00:00Z", Ideas: []model.TradeIdea{idea}}, 30, 3, time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	if len(rows) != 1 || rows[0].NetReturn == nil || *rows[0].GrossReturn != 10 || *rows[0].NetReturn != 9.7 || !rows[0].Conditional {
		t.Fatalf("cost scenario did not use closed barrier trade: %+v", rows)
	}
}

func TestResearchExecutionHonorsFillWindow(t *testing.T) {
	idea := buy(100, 95, 110)
	s := &quant.Series{Bars: []quant.Bar{
		bar("2026-09-08", 100, 100, 100, 100),
		bar("2026-09-09", 105, 108, 104, 106),
		bar("2026-09-10", 100, 111, 99, 110),
	}}
	cache := &seriesCache{bySymbol: map[string]*quant.Series{"AAA": s}, byRun: map[string]*quant.Series{}}
	ideas := &model.IdeasResult{GeneratedAt: "2026-09-08T21:00:00Z", Ideas: []model.TradeIdea{idea}}
	short := researchExecutionAt(context.Background(), cache, runSummary("r"), ideas, 30, 1, time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))[0]
	long := researchExecutionAt(context.Background(), cache, runSummary("r"), ideas, 30, 2, time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))[0]
	if short.Outcome != OutcomeUnfilled || short.NetReturn != nil || long.NetReturn == nil || long.FillWindowDays != 2 {
		t.Fatalf("fill window ignored: short=%+v long=%+v", short, long)
	}
}

func TestResearchOverlapIncludesOppositeDirectionBeyondWeeklyDedupe(t *testing.T) {
	a := Entry{Ticker: "AAA", Direction: "BUY", GeneratedAt: "2026-08-03T12:00:00Z", TimeframeDays: 15, CallEndDate: "2026-08-24", CallDone: true}
	b := a
	b.Direction = "SELL"
	b.GeneratedAt = "2026-08-13T12:00:00Z"
	arm := summarizeResearchArm(ControlArm{Entries: []Entry{a, b}})
	if arm.Duplicates != 0 || arm.Overlapping != 1 {
		t.Fatalf("overlap hidden by dedupe: %+v", arm)
	}
}

func TestResearchBenchmarkKeepsHolidayAndIntradayAnchors(t *testing.T) {
	stock := &quant.Series{Bars: []quant.Bar{bar("2026-09-04", 100, 100, 100, 100), bar("2026-09-08", 105, 105, 105, 105), bar("2026-09-09", 110, 110, 110, 110)}}
	bench := &quant.Series{Bars: []quant.Bar{bar("2026-09-04", 200, 200, 200, 200), bar("2026-09-08", 202, 202, 202, 202), bar("2026-09-09", 204, 204, 204, 204)}}
	cache := &seriesCache{bySymbol: map[string]*quant.Series{"AAA": stock}, byRun: map[string]*quant.Series{}}
	dir := t.TempDir()
	got, ok := researchBenchmarkReturn(context.Background(), cache, dir, "AAA", bench, "2026-09-07", "2026-09-09", 100)
	if !ok || abs(got-.02) > 1e-9 {
		t.Fatalf("holiday advanced benchmark anchor: %v %v", got, ok)
	}
	r := &store.Run{Dir: dir}
	if err := r.WriteQuantPack(&quant.Pack{ByTicker: map[string]quant.Metrics{"AAA": {AsOf: "2026-09-04", LastClose: 100}}}); err != nil {
		t.Fatal(err)
	}
	got, ok = researchBenchmarkReturn(context.Background(), cache, dir, "AAA", bench, "2026-09-08", "2026-09-09", 100)
	if !ok || abs(got-.02) > 1e-9 {
		t.Fatalf("intraday snapshot anchor ignored: %v %v", got, ok)
	}
	bench.Bars = bench.Bars[1:]
	if _, ok := researchBenchmarkReturn(context.Background(), cache, dir, "AAA", bench, "2026-09-08", "2026-09-09", 100); ok {
		t.Fatal("missing boundary substituted with later close")
	}
}

func TestResearchComparisonCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CompareResearch(ctx, t.TempDir(), nil); err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
}
