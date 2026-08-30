package orchestrator

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// The integration tests run the full pipeline against the fake model CLIs in
// testdata/fakebin. The fakes branch on the persona header inside the -p prompt
// and emit canned reports, so no network or real model CLI is involved.

func fakeBinaries(t *testing.T) map[model.CLI]string {
	t.Helper()
	agy, err := filepath.Abs("../../testdata/fakebin/agy")
	if err != nil {
		t.Fatal(err)
	}
	claude, err := filepath.Abs("../../testdata/fakebin/claude")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{agy, claude} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("fake binary missing: %v", err)
		}
	}
	return map[model.CLI]string{model.CLIGemini: agy, model.CLIClaude: claude}
}

// fakeYahoo serves a synthetic two-year daily series per symbol so the whole
// pipeline runs without touching the network. Registered via CFR_YAHOO_BASE.
//
// It generates rather than replays a fixture because the universe-wide
// pre-screen ranks names against each other: one canned five-bar series for
// every symbol would leave every metric zero, every name excluded for short
// history, and the ranking untested. The path is seeded from the symbol, so a
// given ticker gets the same series on every run and the ranking is stable.
func fakeYahoo(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sym := path.Base(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Write(syntheticChart(sym))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)
}

// syntheticChart builds a Yahoo chart response for one symbol: 400 weekday bars
// ending at the most recent weekday, on a deterministic random walk whose drift
// and volatility are derived from the symbol. Dollar volume is well clear of the
// pre-screen's liquidity floor so exclusions are driven by the test, not by the
// fixture.
func syntheticChart(symbol string) []byte {
	h := fnv.New32a()
	h.Write([]byte(symbol))
	seed := int64(h.Sum32())
	rng := rand.New(rand.NewSource(seed))

	const bars = 400
	drift := (float64(seed%21) - 10) / 10000 // ±0.1% per day
	vol := 0.008 + float64(seed%7)/1000      // 0.8%–1.4% daily
	price := 50 + float64(seed%450)          // $50–$500

	// Walk back to the start date over weekdays, then forward again.
	day := time.Now().UTC().Truncate(24 * time.Hour)
	for day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		day = day.AddDate(0, 0, -1)
	}
	dates := make([]time.Time, 0, bars)
	for d := day; len(dates) < bars; d = d.AddDate(0, 0, -1) {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		dates = append(dates, d)
	}

	ts := make([]int64, bars)
	open := make([]float64, bars)
	high := make([]float64, bars)
	low := make([]float64, bars)
	closes := make([]float64, bars)
	volume := make([]float64, bars)
	for i := range dates {
		j := bars - 1 - i // oldest first
		ts[j] = dates[i].Unix()
	}
	for i := 0; i < bars; i++ {
		o := price
		price *= math.Exp(drift + vol*rng.NormFloat64())
		open[i] = o
		closes[i] = price
		high[i] = math.Max(o, price) * (1 + vol/2)
		low[i] = math.Min(o, price) * (1 - vol/2)
		volume[i] = 2e6 + rng.Float64()*1e6
	}

	type quote struct {
		Open   []float64 `json:"open"`
		High   []float64 `json:"high"`
		Low    []float64 `json:"low"`
		Close  []float64 `json:"close"`
		Volume []float64 `json:"volume"`
	}
	resp := map[string]any{"chart": map[string]any{"result": []map[string]any{{
		"timestamp": ts,
		"indicators": map[string]any{
			"quote":    []quote{{Open: open, High: high, Low: low, Close: closes, Volume: volume}},
			"adjclose": []map[string]any{{"adjclose": closes}},
		},
	}}}}
	out, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	return out
}

