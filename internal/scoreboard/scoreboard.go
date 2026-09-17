// Package scoreboard measures how past trade ideas performed.
//
// The default measurement is a *path replay* (see replay.go): each idea is
// simulated forward through its own daily bars — does the limit entry fill,
// which barrier is touched first, where does the position end. Win rate is
// computed over closed trades only.
//
// Build is the original measurement, kept for `cfr scoreboard --legacy`: it
// compares price-at-generation against *today's* price and calls anything
// positive a win. That answers a question nobody asked — an idea that reached
// its target and retraced scored as a loss, an entry that never filled scored
// as a flat trade — and is retained only so the two numbers can be compared on
// the same history.
package scoreboard

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// Entry is one scored idea from a past run. The replay fields are empty in
// legacy mode and the legacy fields are empty in replay mode; Summary.Replay
// says which set to read.
type Entry struct {
	ResearchMode string  `json:"research_mode,omitempty"`
	PlanStatus   string  `json:"plan_status,omitempty"`
	RunName      string  `json:"run"`
	GeneratedAt  string  `json:"generated_at"`
	Ticker       string  `json:"ticker"`
	Index        string  `json:"index,omitempty"`
	Direction    string  `json:"direction"`
	Confidence   int     `json:"confidence"`
	PriceAtGen   float64 `json:"price_at_generation"`

	// Legacy fields: the idea marked to the latest close.
	Current     float64 `json:"current_price,omitempty"`
	CurrentAsOf string  `json:"current_as_of,omitempty"`
	TargetHit   bool    `json:"target_hit,omitempty"`
	StopHit     bool    `json:"stop_hit,omitempty"`

	// Replay fields: the trade as it would actually have been taken.
	Outcome       Outcome `json:"outcome,omitempty"`
	Stop          float64 `json:"stop,omitempty"`
	Target        float64 `json:"target,omitempty"`
	TimeframeDays int     `json:"timeframe_days,omitempty"`
	EntryPlanned  float64 `json:"entry_planned,omitempty"`
	EntryFilled   float64 `json:"entry_filled,omitempty"`
	EntryDate     string  `json:"entry_date,omitempty"`
	ExitPrice     float64 `json:"exit_price,omitempty"`
	ExitDate      string  `json:"exit_date,omitempty"`
	// BarsHeld is sessions elapsed in the simulation: the holding period for a
	// filled position, and the sessions since generation for one still waiting
	// on its entry.
	BarsHeld int `json:"bars_held,omitempty"`

	// PnLPct is direction-aware, measured from the fill (replay) or from
	// price-at-generation (legacy). RiskAdjPnL restates it in R — multiples of
	// the distance to the stop — so a 3% win on a 1% stop and a 3% win on a 6%
	// stop stop being the same number.
	PnLPct          float64 `json:"pnl_pct"`
	RiskAdjPnL      float64 `json:"risk_adj_pnl,omitempty"`
	BenchmarkPnLPct float64 `json:"benchmark_pnl_pct,omitempty"`
	ExcessPnLPct    float64 `json:"excess_pnl_pct,omitempty"`

	// The horizon measurement (horizon.go): did the name move the way this idea
	// said it would, over the idea's own holding period? It is independent of
	// the barrier outcome above — an idea can be stopped out and still have been
	// directionally right, and can expire untouched having been wrong.
	//
	// Call* is anchored at the generation close, so it scores the directional
	// claim with the entry-limit lottery removed and exists for every idea
	// including the ones that never filled. Trade* is anchored at the fill and
	// exists only for ideas that filled.
	//
	// The Done flags say the window has actually elapsed. A recent idea has
	// neither, and reading a zero there as a flat result is the mistake this
	// pair of booleans exists to prevent.
	CallDone       bool    `json:"call_done,omitempty"`
	CallPnLPct     float64 `json:"call_pnl_pct,omitempty"`
	CallBenchPct   float64 `json:"call_bench_pct,omitempty"`
	CallExcessPct  float64 `json:"call_excess_pct,omitempty"`
	CallEndDate    string  `json:"call_end_date,omitempty"`
	TradeDone      bool    `json:"trade_done,omitempty"`
	TradePnLPct    float64 `json:"trade_pnl_pct,omitempty"`
	TradeExcessPct float64 `json:"trade_excess_pct,omitempty"`

	// DomainScores is the signed per-domain strength recorded at generation, so
	// the scoreboard can ask which domains were right rather than only whether
	// the trade worked.
	DomainScores map[string]int `json:"domain_scores,omitempty"`
	// The rest of what the idea recorded about itself at generation, carried
	// through so a post-mortem can ask *why* a trade worked rather than only
	// whether it did. None of it is used by the win rate.
	//
	// Why is the Chief's own stated thesis — the single most useful field in a
	// post-mortem and the one thing no amount of replay arithmetic can
	// reconstruct. BaseConfidence and Consensus are the computed score and the
	// agreement behind it. Sector is joined from the producing run's shortlist,
	// which is where sector actually lives.
	Why            string  `json:"why,omitempty"`
	PositionNote   string  `json:"position_note,omitempty"`
	BaseConfidence int     `json:"base_confidence,omitempty"`
	Consensus      float64 `json:"consensus,omitempty"`
	Sector         string  `json:"sector,omitempty"`
	ExpectancyR    float64 `json:"expectancy_r,omitempty"`
	BreakevenWin   float64 `json:"breakeven_win_rate,omitempty"`
	// PersonaSet identifies the exact prompt set the producing run used, so an
	// A/B between two persona directories can be settled on closed trades
	// instead of on how the reports read. Empty for runs that recorded none.
	PersonaSet string `json:"persona_set,omitempty"`

	Err string `json:"error,omitempty"`
}

