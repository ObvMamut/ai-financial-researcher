package backtest

import (
	"math"
	"sort"
)

// minCrossSection is the fewest paired observations a per-date IC or quintile
// spread is computed from. Below it a rank correlation is mostly noise.
const minCrossSection = 15

// meanSD returns the mean and the sample (n−1) standard deviation of the finite
// values in xs; NaN for either when there are too few.
func meanSD(xs []float64) (float64, float64) {
	var sum float64
	n := 0
	for _, x := range xs {
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			sum += x
			n++
		}
	}
	if n == 0 {
		return math.NaN(), math.NaN()
	}
	m := sum / float64(n)
	if n < 2 {
		return m, math.NaN()
	}
	var ss float64
	for _, x := range xs {
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			ss += (x - m) * (x - m)
		}
	}
	return m, math.Sqrt(ss / float64(n-1))
}

// finite drops NaN and ±Inf, keeping order.
func finite(xs []float64) []float64 {
	out := make([]float64, 0, len(xs))
	for _, x := range xs {
		if !math.IsNaN(x) && !math.IsInf(x, 0) {
			out = append(out, x)
		}
	}
	return out
}

// averageRanks ranks xs from 1, giving tied values the mean of the ranks they
// span (pandas' default "average" method).
func averageRanks(xs []float64) []float64 {
	idx := make([]int, len(xs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return xs[idx[a]] < xs[idx[b]] })
	ranks := make([]float64, len(xs))
	for i := 0; i < len(idx); {
		j := i
		for j+1 < len(idx) && xs[idx[j+1]] == xs[idx[i]] {
			j++
		}
		r := float64(i+j)/2 + 1
		for k := i; k <= j; k++ {
			ranks[idx[k]] = r
		}
		i = j + 1
	}
	return ranks
}

// pearson is the Pearson correlation of two equal-length slices; NaN when
// either has no variance.
func pearson(a, b []float64) float64 {
	ma, _ := meanSD(a)
	mb, _ := meanSD(b)
	var sab, saa, sbb float64
	for i := range a {
		da, db := a[i]-ma, b[i]-mb
		sab += da * db
		saa += da * da
		sbb += db * db
	}
	if saa == 0 || sbb == 0 {
		return math.NaN()
	}
	return sab / math.Sqrt(saa*sbb)
}

// pairs keeps the positions where both x and y are finite.
func pairs(x, y []float64) ([]float64, []float64) {
	var a, b []float64
	for i := range x {
		if math.IsNaN(x[i]) || math.IsNaN(y[i]) || math.IsInf(x[i], 0) || math.IsInf(y[i], 0) {
			continue
		}
		a = append(a, x[i])
		b = append(b, y[i])
	}
	return a, b
}

// Spearman is the rank correlation of the finite pairs in x and y, NaN below
// minCrossSection pairs.
func Spearman(x, y []float64) float64 {
	a, b := pairs(x, y)
	if len(a) < minCrossSection {
		return math.NaN()
	}
	return pearson(averageRanks(a), averageRanks(b))
}

// quintileSpread is the mean of y over the top fifth of x less its mean over
// the bottom fifth, plus the top fifth's own mean. Ties in x are broken by
// position, and the bins are pandas.qcut's on ranks 1..n, so the two reports
// assign the same names to the same buckets.
func quintileSpread(x, y []float64) (spread, top float64) {
	a, b := pairs(x, y)
	n := len(a)
	if n < minCrossSection {
		return math.NaN(), math.NaN()
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(p, q int) bool { return a[idx[p]] < a[idx[q]] })
	var topSum, botSum float64
	var topN, botN int
	for pos, i := range idx {
		switch quintileOf(float64(pos+1), n) {
		case 4:
			topSum += b[i]
			topN++
		case 0:
			botSum += b[i]
			botN++
		}
	}
	if topN == 0 || botN == 0 {
		return math.NaN(), math.NaN()
	}
	return topSum/float64(topN) - botSum/float64(botN), topSum / float64(topN)
}

// quintileOf places rank r of n into qcut's five equal-frequency bins, whose
// edges on the values 1..n are 1 + (n−1)·k/5 and whose intervals are closed on
// the right (the first also on the left).
func quintileOf(r float64, n int) int {
	for k := 0; k < 4; k++ {
		if r <= 1+float64(n-1)*float64(k+1)/5 {
			return k
		}
	}
	return 4
}

// NeweyWestT is the t-statistic of the mean of xs with a Bartlett-kernel HAC
// variance over the given number of lags. Overlapping forward windows make
// consecutive weekly ICs autocorrelated — a 10-session window spans two
// rebalances — and an ordinary t on them overstates significance. NaN below ten
// finite observations. The variance uses n in the denominator, as the Python
// spec did.
func NeweyWestT(xs []float64, lags int) float64 {
	x := finite(xs)
	n := len(x)
	if n < 10 {
		return math.NaN()
	}
	m, _ := meanSD(x)
	e := make([]float64, n)
	for i := range x {
		e[i] = x[i] - m
	}
	v := 0.0
	for _, d := range e {
		v += d * d
	}
	v /= float64(n)
	for l := 1; l <= lags; l++ {
		w := 1 - float64(l)/float64(lags+1)
		c := 0.0
		for i := l; i < n; i++ {
			c += e[i] * e[i-l]
		}
		v += 2 * w * c / float64(n)
	}
	if v <= 0 {
		return math.NaN()
	}
	return m / math.Sqrt(v/float64(n))
}

// nwLags is the Newey-West lag count for an h-session horizon on weekly
// observations: h/5 + 1, so a window overlapping k later rebalances is covered.
func nwLags(h int) int { return h/5 + 1 }
