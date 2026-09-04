package scoreboard

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// The control arm: does the model stack beat the arithmetic it is built on?
//
// The pipeline is a funnel. Stage 0.5 ranks the universe on a computed composite
// and the sign of that composite *is* a direction; the scouts then screen that
// ranking, the specialists analyse the twelve names it produced, and the Chief
// picks five of them. Every stage above the pre-screen can re-rank the funnel's
// output. None of them can reach a name the funnel did not pass.
//
// That makes one question decide whether any of the model stages earn their
// cost, and nothing in this repository could answer it: **would taking the top
// of the pre-screen ranking, with no model called at all, have done as well?**
//
// Control answers it by scoring three sets of directional calls through one
// identical procedure — same anchor, same horizon, same benchmark arithmetic:
//
//	composite  the pre-screen's own top names, direction = the sign of its score.
//	           No model was involved at any point.
//	shortlist  the twelve names the funnel passed, at the bias the scouts gave
//	           them. Scouts and the merge included; specialists and Chief not.
//	shipped    what ideas.json actually contains.
//
// Read it as a chain rather than as three numbers: shipped over composite is
// what the whole model stack adds, and shipped over shortlist is what the
// specialists and the Chief add on top of the screening they were handed.
//
// Deliberately absent: entries, stops, targets and fills. A control arm has no
// levels — the pre-screen never proposed any — so comparing on barrier outcomes
// would be comparing a trade against a call. Everything here is the call.

// DefaultControlHorizon is the window every arm is scored over, in sessions.
// One fixed number for all three, rather than each idea's own timeframe_days:
// the shipped ideas state a holding period and the control arms cannot, and a
// comparison in which one arm picks its own window is not a comparison. Fifteen
// sessions is three trading weeks — the horizon this pipeline is built for.
const DefaultControlHorizon = 15

// controlArmSize is how many names the composite arm takes per run. It matches
// the five the Chief ships, so the two arms are the same size bet.
const controlArmSize = 5

// prescreenRow is the subset of the orchestrator's PrescreenRow this package
// needs. The field tags must match internal/orchestrator's, which the artifact
// shape enforces: a rename there that is not made here shows up as an empty
// composite arm rather than as a wrong number.
type prescreenRow struct {
	Ticker   string  `json:"ticker"`
	Name     string  `json:"name"`
	Index    string  `json:"index"`
	Close    float64 `json:"close,omitempty"`
	Setup    string  `json:"setup,omitempty"`
	Score    float64 `json:"score"`
	Excluded string  `json:"excluded,omitempty"`
}

type prescreenFile struct {
	Rows []prescreenRow `json:"rows"`
}

// ControlArm is one set of directional calls and how they scored.
type ControlArm struct {
	Name    string        `json:"name"`
	Label   string        `json:"label"`
	Record  HorizonRecord `json:"record"`
	Entries []Entry       `json:"entries,omitempty"`
	// Pending counts calls whose window has not elapsed yet. They are not
	// misses, and an arm whose Record.N is small because of them is not a
	// verdict — it is a measurement still being taken.
	Pending int `json:"pending"`
	// Unmeasurable counts calls with no usable price history at all.
	Unmeasurable int `json:"unmeasurable"`
	// Duplicates counts calls dropped as re-proposals of a call this arm had
	// already made. Twenty-five runs over eight days re-proposed the same names
	// repeatedly, and an arm scored over the tickets rather than the bets would
	// report a sample several times larger than the evidence in it.
	Duplicates int `json:"duplicates"`
}

// ControlReport is the three arms plus the runs they were drawn from.
type ControlReport struct {
	HorizonDays int          `json:"horizon_days"`
	RunCount    int          `json:"run_count"`
	Arms        []ControlArm `json:"arms"`
	// Skipped names runs that carry no prescreen.json, so the composite arm
	// could not be reconstructed for them. Every run predating Stage 0.5 is one.
	Skipped []string `json:"skipped,omitempty"`
}