// sharedDataDir is one cache dir for the whole test binary so the daily-keyed
// Yahoo cache carries across tests and the rate limiter isn't paid repeatedly.
var sharedDataDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cfr-test-data")
	if err != nil {
		panic(err)
	}
	sharedDataDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func testConfig(t *testing.T, mode model.Mode) Config {
	t.Helper()
	fakeYahoo(t)
	agentsDir, err := filepath.Abs("../../agents")
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Mode:      mode,
		AgentsDir: agentsDir,
		RunsDir:   t.TempDir(),
		DataDir:   sharedDataDir,
		Timeouts: model.StageTimeouts{
			Screening: 30 * time.Second,
			Analysis:  30 * time.Second,
			Synthesis: 30 * time.Second,
		},
		Retry: model.RetryPolicy{
			MaxAttempts: 2,
			BaseDelay:   10 * time.Millisecond,
			MaxDelay:    20 * time.Millisecond,
		},
		Binaries: fakeBinaries(t),
	}
}

// drain collects every event until the channel closes and returns the terminal
// complete/error events (either may be nil).
func drain(t *testing.T, ch <-chan Event) (complete *Event, runErr *Event, logs []string) {
	t.Helper()
	deadline := time.After(2 * time.Minute)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return complete, runErr, logs
			}
			switch e.Type {
			case EventComplete:
				ev := e
				complete = &ev
			case EventError:
				ev := e
				runErr = &ev
			case EventLog:
				logs = append(logs, e.Message)
			}
		case <-deadline:
			t.Fatal("timed out draining event channel")
		}
	}
}

// runDir returns the single run directory created under RunsDir.
func runDir(t *testing.T, runsDir string) string {
	t.Helper()
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) != 1 {
		t.Fatalf("expected exactly 1 run dir, got %v", dirs)
	}
	return filepath.Join(runsDir, dirs[0])
}

func readMeta(t *testing.T, dir string) model.RunMeta {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta model.RunMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("metadata.json: %v", err)
	}
	return meta
}

