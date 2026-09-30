package backtest

// Region-scoped test machinery. The C-series bar asks for the same sign in
// every region, and v2's every-region bar is unreachable by construction for
// a signal that exists in one region only: drift and earn_window read SEC
// release dates, and there is no point-in-time EU or Asia source. A test
// registered as scoped is evaluated on that region's names alone and, if it
// passes, may be adopted for that region's names only. The bar is otherwise
// the lab's: |t| > 2.5 in the registered direction, the same sign in both
// halves, against the beta-adjusted target.
//
// Scoping is not a filter over the per-index cells crossSections builds. The
// US region is sp500 ∪ nq100, and 35 of nq100's 56 names are also in sp500:
// averaging the two indices' per-date ICs, as the "US" slice of the signal
// tables does, reads a cross-listed name twice on one date. A scoped
// cross-section is instead one pooled cell per date with one row per ticker
// (scopeRecords), which also gives a sparse signal like drift one larger
// cross-section to clear minCrossSection in rather than two smaller ones.

import (
	"fmt"
	"math"
	"sort"

	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// usScopeNote is the report's definition of the US scope.
const usScopeNote = "US = sp500 ∪ nq100, pooled into one cross-section per date with one row per ticker: a name in both indices enters once, as its sp500 row (measured against ^GSPC); nq100-only names keep their ^NDX benchmark"

// scopeRecords returns region's records with one row per (date, ticker). A
// ticker listed in more than one of the region's indices keeps the row of the
// index that comes first in universe.AllIndices() — sp500 before nq100 — so
// every cross-listed US name is read against the broad-market benchmark, and
// the choice does not depend on the panel's row order.
func scopeRecords(recs []Record, region string) []Record {
	rank := map[string]int{}
	for i, idx := range universe.AllIndices() {
		rank[idx] = i
	}
	type key struct{ date, ticker string }
	at := map[key]int{}
	var out []Record
	for _, r := range recs {
		if r.Region != region {
			continue
		}
		k := key{r.Date, r.Ticker}
		if j, ok := at[k]; ok {
			if rank[r.Index] < rank[out[j].Index] {
				out[j] = r
			}
			continue
		}
		at[k] = len(out)
		out = append(out, r)
	}
	return out
}

// scopedCells is one pooled cell per date over scopeRecords(recs, region),
// computed by the same crossSections every other statistic uses. Each cell's
// index and region are the region's name.
func scopedCells(recs []Record, region string) []cell {
	scoped := scopeRecords(recs, region)
	for i := range scoped {
		scoped[i].Index = region
	}
	cells := crossSections(scoped)
	for i := range cells {
		cells[i].region = region
	}
	return cells
}

// applyScopedBar sets a scoped test's statistics and verdict from its per-date
// series (all dates, and each half's). sign is the registered direction: +1
// or −1 requires t beyond ±adoptionT and both halves' means of that sign; 0
// is two-sided — |t| > adoptionT, both halves the sign of the overall mean.
func applyScopedBar(r TestResult, all, h1, h2 []float64, lags, sign int) TestResult {
	r.Status = "run"
	m, _ := meanSD(all)
	t := NeweyWestT(all, lags)
	r.Mean, r.T, r.NDates = Num(m), Num(t), len(finite(all))
	h1m, _ := meanSD(h1)
	h2m, _ := meanSD(h2)
	r.Halves = map[string]Num{"H1": Num(h1m), "H2": Num(h2m)}
	r.HalfNDates = map[string]int{"H1": len(finite(h1)), "H2": len(finite(h2))}

	dir, want := float64(sign), fmt.Sprintf("%+.1f", float64(sign)*adoptionT)
	if sign == 0 {
		dir, want = math.Copysign(1, m), fmt.Sprintf("|t| %.1f", adoptionT)
	}
	switch {
	case math.IsNaN(t) || !(dir*t > adoptionT):
		r.Verdict = "fails: t does not clear " + want
	case !(dir*h1m > 0 && dir*h2m > 0):
		r.Verdict = "fails: sign not the same in both halves"
	default:
		r.Pass = true
		r.Verdict = "passes the " + r.Scope + "-scoped bar"
	}
	return r
}

// evaluateScoped is evaluate for a region-scoped test: cells is
// scopedCells(recs, region), whose one pooled cell per date makes f's value
// the date's statistic. The Newey-West lags are the 10-session horizon's, as
// in evaluate; there is no region leg, since the test lives in one region.
func evaluateScoped(r TestResult, cells []cell, mid string, sign int, f func(cell) float64) TestResult {
	if len(cells) > 0 {
		r.Scope = cells[0].region
	}
	keep := func(cell) bool { return true }
	_, all := dateSeries(cells, keep, f)
	_, h1 := dateSeries(cells, func(c cell) bool { return c.date < mid }, f)
	_, h2 := dateSeries(cells, func(c cell) bool { return c.date >= mid }, f)
	return applyScopedBar(r, all, h1, h2, nwLags(10), sign)
}

// SignalBook is a scoped weekly book's summary: each week, the region's
// picksPerIndex names with the largest |signal|, each held at the signal's
// sign for Horizons[bookHorizon] sessions, equal-weighted, scored on the
// beta-adjusted excess and charged the lab's 30bp once per name.
type SignalBook struct {
	Signal string `json:"signal"`
	Region string `json:"region"`
	// Weeks counts weeks with a priced book; FullWeeks those whose book had all
	// picksPerIndex names (a sparse signal can leave fewer eligible, and the
	// book then holds what there is). MeanNames is the mean book size over Weeks.
	Weeks        int `json:"weeks"`
	FullWeeks    int `json:"full_weeks"`
	MeanNames    Num `json:"mean_names"`
	MeanGrossPct Num `json:"mean_beta_adjusted_gross_pct"`
	MeanNetPct   Num `json:"mean_beta_adjusted_net_pct"`
	H1NetPct     Num `json:"h1_beta_adjusted_net_pct"`
	H2NetPct     Num `json:"h2_beta_adjusted_net_pct"`
}

// scopedSignalBook builds one date's book from that date's scoped records:
// the picksPerIndex largest |sig| (ties on the ticker), held at sig's sign. A
// NaN or zero signal has no direction and is never held.
func scopedSignalBook(week []Record, sig int) []BookPick {
	var cands []Record
	for _, r := range week {
		if v := r.Sig[sig]; !math.IsNaN(v) && v != 0 {
			cands = append(cands, r)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := math.Abs(cands[i].Sig[sig]), math.Abs(cands[j].Sig[sig])
		if a != b {
			return a > b
		}
		return cands[i].Ticker < cands[j].Ticker
	})
	if len(cands) > picksPerIndex {
		cands = cands[:picksPerIndex]
	}
	book := make([]BookPick, len(cands))
	for i, r := range cands {
		dir := math.Copysign(1, r.Sig[sig])
		book[i] = BookPick{
			Ticker: r.Ticker, Sector: r.Sector, Index: r.Index, Region: r.Region, Dir: dir,
			XS: dir * r.XS[bookHorizon], BX: dir * r.BX[bookHorizon],
		}
	}
	return book
}

// signalBookTest replays scopedSignalBook over every date of region's scoped
// records and applies the scoped bar (applyScopedBar, sign as there) to the
// weekly net-of-cost beta-adjusted book excess, with Newey-West lags at the
// book's own 15-session horizon. A week whose book has no priced pick (its
// forward window runs past the data) is left out, as in BuildBookGrid.
func signalBookTest(r TestResult, recs []Record, region string, sig int, mid string, sign int) (TestResult, SignalBook) {
	byDate := map[string][]Record{}
	var dates []string
	for _, rec := range scopeRecords(recs, region) {
		if _, ok := byDate[rec.Date]; !ok {
			dates = append(dates, rec.Date)
		}
		byDate[rec.Date] = append(byDate[rec.Date], rec)
	}
	sort.Strings(dates)

	sb := SignalBook{Signal: SignalNames[sig], Region: region}
	var all, h1, h2 []float64
	names := 0
	for _, d := range dates {
		book := scopedSignalBook(byDate[d], sig)
		gross := bookReturn(book, true)
		if math.IsNaN(gross) {
			continue
		}
		sb.Weeks++
		names += len(book)
		if len(book) == picksPerIndex {
			sb.FullWeeks++
		}
		net := gross - costPerLeg // equal weights: each name's 30bp, once
		all = append(all, net)
		if d < mid {
			h1 = append(h1, net)
		} else {
			h2 = append(h2, net)
		}
	}
	if sb.Weeks > 0 {
		sb.MeanNames = Num(float64(names) / float64(sb.Weeks))
	} else {
		sb.MeanNames = Num(math.NaN())
	}
	m, _ := meanSD(all)
	m1, _ := meanSD(h1)
	m2, _ := meanSD(h2)
	sb.MeanGrossPct, sb.MeanNetPct = Num(100*(m+costPerLeg)), Num(100*m)
	sb.H1NetPct, sb.H2NetPct = Num(100*m1), Num(100*m2)

	r.Scope = region
	return applyScopedBar(r, all, h1, h2, nwLags(Horizons[bookHorizon]), sign), sb
}

// ScopedReport is the US-scoped block of the report: the registered Wave D
// tests (docs/workflow/backtest.md) — D1, drift's IC10; D2, the drift book;
// D3, earn_window's IC10 (C2 approximated) — each counted in TestsRun when it
// runs.
type ScopedReport struct {
	Region string `json:"region"`
	Scope  string `json:"scope"`
	// Dates counts dates with at least one scoped row; each test's n_dates is
	// how many of them had a finite statistic (for an IC, ≥ minCrossSection
	// names with the signal), and half_n_dates splits that by half.
	Dates     int          `json:"dates"`
	Tests     []TestResult `json:"tests"`
	DriftBook SignalBook   `json:"drift_book"`
}

// usScoped runs D1–D3, each registered with direction +1: the beta-adjusted
// IC10 of drift (D1) and of earn_window (D3; earn_window is 0/1, so its IC is
// a rank IC with ties at average rank, and its quintile spreads are not read),
// and the top-5-by-|drift| book net of 30bp (D2). A test with no date carrying
// a finite statistic — no filing history at all, as without an SEC contact
// address — is recorded "untestable" rather than as a failed run.
func usScoped(recs []Record, mid string) ScopedReport {
	cells := scopedCells(recs, "US")
	rep := ScopedReport{Region: "US", Scope: usScopeNote, Dates: len(cells)}
	const h10 = 1
	ic := func(id string, sig int, hyp string) TestResult {
		return evaluateScoped(TestResult{
			ID:        id,
			Title:     "US-scoped beta-adjusted IC10 of " + SignalNames[sig] + ": " + hyp,
			Statistic: "per-date Spearman IC10 vs beta-adjusted excess over the pooled US cross-section, Newey-West t (3 lags)",
		}, cells, mid, +1, func(c cell) float64 { return c.bic[sig][h10] })
	}
	book, sb := signalBookTest(TestResult{
		ID:        "D2",
		Title:     fmt.Sprintf("US top-%d-by-|drift| weekly book, held at drift's sign", picksPerIndex),
		Statistic: fmt.Sprintf("weekly equal-weighted beta-adjusted %d-session excess net of 30bp, Newey-West t (%d lags)", Horizons[bookHorizon], nwLags(Horizons[bookHorizon])),
	}, recs, "US", SigDrift, mid, +1)
	rep.DriftBook = sb
	rep.Tests = []TestResult{
		ic("D1", SigDrift, "post-earnings drift continues over the next 10 sessions"),
		book,
		ic("D3", SigEarnWindow, "C2's earnings-announcement premium, the release date extrapolated from cadence"),
	}
	for i, t := range rep.Tests {
		if t.NDates == 0 {
			rep.Tests[i].Status, rep.Tests[i].Pass = "untestable", false
			rep.Tests[i].Verdict = "not run: no date had a finite statistic (no earnings-release history)"
		}
	}
	return rep
}
