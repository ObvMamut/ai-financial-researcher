package backtest

import (
	"encoding/json"
	"math"
	"sort"
)

// Round-trip cost per leg, in return units: 30bp. A long-short quintile spread
// pays it on each leg; a top-quintile or single-name trade pays it once.
const costPerLeg = 0.003

// cell is one (date, index) cross-section's per-signal statistics.
type cell struct {
	date, index, region string
	ic                  [NumSignals][3]float64 // Spearman vs benchmark-excess, per horizon
	bic                 [NumSignals][3]float64 // Spearman vs beta-adjusted excess (C4)
	qs10, qs15, top10   [NumSignals]float64
}

// crossSections computes every (date, index) cell from the panel.
func crossSections(recs []Record) []cell {
	type key struct{ date, index string }
	groups := map[key][]int{}
	var order []key
	for i, r := range recs {
		k := key{r.Date, r.Index}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], i)
	}
	sort.Slice(order, func(a, b int) bool {
		if order[a].date != order[b].date {
			return order[a].date < order[b].date
		}
		return order[a].index < order[b].index
	})
	out := make([]cell, 0, len(order))
	for _, k := range order {
		members := groups[k]
		c := cell{date: k.date, index: k.index, region: Region(k.index)}
		col := func(f func(Record) float64) []float64 {
			xs := make([]float64, len(members))
			for j, i := range members {
				xs[j] = f(recs[i])
			}
			return xs
		}
		var xs, bx [3][]float64
		for h := range Horizons {
			xs[h] = col(func(r Record) float64 { return r.XS[h] })
			bx[h] = col(func(r Record) float64 { return r.BX[h] })
		}
		for s := 0; s < NumSignals; s++ {
			sig := col(func(r Record) float64 { return r.Sig[s] })
			for h := range Horizons {
				c.ic[s][h] = Spearman(sig, xs[h])
				c.bic[s][h] = Spearman(sig, bx[h])
			}
			c.qs10[s], c.top10[s] = quintileSpread(sig, xs[1])
			c.qs15[s], _ = quintileSpread(sig, xs[2])
		}
		out = append(out, c)
	}
	return out
}

// dateSeries averages a per-cell statistic across the indices present on each
// date, returning one value per date in date order. A NaN cell does not count.
func dateSeries(cells []cell, keep func(cell) bool, f func(cell) float64) ([]string, []float64) {
	type acc struct {
		sum float64
		n   int
	}
	by := map[string]*acc{}
	var dates []string
	for _, c := range cells {
		if !keep(c) {
			continue
		}
		a := by[c.date]
		if a == nil {
			a = &acc{}
			by[c.date] = a
			dates = append(dates, c.date)
		}
		if v := f(c); !math.IsNaN(v) {
			a.sum += v
			a.n++
		}
	}
	sort.Strings(dates)
	vals := make([]float64, len(dates))
	for i, d := range dates {
		if a := by[d]; a.n > 0 {
			vals[i] = a.sum / float64(a.n)
		} else {
			vals[i] = math.NaN()
		}
	}
	return dates, vals
}

// SignalStats is one signal's summary over one slice of the panel.
type SignalStats struct {
	Signal string `json:"signal"`
	IC     [3]Num `json:"ic"`      // mean per-date rank IC, horizons 5/10/15
	IR     [3]Num `json:"icir"`    // mean / sd of the per-date IC
	TNW    [3]Num `json:"t_nw"`    // Newey-West t, lags h/5+1
	NDates int    `json:"n_dates"` // dates with a finite IC10
	// Quintile spreads, % per holding period, against benchmark excess.
	QS10Pct     Num `json:"qs10_gross_pct"`
	QS10NetPct  Num `json:"qs10_net_pct"` // less 30bp on each leg
	TQS10       Num `json:"t_qs10"`
	QS15Pct     Num `json:"qs15_gross_pct"`
	QS15NetPct  Num `json:"qs15_net_pct"`
	Top10NetPct Num `json:"top_quintile10_net_pct"` // top fifth's excess less 30bp
}

// Slice is a labelled subset of cells: all, one region, or one half.
type Slice struct {
	Label string
	Keep  func(cell) bool
}

func slices(mid string) []Slice {
	out := []Slice{{"all", func(cell) bool { return true }}}
	for _, reg := range Regions {
		reg := reg
		out = append(out, Slice{reg, func(c cell) bool { return c.region == reg }})
	}
	out = append(out,
		Slice{"H1", func(c cell) bool { return c.date < mid }},
		Slice{"H2", func(c cell) bool { return c.date >= mid }})
	return out
}

// summarize computes every signal's stats over one slice; beta selects the
// beta-adjusted target for the ICs (the quintile spreads stay on plain excess).
func summarize(cells []cell, keep func(cell) bool, beta bool) []SignalStats {
	out := make([]SignalStats, NumSignals)
	for s := 0; s < NumSignals; s++ {
		st := SignalStats{Signal: SignalNames[s]}
		for h, hz := range Horizons {
			_, v := dateSeries(cells, keep, func(c cell) float64 {
				if beta {
					return c.bic[s][h]
				}
				return c.ic[s][h]
			})
			m, sd := meanSD(v)
			st.IC[h], st.IR[h], st.TNW[h] = Num(m), Num(m/sd), Num(NeweyWestT(v, nwLags(hz)))
			if h == 1 {
				st.NDates = len(finite(v))
			}
		}
		_, q10 := dateSeries(cells, keep, func(c cell) float64 { return c.qs10[s] })
		_, q15 := dateSeries(cells, keep, func(c cell) float64 { return c.qs15[s] })
		_, t10 := dateSeries(cells, keep, func(c cell) float64 { return c.top10[s] })
		m10, _ := meanSD(q10)
		m15, _ := meanSD(q15)
		mt, _ := meanSD(t10)
		st.QS10Pct, st.QS10NetPct = Num(100*m10), Num(100*(m10-2*costPerLeg))
		st.QS15Pct, st.QS15NetPct = Num(100*m15), Num(100*(m15-2*costPerLeg))
		st.TQS10 = Num(NeweyWestT(q10, nwLags(10)))
		st.Top10NetPct = Num(100 * (mt - costPerLeg))
		out[s] = st
	}
	return out
}

