package scoreboard

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

// fakeYahoo serves the shared chart fixture (adjusted last close 53.0) for
// every symbol.
func fakeYahoo(t *testing.T) {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "marketdata", "testdata", "yahoo_chart_sample.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)
}

func writeRun(t *testing.T, runsDir, name string, ideas model.IdeasResult) {
	t.Helper()
	dir := filepath.Join(runsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(ideas)
	if err := os.WriteFile(filepath.Join(dir, "ideas.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildScoresDirectionAware(t *testing.T) {
	fakeYahoo(t)
	runsDir := t.TempDir()

	writeRun(t, runsDir, "2026-07-01T00-00-00", model.IdeasResult{
		Mode:        "independent",
		GeneratedAt: "2026-07-01T00:00:00Z",
		Ideas: []model.TradeIdea{
			// Current price from the fixture is 53.0 for every symbol.
			{Rank: 1, Ticker: "AAA", Direction: model.DirectionBuy, Confidence: 70,
				PriceAtGeneration: 50, Entry: 50, Stop: 48, Target: 52},
			{Rank: 2, Ticker: "BBB", Direction: model.DirectionSell, Confidence: 60,
				PriceAtGeneration: 55, Entry: 55, Stop: 56, Target: 50},
			// No baseline → must be skipped, not scored as 0%.
			{Rank: 3, Ticker: "CCC", Direction: model.DirectionBuy, Confidence: 50},
		},
	})

	yc := marketdata.NewYahooClient(marketdata.NewCache(t.TempDir()))
	sum, err := Build(context.Background(), runsDir, yc)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if sum.Scored != 2 || sum.Skipped != 1 || sum.RunCount != 1 {
		t.Fatalf("Scored=%d Skipped=%d RunCount=%d, want 2/1/1", sum.Scored, sum.Skipped, sum.RunCount)
	}
	if sum.Wins != 2 || sum.Losses != 0 || sum.WinRate != 1.0 {
		t.Errorf("Wins=%d Losses=%d WinRate=%v, want 2/0/1.0", sum.Wins, sum.Losses, sum.WinRate)
	}

	byTicker := map[string]Entry{}
	for _, e := range sum.Entries {
		byTicker[e.Ticker] = e
	}

	// BUY at 50 → 53: +6.00%, target 52 hit, stop 48 not hit.
	buy := byTicker["AAA"]
	if buy.PnLPct != 6.00 {
		t.Errorf("BUY PnL = %v, want 6.00", buy.PnLPct)
	}
	if !buy.TargetHit || buy.StopHit {
		t.Errorf("BUY TargetHit=%v StopHit=%v, want true/false", buy.TargetHit, buy.StopHit)
	}

	// SELL at 55 → 53: price fell, so direction-aware P&L is +3.64%.
	sell := byTicker["BBB"]
	if sell.PnLPct != 3.64 {
		t.Errorf("SELL PnL = %v, want 3.64", sell.PnLPct)
	}
	if sell.TargetHit || sell.StopHit {
		t.Errorf("SELL TargetHit=%v StopHit=%v, want false/false (53 is between 50 and 56)", sell.TargetHit, sell.StopHit)
	}

	wantAvg := (6.00 + 3.64) / 2
	if math.Abs(sum.AvgPnL-wantAvg) > 1e-9 {
		t.Errorf("AvgPnL = %v, want %v", sum.AvgPnL, wantAvg)
	}

	text := sum.FormatText()
	if !strings.Contains(text, "Win rate 100%") || !strings.Contains(text, "AAA") {
		t.Errorf("FormatText missing expected content:\n%s", text)
	}
}

func TestBuildEmptyRunsDir(t *testing.T) {
	fakeYahoo(t)
	yc := marketdata.NewYahooClient(marketdata.NewCache(t.TempDir()))
	sum, err := Build(context.Background(), filepath.Join(t.TempDir(), "missing"), yc)
	if err != nil {
		t.Fatalf("Build on missing dir: %v", err)
	}
	if sum.Scored != 0 || sum.RunCount != 0 {
		t.Errorf("empty dir scored something: %+v", sum)
	}
}
