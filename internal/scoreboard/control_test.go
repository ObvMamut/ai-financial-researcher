package scoreboard

import (
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func row(ticker, index string, score, close float64) prescreenRow {
	return prescreenRow{Ticker: ticker, Index: index, Score: score, Close: close}
}

func TestCompositeCallsTakeBothEndsOfTheRanking(t *testing.T) {
	// The composite is a *signed long ranking*, so its strongest calls sit at
	// both ends: |score| is the conviction and the sign is the direction.
	// Taking the top n would produce a long-only arm and measure a bull market
	// rather than the ranking — the same defect the scout tables were split to
	// fix.
	rows := []prescreenRow{
		row("STRONGLONG", "sp500", +2.9, 100),
		row("MIDLONG", "sp500", +0.4, 100),
		row("MIDSHORT", "sp500", -0.3, 100),
		row("STRONGSHORT", "sp500", -3.1, 100),
	}
	got := compositeCalls(rows, 2)
	if len(got) != 2 {
		t.Fatalf("got %d calls, want 2", len(got))
	}
	if got[0].ticker != "STRONGSHORT" || got[0].direction != model.DirectionSell {
		t.Errorf("first call = %s %s, want STRONGSHORT SELL (|-3.1| is the largest)", got[0].ticker, got[0].direction)
	}
	if got[1].ticker != "STRONGLONG" || got[1].direction != model.DirectionBuy {
		t.Errorf("second call = %s %s, want STRONGLONG BUY", got[1].ticker, got[1].direction)
	}
}

func TestCompositeCallsSkipExcludedAndUnpricedRows(t *testing.T) {
	// An excluded row was never a candidate — illiquid, or too short a history
	// for the composite to mean anything — and a row with no close cannot be
	// anchored. Neither is a call the pre-screen would have made.
	rows := []prescreenRow{
		{Ticker: "ILLIQUID", Score: -9, Close: 100, Excluded: "illiquid (20d ADV $2.1M, floor $20M)"},
		{Ticker: "NOPRICE", Score: 8, Close: 0},
		{Ticker: "ZERO", Score: 0, Close: 100},
		row("REAL", "sp500", 1.2, 100),
	}
	got := compositeCalls(rows, 5)
	if len(got) != 1 || got[0].ticker != "REAL" {
		t.Fatalf("got %v, want only REAL", got)
	}
}

func TestCompositeCallsTakeADualListedNameOnce(t *testing.T) {
	// A name in two indices has a row and a composite per index. It is one bet,
	// and taking it twice would let one name fill the arm.
	rows := []prescreenRow{
		row("MU", "sp500", 3.7, 100),
		row("MU", "nq100", 2.6, 100),
		row("OTHER", "sp500", 1.0, 50),
	}
	got := compositeCalls(rows, 2)
	if len(got) != 2 {
		t.Fatalf("got %d calls, want 2", len(got))
	}
	if got[0].ticker != "MU" || got[1].ticker != "OTHER" {
		t.Errorf("got %s and %s, want MU once then OTHER", got[0].ticker, got[1].ticker)
	}
	if got[0].index != "sp500" {
		t.Errorf("MU taken under %q, want the stronger sp500 row", got[0].index)
	}
}

func TestShortlistCallsDropNeutralNominations(t *testing.T) {
	// A neutral nomination is not a directional call, and scoring it either way
	// would invent one.
	cands := []model.Candidate{
		{Ticker: "UP", Bias: model.BiasBullish},
		{Ticker: "FLAT", Bias: model.BiasNeutral},
		{Ticker: "DOWN", Bias: model.BiasBearish},
	}
	got := shortlistCalls(cands, []prescreenRow{row("UP", "sp500", 1, 10), row("DOWN", "sp500", -1, 20)})
	if len(got) != 2 {
		t.Fatalf("got %d calls, want 2", len(got))
	}
	if got[0].direction != model.DirectionBuy || got[1].direction != model.DirectionSell {
		t.Errorf("directions = %s, %s; want BUY then SELL", got[0].direction, got[1].direction)
	}
	if got[0].anchor != 10 || got[1].anchor != 20 {
		t.Errorf("anchors = %.0f, %.0f; want the pre-screen closes 10 and 20", got[0].anchor, got[1].anchor)
	}
}

func TestVerdictRefusesToConcludeFromThinArms(t *testing.T) {
	// Three arms of five calls will differ by twenty points on noise alone. The
	// refusal is the point: a reader who takes that as a finding has been misled
	// by a number this file produced.
	thin := &ControlReport{Arms: []ControlArm{
		{Name: "composite", Record: HorizonRecord{N: 5, HitRate: 0.4}},
		{Name: "shortlist", Record: HorizonRecord{N: 6, HitRate: 0.5}},
		{Name: "shipped", Record: HorizonRecord{N: 5, HitRate: 0.8}},
	}}
	if got := thin.verdict(); !strings.Contains(got, "noise at these counts") {
		t.Errorf("verdict on n=5 arms did not warn:\n%s", got)
	}

	thick := &ControlReport{Arms: []ControlArm{
		{Name: "composite", Record: HorizonRecord{N: 40, HitRate: 0.5, AvgExcess: 0}},
		{Name: "shortlist", Record: HorizonRecord{N: 40, HitRate: 0.52, AvgExcess: 0.5}},
		{Name: "shipped", Record: HorizonRecord{N: 40, HitRate: 0.6, AvgExcess: 1.5}},
	}}
	got := thick.verdict()
	if strings.Contains(got, "noise at these counts") {
		t.Errorf("verdict on n=40 arms warned anyway:\n%s", got)
	}
	if !strings.Contains(got, "Whole model stack adds:") || !strings.Contains(got, "Specialists and Chief add:") {
		t.Errorf("verdict is missing one of its two comparisons:\n%s", got)
	}
}
