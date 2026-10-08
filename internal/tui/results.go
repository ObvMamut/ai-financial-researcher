package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

type resultsModel struct {
	meta        *model.RunMeta
	ideas       *model.IdeasResult
	runDir      string
	cursor      int // which idea is selected (for keyboard nav)
	fromHistory bool

	detail *detailModel // non-nil while the detail view is open

	width  int
	height int
}

// openReportsMsg asks the app to open the report viewer for a run directory.
type openReportsMsg struct{ runDir string }

func newResultsModel(ideas *model.IdeasResult, runDir string) resultsModel {
	return resultsModel{ideas: ideas, runDir: runDir, width: 100, height: 30}
}

func (m resultsModel) Init() tea.Cmd { return nil }

// inDetail reports whether the detail view is open (App uses this to route
// esc/b/r keys correctly).
func (m resultsModel) inDetail() bool { return m.detail != nil }

func (m resultsModel) Update(msg tea.Msg) (resultsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.detail != nil {
			m.detail.width = msg.Width
			m.detail.height = msg.Height
		}

	case tea.KeyMsg:
		if m.detail != nil {
			switch msg.String() {
			case "esc", "backspace", "left":
				m.detail = nil
			case "]", "n":
				m.detail.next()
				m.cursor = m.detail.idx
			case "[", "p":
				m.detail.prev()
				m.cursor = m.detail.idx
			case "c":
				m.detail.cycleWindow()
			case "t":
				dir := m.runDir
				return m, func() tea.Msg { return openReportsMsg{runDir: dir} }
			}
			return m, nil
		}

		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.ideas.Ideas)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.ideas.Ideas) > 0 {
				d := newDetailModel(m.runDir, m.ideas.Ideas, m.cursor, m.width, m.height)
				m.detail = &d
			}
		case "t":
			dir := m.runDir
			return m, func() tea.Msg { return openReportsMsg{runDir: dir} }
		}
	}
	return m, nil
}

// screenNoEdgeLine is E1's and Wave D's registered consequence
// (docs/workflow/backtest.md): in legacy independent research the ideas, their
// order and their confidence all come out of the pre-screen funnel, and the
// 10-year lab replay finds its beta-adjusted top-5 excess positive in only 5
// of 11 years once its own 30bp cost is paid. The model stages add nothing
// measurable above that funnel (the 2026-09-23 attribution), and post-earnings
// drift, the one documented effect on this clock, failed D1 and D2 in the lab.
// v5 (2026-10-01) found the screen's top-5 book at 21 and 63 sessions and 12-1
// momentum alone at 21 and 63 failing too (H1, H2).
// v8 (2026-10-08): MAX, IVOL and FIP, three published anomalies the composite
// lacks, fail on the point-in-time US lab at 21 sessions (N1-N3).
// The reader should know that nothing tested on this horizon picks well.
const screenNoEdgeLine = "  No signal tested has shown an edge net of cost: not the pre-screen\n  that ranked these ideas, held 15, 21 or 63 sessions, not 12-1\n  momentum alone, not the model stages, not post-earnings drift. A\n  survivorship-free US replay (2017-2026) confirms it for the screen\n  and finds none in MAX, IVOL or FIP either."

