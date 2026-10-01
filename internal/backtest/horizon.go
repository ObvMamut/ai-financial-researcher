package backtest

import (
	"fmt"
	"math"
	"sort"
)

// Indices into Horizons for the v5 horizon tests.
const (
	h15 = 2
	h21 = 3
	h63 = 4
)

// HorizonReport is the v5 horizon block: the pre-registered tests of whether a
// signal's edge lives at a horizon other than the 10–15 sessions the lab
// started from (docs/workflow/backtest.md, "Pre-registered horizon tests").
// Tests holds only registered tests, in order H1-21, H1-63, H2-21, H2-63;
// Reference (H1-15) is a comparison and stays out of it, so nothing that builds
// the test family from Tests can count it.
type HorizonReport struct {
	Tests     []TestResult  `json:"tests"`
	Reference TestResult    `json:"reference_15"`
	Books     []HorizonBook `json:"books"`
}

// HorizonBook summarises the H1 book at one horizon: the composite's top-5 per
// index per week, pooled, held at the score's sign, beta-adjusted, percentages
// in percent. Weeks counts dates with a priced pick; the net figures subtract
// costPerLeg once per name.
type HorizonBook struct {
	Horizon   int `json:"horizon"`
	Weeks     int `json:"weeks"`
	MeanPicks Num `json:"mean_picks"`
	GrossPct  Num `json:"mean_beta_adjusted_gross_pct"`
	NetPct    Num `json:"mean_beta_adjusted_net_pct"`
	H1NetPct  Num `json:"h1_beta_adjusted_net_pct"`
	H2NetPct  Num `json:"h2_beta_adjusted_net_pct"`
}

// horizonTests runs the v5 tests in registered order: H1-21 and H1-63, the
// composite's top-5-per-index book, then H2-21 and H2-63, 12-1 momentum's
// beta-adjusted IC at its own horizon, each with the Newey-West lags that
// horizon's overlap calls for. H1-15 is computed the same way as a reference.
func horizonTests(recs []Record, cells []cell, mid string) HorizonReport {
	h2 := func(k int) TestResult {
		h, lags := Horizons[k], nwLags(Horizons[k])
		return evaluate(TestResult{
			ID:    fmt.Sprintf("H2-%d", h),
			Title: fmt.Sprintf("12-1 momentum alone at its own horizon: beta-adjusted IC%d", h),
			Statistic: fmt.Sprintf("per-date Spearman IC%d of mom12_1 vs beta-adjusted excess, averaged across indices per date, Newey-West t (%d lags)",
				h, lags),
		}, cells, mid, lags, func(c cell) float64 { return c.bic[SigMom12_1][k] })
	}
	picks := topPicks(recs)
	rep := HorizonReport{}
	var books [3]HorizonBook
	var tests [3]TestResult
	for i, k := range []int{h15, h21, h63} {
		tests[i], books[i] = h1Book(picks, k, mid)
	}
	rep.Reference = tests[0]
	rep.Reference.Status, rep.Reference.Pass = "comparison", false
	rep.Reference.P, rep.Reference.PHolm = Num(math.NaN()), Num(math.NaN())
	rep.Reference.Verdict = "reference, not a test: " + rep.Reference.Verdict
	rep.Books = books[:]
	rep.Tests = []TestResult{tests[1], tests[2], h2(h21), h2(h63)}
	return rep
}

// h1Book is the H1 test at horizon index k and its book summary. Per date the
// statistic is the mean of dir·BX[k] over that date's priced picks (all indices
// pooled, cross-listings kept), less costPerLeg once — equal weights, so each
// name pays its 30bp once whatever the horizon. A date with no priced pick is
// dropped; each region's series is the same mean over that region's picks.
func h1Book(picks []Record, k int, mid string) (TestResult, HorizonBook) {
	h, lags := Horizons[k], nwLags(Horizons[k])
	r := TestResult{
		ID:        fmt.Sprintf("H1-%d", h),
		Title:     fmt.Sprintf("the composite's top-%d per index per week, held %d sessions at the score's sign", picksPerIndex, h),
		Statistic: fmt.Sprintf("per-date mean beta-adjusted %d-session excess of E1's picks, net of 30bp, Newey-West t (%d lags)", h, lags),
		Status:    "run",
	}
	type acc struct{ sum, n float64 }
	type day struct {
		all    acc
		region map[string]acc
	}
	byDate := map[string]*day{}
	for _, p := range picks {
		x := p.BX[k]
		if math.IsNaN(x) || math.IsInf(x, 0) {
			continue
		}
		d := byDate[p.Date]
		if d == nil {
			d = &day{region: map[string]acc{}}
			byDate[p.Date] = d
		}
		v := math.Copysign(1, p.Sig[SigScore]) * x
		d.all.sum, d.all.n = d.all.sum+v, d.all.n+1
		a := d.region[p.Region]
		d.region[p.Region] = acc{a.sum + v, a.n + 1}
	}
	dates := make([]string, 0, len(byDate))
	for d := range byDate {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	var all, h1, h2 []float64
	reg := map[string][]float64{}
	var npicks float64
	for _, d := range dates {
		v := byDate[d].all.sum/byDate[d].all.n - costPerLeg
		npicks += byDate[d].all.n
		all = append(all, v)
		if d < mid {
			h1 = append(h1, v)
		} else {
			h2 = append(h2, v)
		}
		for name, a := range byDate[d].region {
			reg[name] = append(reg[name], a.sum/a.n-costPerLeg)
		}
	}
	m, _ := meanSD(all)
	m1, _ := meanSD(h1)
	m2, _ := meanSD(h2)
	r.Mean, r.T, r.NDates = Num(m), Num(NeweyWestT(all, lags)), len(all)
	r.Halves = map[string]Num{"H1": Num(m1), "H2": Num(m2)}
	r.Regions = map[string]Num{}
	for _, name := range Regions {
		rm, _ := meanSD(reg[name])
		r.Regions[name] = Num(rm)
	}
	b := HorizonBook{Horizon: h, Weeks: len(all), MeanPicks: Num(math.NaN())}
	if len(all) > 0 {
		b.MeanPicks = Num(npicks / float64(len(all)))
	}
	b.GrossPct, b.NetPct = Num(100*(m+costPerLeg)), Num(100*m)
	b.H1NetPct, b.H2NetPct = Num(100*m1), Num(100*m2)
	return applyBar(r), b
}
