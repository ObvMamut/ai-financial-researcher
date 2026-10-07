package scoreboard

import (
	"strings"
	"testing"
)

func closed(ticker, dir string, outcome Outcome, r float64, mod func(*Entry)) Entry {
	e := Entry{
		Ticker: ticker, Direction: dir, Outcome: outcome,
		PriceAtGen: 100, EntryPlanned: 100, EntryFilled: 100, Stop: 94, Target: 112,
		RiskAdjPnL: r, PnLPct: r * 6,
	}
	if mod != nil {
		mod(&e)
	}
	return e
}

func attributionOf(entries ...Entry) *Attribution {
	s := &Summary{Replay: true, Entries: entries}
	s.aggregate()
	return Attribute(s)
}

func TestAttributionSplitsOnCoverageAndAgreement(t *testing.T) {
	a := attributionOf(
		closed("AAA", "BUY", OutcomeTarget, 1.8, func(e *Entry) {
			e.DomainScores = map[string]int{"quant": 8}
			e.Consensus = 1.0
		}),
		closed("BBB", "BUY", OutcomeStop, -1.0, func(e *Entry) {
			e.DomainScores = map[string]int{"quant": 8}
			e.Consensus = 0.95
		}),
		closed("CCC", "BUY", OutcomeTarget, 1.9, func(e *Entry) {
			e.DomainScores = map[string]int{"quant": 4, "news": 6, "fundamentals": -2, "sentiment": 3, "macro": 3}
			e.Consensus = 0.4
		}),
	)
	if a.NClosed != 3 {
		t.Fatalf("NClosed = %d, want 3", a.NClosed)
	}
	if got := a.ByCoverage["1-2 domains"].N; got != 2 {
		t.Errorf("thin-coverage bucket n = %d, want 2", got)
	}
	if got := a.ByCoverage["5 domains"].N; got != 1 {
		t.Errorf("full-coverage bucket n = %d, want 1", got)
	}
	// The two axes the base score conflates are now separately countable.
	if got := a.ByConsensus["unanimous (85%+)"].N; got != 2 {
		t.Errorf("unanimous bucket n = %d, want 2", got)
	}
	if got := a.ByConsensus["split (<50%)"].N; got != 1 {
		t.Errorf("split bucket n = %d, want 1", got)
	}
}

// An idea from before consensus was recorded has none. Bucketing it as zero
// would file it under total disagreement, which is a claim the data does not
// make.
func TestAttributionLeavesUnrecordedConsensusOut(t *testing.T) {
	a := attributionOf(closed("AAA", "BUY", OutcomeTarget, 1.0, nil))
	if len(a.ByConsensus) != 0 {
		t.Errorf("ByConsensus = %v, want empty for an idea that recorded none", a.ByConsensus)
	}
}

// The loudest silent failure the pipeline has: of 94 stored ideas only 5 had
// ever closed. A limit that never trades is not a loss and not a win — it is a
// trade that did not happen, and nothing else in this package can see it.
func TestAttributionMeasuresTheFillRate(t *testing.T) {
	s := &Summary{Replay: true, Skipped: 4, Entries: []Entry{
		closed("AAA", "BUY", OutcomeTarget, 1.8, nil),
		closed("BBB", "BUY", OutcomeUnfilled, 0, func(e *Entry) {
			e.EntryFilled, e.EntryPlanned = 0, 94 // asked 6% below the close
		}),
		closed("CCC", "BUY", OutcomeUnfilled, 0, func(e *Entry) {
			e.EntryFilled, e.EntryPlanned = 0, 95
		}),
		closed("DDD", "SELL", OutcomeError, 0, func(e *Entry) { e.EntryFilled = 0 }),
	}}
	s.aggregate()
	a := Attribute(s)

	f := a.Fills
	if f.Replayable != 4 || f.Filled != 1 || f.Unfilled != 2 || f.Errored != 1 || f.Skipped != 4 {
		t.Errorf("fill record = %+v, want 4 replayable / 1 filled / 2 unfilled / 1 errored / 4 skipped", f)
	}
	// And the offsets say *why*: the two that never traded both asked for a
	// price well below the tape.
	waiting := f.ByOffset["below -1.5% (waiting for a better price)"]
	if waiting.N != 2 || waiting.Filled != 0 {
		t.Errorf("far-limit bucket = %+v, want 2 ideas and no fills", waiting)
	}
	at := f.ByOffset["at the close (±0.25%)"]
	if at.N != 1 || at.Filled != 1 {
		t.Errorf("at-the-close bucket = %+v, want 1 idea filled", at)
	}
}

