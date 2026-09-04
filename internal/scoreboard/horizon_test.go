package scoreboard

import (
	"context"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// emptyCache is a series cache with no price source: every benchmark lookup
// fails, which is the path a name with no mapped index takes.
func emptyCache() *seriesCache {
	return &seriesCache{yc: nil, bySymbol: map[string]*quant.Series{"": nil}, byRun: map[string]*quant.Series{}}
}

func TestMeasureHorizonScoresTheDirectionAtTheWindowClose(t *testing.T) {
	bars := []quant.Bar{
		bar("2026-01-06", 100, 102, 99, 101),
		bar("2026-01-07", 101, 103, 100, 102),
		bar("2026-01-08", 102, 130, 101, 110), // a spike inside the window…
		bar("2026-01-09", 110, 111, 104, 105), // …that is not where the window ends
	}
	got := measureHorizon(context.Background(), emptyCache(), bars, 100, model.DirectionBuy, 4, "", "")
	if !got.complete {
		t.Fatal("complete = false, want a completed 4-session window")
	}
	if got.pct != 5 {
		t.Errorf("pct = %+.2f, want +5.00 — the close at the end of the window, not the spike inside it", got.pct)
	}
	if got.endDate != "2026-01-09" {
		t.Errorf("endDate = %q, want 2026-01-09", got.endDate)
	}
}

func TestMeasureHorizonSignsAShortTheOtherWay(t *testing.T) {
	// A short is right when the price falls. The signed figure has to say so,
	// or every short in the record reads as a loss.
	bars := []quant.Bar{
		bar("2026-01-06", 100, 101, 94, 95),
		bar("2026-01-07", 95, 96, 89, 90),
	}
	got := measureHorizon(context.Background(), emptyCache(), bars, 100, model.DirectionSell, 2, "", "")
	if !got.complete || got.pct != 10 {
		t.Fatalf("pct = %+.2f (complete %v), want +10.00 for a short that fell 10%%", got.pct, got.complete)
	}
	if !horizonHit(got) {
		t.Error("horizonHit = false, want true — the short was right")
	}
}

func TestMeasureHorizonIsIncompleteBeforeTheWindowElapses(t *testing.T) {
	// The failure this guards: an idea two sessions old reading as a flat
	// result rather than as an unfinished one. A zero here is not a miss.
	bars := []quant.Bar{
		bar("2026-01-06", 100, 102, 99, 101),
		bar("2026-01-07", 101, 103, 100, 102),
	}
	got := measureHorizon(context.Background(), emptyCache(), bars, 100, model.DirectionBuy, 15, "", "")
	if got.complete {
		t.Error("complete = true with 2 of 15 sessions elapsed")
	}
	if got.pct != 0 {
		t.Errorf("pct = %+.2f, want 0 for an unfinished window", got.pct)
	}
	if horizonHit(got) {
		t.Error("horizonHit = true on an unfinished window")
	}
}

func TestMeasureHorizonNeedsAnAnchor(t *testing.T) {
	bars := []quant.Bar{bar("2026-01-06", 100, 102, 99, 101)}
	if got := measureHorizon(context.Background(), emptyCache(), bars, 0, model.DirectionBuy, 1, "", ""); got.complete {
		t.Error("complete = true with no anchor price — an old idea that recorded none is unmeasurable, not flat")
	}
}

func TestMeasureHorizonNetsOffTheBenchmark(t *testing.T) {
	// "The long was right" and "the market went up" are not the same finding,
	// and a record that cannot tell them apart flatters every long in a rally.
	name := []quant.Bar{
		bar("2026-01-06", 100, 102, 99, 101),
		bar("2026-01-07", 101, 103, 100, 102),
	}
	benchSeries := &quant.Series{Symbol: "^BENCH", Bars: []quant.Bar{
		bar("2026-01-05", 1000, 1001, 999, 1000), // the anchor session
		bar("2026-01-06", 1000, 1010, 1000, 1005),
		bar("2026-01-07", 1005, 1020, 1005, 1015), // the market did +1.5%
	}}
	cache := &seriesCache{bySymbol: map[string]*quant.Series{"^BENCH": benchSeries}, byRun: map[string]*quant.Series{}}

	got := measureHorizon(context.Background(), cache, name, 100, model.DirectionBuy, 2, "^BENCH", "2026-01-05")
	if got.pct != 2 {
		t.Fatalf("pct = %+.2f, want +2.00", got.pct)
	}
	if got.bench != 1.5 {
		t.Errorf("bench = %+.2f, want +1.50", got.bench)
	}
	if got.excess != 0.5 {
		t.Errorf("excess = %+.2f, want +0.50 — a 2%% long in a 1.5%% market", got.excess)
	}
}

func TestMeasureHorizonExcessAddsTheBenchmarkForAShort(t *testing.T) {
	// pnl() has already flipped the sign for a short, so a market that rose
	// makes the short's excess worse, not better.
	name := []quant.Bar{
		bar("2026-01-06", 100, 101, 97, 98),
		bar("2026-01-07", 98, 99, 97, 98), // the name fell 2%: a +2% short
	}
	benchSeries := &quant.Series{Symbol: "^BENCH", Bars: []quant.Bar{
		bar("2026-01-05", 1000, 1001, 999, 1000),
		bar("2026-01-07", 1000, 1035, 1000, 1030), // the market rose 3%
	}}
	cache := &seriesCache{bySymbol: map[string]*quant.Series{"^BENCH": benchSeries}, byRun: map[string]*quant.Series{}}

	got := measureHorizon(context.Background(), cache, name, 100, model.DirectionSell, 2, "^BENCH", "2026-01-05")
	if got.pct != 2 {
		t.Fatalf("pct = %+.2f, want +2.00 for a short on a name that fell 2%%", got.pct)
	}
	if got.excess != 5 {
		t.Errorf("excess = %+.2f, want +5.00 — the short beat a market that rose 3%%", got.excess)
	}
}

func TestHorizonRecordCountsEveryCompletedWindowIncludingUnfilled(t *testing.T) {
	// The reason the call is measured at all: an idea whose limit never traded
	// still said which way the stock would go, and scoring the pipeline only on
	// the calls that got a fill throws away the record of the rest.
	s := &Summary{Replay: true, Entries: []Entry{
		{Outcome: OutcomeUnfilled, Direction: "BUY", CallDone: true, CallPnLPct: 6, CallExcessPct: 4},
		{Outcome: OutcomeStop, Direction: "BUY", CallDone: true, CallPnLPct: 3, CallExcessPct: -1,
			TradeDone: true, TradePnLPct: 3, TradeExcessPct: -1},
		{Outcome: OutcomeExpired, Direction: "SELL", CallDone: true, CallPnLPct: -2, CallExcessPct: -2,
			TradeDone: true, TradePnLPct: -2, TradeExcessPct: -2},
		{Outcome: OutcomeOpen, Direction: "BUY"}, // window not elapsed: not a miss
	}}
	s.aggregate()

	if s.Horizon.N != 3 {
		t.Fatalf("Horizon.N = %d, want 3 — the unfilled idea counts, the unfinished one does not", s.Horizon.N)
	}
	if s.Horizon.Hits != 2 {
		t.Errorf("Horizon.Hits = %d, want 2", s.Horizon.Hits)
	}
	if s.Horizon.ExcessHits != 1 {
		t.Errorf("Horizon.ExcessHits = %d, want 1 — only one window beat its benchmark", s.Horizon.ExcessHits)
	}
	if s.HorizonTrade.N != 2 {
		t.Errorf("HorizonTrade.N = %d, want 2 — only the ideas that filled", s.HorizonTrade.N)
	}
	// The point of keeping both: the same ideas score differently.
	if s.Horizon.HitRate == s.HorizonTrade.HitRate {
		t.Errorf("the call and the trade scored identically (%.2f); the fixture was built so they diverge", s.Horizon.HitRate)
	}
}
