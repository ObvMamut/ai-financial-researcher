package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// homeChoice identifies which option the cursor is on.
type homeChoice int

const (
	choiceIndependent homeChoice = iota
	choiceStock
	choiceHistory
	choiceScoreboard
)

var indexLabels = map[string]string{
	"sp500":   "S&P 500        (US large cap)",
	"nq100":   "Nasdaq 100     (US tech/growth)",
	"eu50":    "EURO STOXX 50  (Eurozone blue chips)",
	"asia100": "Asia 100       (pan-Asia large caps)",
}

type homeModel struct {
	choice    homeChoice
	input     textinput.Model
	inputMode bool // true when user is typing a ticker

	indexMode   bool // true when selecting indices for independent research
	indexCursor int
	indexKeys   []string
	indexSel    map[string]bool

	err string
}

func newHomeModel() homeModel {
	ti := textinput.New()
	ti.Placeholder = "e.g. AAPL, TSLA, 7203.T"
	ti.CharLimit = 20
	ti.Width = 30

	keys := universe.AllIndices()
	sel := make(map[string]bool, len(keys))
	for _, k := range keys {
		sel[k] = true // default: screen everything
	}
	return homeModel{input: ti, indexKeys: keys, indexSel: sel}
}

// startRunMsg is sent to the app when the user confirms their selection.
type startRunMsg struct {
	req model.RunRequest
}

// openHistoryMsg asks the app to show the run-history screen.
type openHistoryMsg struct{}

// openScoreboardMsg asks the app to show the performance scoreboard.
type openScoreboardMsg struct{}

func (m homeModel) Init() tea.Cmd { return nil }

func (m homeModel) Update(msg tea.Msg) (homeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.inputMode {
			return m.handleInputMode(msg)
		}
		if m.indexMode {
			return m.handleIndexMode(msg)
		}
		return m.handleMenuMode(msg)
	}
	return m, nil
}

func (m homeModel) handleMenuMode(msg tea.KeyMsg) (homeModel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.choice > choiceIndependent {
			m.choice--
		}
	case "down", "j", "tab":
		if m.choice < choiceScoreboard {
			m.choice++
		} else if msg.String() == "tab" {
			m.choice = choiceIndependent
		}
	case "enter", " ":
		switch m.choice {
		case choiceIndependent:
			m.indexMode = true
			m.err = ""
			return m, nil
		case choiceStock:
			m.inputMode = true
			m.err = ""
			m.input.Focus()
			return m, textinput.Blink
		case choiceHistory:
			return m, func() tea.Msg { return openHistoryMsg{} }
		case choiceScoreboard:
			return m, func() tea.Msg { return openScoreboardMsg{} }
		}
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m homeModel) handleIndexMode(msg tea.KeyMsg) (homeModel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.indexCursor > 0 {
			m.indexCursor--
		}
	case "down", "j":
		if m.indexCursor < len(m.indexKeys)-1 {
			m.indexCursor++
		}
	case " ":
		k := m.indexKeys[m.indexCursor]
		m.indexSel[k] = !m.indexSel[k]
		m.err = ""
	case "a":
		// Toggle all: if everything is on, clear; otherwise select all.
		all := true
		for _, k := range m.indexKeys {
			if !m.indexSel[k] {
				all = false
				break
			}
		}
		for _, k := range m.indexKeys {
			m.indexSel[k] = !all
		}
		m.err = ""
	case "enter":
		var selected []string
		for _, k := range m.indexKeys {
			if m.indexSel[k] {
				selected = append(selected, k)
			}
		}
		if len(selected) == 0 {
			m.err = "Select at least one index (space to toggle)."
			return m, nil
		}
		if len(selected) == len(m.indexKeys) {
			selected = nil // all = orchestrator default
		}
		req := model.RunRequest{Mode: model.ModeIndependent, Indices: selected}
		return m, func() tea.Msg { return startRunMsg{req: req} }
	case "esc":
		m.indexMode = false
		m.err = ""
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m homeModel) handleInputMode(msg tea.KeyMsg) (homeModel, tea.Cmd) {
	switch msg.String() {
	case "enter":
		ticker := strings.TrimSpace(m.input.Value())
		if ticker == "" {
			m.err = "Please enter a ticker symbol."
			return m, nil
		}
		if !isValidTicker(ticker) {
			m.err = "Invalid ticker — use letters, digits, dots, and hyphens only."
			return m, nil
		}
		req := model.RunRequest{Mode: model.ModeSingle, Ticker: strings.ToUpper(ticker)}
		return m, func() tea.Msg {
			return startRunMsg{req: req}
		}
	case "esc":
		m.inputMode = false
		m.input.Blur()
		m.err = ""
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m homeModel) View() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render("Claude Financial Researcher"))
	sb.WriteString("\n")
	sb.WriteString(subtitleStyle.Render("AI-powered swing trade ideas · no API keys required"))
	sb.WriteString("\n\n")

	menu := []struct {
		choice homeChoice
		label  string
	}{
		{choiceIndependent, "Independent research  (screen selected indices → top 5 ideas)"},
		{choiceStock, "Input a stock         (analyse one ticker → single verdict)"},
		{choiceHistory, "Run history           (browse past runs, ideas, and reports)"},
		{choiceScoreboard, "Scoreboard            (how past ideas performed vs today's prices)"},
	}
	for _, item := range menu {
		if m.choice == item.choice && !m.indexMode && !m.inputMode {
			sb.WriteString(selectedStyle.Render("▶ " + item.label))
		} else {
			sb.WriteString(mutedStyle.Render("  " + item.label))
		}
		sb.WriteString("\n")
	}

	switch {
	case m.indexMode:
		sb.WriteString("\n")
		sb.WriteString(subtitleStyle.Render("  Select indices to screen:"))
		sb.WriteString("\n")
		for i, k := range m.indexKeys {
			mark := "[ ]"
			if m.indexSel[k] {
				mark = "[x]"
			}
			line := fmt.Sprintf("  %s %s", mark, indexLabels[k])
			if i == m.indexCursor {
				sb.WriteString(selectedStyle.Render("▶" + line[1:]))
			} else {
				sb.WriteString(line)
			}
			sb.WriteString("\n")
		}
		if m.err != "" {
			sb.WriteString("  ")
			sb.WriteString(failedStyle.Render(m.err))
			sb.WriteString("\n")
		}
		sb.WriteString(mutedStyle.Render("  space toggle · a all/none · Enter start · Esc cancel"))

	case m.inputMode:
		sb.WriteString("\n")
		sb.WriteString("  Ticker: ")
		sb.WriteString(m.input.View())
		if m.err != "" {
			sb.WriteString("\n  ")
			sb.WriteString(failedStyle.Render(m.err))
		}
		sb.WriteString("\n")
		sb.WriteString(mutedStyle.Render("  Enter to confirm · Esc to cancel"))

	default:
		sb.WriteString("\n")
		sb.WriteString(mutedStyle.Render("  ↑/↓ select · Enter to start · q to quit"))
	}

	return sb.String()
}

// isValidTicker rejects obviously malformed input.
func isValidTicker(s string) bool {
	if len(s) == 0 || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '-') {
			return false
		}
	}
	return true
}
