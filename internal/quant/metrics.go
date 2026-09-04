package quant

import (
	"fmt"
	"math"
)

// Metrics is the full statistical read on one symbol. Zero values paired with
// an entry in Flags mean "not computable from the available history".
type Metrics struct {
	Symbol    string  `json:"symbol"`
	AsOf      string  `json:"as_of"`
	Bars      int     `json:"bars"`
	LastClose float64 `json:"last_close"`

	// Returns ladder (simple total returns over trailing windows).
	Ret5d   float64 `json:"ret_5d"`
	Ret21d  float64 `json:"ret_21d"`
	Ret63d  float64 `json:"ret_63d"`
	Ret126d float64 `json:"ret_126d"`
	Ret252d float64 `json:"ret_252d"`

	// Momentum: 12-1 (252d return excluding the most recent 21d), the classic
	// Jegadeesh-Titman formation that sidesteps short-term reversal.
	Mom12_1 float64 `json:"mom_12_1"`
	// Vol-adjusted momentum: Mom12_1 / annualized 60d vol (Sharpe-like).
	VolAdjMom float64 `json:"vol_adj_mom"`

	// Price relative to the trailing 252d high (George & Hwang): values near
	// 1.0 have historically predicted continuation, not danger.
	PriceTo52wHigh float64 `json:"price_to_52w_high"`

	// Short-term reversal (Chen-Stivers-Sun flavored): the trailing 5d return
	// z-scored against its own 1y distribution, plus the turnover ratio
	// (5d avg volume / 63d avg volume) that conditions the signal — reversal
	// after a high-turnover extreme move is the empirically strong case.
	STRZScore     float64 `json:"str_z_score"`
	TurnoverRatio float64 `json:"turnover_ratio"`

	// Yang-Zhang OHLC realized volatility, annualized. Uses overnight gaps +
	// open-to-close + Rogers-Satchell terms; far more efficient than
	// close-to-close vol on the same window.
	VolYZ20  float64 `json:"vol_yz_20"` // 20d window
	VolYZ60  float64 `json:"vol_yz_60"` // 60d window
	VolTrend float64 `json:"vol_trend"` // VolYZ20 / VolYZ60 (>1 = vol expanding)

	// Lo-MacKinlay variance ratios of daily log returns. VR≈1 random walk,
	// VR>1 positive autocorrelation (trending), VR<1 mean reversion. Regime is
	// tagged only when the deviation clears an asymptotic 2σ band.
	VR5    float64 `json:"vr_5"`
	VR10   float64 `json:"vr_10"`
	Regime string  `json:"regime"` // trending | mean-reverting | random-walk

	// Risk shape.
	MaxDrawdown126 float64 `json:"max_drawdown_126"` // most negative peak-to-trough over 126d, ≤ 0
	WorstDay252    float64 `json:"worst_day_252"`    // most negative daily log return, ≤ 0
	Skew252        float64 `json:"skew_252"`
	Kurt252        float64 `json:"kurt_252"` // excess kurtosis

	// Liquidity. AvgDollarVol20 is Σ close·volume in the listing's *own*
	// currency, which is the only thing the bars can say. AvgDollarVol20USD is
	// that figure converted, and it is the one every threshold compares against —
	// a floor stated in dollars has to be met in dollars. It is 0 when no FX rate
	// was available, which callers must read as "unknown", not as "illiquid".
	Currency          string  `json:"currency,omitempty"`
	FXToUSD           float64 `json:"fx_to_usd,omitempty"`
	AvgDollarVol20    float64 `json:"avg_dollar_vol_20"`
	AvgDollarVol20USD float64 `json:"avg_dollar_vol_20_usd,omitempty"`

	// Benchmark relation over up to 252 aligned daily returns.
	Benchmark string  `json:"benchmark,omitempty"`
	Beta      float64 `json:"beta"`
	Corr      float64 `json:"corr"`

	// SigmaDaily is the daily vol implied by VolYZ20 (VolYZ20/√252): the unit
	// for the vol-scaled stop/target distances below.
	SigmaDaily float64 `json:"sigma_daily"`
	// Distances[k][h] = k·σ_daily·√h as a fraction of price, for horizon h
	// trading days. Keys: "1s"/"2s" × horizons 5, 10, 20.
	Distances map[string]float64 `json:"distances,omitempty"`

	Flags []string `json:"flags,omitempty"` // metrics skipped + why
}

const (
	tradingDays  = 252
	momentumSkip = 21
)