// midDate splits the sample into halves the way the spec did: the date at
// position len/2 of the sorted unique dates starts the second half.
func midDate(cells []cell) string {
	seen := map[string]bool{}
	var dates []string
	for _, c := range cells {
		if !seen[c.date] {
			seen[c.date] = true
			dates = append(dates, c.date)
		}
	}
	sort.Strings(dates)
	if len(dates) == 0 {
		return ""
	}
	return dates[len(dates)/2]
}

// Adoption bar for the pre-registered tests (docs/workflow/backtest.md): the
// headline Newey-West t must clear it in the pre-registered direction, and the
// slice means must share that sign in both halves and every region.
const adoptionT = 2.5

// TestResult is one pre-registered test's outcome.
type TestResult struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Statistic string `json:"statistic"`
	// Status is "run", "untestable" or "skipped"; only "run" counts toward the
	// number of tests the report says it ran.
	Status  string         `json:"status"`
	Note    string         `json:"note,omitempty"`
	Mean    Num            `json:"mean"`
	T       Num            `json:"t_nw"`
	NDates  int            `json:"n_dates,omitempty"`
	Halves  map[string]Num `json:"halves,omitempty"`
	Regions map[string]Num `json:"regions,omitempty"`
	Pass    bool           `json:"pass"`
	Verdict string         `json:"verdict"`
}

// evaluate applies the adoption bar to one per-cell statistic whose
// pre-registered direction is positive.
func evaluate(r TestResult, cells []cell, mid string, f func(cell) float64) TestResult {
	r.Status = "run"
	_, v := dateSeries(cells, func(cell) bool { return true }, f)
	m, _ := meanSD(v)
	t := NeweyWestT(v, nwLags(10))
	r.Mean, r.T = Num(m), Num(t)
	r.NDates = len(finite(v))
	r.Halves, r.Regions = map[string]Num{}, map[string]Num{}
	consistent := true
	for _, sl := range slices(mid)[1:] {
		_, sv := dateSeries(cells, sl.Keep, f)
		sm, _ := meanSD(sv)
		if sl.Label == "H1" || sl.Label == "H2" {
			r.Halves[sl.Label] = Num(sm)
		} else {
			r.Regions[sl.Label] = Num(sm)
		}
		if !(sm > 0) {
			consistent = false
		}
	}
	switch {
	case !(t > adoptionT):
		r.Verdict = "fails: t does not clear +2.5"
	case !consistent:
		r.Verdict = "fails: sign not positive in both halves and every region"
	default:
		r.Pass = true
		r.Verdict = "passes the adoption bar"
	}
	return r
}

// preregistered runs the fixed test list. The list, the statistics and the bar
// were committed to docs/workflow/backtest.md before this ever ran on data.
func preregistered(cells []cell, mid string) []TestResult {
	const h10 = 1
	return []TestResult{
		evaluate(TestResult{
			ID: "C1", Title: "raise mom12-1's weight relative to ret63d (1.0·z(mom12-1) + 0.5·z(ret63d), penalties and clamps unchanged)",
			Statistic: "per-date IC10(variant) − IC10(shipped composite), Newey-West t (3 lags)",
		}, cells, mid, func(c cell) float64 { return c.ic[SigC1][h10] - c.ic[SigScore][h10] }),
		{
			ID: "C2", Title: "earnings-announcement premium: tilt long into names reporting inside the window",
			Status: "untestable", Verdict: "not run",
			Note: "no existing provider or cache holds point-in-time historical earnings-announcement dates: the Alpha Vantage calendar is forward-only and keyed, EDGAR's daily index gives 10-Q/10-K filing dates (weeks after the announcement) for US filers only, and nothing covers eu50 or asia100 — so the every-region bar could not be met even in principle",
		},
		evaluate(TestResult{
			ID: "C3", Title: "news-conditioned residual reversal: fade a residual 5-day move without abnormal volume, follow one with it (any of the last 5 sessions ≥ 2× the mean of the 20 before)",
			Statistic: "per-date IC10 of the C3 signal, Newey-West t (3 lags)",
		}, cells, mid, func(c cell) float64 { return c.ic[SigC3][h10] }),
		evaluate(TestResult{
			ID: "C4", Title: "beta-adjusted target: the shipped composite's IC10 against r − β·r_bench instead of r − r_bench",
			Statistic: "per-date IC10 of the shipped composite vs beta-adjusted excess, Newey-West t (3 lags)",
		}, cells, mid, func(c cell) float64 { return c.bic[SigScore][h10] }),
		{
			ID: "C5", Title: "breadth: the composite's t on a wider liquid US universe",
			Status: "skipped", Verdict: "not run",
			Note: "needs a new universe file of several hundred US names; no existing file or keyless source provides that list, and building one is out of this change's scope",
		},
	}
}

// Num is a float that encodes NaN and ±Inf as JSON null — a statistic that
// could not be computed is absent, not zero.
type Num float64

// MarshalJSON implements json.Marshaler.
func (n Num) MarshalJSON() ([]byte, error) {
	f := float64(n)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return []byte("null"), nil
	}
	return json.Marshal(f)
}
