package backtest

// Book builder for E2 (docs/plans/2026-09-25-inputs-and-lab-before-edge.md §4):
// a live-shaped weekly book, replayed under a grid of max_per_sector values, to
// answer whether the sector cap that dropped ORCL for SAP.DE on a 0.5% merit
// gap costs or saves the book across ~500 weeks rather than one run (lead 2).
//
// The live funnel is: scouts nominate per index → the merged shortlist is
// deduped across cross-listings and capped per index → merit_veto holds each
// sector to max_per_sector → the top 5 ship. There is no model here (no scout
// call, no Chief), so the scout step is mechanically stood in for by a
// top-|composite| cut per index — the risk section of task-6-brief.md names
// this explicitly: the LLM scouts' actual nominations differ from a composite
// cut, so this file is a check on that model, not a replacement for the live
// shadow arm that reads real scout output. Two further live steps are not
// modeled at all: max_shortlist (12, the merged-shortlist cap that runs before
// max_per_index/max_per_sector even see the list) and the shortlist's own
// max_per_sector+1 reservation (the "one spare per sector" buffer the merit
// sort carries so the risk gate has something to choose between). Both are
// shortlist-construction details the mechanical scout stand-in already
// replaces wholesale; see docs/workflow/backtest.md's E2 section for why
// leaving them out does not change what this experiment can answer.
//
// Reuses picksPerIndex and the horizon convention from barrier.go (the book's
// final top-5 cut and its 15-session hold are the same trade the barrier study
// already prices), the Record.Sector and Record.Region fields (panel.go), and
// orchestrator's own funnel defaults (DefaultMaxPerIndex, DefaultMaxPerSector)
// rather than copies of the numbers.

import (
	"fmt"
	"math"
	"sort"

	"github.com/mamut/claude-financial-researcher/internal/orchestrator"
)

// scoutNominationsPerIndex stands in for the scouts' per-index nomination
// count. agents/scout.md asks each scout for "5-10 candidate tickers"; there
// is no recorded live average to read instead, so this takes the middle of
// that range rounded up to 8. Rounding up (rather than down to 5, the
// midpoint's floor) matters: at 5 the next step, max_per_index — itself 5 by
// default — would never bind, collapsing the funnel's two nomination-capping
// steps into one and understating how much the live pipeline discards before
// the sector cap ever runs.
const scoutNominationsPerIndex = 8

// SectorCaps is the max_per_sector grid E2 measures: the live default (2,
// orchestrator.DefaultMaxPerSector), its two neighbouring integers, and 0
// ("off" — no cap), so the grid brackets the live setting on both sides plus
// the uncapped baseline.
var SectorCaps = []int{1, 2, 3, 0}

// bookHorizon indexes Horizons (== 15 sessions), matching barrierHorizon: the
// book's picks are the same construction as the barrier study's trades (top-5
// |composite| per index per week, next-open entry, 15-session hold), so they
// are scored on the same clock.
const bookHorizon = 2

// BookPick is one name in a weekly book, carrying the direction of its own
// composite's sign — the convention barrier.go's trade.dir already uses for
// "top 5" — so a later long/short split can read Dir directly per pick.
// Region is carried per pick (not assumed for the whole book, which pools all
// four indices in one basket) so the paired adoption test below can restrict
// to one region's picks.
type BookPick struct {
	Ticker string
	Sector string
	Index  string
	Region string
	Dir    float64 // +1 long, -1 short (sign of the composite that picked it)
	XS     float64 // dir * plain benchmark-excess at bookHorizon
	BX     float64 // dir * beta-adjusted excess at bookHorizon
}