// Stretch expresses a trailing return over days sessions in units of that
// name's own volatility over the same span, so a 10% month is "large" for a
// utility and unremarkable for a semiconductor. It is the same normalisation
// STRZScore applies to the trailing week, stated as a function so the pre-screen
// and the Chief's reference block cannot disagree about it.
//
// An unknown sigma yields 0. That reads as "not extended", which is wrong, but
// it is the only neutral value available and the names it affects are the ones
// with no usable vol data at all — which every downstream check already refuses.
func Stretch(ret, sigmaDaily float64, days int) float64 {
	if sigmaDaily <= 0 || days <= 0 {
		return 0
	}
	return ret / (sigmaDaily * math.Sqrt(float64(days)))
}

// Stretch21 is the last month's move in units of the name's own 21-day
// volatility. STRZScore covers the same ground over five sessions, and five
// sessions turned out to be shorter than the thing being measured: on
// 2026-09-04 AMGN sat at 0.993 of its 52-week high after +47% on 12-1 and +29%
// on the quarter, but its week was +1.6%, so the only extension signal in the
// system read +0.19 and the Chief concluded "extension risk is absent". The
// same name reads +1.2 over 21 days.
func (m Metrics) Stretch21() float64 { return Stretch(m.Ret21d, m.SigmaDaily, 21) }

// Compute derives all metrics for s, optionally relating it to bench (may be
// nil). It needs ~60 bars for a useful read and 273+ for the full set.
func Compute(s *Series, bench *Series) Metrics {
	m := Metrics{Symbol: s.Symbol, AsOf: s.AsOf(), Bars: len(s.Bars), LastClose: s.LastClose()}
	flag := func(format string, args ...any) { m.Flags = append(m.Flags, fmt.Sprintf(format, args...)) }

	if len(s.Bars) < 22 {
		flag("insufficient history (%d bars) — metrics not computed", len(s.Bars))
		return m
	}

	// Returns ladder.
	lad := []struct {
		n   int
		dst *float64
	}{
		{5, &m.Ret5d}, {21, &m.Ret21d}, {63, &m.Ret63d}, {126, &m.Ret126d}, {252, &m.Ret252d},
	}
	for _, l := range lad {
		if r, ok := s.TotalReturn(l.n); ok {
			*l.dst = r
		} else {
			flag("ret_%dd: needs %d bars, have %d", l.n, l.n+1, len(s.Bars))
		}
	}

	// 12-1 momentum: total return from t-252 to t-21.
	if len(s.Bars) >= tradingDays+1 {
		p0 := s.Bars[len(s.Bars)-1-tradingDays].Close
		p1 := s.Bars[len(s.Bars)-1-momentumSkip].Close
		if p0 > 0 {
			m.Mom12_1 = p1/p0 - 1
		}
	} else {
		flag("mom_12_1: needs %d bars, have %d", tradingDays+1, len(s.Bars))
	}

	// Price-to-52-week-high.
	hi := 0.0
	for _, b := range tailBars(s.Bars, tradingDays) {
		if b.High > hi {
			hi = b.High
		}
	}
	if hi > 0 {
		m.PriceTo52wHigh = m.LastClose / hi
	}

	rets := s.LogReturns()

	// Short-term reversal z-score: trailing 5d log return vs the distribution
	// of rolling 5d log returns over the past year.
	if len(rets) >= 30 {
		window := tail(rets, tradingDays)
		var fivedays []float64
		for i := 5; i <= len(window); i++ {
			sum := 0.0
			for _, r := range window[i-5 : i] {
				sum += r
			}
			fivedays = append(fivedays, sum)
		}
		cur := fivedays[len(fivedays)-1]
		sd := stddev(fivedays)
		if sd > 0 {
			m.STRZScore = (cur - mean(fivedays)) / sd
		}
	} else {
		flag("str_z_score: needs 31 bars, have %d", len(s.Bars))
	}

	// Turnover ratio: 5d vs 63d average share volume.
	vol5 := avgVolume(tailBars(s.Bars, 5))
	vol63 := avgVolume(tailBars(s.Bars, 63))
	if vol63 > 0 {
		m.TurnoverRatio = vol5 / vol63
	}

	// Yang-Zhang volatility.
	if v, ok := yangZhang(tailBars(s.Bars, 21)); ok { // 21 bars → 20 overnight gaps
		m.VolYZ20 = v
	} else {
		flag("vol_yz_20: OHLC data unusable in 20d window")
	}
	if v, ok := yangZhang(tailBars(s.Bars, 61)); ok {
		m.VolYZ60 = v
	}
	if m.VolYZ60 > 0 {
		m.VolTrend = m.VolYZ20 / m.VolYZ60
		if m.Mom12_1 != 0 {
			m.VolAdjMom = m.Mom12_1 / m.VolYZ60
		}
	}

	// Variance ratios on up to 2y of daily returns.
	vrRets := tail(rets, 2*tradingDays)
	m.VR5 = varianceRatio(vrRets, 5)
	m.VR10 = varianceRatio(vrRets, 10)
	m.Regime = classifyRegime(vrRets, m.VR5, 5)

	// Risk shape.
	m.MaxDrawdown126 = maxDrawdown(tailBars(s.Bars, 126))
	worst := 0.0
	for _, r := range tail(rets, tradingDays) {
		if r < worst {
			worst = r
		}
	}
	m.WorstDay252 = worst
	m.Skew252, m.Kurt252 = skewKurt(tail(rets, tradingDays))

	// Liquidity: 20d average dollar volume.
	dv := 0.0
	bars20 := tailBars(s.Bars, 20)
	for _, b := range bars20 {
		dv += b.Close * b.Volume
	}
	if len(bars20) > 0 {
		m.AvgDollarVol20 = dv / float64(len(bars20))
	}
	// Currency/FXToUSD/AvgDollarVol20USD stay zero here: bars carry no currency
	// and guessing one is the whole defect. ApplyFX fills them in.

	// Benchmark relation.
	if bench != nil && len(bench.Bars) > 1 {
		ra, rb := AlignedReturns(s, bench)
		ra, rb = tail(ra, tradingDays), tail(rb, tradingDays)
		if len(ra) >= 60 {
			m.Benchmark = bench.Symbol
			vb := variance(rb)
			if vb > 0 {
				m.Beta = covariance(ra, rb) / vb
			}
			sa, sb := stddev(ra), stddev(rb)
			if sa > 0 && sb > 0 {
				m.Corr = covariance(ra, rb) / (sa * sb)
			}
		} else {
			flag("beta: only %d aligned return days with %s", len(ra), bench.Symbol)
		}
	}

	// Vol-scaled distances for stop/target sizing: k·σ_daily·√h.
	if m.VolYZ20 > 0 {
		m.SigmaDaily = m.VolYZ20 / math.Sqrt(tradingDays)
		m.Distances = map[string]float64{}
		for _, h := range []int{5, 10, 20} {
			for k := 1; k <= 2; k++ {
				key := fmt.Sprintf("%ds_h%d", k, h)
				m.Distances[key] = float64(k) * m.SigmaDaily * math.Sqrt(float64(h))
			}
		}
	}

	return m
}