func TestIndependentRun(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeIndependent)

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}
	if complete.Ideas == nil {
		t.Fatal("EventComplete has nil Ideas")
	}
	// Not "exactly 5". The risk gate drops constructions it cannot justify, and
	// shipping four is the intended outcome — an unsound idea is worse than a
	// missing one because it looks like the others.
	if got := len(complete.Ideas.Ideas); got < 1 || got > 5 {
		t.Fatalf("want between 1 and 5 ideas, got %d", got)
	}
	if complete.Ideas.Ideas[0].Ticker != "NVDA" {
		t.Errorf("rank-1 ticker = %q, want NVDA", complete.Ideas.Ideas[0].Ticker)
	}
	for i, idea := range complete.Ideas.Ideas {
		if idea.Rank != i+1 {
			t.Errorf("ranks not contiguous after the risk gate: %+v", complete.Ideas.Ideas)
			break
		}
	}

	// L2 trade mechanics must arrive parsed and consistent.
	first := complete.Ideas.Ideas[0]
	if first.Entry <= 0 || first.Stop <= 0 || first.Target <= 0 {
		t.Errorf("rank-1 idea missing trade levels: %+v", first)
	}
	if first.RiskReward <= 0 || first.TimeframeDays <= 0 {
		t.Errorf("rank-1 idea missing risk_reward/timeframe: %+v", first)
	}

	dir := runDir(t, cfg.RunsDir)
	wantFiles := []string{
		"scout-sp500.md", "scout-nq100.md", "scout-eu50.md", "scout-asia100.md",
		"news.md", "fundamentals.md", "quant.md", "sentiment.md", "macro.md",
		"chief-analyst.md",
		"shortlist.json", "ideas.json", "metadata.json",
		"quant.json", "prescreen.json", filepath.Join("prices", "NVDA.json"),
	}
	for _, f := range wantFiles {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing artifact %s: %v", f, err)
		}
	}

	var shortlist []model.Candidate
	data, err := os.ReadFile(filepath.Join(dir, "shortlist.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &shortlist); err != nil {
		t.Fatalf("shortlist.json: %v", err)
	}
	if len(shortlist) == 0 {
		t.Fatal("shortlist.json is empty")
	}
	if len(shortlist) > 12 {
		t.Errorf("shortlist has %d names, want ≤ 12", len(shortlist))
	}
	seen := map[string]bool{}
	for _, c := range shortlist {
		if seen[c.Ticker] {
			t.Errorf("duplicate ticker in shortlist: %s", c.Ticker)
		}
		seen[c.Ticker] = true
		if c.Index == "" {
			t.Errorf("candidate %s missing source index", c.Ticker)
		}
		// Identity comes from the universe row, not from the model, and the
		// sector travels with the name into every downstream prompt.
		if c.Sector == "" {
			t.Errorf("candidate %s missing sector — identity was not enriched from the universe", c.Ticker)
		}
		if c.Reason == "" {
			t.Errorf("candidate %s lost its scout reason on the way to the shortlist", c.Ticker)
		}
	}
	if !seen["NVDA"] {
		t.Error("shortlist missing NVDA")
	}
	// The fakes nominate ASML (nq100) and ASML.AS (eu50): the cross-listing
	// must collapse to the unsuffixed primary listing.
	if !seen["ASML"] || seen["ASML.AS"] {
		t.Errorf("cross-listing dedupe failed: ASML=%v ASML.AS=%v", seen["ASML"], seen["ASML.AS"])
	}

	meta := readMeta(t, dir)
	// The hermetic config configures no provider keys, so news, fundamentals
	// and sentiment have verified data for nothing. That is a degraded run: a
	// specialist writing from recollection alone must not be signed off as
	// complete just because its process exited 0.
	if meta.Outcome != "degraded" {
		t.Errorf("outcome = %q, want degraded (three domains have zero coverage)", meta.Outcome)
	}
	if meta.Warnings == nil {
		t.Error("metadata warnings is null, want []")
	}
	for _, d := range []string{"news", "fundamentals", "sentiment"} {
		want := d + ": no verified data for"
		found := false
		for _, w := range meta.Warnings {
			if strings.Contains(w, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("metadata warnings missing the zero-coverage reason for %s: %v", d, meta.Warnings)
		}
	}
	// Five specialists, four scouts and the chief analyst: every model call in
	// the run gets a row, so no wall time is unattributed.
	if len(meta.Domains) != 10 {
		t.Errorf("want 10 domain statuses (5 specialists + 4 scouts + chief), got %d", len(meta.Domains))
	}
	for _, want := range []string{"scout-sp500", "scout-nq100", "scout-eu50", "scout-asia100", "chief-analyst"} {
		found := false
		for _, d := range meta.Domains {
			if d.Domain == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no domain status row for %s", want)
		}
	}
	// Every stage's wall clock is recorded; 79% of a real run's time used to be
	// unattributed because the in-process stages had no timings at all.
	for _, want := range []string{"prescreen", "screening", "quant", "analysis", "synthesis"} {
		if _, ok := meta.Stages[want]; !ok {
			t.Errorf("no wall-clock entry for stage %s: %v", want, meta.Stages)
		}
	}
	if meta.Engine == "" {
		t.Error("metadata does not record which engine produced the run")
	}
	if len(meta.PersonaSHA) == 0 {
		t.Error("metadata does not record the persona hashes the run used")
	}
	for _, d := range meta.Domains {
		if d.Status != model.StatusDone {
			t.Errorf("domain %s status = %s, want done", d.Domain, d.Status)
		}
		if d.Attempts < 1 {
			t.Errorf("domain %s attempts = %d, want ≥ 1", d.Domain, d.Attempts)
		}
		// Grounded now means the domain had its own verified evidence. The
		// quant specialist gets the computed metrics pack, so it qualifies.
		if d.Domain == "quant" && !d.Grounded {
			t.Errorf("domain quant should be grounded via the computed metrics pack")
		}
		// News/sentiment/fundamentals have no provider keys in the hermetic
		// config, so they must report themselves ungrounded rather than
		// borrowing the shared price context's credibility.
		if d.Domain == "news" || d.Domain == "sentiment" || d.Domain == "fundamentals" {
			if d.Grounded {
				t.Errorf("domain %s reported grounded with no provider data", d.Domain)
			}
			if len(d.Ungrounded) == 0 {
				t.Errorf("domain %s should list its ungrounded tickers", d.Domain)
			}
		}
	}
}

func TestIndependentRunSubsetIndices(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeIndependent)
	cfg.Indices = []string{"eu50", "sp500"}

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}

	dir := runDir(t, cfg.RunsDir)
	for _, f := range []string{"scout-sp500.md", "scout-eu50.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing scout report %s: %v", f, err)
		}
	}
	for _, f := range []string{"scout-nq100.md", "scout-asia100.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("unexpected scout report %s for unselected index", f)
		}
	}

	meta := readMeta(t, dir)
	if len(meta.Indices) != 2 || meta.Indices[0] != "sp500" || meta.Indices[1] != "eu50" {
		t.Errorf("metadata indices = %v, want [sp500 eu50] (canonical order)", meta.Indices)
	}
}

