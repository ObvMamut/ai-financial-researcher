// Package quant computes statistically grounded price metrics for the research
// pipeline. It deliberately avoids classic chart-based technical analysis
// (moving-average crossovers, RSI, patterns); every metric here has a published
// empirical basis: return ladders and 12-1 momentum (Jegadeesh & Titman 1993),
// price-to-52-week-high (George & Hwang 2004), turnover-conditioned short-term
// reversal (Chen, Stivers & Sun 2024), Yang-Zhang OHLC volatility (Yang & Zhang
// 2000), and Lo-MacKinlay variance ratios (1988).
//
// The package is pure stdlib and has no dependencies on the rest of the app;
// marketdata imports quant, never the reverse.
package quant

import (
	"math"
	"sort"
)

// Bar is one daily OHLCV observation. Close should be the adjusted close when
// available so returns include dividends/splits.
type Bar struct {
	Date   string  `json:"date"` // YYYY-MM-DD
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

// Series is a daily price history in ascending date order.
type Series struct {
	Symbol string `json:"symbol"`
	Bars   []Bar  `json:"bars"`
}

// Sort orders bars ascending by date (ISO dates sort lexically).
func (s *Series) Sort() {
	sort.Slice(s.Bars, func(i, j int) bool { return s.Bars[i].Date < s.Bars[j].Date })
}

// LastClose returns the most recent close, or 0 for an empty series.
func (s *Series) LastClose() float64 {
	if len(s.Bars) == 0 {
		return 0
	}
	return s.Bars[len(s.Bars)-1].Close
}

// AsOf returns the date of the most recent bar, or "".
func (s *Series) AsOf() string {
	if len(s.Bars) == 0 {
		return ""
	}
	return s.Bars[len(s.Bars)-1].Date
}

// LogReturns returns close-to-close daily log returns (length len(Bars)-1).
func (s *Series) LogReturns() []float64 {
	if len(s.Bars) < 2 {
		return nil
	}
	out := make([]float64, 0, len(s.Bars)-1)
	for i := 1; i < len(s.Bars); i++ {
		p0, p1 := s.Bars[i-1].Close, s.Bars[i].Close
		if p0 <= 0 || p1 <= 0 {
			out = append(out, 0)
			continue
		}
		out = append(out, math.Log(p1/p0))
	}
	return out
}

// TotalReturn is the simple return over the trailing n bars (close[last] /
// close[last-n] - 1). Returns (0, false) when history is insufficient.
func (s *Series) TotalReturn(n int) (float64, bool) {
	if n <= 0 || len(s.Bars) < n+1 {
		return 0, false
	}
	p0 := s.Bars[len(s.Bars)-1-n].Close
	p1 := s.Bars[len(s.Bars)-1].Close
	if p0 <= 0 {
		return 0, false
	}
	return p1/p0 - 1, true
}

// AlignedReturns pairs daily log returns of two series by date, for beta and
// correlation. Only dates present in both series contribute.
func AlignedReturns(a, b *Series) (ra, rb []float64) {
	if a == nil || b == nil {
		return nil, nil
	}
	closeByDate := make(map[string]float64, len(b.Bars))
	for _, bar := range b.Bars {
		closeByDate[bar.Date] = bar.Close
	}
	var prevA, prevB float64
	havePrev := false
	for _, bar := range a.Bars {
		bc, ok := closeByDate[bar.Date]
		if !ok || bar.Close <= 0 || bc <= 0 {
			continue
		}
		if havePrev {
			ra = append(ra, math.Log(bar.Close/prevA))
			rb = append(rb, math.Log(bc/prevB))
		}
		prevA, prevB = bar.Close, bc
		havePrev = true
	}
	return ra, rb
}

// Correlation is the Pearson correlation of two series' aligned daily log
// returns. The bool is false when they share too little history to mean
// anything — 20 common sessions is already generous for a book-level check.
//
// It is exported because the risk gate asks a question the metrics pack cannot:
// not how each name relates to its benchmark, but how the ideas relate to each
// other. Five ideas at ρ ≈ 0.9 are one position in five tickets.
//
// One known limit: a cross-market pair is measured on closes stamped with the
// same calendar date but struck up to fourteen hours apart — Tokyo's 06:00 UTC
// against New York's 21:00 — so a Tokyo/NYSE pair's correlation is systematically
// understated and the gate's ρ ceiling rarely binds on one. Lagging one leg would
// fix the sign of the error but not its size, and would need a per-market
// calendar the pipeline does not keep. The correlation is read as a floor on how
// related two ideas are, never as the measurement.
func Correlation(a, b *Series) (float64, bool) {
	ra, rb := AlignedReturns(a, b)
	if len(ra) < 20 {
		return 0, false
	}
	sa, sb := stddev(ra), stddev(rb)
	if sa == 0 || sb == 0 {
		return 0, false
	}
	return covariance(ra, rb) / (sa * sb), true
}

// ── small stat helpers (shared within the package) ──────────────────────────

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// variance is the unbiased sample variance.
func variance(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := mean(xs)
	var ss float64
	for _, x := range xs {
		d := x - m
		ss += d * d
	}
	return ss / float64(len(xs)-1)
}

func stddev(xs []float64) float64 { return math.Sqrt(variance(xs)) }

func tail(xs []float64, n int) []float64 {
	if len(xs) <= n {
		return xs
	}
	return xs[len(xs)-n:]
}
