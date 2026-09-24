package scoreboard

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// mooBuy is a market-on-open long priced off a 100 close, with a catastrophe
// stop and no target unless one is given.
func mooBuy(stop, target float64, days int) model.TradeIdea {
	idea := buy(100, stop, target)
	idea.EntryType = model.EntryMarketOnOpen
	idea.PriceAtGeneration = 100
	idea.TimeframeDays = days
	return idea
}

func replayOne(t *testing.T, idea model.TradeIdea, bars ...quant.Bar) Entry {
	t.Helper()
	s := &quant.Series{Symbol: "AAA", Bars: append([]quant.Bar{bar("2026-01-02", 100, 100, 100, 100)}, bars...)}
	return replayIdea(context.Background(), runSummary("run-1"), "2026-01-02T20:00:00Z", idea, stubbedCache(map[string]*quant.Series{"AAA": s}), 3)
}

// A gap up is exactly where the old limit never filled — and where the call
// was most often right. Market-on-open takes it at the open, above the
// reference close.
func TestMarketOnOpenFillsAtTheNextOpenThroughAGap(t *testing.T) {
	e := replayOne(t, mooBuy(80, 0, 3),
		bar("2026-01-05", 104, 106, 103, 105),
		bar("2026-01-06", 106, 108, 105, 107),
		bar("2026-01-07", 108, 110, 107, 109),
	)
	if e.EntryType != model.EntryMarketOnOpen {
		t.Errorf("entry type = %q, not carried onto the row", e.EntryType)
	}
	if e.EntryFilled != 104 || e.EntryDate != "2026-01-05" {
		t.Errorf("fill = %.2f on %s, want the 104 open on 2026-01-05", e.EntryFilled, e.EntryDate)
	}
	if e.Outcome != OutcomeExpired || e.ExitPrice != 109 || e.BarsHeld != 3 {
		t.Errorf("exit = %s at %.2f after %d bars, want expired at the 109 close after 3", e.Outcome, e.ExitPrice, e.BarsHeld)
	}
	if e.PnLPct != 4.81 {
		t.Errorf("P&L = %.2f%%, want 104 → 109 = 4.81", e.PnLPct)
	}
	// The same idea as a limit at 100 never trades.
	limit := mooBuy(80, 0, 3)
	limit.EntryType = ""
	limit.Target = 130
	if l := replayOne(t, limit,
		bar("2026-01-05", 104, 106, 103, 105),
		bar("2026-01-06", 106, 108, 105, 107),
		bar("2026-01-07", 108, 110, 107, 109),
	); l.Outcome != OutcomeUnfilled {
		t.Errorf("limit control outcome = %s, want unfilled", l.Outcome)
	}
}

func TestMarketOnOpenExitsAtTheCatastropheStop(t *testing.T) {
	e := replayOne(t, mooBuy(80, 0, 10),
		bar("2026-01-05", 99, 101, 95, 96),
		bar("2026-01-06", 90, 91, 78, 79), // through the stop intraday
		bar("2026-01-07", 79, 85, 78, 84),
	)
	if e.EntryFilled != 99 || e.Outcome != OutcomeStop || e.ExitPrice != 80 || e.ExitDate != "2026-01-06" {
		t.Fatalf("got fill %.2f, %s at %.2f on %s; want fill 99, stop at 80 on 2026-01-06", e.EntryFilled, e.Outcome, e.ExitPrice, e.ExitDate)
	}
	// R is measured against the stop distance from the actual fill: 19 wide.
	if e.RiskAdjPnL != -1 {
		t.Errorf("R = %.2f, want -1", e.RiskAdjPnL)
	}
}

// With no target the position rides through a move that would have hit the
// old take-profit and exits on time.
func TestMarketOnOpenWithoutATargetExitsOnTime(t *testing.T) {
	e := replayOne(t, mooBuy(80, 0, 2),
		bar("2026-01-05", 100, 101, 99, 100),
		bar("2026-01-06", 101, 140, 100, 125),
		bar("2026-01-07", 125, 130, 120, 128),
	)
	if e.Outcome != OutcomeExpired || e.ExitPrice != 125 || e.ExitDate != "2026-01-06" {
		t.Errorf("got %s at %.2f on %s, want expired at the 125 close on 2026-01-06", e.Outcome, e.ExitPrice, e.ExitDate)
	}
}

// A target the Chief chose to state is still honoured as a take-profit.
func TestMarketOnOpenHonoursAStatedTarget(t *testing.T) {
	e := replayOne(t, mooBuy(80, 115, 10),
		bar("2026-01-05", 100, 101, 99, 100),
		bar("2026-01-06", 101, 116, 100, 112),
	)
	if e.Outcome != OutcomeTarget || e.ExitPrice != 115 {
		t.Errorf("got %s at %.2f, want target at 115", e.Outcome, e.ExitPrice)
	}
}

// Generated after the last close in the data: no session to fill in yet, which
// is `open`, never `unfilled`.
func TestMarketOnOpenWithNoSessionYetIsOpen(t *testing.T) {
	if e := replayOne(t, mooBuy(80, 0, 10)); e.Outcome != OutcomeOpen {
		t.Errorf("outcome = %s, want open", e.Outcome)
	}
}

// An ideas.json written before entry_type existed carries no such key. It must
// decode as a limit and replay exactly as it always did: fill inside the
// window at the limit, then the first barrier.
func TestHistoricalIdeaWithoutEntryTypeReplaysAsALimit(t *testing.T) {
	raw := `{"ticker":"AAA","direction":"BUY","entry":100,"stop":95,"target":110,"timeframe_days":10,"price_at_generation":101}`
	var idea model.TradeIdea
	if err := json.Unmarshal([]byte(raw), &idea); err != nil {
		t.Fatal(err)
	}
	if idea.MarketOnOpen() || idea.EntryType != "" {
		t.Fatalf("a pre-field idea decoded as %q", idea.EntryType)
	}
	e := replayOne(t, idea,
		bar("2026-01-05", 102, 103, 101, 102), // above the limit: no fill
		bar("2026-01-06", 101, 102, 99, 100),  // trades through 100
		bar("2026-01-07", 100, 111, 100, 110), // target
	)
	if e.EntryFilled != 100 || e.EntryDate != "2026-01-06" || e.Outcome != OutcomeTarget || e.ExitPrice != 110 {
		t.Errorf("historical limit replayed as fill %.2f on %s, %s at %.2f; want 100 on 2026-01-06, target at 110",
			e.EntryFilled, e.EntryDate, e.Outcome, e.ExitPrice)
	}
}