func TestSingleStockRun(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeSingle)
	cfg.Ticker = "AAPL"

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}
	if got := len(complete.Ideas.Ideas); got != 1 {
		t.Fatalf("want 1 idea, got %d", got)
	}
	if complete.Ideas.Ideas[0].Ticker != "AAPL" {
		t.Errorf("ticker = %q, want AAPL", complete.Ideas.Ideas[0].Ticker)
	}

	dir := runDir(t, cfg.RunsDir)
	// No scouts in single-stock mode.
	if _, err := os.Stat(filepath.Join(dir, "scout-sp500.md")); err == nil {
		t.Error("scout report present in single-stock mode")
	}
	// Same as the independent run: no provider keys means no verified news,
	// fundamentals or sentiment, so the run is honest about being degraded.
	if got := readMeta(t, dir).Outcome; got != "degraded" {
		t.Errorf("outcome = %q, want degraded (domains with zero coverage)", got)
	}
}

func TestChiefBadJSONDegrades(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "badjson")
	cfg := testConfig(t, model.ModeIndependent)

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}
	if complete.Ideas == nil {
		t.Fatal("EventComplete has nil Ideas")
	}
	assertDegradedIdeas(t, complete.Ideas)
	if readMeta(t, runDir(t, cfg.RunsDir)).Outcome != "degraded" {
		t.Error("outcome != degraded after chief bad JSON")
	}
}

func TestChiefFailureDegrades(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "chief-fail")
	cfg := testConfig(t, model.ModeIndependent)

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}
	assertDegradedIdeas(t, complete.Ideas)
	if readMeta(t, runDir(t, cfg.RunsDir)).Outcome != "degraded" {
		t.Error("outcome != degraded after chief failure")
	}
}

// assertDegradedIdeas checks the mechanical fallback produced real, capped ideas
// from the specialist score tails instead of an empty list.
func assertDegradedIdeas(t *testing.T, ideas *model.IdeasResult) {
	t.Helper()
	if len(ideas.Ideas) == 0 {
		t.Fatal("degraded fallback produced no ideas despite parseable specialist scores")
	}
	for _, idea := range ideas.Ideas {
		if idea.Confidence > 55 {
			t.Errorf("%s: degraded confidence %d exceeds cap 55", idea.Ticker, idea.Confidence)
		}
		if idea.Direction != model.DirectionBuy && idea.Direction != model.DirectionSell {
			t.Errorf("%s: invalid direction %q", idea.Ticker, idea.Direction)
		}
	}
	for _, idea := range ideas.Ideas {
		if idea.Ticker == "NKE" && idea.Direction != model.DirectionSell {
			t.Errorf("NKE should rank as SELL in degraded mode, got %s", idea.Direction)
		}
	}
}

