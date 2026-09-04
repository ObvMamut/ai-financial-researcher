package scoreboard

import (
	"context"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

func runSummary(name string) store.RunSummary {
	return store.RunSummary{Dir: "", Name: name}
}

func bar(date string, o, h, l, c float64) quant.Bar {
	return quant.Bar{Date: date, Open: o, High: h, Low: l, Close: c, Volume: 1_000_000}
}

func buy(entry, stop, target float64) model.TradeIdea {
	return model.TradeIdea{Ticker: "AAA", Direction: model.DirectionBuy, Entry: entry, Stop: stop, Target: target}
}

func sell(entry, stop, target float64) model.TradeIdea {
	return model.TradeIdea{Ticker: "AAA", Direction: model.DirectionSell, Entry: entry, Stop: stop, Target: target}
}

func TestWalkToExitTargetThenRetraceIsStillAWin(t *testing.T) {
	// The whole reason for the replay: this idea reached its target and then
	// gave it all back. Marking it to today's price called it a loss.
	bars := []quant.Bar{
		bar("2026-01-05", 100, 102, 99, 101),
		bar("2026-01-06", 101, 111, 100, 105), // target 110 touched
		bar("2026-01-07", 105, 106, 90, 92),   // and then a collapse
	}
	got := walkToExit(bars, buy(100, 95, 110), 10)
	if got.outcome != OutcomeTarget {
		t.Fatalf("outcome = %q, want target", got.outcome)
	}
	if got.price != 110 {
		t.Errorf("exit price = %.2f, want the target 110", got.price)
	}
	if got.date != "2026-01-06" || got.bars != 2 {
		t.Errorf("exit = %s after %d bars, want 2026-01-06 after 2", got.date, got.bars)
	}
}

func TestWalkToExitGapsThroughTheStop(t *testing.T) {
	// A stop is not a guaranteed price. A name that opens 5 below it fills
	// there, and a backtest that books the stop level invents money.
	bars := []quant.Bar{
		bar("2026-01-05", 100, 101, 99, 100),
		bar("2026-01-06", 90, 92, 88, 89), // gapped clean through a stop at 95
	}
	got := walkToExit(bars, buy(100, 95, 110), 10)
	if got.outcome != OutcomeStop {
		t.Fatalf("outcome = %q, want stop", got.outcome)
	}
	if got.price != 90 {
		t.Errorf("exit price = %.2f, want the open 90 — the stop at 95 never traded", got.price)
	}
}

func TestWalkToExitAssumesTheStopWhenOneBarTouchesBoth(t *testing.T) {
	// Daily bars do not say which came first. Assuming the target is how a
	// backtest flatters itself into a strategy nobody can trade.
	bars := []quant.Bar{bar("2026-01-05", 100, 111, 94, 105)}
	got := walkToExit(bars, buy(100, 95, 110), 10)
	if got.outcome != OutcomeStop {
		t.Fatalf("outcome = %q, want stop on a bar that touched both", got.outcome)
	}
	if got.price != 95 {
		t.Errorf("exit price = %.2f, want the stop 95 (the open did not gap through it)", got.price)
	}
}

func TestWalkToExitExpiresAtTheHorizonClose(t *testing.T) {
	bars := []quant.Bar{
		bar("2026-01-05", 100, 102, 99, 101),
		bar("2026-01-06", 101, 103, 100, 102),
		bar("2026-01-07", 102, 104, 101, 103),
		bar("2026-01-08", 103, 105, 102, 104), // past the 3-day horizon
	}
	got := walkToExit(bars, buy(100, 95, 110), 3)
	if got.outcome != OutcomeExpired {
		t.Fatalf("outcome = %q, want expired", got.outcome)
	}
	if got.price != 103 || got.date != "2026-01-07" || got.bars != 3 {
		t.Errorf("exit = %.2f on %s after %d bars, want 103 on 2026-01-07 after 3", got.price, got.date, got.bars)
	}
}

func TestWalkToExitStaysOpenWithoutEnoughHistory(t *testing.T) {
	// An idea two days old has not lost; it has not finished. Counting it as a
	// flat trade is what dragged the old win rate toward zero.
	bars := []quant.Bar{
		bar("2026-01-05", 100, 102, 99, 101),
		bar("2026-01-06", 101, 103, 100, 102),
	}
	got := walkToExit(bars, buy(100, 95, 110), 10)
	if got.outcome != OutcomeOpen {
		t.Fatalf("outcome = %q, want open", got.outcome)
	}
	if got.price != 102 || got.bars != 2 {
		t.Errorf("mark = %.2f after %d bars, want 102 after 2", got.price, got.bars)
	}
}

func TestWalkToExitSellSide(t *testing.T) {
	// Short: the stop is above and the target below.
	bars := []quant.Bar{bar("2026-01-05", 100, 101, 89, 92)}
	got := walkToExit(bars, sell(100, 105, 90), 10)
	if got.outcome != OutcomeTarget || got.price != 90 {
		t.Fatalf("short target: got %q at %.2f, want target at 90", got.outcome, got.price)
	}

	bars = []quant.Bar{bar("2026-01-05", 100, 106, 89, 104)}
	got = walkToExit(bars, sell(100, 105, 90), 10)
	if got.outcome != OutcomeStop || got.price != 105 {
		t.Fatalf("short whipsaw: got %q at %.2f, want stop at 105", got.outcome, got.price)
	}
}

func TestSimulateFillWithinTheBarRange(t *testing.T) {
	bars := []quant.Bar{bar("2026-01-05", 102, 103, 99, 101)}
	i, price, ok := simulateFill(bars, 100, model.DirectionBuy, 3)
	if !ok || i != 0 || price != 100 {
		t.Fatalf("fill = (%d, %.2f, %v), want the limit 100 on bar 0", i, price, ok)
	}
}

func TestSimulateFillTakesTheGapInYourFavour(t *testing.T) {
	// A buy limit at 100 on a bar that opens at 98 fills at 98. This is the one
	// place optimism is arithmetically correct.
	bars := []quant.Bar{bar("2026-01-05", 98, 99, 96, 97)}
	_, price, ok := simulateFill(bars, 100, model.DirectionBuy, 3)
	if !ok || price != 98 {
		t.Fatalf("buy fill = %.2f (ok=%v), want the open 98", price, ok)
	}

	bars = []quant.Bar{bar("2026-01-05", 105, 106, 103, 104)}
	_, price, ok = simulateFill(bars, 100, model.DirectionSell, 3)
	if !ok || price != 105 {
		t.Fatalf("sell fill = %.2f (ok=%v), want the open 105", price, ok)
	}
}

func TestSimulateFillGivesUpAfterTheWindow(t *testing.T) {
	// Four sessions of the name running away from the limit. By the fourth the
	// setup the idea described is not the setup in front of you.
	bars := []quant.Bar{
		bar("2026-01-05", 104, 106, 103, 105),
		bar("2026-01-06", 106, 108, 105, 107),
		bar("2026-01-07", 108, 110, 107, 109),
		bar("2026-01-08", 101, 102, 99, 100), // back in range, but too late
	}
	if _, _, ok := simulateFill(bars, 100, model.DirectionBuy, 3); ok {
		t.Error("filled outside the 3-session window")
	}
	if _, _, ok := simulateFill(bars, 100, model.DirectionBuy, 4); !ok {
		t.Error("did not fill on the fourth session with a 4-session window")
	}
}

// stubbedCache pre-loads the series cache so replayIdea never touches the network.
func stubbedCache(series map[string]*quant.Series) *seriesCache {
	c := &seriesCache{bySymbol: map[string]*quant.Series{}, byRun: map[string]*quant.Series{}}
	for k, v := range series {
		c.bySymbol[k] = v
	}
	return c
}

// The saved-snapshot fallback shared the symbol-keyed cache, so a ticker
// appearing in two runs got exactly one shot at a saved copy: whichever run
// asked first had its snapshot reused to replay the *other* run's idea. An older
// run's snapshot necessarily lacks the bars a newer idea needs, so that idea
// replayed as `open` forever — a closed trade quietly missing from the record
// the Chief and the risk gate both read.
func TestSeriesCacheAsksEveryRunForItsOwnSnapshot(t *testing.T) {
	write := func(dir string, bars []quant.Bar) string {
		t.Helper()
		run := &store.Run{Dir: dir}
		if err := run.WritePrices("AAA", &quant.Series{Symbol: "AAA", Bars: bars}); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	older := write(t.TempDir(), []quant.Bar{bar("2026-01-05", 100, 101, 99, 100)})
	newer := write(t.TempDir(), []quant.Bar{
		bar("2026-01-05", 100, 101, 99, 100),
		bar("2026-02-05", 100, 111, 100, 110),
	})

	// The live fetch has already failed for this symbol — the state the bug
	// lives in. There is no network here and none is attempted.
	c := &seriesCache{bySymbol: map[string]*quant.Series{"AAA": nil}, byRun: map[string]*quant.Series{}}

	first, err := c.get(context.Background(), "AAA", older)
	if err != nil || first == nil {
		t.Fatalf("older run's own snapshot not found: %v", err)
	}
	second, err := c.get(context.Background(), "AAA", newer)
	if err != nil || second == nil {
		t.Fatalf("newer run got no snapshot at all: %v", err)
	}
	if len(second.Bars) != 2 {
		t.Errorf("newer run replayed against %d bars, want 2 — it was handed the older run's copy",
			len(second.Bars))
	}
	// Each run's copy is still cached: the second ask for the same run does not
	// re-read the file, and a run with no snapshot stays an honest error.
	if again, _ := c.get(context.Background(), "AAA", newer); again != second {
		t.Error("a run's snapshot was re-read instead of cached")
	}
	if _, err := c.get(context.Background(), "AAA", t.TempDir()); err == nil {
		t.Error("a run with no saved prices reported a series")
	}
}

func TestReplayIdeaMeasuresExcessOverTheBenchmark(t *testing.T) {
	// A long that made 10% while its index made 4% earned 6%. The old
	// scoreboard reported the 10% and let a rising tide look like skill.
	stock := &quant.Series{Symbol: "AAA", Bars: []quant.Bar{
		bar("2026-01-02", 100, 100, 100, 100), // generation day — not tradable
		bar("2026-01-05", 100, 101, 99, 100),  // fill at the 100 limit
		bar("2026-01-06", 100, 111, 100, 110), // target 110
	}}
	bench := &quant.Series{Symbol: "^GSPC", Bars: []quant.Bar{
		bar("2026-01-05", 5000, 5000, 5000, 5000),
		bar("2026-01-06", 5200, 5200, 5200, 5200),
	}}
	cache := stubbedCache(map[string]*quant.Series{"AAA": stock, "^GSPC": bench})

	idea := buy(100, 95, 110)
	idea.Index = "sp500"
	idea.PriceAtGeneration = 100
	e := replayIdea(context.Background(), runSummary("run-1"), "2026-01-02T20:00:00Z", idea, cache, 3)

	if e.Outcome != OutcomeTarget {
		t.Fatalf("outcome = %q, want target", e.Outcome)
	}
	if e.EntryFilled != 100 || e.EntryDate != "2026-01-05" {
		t.Errorf("fill = %.2f on %s, want 100 on 2026-01-05 (the generation bar is not tradable)", e.EntryFilled, e.EntryDate)
	}
	if e.PnLPct != 10 {
		t.Errorf("P&L = %.2f%%, want 10", e.PnLPct)
	}
	if e.BenchmarkPnLPct != 4 {
		t.Errorf("benchmark = %.2f%%, want 4", e.BenchmarkPnLPct)
	}
	if e.ExcessPnLPct != 6 {
		t.Errorf("excess = %.2f%%, want 10 − 4 = 6", e.ExcessPnLPct)
	}
	// 10 points of gain on 5 points of risk is 2R.
	if e.RiskAdjPnL != 2 {
		t.Errorf("R = %.2f, want 2.00 (10 gained on a 5-wide stop)", e.RiskAdjPnL)
	}
}

func TestReplayIdeaCreditsAShortForAFallingBenchmark(t *testing.T) {
	// Short excess is P&L *plus* the benchmark's move: being short into a 4%
	// index decline is 4% of the result the market handed you.
	stock := &quant.Series{Symbol: "AAA", Bars: []quant.Bar{
		bar("2026-01-02", 100, 100, 100, 100),
		bar("2026-01-05", 100, 101, 99, 100),
		bar("2026-01-06", 95, 96, 89, 90), // target 90
	}}
	bench := &quant.Series{Symbol: "^GSPC", Bars: []quant.Bar{
		bar("2026-01-05", 5000, 5000, 5000, 5000),
		bar("2026-01-06", 4800, 4800, 4800, 4800),
	}}
	cache := stubbedCache(map[string]*quant.Series{"AAA": stock, "^GSPC": bench})

	idea := sell(100, 105, 90)
	idea.Index = "sp500"
	e := replayIdea(context.Background(), runSummary("run-1"), "2026-01-02T20:00:00Z", idea, cache, 3)

	if e.PnLPct != 10 {
		t.Fatalf("short P&L = %.2f%%, want 10", e.PnLPct)
	}
	if e.BenchmarkPnLPct != -4 {
		t.Fatalf("benchmark = %.2f%%, want -4", e.BenchmarkPnLPct)
	}
	if e.ExcessPnLPct != 6 {
		t.Errorf("short excess = %.2f%%, want 10 + (−4) = 6", e.ExcessPnLPct)
	}
}

func TestReplayIdeaReportsAnUnfilledLimit(t *testing.T) {
	stock := &quant.Series{Symbol: "AAA", Bars: []quant.Bar{
		bar("2026-01-02", 100, 100, 100, 100),
		bar("2026-01-05", 104, 106, 103, 105),
		bar("2026-01-06", 106, 108, 105, 107),
		bar("2026-01-07", 108, 110, 107, 109),
	}}
	cache := stubbedCache(map[string]*quant.Series{"AAA": stock})
	e := replayIdea(context.Background(), runSummary("run-1"), "2026-01-02T20:00:00Z", buy(100, 95, 110), cache, 3)

	if e.Outcome != OutcomeUnfilled {
		t.Fatalf("outcome = %q, want unfilled", e.Outcome)
	}
	if e.PnLPct != 0 || e.EntryFilled != 0 {
		t.Errorf("an unfilled limit is not a trade: P&L %.2f, fill %.2f", e.PnLPct, e.EntryFilled)
	}
	if e.EntryPlanned != 100 {
		t.Errorf("planned entry = %.2f, want the 100 that never traded", e.EntryPlanned)
	}
}

func TestAggregateCountsWinRateOverClosedTradesOnly(t *testing.T) {
	// The 32%-win-rate bug in one test: two wins, one loss, and three rows that
	// were never trades. The old denominator was six.
	s := &Summary{Entries: []Entry{
		{Direction: "BUY", Outcome: OutcomeTarget, PnLPct: 8, EntryFilled: 100, Stop: 95, RiskAdjPnL: 1.6},
		{Direction: "BUY", Outcome: OutcomeExpired, PnLPct: 1, EntryFilled: 100, Stop: 95, RiskAdjPnL: 0.2},
		{Direction: "BUY", Outcome: OutcomeStop, PnLPct: -5, EntryFilled: 100, Stop: 95, RiskAdjPnL: -1},
		{Direction: "BUY", Outcome: OutcomeOpen, PnLPct: 0.4, EntryFilled: 100, Stop: 95},
		{Direction: "BUY", Outcome: OutcomeUnfilled},
		{Direction: "BUY", Outcome: OutcomeError, Err: "no history"},
	}}
	s.aggregate()

	if s.Closed != 3 {
		t.Fatalf("closed = %d, want 3", s.Closed)
	}
	if s.Wins != 2 || s.Losses != 1 {
		t.Errorf("record = %dW/%dL, want 2W/1L", s.Wins, s.Losses)
	}
	if want := 2.0 / 3.0; s.WinRate != want {
		t.Errorf("win rate = %.4f, want %.4f — open, unfilled and error rows are not losses", s.WinRate, want)
	}
	if s.Scored != 4 {
		t.Errorf("scored = %d, want 4 (the three closed plus the open mark)", s.Scored)
	}
	if got, want := s.AvgPnL, round2((8+1-5)/3.0); got != want {
		t.Errorf("avg P&L = %.2f, want %.2f", got, want)
	}
	if got, want := s.AvgR, round2((1.6+0.2-1)/3.0); got != want {
		t.Errorf("avg R = %.2f, want %.2f", got, want)
	}
	if s.ByOutcome[OutcomeUnfilled] != 1 || s.ByOutcome[OutcomeTarget] != 1 {
		t.Errorf("outcome tally = %v", s.ByOutcome)
	}
}

func TestAggregateScoresDomainsOnTheTradesTheyBacked(t *testing.T) {
	// A domain is judged on the trades it argued for. Scoring it on trades it
	// called the other way, or had no view on, measures nothing.
	s := &Summary{Entries: []Entry{
		{Direction: "BUY", Outcome: OutcomeTarget, PnLPct: 8, Confidence: 70, Index: "sp500",
			DomainScores: map[string]int{"quant": 6, "macro": -3, "news": 0}},
		{Direction: "BUY", Outcome: OutcomeStop, PnLPct: -5, Confidence: 45, Index: "nq100",
			DomainScores: map[string]int{"quant": 4, "macro": 5}},
		{Direction: "SELL", Outcome: OutcomeTarget, PnLPct: 6, Confidence: 85, Index: "sp500",
			DomainScores: map[string]int{"macro": -7, "quant": 2}},
	}}
	s.aggregate()

	// quant backed the two longs; the short's +2 is a bullish read against a SELL.
	if got := s.ByDomain["quant"]; got.N != 2 || got.Wins != 1 {
		t.Errorf("quant = %+v, want N=2 W=1", got)
	}
	// macro backed the losing long and the winning short; its −3 opposed the
	// first long, and an opposed call is not a call.
	if got := s.ByDomain["macro"]; got.N != 2 || got.Wins != 1 {
		t.Errorf("macro = %+v, want N=2 W=1", got)
	}
	// A neutral 0 is coverage, not a call.
	if got, ok := s.ByDomain["news"]; ok {
		t.Errorf("news scored a trade it had no view on: %+v", got)
	}

	if got := s.ByDirection["SELL"]; got.N != 1 || got.WinRate != 1 {
		t.Errorf("SELL = %+v, want one trade, won", got)
	}
	if got := s.ByIndex["sp500"]; got.N != 2 {
		t.Errorf("sp500 = %+v, want 2", got)
	}
	// Buckets are read off the recorded domain scores, not off the stated
	// Confidence. The 85 on the short was a number a model asserted on a scale
	// this build no longer uses.
	//
	// Macro's scores are still *recorded* on a past idea and still answer "was
	// macro right", which is what ByDomain above measures. They no longer carry
	// weight, so they no longer move a confidence: the short's 85 rested on a
	// macro −7 that a quant +2 half-cancelled, and what is left of it is the
	// quant read alone.
	//   long 1: .35·.6              = 0.21 → /0.80 → 26 → "25-39"
	//   long 2: .35·.4              = 0.14 → /0.80 → 18 → "<25"
	//   short : .35·.2              = 0.07 → /0.80 →  9 → "<25"
	if got := s.ByConfidence["<25"]; got.N != 2 || got.Wins != 1 {
		t.Errorf("<25 bucket = %+v, want the losing long and the winning short", got)
	}
	if got := s.ByConfidence["25-39"]; got.N != 1 || got.Wins != 1 {
		t.Errorf("25-39 bucket = %+v, want the winning long", got)
	}
	if got, ok := s.ByConfidence["80+"]; ok {
		t.Errorf("a bucket from the old scale was populated: %+v", got)
	}
}

// TestAggregateSeparatesIdeasWithNoRecordedDomainScores pins the boundary
// between the eras this codebase has scored confidence in.
//
// An idea from before per-domain scores were recorded carries a number a model
// asserted, on a scale nothing else in this build uses. Bucketing it by that
// number would put it beside ideas whose confidence is computed arithmetic and
// call the two comparable.
func TestAggregateSeparatesIdeasWithNoRecordedDomainScores(t *testing.T) {
	s := &Summary{Entries: []Entry{
		{Direction: "BUY", Outcome: OutcomeTarget, PnLPct: 9, Confidence: 88},
		{Direction: "BUY", Outcome: OutcomeStop, PnLPct: -4, Confidence: 72},
		{Direction: "BUY", Outcome: OutcomeTarget, PnLPct: 6, Confidence: 41,
			DomainScores: map[string]int{"quant": 8, "news": 7, "fundamentals": 6}},
	}}
	s.aggregate()

	if got := s.ByConfidence[legacyConfidenceBucket]; got.N != 2 {
		t.Errorf("legacy bucket = %+v, want the two ideas with no domain scores", got)
	}
	// .35·.8 + .25·.7 + .15·.6 = 0.545 → /0.77 → 71
	if got := s.ByConfidence["55+"]; got.N != 1 {
		t.Errorf("55+ bucket = %+v, want the idea that can be re-scored", got)
	}
	// The legacy slice is kept out of what the Chief Analyst is shown.
	for _, b := range comparableConfidenceBuckets() {
		if b == legacyConfidenceBucket {
			t.Fatal("the legacy bucket must not reach the track-record block")
		}
	}
}

func TestReplayIdeaWaitsWhileTheFillWindowIsStillLive(t *testing.T) {
	// One session old and the limit has not traded. That is not a failed entry
	// — it is an entry with two sessions left. Marking it unfilled would write
	// off every idea generated in the last two days.
	stock := &quant.Series{Symbol: "AAA", Bars: []quant.Bar{
		bar("2026-01-02", 100, 100, 100, 100),
		bar("2026-01-05", 104, 106, 103, 105),
	}}
	cache := stubbedCache(map[string]*quant.Series{"AAA": stock})
	e := replayIdea(context.Background(), runSummary("run-1"), "2026-01-02T20:00:00Z", buy(100, 95, 110), cache, 3)

	if e.Outcome != OutcomeOpen {
		t.Fatalf("outcome = %q, want open — the 3-session window has 2 sessions left", e.Outcome)
	}
	if e.EntryFilled != 0 || e.PnLPct != 0 {
		t.Errorf("nothing filled, so nothing is owned: fill %.2f, P&L %.2f", e.EntryFilled, e.PnLPct)
	}
	if e.EntryPlanned != 100 || e.BarsHeld != 1 {
		t.Errorf("planned %.2f after %d session(s), want 100 after 1", e.EntryPlanned, e.BarsHeld)
	}
}

func TestReplayIdeaWithNoSessionsYet(t *testing.T) {
	// Generated after Friday's close and scored on Saturday: nothing has
	// happened, and the row must not read as a flat trade.
	stock := &quant.Series{Symbol: "AAA", Bars: []quant.Bar{
		bar("2026-01-02", 100, 100, 100, 100),
	}}
	cache := stubbedCache(map[string]*quant.Series{"AAA": stock})
	e := replayIdea(context.Background(), runSummary("run-1"), "2026-01-02T20:00:00Z", buy(100, 95, 110), cache, 3)

	if e.Outcome != OutcomeOpen || e.BarsHeld != 0 {
		t.Fatalf("outcome = %q after %d sessions, want open after 0", e.Outcome, e.BarsHeld)
	}

	// And such a row is not "scored": there is no P&L in it to average.
	s := &Summary{Entries: []Entry{e}}
	s.aggregate()
	if s.Scored != 0 || s.Closed != 0 {
		t.Errorf("scored %d / closed %d, want 0 / 0 for an idea that never opened", s.Scored, s.Closed)
	}
}
