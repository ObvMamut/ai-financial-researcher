package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
)

// ScoreboardFunc builds the performance summary; injected so the TUI does not
// construct market-data clients itself.
type ScoreboardFunc func(ctx context.Context) (*scoreboard.Summary, error)

// scoreboardReadyMsg carries the async build result.
type scoreboardReadyMsg struct {
	sum *scoreboard.Summary
	err error
}

// scoreboardBackMsg returns to the home screen.
type scoreboardBackMsg struct{}

type scoreboardModel struct {
	buildFn ScoreboardFunc
	sum     *scoreboard.Summary
	err     string
	loading bool
	cursor  int
	height  int
}

func newScoreboardModel(buildFn ScoreboardFunc) scoreboardModel {
	return scoreboardModel{buildFn: buildFn, loading: true, height: 30}
}

func (m scoreboardModel) Init() tea.Cmd {
	fn := m.buildFn
	return func() tea.Msg {
		if fn == nil {
			return scoreboardReadyMsg{err: fmt.Errorf("scoreboard unavailable")}
		}
		sum, err := fn(context.Background())
		return scoreboardReadyMsg{sum: sum, err: err}
	}
}

func (m scoreboardModel) Update(msg tea.Msg) (scoreboardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height
	case scoreboardReadyMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err.Error()
		}
		m.sum = msg.sum
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.sum != nil && m.cursor < len(m.sum.Entries)-1 {
				m.cursor++
			}
		case "esc", "q", "b":
			return m, func() tea.Msg { return scoreboardBackMsg{} }
		}
	}
	return m, nil
}

