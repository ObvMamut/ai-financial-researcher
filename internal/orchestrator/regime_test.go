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
	book, capped, _ := pickBook(eligible, sector, map[string]bool{"D": true}, 3, 2, nil, 1)
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

// corrBookSeries builds two paths: noise-free copies of one return stream (rho ~ 1)
// and an unrelated one.
func corrBookSeries() map[string]*quant.Series {
	base := make([]float64, 60)
	other := make([]float64, 60)
	for i := range base {
		base[i] = 0.01 * math.Sin(float64(i)*0.7)
		other[i] = 0.01 * math.Cos(float64(i)*1.9)
	}
	near := make([]float64, 60)
	for i := range near {
		near[i] = base[i] * 1.02
	}
	return map[string]*quant.Series{
		"A": seriesOf("A", 100, base), "B": seriesOf("B", 50, near), "C": seriesOf("C", 80, other),
	}
}

func bookTickers(book []model.TradeIdea) string {
	var got []string
	for _, b := range book {
		got = append(got, b.Ticker)
	}
	return strings.Join(got, ",")
}

func TestPickBookSkipsASameDirectionCorrelatedName(t *testing.T) {
	eligible := []model.TradeIdea{
		{Ticker: "A", Direction: model.DirectionBuy}, {Ticker: "B", Direction: model.DirectionBuy}, {Ticker: "C", Direction: model.DirectionBuy}}
	book, _, corr := pickBook(eligible, nil, nil, 2, 0, corrBookSeries(), 0.75)
	if got := bookTickers(book); got != "A,C" {
		t.Fatalf("book = %s, want A,C", got)
	}
	if p := corr["B"]; p.With != "A" || p.Rho <= 0.75 {
		t.Errorf("B correlated = %+v, want partner A above 0.75", p)
	}
}

func TestPickBookKeepsAnOppositeDirectionCorrelatedPair(t *testing.T) {
	eligible := []model.TradeIdea{{Ticker: "A", Direction: model.DirectionBuy}, {Ticker: "B", Direction: model.DirectionSell}}
	book, _, corr := pickBook(eligible, nil, nil, 2, 0, corrBookSeries(), 0.75)
	if got := bookTickers(book); got != "A,B" || len(corr) != 0 {
		t.Errorf("book = %s corr = %v, want both shipped", got, corr)
	}
}

func TestPickBookCorrelationCeilingOfOneDisables(t *testing.T) {
	eligible := []model.TradeIdea{{Ticker: "A", Direction: model.DirectionBuy}, {Ticker: "B", Direction: model.DirectionBuy}}
	book, _, corr := pickBook(eligible, nil, nil, 2, 0, corrBookSeries(), 1)
	if got := bookTickers(book); got != "A,B" || len(corr) != 0 {
		t.Errorf("book = %s corr = %v, want both shipped", got, corr)
	}
}

func TestPickBookDoesNotSkipANameWithNoSeries(t *testing.T) {
	eligible := []model.TradeIdea{{Ticker: "A", Direction: model.DirectionBuy}, {Ticker: "Z", Direction: model.DirectionBuy}}
	book, _, corr := pickBook(eligible, nil, nil, 2, 0, corrBookSeries(), 0.75)
	if got := bookTickers(book); got != "A,Z" || len(corr) != 0 {
		t.Errorf("book = %s corr = %v, want both shipped", got, corr)
	}
}

// The refill after a Chief veto goes through the same pickBook, so a reserve
// that correlates with a name still in the book is skipped in favour of the next.
func TestPickBookRefillAfterAVetoRespectsCorrelation(t *testing.T) {
	s := corrBookSeries()
	eligible := []model.TradeIdea{
		{Ticker: "X", Direction: model.DirectionBuy}, {Ticker: "A", Direction: model.DirectionBuy},
		{Ticker: "B", Direction: model.DirectionBuy}, {Ticker: "C", Direction: model.DirectionBuy}}
	s["X"] = s["C"]
	book, _, _ := pickBook(eligible, nil, map[string]bool{"X": true}, 2, 0, s, 0.75)
	if got := bookTickers(book); got != "A,C" {
		t.Errorf("book = %s, want A,C (B correlates with A)", got)
	}
}