// call is one directional claim, before it is measured. The three arms differ
// only in how their calls are chosen.
type call struct {
	ticker     string
	name       string
	index      string
	direction  model.Direction
	anchor     float64 // the close the call was made off
	setup      string
	confidence int
}

// Control builds and scores the three arms over every run in runsDir.
func Control(ctx context.Context, runsDir string, yc marketdata.PriceSource, horizon int) (*ControlReport, error) {
	runs, err := store.ListRuns(runsDir)
	if err != nil {
		return nil, err
	}
	if horizon <= 0 {
		horizon = DefaultControlHorizon
	}
	rep := &ControlReport{HorizonDays: horizon}
	cache := &seriesCache{yc: yc, bySymbol: map[string]*quant.Series{}, byRun: map[string]*quant.Series{}}

	composite := ControlArm{Name: "composite", Label: "pre-screen composite, no model"}
	shortlist := ControlArm{Name: "shortlist", Label: "funnel output at the scouts' bias"}
	shipped := ControlArm{Name: "shipped", Label: "what the pipeline shipped"}

	for _, r := range runs {
		ideas, err := store.LoadIdeas(r.Dir)
		if err != nil || ideas == nil || len(ideas.Ideas) == 0 {
			continue
		}
		rep.RunCount++
		meta, _ := store.LoadMeta(r.Dir)

		var ps prescreenFile
		hasPrescreen, _ := store.ReadPrescreen(r.Dir, &ps)
		if !hasPrescreen {
			rep.Skipped = append(rep.Skipped, r.Name)
		}

		score := func(arm *ControlArm, calls []call) {
			for _, c := range calls {
				e, state := measureCall(ctx, cache, r, ideas.GeneratedAt, c, horizon)
				if state == callUnmeasurable {
					arm.Unmeasurable++
					continue
				}
				arm.Entries = append(arm.Entries, e)
			}
		}

		if hasPrescreen {
			score(&composite, compositeCalls(ps.Rows, controlArmSize))
		}
		if meta != nil {
			score(&shortlist, shortlistCalls(meta.Shortlist, ps.Rows))
		}
		score(&shipped, shippedCalls(ideas.Ideas))
	}

	rep.Arms = []ControlArm{composite, shortlist, shipped}
	// Each arm is deduplicated on its own entries rather than on the union: the
	// arms are three different sets of calls, and a name the shipped arm took
	// once and the shortlist arm took five times is one bet in each.
	for i := range rep.Arms {
		indep, dropped := Dedupe(rep.Arms[i].Entries, DefaultDedupeWindowDays)
		rep.Arms[i].Duplicates = dropped
		acc := &horizonAcc{}
		pending := 0
		for _, e := range indep {
			if e.CallDone {
				acc.add(e.CallPnLPct, e.CallExcessPct)
			} else {
				pending++
			}
		}
		rep.Arms[i].Record, rep.Arms[i].Pending = acc.record(), pending
	}
	return rep, nil
}

// compositeCalls takes the n strongest rows of a run's ranking, whichever
// direction they point. The composite is a signed long ranking, so the strongest
// calls sit at *both* ends of it: |score| is the conviction and its sign is the
// direction. Taking the top n would produce a long-only arm and measure a bull
// market rather than the ranking.
func compositeCalls(rows []prescreenRow, n int) []call {
	ranked := make([]prescreenRow, 0, len(rows))
	for _, r := range rows {
		if r.Excluded != "" || r.Score == 0 || r.Close <= 0 {
			continue
		}
		ranked = append(ranked, r)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := abs(ranked[i].Score), abs(ranked[j].Score)
		if a != b {
			return a > b
		}
		return ranked[i].Ticker < ranked[j].Ticker
	})

	out := make([]call, 0, n)
	seen := map[string]bool{}
	for _, r := range ranked {
		if len(out) == n {
			break
		}
		// A name in two indices has two rows and two composites. It is one bet,
		// and taking it twice would let a dual-listed name fill the arm.
		key := strings.ToUpper(r.Ticker)
		if seen[key] {
			continue
		}
		seen[key] = true
		dir := model.DirectionBuy
		if r.Score < 0 {
			dir = model.DirectionSell
		}
		out = append(out, call{ticker: r.Ticker, name: r.Name, index: r.Index,
			direction: dir, anchor: r.Close, setup: r.Setup})
	}
	return out
}

