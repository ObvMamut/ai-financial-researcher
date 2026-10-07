package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestStampEventWindowsMarksOnlyEventsBeforeTheTimeExit(t *testing.T) {
	// 2026-10-07: FCX reports 10-22, inside a 15-session hold. TTE.PA's 10-29 is
	// the session after the hold ends (10-28), although the news report called
	// it inside the window; TJX's 11-18 is well past. The date comes from the
	// verified calendar, not from any label a model wrote.
	asOf := time.Date(2026, 10, 7, 5, 13, 0, 0, time.UTC)
	v := verified{AsOf: asOf, Events: map[string]time.Time{
		"FCX":    time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC),
		"TTE.PA": time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC),
		"TJX":    time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC),
	}}
	ideas := []model.TradeIdea{
		{Ticker: "fcx", TimeframeDays: 15},
		{Ticker: "TTE.PA", TimeframeDays: 15},
		{Ticker: "TJX", TimeframeDays: 15},
		{Ticker: "MU", TimeframeDays: 15},
	}
	stampEventWindows(ideas, v)
	want := []string{"2026-10-22", "", "", ""}
	for i, w := range want {
		if ideas[i].EventInWindow != w {
			t.Errorf("%s: event_in_window %q, want %q", ideas[i].Ticker, ideas[i].EventInWindow, w)
		}
	}
}

func TestSelectionRowsCarryTheEventInsideTheMeritVetoHold(t *testing.T) {
	asOf := time.Date(2026, 10, 7, 5, 13, 0, 0, time.UTC)
	v := verified{AsOf: asOf, Events: map[string]time.Time{"FCX": time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)}}
	rows := buildSelectionRows(Config{}, []model.Candidate{{Ticker: "FCX", Bias: model.BiasBullish}, {Ticker: "MU", Bias: model.BiasBullish}}, nil, nil, nil, v)
	got := map[string]string{}
	for _, r := range rows {
		got[r.Ticker] = r.EventInWindow
	}
	if got["FCX"] != "2026-10-22" || got["MU"] != "" {
		t.Errorf("rows carry %v, want FCX 2026-10-22 and MU none", got)
	}
}

func TestSelectionBlockTellsTheWriterWhichHoldsRunThroughEarnings(t *testing.T) {
	rows := []model.SelectionRow{
		{Ticker: "FCX", Direction: model.DirectionBuy, MeritRank: 1, EventInWindow: "2026-10-22"},
		{Ticker: "TTE.PA", Direction: model.DirectionBuy, MeritRank: 2},
	}
	book := []model.TradeIdea{{Ticker: "FCX"}, {Ticker: "TTE.PA"}}
	got := selectionBlock(rows, book, nil, 5)
	for _, want := range []string{
		"FCX BUY · merit +0.00 · setup no pre-screen row · base 0 · earnings 2026-10-22 inside the hold",
		"TTE.PA BUY · merit +0.00 · setup no pre-screen row · base 0 · no verified earnings inside the hold",
		"nothing exits early on an event",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("selection block lacks %q:\n%s", want, got)
		}
	}
}
