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

	if !strings.Contains(m.View(), "Fetching current prices") {
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