// dedupeTickers keeps one Record per ticker, the one with the higher
// |composite|, and sorts the result by |composite| descending (ties broken on
// the ticker string, for a deterministic order independent of map iteration).
//
// 35 of nq100's 56 names also sit in sp500, each standardised — and so scored
// — within its own index's cross-section (docs/workflow/independent-research.md's
// "Composite, z-scored within each index"), so a cross-listed name generally
// carries two different composite values, one per index. Pooling per-index
// survivors without this let one ticker take two book slots and two
// sector-cap slots. The live merge step dedupes cross-listings the same way
// before ranking (CLAUDE.md step 1: "Orchestrator merges/dedupes (incl.
// cross-listings)"), and the lab already does this for sector momentum
// (addIndustryMomentum's a.seen[r.Ticker], panel.go).
func dedupeTickers(pool []Record) []Record {
	sort.SliceStable(pool, func(i, j int) bool {
		si, sj := math.Abs(pool[i].Sig[SigScore]), math.Abs(pool[j].Sig[SigScore])
		if si != sj {
			return si > sj
		}
		return pool[i].Ticker < pool[j].Ticker
	})
	seen := make(map[string]bool, len(pool))
	out := make([]Record, 0, len(pool))
	for _, r := range pool {
		if seen[r.Ticker] {
			continue
		}
		seen[r.Ticker] = true
		out = append(out, r)
	}
	return out
}

// buildWeeklyBook constructs one date's book from that date's records, given
// the scout stand-in above and the two caps the live funnel applies next.
//
// The walk is one merit-ordered pass, exactly like the live merit_veto's
// pickBook (internal/orchestrator/selection.go): candidates are taken in
// descending |composite| order, a sector at its cap is skipped rather than
// stopping the walk, and the first picksPerIndex survivors are the book. A
// zero maxPerSector means no cap ("off").
func buildWeeklyBook(week []Record, maxPerIndex, maxPerSector int) []BookPick {
	byIndex := map[string][]Record{}
	for _, r := range week {
		s := r.Sig[SigScore]
		if math.IsNaN(s) || s == 0 {
			continue
		}
		byIndex[r.Index] = append(byIndex[r.Index], r)
	}

	var pool []Record
	for _, recs := range byIndex {
		sort.SliceStable(recs, func(i, j int) bool {
			return math.Abs(recs[i].Sig[SigScore]) > math.Abs(recs[j].Sig[SigScore])
		})
		if len(recs) > scoutNominationsPerIndex {
			recs = recs[:scoutNominationsPerIndex] // stands in for the scout call
		}
		if len(recs) > maxPerIndex {
			recs = recs[:maxPerIndex] // the live shortlist's per-index cap
		}
		pool = append(pool, recs...)
	}
	pool = dedupeTickers(pool) // cross-listings count once; see dedupeTickers

	var book []BookPick
	perSector := map[string]int{}
	for _, r := range pool {
		if len(book) == picksPerIndex { // "the top 5" — barrier.go's book size
			break
		}
		if maxPerSector > 0 && r.Sector != "" && perSector[r.Sector] >= maxPerSector {
			continue
		}
		perSector[r.Sector]++
		dir := math.Copysign(1, r.Sig[SigScore])
		book = append(book, BookPick{
			Ticker: r.Ticker, Sector: r.Sector, Index: r.Index, Region: r.Region, Dir: dir,
			XS: dir * r.XS[bookHorizon], BX: dir * r.BX[bookHorizon],
		})
	}
	return book
}

// bookReturnFiltered is the equal-weighted mean return over the picks keep
// selects, dropping a pick whose target is unknown (its forward window ran
// past the data). NaN when no matching pick has one.
func bookReturnFiltered(book []BookPick, beta bool, keep func(BookPick) bool) float64 {
	sum, n := 0.0, 0
	for _, p := range book {
		if !keep(p) {
			continue
		}
		v := p.XS
		if beta {
			v = p.BX
		}
		if math.IsNaN(v) {
			continue
		}
		sum += v
		n++
	}
	if n == 0 {
		return math.NaN()
	}
	return sum / float64(n)
}

// bookReturn is the whole book's equal-weighted mean return.
func bookReturn(book []BookPick, beta bool) float64 {
	return bookReturnFiltered(book, beta, func(BookPick) bool { return true })
}

