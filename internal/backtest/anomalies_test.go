package backtest

import (
	"math"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// barsFromCloses builds daily bars on consecutive calendar days, so two series
// built from the same start share every date.
func barsFromCloses(closes []float64) []quant.Bar {
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]quant.Bar, len(closes))
	for i, c := range closes {
		out[i] = quant.Bar{Date: start.AddDate(0, 0, i).Format("2006-01-02"), Open: c, High: c, Low: c, Close: c, Volume: 1}
	}
	return out
}

func flatCloses(n int, c float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = c
	}
	return out
}

func TestMaxDailyReturn(t *testing.T) {
	if got := maxDailyReturn(barsFromCloses(flatCloses(30, 50)), 21); got != 0 {
		t.Errorf("flat closes: MAX = %v, want 0", got)
	}

	// A +8% spike five sessions ago, a +3% day, and a larger +20% day that sits
	// one bar outside the 21-session window and must not count.
	closes := flatCloses(40, 100)
	closes[17] = 120 // change 16→17 is +20%: bar 17 is outside the last 21 changes (19..39)
	for i := 18; i < 40; i++ {
		closes[i] = 120
	}
	closes[35] = 129.6 // +8% on bar 35
	for i := 36; i < 40; i++ {
		closes[i] = 129.6
	}
	closes[38] = 129.6 * 1.03 // +3%, then back down
	closes[39] = 129.6
	got := maxDailyReturn(barsFromCloses(closes), 21)
	if math.Abs(got-0.08) > 1e-12 {
		t.Errorf("spike: MAX = %v, want 0.08", got)
	}
	// Widening the window to 23 changes reaches bar 17's +20%.
	if got := maxDailyReturn(barsFromCloses(closes), 23); math.Abs(got-0.2) > 1e-12 {
		t.Errorf("wide window: MAX = %v, want 0.2", got)
	}

	// The window is the last 21 changes, so 22 bars is the minimum.
	if got := maxDailyReturn(barsFromCloses(flatCloses(21, 10)), 21); !math.IsNaN(got) {
		t.Errorf("21 bars: MAX = %v, want NaN", got)
	}
	if got := maxDailyReturn(barsFromCloses(flatCloses(22, 10)), 21); got != 0 {
		t.Errorf("22 bars: MAX = %v, want 0", got)
	}
	bad := flatCloses(30, 10)
	bad[25] = 0
	if got := maxDailyReturn(barsFromCloses(bad), 21); !math.IsNaN(got) {
		t.Errorf("non-positive close: MAX = %v, want NaN", got)
	}
}

// ivolFixture builds a benchmark and a stock whose last n log returns are
// exactly r = a + β·r_b + e, with e orthogonal to a constant and to r_b, so
// the OLS residuals are e itself. Earlier history follows a different model,
// which must not leak into the window.
func ivolFixture(n, extra int) (stock, bench *quant.Series, sigma float64) {
	const a, beta = 0.0002, 1.3
	total := n + extra
	rb := make([]float64, total)
	ra := make([]float64, total)
	for i := range rb {
		rb[i] = 0.01 * math.Sin(1.7*float64(i)+0.3)
	}
	e := make([]float64, n)
	for i := range e {
		e[i] = 0.004*math.Cos(2.9*float64(i)) + 0.002*math.Sin(0.37*float64(i*i))
	}
	// Project e off span{1, r_b(window)}.
	w := rb[extra:]
	var mb float64
	for _, x := range w {
		mb += x
	}
	mb /= float64(n)
	var me float64
	for _, x := range e {
		me += x
	}
	me /= float64(n)
	var sxy, sxx float64
	for i := range e {
		sxy += (w[i] - mb) * (e[i] - me)
		sxx += (w[i] - mb) * (w[i] - mb)
	}
	k := sxy / sxx
	var ss float64
	for i := range e {
		e[i] = (e[i] - me) - k*(w[i]-mb)
		ss += e[i] * e[i]
	}
	sigma = math.Sqrt(ss / float64(n-2))

	for i := 0; i < extra; i++ {
		ra[i] = -0.5*rb[i] + 0.03*math.Sin(float64(i))
	}
	for i := 0; i < n; i++ {
		ra[extra+i] = a + beta*w[i] + e[i]
	}
	cs, cb := make([]float64, total+1), make([]float64, total+1)
	cs[0], cb[0] = 50, 4000
	for i := 0; i < total; i++ {
		cs[i+1] = cs[i] * math.Exp(ra[i])
		cb[i+1] = cb[i] * math.Exp(rb[i])
	}
	return &quant.Series{Symbol: "S", Bars: barsFromCloses(cs)}, &quant.Series{Symbol: "B", Bars: barsFromCloses(cb)}, sigma
}

