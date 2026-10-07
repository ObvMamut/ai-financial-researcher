package tui

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

func writeFakeRun(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	r := &store.Run{Dir: dir}

	// 200 bars of gently rising prices.
	var s quant.Series
	s.Symbol = "NVDA"
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 200; i++ {
		p := 100 * math.Exp(0.002*float64(i))
		s.Bars = append(s.Bars, quant.Bar{
			Date: t0.AddDate(0, 0, i).Format("2006-01-02"),
			Open: p, High: p * 1.01, Low: p * 0.99, Close: p, Volume: 1e6,
		})
	}
	if err := r.WritePrices("NVDA", s); err != nil {
		t.Fatal(err)
	}

	report := "Quant analysis.\n\n```json\n" +
		`{"domain": "quant", "scores": [{"ticker": "NVDA", "bias": "bullish", "strength": 8, "note": "momentum aligned"}], "missing": []}` +
		"\n```\n"
	if err := os.WriteFile(dir+"/quant.md", []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDetailViewRendersChartLevelsAndScores(t *testing.T) {
	dir := writeFakeRun(t)
	ideas := []model.TradeIdea{{
		Rank: 1, Ticker: "NVDA", Name: "NVIDIA", Index: "sp500",
		Direction: model.DirectionBuy, Confidence: 80,
		Entry: 145, Stop: 138, Target: 160, RiskReward: 2.1, TimeframeDays: 15,
		Why: "Test rationale.",
	}}

	d := newDetailModel(dir, ideas, 0, 100, 40)
	view := d.View()

	for _, want := range []string{"NVDA", "Entry", "138.00", "160.00", "quant", "8/10", "Test rationale."} {
		if !strings.Contains(view, want) {
			t.Errorf("detail view missing %q", want)
		}
	}
	if chart := d.renderChart(); chart == "" {
		t.Error("chart should render with 200 bars at width 100")
	}

	// Narrow terminal: no chart, but the view must still work.
	d.width = 50
	if chart := d.renderChart(); chart != "" {
		t.Error("chart should be suppressed under 60 cols")
	}
	if v := d.View(); !strings.Contains(v, "NVDA") {
		t.Error("narrow view broke")
	}
}

func TestDetailViewOldRunWithoutPricesOrLevels(t *testing.T) {
	dir := t.TempDir() // no prices, no reports
	ideas := []model.TradeIdea{{
		Rank: 1, Ticker: "AAPL", Name: "Apple", Index: "sp500",
		Direction: model.DirectionBuy, Confidence: 70, Why: "Old-style idea.",
	}}
	d := newDetailModel(dir, ideas, 0, 100, 40)
	view := d.View()
	if !strings.Contains(view, "No trade levels recorded") {
		t.Error("missing graceful no-levels notice")
	}
	if !strings.Contains(view, "chart unavailable") {
		t.Error("missing graceful no-chart notice")
	}
}

func TestDetailNavigationWraps(t *testing.T) {
	dir := writeFakeRun(t)
	ideas := []model.TradeIdea{
		{Rank: 1, Ticker: "NVDA", Direction: model.DirectionBuy},
		{Rank: 2, Ticker: "ASML", Direction: model.DirectionBuy},
	}
	d := newDetailModel(dir, ideas, 1, 100, 40)
	d.next()
	if d.idx != 0 {
		t.Errorf("next should wrap to 0, got %d", d.idx)
	}
	d.prev()
	if d.idx != 1 {
		t.Errorf("prev should wrap to 1, got %d", d.idx)
	}
}

func TestDetailViewSaysWhenAnIdeaRepeatsAnOpenCallOrHoldsThroughEarnings(t *testing.T) {
	dir := t.TempDir()
	ideas := []model.TradeIdea{{
		Rank: 1, Ticker: "FCX", Name: "Freeport", Index: "sp500",
		Direction: model.DirectionBuy, Confidence: 24, Entry: 72.56, Stop: 53.39, TimeframeDays: 15,
		RepeatOf: "2026-10-06T18-07-24", EventInWindow: "2026-10-22",
	}}
	d := newDetailModel(dir, ideas, 0, 100, 40)
	view := d.View()
	for _, want := range []string{
		"repeat of the call shipped in run 2026-10-06T18-07-24",
		"holds through earnings on 2026-10-22",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("detail view missing %q:\n%s", want, view)
		}
	}
}
