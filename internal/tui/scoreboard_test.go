package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
)

func TestScoreboardScreenLifecycle(t *testing.T) {
	sum := &scoreboard.Summary{
		Scored: 2, Wins: 1, Losses: 1, WinRate: 0.5, AvgPnL: 1.25, RunCount: 1,
		Entries: []scoreboard.Entry{
			{RunName: "2026-07-01T00-00-00", Ticker: "AAA", Direction: "BUY",
				PriceAtGen: 50, Current: 53, PnLPct: 6.0, TargetHit: true},
			{RunName: "2026-07-01T00-00-00", Ticker: "BBB", Direction: "SELL",
				PriceAtGen: 55, Current: 57, PnLPct: -3.5},
		},
	}
	m := newScoreboardModel(func(ctx context.Context) (*scoreboard.Summary, error) {
		return sum, nil
	})

	if !strings.Contains(m.View(), "Replaying past ideas") {
		t.Errorf("loading view missing progress line:\n%s", m.View())
	}

	// Init's command performs the build; feed its result back in.
	msg := m.Init()()
	m, _ = m.Update(msg)

	v := m.View()
	for _, want := range []string{"Win rate 50%", "AAA", "BBB", "target", "+6.00%"} {
		if !strings.Contains(v, want) {
			t.Errorf("ready view missing %q:\n%s", want, v)
		}
	}

	// esc requests the back message.
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc produced no command")
	}
	if _, ok := cmd().(scoreboardBackMsg); !ok {
		t.Errorf("esc message = %T, want scoreboardBackMsg", cmd())
	}
}

func TestScoreboardScreenEmpty(t *testing.T) {
	m := newScoreboardModel(func(ctx context.Context) (*scoreboard.Summary, error) {
		return &scoreboard.Summary{Skipped: 3}, nil
	})
	m, _ = m.Update(m.Init()())
	v := m.View()
	if !strings.Contains(v, "Nothing to score yet") || !strings.Contains(v, "3 idea(s)") {
		t.Errorf("empty view wrong:\n%s", v)
	}
}

func TestScoreboardScreenShowsTheReplay(t *testing.T) {
	// The replay's headline is the closed-trade record, and the rows say which
	// barrier ended each trade. A row that never became a trade says so instead
	// of showing a 0.00% loss.
	sum := &scoreboard.Summary{
		Replay: true, Scored: 3, Closed: 2, Wins: 1, Losses: 1, WinRate: 0.5,
		AvgPnL: 1.25, AvgR: 0.35, RunCount: 1,
		ByOutcome: map[scoreboard.Outcome]int{
			scoreboard.OutcomeTarget: 1, scoreboard.OutcomeStop: 1, scoreboard.OutcomeUnfilled: 1,
		},
		Entries: []scoreboard.Entry{
			{RunName: "2026-07-01T00-00-00", Ticker: "AAA", Direction: "BUY",
				Outcome: scoreboard.OutcomeTarget, EntryFilled: 50, Stop: 47, ExitPrice: 53,
				PnLPct: 6.0, RiskAdjPnL: 1.0, BarsHeld: 4},
			{RunName: "2026-07-01T00-00-00", Ticker: "BBB", Direction: "SELL",
				Outcome: scoreboard.OutcomeStop, EntryFilled: 55, Stop: 57, ExitPrice: 57,
				PnLPct: -3.5, RiskAdjPnL: -1.0, BarsHeld: 2},
			{RunName: "2026-07-01T00-00-00", Ticker: "CCC", Direction: "BUY",
				Outcome: scoreboard.OutcomeUnfilled, EntryPlanned: 90},
		},
	}
	m := newScoreboardModel(func(ctx context.Context) (*scoreboard.Summary, error) { return sum, nil })
	m, _ = m.Update(m.Init()())

	v := m.View()
	for _, want := range []string{"2 closed", "Win rate 50%", "avg R +0.35", "target", "stop", "never filled", "+6.00%"} {
		if !strings.Contains(v, want) {
			t.Errorf("replay view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "current price") {
		t.Errorf("replay view still talks about the current price:\n%s", v)
	}
}
