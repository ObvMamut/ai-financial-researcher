package quant

import (
	"fmt"
	"sort"
	"strings"
)

// Pack bundles computed metrics for a run's shortlist. It renders to markdown
// for prompt injection and serializes to runs/<ts>/quant.json.
type Pack struct {
	AsOf     string             `json:"as_of"` // date of the newest bar used
	ByTicker map[string]Metrics `json:"by_ticker"`
	Errors   []string           `json:"errors,omitempty"` // fetch/compute failures per symbol
}

func NewPack() *Pack {
	return &Pack{ByTicker: map[string]Metrics{}}
}

func (p *Pack) tickers() []string {
	out := make([]string, 0, len(p.ByTicker))
	for t := range p.ByTicker {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func pct(x float64) string { return fmt.Sprintf("%+.1f%%", x*100) }

// Markdown renders the full pack for the quant specialist's prompt. The
// heading doubles as the grounding marker checked by the orchestrator.
func (p *Pack) Markdown() string {
	if len(p.ByTicker) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("### Verified Market Data\n\n")
	sb.WriteString(fmt.Sprintf("Computed in-process from Yahoo Finance daily OHLCV (adjusted closes), as of %s. ", p.AsOf))
	sb.WriteString("These numbers are ground truth — do not re-derive or contradict them.\n\n")

	for _, t := range p.tickers() {
		m := p.ByTicker[t]
		sb.WriteString(fmt.Sprintf("#### %s (close %.2f, %s, %d bars)\n", t, m.LastClose, m.AsOf, m.Bars))
		sb.WriteString(fmt.Sprintf("- Returns: 5d %s | 21d %s | 63d %s | 126d %s | 252d %s\n",
			pct(m.Ret5d), pct(m.Ret21d), pct(m.Ret63d), pct(m.Ret126d), pct(m.Ret252d)))
		sb.WriteString(fmt.Sprintf("- Momentum 12-1: %s (vol-adjusted %.2f) | price/52w-high: %.3f\n",
			pct(m.Mom12_1), m.VolAdjMom, m.PriceTo52wHigh))
		sb.WriteString(fmt.Sprintf("- Short-term reversal z: %+.2f (5d return vs its 1y distribution) | turnover ratio 5d/63d: %.2f\n",
			m.STRZScore, m.TurnoverRatio))
		sb.WriteString(fmt.Sprintf("- Realized vol (Yang-Zhang, annualized): 20d %.1f%% | 60d %.1f%% | trend %.2f\n",
			m.VolYZ20*100, m.VolYZ60*100, m.VolTrend))
		sb.WriteString(fmt.Sprintf("- Variance ratio: VR(5) %.2f | VR(10) %.2f → regime: %s\n", m.VR5, m.VR10, m.Regime))
		sb.WriteString(fmt.Sprintf("- Risk: max drawdown 126d %s | worst day 252d %s | skew %+.2f | excess kurtosis %+.2f\n",
			pct(m.MaxDrawdown126), pct(m.WorstDay252), m.Skew252, m.Kurt252))
		sb.WriteString(fmt.Sprintf("- Liquidity: 20d avg dollar volume $%.0fM\n", m.AvgDollarVol20/1e6))
		if m.Benchmark != "" {
			sb.WriteString(fmt.Sprintf("- Vs %s: beta %.2f, correlation %.2f\n", m.Benchmark, m.Beta, m.Corr))
		}
		if m.SigmaDaily > 0 {
			sb.WriteString(fmt.Sprintf("- Vol-scaled distances (σ_daily %.2f%%): 1σ over 5/10/20d = %s / %s / %s; 2σ = %s / %s / %s\n",
				m.SigmaDaily*100,
				fmt.Sprintf("%.1f%%", m.Distances["1s_h5"]*100),
				fmt.Sprintf("%.1f%%", m.Distances["1s_h10"]*100),
				fmt.Sprintf("%.1f%%", m.Distances["1s_h20"]*100),
				fmt.Sprintf("%.1f%%", m.Distances["2s_h5"]*100),
				fmt.Sprintf("%.1f%%", m.Distances["2s_h10"]*100),
				fmt.Sprintf("%.1f%%", m.Distances["2s_h20"]*100)))
		}
		if len(m.Flags) > 0 {
			sb.WriteString(fmt.Sprintf("- Caveats: %s\n", strings.Join(m.Flags, "; ")))
		}
		sb.WriteString("\n")
	}
	if len(p.Errors) > 0 {
		sb.WriteString(fmt.Sprintf("Data gaps: %s\n", strings.Join(p.Errors, "; ")))
	}
	return sb.String()
}

// CompactLine summarises one ticker for prompts that shouldn't carry the full
// pack (chief analyst, news/sentiment specialists). It renders **two** lines:
// trend on the first, risk shape and caveats on the second.
//
// It used to be one line covering trend alone. The Chief Analyst places every
// stop and target off this block, and was told to size them in σ√h — so it had
// to re-derive distances the pack had already computed, and a staleness flag on
// the very close it was pricing off never reached it. Both now travel with the
// line. Empty if the ticker is unknown.
func (p *Pack) CompactLine(ticker string) string {
	m, ok := p.ByTicker[strings.ToUpper(ticker)]
	if !ok {
		return ""
	}
	line := fmt.Sprintf("%s: close %.2f (%s), 5d %s, 21d %s, mom12-1 %s, p/52wH %.2f, vol20d %.0f%% (volTrend %.2f), VR5 %.2f (%s), σ_daily %.2f%%",
		m.Symbol, m.LastClose, m.AsOf, pct(m.Ret5d), pct(m.Ret21d), pct(m.Mom12_1),
		m.PriceTo52wHigh, m.VolYZ20*100, m.VolTrend, m.VR5, m.Regime, m.SigmaDaily*100)
	if m.Benchmark != "" {
		line += fmt.Sprintf(", beta %.2f, corr %.2f", m.Beta, m.Corr)
	}
	if m.AvgDollarVol20 > 0 {
		line += fmt.Sprintf(", ADV $%.0fM", m.AvgDollarVol20/1e6)
	}

	// Second line: the numbers a stop is actually placed with, plus anything
	// that makes the first line untrustworthy.
	var risk []string
	if d, ok := m.Distances["1s_h10"]; ok && d > 0 {
		risk = append(risk, fmt.Sprintf("1σ(10d) ±%.1f%%, 2σ ±%.1f%%", d*100, m.Distances["2s_h10"]*100))
	}
	risk = append(risk,
		fmt.Sprintf("maxDD126 %.1f%%", m.MaxDrawdown126*100),
		fmt.Sprintf("worst day %.1f%%", m.WorstDay252*100),
		fmt.Sprintf("skew %+.2f", m.Skew252),
		fmt.Sprintf("kurt %+.2f", m.Kurt252))
	if len(m.Flags) > 0 {
		risk = append(risk, "flags: "+strings.Join(m.Flags, "; "))
	}
	return line + "\n  ↳ " + strings.Join(risk, " | ")
}

// CompactBlock renders CompactLines for every ticker, sorted, one per line.
func (p *Pack) CompactBlock() string {
	if len(p.ByTicker) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, t := range p.tickers() {
		sb.WriteString("- ")
		sb.WriteString(p.CompactLine(t))
		sb.WriteByte('\n')
	}
	return sb.String()
}