// hasR reports whether this entry's R-multiple is meaningful.
func (e Entry) hasR() bool { return e.Stop > 0 && e.EntryFilled > 0 }

// backedBy reports whether a domain called this trade's direction. Domain
// scores are signed bullish-positive, so agreement is a matching sign.
func (e Entry) backedBy(score int) bool {
	if score == 0 {
		return false
	}
	if e.Direction == string(model.DirectionSell) {
		return score < 0
	}
	return score > 0
}

// Bucket is the closed-trade record of one slice of the history.
type Bucket struct {
	N       int     `json:"n"`
	Wins    int     `json:"wins"`
	Losses  int     `json:"losses"`
	WinRate float64 `json:"win_rate"`
	AvgPnL  float64 `json:"avg_pnl_pct"`
	AvgR    float64 `json:"avg_r"`
}

// Summary aggregates all scored entries.
type Summary struct {
	Entries []Entry `json:"entries"`
	// Replay is true when the entries came from the path simulation rather than
	// the legacy mark-to-market.
	Replay bool `json:"replay"`

	// Scored counts entries carrying a P&L: in replay mode the positions that
	// actually filled (closed or still open), in legacy mode every priced idea.
	Scored int `json:"scored"`
	// Closed counts finished trades — the win-rate denominator. An idea still
	// running, or one whose limit never traded, is not a loss, and counting it
	// as one is what produced a 32% win rate from a history a third of whose
	// rows were 0.00%.
	Closed    int     `json:"closed,omitempty"`
	Wins      int     `json:"wins"`
	Losses    int     `json:"losses"`
	WinRate   float64 `json:"win_rate"`
	AvgPnL    float64 `json:"avg_pnl_pct"`
	AvgR      float64 `json:"avg_r,omitempty"`
	AvgExcess float64 `json:"avg_excess_pnl_pct,omitempty"`

	// The horizon record: how often the direction was right over the idea's own
	// holding period, which is the question this pipeline exists to answer. It
	// is computed over every idea whose window has elapsed — filled or not,
	// stopped or not — so its denominator is larger than Closed and its answer
	// is about the call rather than about the trade.
	Horizon      HorizonRecord `json:"horizon,omitempty"`
	HorizonTrade HorizonRecord `json:"horizon_trade,omitempty"`
	Skipped      int           `json:"skipped"` // ideas without a baseline price (old runs)
	RunCount     int           `json:"run_count"`
	// Duplicates is how many ideas were re-proposals of a call already counted —
	// the same ticker in the same direction inside DefaultDedupeWindowDays. They
	// are still listed in the rows below but excluded from every cell, because
	// five runs proposing one short in one afternoon are one bet and not five.
	// See dedupe.go.
	Duplicates int `json:"duplicates,omitempty"`

	ByOutcome    map[Outcome]int   `json:"by_outcome,omitempty"`
	ByDirection  map[string]Bucket `json:"by_direction,omitempty"`
	ByIndex      map[string]Bucket `json:"by_index,omitempty"`
	ByConfidence map[string]Bucket `json:"by_confidence,omitempty"`
	// ByDomain is scored over the closed trades each domain *backed* — where
	// its signed score agreed with the direction taken. A domain that keeps
	// backing losers is the one to reweight.
	ByDomain map[string]Bucket `json:"by_domain,omitempty"`
	// ByPersona is the same record split by the prompt set that produced the
	// ideas — the A/B arms.
	ByPersona map[string]Bucket `json:"by_persona,omitempty"`
}