// ApplyFX records the listing's currency and the rate that converts it to USD,
// and derives the USD liquidity figure the thresholds compare against.
//
// It is separate from Compute because a price series does not say what currency
// it is in — the exchange suffix does, and resolving that needs a network fetch
// Compute has no business making. A rate of 0 or less leaves AvgDollarVol20USD
// at zero and adds a flag, so an unconvertible name reads as "unknown", never as
// a number in the wrong units.
func (m *Metrics) ApplyFX(code string, rate float64) {
	m.Currency, m.FXToUSD, m.AvgDollarVol20USD = code, 0, 0
	if rate <= 0 {
		m.Flags = append(m.Flags, fmt.Sprintf(
			"no %s/USD rate — liquidity in USD not computable; avg_dollar_vol_20 is %s", code, code))
		return
	}
	m.FXToUSD = rate
	m.AvgDollarVol20USD = m.AvgDollarVol20 * rate
}

func tailBars(bars []Bar, n int) []Bar {
	if len(bars) <= n {
		return bars
	}
	return bars[len(bars)-n:]
}

func avgVolume(bars []Bar) float64 {
	if len(bars) == 0 {
		return 0
	}
	sum := 0.0
	for _, b := range bars {
		sum += b.Volume
	}
	return sum / float64(len(bars))
}

