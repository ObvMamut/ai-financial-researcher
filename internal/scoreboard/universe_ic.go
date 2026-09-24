package scoreboard

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/store"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// Whole-universe IC: does the pre-screen's ranking predict the next two to three
// weeks across *every* name it ranked, not only the five at its ends?
//
// The composite arm scores five calls a run. At that rate a 50bp edge takes a
// year of runs to see. But the pre-screen ranks the whole sample — some 268
// names — and every one of those rows is a forecast whose outcome arrives on
// the same clock as the five. Rank-correlating each run's scores with the
// realised benchmark-excess returns turns one run into one IC measured over
// hundreds of names: the fastest honest read on whether the screen works out of
// sample, going forward.
//
// The correlation is taken within each index and the indices averaged, because
// that is how the pre-screen itself standardises: a score is a z-score inside
// its own index, and pooling sp500 and asia100 scores would rank two different
// scales against each other. Within one single-market index every name shares a
// benchmark, so ranking excess returns and ranking raw returns agree; the
// benchmark matters for asia100, whose names are measured against their own
// exchange.

// UniverseICHorizons are the windows the universe IC is always reported over,
// whatever --horizon the arms use: the thesis horizon is 10–15 sessions and the
// question is whether the screen predicts either end of it.
var UniverseICHorizons = []int{10, 15}

// minICNames is the fewest completed rows an index needs before its rank
// correlation is computed. Below it one outlier decides the sign.
const minICNames = 10

// UniverseIC is the whole-universe rank correlation at one horizon.
type UniverseIC struct {
	HorizonDays int `json:"horizon_days"`
	// Runs is how many runs contributed an IC; MeanIC is their plain average.
	Runs   int     `json:"runs"`
	MeanIC float64 `json:"mean_ic"`
	// CI is the week-clustered 95% bootstrap interval on MeanIC, runs as the
	// units: runs in one week rank the same market move and are one draw.
	CI *ExcessCI `json:"ci,omitempty"`
	// Pending counts runs whose window has not elapsed for enough names.
	Pending int `json:"pending"`
	// Repeats counts (run, index) rankings dropped because an earlier run had
	// already ranked that index off exactly the same closes. Five runs one
	// afternoon, or a Saturday and a Sunday run, are one ranking, not several.
	Repeats int     `json:"repeat_rankings_dropped,omitempty"`
	PerRun  []RunIC `json:"per_run,omitempty"`
}

// RunIC is one run's universe IC: the average of its per-index correlations.
type RunIC struct {
	Run         string             `json:"run"`
	GeneratedAt string             `json:"generated_at"`
	IC          float64            `json:"ic"`
	N           int                `json:"n"`
	ByIndex     map[string]float64 `json:"by_index"`
}

// runUniverseIC is one run's IC at horizon h. ok is false while fewer than
// minICNames rows in every index have a completed window.
func runUniverseIC(ctx context.Context, cache *seriesCache, r store.RunSummary,
	generatedAt string, rows []prescreenRow, h int) (RunIC, bool) {

	genDate := dateOf(generatedAt)
	type obs struct{ score, excess float64 }
	byIndex := map[string][]obs{}
	for _, row := range rows {
		if row.Excluded != "" || row.Ticker == "" {
			continue
		}
		s, err := cache.get(ctx, row.Ticker, r.Dir)
		if err != nil || s == nil || len(s.Bars) == 0 {
			continue
		}
		anchor := row.Close
		if anchor <= 0 {
			anchor = closeOnOrBefore(s, genDate)
		}
		res := measureHorizon(ctx, cache, barsAfter(s, genDate), anchor,
			model.DirectionBuy, h, benchmarkFor(row.Index, row.Ticker), genDate)
		if !res.complete {
			continue
		}
		byIndex[row.Index] = append(byIndex[row.Index], obs{row.Score, res.excess})
	}

	out := RunIC{Run: r.Name, GeneratedAt: generatedAt, ByIndex: map[string]float64{}}
	var sum float64
	for idx, list := range byIndex {
		if len(list) < minICNames {
			continue
		}
		xs, ys := make([]float64, len(list)), make([]float64, len(list))
		for i, o := range list {
			xs[i], ys[i] = o.score, o.excess
		}
		ic, ok := spearman(xs, ys)
		if !ok {
			continue
		}
		out.ByIndex[idx] = round4(ic)
		out.N += len(list)
		sum += ic
	}
	if len(out.ByIndex) == 0 {
		return out, false
	}
	out.IC = round4(sum / float64(len(out.ByIndex)))
	return out, true
}

// icRun is what the IC aggregation needs from one run.
type icRun struct {
	summary     store.RunSummary
	generatedAt string
	rows        []prescreenRow
}