func (m resultsModel) View() string {
	if m.detail != nil {
		return m.detail.View()
	}

	var sb strings.Builder

	sb.WriteString(titleStyle.Render("Trade Ideas"))
	sb.WriteString("\n")
	sb.WriteString(subtitleStyle.Render(fmt.Sprintf("Generated %s · mode: %s",
		m.ideas.GeneratedAt, m.ideas.Mode)))
	sb.WriteString("\n\n")

	if line := model.SourceDiagnosticsLine(m.meta); line != "" {
		sb.WriteString(mutedStyle.Render(line) + "\n")
	}
	if line := model.ChiefProvenanceLine(m.meta); line != "" {
		sb.WriteString(mutedStyle.Render(line))
		sb.WriteString("\n\n")
	}

	if m.ideas.ResearchMode == "thesis" {
		summary := m.ideas.ResearchSummary
		if summary == nil && m.meta != nil {
			summary = model.SummarizeResearch(m.meta.ResearchOutcomes, m.ideas.Decisions)
		}
		if summary != nil {
			sb.WriteString(summary.String() + "\n\n")
		}
		for _, issue := range model.ResearchRunIssues(m.meta) {
			sb.WriteString(issue + "\n")
		}
	}
	if m.ideas.Mode == "independent" && m.ideas.ResearchMode != "thesis" && len(m.ideas.Ideas) > 0 {
		sb.WriteString(mutedStyle.Render(screenNoEdgeLine) + "\n\n")
	}
	if len(m.ideas.Ideas) == 0 {
		sb.WriteString(mutedStyle.Render("  No ideas returned."))
	} else {
		for i, idea := range m.ideas.Ideas {
			m.renderIdea(&sb, idea, i == m.cursor)
		}
	}

	if m.ideas.ResearchMode == "thesis" {
		for _, d := range m.ideas.Decisions {
			if d.Status == "watchlist" || d.Status == "rejected" {
				sb.WriteString(fmt.Sprintf("\n  %s · %s: %s\n", d.Ticker, decisionLabel(d), truncate(decisionReason(d), 180)))
			}
		}
	}
	if m.ideas.Notes != "" {
		sb.WriteString("\n")
		sb.WriteString(mutedStyle.Render("  Notes: " + truncate(m.ideas.Notes, 100)))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	sb.WriteString(mutedStyle.Render(fmt.Sprintf("  Artifacts saved to: %s", m.runDir)))
	sb.WriteString("\n")
	back := "q quit"
	if m.fromHistory {
		back = "esc back to history · q quit"
	}
	sb.WriteString(mutedStyle.Render("  ↑/↓ browse · Enter detail · t reports · " + back + " · r run again"))

	return sb.String()
}

func (m resultsModel) renderIdea(sb *strings.Builder, idea model.TradeIdea, selected bool) {
	// Direction badge
	var dirStr string
	if idea.Direction == model.DirectionBuy {
		dirStr = buyStyle.Render(" BUY  ")
	} else {
		dirStr = sellStyle.Render(" SELL ")
	}

	// Confidence bar
	bar := confidenceBar(idea.Confidence)

	prefix := "  "
	if selected {
		prefix = "▶ "
	}

	header := fmt.Sprintf("%s#%d  %s  %-8s  %-22s  %s  %d%%",
		prefix,
		idea.Rank,
		dirStr,
		idea.Ticker,
		truncate(idea.Name, 22),
		bar,
		idea.Confidence,
	)

	if idea.Thesis != nil {
		header = fmt.Sprintf("%s#%d  %s  %-8s  %s · evidence %s", prefix, idea.Rank, dirStr, idea.Ticker, idea.Status, idea.Thesis.EvidenceQuality)
	}
	if selected {
		sb.WriteString(selectedStyle.Render(header))
	} else {
		sb.WriteString(header)
	}
	sb.WriteString("\n")

	// Trade mechanics line (when present).
	if idea.Entry > 0 {
		mech := fmt.Sprintf("      entry %.2f · stop %.2f · target %.2f · R/R %.1f · ~%dd",
			idea.Entry, idea.Stop, idea.Target, idea.RiskReward, idea.TimeframeDays)
		sb.WriteString(mutedStyle.Render(mech))
		sb.WriteString("\n")
	}

	if idea.Thesis != nil {
		sb.WriteString(mutedStyle.Render("      Why now: " + truncate(idea.Thesis.WhyNow, 100) + "\n      Invalidated by: " + truncate(idea.Thesis.Invalidation, 100) + "\n      Entry expires " + idea.Thesis.EntryExpiresOn + " · exit by " + idea.Thesis.ExpiresOn + "\n"))
	}
	// Why (indented)
	if idea.Why != "" {
		sb.WriteString("      ")
		sb.WriteString(mutedStyle.Render(truncate(idea.Why, 90)))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
}

// Confidence bands for the bar's colour. They are set against the scale
// `internal/orchestrator/basescore.go` actually produces: a base score is a
// weighted vote across five domains scored against the strongest joint verdict
// their rubrics permit, so full agreement is 70+ and a well-supported idea with
// one domain dissenting lands in the 50s and 60s.
//
// The old thresholds (70 / 50) were set when the Chief Analyst asserted its own
// confidence and routinely returned 70–88. Once base-score anchoring went live on
// 2026-08-31 nothing shipped above 45 for four consecutive runs, so every idea in
// every run rendered red — the app telling the user its own best work had failed.
const (
	confidenceStrong   = 55
	confidenceModerate = 35
)

func confidenceBar(conf int) string {
	total := 10
	filled := (conf * total) / 100
	if filled > total {
		filled = total
	} else if filled < 0 {
		filled = 0
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", total-filled)
	switch {
	case conf >= confidenceStrong:
		return doneStyle.Render(bar)
	case conf >= confidenceModerate:
		return runningStyle.Render(bar)
	default:
		return failedStyle.Render(bar)
	}
}

func decisionLabel(d model.SelectionDecision) string {
	if d.Blocked == model.BlockedResearchFailure {
		return "research failed"
	}
	return d.Status
}

func decisionReason(d model.SelectionDecision) string {
	if d.Blocked == model.BlockedResearchFailure && d.ReviewReason != "" {
		return d.ReviewReason
	}
	return d.Reason
}