// HorizonRecord is the directional record over completed holding periods.
// Hits counts the windows that moved the way the idea said; N counts the windows
// that have elapsed at all.
type HorizonRecord struct {
	N         int     `json:"n"`
	Hits      int     `json:"hits"`
	HitRate   float64 `json:"hit_rate"`
	AvgPnL    float64 `json:"avg_pnl_pct"`
	AvgExcess float64 `json:"avg_excess_pct"`
	// ExcessHits counts the windows that beat their own benchmark. A long that
	// rose 2% in a market that rose 4% is a hit on HitRate and a miss here, and
	// the gap between the two figures is how much of the record is the market.
	ExcessHits    int     `json:"excess_hits"`
	ExcessHitRate float64 `json:"excess_hit_rate"`
}

// horizonAcc accumulates one horizon record before it is averaged.
type horizonAcc struct {
	n, hits, excessHits int
	pnl, excess         float64
}

func (a *horizonAcc) add(pct, excess float64) {
	a.n++
	if pct > 0 {
		a.hits++
	}
	if excess > 0 {
		a.excessHits++
	}
	a.pnl += pct
	a.excess += excess
}

func (a *horizonAcc) record() HorizonRecord {
	r := HorizonRecord{N: a.n, Hits: a.hits, ExcessHits: a.excessHits}
	if a.n > 0 {
		r.HitRate = float64(a.hits) / float64(a.n)
		r.ExcessHitRate = float64(a.excessHits) / float64(a.n)
		r.AvgPnL = round2(a.pnl / float64(a.n))
		r.AvgExcess = round2(a.excess / float64(a.n))
	}
	return r
}

// MinClosedPerArm is the number of closed trades an A/B arm needs before its
// number is worth reading. Below it the comparison is still shown — watching it
// fill up is the point — but labelled as what it is.
const MinClosedPerArm = 15

// confidenceBuckets is the fixed display order of the confidence slices.
//
// The boundaries follow the scale internal/orchestrator/basescore.go produces —
// a weighted vote across five domains scored against the strongest joint verdict
// their rubrics permit. The old `<40 / 40-59 / 60-79 / 80+` split was set when
// the Chief Analyst asserted its own confidence and returned 70–88; once
// base-score anchoring went live nothing shipped above 45, so every idea landed
// in `<40` and the whole slice collapsed to one cell carrying no information.
//
// `legacy` holds the ideas that cannot be placed on this scale at all — those
// generated before the run recorded per-domain scores, whose stated confidence
// came from a model rather than from arithmetic. Bucketing them by number would
// be comparing two different measurements.
var confidenceBuckets = []string{"<25", "25-39", "40-54", "55+", "legacy", "thesis (unscored)"}

const legacyConfidenceBucket = "legacy"

// comparableConfidenceBuckets is confidenceBuckets without the legacy slice —
// the buckets a live run's ideas can actually land in.
func comparableConfidenceBuckets() []string {
	out := make([]string, 0, len(confidenceBuckets))
	for _, b := range confidenceBuckets {
		if b != legacyConfidenceBucket && b != "thesis (unscored)" {
			out = append(out, b)
		}
	}
	return out
}

func confidenceBucket(c int) string {
	switch {
	case c < 25:
		return "<25"
	case c < 40:
		return "25-39"
	case c < 55:
		return "40-54"
	default:
		return "55+"
	}
}

