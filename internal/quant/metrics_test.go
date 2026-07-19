package quant

import (
	"math"
	"strings"
	"testing"
	"time"
)

// lcg is a tiny deterministic PRNG so golden values are stable across platforms.
type lcg struct{ state uint64 }

func (l *lcg) next() float64 { // uniform in [0, 1)
	l.state = l.state*6364136223846793005 + 1442695040888963407
	return float64(l.state>>11) / float64(1<<53)
}

func (l *lcg) sym() float64 { return 2*l.next() - 1 } // uniform in [-1, 1)

// seriesFromCloses builds a Series with synthetic OHLC around the closes.
func seriesFromCloses(symbol string, closes []float64) *Series {
	s := &Series{Symbol: symbol}
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	prev := closes[0]
	for i, c := range closes {
		o := prev
		hi := math.Max(o, c) * 1.001
		lo := math.Min(o, c) * 0.999
		s.Bars = append(s.Bars, Bar{
			Date: t0.AddDate(0, 0, i).Format("2006-01-02"),
			Open: o, High: hi, Low: lo, Close: c, Volume: 1e6,
		})
		prev = c
	}
	return s
}

func seriesFromLogReturns(symbol string, start float64, rets []float64) *Series {
	closes := make([]float64, len(rets)+1)
	closes[0] = start
	for i, r := range rets {
		closes[i+1] = closes[i] * math.Exp(r)
	}
	return seriesFromCloses(symbol, closes)
}

func approx(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v ± %v", name, got, want, tol)
	}
}

func TestDriftSeriesMomentum(t *testing.T) {
	// Constant daily log return μ: every window return is exp(nμ)−1 exactly.
	const mu = 0.001
	rets := make([]float64, 300)
	for i := range rets {
		rets[i] = mu
	}
	s := seriesFromLogReturns("DRIFT", 100, rets)
	m := Compute(s, nil)

	approx(t, "Ret5d", m.Ret5d, math.Exp(5*mu)-1, 1e-9)
	approx(t, "Ret21d", m.Ret21d, math.Exp(21*mu)-1, 1e-9)
	approx(t, "Ret252d", m.Ret252d, math.Exp(252*mu)-1, 1e-9)
	// 12-1 momentum spans t-252 → t-21: 231 days of drift.
	approx(t, "Mom12_1", m.Mom12_1, math.Exp(231*mu)-1, 1e-9)
	if m.PriceTo52wHigh < 0.99 || m.PriceTo52wHigh > 1.0 {
		t.Errorf("PriceTo52wHigh = %v, want ≈ 1 for a monotonic riser", m.PriceTo52wHigh)
	}
	if m.MaxDrawdown126 != 0 {
		t.Errorf("MaxDrawdown126 = %v, want 0 for a monotonic riser", m.MaxDrawdown126)
	}
}

func TestIIDNoiseIsRandomWalk(t *testing.T) {
	rng := &lcg{state: 42}
	rets := make([]float64, 504)
	for i := range rets {
		rets[i] = 0.01 * rng.sym()
	}
	s := seriesFromLogReturns("NOISE", 100, rets)
	m := Compute(s, nil)

	approx(t, "VR5", m.VR5, 1, 0.25)
	approx(t, "VR10", m.VR10, 1, 0.35)
	if m.Regime != "random-walk" {
		t.Errorf("Regime = %q, want random-walk (VR5=%v)", m.Regime, m.VR5)
	}
	approx(t, "Skew252", m.Skew252, 0, 0.5)
}

func TestMeanRevertingRegime(t *testing.T) {
	// Alternating ±1%: q-period sums nearly cancel → VR ≪ 1.
	rets := make([]float64, 504)
	for i := range rets {
		if i%2 == 0 {
			rets[i] = 0.01
		} else {
			rets[i] = -0.01
		}
	}
	s := seriesFromLogReturns("REVERT", 100, rets)
	m := Compute(s, nil)

	if m.VR5 >= 0.5 {
		t.Errorf("VR5 = %v, want ≪ 1 for alternating returns", m.VR5)
	}
	if m.Regime != "mean-reverting" {
		t.Errorf("Regime = %q, want mean-reverting", m.Regime)
	}
}

func TestTrendingRegime(t *testing.T) {
	// AR(1) with φ=0.9 → strong positive autocorrelation → VR(5) ≫ 1.
	rng := &lcg{state: 7}
	rets := make([]float64, 504)
	prev := 0.0
	for i := range rets {
		prev = 0.9*prev + 0.002*rng.sym()
		rets[i] = prev
	}
	s := seriesFromLogReturns("TREND", 100, rets)
	m := Compute(s, nil)

	if m.VR5 <= 1.5 {
		t.Errorf("VR5 = %v, want ≫ 1 for AR(1) φ=0.9", m.VR5)
	}
	if m.Regime != "trending" {
		t.Errorf("Regime = %q, want trending", m.Regime)
	}
}