// A SELL's limit is better than the close when it is *above* it, so the offset
// has to be signed toward the trade's own direction or the two sides land in
// opposite buckets for the same behaviour.
func TestEntryOffsetIsSignedTowardTheTrade(t *testing.T) {
	buy := Entry{Direction: "BUY", PriceAtGen: 100, EntryPlanned: 97}
	sell := Entry{Direction: "SELL", PriceAtGen: 100, EntryPlanned: 103}
	gotBuy, _ := entryOffsetBucket(buy)
	gotSell, _ := entryOffsetBucket(sell)
	if gotBuy != gotSell {
		t.Errorf("a BUY 3%% below and a SELL 3%% above are the same behaviour: %q vs %q", gotBuy, gotSell)
	}
	if !strings.Contains(gotBuy, "better price") {
		t.Errorf("bucket = %q, want the waiting-for-a-better-price slice", gotBuy)
	}
}

// A cell of two says nothing, and printing it invites a reader to make
// something of it anyway.
func TestAttributionLinesDropThinCells(t *testing.T) {
	a := attributionOf(
		closed("AAA", "BUY", OutcomeTarget, 1.8, func(e *Entry) { e.Sector = "Energy" }),
		closed("BBB", "BUY", OutcomeStop, -1.0, func(e *Entry) { e.Sector = "Energy" }),
		closed("CCC", "BUY", OutcomeTarget, 1.2, func(e *Entry) { e.Sector = "Energy" }),
		closed("DDD", "BUY", OutcomeTarget, 1.1, func(e *Entry) { e.Sector = "Health Care" }),
	)
	lines := strings.Join(a.Lines(3), "\n")
	if !strings.Contains(lines, "sector Energy") {
		t.Errorf("a three-trade cell should show:\n%s", lines)
	}
	if strings.Contains(lines, "Health Care") {
		t.Errorf("a one-trade cell must not show:\n%s", lines)
	}
	// And the cell index a lesson has to name agrees with what was printed.
	cells := a.Cells(3)
	if !cells["energy"] || cells["health care"] {
		t.Errorf("Cells = %v, want Energy only", cells)
	}
}

// The legacy mark-to-market has no fills, no barriers and no consensus, so none
// of these questions can honestly be asked of it.
func TestAttributionRefusesTheLegacySummary(t *testing.T) {
	if got := Attribute(&Summary{Replay: false}); got != nil {
		t.Errorf("Attribute(legacy) = %+v, want nil", got)
	}
	if got := Attribute(nil); got != nil {
		t.Errorf("Attribute(nil) = %+v, want nil", got)
	}
}

func TestAttributionSplitsOnWhetherTheHoldRanThroughEarnings(t *testing.T) {
	a := attributionOf(
		closed("FCX", "BUY", OutcomeExpired, 0.4, func(e *Entry) { e.Earnings = earningsHeld }),
		closed("MU", "BUY", OutcomeExpired, -0.2, func(e *Entry) { e.Earnings = earningsNone }),
		// A run that recorded no calendar says nothing either way.
		closed("OLD", "BUY", OutcomeExpired, 0.1, nil),
	)
	if got := a.ByEarnings[earningsHeld].N; got != 1 {
		t.Errorf("held-through bucket n = %d, want 1", got)
	}
	if got := a.ByEarnings[earningsNone].N; got != 1 {
		t.Errorf("no-earnings bucket n = %d, want 1", got)
	}
	if len(a.ByEarnings) != 2 {
		t.Errorf("ByEarnings = %v: an entry with no recorded calendar was bucketed", a.ByEarnings)
	}
	if !a.Cells(1)["earnings "+earningsHeld] {
		t.Error("a post-mortem lesson cannot name the earnings cell")
	}
}

func TestEarningsKeyComparesTheEventToTheActualHold(t *testing.T) {
	events := map[string]string{"FCX": "2026-10-22", "TTE.PA": "2026-10-29"}
	cases := []struct {
		ticker, entry, exit, want string
		known                     bool
	}{
		{"FCX", "2026-10-08", "2026-10-28", earningsHeld, true},
		{"TTE.PA", "2026-10-08", "2026-10-28", earningsNone, true},
		{"MU", "2026-10-08", "2026-10-28", earningsNone, true},
		{"FCX", "", "", "", true},                      // never filled: no hold to run through
		{"FCX", "2026-10-08", "2026-10-28", "", false}, // the run recorded no calendar
	}
	for _, c := range cases {
		e := Entry{Ticker: c.ticker, EntryDate: c.entry, ExitDate: c.exit}
		if got := earningsKey(e, events, c.known); got != c.want {
			t.Errorf("%s %s→%s known=%v: %q, want %q", c.ticker, c.entry, c.exit, c.known, got, c.want)
		}
	}
}
