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
//	shipped    what ideas.json actually contains, for legacy-mode runs.
//
// Read it as a chain rather than as three numbers: shipped over composite is
// what the whole model stack adds, and shipped over shortlist is what the
// specialists and the Chief add on top of the screening they were handed.
// merit_veto selection adds three shadow arms from data/selection.json — the
// Chief's own ranking, the vetoed names and the sector-capped names
// (selection_arms.go). shipped is also split by ideas.json's own `selection`
// field (shipped-merit_veto, shipped-chief, shipped-unrecorded) alongside the
// pooled shipped arm, since a policy's edge is not the other policy's.
// Thesis mode gets its own arms (thesis_arms.go), every arm carries a
// beta-hedged excess beside the plain one (hedge.go), and the report closes
// with the pre-screen's whole-universe IC (universe_ic.go).
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
	// Overlapping counts remaining same-ticker windows that overlap even after
	// weekly deduplication. These observations are not independent samples.
	Overlapping int `json:"overlapping_calls,omitempty"`
	// ExcessCI is the week-clustered 95% bootstrap interval on Record.AvgExcess,
	// absent while the closed calls span fewer than two weeks.
	ExcessCI *ExcessCI `json:"excess_ci,omitempty"`
	// Hedged is the arm's average excess net of each call's own beta to its
	// benchmark rather than one unit of it (hedge.go). The gap between it and
	// Record.AvgExcess is the arm's market exposure, not its selection.
	Hedged *HedgedRecord `json:"beta_hedged,omitempty"`
	// NonDirectional counts thesis dossiers whose lean was absent or NONE.
	// They are not calls, and are reported so a thin lean arm can be told
	// apart from a pipeline that researched nothing.
	NonDirectional int `json:"non_directional,omitempty"`
	// independent is the deduplicated entries the record was scored over, kept
	// so two arms can be differenced on the same bets their records describe.
	independent []Entry
}

// ArmDiff is the difference in average excess between two arms, with the
// interval from resampling both arms' weeks together.
type ArmDiff struct {
	Over   string    `json:"over"`
	Under  string    `json:"under"`
	Excess float64   `json:"excess_pct"`
	CI     *ExcessCI `json:"ci,omitempty"`
}

// ControlReport is the arms plus the runs they were drawn from.
type ControlReport struct {
	HorizonDays int          `json:"horizon_days"`
	RunCount    int          `json:"run_count"`
	Arms        []ControlArm `json:"arms"`
	// Diffs are the comparisons the arms exist to make (armPairs): shipped
	// over composite and over shortlist, and the thesis leans over shortlist.
	Diffs []ArmDiff `json:"diffs,omitempty"`
	// UniverseIC is the pre-screen's whole-universe rank correlation with the
	// realised benchmark-excess return, one entry per horizon (universe_ic.go).
	UniverseIC []UniverseIC `json:"universe_ic,omitempty"`
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
	// conviction and leanStrength are what a thesis lean said about itself.
	conviction   int
	leanStrength string
}

// ControlOptions configures a control report.
type ControlOptions struct {
	// Horizon is the window every arm is scored over, in sessions.
	Horizon int
	// LeanBackfill is the historical lean CSV; empty or missing skips it.
	LeanBackfill string
	// ICHorizons are the windows the universe IC is measured over; nil means
	// UniverseICHorizons.
	ICHorizons []int
}

// Control builds and scores the arms over every run in runsDir, with the
// historical lean backfill read from its default location.
func Control(ctx context.Context, runsDir string, yc marketdata.PriceSource, horizon int) (*ControlReport, error) {
	return ControlWithOptions(ctx, runsDir, yc, ControlOptions{Horizon: horizon, LeanBackfill: DefaultLeanBackfill})
}