// worst4WeekPct is the most negative sum of any 4 consecutive values in weekly
// (already in date order). Nothing in this lab compounds returns into a NAV —
// the quintile-spread and barrier statistics are also sums/means of
// per-period returns — so this is a rolling worst-month sum, not a
// peak-to-trough drawdown off a compounded equity curve. NaN below 4 weeks or
// if every 4-week window touches a week with no priced book.
//
// Each input value is already a 15-session (~3-week) hold's return, so any 4
// consecutive weekly values span holds that overlap by construction — see
// BookCapStats.Worst4WkOverlapPct, the field that reports this, for why that
// is surfaced in the name rather than corrected for.
func worst4WeekPct(weekly []float64) float64 {
	worst := math.Inf(1)
	for i := 0; i+4 <= len(weekly); i++ {
		sum, ok := 0.0, true
		for _, v := range weekly[i : i+4] {
			if math.IsNaN(v) {
				ok = false
				break
			}
			sum += v
		}
		if ok && sum < worst {
			worst = sum
		}
	}
	if math.IsInf(worst, 1) {
		return math.NaN()
	}
	return worst
}

// BookCapStats is one max_per_sector value's outcome over one slice (all, H1
// or H2) of the replay's weeks. The headline trio — mean, sd, worst 4-week —
// is reported against the beta-adjusted target (C4); plain benchmark-excess is
// shown as a mean only, for reference.
type BookCapStats struct {
	// Weeks counts only the weeks this cap produced a book with a known (finite)
	// beta-adjusted return — a tail week whose 15-session forward window runs
	// past the data has a book but no return, and must not inflate this count.
	Weeks          int `json:"weeks"`
	MeanExcessPct  Num `json:"mean_excess_pct"`               // plain benchmark-excess, for reference
	MeanBetaAdjPct Num `json:"mean_beta_adjusted_excess_pct"` // C4 target — the headline figure
	WeeklySDPct    Num `json:"weekly_book_sd_pct"`            // sd of the beta-adjusted weekly series
	// Worst4WkOverlapPct is the most negative sum of any 4 consecutive weekly
	// book returns (worst4WeekPct), each of which is itself a 15-session
	// (~3-week) hold. Consecutive weekly books therefore already overlap in
	// the sessions they're held over — this is a rolling worst-4-week *sum of
	// overlapping cohorts' returns*, not a capital-scaled portfolio drawdown:
	// modelling how much of one cohort's capital is actually still at risk
	// while the next two are open is a portfolio-construction question this
	// lab does not answer, so the field is named for what it measures rather
	// than presented as a true book-level drawdown.
	Worst4WkOverlapPct Num `json:"worst_4wk_overlap_pct"`
}

// summarizeBookCap reduces one arm's weeks (keyed by date) to BookCapStats
// over the dates keep selects. A date absent from weeks (that cap's book was
// empty that week) is skipped, exactly like a date whose book return is NaN.
func summarizeBookCap(weeks map[string]bookWeek, dates []string, keep func(string) bool) BookCapStats {
	var plain, beta []float64
	for _, d := range dates {
		if !keep(d) {
			continue
		}
		w, ok := weeks[d]
		if !ok {
			continue
		}
		plain = append(plain, w.plain)
		beta = append(beta, w.beta)
	}
	mp, _ := meanSD(plain)
	mb, sb := meanSD(beta)
	return BookCapStats{
		Weeks: len(finite(beta)), MeanExcessPct: Num(100 * mp), MeanBetaAdjPct: Num(100 * mb),
		WeeklySDPct: Num(100 * sb), Worst4WkOverlapPct: Num(100 * worst4WeekPct(beta)),
	}
}

// BookCapResult is one max_per_sector value's grid row.
type BookCapResult struct {
	MaxPerSector int          `json:"max_per_sector"` // 0 means "off" (no cap)
	All          BookCapStats `json:"all"`
	H1           BookCapStats `json:"h1"`
	H2           BookCapStats `json:"h2"`
}

