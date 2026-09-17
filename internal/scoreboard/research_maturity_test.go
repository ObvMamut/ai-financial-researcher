package scoreboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

func evaluationTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestResearchMaturityRequiresCalendarEndpointAndCompleteBars(t *testing.T) {
	// Labor Day is closed. The two-session horizon ends September 9, even if
	// a provider inserts a holiday row or omits a session and later catches up.
	full := []quant.Bar{
		bar("2026-09-04", 100, 100, 100, 100),
		bar("2026-09-07", 900, 900, 900, 900),
		bar("2026-09-08", 101, 101, 101, 101),
		bar("2026-09-09", 102, 102, 102, 102),
		bar("2026-09-10", 103, 103, 103, 103),
	}
	for _, tc := range []struct {
		name, asOf, ticker string
		bars               []quant.Bar
		want               callState
		reason             string
	}{
		{"complete", "2026-09-09T22:00:00Z", "AAA", full, callScored, ""},
		{"intraday", "2026-09-09T18:00:00Z", "AAA", full, callPending, ""},
		{"future rows", "2026-09-08T23:00:00Z", "AAA", full, callPending, ""},
		{"missing matured intermediate", "2026-09-11T00:00:00Z", "AAA", append(append([]quant.Bar{}, full[:2]...), full[3:]...), callUnmeasurable, "2026-09-08"},
		{"missing matured endpoint", "2026-09-11T00:00:00Z", "AAA", append(append([]quant.Bar{}, full[:3]...), full[4:]...), callUnmeasurable, "2026-09-09"},
		{"missing future history", "2026-09-08T23:00:00Z", "AAA", nil, callPending, ""},
		{"missing matured history", "2026-09-11T00:00:00Z", "AAA", nil, callUnmeasurable, "no price history"},
		{"unknown foreign calendar", "2026-09-11T00:00:00Z", "7203.T", full, callUnmeasurable, "calendar unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := &seriesCache{bySymbol: map[string]*quant.Series{tc.ticker: {Bars: tc.bars}}, byRun: map[string]*quant.Series{}}
			e, state := measureResearchCall(context.Background(), cache, runSummary("r"), "2026-09-04T22:00:00Z", call{ticker: tc.ticker, direction: model.DirectionBuy, anchor: 100}, 2, evaluationTime(tc.asOf))
			if state != tc.want || !strings.Contains(e.Err, tc.reason) {
				t.Fatalf("got state=%v entry=%+v", state, e)
			}
			if state == callScored && (e.CallEndDate != "2026-09-09" || e.CallPnLPct != 2) {
				t.Fatalf("wrong window: %+v", e)
			}
			if state == callPending && e.CallDone {
				t.Fatal("pending call scored")
			}
		})
	}
}

func TestResearchIntradayGenerationUsesPreviousCompletedAnchor(t *testing.T) {
	s := &quant.Series{Bars: []quant.Bar{bar("2026-09-04", 100, 100, 100, 100), bar("2026-09-08", 200, 200, 200, 200), bar("2026-09-09", 110, 110, 110, 110)}}
	cache := &seriesCache{bySymbol: map[string]*quant.Series{"AAA": s}, byRun: map[string]*quant.Series{}}
	e, state := measureResearchCall(context.Background(), cache, runSummary("r"), "2026-09-08T16:00:00Z", call{ticker: "AAA", direction: model.DirectionBuy}, 1, evaluationTime("2026-09-10T00:00:00Z"))
	if state != callScored || e.PriceAtGen != 100 || e.CallPnLPct != 10 {
		t.Fatalf("lookahead anchor: %+v %v", e, state)
	}
	bench := &quant.Series{Bars: []quant.Bar{bar("2026-09-04", 100, 100, 100, 100), bar("2026-09-08", 200, 200, 200, 200), bar("2026-09-09", 102, 102, 102, 102)}}
	b, ok := researchBenchmarkReturn(context.Background(), cache, t.TempDir(), "AAA", bench, "2026-09-08T16:00:00Z", e.CallEndDate, e.PriceAtGen)
	if !ok || abs(b-.02) > 1e-9 {
		t.Fatalf("misaligned benchmark: %v %v", b, ok)
	}
}