func TestResidualVol(t *testing.T) {
	stock, bench, want := ivolFixture(63, 120)
	got := residualVol(stock, bench, 63)
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("IVOL = %.15g, want %.15g (diff %g)", got, want, got-want)
	}
	if want <= 0 {
		t.Fatalf("fixture: σ(e) = %v", want)
	}

	// Exactly 63 pairs suffices; 62 does not.
	s63, b63, want63 := ivolFixture(63, 0)
	if got := residualVol(s63, b63, 63); math.Abs(got-want63) > 1e-12 {
		t.Errorf("63 pairs: IVOL = %v, want %v", got, want63)
	}
	short := &quant.Series{Symbol: "B", Bars: b63.Bars[1:]}
	if got := residualVol(s63, short, 63); !math.IsNaN(got) {
		t.Errorf("short benchmark (62 pairs): IVOL = %v, want NaN", got)
	}

	if got := residualVol(stock, nil, 63); !math.IsNaN(got) {
		t.Errorf("nil benchmark: IVOL = %v, want NaN", got)
	}
	if got := residualVol(stock, &quant.Series{Symbol: "B"}, 63); !math.IsNaN(got) {
		t.Errorf("empty benchmark: IVOL = %v, want NaN", got)
	}
	if got := residualVol(nil, bench, 63); !math.IsNaN(got) {
		t.Errorf("nil series: IVOL = %v, want NaN", got)
	}
	flat := &quant.Series{Symbol: "B", Bars: barsFromCloses(flatCloses(len(bench.Bars), 4000))}
	if got := residualVol(stock, flat, 63); !math.IsNaN(got) {
		t.Errorf("flat benchmark: IVOL = %v, want NaN", got)
	}
}

func TestFIPSignal(t *testing.T) {
	// 300 bars: n−1−252 = 47 and n−1−21 = 278, so the window's 231 changes are
	// bars 48..278 against their predecessors. Outside it the series moves
	// wildly, which must not count.
	const n = 300
	closes := make([]float64, n)
	closes[0] = 100
	for i := 1; i < n; i++ {
		switch {
		case i < 48 || i > 278:
			closes[i] = closes[i-1] * 0.9 // outside the window: all down
		case i-48 < 120:
			closes[i] = closes[i-1] * 1.01 // 120 up days
		case i-48 < 120+40:
			closes[i] = closes[i-1] * 0.99 // 40 down days
		default:
			closes[i] = closes[i-1] // 71 flat days
		}
	}
	bars := barsFromCloses(closes)
	up, down := 120.0, 40.0
	for _, mom := range []float64{0.25, -0.25} {
		sign := 1.0
		if mom < 0 {
			sign = -1
		}
		id := sign * (down - up) / 231
		want := -mom * id
		got := fipSignal(bars, mom)
		if math.Abs(got-want) > 1e-15 {
			t.Errorf("mom %v: FIP = %v, want %v", mom, got, want)
		}
		// Both reduce to |mom|·(%up − %down).
		if math.Abs(got-math.Abs(mom)*(up-down)/231) > 1e-15 {
			t.Errorf("mom %v: FIP = %v is not |mom|·(%%up−%%down)", mom, got)
		}
	}
	if got := fipSignal(bars, 0); got != 0 {
		t.Errorf("zero momentum: FIP = %v, want 0", got)
	}

	// The mirror image: more down days than up gives a negative reading.
	for i := 48; i <= 278; i++ {
		if closes[i] != closes[i-1] {
			bars[i].Close = bars[i-1].Close * (2 - closes[i]/closes[i-1]) // 1.01 ↔ 0.99
		} else {
			bars[i].Close = bars[i-1].Close
		}
	}
	if got, want := fipSignal(bars, -0.1), 0.1*(down-up)/231; math.Abs(got-want) > 1e-15 {
		t.Errorf("mirrored: FIP = %v, want %v", got, want)
	}

	if got := fipSignal(bars[:252], 0.25); !math.IsNaN(got) {
		t.Errorf("252 bars: FIP = %v, want NaN", got)
	}
	if got := fipSignal(bars[:253], 0.25); math.IsNaN(got) {
		t.Errorf("253 bars: FIP is NaN, want finite")
	}
	if got := fipSignal(bars, math.NaN()); !math.IsNaN(got) {
		t.Errorf("NaN momentum: FIP = %v, want NaN", got)
	}
}