func TestAllScoutsEmptyErrors(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "scout-empty")
	cfg := testConfig(t, model.ModeIndependent)

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr == nil {
		t.Fatal("expected EventError when all scouts return nothing")
	}
	if complete != nil {
		t.Fatal("unexpected EventComplete alongside scout failure")
	}
}

func TestAllSpecialistsFailErrors(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "spec-fail")
	cfg := testConfig(t, model.ModeIndependent)

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr == nil {
		t.Fatal("expected EventError when <2 specialist reports succeed")
	}
	if complete != nil {
		t.Fatal("unexpected EventComplete alongside specialist failure")
	}
}

// End to end: a specialist that scores an off-shortlist ticker and claims
// `"missing": []` must reach synthesis with those scores gone and the
// correction recorded. This is the 2026-08-28 failure mode — every specialist
// declared full coverage against near-zero real coverage and the run shipped as
// `outcome: complete` with the highest confidence of any recent run.
func TestIndependentRunEnforcesCoverage(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "overclaim")
	cfg := testConfig(t, model.ModeIndependent)

	complete, runErr, logs := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("run error: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no completion event")
	}

	dir := runDir(t, cfg.RunsDir)
	meta := readMeta(t, dir)

	sawCorrection, sawOffShortlist := false, false
	for _, d := range meta.Domains {
		if len(d.CorrectedScores) > 0 {
			sawCorrection = true
		}
		for _, tk := range d.OffShortlistScores {
			if tk == "ZZZZ" {
				sawOffShortlist = true
			}
		}
	}
	if !sawOffShortlist {
		t.Errorf("ZZZZ was never on the shortlist and must be recorded as an off-shortlist score: %+v", meta.Domains)
	}
	if !sawCorrection {
		t.Errorf("no domain recorded a corrected score, but every domain was ungrounded: %+v", meta.Domains)
	}

	// The artifact on disk must be what the Chief Analyst read, so the report
	// itself carries the corrected tail — not just the metadata.
	report, err := os.ReadFile(filepath.Join(dir, "news.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(report), `"ZZZZ"`) {
		t.Errorf("news.md still scores ZZZZ:\n%s", report)
	}

	if !containsLog(logs, "not on the shortlist") {
		t.Errorf("the run log should say a ticker was scored off-shortlist: %v", logs)
	}
}

// A report with no parseable JSON tail is a refusal or a truncation. It used to
// pass as "done" on non-empty stdout alone and flow silently into synthesis.
func TestIndependentRunFailsDomainWithNoJSONTail(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "no-tail")
	cfg := testConfig(t, model.ModeIndependent)

	_, runErr, _ := drain(t, Run(context.Background(), cfg))

	// All five specialists produce untailed prose, so fewer than two usable
	// reports survive and the run refuses to synthesise from nothing.
	if runErr == nil {
		t.Fatal("a run whose every specialist returned untailed prose must not synthesise")
	}
	if !strings.Contains(runErr.Message, "fewer than 2 specialist reports") {
		t.Errorf("run error = %q, want the too-few-reports gate", runErr.Message)
	}
}

