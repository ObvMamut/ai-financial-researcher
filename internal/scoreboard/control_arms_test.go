package scoreboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// mapPrices is a price source over synthetic series, keyed by upper-cased symbol.
type mapPrices map[string]*quant.Series

func (m mapPrices) History(_ context.Context, sym string) (*quant.Series, error) {
	if s, ok := m[strings.ToUpper(sym)]; ok {
		return s, nil
	}
	return nil, errors.New("no series")
}
func (m mapPrices) HistoryFresh(ctx context.Context, sym string) (*quant.Series, error) {
	return m.History(ctx, sym)
}
func (m mapPrices) LastClose(context.Context, string) (float64, string, error) {
	return 0, "", errors.New("unused")
}
func (m mapPrices) Prefetch(context.Context, []string) int { return 0 }

// sessions returns n weekday dates starting at start.
func sessions(start string, n int) []string {
	d, _ := time.Parse("2006-01-02", start)
	out := make([]string, 0, n)
	for len(out) < n {
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			out = append(out, d.Format("2006-01-02"))
		}
		d = d.AddDate(0, 0, 1)
	}
	return out
}

// synth builds a series whose close at session i is close(i).
func synth(sym string, dates []string, close func(i int) float64) *quant.Series {
	s := &quant.Series{Symbol: sym}
	for i, d := range dates {
		c := close(i)
		s.Bars = append(s.Bars, quant.Bar{Date: d, Open: c, High: c, Low: c, Close: c, Volume: 1e6})
	}
	return s
}

// benchClose wiggles, so a beta against it is defined.
func benchClose(i int) float64 { return 100 * math.Exp(0.01*math.Sin(float64(i))) }

func TestSpearmanRanksAndTies(t *testing.T) {
	if r, ok := spearman([]float64{1, 2, 3, 4}, []float64{10, 20, 35, 90}); !ok || math.Abs(r-1) > 1e-12 {
		t.Errorf("monotone = %v ok=%v, want 1", r, ok)
	}
	if r, ok := spearman([]float64{1, 2, 3, 4}, []float64{4, 3, 2, 1}); !ok || math.Abs(r+1) > 1e-12 {
		t.Errorf("reversed = %v, want -1", r)
	}
	// Tied scores take their average rank rather than the order they arrived in.
	if got := ranks([]float64{5, 1, 5, 3}); fmt.Sprint(got) != "[3.5 1 3.5 2]" {
		t.Errorf("ranks = %v, want [3.5 1 3.5 2]", got)
	}
	if _, ok := spearman([]float64{1, 1, 1}, []float64{1, 2, 3}); ok {
		t.Error("a constant ranking has no correlation to report")
	}
}

func TestBetaBeforeIsPointInTime(t *testing.T) {
	dates := sessions("2025-01-06", 300)
	b := synth("^GSPC", dates, benchClose)
	// Log returns exactly twice the benchmark's, until the anchor.
	anchor := 200
	s := synth("TWO", dates, func(i int) float64 {
		if i <= anchor {
			return math.Pow(benchClose(i)/100, 2) * 50
		}
		return 50 * float64(1+i%7) // anything at all after the anchor
	})
	beta, ok := betaBefore(s, b, dates[anchor])
	if !ok || math.Abs(beta-2) > 1e-9 {
		t.Fatalf("beta = %v ok=%v, want 2 from the bars up to the anchor only", beta, ok)
	}
	if _, ok := betaBefore(s, b, dates[30]); ok {
		t.Error("thirty sessions of history must not produce a beta")
	}
}

func TestHedgedExcessSignsLikeTheExcess(t *testing.T) {
	// A long up 5% in a market up 2% at beta 1.5 selected 2 points.
	if got := hedgedExcess(model.DirectionBuy, 5, 2, 1.5); got != 2 {
		t.Errorf("long hedged = %v, want 2", got)
	}
	// A short whose name fell 3% (pct +3) in a market down 2% at beta 1.5
	// fell exactly as its beta implied: zero selection.
	if got := hedgedExcess(model.DirectionSell, 3, -2, 1.5); got != 0 {
		t.Errorf("short hedged = %v, want 0", got)
	}
}

