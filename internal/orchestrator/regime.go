package orchestrator

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// Regime windows, in sessions: the trend is read over a quarter, the volatility
// over a month, and "normal" volatility is the median of that monthly reading
// over the last year.
const (
	regimeTrendDays = 63
	regimeVolDays   = 21
	regimeVolYear   = 252
)

// computedRegimeLines replaces the macro specialist under merit_veto selection:
// one line per benchmark the run already fetched, computed from its own bars.
//
// Macro carried zero weight and measured no information coefficient, and a
// regime is one fact per market rather than a fact about each name. What the
// Chief needs from it — is the market this book sits in rising or falling, calm
// or stressed — is two numbers per benchmark, and Go has the series.
func computedRegimeLines(p *quant.Pack, series map[string]*quant.Series) []string {
	if p == nil || len(p.Benchmarks) == 0 {
		return nil
	}
	syms := make([]string, 0, len(p.Benchmarks))
	for s := range p.Benchmarks {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	var out []string
	for _, sym := range syms {
		m := p.Benchmarks[sym]
		trend := "up"
		if m.Ret63d < 0 {
			trend = "down"
		}
		line := fmt.Sprintf("%s: %d-session return %+.1f%% (%s)", sym, regimeTrendDays, m.Ret63d*100, trend)
		if now, median, ok := realizedVolVsYear(series[strings.ToUpper(sym)]); ok {
			state := "calm"
			if now > median {
				state = "stressed"
			}
			line += fmt.Sprintf(" · %d-session realized vol %.1f%% vs 1y median %.1f%% (%s)",
				regimeVolDays, now*100, median*100, state)
		}
		if m.AsOf != "" {
			line += " · as of " + m.AsOf
		}
		out = append(out, line)
	}
	return out
}

// regimeBlock renders the computed lines for the Chief's prompt.
func regimeBlock(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("### Computed market regime (replaces the macro specialist)\n\n")
	sb.WriteString("One line per benchmark, computed in Go from the same daily bars as everything else. Context only: it moves no name.\n\n")
	for _, l := range lines {
		sb.WriteString("- ")
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	return sb.String()
}

// realizedVolVsYear is the annualised realized volatility of the last
// regimeVolDays log returns and the median of that same rolling figure over
// the last regimeVolYear sessions.
func realizedVolVsYear(s *quant.Series) (now, median float64, ok bool) {
	if s == nil {
		return 0, 0, false
	}
	rets := s.LogReturns()
	if len(rets) < regimeVolDays+1 {
		return 0, 0, false
	}
	start := len(rets) - regimeVolYear
	if start < regimeVolDays {
		start = regimeVolDays
	}
	var rolling []float64
	for end := start; end <= len(rets); end++ {
		rolling = append(rolling, annualisedStdev(rets[end-regimeVolDays:end]))
	}
	now = rolling[len(rolling)-1]
	sorted := append([]float64(nil), rolling...)
	sort.Float64s(sorted)
	if n := len(sorted); n%2 == 1 {
		median = sorted[n/2]
	} else {
		median = (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return now, median, true
}

func annualisedStdev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var mean float64
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	var ss float64
	for _, x := range xs {
		ss += (x - mean) * (x - mean)
	}
	return math.Sqrt(ss/float64(len(xs)-1)) * math.Sqrt(252)
}