// yangZhang estimates annualized volatility from OHLC bars (Yang & Zhang 2000):
// σ² = σ²_overnight + k·σ²_open-to-close + (1−k)·σ²_RS with
// k = 0.34 / (1.34 + (n+1)/(n−1)).
func yangZhang(bars []Bar) (float64, bool) {
	if len(bars) < 3 {
		return 0, false
	}
	var on, oc, rs []float64
	for i := 1; i < len(bars); i++ {
		b, prev := bars[i], bars[i-1]
		if b.Open <= 0 || b.High <= 0 || b.Low <= 0 || b.Close <= 0 || prev.Close <= 0 {
			continue
		}
		on = append(on, math.Log(b.Open/prev.Close))
		oc = append(oc, math.Log(b.Close/b.Open))
		ho, hc := math.Log(b.High/b.Open), math.Log(b.High/b.Close)
		lo, lc := math.Log(b.Low/b.Open), math.Log(b.Low/b.Close)
		rs = append(rs, ho*hc+lo*lc)
	}
	n := float64(len(on))
	if n < 2 {
		return 0, false
	}
	k := 0.34 / (1.34 + (n+1)/(n-1))
	v := variance(on) + k*variance(oc) + (1-k)*mean(rs)
	if v <= 0 {
		// The Rogers-Satchell term can push the sum to zero or slightly negative
		// on degenerate data (a halted name, a synthetic series, bars where every
		// OHLC is the same price). That is "not computable", not "zero
		// volatility", and the difference matters downstream: a zero σ leaves
		// SigmaDaily at 0, and the risk gate cannot check a stop in σ units it
		// does not have. Reporting ok=false raises the flag Compute already
		// writes for an unusable window, so the gap is visible instead of
		// presenting as a confidently computed zero.
		return 0, false
	}
	return math.Sqrt(v * tradingDays), true
}

// varianceRatio is the Lo-MacKinlay VR(q) with overlapping q-period returns
// and unbiased variance estimators. Returns 1 when not computable.
func varianceRatio(rets []float64, q int) float64 {
	n := len(rets)
	if q < 2 || n < q*4 {
		return 1
	}
	mu := mean(rets)
	var s1 float64
	for _, r := range rets {
		d := r - mu
		s1 += d * d
	}
	varA := s1 / float64(n-1)
	if varA == 0 {
		return 1
	}
	// Overlapping q-period returns, bias-corrected denominator (Lo-MacKinlay 1988).
	mq := float64(q) * (float64(n) - float64(q) + 1) * (1 - float64(q)/float64(n))
	var s2 float64
	for i := q; i <= n; i++ {
		sum := 0.0
		for _, r := range rets[i-q : i] {
			sum += r
		}
		d := sum - float64(q)*mu
		s2 += d * d
	}
	varC := s2 / mq
	return varC / varA
}

// classifyRegime tags the VR(q) deviation only when it clears the asymptotic
// 2σ band under the homoskedastic random-walk null:
// std(VR) ≈ sqrt(2(2q−1)(q−1) / (3q·n)).
func classifyRegime(rets []float64, vr float64, q int) string {
	n := float64(len(rets))
	if n < float64(q*4) {
		return "random-walk"
	}
	band := 2 * math.Sqrt(2*float64(2*q-1)*float64(q-1)/(3*float64(q)*n))
	switch {
	case vr > 1+band:
		return "trending"
	case vr < 1-band:
		return "mean-reverting"
	default:
		return "random-walk"
	}
}

// maxDrawdown returns the most negative peak-to-trough decline of closes (≤ 0).
func maxDrawdown(bars []Bar) float64 {
	peak, worst := 0.0, 0.0
	for _, b := range bars {
		if b.Close > peak {
			peak = b.Close
		}
		if peak > 0 {
			dd := b.Close/peak - 1
			if dd < worst {
				worst = dd
			}
		}
	}
	return worst
}

// skewKurt returns sample skewness and excess kurtosis.
func skewKurt(xs []float64) (skew, kurt float64) {
	n := float64(len(xs))
	if n < 4 {
		return 0, 0
	}
	m := mean(xs)
	var m2, m3, m4 float64
	for _, x := range xs {
		d := x - m
		m2 += d * d
		m3 += d * d * d
		m4 += d * d * d * d
	}
	m2 /= n
	m3 /= n
	m4 /= n
	if m2 == 0 {
		return 0, 0
	}
	skew = m3 / math.Pow(m2, 1.5)
	kurt = m4/(m2*m2) - 3
	return skew, kurt
}

func covariance(a, b []float64) float64 {
	if len(a) != len(b) || len(a) < 2 {
		return 0
	}
	ma, mb := mean(a), mean(b)
	var sum float64
	for i := range a {
		sum += (a[i] - ma) * (b[i] - mb)
	}
	return sum / float64(len(a)-1)
}