// ControlWithOptions builds and scores the arms over every run in runsDir.
func ControlWithOptions(ctx context.Context, runsDir string, yc marketdata.PriceSource, opt ControlOptions) (*ControlReport, error) {
	runs, err := store.ListRuns(runsDir)
	if err != nil {
		return nil, err
	}
	horizon := opt.Horizon
	if horizon <= 0 {
		horizon = DefaultControlHorizon
	}
	icHorizons := opt.ICHorizons
	if icHorizons == nil {
		icHorizons = UniverseICHorizons
	}
	rep := &ControlReport{HorizonDays: horizon}
	cache := &seriesCache{yc: yc, bySymbol: map[string]*quant.Series{}, byRun: map[string]*quant.Series{}}

	composite := ControlArm{Name: "composite", Label: "pre-screen composite, no model"}
	shortlist := ControlArm{Name: "shortlist", Label: "funnel output at the scouts' bias"}
	shipped := ControlArm{Name: "shipped", Label: "what the legacy pipeline shipped"}
	// shipped, split by the policy ideas.json recorded it under (docs/workflow/
	// scoreboard.md's former caveat: "shipped pools both selection policies").
	// An older run predating the selection field is neither policy — it is
	// labelled unrecorded rather than folded into either one.
	shippedMerit := ControlArm{Name: "shipped-" + model.SelectionMeritVeto, Label: "shipped, merit_veto policy"}
	shippedChief := ControlArm{Name: "shipped-" + model.SelectionChief, Label: "shipped, chief policy"}
	shippedUnrecorded := ControlArm{Name: "shipped-unrecorded", Label: "shipped, pre-field runs (no selection recorded)"}
	thesis := ControlArm{Name: "thesis", Label: "what thesis mode shipped"}
	thesisLean := ControlArm{Name: "thesis-lean", Label: "every thesis dossier at its lean"}
	backfill := ControlArm{Name: "thesis-lean-backfill", Label: "hand-judged leans, pre-lean runs"}
	chiefShadow := ControlArm{Name: "chief-shadow", Label: "Chief's shadow top-5, merit_veto"}
	vetoed := ControlArm{Name: "vetoed", Label: "shortlist names a model vetoed"}
	sectorCapped := ControlArm{Name: "sector-capped", Label: "shortlist names the sector cap dropped"}

	byName := map[string]store.RunSummary{}
	backfillIndex := map[string]map[string]string{}
	var icRuns []icRun
	for _, r := range runs {
		byName[r.Name] = r
		ideas, err := store.LoadIdeas(r.Dir)
		if err != nil {
			ideas = nil
		}
		meta, _ := store.LoadMeta(r.Dir)
		var ps prescreenFile
		hasPrescreen, _ := store.ReadPrescreen(r.Dir, &ps)
		backfillIndex[r.Name] = runIndexOf(meta, ps.Rows)

		generatedAt := r.GeneratedAt
		if ideas != nil && ideas.GeneratedAt != "" {
			generatedAt = ideas.GeneratedAt
		}
		if hasPrescreen && generatedAt != "" {
			icRuns = append(icRuns, icRun{summary: r, generatedAt: generatedAt, rows: ps.Rows})
		}

		score := func(arm *ControlArm, calls []call) {
			for _, c := range calls {
				e, state := measureCall(ctx, cache, r, generatedAt, c, horizon)
				if state == callUnmeasurable {
					arm.Unmeasurable++
					continue
				}
				arm.Entries = append(arm.Entries, e)
			}
		}

		// Every lean a thesis run recorded is scored whether or not the run
		// reached synthesis: a run that shipped nothing still judged every
		// name it researched.
		if isThesisRun(ideas, meta) && generatedAt != "" {
			calls, none := leanCalls(r.Dir, prescreenCloses(ps.Rows))
			thesisLean.NonDirectional += none
			score(&thesisLean, calls)
		}

		if ideas == nil {
			continue
		}
		rep.RunCount++
		if !hasPrescreen {
			rep.Skipped = append(rep.Skipped, r.Name)
		}
		if hasPrescreen {
			score(&composite, compositeCalls(ps.Rows, controlArmSize))
		}
		if meta != nil {
			score(&shortlist, shortlistCalls(meta.Shortlist, ps.Rows))
		}
		// The two modes' shipped ideas are two different pipelines' output,
		// and pooling them would let one hide inside the other's record.
		if isThesisRun(ideas, meta) {
			score(&thesis, shippedCalls(ideas.Ideas))
		} else {
			shippedThisRun := shippedCalls(ideas.Ideas)
			score(&shipped, shippedThisRun)
			switch ideas.Selection {
			case model.SelectionMeritVeto:
				score(&shippedMerit, shippedThisRun)
			case model.SelectionChief:
				score(&shippedChief, shippedThisRun)
			default:
				score(&shippedUnrecorded, shippedThisRun)
			}
			sel := readSelection(r.Dir)
			score(&chiefShadow, chiefShadowCalls(sel))
			score(&vetoed, vetoedCalls(sel))
			score(&sectorCapped, sectorCappedCalls(sel))
		}
	}

	leans, none, err := readLeanBackfill(opt.LeanBackfill)
	if err != nil {
		return nil, err
	}
	backfill.NonDirectional = none
	for _, l := range leans {
		r, generatedAt := backfillRun(byName, l)
		c := call{ticker: l.ticker, index: backfillIndex[l.run][strings.ToUpper(l.ticker)],
			direction: l.direction, leanStrength: l.strength}
		e, state := measureCall(ctx, cache, r, generatedAt, c, horizon)
		if state == callUnmeasurable {
			backfill.Unmeasurable++
			continue
		}
		backfill.Entries = append(backfill.Entries, e)
	}

	rep.Arms = []ControlArm{composite, shortlist, shipped, shippedMerit, shippedChief, shippedUnrecorded,
		thesis, thesisLean, backfill, chiefShadow, vetoed, sectorCapped}
	// Each arm is deduplicated on its own entries rather than on the union: the
	// arms are different sets of calls, and a name the shipped arm took once
	// and the shortlist arm took five times is one bet in each.
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
		rep.Arms[i].Hedged = hedgedRecord(indep)
		rep.Arms[i].independent = indep
		if ci, ok := excessCI(indep); ok {
			rep.Arms[i].ExcessCI = &ci
		}
	}
	rep.Diffs = armDiffs(rep.Arms)
	rep.UniverseIC = universeICs(ctx, cache, icRuns, icHorizons)
	return rep, nil
}

