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
	RunName     string  `json:"run"`
	GeneratedAt string  `json:"generated_at"`
	Ticker      string  `json:"ticker"`
	Index       string  `json:"index,omitempty"`
	Direction   string  `json:"direction"`
	Confidence  int     `json:"confidence"`
	PriceAtGen  float64 `json:"price_at_generation"`

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

	// DomainScores is the signed per-domain strength recorded at generation, so
	// the scoreboard can ask which domains were right rather than only whether
	// the trade worked.
	DomainScores map[string]int `json:"domain_scores,omitempty"`

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
	Skipped   int     `json:"skipped"` // ideas without a baseline price (old runs)
	RunCount  int     `json:"run_count"`

	ByOutcome    map[Outcome]int   `json:"by_outcome,omitempty"`
	ByDirection  map[string]Bucket `json:"by_direction,omitempty"`
	ByIndex      map[string]Bucket `json:"by_index,omitempty"`
	ByConfidence map[string]Bucket `json:"by_confidence,omitempty"`
	// ByDomain is scored over the closed trades each domain *backed* — where
	// its signed score agreed with the direction taken. A domain that keeps
	// backing losers is the one to reweight.
	ByDomain map[string]Bucket `json:"by_domain,omitempty"`
}

// confidenceBuckets is the fixed display order of the confidence slices.
var confidenceBuckets = []string{"<40", "40-59", "60-79", "80+"}

func confidenceBucket(c int) string {
	switch {
	case c < 40:
		return "<40"
	case c < 60:
		return "40-59"
	case c < 80:
		return "60-79"
	default:
		return "80+"
	}
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
	dir, idx, conf, dom := accs{}, accs{}, accs{}, accs{}
	var excess float64
	var excessN int

	for _, e := range s.Entries {
		s.ByOutcome[e.Outcome]++
		// A position that never filled carries no P&L, whether because its
		// window ran out or because it has not opened yet.
		if e.Outcome.closed() || (e.Outcome == OutcomeOpen && e.EntryFilled > 0) {
			s.Scored++
		}
		if !e.Outcome.closed() {
			continue
		}
		s.Closed++
		total.add(e)
		dir.add(e.Direction, e)
		idx.add(e.Index, e)
		conf.add(confidenceBucket(e.Confidence), e)
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
}

// Build walks runsDir and marks every scorable idea to the latest close. This
// is the legacy measurement; Replay is the one that describes the trade the
// idea actually specified.
func Build(ctx context.Context, runsDir string, yc *marketdata.YahooClient) (*Summary, error) {
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
	sb.WriteString("\n")

	for _, e := range s.Entries {
		switch e.Outcome {
		case OutcomeError:
			fmt.Fprintf(&sb, "  %-21s %-8s %-4s  error: %s\n", e.RunName, e.Ticker, e.Direction, e.Err)
			continue
		case OutcomeUnfilled:
			fmt.Fprintf(&sb, "  %-21s %-8s %-4s  limit %.2f never traded within the fill window\n",
				e.RunName, e.Ticker, e.Direction, e.EntryPlanned)
			continue
		}
		if e.Outcome == OutcomeOpen && e.EntryFilled == 0 {
			fmt.Fprintf(&sb, "  %-21s %-8s %-4s  waiting on the %.2f entry (%s)\n",
				e.RunName, e.Ticker, e.Direction, e.EntryPlanned, sessionsSoFar(e.BarsHeld))
			continue
		}
		r := ""
		if e.hasR() {
			r = fmt.Sprintf(" %+5.2fR", e.RiskAdjPnL)
		}
		fmt.Fprintf(&sb, "  %-21s %-8s %-4s  %8.2f → %8.2f  %+7.2f%%%s  %s (%dd)\n",
			e.RunName, e.Ticker, e.Direction, e.EntryFilled, e.ExitPrice, e.PnLPct, r, e.Outcome, e.BarsHeld)
	}

	if s.Closed > 0 {
		sb.WriteString("\n")
		writeBuckets(&sb, "By direction", s.ByDirection, sortedKeys(s.ByDirection))
		writeBuckets(&sb, "By index", s.ByIndex, sortedKeys(s.ByIndex))
		writeBuckets(&sb, "By confidence", s.ByConfidence, confidenceBuckets)
		writeBuckets(&sb, "By domain backing the trade", s.ByDomain, sortedKeys(s.ByDomain))
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