func containsLog(logs []string, sub string) bool {
	for _, l := range logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// Stage 0.5 ranks the whole selected universe before any model is called, and
// the scouts screen that ranking rather than a bare ticker list. Without it the
// scouts nominated on familiarity and invented the "recent breakout" that
// justified each pick.
func TestPrescreenFeedsTheScouts(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeIndependent)
	cfg.Indices = []string{"sp500", "nq100"}

	complete, runErr, logs := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}
	dir := runDir(t, cfg.RunsDir)

	var ps Prescreen
	data, err := os.ReadFile(filepath.Join(dir, "prescreen.json"))
	if err != nil {
		t.Fatalf("prescreen.json: %v", err)
	}
	if err := json.Unmarshal(data, &ps); err != nil {
		t.Fatalf("prescreen.json: %v", err)
	}
	if ps.Params.Formula == "" {
		t.Error("prescreen.json does not record the formula its scores came from")
	}
	// Every constituent of both selected indices is accounted for, and nothing
	// from an index that was not selected leaked in.
	uni, err := universe.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := len(uni.Constituents("sp500")) + len(uni.Constituents("nq100"))
	if len(ps.Rows) != want {
		t.Errorf("prescreen has %d rows, want one per constituent of the selected indices (%d)", len(ps.Rows), want)
	}
	for _, r := range ps.Rows {
		if r.Index != "sp500" && r.Index != "nq100" {
			t.Fatalf("row from an unselected index: %+v", r)
		}
	}
	// Rows are ranked, with the excluded ones last.
	sawExcluded := false
	for i, r := range ps.Rows {
		if r.Excluded != "" {
			sawExcluded = true
			continue
		}
		if sawExcluded {
			t.Fatalf("scorable row %s at position %d follows an excluded one", r.Ticker, i)
		}
	}
	if _, ok := ps.Row("NVDA"); !ok {
		t.Error("prescreen has no row for NVDA")
	}

	// The table reached the model: the fake scout echoes a marker when its
	// prompt carries one.
	for _, idx := range cfg.Indices {
		report := readFile(t, filepath.Join(dir, "scout-"+idx+".md"))
		if !strings.Contains(report, "saw-prescreen-table") {
			t.Errorf("scout-%s ran without the pre-screen table in its prompt", idx)
		}
	}

	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "Stage 0.5") {
		t.Errorf("run log never mentions the pre-screen stage:\n%s", joined)
	}
	if !strings.Contains(joined, "merit") {
		t.Errorf("run log does not attribute the shortlist to the pre-screen merit:\n%s", joined)
	}
}

// A scout nomination that is not in the index it was handed is a hallucinated
// symbol. It used to reach the shortlist and consume a data-provider slot and an
// analysis slot on a company the run had never screened.
func TestOffUniverseNominationsNeverReachTheShortlist(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeIndependent)
	cfg.Indices = []string{"sp500"}

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}
	uni, err := universe.Load()
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, c := range uni.Constituents("sp500") {
		known[strings.ToUpper(c.Ticker)] = true
	}
	for _, c := range complete.Meta.Shortlist {
		if !known[strings.ToUpper(c.Ticker)] {
			t.Errorf("shortlisted %s is not in the index that was screened", c.Ticker)
		}
	}
}

// Confidence used to be whatever the Chief asserted. The base score is computed
// from the same reports the Chief read, so a number scored against a different
// thesis than the domains reported is now caught and corrected.
func TestChiefConfidenceIsAnchoredToTheComputedBase(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "off-base")
	cfg := testConfig(t, model.ModeIndependent)
	cfg.Indices = []string{"sp500"}
	cfg.ChiefAdjustBand = 10 // stated, not defaulted, so the assertions can use it

	complete, runErr, logs := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}

	byTicker := map[string]model.TradeIdea{}
	for _, idea := range complete.Ideas.Ideas {
		byTicker[idea.Ticker] = idea
	}
	nvda, ok := byTicker["NVDA"]
	if !ok {
		t.Fatalf("NVDA missing from ideas %+v", complete.Ideas.Ideas)
	}
	// This run has no provider keys, so coverage enforcement leaves only the
	// computed quant domain standing: 35% of the weight, which caps the base at
	// 40. The fake chief asserted 99. A run that can only see one domain does
	// not get to be certain, and that is now arithmetic rather than a request.
	if nvda.Confidence > nvda.BaseConfidence+cfg.ChiefAdjustBand {
		t.Errorf("NVDA confidence %d exceeds base %d + band %d",
			nvda.Confidence, nvda.BaseConfidence, cfg.ChiefAdjustBand)
	}
	if nvda.DomainScores["quant"] != 8 {
		t.Errorf("the per-domain scores behind the base must travel with the idea, got %v", nvda.DomainScores)
	}

	// NKE is bearish in every domain, so a BUY on it starts from zero.
	if nke, ok := byTicker["NKE"]; ok && nke.Direction == model.DirectionBuy {
		if nke.Confidence > cfg.ChiefAdjustBand {
			t.Errorf("a BUY against five bearish domains scored %d, want at most the band %d",
				nke.Confidence, cfg.ChiefAdjustBand)
		}
	}

	joined := strings.Join(complete.Meta.Warnings, "\n")
	if !strings.Contains(joined, "outside the computed base") {
		t.Errorf("clamping a confidence must be recorded in the run's warnings, got %v", complete.Meta.Warnings)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "corrective re-prompt") {
		t.Errorf("a confidence far outside the band should have triggered one re-prompt:\n%s", strings.Join(logs, "\n"))
	}
	// The Chief was actually shown the arithmetic it is being held to.
	ideasJSON := readFile(t, filepath.Join(runDir(t, cfg.RunsDir), "ideas.json"))
	if !strings.Contains(ideasJSON, "base_confidence") || !strings.Contains(ideasJSON, "domain_scores") {
		t.Errorf("ideas.json should record the base and per-domain scores each idea was anchored to:\n%s", ideasJSON)
	}
}

