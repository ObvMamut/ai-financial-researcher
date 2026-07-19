package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// historyOpenMsg asks the app to open one past run's results.
type historyOpenMsg struct{ dir string }

// historyBackMsg returns to the home screen.
type historyBackMsg struct{}

type historyModel struct {
	runs   []store.RunSummary
	cursor int
	err    string
	height int
}

func newHistoryModel(runsDir string) historyModel {
	m := historyModel{height: 30}
	runs, err := store.ListRuns(runsDir)
	if err != nil {
		m.err = err.Error()
	}
	m.runs = runs
	return m
}

func (m historyModel) Init() tea.Cmd { return nil }

func (m historyModel) Update(msg tea.Msg) (historyModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.runs)-1 {
				m.cursor++
			}
		case "enter":
			if m.cursor < len(m.runs) {
				dir := m.runs[m.cursor].Dir
				return m, func() tea.Msg { return historyOpenMsg{dir: dir} }
			}
		case "esc", "q", "b":
			return m, func() tea.Msg { return historyBackMsg{} }
		}
	}
	return m, nil
}

func (m historyModel) View() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Run History"))
	sb.WriteString("\n")
	sb.WriteString(subtitleStyle.Render(fmt.Sprintf("%d saved runs", len(m.runs))))
	sb.WriteString("\n\n")

	if m.err != "" {
		sb.WriteString(failedStyle.Render("  " + m.err))
		sb.WriteString("\n")
	}
	if len(m.runs) == 0 {
		sb.WriteString(mutedStyle.Render("  No past runs found."))
		sb.WriteString("\n")
	}

	// Window the list to the terminal height.
	maxRows := m.height - 8
	if maxRows < 5 {
		maxRows = 5
	}
	start := 0
	if m.cursor >= maxRows {
		start = m.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(m.runs) {
		end = len(m.runs)
	}

	for i := start; i < end; i++ {
		r := m.runs[i]
		prefix := "  "
		if i == m.cursor {
			prefix = "▶ "
		}

		what := r.Mode
		if r.Ticker != "" {
			what = fmt.Sprintf("%s (%s)", r.Mode, r.Ticker)
		}
		if what == "" {
			what = "?"
		}

		outcome := r.Outcome
		switch r.Outcome {
		case "complete":
			outcome = doneStyle.Render("complete")
		case "degraded":
			outcome = runningStyle.Render("degraded")
		case "unknown":
			outcome = mutedStyle.Render("unknown ")
		default:
			outcome = failedStyle.Render(r.Outcome)
		}

		line := fmt.Sprintf("%s%-21s  %-22s  %s  %d idea(s)", prefix, r.Name, what, outcome, r.NumIdeas)
		if i == m.cursor {
			sb.WriteString(selectedStyle.Render(line))
		} else {
			sb.WriteString(line)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	sb.WriteString(mutedStyle.Render("  ↑/↓ browse · Enter open · esc back"))
	return sb.String()
}
