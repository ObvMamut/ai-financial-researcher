package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/parse"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"

	"github.com/NimbleMarkets/ntcharts/linechart/timeserieslinechart"
)

// chartWindows are the selectable lookback windows (trading days).
var chartWindows = []int{90, 180, 375}

// detailModel renders one trade idea in depth: levels, price chart with
// entry/stop/target overlays, per-domain specialist scores, and rationale.
type detailModel struct {
	runDir string
	ideas  []model.TradeIdea
	idx    int // which idea is shown

	series    *quant.Series                // nil when the run has no saved prices
	scores    map[string]model.DomainScore // domain → score for this ticker
	windowIdx int

	width  int
	height int
}

func newDetailModel(runDir string, ideas []model.TradeIdea, idx, width, height int) detailModel {
	d := detailModel{runDir: runDir, ideas: ideas, idx: idx, width: width, height: height, windowIdx: 1}
	d.loadIdea()
	return d
}

// idea is the one on screen. The bounds check is not currently reachable — the
// results screen refuses to open a detail view on an empty list — but a run
// shipping zero ideas is a documented outcome of the risk gate, and the guard
// that makes this safe lives in another file. A panic here takes the user's
// terminal with it.
func (d *detailModel) idea() model.TradeIdea {
	if d.idx < 0 || d.idx >= len(d.ideas) {
		return model.TradeIdea{}
	}
	return d.ideas[d.idx]
}

// loadIdea pulls the saved price series and specialist scores for the current
// idea from the run directory. Both are optional — old runs degrade to text.
func (d *detailModel) loadIdea() {
	d.series = nil
	d.scores = map[string]model.DomainScore{}
	idea := d.idea()

	var s quant.Series
	if ok, err := store.ReadPrices(d.runDir, idea.Ticker, &s); err == nil && ok && len(s.Bars) > 0 {
		d.series = &s
	}

	for _, name := range store.ListReports(d.runDir) {
		content := store.ReadReportFile(d.runDir, name)
		raw, ok := parse.LastJSONBlock(content)
		if !ok {
			continue
		}
		var sr model.SpecialistResult
		if json.Unmarshal([]byte(raw), &sr) != nil || sr.Domain == "" {
			continue
		}
		domain := strings.ToLower(sr.Domain)
		if domain == "technicals" {
			domain = "quant" // legacy runs
		}
		for _, sc := range sr.Scores {
			if strings.EqualFold(sc.Ticker, idea.Ticker) {
				d.scores[domain] = sc
				break
			}
		}
	}
}

func (d *detailModel) next()        { d.idx = (d.idx + 1) % len(d.ideas); d.loadIdea() }
func (d *detailModel) prev()        { d.idx = (d.idx - 1 + len(d.ideas)) % len(d.ideas); d.loadIdea() }
func (d *detailModel) cycleWindow() { d.windowIdx = (d.windowIdx + 1) % len(chartWindows) }

func (d *detailModel) View() string {
	idea := d.idea()
	var sb strings.Builder

	dir := buyStyle.Render(" BUY  ")
	if idea.Direction == model.DirectionSell {
		dir = sellStyle.Render(" SELL ")
	}
	sb.WriteString(titleStyle.Render(fmt.Sprintf("#%d %s — %s", idea.Rank, idea.Ticker, idea.Name)))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("%s  %s · confidence %d%%  %s\n\n",
		dir, subtitleStyle.Render(idea.Index), idea.Confidence, confidenceBar(idea.Confidence)))

	// Levels table.
	if idea.Entry > 0 {
		sb.WriteString(fmt.Sprintf("  Entry %s   Stop %s   Target %s   R/R %.1f   ~%dd\n",
			fmt.Sprintf("%.2f", idea.Entry),
			failedStyle.Render(fmt.Sprintf("%.2f", idea.Stop)),
			doneStyle.Render(fmt.Sprintf("%.2f", idea.Target)),
			idea.RiskReward, idea.TimeframeDays))
		if idea.PositionNote != "" {
			sb.WriteString(mutedStyle.Render("  Position: " + idea.PositionNote))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString(mutedStyle.Render("  No trade levels recorded for this idea (older run).\n\n"))
	}

	// Price chart.
	if chart := d.renderChart(); chart != "" {
		sb.WriteString(chart)
		sb.WriteString("\n")
		sb.WriteString(mutedStyle.Render(fmt.Sprintf("  ─ price · last %dd · entry/stop/target overlaid · c to zoom", chartWindows[d.windowIdx])))
		sb.WriteString("\n\n")
	} else if d.series == nil {
		sb.WriteString(mutedStyle.Render("  (no saved price data for this run — chart unavailable)\n\n"))
	}

	// Per-domain scores.
	if len(d.scores) > 0 {
		sb.WriteString(subtitleStyle.Render("  Specialist scores"))
		sb.WriteString("\n")
		domains := make([]string, 0, len(d.scores))
		for k := range d.scores {
			domains = append(domains, k)
		}
		sort.Strings(domains)
		for _, dom := range domains {
			sc := d.scores[dom]
			biasStr := string(sc.Bias)
			switch sc.Bias {
			case model.BiasBullish:
				biasStr = doneStyle.Render("bullish ")
			case model.BiasBearish:
				biasStr = failedStyle.Render("bearish ")
			default:
				biasStr = mutedStyle.Render("neutral ")
			}
			sb.WriteString(fmt.Sprintf("  %-13s %s %2d/10  %s\n",
				dom, biasStr, sc.Strength, mutedStyle.Render(truncate(sc.Note, maxNote(d.width)))))
		}
		sb.WriteString("\n")
	}

	// Rationale.
	sb.WriteString(subtitleStyle.Render("  Why"))
	sb.WriteString("\n")
	sb.WriteString("  " + wrap(idea.Why, d.width-4, "  "))
	sb.WriteString("\n\n")
	sb.WriteString(mutedStyle.Render("  [/] prev/next idea · c chart window · t reports · esc back"))
	return sb.String()
}