// comparableConfidence re-reads a closed idea's confidence on the current scale.
//
// An idea's stored Confidence is on whichever scale its run used — this codebase
// has had three — but its DomainScores are the raw domain evidence and can be
// re-scored at any time. Recomputing is what lets a track record span the
// changes rather than restarting at every one.
//
// The weights are today's defaults, not the producing run's. That is the honest
// approximation: the run's own weights are not carried on the idea, and the
// defaults have not moved since per-domain scores were first recorded.
func comparableConfidence(e Entry) (int, bool) {
	return model.ScaledConfidence(model.DefaultDomainWeights(), e.DomainScores)
}

// bucketAcc accumulates one slice before it is averaged.
type bucketAcc struct {
	n, wins, losses int
	pnl, r          float64
	rN              int
}

func (a *bucketAcc) add(e Entry) {
	a.n++
	switch {
	case e.PnLPct > 0:
		a.wins++
	case e.PnLPct < 0:
		a.losses++
	}
	a.pnl += e.PnLPct
	if e.hasR() {
		a.r += e.RiskAdjPnL
		a.rN++
	}
}

func (a *bucketAcc) bucket() Bucket {
	b := Bucket{N: a.n, Wins: a.wins, Losses: a.losses}
	if a.n > 0 {
		b.WinRate = float64(a.wins) / float64(a.n)
		b.AvgPnL = round2(a.pnl / float64(a.n))
	}
	if a.rN > 0 {
		b.AvgR = round2(a.r / float64(a.rN))
	}
	return b
}

// accs is a lazily-populated set of slices.
type accs map[string]*bucketAcc

func (m accs) add(key string, e Entry) {
	if key == "" {
		return
	}
	a, ok := m[key]
	if !ok {
		a = &bucketAcc{}
		m[key] = a
	}
	a.add(e)
}

func (m accs) buckets() map[string]Bucket {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]Bucket, len(m))
	for k, a := range m {
		out[k] = a.bucket()
	}
	return out
}

// aggregate computes every headline number and slice from s.Entries. It is the
// only place a win is defined, so the replay and any future measurement cannot
// drift apart on what counts as one.
func (s *Summary) aggregate() {
	s.ByOutcome = map[Outcome]int{}
	total := &bucketAcc{}
	dir, idx, conf, dom, per := accs{}, accs{}, accs{}, accs{}, accs{}
	var excess float64
	var excessN int
	hCall, hTrade := &horizonAcc{}, &horizonAcc{}

	// The outcome tally is a census of what happened to every idea, so it counts
	// all of them. Everything after it is the *record*, and a record counts bets
	// rather than tickets — see dedupe.go.
	for _, e := range s.Entries {
		s.ByOutcome[e.Outcome]++
		// A position that never filled carries no P&L, whether because its
		// window ran out or because it has not opened yet.
		if e.Outcome.closed() || (e.Outcome == OutcomeOpen && e.EntryFilled > 0) {
			s.Scored++
		}
	}

	independent, duplicates := Dedupe(s.Entries, DefaultDedupeWindowDays)
	s.Duplicates = duplicates

	for _, e := range independent {
		// The horizon record is accumulated before the closed-trade filter
		// below, on purpose: an idea whose limit never traded still made a
		// directional claim, and excluding it would score the pipeline only on
		// the calls that happened to get a fill.
		if e.CallDone {
			hCall.add(e.CallPnLPct, e.CallExcessPct)
		}
		if e.TradeDone {
			hTrade.add(e.TradePnLPct, e.TradeExcessPct)
		}
		if !e.Outcome.closed() {
			continue
		}
		s.Closed++
		total.add(e)
		dir.add(e.Direction, e)
		idx.add(e.Index, e)
		if e.ResearchMode == "thesis" {
			conf.add("thesis (unscored)", e)
		} else if c, ok := comparableConfidence(e); ok {
			conf.add(confidenceBucket(c), e)
		} else {
			conf.add(legacyConfidenceBucket, e)
		}
		per.add(e.PersonaSet, e)
		for d, sc := range e.DomainScores {
			if e.backedBy(sc) {
				dom.add(d, e)
			}
		}
		if e.BenchmarkPnLPct != 0 || e.ExcessPnLPct != 0 {
			excess += e.ExcessPnLPct
			excessN++
		}
	}

	b := total.bucket()
	s.Wins, s.Losses, s.WinRate, s.AvgPnL, s.AvgR = b.Wins, b.Losses, b.WinRate, b.AvgPnL, b.AvgR
	if excessN > 0 {
		s.AvgExcess = round2(excess / float64(excessN))
	}
	s.ByDirection, s.ByIndex, s.ByConfidence, s.ByDomain = dir.buckets(), idx.buckets(), conf.buckets(), dom.buckets()
	s.ByPersona = per.buckets()
	s.Horizon, s.HorizonTrade = hCall.record(), hTrade.record()
}

