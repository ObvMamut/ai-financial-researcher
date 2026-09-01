package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/orchestrator"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// agentRow tracks display state for one agent.
type agentRow struct {
	role     string
	status   model.AgentStatus
	duration int64 // ms, set when done/failed
	errMsg   string
}

type runModel struct {
	mode    model.Mode
	ticker  string
	eventCh <-chan orchestrator.Event
	cancel  context.CancelFunc

	agents   []agentRow
	agentIdx map[string]int // role → index in agents slice
	logs     []string       // all log lines
	viewport viewport.Model
	progress progress.Model
	loading  bool
	done     bool
	aborted  bool
	errMsg   string
	runDir   string

	width    int
	height   int
	spinner  int
	spinTime time.Time
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func newRunModel(req model.RunRequest, eventCh <-chan orchestrator.Event, cancel context.CancelFunc) runModel {
	// Pre-populate agent rows in pipeline order
	var roles []string
	if req.Mode == model.ModeIndependent {
		indices := req.Indices
		if len(indices) == 0 {
			indices = universe.AllIndices()
		}
		for _, idx := range indices {
			roles = append(roles, "scout-"+idx)
		}
	}
	roles = append(roles,
		"quant-data",
		"news", "fundamentals", "quant", "sentiment", "macro",
		"chief-analyst")

	rows := make([]agentRow, len(roles))
	idx := make(map[string]int, len(roles))
	for i, r := range roles {
		rows[i] = agentRow{role: r, status: model.StatusQueued}
		idx[r] = i
	}

	prog := progress.New(progress.WithDefaultGradient())
	vp := viewport.New(80, 10)

	return runModel{
		mode:     req.Mode,
		ticker:   req.Ticker,
		eventCh:  eventCh,
		cancel:   cancel,
		agents:   rows,
		agentIdx: idx,
		viewport: vp,
		loading:  true,
		progress: prog,
		spinTime: time.Now(),
	}
}

// waitForEvent is a Bubble Tea command that blocks until the next orchestrator event.
func waitForEvent(ch <-chan orchestrator.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return orchestratorDoneMsg{}
		}
		return e
	}
}

type orchestratorDoneMsg struct{}
type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m runModel) Init() tea.Cmd {
	return tea.Batch(waitForEvent(m.eventCh), tickCmd())
}