// shortlistCalls is the funnel's own output at the bias the scouts gave it. A
// neutral nomination is dropped: it is not a directional call and scoring it
// either way would invent one.
func shortlistCalls(cands []model.Candidate, rows []prescreenRow) []call {
	closes := map[string]float64{}
	for _, r := range rows {
		// Keyed by index too, because a dual-listed name has a row per index and
		// they carry the same close; first one wins either way.
		if r.Close > 0 {
			k := strings.ToUpper(r.Ticker)
			if _, ok := closes[k]; !ok {
				closes[k] = r.Close
			}
		}
	}
	out := make([]call, 0, len(cands))
	for _, c := range cands {
		var dir model.Direction
		switch c.Bias {
		case model.BiasBullish:
			dir = model.DirectionBuy
		case model.BiasBearish:
			dir = model.DirectionSell
		default:
			continue
		}
		out = append(out, call{ticker: c.Ticker, name: c.Name, index: c.Index,
			direction: dir, anchor: closes[strings.ToUpper(c.Ticker)], setup: c.Setup})
	}
	return out
}

// shippedCalls is what the pipeline actually returned, reduced to the same
// (ticker, direction, anchor) shape as the other two arms so all three are
// scored by one procedure.
func shippedCalls(ideas []model.TradeIdea) []call {
	out := make([]call, 0, len(ideas))
	for _, i := range ideas {
		out = append(out, call{ticker: i.Ticker, name: i.Name, index: i.Index,
			direction: i.Direction, anchor: i.PriceAtGeneration,
			setup: i.Setup, confidence: i.Confidence})
	}
	return out
}

// callState says why a call did or did not contribute to its arm's record.
type callState int

const (
	callScored callState = iota
	callPending
	callUnmeasurable
)

// measureCall scores one directional claim over the fixed horizon. It is the
// single procedure all three arms go through: the arms differ in which calls
// they make, never in how those calls are judged.
func measureCall(ctx context.Context, cache *seriesCache, r store.RunSummary,
	generatedAt string, c call, horizon int) (Entry, callState) {

	e := Entry{
		RunName:       r.Name,
		GeneratedAt:   generatedAt,
		Ticker:        c.ticker,
		Index:         c.index,
		Direction:     string(c.direction),
		Confidence:    c.confidence,
		PriceAtGen:    c.anchor,
		TimeframeDays: horizon,
	}
	s, err := cache.get(ctx, c.ticker, r.Dir)
	if err != nil || s == nil || len(s.Bars) == 0 {
		e.Err = "no price history"
		return e, callUnmeasurable
	}
	genDate := dateOf(generatedAt)
	if c.anchor <= 0 {
		// The shortlist arm's anchor comes from prescreen.json, which only
		// exists for runs generated after Stage 0.5 was built. The series
		// itself carries the same close, so a missing row would cost coverage of
		// the funnel's own record for no reason — take it from the bars instead.
		c.anchor = closeOnOrBefore(s, genDate)
		e.PriceAtGen = c.anchor
	}
	if c.anchor <= 0 {
		e.Err = "no anchor price"
		return e, callUnmeasurable
	}
	res := measureHorizon(ctx, cache, barsAfter(s, genDate), c.anchor,
		c.direction, horizon, benchmarkFor(c.index, c.ticker), genDate)
	if !res.complete {
		return e, callPending
	}
	e.CallDone = true
	e.CallPnLPct, e.CallBenchPct, e.CallExcessPct, e.CallEndDate = res.pct, res.bench, res.excess, res.endDate
	return e, callScored
}