// dedupeRankings keeps, for each run oldest first, only the indices whose
// anchor session no earlier run has already ranked, and counts the (run, index)
// rankings it dropped. sessionOf names the session an index's ranking on a
// generation date is anchored to.
//
// The key is the session rather than the calendar date, because the session is
// what a ranking is measured from: a Saturday and a Sunday run rank the same
// Friday closes, and a run on a US holiday repeats the US ranking while its
// European rows have moved on. Nor is it the closes themselves: a run during
// market hours sees a live partial bar, so five runs in one afternoon carry
// five slightly different closes and are still one ranking of one day. Keeping
// the earliest matches Dedupe's rule that the first time a call was made is
// when it was made.
func dedupeRankings(runs []icRun, sessionOf func(index, date string) string) ([]icRun, int) {
	sorted := append([]icRun(nil), runs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].generatedAt < sorted[j].generatedAt })
	seen := map[string]bool{}
	dropped := 0
	var out []icRun
	for _, r := range sorted {
		date := dateOf(r.generatedAt)
		indices := map[string]bool{}
		for _, row := range r.rows {
			if row.Excluded == "" && row.Ticker != "" {
				indices[row.Index] = true
			}
		}
		keep := map[string]bool{}
		for idx := range indices {
			k := idx + "\x00" + sessionOf(idx, date)
			if seen[k] {
				dropped++
				continue
			}
			seen[k] = true
			keep[idx] = true
		}
		if len(keep) == 0 {
			continue
		}
		var rows []prescreenRow
		for _, row := range r.rows {
			if keep[row.Index] {
				rows = append(rows, row)
			}
		}
		out = append(out, icRun{summary: r.summary, generatedAt: r.generatedAt, rows: rows})
	}
	return out, dropped
}

// universeICs measures every distinct ranking at each horizon.
func universeICs(ctx context.Context, cache *seriesCache, runs []icRun, horizons []int) []UniverseIC {
	distinct, repeats := dedupeRankings(runs, func(index, date string) string {
		return benchmarkSession(ctx, cache, index, date)
	})
	out := make([]UniverseIC, 0, len(horizons))
	for _, h := range horizons {
		u := UniverseIC{HorizonDays: h, Repeats: repeats}
		weeks := map[string]weekSum{}
		var sum float64
		for _, r := range distinct {
			ric, ok := runUniverseIC(ctx, cache, r.summary, r.generatedAt, r.rows, h)
			if !ok {
				u.Pending++
				continue
			}
			u.PerRun = append(u.PerRun, ric)
			u.Runs++
			sum += ric.IC
			if k, ok := weekKey(r.generatedAt); ok {
				w := weeks[k]
				w.sum += ric.IC
				w.n++
				weeks[k] = w
			}
		}
		if u.Runs > 0 {
			u.MeanIC = round4(sum / float64(u.Runs))
		}
		if ci, ok := clusteredMeanCI(weeks, round4); ok {
			u.CI = &ci
		}
		out = append(out, u)
	}
	return out
}

// benchmarkSession is the last session on or before date in the index's own
// benchmark: the session a ranking made that day is anchored to. Without a
// benchmark series the calendar date stands in for it.
func benchmarkSession(ctx context.Context, cache *seriesCache, index, date string) string {
	b, err := cache.get(ctx, universe.BenchmarkSymbol(index), "")
	if err != nil || b == nil {
		return date
	}
	i := sort.Search(len(b.Bars), func(i int) bool { return b.Bars[i].Date > date })
	if i == 0 {
		return date
	}
	return b.Bars[i-1].Date
}

// spearman is the rank correlation of xs and ys, ties taking their average
// rank. ok is false when either side has no variation to rank.
func spearman(xs, ys []float64) (float64, bool) {
	if len(xs) != len(ys) || len(xs) < 3 {
		return 0, false
	}
	return pearson(ranks(xs), ranks(ys))
}

func ranks(xs []float64) []float64 {
	idx := make([]int, len(xs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return xs[idx[a]] < xs[idx[b]] })
	out := make([]float64, len(xs))
	for i := 0; i < len(idx); {
		j := i
		for j+1 < len(idx) && xs[idx[j+1]] == xs[idx[i]] {
			j++
		}
		avg := float64(i+j)/2 + 1
		for k := i; k <= j; k++ {
			out[idx[k]] = avg
		}
		i = j + 1
	}
	return out
}

func pearson(a, b []float64) (float64, bool) {
	n := float64(len(a))
	var ma, mb float64
	for i := range a {
		ma += a[i]
		mb += b[i]
	}
	ma, mb = ma/n, mb/n
	var cov, va, vb float64
	for i := range a {
		da, db := a[i]-ma, b[i]-mb
		cov += da * db
		va += da * da
		vb += db * db
	}
	if va == 0 || vb == 0 {
		return 0, false
	}
	return cov / math.Sqrt(va*vb), true
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// formatLine renders one horizon's universe IC for the text report.
func (u UniverseIC) formatLine() string {
	var sb strings.Builder
	if u.Runs == 0 {
		sb.WriteString("—")
	} else {
		fmt.Fprintf(&sb, "%+.3f over %d run(s)", u.MeanIC, u.Runs)
		if u.CI != nil {
			fmt.Fprintf(&sb, ", 95%% CI [%+.3f, %+.3f] (%d weeks)", u.CI.Low, u.CI.High, u.CI.Weeks)
		} else {
			sb.WriteString(", no interval (fewer than two weeks)")
		}
	}
	if u.Pending > 0 {
		fmt.Fprintf(&sb, "; %d run(s) still inside the window", u.Pending)
	}
	return sb.String()
}