// renderChart draws the price series with entry/stop/target overlays. Empty
// string when there's no data or the terminal is too narrow.
func (d *detailModel) renderChart() string {
	if d.series == nil || d.width < 60 {
		return ""
	}
	bars := d.series.Bars
	if n := chartWindows[d.windowIdx]; len(bars) > n {
		bars = bars[len(bars)-n:]
	}
	if len(bars) < 2 {
		return ""
	}

	w := d.width - 6
	if w > 100 {
		w = 100
	}
	h := d.height - 22
	if h < 8 {
		h = 8
	}
	if h > 16 {
		h = 16
	}

	minY, maxY := bars[0].Close, bars[0].Close
	for _, b := range bars {
		if b.Close < minY {
			minY = b.Close
		}
		if b.Close > maxY {
			maxY = b.Close
		}
	}
	idea := d.idea()
	for _, lvl := range []float64{idea.Entry, idea.Stop, idea.Target} {
		if lvl > 0 {
			if lvl < minY {
				minY = lvl
			}
			if lvl > maxY {
				maxY = lvl
			}
		}
	}
	pad := (maxY - minY) * 0.05
	if pad == 0 {
		pad = maxY * 0.01
	}
	minY -= pad
	maxY += pad

	t0 := parseDate(bars[0].Date)
	t1 := parseDate(bars[len(bars)-1].Date)

	c := timeserieslinechart.New(w, h,
		timeserieslinechart.WithTimeRange(t0, t1),
		timeserieslinechart.WithYRange(minY, maxY),
	)
	c.XLabelFormatter = timeserieslinechart.DateTimeLabelFormatter()
	for _, b := range bars {
		c.PushDataSet("price", timeserieslinechart.TimePoint{Time: parseDate(b.Date), Value: b.Close})
	}
	c.SetDataSetStyle("price", runningStyle)

	overlay := func(name string, level float64, style lipgloss.Style) {
		if level <= 0 {
			return
		}
		c.PushDataSet(name, timeserieslinechart.TimePoint{Time: t0, Value: level})
		c.PushDataSet(name, timeserieslinechart.TimePoint{Time: t1, Value: level})
		c.SetDataSetStyle(name, style)
	}
	overlay("entry", idea.Entry, selectedStyle)
	overlay("stop", idea.Stop, failedStyle)
	overlay("target", idea.Target, doneStyle)

	c.DrawBrailleAll()
	return c.View()
}

func parseDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func maxNote(width int) int {
	n := width - 32
	if n < 20 {
		n = 20
	}
	return n
}

// wrap does simple word wrapping with a hanging indent.
func wrap(s string, width int, indent string) string {
	if width < 20 {
		width = 20
	}
	words := strings.Fields(s)
	var sb strings.Builder
	line := 0
	for i, w := range words {
		if line+len(w)+1 > width && line > 0 {
			sb.WriteString("\n" + indent)
			line = 0
		} else if i > 0 {
			sb.WriteString(" ")
			line++
		}
		sb.WriteString(w)
		line += len(w)
	}
	return sb.String()
}