// BookGrid is E2's whole sector-cap grid.
type BookGrid struct {
	NominationsPerIndex int             `json:"nominations_per_index"` // scoutNominationsPerIndex
	MaxPerIndex         int             `json:"max_per_index"`         // orchestrator.DefaultMaxPerIndex
	Arms                []BookCapResult `json:"arms"`
	// PairedTests is one adoption test per non-default cap in SectorCaps
	// (docs/workflow/backtest.md's E2 section): the per-week difference in
	// beta-adjusted book excess against the live default's arm.
	PairedTests []TestResult `json:"paired_tests"`
}

// bookWeek is one date's outcome for one grid arm: the book's overall plain
// and beta-adjusted returns, and the beta-adjusted return restricted to each
// region's picks. A book pools all four indices in one basket (there is no
// single region per week the way a per-index cell in analyze.go has one), so
// the region breakdown is a second, narrower statistic computed from the same
// week's book rather than a different book.
type bookWeek struct {
	plain, beta  float64
	betaByRegion map[string]float64
}

// weekStats reduces one date's book to a bookWeek.
func weekStats(book []BookPick) bookWeek {
	bw := bookWeek{plain: bookReturn(book, false), beta: bookReturn(book, true), betaByRegion: map[string]float64{}}
	for _, reg := range Regions {
		bw.betaByRegion[reg] = bookReturnFiltered(book, true, func(p BookPick) bool { return p.Region == reg })
	}
	return bw
}

// diffSeries pairs cand and def on the dates both have a value for (via pick)
// and both values are finite, returning candidate-minus-default. A date
// either side lacks — including one where pick reads an unpopulated region —
// is dropped rather than treated as zero.
func diffSeries(cand, def map[string]bookWeek, dates []string, pick func(bookWeek) float64) []float64 {
	var out []float64
	for _, d := range dates {
		cw, ok1 := cand[d]
		dw, ok2 := def[d]
		if !ok1 || !ok2 {
			continue
		}
		cv, dv := pick(cw), pick(dw)
		if math.IsNaN(cv) || math.IsNaN(dv) {
			continue
		}
		out = append(out, cv-dv)
	}
	return out
}

// sameSign reports whether a and b are both strictly positive or both
// strictly negative; NaN or zero on either side is never "the same sign".
func sameSign(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) || a == 0 || b == 0 {
		return false
	}
	return (a > 0) == (b > 0)
}

// pairedCapTest is E2's per-cap adoption test: the candidate arm's per-week
// beta-adjusted book excess minus the live default arm's, on weeks both
// priced, its Newey-West t (nwLags at the book's own 15-session horizon), and
// its mean in each half and region.
//
// The bar is the Wave B rule from task-6-brief.md's controller ruling —
// |t| > 2.5, same sign in both halves and every region — which is two-sided,
// unlike the one-sided pre-registered signal tests above (adoptionT's own
// comment: "the pre-registered direction is positive for every test"). Those
// tests each propose a specific directional improvement; a departure from the
// live max_per_sector could plausibly help or hurt, so both directions must be
// eligible to pass here, provided the sign is internally consistent.
func pairedCapTest(id, title string, cand, def map[string]bookWeek, dates []string, mid string) TestResult {
	r := TestResult{
		ID: id, Title: title,
		Statistic: "per-week diff (candidate minus the live default) of beta-adjusted book excess, Newey-West t",
	}
	overall := func(w bookWeek) float64 { return w.beta }
	all := diffSeries(cand, def, dates, overall)
	r.NDates = len(all)
	m, _ := meanSD(all)
	t := NeweyWestT(all, nwLags(Horizons[bookHorizon]))
	r.Mean, r.T = Num(m), Num(t)
	r.P = Num(twoSidedP(t))

	var h1dates, h2dates []string
	for _, d := range dates {
		if d < mid {
			h1dates = append(h1dates, d)
		} else {
			h2dates = append(h2dates, d)
		}
	}
	h1m, _ := meanSD(diffSeries(cand, def, h1dates, overall))
	h2m, _ := meanSD(diffSeries(cand, def, h2dates, overall))
	r.Halves = map[string]Num{"H1": Num(h1m), "H2": Num(h2m)}

	consistent := sameSign(h1m, m) && sameSign(h2m, m)
	r.Regions = map[string]Num{}
	for _, reg := range Regions {
		rm, _ := meanSD(diffSeries(cand, def, dates, func(w bookWeek) float64 { return w.betaByRegion[reg] }))
		r.Regions[reg] = Num(rm)
		if !sameSign(rm, m) {
			consistent = false
		}
	}

	r.Status = "run"
	switch {
	case !(math.Abs(t) > adoptionT):
		r.Verdict = "fails: |t| does not clear 2.5"
	case !consistent:
		r.Verdict = "fails: sign not the same in both halves and every region"
	default:
		r.Pass = true
		dir := "outperforms"
		if m < 0 {
			dir = "underperforms"
		}
		r.Verdict = fmt.Sprintf("passes the adoption bar: %s the live default", dir)
	}
	return r
}

