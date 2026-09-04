package scoreboard

import "testing"

func idea(run, ts, ticker, dir string) Entry {
	return Entry{RunName: run, GeneratedAt: ts, Ticker: ticker, Direction: dir}
}

func TestDedupeCollapsesTheSameCallReproposedTheSameDay(t *testing.T) {
	// Five runs were fired on 2026-09-01 and three of them proposed the same
	// short. That is one bet, and a win rate that reads it as three is not
	// measuring the pipeline, it is measuring how often the pipeline was run.
	got, dropped := Dedupe([]Entry{
		idea("r1", "2026-09-01T06:50:32Z", "STLAM.MI", "SELL"),
		idea("r2", "2026-09-01T12:56:18Z", "STLAM.MI", "SELL"),
		idea("r3", "2026-09-01T18:38:59Z", "STLAM.MI", "SELL"),
	}, DefaultDedupeWindowDays)

	if dropped != 2 || len(got) != 1 {
		t.Fatalf("kept %d dropped %d, want 1 kept and 2 dropped", len(got), dropped)
	}
	if got[0].RunName != "r1" {
		t.Errorf("kept %q, want the earliest call r1 — a re-proposal must not be scored off a better later price", got[0].RunName)
	}
}

func TestDedupeKeepsTheOppositeDirectionOnTheSameName(t *testing.T) {
	// Reversing on a name is a new call, not a repeat of the old one.
	got, dropped := Dedupe([]Entry{
		idea("r1", "2026-09-01T06:50:32Z", "TSLA", "BUY"),
		idea("r2", "2026-09-01T18:38:59Z", "TSLA", "SELL"),
	}, DefaultDedupeWindowDays)
	if dropped != 0 || len(got) != 2 {
		t.Fatalf("kept %d dropped %d, want both kept", len(got), dropped)
	}
}

func TestDedupeKeepsARepeatOnceTheWindowHasPassed(t *testing.T) {
	// Past the window the first call has had its chance to resolve, so the
	// second one is genuinely a second observation.
	got, dropped := Dedupe([]Entry{
		idea("r1", "2026-09-01T06:50:32Z", "MU", "BUY"),
		idea("r2", "2026-09-03T06:26:46Z", "MU", "BUY"), // inside 7 days
		idea("r3", "2026-09-20T06:26:46Z", "MU", "BUY"), // well outside
	}, DefaultDedupeWindowDays)
	if dropped != 1 || len(got) != 2 {
		t.Fatalf("kept %d dropped %d, want 2 kept and 1 dropped", len(got), dropped)
	}
	if got[0].RunName != "r1" || got[1].RunName != "r3" {
		t.Errorf("kept %q and %q, want r1 and r3", got[0].RunName, got[1].RunName)
	}
}

func TestDedupeChainsFromTheKeptCallNotTheDroppedOne(t *testing.T) {
	// Three calls each 5 days apart: the second is inside the first's window and
	// is dropped, and the third is 10 days from the first, so it survives. If
	// the window chained off the dropped call instead, a daily cadence would
	// collapse an entire month into one observation.
	got, dropped := Dedupe([]Entry{
		idea("r1", "2026-09-01T00:00:00Z", "AMGN", "BUY"),
		idea("r2", "2026-09-06T00:00:00Z", "AMGN", "BUY"),
		idea("r3", "2026-09-11T00:00:00Z", "AMGN", "BUY"),
	}, DefaultDedupeWindowDays)
	if dropped != 1 || len(got) != 2 {
		t.Fatalf("kept %d dropped %d, want 2 kept and 1 dropped", len(got), dropped)
	}
	if got[0].RunName != "r1" || got[1].RunName != "r3" {
		t.Errorf("kept %q and %q, want r1 and r3", got[0].RunName, got[1].RunName)
	}
}

