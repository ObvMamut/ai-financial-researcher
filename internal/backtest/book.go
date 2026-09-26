package backtest

// Book builder for E2 (docs/plans/2026-09-25-inputs-and-lab-before-edge.md §4):
// a live-shaped weekly book, replayed under a grid of max_per_sector values, to
// answer whether the sector cap that dropped ORCL for SAP.DE on a 0.5% merit
// gap costs or saves the book across ~500 weeks rather than one run (lead 2).
//
// The live funnel is: scouts nominate per index → the merged shortlist is
// capped per index → merit_veto holds each sector to max_per_sector → the top
// 5 ship. There is no model here (no scout call, no Chief), so the scout step
// is mechanically stood in for by a top-|composite| cut per index — the risk
// section of task-6-brief.md names this explicitly: the LLM scouts' actual
// nominations differ from a composite cut, so this file is a check on that
// model, not a replacement for the live shadow arm that reads real scout
// output.
//
// Reuses picksPerIndex and the horizon convention from barrier.go (the book's
// final top-5 cut and its 15-session hold are the same trade the barrier study
// already prices), the Record.Sector field addIndustryMomentum reads
// (panel.go), and orchestrator's own funnel defaults (DefaultMaxPerIndex,
// DefaultMaxPerSector) rather than copies of the numbers.

import (
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
type BookPick struct {
	Ticker string
	Sector string
	Index  string
	Dir    float64 // +1 long, -1 short (sign of the composite that picked it)
	XS     float64 // dir * plain benchmark-excess at bookHorizon
	BX     float64 // dir * beta-adjusted excess at bookHorizon
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
	sort.SliceStable(pool, func(i, j int) bool {
		return math.Abs(pool[i].Sig[SigScore]) > math.Abs(pool[j].Sig[SigScore])
	})

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
			Ticker: r.Ticker, Sector: r.Sector, Index: r.Index, Dir: dir,
			XS: dir * r.XS[bookHorizon], BX: dir * r.BX[bookHorizon],
		})
	}
	return book
}

// bookReturn is the book's equal-weighted mean return over its picks, dropping
// a pick whose target is unknown (its forward window ran past the data). NaN
// when no pick in the book has one.
func bookReturn(book []BookPick, beta bool) float64 {
	sum, n := 0.0, 0
	for _, p := range book {
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

// worst4WeekPct is the most negative sum of any 4 consecutive values in weekly
// (already in date order). Nothing in this lab compounds
// returns into a NAV — the quintile-spread and barrier statistics are also
// sums/means of per-period returns — so this is a rolling worst-month sum, not
// a peak-to-trough drawdown off a compounded equity curve. NaN below 4 weeks
// or if every 4-week window touches a week with no priced book.
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
	Weeks          int `json:"weeks"`                         // weeks this cap produced a non-empty book
	MeanExcessPct  Num `json:"mean_excess_pct"`               // plain benchmark-excess, for reference
	MeanBetaAdjPct Num `json:"mean_beta_adjusted_excess_pct"` // C4 target — the headline figure
	WeeklySDPct    Num `json:"weekly_book_sd_pct"`            // sd of the beta-adjusted weekly series
	Worst4WeekPct  Num `json:"worst_4week_pct"`               // most negative 4-week sum, beta-adjusted
}

// summarizeBookWeeks reduces one arm's per-week (plain, beta-adjusted) return
// pairs to BookCapStats. plain and beta hold one entry per week this cap
// produced a non-empty book, in ascending date order; either entry may itself
// be NaN (bookReturn found no picks with a known target that week), which
// meanSD/worst4WeekPct already treat as missing rather than zero.
func summarizeBookWeeks(plain, beta []float64) BookCapStats {
	mp, _ := meanSD(plain)
	mb, sb := meanSD(beta)
	return BookCapStats{
		Weeks: len(plain), MeanExcessPct: Num(100 * mp), MeanBetaAdjPct: Num(100 * mb),
		WeeklySDPct: Num(100 * sb), Worst4WeekPct: Num(100 * worst4WeekPct(beta)),
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
}

// BuildBookGrid replays buildWeeklyBook over every date in recs for each value
// in SectorCaps. mid is the same second-half boundary the rest of the report
// uses (midDate(cells) in run.go), so this grid's H1/H2 split lines up with
// every other table.
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
	for _, sectorCap := range SectorCaps {
		var allP, allB, h1P, h1B, h2P, h2B []float64
		for _, d := range dates {
			book := buildWeeklyBook(byDate[d], maxPerIndex, sectorCap)
			if len(book) == 0 {
				continue
			}
			p, b := bookReturn(book, false), bookReturn(book, true)
			allP, allB = append(allP, p), append(allB, b)
			if d < mid {
				h1P, h1B = append(h1P, p), append(h1B, b)
			} else {
				h2P, h2B = append(h2P, p), append(h2B, b)
			}
		}
		grid.Arms = append(grid.Arms, BookCapResult{
			MaxPerSector: sectorCap,
			All:          summarizeBookWeeks(allP, allB),
			H1:           summarizeBookWeeks(h1P, h1B),
			H2:           summarizeBookWeeks(h2P, h2B),
		})
	}
	return grid
}