// closeOnOrBefore is the last close at or before date — the price a call made on
// that date was made off. It is *not* the first close after it: a call made
// after Friday's close is anchored to Friday, and anchoring it to Monday would
// hand it a weekend of hindsight.
func closeOnOrBefore(s *quant.Series, date string) float64 {
	if s == nil || date == "" {
		return 0
	}
	i := sort.Search(len(s.Bars), func(i int) bool { return s.Bars[i].Date > date })
	if i == 0 {
		return 0
	}
	return s.Bars[i-1].Close
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// FormatText renders the comparison. The arms print in funnel order — composite,
// then the funnel's output, then what shipped — so the two differences a reader
// wants are adjacent to the numbers they are drawn from.
func (r *ControlReport) FormatText() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Control arms: %d run(s), %d-session horizon, every arm scored identically.\n",
		r.RunCount, r.HorizonDays)
	if len(r.Skipped) > 0 {
		fmt.Fprintf(&sb, "%d run(s) carry no prescreen.json, so the composite arm skips them.\n", len(r.Skipped))
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "  %-12s %-34s %5s  %7s  %9s  %9s  %9s\n",
		"arm", "what it is", "n", "right", "beat bench", "avg", "avg excess")
	for _, a := range r.Arms {
		rec := a.Record
		if rec.N == 0 {
			fmt.Fprintf(&sb, "  %-12s %-34s %5s  %7s  %9s  %9s  %9s   (%d still inside the window)\n",
				a.Name, a.Label, "—", "—", "—", "—", "—", a.Pending)
			continue
		}
		fmt.Fprintf(&sb, "  %-12s %-34s %5d  %6.0f%%  %8.0f%%  %+8.2f%%  %+8.2f%%\n",
			a.Name, a.Label, rec.N, rec.HitRate*100, rec.ExcessHitRate*100, rec.AvgPnL, rec.AvgExcess)
	}
	sb.WriteString("\n")
	for _, a := range r.Arms {
		if a.Duplicates > 0 || a.Pending > 0 || a.Unmeasurable > 0 {
			fmt.Fprintf(&sb, "  %-12s %d re-proposal(s) dropped, %d still inside the window, %d unmeasurable\n",
				a.Name, a.Duplicates, a.Pending, a.Unmeasurable)
		}
	}
	sb.WriteString(r.verdict())
	return sb.String()
}

// verdict states the two comparisons the arms exist to make, and refuses to
// state them when the counts cannot carry one.
//
// The refusal is the important half. Three arms of five closed calls each will
// differ by twenty percentage points on noise alone, and a reader who takes that
// as a finding has been misled by a number this file produced.
func (r *ControlReport) verdict() string {
	by := map[string]HorizonRecord{}
	for _, a := range r.Arms {
		by[a.Name] = a.Record
	}
	composite, shortlist, shipped := by["composite"], by["shortlist"], by["shipped"]

	var sb strings.Builder
	sb.WriteString("\n")
	gap := func(label string, over, under HorizonRecord, what string) {
		if over.N == 0 || under.N == 0 {
			return
		}
		d := (over.HitRate - under.HitRate) * 100
		e := over.AvgExcess - under.AvgExcess
		fmt.Fprintf(&sb, "  %-34s %+5.0f pts on hit rate, %+6.2f%% on excess   (%s)\n", label, d, e, what)
	}
	gap("Whole model stack adds:", shipped, composite, "shipped vs composite")
	gap("Specialists and Chief add:", shipped, shortlist, "shipped vs shortlist")

	if n := minN(composite, shortlist, shipped); n > 0 && n < MinArmN {
		fmt.Fprintf(&sb, "\n  Both figures are noise at these counts (smallest arm n=%d, want %d+). "+
			"They are printed so the sample can be watched filling up, not so they can be read yet.\n", n, MinArmN)
	}
	return sb.String()
}

// MinArmN is the closed-call count an arm needs before a difference between two
// arms means anything. It is not a statistical threshold so much as a floor
// under embarrassment: below it, a twenty-point gap is one trade.
const MinArmN = 30

func minN(recs ...HorizonRecord) int {
	n := 0
	for _, r := range recs {
		if r.N == 0 {
			continue
		}
		if n == 0 || r.N < n {
			n = r.N
		}
	}
	return n
}