func (m runModel) Update(msg tea.Msg) (runModel, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = msg.Width - 4
		m.viewport.Height = msg.Height - len(m.agents) - 10
		m.progress.Width = msg.Width - 10
		if m.viewport.Height < 0 {
			m.viewport.Height = 5
		}
		return m, nil

	case tickMsg:
		m.spinner = (m.spinner + 1) % len(spinFrames)
		return m, tickCmd()

	case orchestratorDoneMsg:
		if !m.done {
			m.done = true
			m.aborted = true
		}
		return m, nil

	case orchestrator.Event:
		m.loading = false
		var nextCmd tea.Cmd
		if !m.done {
			nextCmd = waitForEvent(m.eventCh)
		}
		switch msg.Type {
		case orchestrator.EventStatus:
			i, ok := m.agentIdx[msg.Agent]
			if !ok {
				// Unknown role (new pipeline stage): append a row instead of
				// silently dropping the status.
				i = len(m.agents)
				m.agents = append(m.agents, agentRow{role: msg.Agent, status: model.StatusQueued})
				m.agentIdx[msg.Agent] = i
			}
			m.agents[i].status = msg.Status
			if msg.Report != nil {
				m.agents[i].duration = msg.Report.Duration
				m.agents[i].errMsg = msg.Report.Err
			}
		case orchestrator.EventLog:
			m.logs = append(m.logs, msg.Message)
			m.viewport.SetContent(strings.Join(m.logs, "\n"))
			m.viewport.GotoBottom()
		case orchestrator.EventComplete:
			m.done = true
			m.runDir = msg.Message
		case orchestrator.EventError:
			m.done = true
			m.errMsg = msg.Message
		}
		return m, nextCmd

	case tea.KeyMsg:
		if m.done {
			switch msg.String() {
			case "q", "ctrl+c", "esc":
				return m, tea.Quit
			}
		} else {
			if msg.String() == "ctrl+c" {
				m.done = true
				m.aborted = true
				if m.cancel != nil {
					m.cancel()
				}
				// Keep draining in the background so the orchestrator's
				// goroutine — now failing fast on the cancelled context — can
				// still deliver its final event and exit, instead of
				// blocking forever on a send nobody is receiving.
				go func(ch <-chan orchestrator.Event) {
					for range ch {
					}
				}(m.eventCh)
				return m, nil
			}
		}
	}

	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m runModel) View() string {
	var sb strings.Builder

	header := "Running — Independent Research"
	if m.mode == model.ModeSingle {
		header = fmt.Sprintf("Running — %s", m.ticker)
	}
	sb.WriteString(titleStyle.Render(header))
	sb.WriteString("\n\n")

	if m.loading {
		sb.WriteString(mutedStyle.Render("  Starting run…"))
		sb.WriteString("\n\n")
	}

	// Agent status rows
	for _, a := range m.agents {
		_, styledIcon := statusIcon(a.status, m.spinner)
		label := agentLabel(a.role)
		line := fmt.Sprintf("  %s  %-28s", styledIcon, label)
		switch a.status {
		case model.StatusDone:
			line += doneStyle.Render(fmt.Sprintf("done (%dms)", a.duration))
		case model.StatusFailed:
			line += failedStyle.Render("failed")
			if a.errMsg != "" {
				line += mutedStyle.Render(" — " + truncate(a.errMsg, 40))
			}
		case model.StatusRunning:
			line += runningStyle.Render("running…")
		default:
			line += queuedStyle.Render("queued")
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	// Progress
	doneCount := 0
	for _, a := range m.agents {
		if a.status == model.StatusDone || a.status == model.StatusFailed {
			doneCount++
		}
	}
	pct := float64(doneCount) / float64(len(m.agents))
	sb.WriteString("\n  " + m.progress.ViewAs(pct) + "\n")

	// Logs
	if len(m.logs) > 0 {
		sb.WriteString("\n")
		sb.WriteString(mutedStyle.Render("  ─── log ────────────────────────────────────"))
		sb.WriteString("\n")
		sb.WriteString(m.viewport.View())
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	if m.done {
		if m.aborted {
			sb.WriteString(failedStyle.Render("  Run aborted."))
		} else if m.errMsg != "" {
			sb.WriteString(failedStyle.Render("  Error: " + m.errMsg))
		} else if m.runDir != "" {
			sb.WriteString(doneStyle.Render("  Complete! Showing results…"))
		}
		sb.WriteString("\n")
		sb.WriteString(mutedStyle.Render("  r retry · b back · q quit"))
	} else {
		sb.WriteString(mutedStyle.Render("  Ctrl+C to abort"))
	}

	return sb.String()
}

// IsDone reports whether the pipeline has finished (success or error).
func (m runModel) IsDone() bool { return m.done }

// HasError reports whether the run ended with an error.
func (m runModel) HasError() bool { return m.errMsg != "" }

func statusIcon(s model.AgentStatus, spin int) (icon, styled string) {
	switch s {
	case model.StatusDone:
		return "✓", doneStyle.Render("✓")
	case model.StatusFailed:
		return "✗", failedStyle.Render("✗")
	case model.StatusRunning:
		f := spinFrames[spin]
		return f, runningStyle.Render(f)
	default:
		return "·", queuedStyle.Render("·")
	}
}

func agentLabel(role string) string {
	labels := map[string]string{
		"scout-sp500":   "Scout SP500",
		"scout-nq100":   "Scout NQ100",
		"scout-eu50":    "Scout EU50",
		"scout-asia100": "Scout Asia100",
		"quant-data":    "Price Data & Quant Metrics",
		"news":          "News & Catalysts",
		"fundamentals":  "Fundamentals",
		"quant":         "Quant Analyst",
		"technicals":    "Technicals", // legacy runs
		"sentiment":     "Sentiment",
		"macro":         "Macro",
		"chief-analyst": "Chief Analyst (Claude)",
	}
	if l, ok := labels[role]; ok {
		return l
	}
	return role
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