// Each specialist is written against a computed block. Asserting the role ran
// says nothing about whether it was given the evidence its persona is built on.
func TestSpecialistsReceiveTheComputedBlocksTheirPersonasAssume(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeIndependent)
	cfg.Indices = []string{"sp500"}

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}
	dir := runDir(t, cfg.RunsDir)

	// Macro's question at this horizon is the market regime, and the benchmark
	// prices answer it. It used to be asked whether the backdrop supported a
	// trade while being shown four FRED series and no market prices at all.
	if got := readFile(t, filepath.Join(dir, "macro.md")); !strings.Contains(got, "saw-regime-block") {
		t.Errorf("macro ran without the computed market regime in its prompt")
	}
	// Fundamentals cannot say anything about a multiple without a price.
	if got := readFile(t, filepath.Join(dir, "fundamentals.md")); !strings.Contains(got, "saw-price-context") {
		t.Errorf("fundamentals ran without the verified price context in its prompt")
	}

	// The regime also reaches the Chief, which weighs a macro report at 10% of
	// the score and previously had no market-level price to check it against.
	var pack quant.Pack
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "quant.json"))), &pack); err != nil {
		t.Fatalf("quant.json: %v", err)
	}
	if len(pack.Benchmarks) == 0 {
		t.Errorf("quant.json records no benchmark metrics; the series were fetched and discarded")
	}
	if pack.RegimeBlock() == "" {
		t.Errorf("benchmarks present but no regime block renders")
	}
}

// Every number the gate enforces used to be a preference in a persona, and a
// model asked for "usually 1–2σ" and "risk_reward ≥ 1.5 preferred" satisfies it
// at the cheapest edge of the band. Here the fake Chief does exactly that, and
// does it again when asked to fix it.
func TestRiskGateRePromptsThenDropsUnsoundConstructions(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "bad-levels")
	cfg := testConfig(t, model.ModeIndependent)
	cfg.Indices = []string{"sp500"}

	complete, runErr, logs := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}
	joined := strings.Join(logs, "\n")

	if !strings.Contains(joined, "corrective re-prompt") {
		t.Errorf("a 0.9σ stop at reward:risk 1.4 must buy one corrective call:\n%s", joined)
	}
	if !strings.Contains(joined, "Risk gate dropped") {
		t.Errorf("an unfixed construction must be dropped, not shipped:\n%s", joined)
	}
	if len(complete.Ideas.Ideas) != 0 {
		t.Errorf("every idea here is unsound; got %d survivors: %+v",
			len(complete.Ideas.Ideas), complete.Ideas.Ideas)
	}
	if !strings.Contains(complete.Ideas.Notes, "Risk gate dropped") {
		t.Errorf("notes must say why the list is short: %q", complete.Ideas.Notes)
	}
	// The reason travels into the run's own warnings, not only the log.
	if !strings.Contains(strings.Join(complete.Meta.Warnings, "\n"), "risk gate:") {
		t.Errorf("gate findings missing from metadata warnings: %v", complete.Meta.Warnings)
	}
}

