package backtest

import (
	"fmt"
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

// AnomalyReport is the v8 block: the registered tests N1–N3 and their own
// Holm family over the register (m = 21). The N tests never enter TestsRun or
// the in-report MultipleTesting: they are registered on the point-in-time US
// universe only (gateAnomalies), and their family is the whole register.
type AnomalyReport struct {
	Tests          []TestResult    `json:"tests"`
	RegisterFamily MultipleTesting `json:"register_family"`
}

// anomalySpecs are N1–N3 in registered order.
var anomalySpecs = []struct {
	id, title string
	sig       int
}{
	{"N1", "MAX (Bali, Cakici & Whitelaw 2011): −max simple daily return over the last 21 sessions", SigMax21},
	{"N2", "IVOL (Ang, Hodrick, Xing & Zhang 2006): −σ of the market-model residuals over 63 aligned pairs", SigIVol63},
	{"N3", "FIP (Da, Gurun & Warachka 2014): −mom12_1·ID over mom12-1's window", SigFIP},
}

// anomalyTests evaluates N1–N3 as registered: per (date, index) the Spearman
// of the signal against BX[21], averaged across the US indices per date, held
// to the US-scoped bar (t > +2.5 at nwLags(21), a positive mean in both halves
// split at mid). Only US cells are read; on the registered run (sp500, nq100)
// that is every cell. A test with no date carrying a finite statistic is
// untestable.
func anomalyTests(cells []cell, mid string) []TestResult {
	lags := nwLags(Horizons[h21])
	us := func(c cell) bool { return c.region == "US" }
	out := make([]TestResult, 0, len(anomalySpecs))
	for _, sp := range anomalySpecs {
		sig := sp.sig
		f := func(c cell) float64 { return c.bic[sig][h21] }
		_, all := dateSeries(cells, us, f)
		_, h1 := dateSeries(cells, func(c cell) bool { return us(c) && c.date < mid }, f)
		_, h2 := dateSeries(cells, func(c cell) bool { return us(c) && c.date >= mid }, f)
		r := applyScopedBar(TestResult{
			ID: sp.id, Title: sp.title, Scope: "US",
			Statistic: fmt.Sprintf("per-date Spearman IC%d of the signal vs beta-adjusted excess, averaged across sp500 and nq100 per date, Newey-West t (%d lags)",
				Horizons[h21], lags),
		}, all, h1, h2, lags, +1)
		if r.NDates == 0 {
			r.Status, r.Pass = "untestable", false
			r.P, r.PHolm = Num(math.NaN()), Num(math.NaN())
			r.Verdict = "not run: no date had a finite statistic"
		}
		out = append(out, r)
	}
	return out
}

// priorRegister is the 18 tests already run when N1–N3 were registered, with
// the p-values fixed in that registration (docs/workflow/backtest.md,
// "Pre-registered new anomalies N1–N3"). They are constants, not recomputed:
// the earlier tests are not re-decided. T is not part of the registration.
var priorRegister = func() []TestResult {
	const tenYear = "2026-10-01 10-year Holm table, docs/research/2026-10-01-lab-horizon.md"
	const pit = "docs/research/2026-10-07-evidence/pit-9y.json"
	rows := []struct {
		id   string
		p    float64
		note string
	}{
		{"C1", 0.0756, tenYear}, {"C3", 0.168, tenYear}, {"C4", 0.255, tenYear},
		{"E1", 0.726, tenYear}, {"E2-1", 0.620, tenYear}, {"E2-3", 0.541, tenYear},
		{"E2-off", 0.413, tenYear}, {"E3", 1, tenYear}, {"D1", 0.749, tenYear},
		{"D2", 0.512, tenYear}, {"D3", 0.0329, tenYear}, {"H1-21", 0.139, tenYear},
		{"H1-63", 0.0106, tenYear}, {"H2-21", 0.248, tenYear}, {"H2-63", 0.181, tenYear},
		{"PIT-IC10", 0.617, pit}, {"PIT-H1-63", 0.0968, pit},
		{"PIT-E1", 0.945, "binomialSignP(3, 10), the sign test on 3 of 10 years: " + pit},
	}
	out := make([]TestResult, len(rows))
	for i, r := range rows {
		out[i] = TestResult{ID: r.id, Status: "run", P: Num(r.p), T: Num(math.NaN()), PHolm: Num(math.NaN()), Note: r.note}
	}
	return out
}()

// registerFamily is Holm over the whole register: copies of the 18 prior rows
// plus N1–N3, m = 21. It sets PHolm on tests in place; priorRegister is never
// written. A NaN p (an untestable N test) is ranked as 1 and still counts.
func registerFamily(tests []TestResult) MultipleTesting {
	prior := make([]TestResult, len(priorRegister))
	copy(prior, priorRegister)
	ptrs := make([]*TestResult, 0, len(prior)+len(tests))
	for i := range prior {
		ptrs = append(ptrs, &prior[i])
	}
	for i := range tests {
		ptrs = append(ptrs, &tests[i])
	}
	return buildHolm(ptrs)
}

// anomalyComparisonPrefix opens an N test's verdict on any run but the
// registered one.
const anomalyComparisonPrefix = "comparison, registered on the point-in-time US universe only: "

// gateAnomalies applies the registration's scope to the N tests: only on a
// point-in-time replay of exactly the registered indices, sp500 and nq100, are
// they run and given the m = 21 register family. Anywhere else (one US index
// alone, a sample replay, a region mix) each computed test is a comparison,
// with no p-value and no pass. The registered look was taken on 2026-10-08;
// a later replay of the same scope is a re-run, which the register forbids,
// and its p-values are not new evidence.
func gateAnomalies(res *Result, pit bool, indices []string) {
	seen := map[string]bool{}
	for _, idx := range indices {
		seen[idx] = true
	}
	registered := pit && len(seen) == 2 && seen["sp500"] && seen["nq100"]
	if registered {
		res.Anomalies.RegisterFamily = registerFamily(res.Anomalies.Tests)
		return
	}
	for i := range res.Anomalies.Tests {
		t := &res.Anomalies.Tests[i]
		t.P, t.PHolm, t.Pass = Num(math.NaN()), Num(math.NaN()), false
		if t.Status == "run" {
			t.Status = "comparison"
			t.Verdict = anomalyComparisonPrefix + t.Verdict
		}
	}
}