func TestResearchPairPendingUntilEvaluationTimeMatures(t *testing.T) {
	dir, manifest, _, _ := pairFixture(t)
	opts := ResearchComparisonOptions{PairManifest: manifest, AsOf: evaluationTime("2026-08-04T22:00:00Z")}
	rep, err := CompareResearchWithOptions(context.Background(), dir, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.AsOf != opts.AsOf.Format(time.RFC3339) {
		t.Fatalf("as-of not retained: %s", rep.AsOf)
	}
	for _, key := range []string{"shipped/10", "shipped/15"} {
		d := rep.Pairs[0].Deltas[key]
		if d.State != "pending" || d.ExcessDifference != nil {
			t.Fatalf("future fixture prices scored: %+v", d)
		}
	}
}

func TestResearchPairRejectsContradictoryMetadata(t *testing.T) {
	for _, field := range []string{"mode", "research_mode", "timestamp", "schema"} {
		t.Run(field, func(t *testing.T) {
			dir, manifest, _, run := pairFixture(t)
			m, err := store.LoadMeta(run.Dir)
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "mode":
				m.Mode = "single"
			case "research_mode":
				m.ResearchMode = "legacy"
			case "timestamp":
				m.GeneratedAt = "2026-08-01T22:00:00Z"
			case "schema":
				m.SchemaVersion = 99
			}
			if err := run.WriteMeta(*m); err != nil {
				t.Fatal(err)
			}
			rep, err := CompareResearchWithOptions(context.Background(), dir, nil, ResearchComparisonOptions{PairManifest: manifest, AsOf: evaluationTime("2026-09-01T00:00:00Z")})
			if err != nil {
				t.Fatal(err)
			}
			if rep.Pairs[0].Status != "mismatch" || len(rep.Pairs[0].Deltas) != 0 {
				t.Fatalf("contradictory pair accepted: %+v", rep.Pairs[0])
			}
		})
	}
}

func TestResearchIncompleteTelemetryRemainsLowerBound(t *testing.T) {
	p, c, total := 10, 20, 30
	u := ResearchUsage{}
	u.add(model.DomainStatus{Attempts: 1, Usage: []model.TokenUsage{{Incomplete: true, PromptTokens: &p, CompletionTokens: &c, TotalTokens: &total}}})
	if u.CompleteAttempts != 0 || u.TotalTokens != 30 || u.Attempts != 1 {
		t.Fatalf("partial telemetry counted complete: %+v", u)
	}
}

func TestResearchExecutionIgnoresUncompletedDailyBars(t *testing.T) {
	idea := buy(100, 95, 110)
	s := &quant.Series{Bars: []quant.Bar{bar("2026-09-08", 100, 100, 100, 100), bar("2026-09-09", 100, 111, 99, 110)}}
	cache := &seriesCache{bySymbol: map[string]*quant.Series{"AAA": s}, byRun: map[string]*quant.Series{}}
	ideas := &model.IdeasResult{GeneratedAt: "2026-09-08T22:00:00Z", Ideas: []model.TradeIdea{idea}}
	rows := researchExecutionAt(context.Background(), cache, runSummary("r"), ideas, 30, 3, evaluationTime("2026-09-09T18:00:00Z"))
	if len(rows) != 1 || rows[0].NetReturn != nil {
		t.Fatalf("intraday target counted closed: %+v", rows)
	}
	// A run-local snapshot must not restore filtered future prices on cache miss.
	dir := t.TempDir()
	run := &store.Run{Dir: dir}
	if err := run.WritePrices("AAA", s); err != nil {
		t.Fatal(err)
	}
	cache = &seriesCache{bySymbol: map[string]*quant.Series{}, byRun: map[string]*quant.Series{}}
	rows = researchExecutionAt(context.Background(), cache, store.RunSummary{Name: "r", Dir: dir}, ideas, 30, 3, evaluationTime("2026-09-08T12:00:00Z"))
	if len(rows) != 1 || rows[0].NetReturn != nil {
		t.Fatalf("snapshot reintroduced future prices: %+v", rows)
	}
}