// An idea that survives arrives sized: a share count, a notional, the currency
// at risk, and the expectancy its geometry implies.
func TestSurvivingIdeasArriveSized(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeIndependent)
	cfg.Indices = []string{"sp500"}

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil || len(complete.Ideas.Ideas) == 0 {
		t.Fatal("no ideas survived the gate")
	}
	for _, idea := range complete.Ideas.Ideas {
		if idea.Shares <= 0 || idea.Notional <= 0 || idea.RiskAmount <= 0 {
			t.Errorf("%s is not sized: %+v", idea.Ticker, idea)
		}
		// $100k equity at 0.5% risk is $500 a trade; rounding down to whole
		// shares can only ever land under it.
		if idea.RiskAmount > 500 {
			t.Errorf("%s risks %.2f, above the $500 per-trade budget", idea.Ticker, idea.RiskAmount)
		}
		if idea.BreakevenWinRate <= 0 || idea.BreakevenWinRate >= 1 {
			t.Errorf("%s breakeven win rate = %v", idea.Ticker, idea.BreakevenWinRate)
		}
	}
	ideasJSON := readFile(t, filepath.Join(runDir(t, cfg.RunsDir), "ideas.json"))
	for _, want := range []string{"shares", "notional", "risk_amount", "expectancy_bps", "breakeven_win_rate"} {
		if !strings.Contains(ideasJSON, want) {
			t.Errorf("ideas.json missing %q:\n%s", want, ideasJSON)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestTrackRecordReachesTheChiefAndTheRiskGate(t *testing.T) {
	// The feedback loop is only real if the measured record leaves the JSON
	// file and lands in the prompt the Chief actually reads.
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeIndependent)
	cfg.DataDir = t.TempDir() // a private cache: this test writes a calibration into it

	cal := &scoreboard.Calibration{
		ComputedAt: time.Now().UTC().Format(time.RFC3339),
		NClosed:    31, WinRate: 0.42, AvgPnLPct: 0.8, AvgR: -0.35, AvgBarsHeld: 9,
		Domains: map[string]scoreboard.Bucket{
			"quant": {N: 20, Wins: 11, WinRate: 0.55, AvgR: 0.30},
			"macro": {N: 14, Wins: 4, WinRate: 0.29, AvgR: -0.60},
		},
		Confidence: map[string]scoreboard.Bucket{"60-79": {N: 18, Wins: 8, WinRate: 0.44, AvgR: -0.10}},
	}
	if err := cal.Save(cfg.DataDir); err != nil {
		t.Fatalf("seed calibration: %v", err)
	}

	complete, runErr, logs := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}

	dir := runDir(t, cfg.RunsDir)
	report := readFile(t, filepath.Join(dir, "chief-analyst.md"))
	if !strings.Contains(report, "saw-track-record") {
		t.Errorf("the track record never reached the chief prompt:\n%s", report)
	}

	// And a copy travels with the run, so a past decision can be read against
	// the record that was in front of it.
	if _, err := os.Stat(filepath.Join(dir, scoreboard.CalibrationFile)); err != nil {
		t.Errorf("no calibration copy stored with the run: %v", err)
	}

	// 31 closed trades is past MinClosedForEdge, so the expectancy check must
	// be running on the measured −0.35R rather than the assumed prior.
	if !anyLogContains(logs, "expectancy assumes the measured -0.35R edge") {
		t.Errorf("the risk gate kept its prior despite a usable record:\n%s", strings.Join(logs, "\n"))
	}
}

func anyLogContains(logs []string, substr string) bool {
	for _, l := range logs {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}