// prescreenCloses is the pre-screen's close per upper-cased ticker; the first
// row wins for a dual-listed name, whose rows carry the same close.
func prescreenCloses(rows []prescreenRow) map[string]float64 {
	closes := map[string]float64{}
	for _, r := range rows {
		if r.Close > 0 {
			k := strings.ToUpper(r.Ticker)
			if _, ok := closes[k]; !ok {
				closes[k] = r.Close
			}
		}
	}
	return closes
}

// armPairs are the differences the arms exist to measure, as (over, under):
// what the whole model stack adds, what the specialists and Chief add, and
// what thesis research adds over the names it was handed.
var armPairs = [][2]string{
	{"shipped", "composite"},
	{"shipped", "shortlist"},
	{"thesis-lean", "shortlist"},
	{"thesis-lean-backfill", "shortlist"},
	// merit_veto's shadow arms: what taking the ranking from the Chief did,
	// whether the vetoes remove losers, and whether the sector cap gives up
	// return it did not need to (selection_arms.go). The sector cap only fires
	// under merit_veto, so it is read against shipped-merit_veto rather than
	// the pooled shipped arm, which also carries runs the cap never touched.
	{"shipped", "chief-shadow"},
	{"vetoed", "shipped"},
	{"sector-capped", "shipped-merit_veto"},
}

