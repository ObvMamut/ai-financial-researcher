package orchestrator

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// A benchmark that has been quiet all year and turns violent in its last month
// reads stressed; its regime line carries both numbers and the trend sign.
func TestComputedRegimeReadsVolAgainstItsOwnYear(t *testing.T) {
	s := &quant.Series{Symbol: "^GSPC"}
	price := 100.0
	for i := 0; i < 300; i++ {
		step := 0.002
		if i%2 == 1 {
			step = -0.0015
		}
		if i >= 279 { // the last 21 sessions swing ten times harder
			step *= 10
		}
		price *= math.Exp(step)
		s.Bars = append(s.Bars, quant.Bar{Date: fmt.Sprintf("d%03d", i), Close: price})
	}
	now, median, ok := realizedVolVsYear(s)
	if !ok || now <= median {
		t.Fatalf("now %.3f vs median %.3f (ok=%v): the last month should read above its year", now, median, ok)
	}
	p := quant.NewPack()
	p.Benchmarks["^GSPC"] = quant.Metrics{Ret63d: -0.042, AsOf: "2026-09-23"}
	lines := computedRegimeLines(p, map[string]*quant.Series{"^GSPC": s})
	if len(lines) != 1 || !strings.Contains(lines[0], "-4.2% (down)") || !strings.Contains(lines[0], "(stressed)") {
		t.Errorf("regime line = %v", lines)
	}
	if !strings.Contains(regimeBlock(lines), "replaces the macro specialist") {
		t.Error("the regime block does not say what it replaces")
	}
}

// The book holds each sector to the risk gate's own limit, and a vetoed name's
// slot goes to the next eligible name in merit order.
func TestPickBookHonoursSectorCapAndVetoes(t *testing.T) {
	eligible := []model.TradeIdea{{Ticker: "A"}, {Ticker: "B"}, {Ticker: "C"}, {Ticker: "D"}, {Ticker: "E"}}
	sector := map[string]string{"A": "Tech", "B": "Tech", "C": "Tech", "D": "Energy", "E": "Health"}
	book, capped := pickBook(eligible, sector, map[string]bool{"D": true}, 3, 2)
	var got []string
	for i, b := range book {
		got = append(got, b.Ticker)
		if b.Rank != i+1 {
			t.Errorf("rank %d on position %d", b.Rank, i+1)
		}
	}
	if strings.Join(got, ",") != "A,B,E" || !capped["C"] {
		t.Errorf("book = %v capped = %v, want A,B,E with C capped", got, capped)
	}
}