// Build walks runsDir and marks every scorable idea to the latest close. This
// is the legacy measurement; Replay is the one that describes the trade the
// idea actually specified.
func Build(ctx context.Context, runsDir string, yc marketdata.PriceSource) (*Summary, error) {
	runs, err := store.ListRuns(runsDir)
	if err != nil {
		return nil, err
	}

	sum := &Summary{}
	var pnlTotal float64

	for _, r := range runs {
		ideas, err := store.LoadIdeas(r.Dir)
		if err != nil || ideas == nil {
			continue
		}
		counted := false
		for _, idea := range ideas.Ideas {
			if idea.PriceAtGeneration <= 0 {
				sum.Skipped++
				continue
			}
			counted = true
			e := Entry{
				RunName:     r.Name,
				GeneratedAt: ideas.GeneratedAt,
				Ticker:      idea.Ticker,
				Index:       idea.Index,
				Direction:   string(idea.Direction),
				Confidence:  idea.Confidence,
				PriceAtGen:  idea.PriceAtGeneration,
			}

			cur, asOf, err := yc.LastClose(ctx, idea.Ticker)
			if err != nil || cur <= 0 {
				if err != nil {
					e.Err = err.Error()
				} else {
					e.Err = "no current price"
				}
				sum.Entries = append(sum.Entries, e)
				continue
			}
			e.Current = cur
			e.CurrentAsOf = asOf
			e.PnLPct = pnl(idea.Direction, idea.PriceAtGeneration, cur)

			if idea.Target > 0 && idea.Stop > 0 {
				if idea.Direction == model.DirectionBuy {
					e.TargetHit = cur >= idea.Target
					e.StopHit = cur <= idea.Stop
				} else {
					e.TargetHit = cur <= idea.Target
					e.StopHit = cur >= idea.Stop
				}
			}

			sum.Scored++
			pnlTotal += e.PnLPct
			if e.PnLPct > 0 {
				sum.Wins++
			} else if e.PnLPct < 0 {
				sum.Losses++
			}
			sum.Entries = append(sum.Entries, e)
		}
		if counted {
			sum.RunCount++
		}
	}

	if sum.Scored > 0 {
		sum.WinRate = float64(sum.Wins) / float64(sum.Scored)
		sum.AvgPnL = pnlTotal / float64(sum.Scored)
	}
	return sum, nil
}

// pnl is the direction-aware return in percent.
func pnl(dir model.Direction, entry, current float64) float64 {
	if entry <= 0 {
		return 0
	}
	r := (current - entry) / entry * 100
	if dir == model.DirectionSell {
		r = -r
	}
	r = math.Round(r*100) / 100
	if r == 0 {
		return 0 // normalize -0 so flat SELL ideas don't render "-0.00%"
	}
	return r
}

// FormatText renders a plain-text scoreboard (headless `cfr scoreboard`).
func (s *Summary) FormatText() string {
	if s.Replay {
		return s.formatReplay()
	}
	return s.formatLegacy()
}