// capLabel is the grid's display label for a max_per_sector value: the number,
// or "off" for 0 (no cap).
func capLabel(sectorCap int) string {
	if sectorCap == 0 {
		return "off"
	}
	return fmt.Sprintf("%d", sectorCap)
}

// BuildBookGrid replays buildWeeklyBook over every date in recs for each value
// in SectorCaps, then — for every cap other than the live default — pairs its
// weekly series against the default arm's for the adoption test above. mid is
// the same second-half boundary the rest of the report uses (midDate(cells) in
// run.go), so this grid's H1/H2 split lines up with every other table.
func BuildBookGrid(recs []Record, mid string) BookGrid {
	byDate := map[string][]Record{}
	var dates []string
	for _, r := range recs {
		if _, ok := byDate[r.Date]; !ok {
			dates = append(dates, r.Date)
		}
		byDate[r.Date] = append(byDate[r.Date], r)
	}
	sort.Strings(dates)

	maxPerIndex := orchestrator.DefaultMaxPerIndex
	grid := BookGrid{NominationsPerIndex: scoutNominationsPerIndex, MaxPerIndex: maxPerIndex}

	weeksByCap := make(map[int]map[string]bookWeek, len(SectorCaps))
	for _, sectorCap := range SectorCaps {
		weeks := map[string]bookWeek{}
		for _, d := range dates {
			book := buildWeeklyBook(byDate[d], maxPerIndex, sectorCap)
			if len(book) == 0 {
				continue
			}
			weeks[d] = weekStats(book)
		}
		weeksByCap[sectorCap] = weeks
	}

	for _, sectorCap := range SectorCaps {
		weeks := weeksByCap[sectorCap]
		grid.Arms = append(grid.Arms, BookCapResult{
			MaxPerSector: sectorCap,
			All:          summarizeBookCap(weeks, dates, func(string) bool { return true }),
			H1:           summarizeBookCap(weeks, dates, func(d string) bool { return d < mid }),
			H2:           summarizeBookCap(weeks, dates, func(d string) bool { return d >= mid }),
		})
	}

	def := weeksByCap[orchestrator.DefaultMaxPerSector]
	for _, sectorCap := range SectorCaps {
		if sectorCap == orchestrator.DefaultMaxPerSector {
			continue
		}
		label := capLabel(sectorCap)
		grid.PairedTests = append(grid.PairedTests, pairedCapTest(
			"E2-"+label,
			fmt.Sprintf("max_per_sector=%s vs the live default (%d)", label, orchestrator.DefaultMaxPerSector),
			weeksByCap[sectorCap], def, dates, mid,
		))
	}
	return grid
}
