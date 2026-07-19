package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

type resultsModel struct {
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

	if len(m.ideas.Ideas) == 0 {
		sb.WriteString(mutedStyle.Render("  No ideas returned."))
	} else {
		for i, idea := range m.ideas.Ideas {
			m.renderIdea(&sb, idea, i == m.cursor)
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

	// Why (indented)
	if idea.Why != "" {
		sb.WriteString("      ")
		sb.WriteString(mutedStyle.Render(truncate(idea.Why, 90)))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
}

func confidenceBar(conf int) string {
	total := 10
	filled := (conf * total) / 100
	bar := strings.Repeat("█", filled) + strings.Repeat("░", total-filled)
	switch {
	case conf >= 70:
		return doneStyle.Render(bar)
	case conf >= 50:
		return runningStyle.Render(bar)
	default:
		return failedStyle.Render(bar)
	}
}