func (s *Summary) formatReplay() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Scoreboard (path replay): %d idea(s) across %d run(s)", len(s.Entries), s.RunCount)
	if s.Skipped > 0 {
		fmt.Fprintf(&sb, " (%d skipped: no baseline price)", s.Skipped)
	}
	sb.WriteString("\n")
	if s.Duplicates > 0 {
		fmt.Fprintf(&sb, "%d re-proposal(s) of a call already counted are listed below but excluded from every cell.\n", s.Duplicates)
	}
	if s.Closed > 0 {
		fmt.Fprintf(&sb, "Closed %d · win rate %.0f%% (%dW/%dL) · avg P&L %+.2f%% · avg R %+.2f",
			s.Closed, s.WinRate*100, s.Wins, s.Losses, s.AvgPnL, s.AvgR)
		if s.AvgExcess != 0 {
			fmt.Fprintf(&sb, " · avg excess %+.2f%%", s.AvgExcess)
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("No closed trades yet — the win rate is computed over closed trades only.\n")
	}
	if line := outcomeLine(s.ByOutcome); line != "" {
		fmt.Fprintf(&sb, "Outcomes: %s\n", line)
	}
	sb.WriteString(horizonSection(s.Horizon, s.HorizonTrade))
	sb.WriteString("\n")

	for _, e := range s.Entries {
		switch e.Outcome {
		case OutcomeError:
			fmt.Fprintf(&sb, "  %-21s %-8s %-4s  error: %s\n", e.RunName, e.Ticker, e.Direction, e.Err)
			continue
		case OutcomeUnfilled:
			fmt.Fprintf(&sb, "  %-21s %-8s %-4s  limit %.2f never traded within the fill window%s\n",
				e.RunName, e.Ticker, e.Direction, e.EntryPlanned, callTag(e))
			continue
		}
		if e.Outcome == OutcomeOpen && e.EntryFilled == 0 {
			fmt.Fprintf(&sb, "  %-21s %-8s %-4s  waiting on the %.2f entry (%s)%s\n",
				e.RunName, e.Ticker, e.Direction, e.EntryPlanned, sessionsSoFar(e.BarsHeld), callTag(e))
			continue
		}
		r := ""
		if e.hasR() {
			r = fmt.Sprintf(" %+5.2fR", e.RiskAdjPnL)
		}
		fmt.Fprintf(&sb, "  %-21s %-8s %-4s  %8.2f → %8.2f  %+7.2f%%%s  %s (%dd)%s\n",
			e.RunName, e.Ticker, e.Direction, e.EntryFilled, e.ExitPrice, e.PnLPct, r, e.Outcome, e.BarsHeld, callTag(e))
	}

	if s.Closed > 0 {
		sb.WriteString("\n")
		writeBuckets(&sb, "By direction", s.ByDirection, sortedKeys(s.ByDirection))
		writeBuckets(&sb, "By index", s.ByIndex, sortedKeys(s.ByIndex))
		writeBuckets(&sb, "By confidence", s.ByConfidence, confidenceBuckets)
		writeBuckets(&sb, "By domain backing the trade", s.ByDomain, sortedKeys(s.ByDomain))
		sb.WriteString(personaComparison(s.ByPersona))
	}
	// The attribution goes last because it is the part that asks *which* calls
	// worked rather than how many. It is printed even with no closed trades, for
	// the fill record alone: an idea whose limit never trades is invisible in
	// every line above, and for most of this pipeline's history that was most of
	// them.
	sb.WriteString(attributionSection(Attribute(s)))
	return sb.String()
}

// attributionSection renders the per-cell record beneath the headline slices.
func attributionSection(a *Attribution) string {
	lines := a.Lines(MinCellN)
	if len(lines) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n  Attribution (cells with at least %d closed trades):\n", MinCellN)
	for _, l := range lines {
		sb.WriteString("    " + l + "\n")
	}
	return sb.String()
}

// personaComparison renders the A/B arms. Each line carries its own n and says
// when that n is too small to conclude from, because the failure mode of an A/B
// is not a wrong number — it is a right number read too early.
func personaComparison(m map[string]Bucket) string {
	if len(m) < 2 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n  Persona A/B (closed ideas per arm):\n")
	for _, k := range sortedKeys(m) {
		b := m[k]
		if b.N == 0 {
			continue
		}
		note := ""
		if b.N < MinClosedPerArm {
			note = fmt.Sprintf("   ← too thin (%d of %d closed ideas)", b.N, MinClosedPerArm)
		}
		fmt.Fprintf(&sb, "    %-24s n=%-3d win %3.0f%%  avg %+.2fR  avg %+.2f%%%s\n",
			k, b.N, b.WinRate*100, b.AvgR, b.AvgPnL, note)
	}
	return sb.String()
}