func TestDedupeKeepsUndatedEntries(t *testing.T) {
	// A missing timestamp is not evidence of duplication, and dropping on it
	// would quietly delete the oldest runs from the record.
	got, dropped := Dedupe([]Entry{
		idea("old", "", "AAPL", "BUY"),
		idea("older", "", "AAPL", "BUY"),
	}, DefaultDedupeWindowDays)
	if dropped != 0 || len(got) != 2 {
		t.Fatalf("kept %d dropped %d, want both undated entries kept", len(got), dropped)
	}
}

func TestDedupePreservesInputOrder(t *testing.T) {
	// The scoreboard prints newest-run first; counting must not reorder it.
	got, _ := Dedupe([]Entry{
		idea("newest", "2026-09-04T00:00:00Z", "A", "BUY"),
		idea("middle", "2026-09-03T00:00:00Z", "B", "BUY"),
		idea("oldest", "2026-09-01T00:00:00Z", "C", "BUY"),
	}, DefaultDedupeWindowDays)
	if len(got) != 3 || got[0].RunName != "newest" || got[2].RunName != "oldest" {
		t.Errorf("order = %v, want the input order preserved", []string{got[0].RunName, got[1].RunName, got[2].RunName})
	}
}

func TestDedupeEarliestWinsRegardlessOfInputOrder(t *testing.T) {
	// Entries arrive newest-first, so "keep the first one seen" would keep the
	// latest call. The clock decides, not the slice.
	got, dropped := Dedupe([]Entry{
		idea("late", "2026-09-01T18:38:59Z", "BAYN.DE", "BUY"),
		idea("early", "2026-09-01T06:50:32Z", "BAYN.DE", "BUY"),
	}, DefaultDedupeWindowDays)
	if dropped != 1 || len(got) != 1 {
		t.Fatalf("kept %d dropped %d, want 1 of each", len(got), dropped)
	}
	if got[0].RunName != "early" {
		t.Errorf("kept %q, want the earliest call", got[0].RunName)
	}
}

func TestDedupeIsOffAtZeroWindow(t *testing.T) {
	got, dropped := Dedupe([]Entry{
		idea("r1", "2026-09-01T06:50:32Z", "MU", "BUY"),
		idea("r2", "2026-09-01T12:56:18Z", "MU", "BUY"),
	}, 0)
	if dropped != 0 || len(got) != 2 {
		t.Errorf("kept %d dropped %d, want the feature disabled at a zero window", len(got), dropped)
	}
}

func TestSummaryCountsBetsInCellsAndTicketsInTheOutcomeTally(t *testing.T) {
	closed := func(run, ts, ticker, dir string, pnl float64) Entry {
		e := idea(run, ts, ticker, dir)
		e.Outcome, e.PnLPct, e.EntryFilled = OutcomeExpired, pnl, 100
		return e
	}
	s := &Summary{Replay: true, Entries: []Entry{
		closed("r1", "2026-09-01T06:00:00Z", "STLAM.MI", "SELL", -5),
		closed("r2", "2026-09-01T12:00:00Z", "STLAM.MI", "SELL", -5),
		closed("r3", "2026-09-01T18:00:00Z", "STLAM.MI", "SELL", -5),
		closed("r1", "2026-09-01T06:00:00Z", "MU", "BUY", +5),
	}}
	s.aggregate()

	if s.ByOutcome[OutcomeExpired] != 4 {
		t.Errorf("ByOutcome expired = %d, want 4 — the tally is a census of every idea", s.ByOutcome[OutcomeExpired])
	}
	if s.Duplicates != 2 {
		t.Errorf("Duplicates = %d, want 2", s.Duplicates)
	}
	if s.Closed != 2 {
		t.Fatalf("Closed = %d, want 2 independent observations", s.Closed)
	}
	// Counted as tickets the record is 1W/3L = 25%; as bets it is 1W/1L = 50%.
	if s.WinRate != 0.5 {
		t.Errorf("WinRate = %.2f, want 0.50 — one winning bet and one losing bet", s.WinRate)
	}
}
