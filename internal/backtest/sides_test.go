package backtest

import (
	"math"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// sideRecord builds a minimal Record for sidesStudy/pickTrades: only the
// fields either reads (Date, Index, Ticker, Sig[SigScore], SigmaDaily, P,
// XS[2], BX[2]) are set; everything else is the zero value.
func sideRecord(date, index, ticker string, score, xs15, bx15, sigma float64) Record {
	r := Record{Date: date, Index: index, Ticker: ticker, P: 0, SigmaDaily: sigma}
	for k := range r.Sig {
		r.Sig[k] = math.NaN()
	}
	r.Sig[SigScore] = score
	r.XS[2], r.BX[2] = xs15, bx15
	return r
}

// sideSeries gives a ticker enough bars past P=0 for pickTrades' forward path
// (P+1 .. P+barrierHorizon); the bar values themselves are never read by
// sidesStudy.
func sideSeries(sym string) *quant.Series {
	bars := make([]quant.Bar, barrierHorizon+5)
	for i := range bars {
		bars[i] = bar(100, 100, 100, 100)
	}
	return &quant.Series{Symbol: sym, Bars: bars}
}

// TestSidesStudySplitsLongShortPlainAndBeta checks E3's arithmetic on a hand-
// computed synthetic panel: two dates (one per half), four picks each, a mix
// of long and short and one beta-undefined pick, and confirms the overall
// plain split matches the barrier study's own XS15Long/ShortGrossPct — proof
// sidesStudy reads pickTrades' picks rather than re-selecting them.
func TestSidesStudySplitsLongShortPlainAndBeta(t *testing.T) {
	const mid = "2020-02-01"
	recs := []Record{
		// H1 (before mid)
		sideRecord("2020-01-03", "sp500", "T1", 2, 0.05, 0.04, 0.02),     // long
		sideRecord("2020-01-03", "sp500", "T2", 1, 0.03, 0.02, 0.02),     // long
		sideRecord("2020-01-03", "sp500", "T3", -2, -0.02, -0.015, 0.02), // short
		sideRecord("2020-01-03", "sp500", "T4", -1, 0.01, 0.005, 0.02),   // short
		// H2 (on/after mid)
		sideRecord("2020-02-07", "sp500", "T5", 3, 0.10, math.NaN(), 0.02), // long, beta undefined
		sideRecord("2020-02-07", "sp500", "T6", 2, -0.02, -0.01, 0.02),     // long
		sideRecord("2020-02-07", "sp500", "T7", -3, -0.05, -0.04, 0.02),    // short
		sideRecord("2020-02-07", "sp500", "T8", -2, 0.02, 0.03, 0.02),      // short
	}
	series := map[string]*quant.Series{}
	for _, r := range recs {
		series[r.Ticker] = sideSeries(r.Ticker)
	}

	got := sidesStudy(recs, series, mid)

	approxEq := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }
	check := func(label string, s SideStats, nl, ns int, lp, sp, lb, sb float64) {
		t.Helper()
		if s.NLong != nl || s.NShort != ns {
			t.Errorf("%s: n_long/n_short = %d/%d, want %d/%d", label, s.NLong, s.NShort, nl, ns)
		}
		if !approxEq(float64(s.LongPlainPct), lp) {
			t.Errorf("%s: LongPlainPct = %v, want %v", label, s.LongPlainPct, lp)
		}
		if !approxEq(float64(s.ShortPlainPct), sp) {
			t.Errorf("%s: ShortPlainPct = %v, want %v", label, s.ShortPlainPct, sp)
		}
		if !approxEq(float64(s.LongBetaPct), lb) {
			t.Errorf("%s: LongBetaPct = %v, want %v", label, s.LongBetaPct, lb)
		}
		if !approxEq(float64(s.ShortBetaPct), sb) {
			t.Errorf("%s: ShortBetaPct = %v, want %v", label, s.ShortBetaPct, sb)
		}
	}

	check("H1", got.H1, 2, 2, 4.0, 0.5, 3.0, 0.5)
	check("H2", got.H2, 2, 2, 4.0, 1.5, -1.0, 0.5)
	check("all", got.All, 4, 4, 4.0, 1.0, 5.0/3.0, 0.5)

	// Parity: the overall plain split must equal the barrier study's own
	// XS15Long/ShortGrossPct on the same trades — sidesStudy must not
	// duplicate pickTrades' selection or produce a divergent number from it.
	barRep := barrierStudy(recs, series, mid)
	if !approxEq(float64(got.All.LongPlainPct), float64(barRep.XS15LongGrossPct)) {
		t.Errorf("All.LongPlainPct = %v, barrier XS15LongGrossPct = %v: should match (same trades)", got.All.LongPlainPct, barRep.XS15LongGrossPct)
	}
	if !approxEq(float64(got.All.ShortPlainPct), float64(barRep.XS15ShortGrossPct)) {
		t.Errorf("All.ShortPlainPct = %v, barrier XS15ShortGrossPct = %v: should match (same trades)", got.All.ShortPlainPct, barRep.XS15ShortGrossPct)
	}
}

// TestSidesStudyEmptySideIsNaNNotZero checks that a side with no picks reports
// NaN (absent once marshalled, per Num), not a misleading zero.
func TestSidesStudyEmptySideIsNaNNotZero(t *testing.T) {
	recs := []Record{
		sideRecord("2020-01-03", "sp500", "T1", 1, 0.02, 0.01, 0.02),
	}
	series := map[string]*quant.Series{"T1": sideSeries("T1")}
	got := sidesStudy(recs, series, "2020-02-01")
	if got.All.NShort != 0 {
		t.Fatalf("NShort = %d, want 0", got.All.NShort)
	}
	if !math.IsNaN(float64(got.All.ShortPlainPct)) || !math.IsNaN(float64(got.All.ShortBetaPct)) {
		t.Errorf("empty short side must be NaN, got plain=%v beta=%v", got.All.ShortPlainPct, got.All.ShortBetaPct)
	}
}
