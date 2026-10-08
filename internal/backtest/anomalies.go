package backtest

import (
	"math"

	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// The v8 anomaly signals N1–N3, registered in docs/workflow/backtest.md
// ("Pre-registered new anomalies N1–N3") before this code existed. The
// formulas there are binding; these helpers compute the raw quantities and
// observe orients them so that higher is bullish.
//
// Every helper reads only bars[:len(bars)] of what it is given. The series
// observe passes is a slice of the full history that shares its backing array
// with the future, so reslicing past len would read bars after the date.

// maxDailyReturn is N1's MAX: the largest simple daily return C[i]/C[i−1]−1
// over the last n sessions (Bali, Cakici & Whitelaw 2011). NaN with fewer than
// n+1 bars or a non-positive close in the window.
func maxDailyReturn(bars []quant.Bar, n int) float64 {
	if n <= 0 || len(bars) < n+1 {
		return math.NaN()
	}
	best := math.Inf(-1)
	for i := len(bars) - n; i < len(bars); i++ {
		c0, c1 := bars[i-1].Close, bars[i].Close
		if c0 <= 0 || c1 <= 0 {
			return math.NaN()
		}
		best = math.Max(best, c1/c0-1)
	}
	return best
}

// residualVol is N2's IVOL: the sample standard deviation, with n−2 degrees of
// freedom, of the OLS residuals of the last n date-aligned daily log-return
// pairs of s on its benchmark (Ang, Hodrick, Xing & Zhang 2006). The pairs are
// quant.AlignedReturns', the same pairing beta uses. NaN with no benchmark,
// fewer than n pairs, or zero benchmark variance in the window — never 0.
func residualVol(s, bench *quant.Series, n int) float64 {
	if s == nil || bench == nil || len(bench.Bars) == 0 || n < 3 {
		return math.NaN()
	}
	ra, rb := quant.AlignedReturns(s, bench)
	if len(ra) < n {
		return math.NaN()
	}
	ra, rb = ra[len(ra)-n:], rb[len(rb)-n:]
	var ma, mb float64
	for i := range ra {
		ma += ra[i]
		mb += rb[i]
	}
	ma /= float64(n)
	mb /= float64(n)
	var sxy, sxx float64
	for i := range ra {
		sxy += (rb[i] - mb) * (ra[i] - ma)
		sxx += (rb[i] - mb) * (rb[i] - mb)
	}
	if !(sxx > 0) {
		return math.NaN()
	}
	beta := sxy / sxx
	var ss float64
	for i := range ra {
		e := (ra[i] - ma) - beta*(rb[i]-mb)
		ss += e * e
	}
	return math.Sqrt(ss / float64(n-2))
}

// fipSignal is N3, frog-in-the-pan (Da, Gurun & Warachka 2014), already
// oriented: −mom·ID with ID = sign(mom)·(%down − %up) over the 231 daily close
// changes inside mom12-1's own window, bars t−252..t−21 of the cut series.
// Flat days count only in the denominator. It equals |mom|·(%up − %down).
// NaN with fewer than 253 bars or a NaN momentum.
func fipSignal(bars []quant.Bar, mom float64) float64 {
	const far, near = 252, 21
	n := len(bars)
	if n < far+1 || math.IsNaN(mom) || math.IsInf(mom, 0) {
		return math.NaN()
	}
	var up, down int
	for i := n - far; i <= n-1-near; i++ {
		switch c0, c1 := bars[i-1].Close, bars[i].Close; {
		case c1 > c0:
			up++
		case c1 < c0:
			down++
		}
	}
	sign := 0.0
	switch {
	case mom > 0:
		sign = 1
	case mom < 0:
		sign = -1
	}
	id := sign * float64(down-up) / float64(far-near)
	return -mom * id
}
