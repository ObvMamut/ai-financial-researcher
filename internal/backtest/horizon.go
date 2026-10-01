package backtest

import "fmt"

// Indices into Horizons for the v5 horizon tests.
const (
	h21 = 3
	h63 = 4
)

// HorizonReport is the v5 horizon block: the pre-registered tests of whether a
// signal's edge lives at a horizon other than the 10–15 sessions the lab
// started from (docs/workflow/backtest.md, "Pre-registered horizon tests").
type HorizonReport struct {
	Tests []TestResult `json:"tests"`
}

// horizonTests runs the v5 tests in registered order: H2-21 and H2-63, 12-1
// momentum's beta-adjusted IC at its own horizon, each with the Newey-West lags
// that horizon's overlap calls for.
func horizonTests(cells []cell, mid string) HorizonReport {
	h2 := func(k int) TestResult {
		h, lags := Horizons[k], nwLags(Horizons[k])
		return evaluate(TestResult{
			ID:    fmt.Sprintf("H2-%d", h),
			Title: fmt.Sprintf("12-1 momentum alone at its own horizon: beta-adjusted IC%d", h),
			Statistic: fmt.Sprintf("per-date Spearman IC%d of mom12_1 vs beta-adjusted excess, averaged across indices per date, Newey-West t (%d lags)",
				h, lags),
		}, cells, mid, lags, func(c cell) float64 { return c.bic[SigMom12_1][k] })
	}
	return HorizonReport{Tests: []TestResult{h2(h21), h2(h63)}}
}