func TestParseLean(t *testing.T) {
	for in, want := range map[string]string{
		"BUY": "BUY/", "SELL (weak)": "SELL/weak", "buy (clear)": "BUY/clear",
		"none evident": "", "NONE": "", "": "",
	} {
		dir, q, ok := parseLean(in)
		got := ""
		if ok {
			got = string(dir) + "/" + q
		}
		if got != want {
			t.Errorf("parseLean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadLeanBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leans.csv")
	csv := "run,ticker,run_date,lean,dossier_file_key,status\n" +
		"R1,AAA,2025-06-02,BUY (weak),revision,watchlist\n" +
		"R1,BBB,2025-06-02,none evident,round-3,watchlist\n" +
		"R1,CCC,2025-06-02,SELL (clear),revision,watchlist\n"
	if err := os.WriteFile(path, []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}
	leans, none, err := readLeanBackfill(path)
	if err != nil || len(leans) != 2 || none != 1 {
		t.Fatalf("got %d leans, %d non-directional, err %v; want 2, 1, nil", len(leans), none, err)
	}
	if leans[1].ticker != "CCC" || leans[1].direction != model.DirectionSell || leans[1].strength != "clear" {
		t.Errorf("second lean = %+v", leans[1])
	}
	if l, n, err := readLeanBackfill(filepath.Join(t.TempDir(), "absent.csv")); err != nil || l != nil || n != 0 {
		t.Errorf("a missing backfill must be an empty arm, got %v %d %v", l, n, err)
	}
}

// writeJSON writes v under dir/name, creating the directories.
func writeJSON(t *testing.T, dir, name string, v any) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// controlFixture is a runs directory over a synthetic market: twelve sp500
// names whose post-anchor drift rises with their pre-screen score, so the
// universe IC is exactly 1, and whose history before the anchor carries each
// name's own beta to ^GSPC.
type controlFixture struct {
	runsDir string
	prices  mapPrices
	dates   []string
}

const fixtureNames = 12

func fixtureTicker(i int) string { return fmt.Sprintf("N%02d", i) }

func newControlFixture(t *testing.T) *controlFixture {
	dates := sessions("2025-01-06", 320)
	prices := mapPrices{"^GSPC": synth("^GSPC", dates, benchClose)}
	for i := 0; i < fixtureNames; i++ {
		drift := float64(i-6) * 0.002 // N00 falls hardest, N11 rises most
		prices[fixtureTicker(i)] = synth(fixtureTicker(i), dates, func(k int) float64 {
			base := 100 * math.Pow(benchClose(k)/100, 1.5)
			if k > 150 {
				base *= math.Exp(drift * float64(k-150))
			}
			return base
		})
	}
	return &controlFixture{runsDir: t.TempDir(), prices: prices, dates: dates}
}

// addRun writes a run generated after session anchor, in the given mode.
func (f *controlFixture) addRun(t *testing.T, name string, anchor int, mode string,
	ideas []model.TradeIdea, shortlist []model.Candidate, leans map[string]any) {
	t.Helper()
	dir := filepath.Join(f.runsDir, name)
	gen := f.dates[anchor] + "T22:00:00Z"
	var rows []prescreenRow
	for i := 0; i < fixtureNames; i++ {
		tk := fixtureTicker(i)
		rows = append(rows, prescreenRow{Ticker: tk, Index: "sp500", Score: float64(i - 6),
			Close: closeOnOrBefore(f.prices[tk], f.dates[anchor])})
	}
	writeJSON(t, dir, "prescreen.json", prescreenFile{Rows: rows})
	writeJSON(t, dir, "ideas.json", model.IdeasResult{GeneratedAt: gen, ResearchMode: mode, Ideas: ideas})
	writeJSON(t, dir, "metadata.json", model.RunMeta{GeneratedAt: gen, ResearchMode: mode, Shortlist: shortlist})
	for tk, lean := range leans {
		rec := map[string]any{
			"candidate": map[string]any{"ticker": tk, "index": "sp500"},
			"dossier":   lean,
		}
		writeJSON(t, dir, fmt.Sprintf("data/research-%x.json", tk), rec)
	}
}

func TestUniverseICRanksEveryRowPerIndex(t *testing.T) {
	f := newControlFixture(t)
	f.addRun(t, "r1", 150, "", nil, nil, nil)
	f.addRun(t, "r1b", 150, "", nil, nil, nil) // same day: one ranking, not two
	f.addRun(t, "r2", 160, "", nil, nil, nil)
	f.addRun(t, "r3", 170, "", nil, nil, nil)
	f.addRun(t, "late", 312, "", nil, nil, nil) // window not elapsed yet
	rep, err := ControlWithOptions(context.Background(), f.runsDir, f.prices, ControlOptions{Horizon: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.UniverseIC) != 2 || rep.UniverseIC[0].HorizonDays != 10 || rep.UniverseIC[1].HorizonDays != 15 {
		t.Fatalf("want universe IC at 10 and 15 sessions, got %+v", rep.UniverseIC)
	}
	for _, u := range rep.UniverseIC {
		if u.Runs != 3 || u.Repeats != 1 || u.Pending != 1 {
			t.Errorf("h=%d: runs %d, repeats %d, pending %d; want 3, 1, 1", u.HorizonDays, u.Runs, u.Repeats, u.Pending)
		}
		if math.Abs(u.MeanIC-1) > 1e-9 {
			t.Errorf("h=%d: mean IC %v, want 1 — the drift is monotone in the score", u.HorizonDays, u.MeanIC)
		}
		if u.CI == nil || u.CI.Weeks != 3 {
			t.Errorf("h=%d: want an interval over three weeks, got %+v", u.HorizonDays, u.CI)
		}
		if u.PerRun[0].N != fixtureNames || u.PerRun[0].ByIndex["sp500"] != 1 {
			t.Errorf("h=%d: per-run %+v", u.HorizonDays, u.PerRun[0])
		}
	}
	if !strings.Contains(rep.FormatText(), "Universe IC") {
		t.Error("the text report does not carry the universe IC")
	}
}

// A US holiday run anchors to the same US session as the Friday before it
// while Europe has traded: the US ranking is a repeat, the European one a new
// observation, and the earlier run keeps both of its own. A Sunday run repeats
// Friday everywhere.
func TestDedupeRankingsKeysOnTheAnchorSessionPerIndex(t *testing.T) {
	rows := []prescreenRow{row("AAA", "sp500", 1, 10), row("BBB", "sp500", -1, 20), row("SAP.DE", "eu50", 1, 100)}
	friday := icRun{generatedAt: "2026-09-04T22:00:00Z", rows: rows}
	sunday := icRun{generatedAt: "2026-09-06T12:00:00Z", rows: rows}
	monday := icRun{generatedAt: "2026-09-07T18:00:00Z", rows: rows}
	session := func(index, date string) string {
		if date == "2026-09-07" && index == "eu50" {
			return date // Europe traded on US Labor Day
		}
		if date >= "2026-09-04" {
			return "2026-09-04"
		}
		return date
	}

	got, dropped := dedupeRankings([]icRun{monday, sunday, friday}, session) // newest first, as listed
	if dropped != 3 {
		t.Errorf("dropped %d rankings, want 3 (Sunday's two, Monday's sp500)", dropped)
	}
	if len(got) != 2 || got[0].generatedAt != friday.generatedAt || len(got[0].rows) != 3 {
		t.Fatalf("Friday should come first with all its rows, got %+v", got)
	}
	if len(got[1].rows) != 1 || got[1].rows[0].Index != "eu50" {
		t.Errorf("Monday should keep only its moved eu50 ranking, got %+v", got[1].rows)
	}
}

func TestThesisArmsScoreLeansAndStayOutOfShipped(t *testing.T) {
	f := newControlFixture(t)
	up, down := fixtureTicker(11), fixtureTicker(0)
	short := []model.Candidate{
		{Ticker: up, Index: "sp500", Bias: model.BiasBullish},
		{Ticker: down, Index: "sp500", Bias: model.BiasBullish}, // the scouts got this one wrong
	}
	legacyIdea := model.TradeIdea{Ticker: up, Index: "sp500", Direction: model.DirectionBuy,
		PriceAtGeneration: closeOnOrBefore(f.prices[up], f.dates[150])}
	thesisIdea := legacyIdea
	thesisIdea.Ticker = down
	thesisIdea.Direction = model.DirectionSell
	thesisIdea.PriceAtGeneration = closeOnOrBefore(f.prices[down], f.dates[160])

	f.addRun(t, "2025-legacy", 150, "", []model.TradeIdea{legacyIdea}, short, nil)
	f.addRun(t, "2025-thesis", 160, "thesis", []model.TradeIdea{thesisIdea}, short, map[string]any{
		up:   map[string]any{"lean": "BUY", "conviction": 4},
		down: map[string]any{"lean": "SELL", "conviction": 2},
		// A dossier written before the lean field existed is no call at all.
		fixtureTicker(5): map[string]any{"preferred_direction": "NONE"},
	})
	backfillCSV := filepath.Join(t.TempDir(), "leans.csv")
	if err := os.WriteFile(backfillCSV, []byte("run,ticker,run_date,lean\n"+
		"2025-thesis,"+down+",x,SELL (weak)\n"+
		"gone-run,"+up+","+f.dates[170]+",BUY (clear)\n"+
		"2025-thesis,"+up+",x,none evident\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := ControlWithOptions(context.Background(), f.runsDir, f.prices,
		ControlOptions{Horizon: 10, LeanBackfill: backfillCSV})
	if err != nil {
		t.Fatal(err)
	}
	arms := map[string]ControlArm{}
	for _, a := range rep.Arms {
		arms[a.Name] = a
	}
	if n := arms["shipped"].Record.N; n != 1 || arms["shipped"].Entries[0].Ticker != up {
		t.Errorf("legacy shipped arm = %+v, want only the legacy run's idea", arms["shipped"].Entries)
	}
	if n := arms["thesis"].Record.N; n != 1 || arms["thesis"].Entries[0].Ticker != down {
		t.Errorf("thesis arm = %+v, want only the thesis run's idea", arms["thesis"].Entries)
	}
	lean := arms["thesis-lean"]
	if lean.Record.N != 2 || lean.NonDirectional != 1 {
		t.Fatalf("thesis-lean n=%d, non-directional %d; want 2 and 1", lean.Record.N, lean.NonDirectional)
	}
	if lean.Record.ExcessHits != 2 {
		t.Errorf("both leans pointed the way the names moved, excess hits = %d", lean.Record.ExcessHits)
	}
	convictions := map[string]int{}
	for _, e := range lean.Entries {
		convictions[e.Ticker] = e.Conviction
	}
	if convictions[up] != 4 || convictions[down] != 2 {
		t.Errorf("convictions not recorded: %v", convictions)
	}
	bf := arms["thesis-lean-backfill"]
	if bf.Record.N != 2 || bf.NonDirectional != 1 {
		t.Fatalf("backfill n=%d, non-directional %d; want 2 and 1", bf.Record.N, bf.NonDirectional)
	}
	for _, e := range bf.Entries {
		if e.Ticker == down && e.PriceAtGen != closeOnOrBefore(f.prices[down], f.dates[160]) {
			t.Errorf("backfill anchored at %v, want the run's generation close", e.PriceAtGen)
		}
		if e.LeanStrength == "" {
			t.Errorf("backfill lean strength lost: %+v", e)
		}
	}

	// Every name carries beta 1.5 to ^GSPC before the anchor.
	for _, name := range []string{"shipped", "thesis-lean"} {
		h := arms[name].Hedged
		if h == nil || h.N != arms[name].Record.N || math.Abs(h.AvgBeta-1.5) > 0.01 {
			t.Errorf("%s hedged record = %+v, want beta 1.5 on every call", name, h)
		}
	}

	var sawLeanDiff bool
	for _, d := range rep.Diffs {
		if d.Over == "thesis-lean" && d.Under == "shortlist" {
			sawLeanDiff = true
		}
	}
	if !sawLeanDiff {
		t.Errorf("no thesis-lean − shortlist difference in %+v", rep.Diffs)
	}
	text := rep.FormatText()
	for _, want := range []string{"thesis-lean-backfill", "β-hedged", "Thesis research adds:"} {
		if !strings.Contains(text, want) {
			t.Errorf("text report lacks %q:\n%s", want, text)
		}
	}
}