// armDiffs differences each pair whose arms both have closed calls.
func armDiffs(arms []ControlArm) []ArmDiff {
	by := map[string]ControlArm{}
	for _, a := range arms {
		by[a.Name] = a
	}
	var out []ArmDiff
	for _, p := range armPairs {
		o, u := by[p[0]], by[p[1]]
		if o.Record.N == 0 || u.Record.N == 0 {
			continue
		}
		d := ArmDiff{Over: p[0], Under: p[1],
			Excess: round2(o.Record.AvgExcess - u.Record.AvgExcess)}
		if ci, ok := excessDiffCI(o.independent, u.independent); ok {
			d.CI = &ci
		}
		out = append(out, d)
	}
	return out
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
	closes := prescreenCloses(rows)
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
		Conviction:    c.conviction,
		LeanStrength:  c.leanStrength,
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
	benchSym := benchmarkFor(c.index, c.ticker)
	res := measureHorizon(ctx, cache, barsAfter(s, genDate), c.anchor,
		c.direction, horizon, benchSym, genDate)
	if !res.complete {
		return e, callPending
	}
	e.CallDone = true
	e.CallPnLPct, e.CallBenchPct, e.CallExcessPct, e.CallEndDate = res.pct, res.bench, res.excess, res.endDate
	if res.benchOK {
		if b, err := cache.get(ctx, benchSym, ""); err == nil {
			if beta, ok := betaBefore(s, b, genDate); ok {
				e.CallHedged, e.CallBeta = true, round2(beta)
				e.CallHedgedPct = hedgedExcess(c.direction, res.pct, res.bench, beta)
			}
		}
	}
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
	fmt.Fprintf(&sb, "  %-20s %-34s %5s  %7s  %9s  %9s  %9s  %9s  %s\n",
		"arm", "what it is", "n", "right", "beat bench", "avg", "avg excess", "β-hedged", "95% CI (weeks)")
	for _, a := range r.Arms {
		rec := a.Record
		if rec.N == 0 {
			fmt.Fprintf(&sb, "  %-20s %-34s %5s  %7s  %9s  %9s  %9s  %9s   (%d still inside the window)\n",
				a.Name, a.Label, "—", "—", "—", "—", "—", "—", a.Pending)
			continue
		}
		fmt.Fprintf(&sb, "  %-20s %-34s %5d  %6.0f%%  %8.0f%%  %+8.2f%%  %+8.2f%%  %9s  %s\n",
			a.Name, a.Label, rec.N, rec.HitRate*100, rec.ExcessHitRate*100, rec.AvgPnL, rec.AvgExcess,
			formatHedged(a.Hedged), formatCI(a.ExcessCI))
	}
	sb.WriteString("\n")
	for _, a := range r.Arms {
		if a.Duplicates > 0 || a.Pending > 0 || a.Unmeasurable > 0 || a.NonDirectional > 0 {
			fmt.Fprintf(&sb, "  %-20s %d re-proposal(s) dropped, %d still inside the window, %d unmeasurable",
				a.Name, a.Duplicates, a.Pending, a.Unmeasurable)
			if a.NonDirectional > 0 {
				fmt.Fprintf(&sb, ", %d dossier(s) with no lean", a.NonDirectional)
			}
			sb.WriteString("\n")
		}
	}
	if len(r.UniverseIC) > 0 {
		sb.WriteString("\n  Universe IC (pre-screen score vs realised excess, every ranked name, per index then averaged):\n")
		for _, u := range r.UniverseIC {
			fmt.Fprintf(&sb, "    %2d sessions: %s\n", u.HorizonDays, u.formatLine())
		}
	}
	sb.WriteString(r.verdict())
	return sb.String()
}

// formatHedged renders an arm's average beta-hedged excess and the beta behind
// it, or a dash when no call in the arm had enough history for a beta.
func formatHedged(h *HedgedRecord) string {
	if h == nil {
		return "—"
	}
	return fmt.Sprintf("%+.2f%%", h.AvgHedgedExcess)
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
	gap("Thesis research adds:", by["thesis-lean"], shortlist, "thesis-lean vs shortlist")
	gap("Merit over the Chief's ranking:", shipped, by["chief-shadow"], "shipped vs chief-shadow")
	gap("Vetoed names over what shipped:", by["vetoed"], shipped, "vetoed vs shipped")
	gap("Sector cap cost over merit_veto shipped:", by["sector-capped"], by["shipped-merit_veto"], "sector-capped vs shipped-merit_veto")
	for _, d := range r.Diffs {
		if d.CI == nil {
			continue
		}
		verdict := "indistinguishable from zero"
		if !d.CI.Contains(0) {
			verdict = "excludes zero"
		}
		fmt.Fprintf(&sb, "  excess, %s − %-10s %+6.2f%%, 95%% CI %s — %s\n",
			d.Over, d.Under+":", d.Excess, formatCI(d.CI), verdict)
	}

	if n := minN(composite, shortlist, shipped, by["thesis-lean"]); n > 0 && n < MinArmN {
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

// formatCI renders an interval, or a dash when there are too few weeks for one.
func formatCI(c *ExcessCI) string {
	if c == nil {
		return "—"
	}
	return fmt.Sprintf("[%+.2f%%, %+.2f%%] (%d)", c.Low, c.High, c.Weeks)
}