func TestYangZhangZeroOnFlatSeries(t *testing.T) {
	closes := make([]float64, 300)
	for i := range closes {
		closes[i] = 100
	}
	s := seriesFromCloses("FLAT", closes)
	// Flatten the synthetic OHLC noise too.
	for i := range s.Bars {
		s.Bars[i].Open, s.Bars[i].High, s.Bars[i].Low = 100, 100, 100
	}
	m := Compute(s, nil)
	approx(t, "VolYZ20", m.VolYZ20, 0, 1e-12)
	if m.SigmaDaily != 0 || len(m.Distances) != 0 {
		t.Errorf("flat series should have no distances, got σ=%v %v", m.SigmaDaily, m.Distances)
	}
}

func TestYangZhangMagnitude(t *testing.T) {
	// IID ~1%-scale daily returns: YZ vol should land in the same ballpark as
	// close-to-close vol (well within a factor of 2).
	rng := &lcg{state: 99}
	rets := make([]float64, 300)
	for i := range rets {
		rets[i] = 0.01 * rng.sym()
	}
	s := seriesFromLogReturns("VOL", 100, rets)
	m := Compute(s, nil)

	cc := stddev(tail(rets, 20)) * math.Sqrt(252)
	if m.VolYZ20 < cc/2 || m.VolYZ20 > cc*2 {
		t.Errorf("VolYZ20 = %v, close-close vol = %v — outside sanity band", m.VolYZ20, cc)
	}
	// Distances table consistency.
	approx(t, "1s_h5", m.Distances["1s_h5"], m.SigmaDaily*math.Sqrt(5), 1e-12)
	approx(t, "2s_h20", m.Distances["2s_h20"], 2*m.SigmaDaily*math.Sqrt(20), 1e-12)
}

func TestMaxDrawdown(t *testing.T) {
	var closes []float64
	for i := 0; i <= 100; i++ { // rise to exactly 100
		closes = append(closes, 50+float64(i)*0.5)
	}
	for i := 0; i < 20; i++ { // fall to 80
		closes = append(closes, 100-float64(i+1))
	}
	for i := 0; i < 30; i++ { // partial recovery
		closes = append(closes, 80+float64(i)*0.2)
	}
	s := seriesFromCloses("DD", closes)
	m := Compute(s, nil)
	approx(t, "MaxDrawdown126", m.MaxDrawdown126, -0.20, 0.001)
}

func TestBetaAndCorrelation(t *testing.T) {
	rng := &lcg{state: 1234}
	bench := make([]float64, 300)
	stock := make([]float64, 300)
	for i := range bench {
		r := 0.008 * rng.sym()
		bench[i] = r
		stock[i] = 2 * r // exact 2× exposure
	}
	sb := seriesFromLogReturns("BENCH", 1000, bench)
	ss := seriesFromLogReturns("STOCK", 100, stock)
	m := Compute(ss, sb)

	approx(t, "Beta", m.Beta, 2, 0.01)
	approx(t, "Corr", m.Corr, 1, 0.001)
	if m.Benchmark != "BENCH" {
		t.Errorf("Benchmark = %q, want BENCH", m.Benchmark)
	}
}

func TestShortHistoryFlagsInsteadOfPanic(t *testing.T) {
	s := seriesFromCloses("TINY", []float64{100, 101, 102})
	m := Compute(s, nil)
	if len(m.Flags) == 0 {
		t.Error("expected flags for 3-bar series")
	}
	if m.LastClose != 102 {
		t.Errorf("LastClose = %v, want 102", m.LastClose)
	}
}

func TestPackMarkdownAndCompact(t *testing.T) {
	rng := &lcg{state: 5}
	rets := make([]float64, 300)
	for i := range rets {
		rets[i] = 0.01 * rng.sym()
	}
	s := seriesFromLogReturns("NVDA", 100, rets)
	p := NewPack()
	m := Compute(s, nil)
	p.ByTicker["NVDA"] = m
	p.AsOf = m.AsOf

	md := p.Markdown()
	for _, want := range []string{"### Verified Market Data", "#### NVDA", "Variance ratio", "Yang-Zhang"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown missing %q", want)
		}
	}
	if line := p.CompactLine("nvda"); !strings.Contains(line, "NVDA") {
		t.Errorf("CompactLine should resolve case-insensitively, got %q", line)
	}
	if empty := NewPack().Markdown(); empty != "" {
		t.Errorf("empty pack Markdown = %q, want \"\"", empty)
	}
}
