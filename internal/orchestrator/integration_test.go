package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
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

// fakeYahoo serves the marketdata fixture for every chart request so Stage 1.5
// never touches the network. Registered via CFR_YAHOO_BASE.
func fakeYahoo(t *testing.T) {
	t.Helper()
	fixture, err := os.ReadFile("../marketdata/testdata/yahoo_chart_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)
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
	if got := len(complete.Ideas.Ideas); got != 5 {
		t.Fatalf("want 5 ideas, got %d", got)
	}
	if complete.Ideas.Ideas[0].Ticker != "NVDA" {
		t.Errorf("rank-1 ticker = %q, want NVDA", complete.Ideas.Ideas[0].Ticker)
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
		"quant.json", filepath.Join("prices", "NVDA.json"),
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
	if meta.Outcome != "complete" {
		t.Errorf("outcome = %q, want complete", meta.Outcome)
	}
	if meta.Warnings == nil {
		t.Error("metadata warnings is null, want []")
	}
	if len(meta.Domains) != 5 {
		t.Errorf("want 5 domain statuses, got %d", len(meta.Domains))
	}
	for _, d := range meta.Domains {
		if d.Status != model.StatusDone {
			t.Errorf("domain %s status = %s, want done", d.Domain, d.Status)
		}
		if d.Attempts < 1 {
			t.Errorf("domain %s attempts = %d, want ≥ 1", d.Domain, d.Attempts)
		}
		// The quant specialist always gets the computed pack when price data
		// is available; news/sentiment get the compact price context.
		if d.Domain == "quant" || d.Domain == "news" || d.Domain == "sentiment" {
			if !d.Grounded {
				t.Errorf("domain %s should be grounded via the quant pack", d.Domain)
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
	if readMeta(t, dir).Outcome != "complete" {
		t.Error("outcome != complete")
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
