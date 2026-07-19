// Package scoreboard measures how past trade ideas performed. It walks saved
// runs, compares each idea's price-at-generation against the current price
// (direction-aware), and aggregates a win rate.
//
// Limitation, by design: target/stop hits are judged on the *current* price
// only — an idea that hit its target and then retraced shows the retraced
// value. Judging the true path would require replaying intraday history.
package scoreboard

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// Entry is one scored idea from a past run.
type Entry struct {
	RunName     string  `json:"run"`
	GeneratedAt string  `json:"generated_at"`
	Ticker      string  `json:"ticker"`
	Direction   string  `json:"direction"`
	Confidence  int     `json:"confidence"`
	PriceAtGen  float64 `json:"price_at_generation"`
	Current     float64 `json:"current_price"`
	CurrentAsOf string  `json:"current_as_of"`
	PnLPct      float64 `json:"pnl_pct"` // direction-aware, vs price_at_generation
	TargetHit   bool    `json:"target_hit"`
	StopHit     bool    `json:"stop_hit"`
	Err         string  `json:"error,omitempty"` // price fetch failure
}

// Summary aggregates all scored entries.
type Summary struct {
	Entries  []Entry `json:"entries"`
	Scored   int     `json:"scored"`
	Wins     int     `json:"wins"`
	Losses   int     `json:"losses"`
	WinRate  float64 `json:"win_rate"` // wins / scored
	AvgPnL   float64 `json:"avg_pnl_pct"`
	Skipped  int     `json:"skipped"` // ideas without price_at_generation (old runs)
	RunCount int     `json:"run_count"`
}

// Build walks runsDir and prices every scorable idea. Prices come through the
// shared daily-keyed cache, so repeated calls are cheap.
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
	var sb strings.Builder
	fmt.Fprintf(&sb, "Scoreboard: %d idea(s) scored across %d run(s)", s.Scored, s.RunCount)
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