// sessionsSoFar phrases how much of a young idea's history exists yet. "0
// sessions" is the honest reading of an idea generated after Friday's close.
func sessionsSoFar(n int) string {
	if n == 1 {
		return "1 session so far"
	}
	return fmt.Sprintf("%d sessions so far", n)
}

// callTag renders one idea's directional verdict for the end of its row. It is
// deliberately attached to the unfilled and still-waiting rows too: those are
// exactly the ideas the barrier replay has nothing to say about, and they made a
// directional claim like any other.
func callTag(e Entry) string {
	if !e.CallDone {
		return ""
	}
	verdict := "wrong"
	if e.CallPnLPct > 0 {
		verdict = "right"
	}
	return fmt.Sprintf("  [call %s %+.2f%%]", verdict, e.CallPnLPct)
}

// horizonSection renders the directional record over completed holding periods.
//
// It is printed directly under the barrier outcomes because the two answer
// different questions about the same ideas and the difference between them is
// the point: the barrier line says what the trade did, this one says whether the
// call was right. `beat bench` is the same count net of the name's own index,
// and where it sits well under `right` the record is mostly the market.
func horizonSection(call, trade HorizonRecord) string {
	if call.N == 0 && trade.N == 0 {
		return "Horizon: no idea has completed its holding period yet.\n"
	}
	var sb strings.Builder
	line := func(label string, r HorizonRecord) {
		if r.N == 0 {
			return
		}
		fmt.Fprintf(&sb, "%-24s n=%-3d right %3.0f%% (%d/%d) · beat bench %3.0f%% · avg %+.2f%% · avg excess %+.2f%%\n",
			label, r.N, r.HitRate*100, r.Hits, r.N, r.ExcessHitRate*100, r.AvgPnL, r.AvgExcess)
	}
	line("Horizon (the call):", call)
	line("Horizon (the trade):", trade)
	return sb.String()
}

// outcomeOrder keeps the outcome tally readable: results first, non-results last.
var outcomeOrder = []Outcome{OutcomeTarget, OutcomeStop, OutcomeExpired, OutcomeOpen, OutcomeUnfilled, OutcomeError}

func outcomeLine(m map[Outcome]int) string {
	var parts []string
	for _, o := range outcomeOrder {
		if n := m[o]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", o, n))
		}
	}
	return strings.Join(parts, " · ")
}

func writeBuckets(sb *strings.Builder, label string, m map[string]Bucket, order []string) {
	if len(m) == 0 {
		return
	}
	var parts []string
	for _, k := range order {
		b, ok := m[k]
		if !ok || b.N == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d·%.0f%%·%+.2f%%", k, b.N, b.WinRate*100, b.AvgPnL))
	}
	if len(parts) == 0 {
		return
	}
	fmt.Fprintf(sb, "  %-28s %s\n", label+":", strings.Join(parts, "   "))
}

func sortedKeys(m map[string]Bucket) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *Summary) formatLegacy() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Scoreboard (legacy mark-to-market): %d idea(s) scored across %d run(s)", s.Scored, s.RunCount)
	if s.Skipped > 0 {
		fmt.Fprintf(&sb, " (%d skipped: no baseline price)", s.Skipped)
	}
	sb.WriteString("\n")
	if s.Scored > 0 {
		fmt.Fprintf(&sb, "Win rate %.0f%% (%dW/%dL) · avg P&L %+.2f%%\n", s.WinRate*100, s.Wins, s.Losses, s.AvgPnL)
	}
	sb.WriteString("\n")
	for _, e := range s.Entries {
		if e.Err != "" {
			fmt.Fprintf(&sb, "  %-21s %-8s %-4s  baseline %.2f  (price unavailable: %s)\n",
				e.RunName, e.Ticker, e.Direction, e.PriceAtGen, e.Err)
			continue
		}
		marks := ""
		if e.TargetHit {
			marks = " [target]"
		}
		if e.StopHit {
			marks += " [stop]"
		}
		fmt.Fprintf(&sb, "  %-21s %-8s %-4s  %.2f → %.2f  %+7.2f%%%s\n",
			e.RunName, e.Ticker, e.Direction, e.PriceAtGen, e.Current, e.PnLPct, marks)
	}
	return sb.String()
}