func (m scoreboardModel) View() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Scoreboard"))
	sb.WriteString("\n")
	subtitle := "Past ideas vs current prices (direction-aware)"
	if m.sum != nil && m.sum.Replay {
		subtitle = "Each idea replayed through its own daily bars: fill, then whichever barrier came first"
	}
	sb.WriteString(subtitleStyle.Render(subtitle))
	sb.WriteString("\n\n")

	switch {
	case m.loading:
		sb.WriteString(runningStyle.Render("  Replaying past ideas through their price history…"))
		sb.WriteString("\n\n")
		sb.WriteString(mutedStyle.Render("  esc back"))
		return sb.String()
	case m.err != "":
		sb.WriteString(failedStyle.Render("  " + m.err))
		sb.WriteString("\n\n")
		sb.WriteString(mutedStyle.Render("  esc back"))
		return sb.String()
	case m.sum == nil || len(m.sum.Entries) == 0:
		sb.WriteString(mutedStyle.Render("  Nothing to score yet — ideas need a stored baseline price."))
		if m.sum != nil && m.sum.Skipped > 0 {
			sb.WriteString("\n")
			sb.WriteString(mutedStyle.Render(fmt.Sprintf("  (%d idea(s) from older runs lack one.)", m.sum.Skipped)))
		}
		sb.WriteString("\n\n")
		sb.WriteString(mutedStyle.Render("  esc back"))
		return sb.String()
	}

	s := m.sum
	var head string
	if s.Replay {
		head = fmt.Sprintf("  Win rate %.0f%%  (%dW/%dL) over %d closed   avg P&L %+.2f%%   avg R %+.2f   %d idea(s) / %d run(s)",
			s.WinRate*100, s.Wins, s.Losses, s.Closed, s.AvgPnL, s.AvgR, len(s.Entries), s.RunCount)
	} else {
		head = fmt.Sprintf("  Win rate %.0f%%  (%dW/%dL)   avg P&L %+.2f%%   %d idea(s) / %d run(s)",
			s.WinRate*100, s.Wins, s.Losses, s.AvgPnL, s.Scored, s.RunCount)
	}
	if s.Skipped > 0 {
		head += fmt.Sprintf("   %d skipped", s.Skipped)
	}
	sb.WriteString(selectedStyle.Render(head))
	sb.WriteString("\n")
	if s.Replay {
		sb.WriteString(mutedStyle.Render("  " + outcomeTally(s)))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")

	maxRows := m.height - 10
	if maxRows < 5 {
		maxRows = 5
	}
	start := 0
	if m.cursor >= maxRows {
		start = m.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(s.Entries) {
		end = len(s.Entries)
	}

	for i := start; i < end; i++ {
		e := s.Entries[i]
		prefix := "  "
		if i == m.cursor {
			prefix = "▶ "
		}
		dirStyled := buyStyle.Render(fmt.Sprintf("%-4s", e.Direction))
		if e.Direction == "SELL" {
			dirStyled = sellStyle.Render(fmt.Sprintf("%-4s", e.Direction))
		}

		if e.Err != "" && (!s.Replay || e.Outcome == scoreboard.OutcomeError) {
			note := "price unavailable"
			if s.Replay {
				note = e.Err
			}
			sb.WriteString(fmt.Sprintf("%s%-21s %-8s %s %s\n", prefix, e.RunName, e.Ticker, dirStyled,
				mutedStyle.Render(note)))
			continue
		}
		if e.Outcome == scoreboard.OutcomeOpen && e.EntryFilled == 0 {
			sess := "sessions"
			if e.BarsHeld == 1 {
				sess = "session"
			}
			sb.WriteString(fmt.Sprintf("%s%-21s %-8s %s %s\n", prefix, e.RunName, e.Ticker, dirStyled,
				mutedStyle.Render(fmt.Sprintf("waiting on the %.2f entry — %d %s so far", e.EntryPlanned, e.BarsHeld, sess))))
			continue
		}
		if e.Outcome == scoreboard.OutcomeUnfilled {
			sb.WriteString(fmt.Sprintf("%s%-21s %-8s %s %s\n", prefix, e.RunName, e.Ticker, dirStyled,
				mutedStyle.Render(fmt.Sprintf("never filled — the %.2f limit did not trade", e.EntryPlanned))))
			continue
		}

		pnl := fmt.Sprintf("%+7.2f%%", e.PnLPct)
		switch {
		case e.PnLPct > 0:
			pnl = doneStyle.Render(pnl)
		case e.PnLPct < 0:
			pnl = failedStyle.Render(pnl)
		}

		if s.Replay {
			r := "       "
			if e.Stop > 0 && e.EntryFilled > 0 {
				r = fmt.Sprintf(" %+5.2fR", e.RiskAdjPnL)
			}
			sb.WriteString(fmt.Sprintf("%s%-21s %-8s %s %8.2f → %8.2f  %s%s  %s\n",
				prefix, e.RunName, e.Ticker, dirStyled, e.EntryFilled, e.ExitPrice, pnl, r,
				outcomeStyled(e)))
			continue
		}

		marks := ""
		if e.TargetHit {
			marks += doneStyle.Render(" ✓target")
		}
		if e.StopHit {
			marks += failedStyle.Render(" ✗stop")
		}
		sb.WriteString(fmt.Sprintf("%s%-21s %-8s %s %8.2f → %8.2f  %s%s\n",
			prefix, e.RunName, e.Ticker, dirStyled, e.PriceAtGen, e.Current, pnl, marks))
	}

	sb.WriteString("\n")
	hint := "  ↑/↓ browse · esc back · target/stop judged on current price only"
	if s.Replay {
		hint = "  ↑/↓ browse · esc back · win rate counts closed trades only"
	}
	sb.WriteString(mutedStyle.Render(hint))
	return sb.String()
}

// outcomeTally renders the one-line distribution of how the replayed positions
// ended. It is the fastest read on whether the ideas are being *taken* at all:
// a column of "unfilled" is a levels problem, not a thesis problem.
func outcomeTally(s *scoreboard.Summary) string {
	order := []scoreboard.Outcome{
		scoreboard.OutcomeTarget, scoreboard.OutcomeStop, scoreboard.OutcomeExpired,
		scoreboard.OutcomeOpen, scoreboard.OutcomeUnfilled, scoreboard.OutcomeError,
	}
	var parts []string
	for _, o := range order {
		if n := s.ByOutcome[o]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", o, n))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func outcomeStyled(e scoreboard.Entry) string {
	label := fmt.Sprintf("%s (%dd)", e.Outcome, e.BarsHeld)
	switch e.Outcome {
	case scoreboard.OutcomeTarget:
		return doneStyle.Render(label)
	case scoreboard.OutcomeStop:
		return failedStyle.Render(label)
	case scoreboard.OutcomeOpen:
		return runningStyle.Render(label)
	}
	return mutedStyle.Render(label)
}
