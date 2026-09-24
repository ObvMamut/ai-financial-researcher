package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
	"github.com/mamut/claude-financial-researcher/internal/store"
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
		w.Header().Set("Content-Type", "application/json")
		// The news search shares this base URL with the chart endpoint and is
		// told apart by its path. Serving it matters to the pipeline test rather
		// than to the news provider's own tests: with only charts faked, the
		// news, fundamentals and sentiment domains had no verified data for any
		// name, enforcement deleted every score they wrote, and each shipped
		// idea's entire evidence was the price series — which the risk gate now
		// refuses (checkPriceOnlyEvidence). A hermetic run in which no domain
		// outside the price history can reach anything is not a small version of
		// a real run; it is the degenerate case the gate exists to reject.
		if strings.Contains(r.URL.Path, "/finance/search") {
			w.Write(syntheticNews(r.URL.Query().Get("q")))
			return
		}
		w.Write(syntheticChart(path.Base(r.URL.Path)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_YAHOO_BASE", srv.URL)
}

// syntheticNews reproduces the coverage asymmetry the live runs actually hit:
// the keyless search resolves a US symbol and tags its stories to it, and
// answers a foreign listing with stories tagged to nobody.
//
// Tagging is what decides coverage — the provider reads an untagged story as
// sector context rather than as coverage of the company — so this one
// distinction gives the hermetic pipeline both halves to work with. A US name
// reaches the news domain and can ship; a foreign one is left with the price
// series alone, which is the exact shape the evidence floor exists to refuse and
// the exact shape that put two unresearched EU names into a shipped book on
// 2026-09-04.
//
// A symbol carrying an exchange suffix stands in for the foreign case, which is
// how the universe distinguishes them everywhere else.
func syntheticNews(symbol string) []byte {
	if symbol == "" {
		return []byte(`{"news":[],"quotes":[]}`)
	}
	now := time.Now()
	item := func(title, publisher string, ageHours int, tagged bool) string {
		rel := ""
		if tagged {
			rel = fmt.Sprintf("%q", symbol)
		}
		return fmt.Sprintf(`{"uuid":%q,"title":%q,"publisher":%q,"link":"https://example.test/%s",
			"providerPublishTime":%d,"type":"STORY","relatedTickers":[%s]}`,
			title, title, publisher, url.PathEscape(title),
			now.Add(-time.Duration(ageHours)*time.Hour).Unix(), rel)
	}
	// Keep the fixture's foreign coverage gap explicit even after the bounded
	// mapped-ADR fallback: those issuer queries remain unresolved too.
	unresolvedADR := map[string]bool{"ASML": true, "SAP": true, "SNY": true, "TM": true, "SONY": true, "TSM": true, "BABA": true}
	tagged := !strings.Contains(symbol, ".") && !unresolvedADR[symbol]
	return []byte(fmt.Sprintf(`{"news":[%s,%s],"quotes":[]}`,
		item(symbol+" reports a quarter ahead of guidance", "Reuters", 20, tagged),
		item(symbol+" names a new chief financial officer", "Bloomberg", 44, tagged)))
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

	// The fake Chief ranks NVDA first, and enforcement deletes a score for a
	// name that never made the shortlist — so NVDA has to survive the merit trim
	// for the run to be testable end to end. It used to, on a random walk that
	// drifts *down* 0.06% a day; which names topped the composite was luck, and
	// reweighting the composite toward the 63-day term reshuffled it out.
	// Giving the one name the test depends on a deliberate uptrend makes the
	// fixture say what it means, and stops a change to the ranking formula
	// reading as a broken pipeline.
	if symbol == "NVDA" {
		drift, vol = 0.0025, 0.010
	}

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
		Mode: mode,
		// The legacy tests below were written against the Chief-selects
		// pipeline; merit_veto runs opt in explicitly (selection_test.go).
		Selection: model.SelectionChief,
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
	// Fundamentals and sentiment have no provider at all in the hermetic config.
	// News does — the fixture serves the keyless search — but only for US
	// symbols, so it is the mixed case: real coverage on some names and none on
	// the foreign ones, which is what a live run looks like.
	for _, d := range []string{"fundamentals", "sentiment"} {
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
	// The hashes identify the prompts; the set name says which A/B arm they
	// were. Without it a scoreboard comparison has nothing legible to group by.
	if meta.PersonaSet != "agents" {
		t.Errorf("persona set = %q, want the directory name the personas came from", meta.PersonaSet)
	}
	if _, ok := meta.PersonaSHA["README"]; ok {
		t.Error("a README was loaded as a persona")
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
		// Sentiment and fundamentals have no provider at all in the hermetic
		// config, so they must report themselves ungrounded rather than
		// borrowing the shared price context's credibility.
		if d.Domain == "sentiment" || d.Domain == "fundamentals" {
			if d.Grounded {
				t.Errorf("domain %s reported grounded with no provider data", d.Domain)
			}
			if len(d.Ungrounded) == 0 {
				t.Errorf("domain %s should list its ungrounded tickers", d.Domain)
			}
		}
		// News is the mixed case: the keyless search answers a US symbol and
		// leaves a foreign listing's stories untagged, so the domain is grounded
		// *and* still has names it could not reach. Both halves have to be true
		// at once, which is the state enforcement is actually written for.
		if d.Domain == "news" {
			if !d.Grounded {
				t.Errorf("domain news should be grounded — the fixture serves tagged headlines for US symbols")
			}
			if len(d.Ungrounded) == 0 {
				t.Errorf("domain news should still list the foreign listings it could not reach")
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

// fakeDeepSeekServer serves one canned OpenAI-compatible chat completion whose
// assistant content is the given chief-analyst-style markdown+JSON, standing
// in for the DeepSeek Chief Analyst fallback endpoint (internal/orchestrator's
// ChiefFallback config, resolved through the same provider-agnostic
// apiengine.go the cheap-research API/local engines already use).
func fakeDeepSeekServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]string{"role": "assistant", "content": content},
			}},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fallbackIdeasContent is a plausible chief-analyst-shaped response. Its exact
// levels are not tuned to survive the risk gate — the fallback tests assert the
// outer contract (fallback attempted, domain status, outcome, notes), not
// whether this invented geometry happens to clear every band.
const fallbackIdeasContent = "Synthesis reasoning via the DeepSeek fallback engine.\n\n" +
	"```json\n" +
	"{\n" +
	"  \"mode\": \"independent\",\n" +
	"  \"generated_at\": \"2026-07-18T00:00:00Z\",\n" +
	"  \"ideas\": [\n" +
	"    {\"rank\": 1, \"ticker\": \"NVDA\", \"direction\": \"BUY\", \"confidence\": 60,\n" +
	"     \"entry\": 100.0, \"stop\": 95.0, \"target\": 115.0,\n" +
	"     \"risk_reward\": 3.0, \"timeframe_days\": 15,\n" +
	"     \"position_note\": \"sized by the app\",\n" +
	"     \"why\": \"Fallback synthesis for NVDA.\"}\n" +
	"  ],\n" +
	"  \"notes\": \"Fallback synthesis.\"\n" +
	"}\n" +
	"```\n"

// assertFallbackFired checks the outer contract every successful-fallback run
// must satisfy, regardless of whether the invented levels above happen to
// survive the risk gate.
func assertFallbackFired(t *testing.T, complete *Event) {
	t.Helper()
	if complete == nil {
		t.Fatal("no EventComplete received")
	}
	if complete.Ideas == nil {
		t.Fatal("EventComplete has nil Ideas")
	}
	if complete.Meta.Outcome != "degraded" {
		t.Errorf("outcome = %q, want degraded", complete.Meta.Outcome)
	}
	if complete.Meta.SynthesisFallbackEngine != "deepseek-reasoner" {
		t.Errorf("SynthesisFallbackEngine = %q, want deepseek-reasoner", complete.Meta.SynthesisFallbackEngine)
	}
	found := false
	for _, d := range complete.Meta.Domains {
		if d.Domain == "chief-analyst-fallback" {
			found = true
			if d.Status != model.StatusDone {
				t.Errorf("chief-analyst-fallback status = %s, want done", d.Status)
			}
		}
	}
	if !found {
		t.Errorf("no chief-analyst-fallback domain status recorded: %+v", complete.Meta.Domains)
	}
	if !strings.Contains(complete.Ideas.Notes, "DeepSeek fallback") {
		t.Errorf("ideas.Notes = %q, want it to mention the fallback", complete.Ideas.Notes)
	}
}

// When the primary claude CLI call fails outright, the DeepSeek fallback must
// fire and produce a real, validated/risk-gated synthesis — sitting strictly
// before the mechanical buildDegradedIdeas path.
func TestChiefFailureFallsBackToDeepSeek(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "chief-fail")
	cfg := testConfig(t, model.ModeIndependent)
	srv := fakeDeepSeekServer(t, fallbackIdeasContent)
	cfg.ChiefFallback = model.APIConfig{BaseURL: srv.URL, Model: "deepseek-reasoner", APIKey: "sk-test"}

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	assertFallbackFired(t, complete)
}

// Same fallback path, triggered by the primary call succeeding but returning
// unparseable JSON rather than failing outright.
func TestChiefBadJSONFallsBackToDeepSeek(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "badjson")
	cfg := testConfig(t, model.ModeIndependent)
	srv := fakeDeepSeekServer(t, fallbackIdeasContent)
	cfg.ChiefFallback = model.APIConfig{BaseURL: srv.URL, Model: "deepseek-reasoner", APIKey: "sk-test"}

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	assertFallbackFired(t, complete)
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
	if _, ok := ps.Row("sp500", "NVDA"); !ok {
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

// TestLegacyAPIChiefCorrectiveRepromptEndToEnd closes a coverage gap left by
// Task 4: legacy x chief_engine=api was exercised only by a direct
// chiefTarget+runAgent sequence (TestChiefRoutingMatrix), never by the real
// pipeline's parseIdeas -> validateIdeas -> corrective re-prompt loop
// (orchestrator.go's run()). This drives that loop end to end with the Chief
// on the API engine: an httptest server stands in for the Chief endpoint, and
// — to reuse the existing, exercised off-base/corrective fixture logic rather
// than reimplementing sigma-derived entry/stop/target math in Go — its
// handler shells out to the same testdata/fakebin/claude script the CLI path
// uses, keyed to the same CFR_FAKE_MODE values other tests already rely on:
// "off-base" for the first (over-confident) pass, "ok" for the corrective
// pass once the prompt carries the "## Corrective pass" marker.
func TestLegacyAPIChiefCorrectiveRepromptEndToEnd(t *testing.T) {
	for _, mode := range []model.Mode{model.ModeSingle, model.ModeIndependent} {
		for _, engine := range []string{"claude", "api"} {
			t.Run(string(mode)+"/"+engine, func(t *testing.T) { testLegacyAPIChiefCorrective(t, mode, engine) })
		}
	}
}

func testLegacyAPIChiefCorrective(t *testing.T, runMode model.Mode, engine string) {
	claudeBin, err := filepath.Abs("../../testdata/fakebin/claude")
	if err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, runMode)
	cfg.Ticker = "NVDA"
	cfg.Indices = []string{"sp500"}
	cfg.ChiefAdjustBand = 10

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		prompt := ""
		if len(body.Messages) > 0 {
			prompt = body.Messages[0].Content
		}
		mode := "off-base"
		if strings.Contains(prompt, "## Corrective pass") {
			mode = "ok"
		}
		cmd := exec.Command(claudeBin, "-p", prompt)
		cmd.Env = append(os.Environ(), "CFR_FAKE_MODE="+mode)
		out, cmdErr := cmd.Output()
		if cmdErr != nil {
			t.Errorf("fake claude (standing in for the Chief's content) failed: %v", cmdErr)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(out)}}},
		})
	}))
	defer srv.Close()

	cfg.ChiefEngine = engine
	cfg.ChiefAPI = model.APIConfig{BaseURL: srv.URL, Model: "chief-model", APIKey: "chief-key"}

	if engine == "claude" {
		wrapper := filepath.Join(t.TempDir(), "chief-wrapper")
		script := "#!/bin/sh\ncase \"$*\" in\n*'## Corrective pass'*) export CFR_FAKE_MODE=ok ;;\n*) export CFR_FAKE_MODE=off-base ;;\nesac\nexec '" + claudeBin + "' \"$@\"\n"
		if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		cfg.Binaries[model.CLIClaude] = wrapper
	}
	complete, runErr, logs := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}
	if !strings.Contains(strings.Join(logs, "\n"), "corrective re-prompt") {
		t.Errorf("expected the corrective re-prompt loop to run on an API Chief response:\n%s", strings.Join(logs, "\n"))
	}
	if got := atomic.LoadInt32(&hits); engine == "api" && got != 2 {
		t.Fatalf("chief endpoint saw %d requests, want 2 (initial + corrective)", got)
	}
	if len(complete.Ideas.Ideas) == 0 {
		t.Fatal("expected ideas from the corrected API Chief pass")
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

// A corrective re-prompt that does not land must say so. When the second Chief
// call returned but its JSON did not parse there was no branch at all: no log,
// no warning, the status row still reading `status: done, attempts: 2`, and
// ideas.json announcing "after one corrective re-prompt" beside a book that was
// the uncorrected first pass. The run spent a full synthesis call and left no
// trace of having wasted it.
func TestADiscardedCorrectiveRepromptIsRecorded(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "corrective-badjson")
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

	if !strings.Contains(joined, "unparseable JSON") {
		t.Errorf("a discarded corrective pass left no trace in the log:\n%s", joined)
	}
	if !strings.Contains(strings.Join(complete.Meta.Warnings, "\n"), "uncorrected first pass") {
		t.Errorf("the run's own warnings do not say the book is uncorrected: %v", complete.Meta.Warnings)
	}
	var chief *model.DomainStatus
	for i := range complete.Meta.Domains {
		if complete.Meta.Domains[i].Domain == "chief-analyst" {
			chief = &complete.Meta.Domains[i]
		}
	}
	if chief == nil {
		t.Fatal("no chief-analyst status row")
	}
	if chief.Corrective != "unparseable" {
		t.Errorf("chief-analyst corrective = %q, want %q", chief.Corrective, "unparseable")
	}
	if chief.Attempts < 2 {
		t.Errorf("the second call must still be accounted for: attempts = %d", chief.Attempts)
	}
	// And the note the reader sees must not claim a correction that never
	// happened.
	if strings.Contains(complete.Ideas.Notes, "after one corrective re-prompt") {
		t.Errorf("notes claim a correction that did not land: %q", complete.Ideas.Notes)
	}
	if !strings.Contains(complete.Ideas.Notes, "did not land") {
		t.Errorf("notes do not say the correction failed: %q", complete.Ideas.Notes)
	}
}

// Every number the gate enforces used to be a preference in a persona, and a
// model asked for "usually 1–2σ" and "risk_reward ≥ 1.5 preferred" satisfies it
// at the cheapest edge of the band. Here the fake Chief does exactly that, and
// does it again when asked to fix it.
//
// The stop band and reward:risk floor bind on limit entries, which an operator
// can still select with risk.entry_type = "limit"; a market-on-open idea is
// covered by TestMarketOnOpenIdeasShipWithoutATarget.
func TestRiskGateRePromptsThenDropsUnsoundConstructions(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "bad-levels")
	cfg := testConfig(t, model.ModeIndependent)
	cfg.Indices = []string{"sp500"}
	cfg.Risk.EntryType = model.EntryLimit

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
// at risk, and the expectancy its geometry implies. Run under the limit entry
// type, the only one with a take-profit and therefore a breakeven hit rate.
func TestSurvivingIdeasArriveSized(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeIndependent)
	cfg.Indices = []string{"sp500"}
	cfg.Risk.EntryType = model.EntryLimit

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

// The default entry type: a hermetic headless run whose Chief writes what the
// persona now asks for — no target, and a stop nearer than the catastrophe
// floor — ships every idea as market-on-open, sized off a stop Go widened to
// 2σ√h from the verified close, with no target, no reward:risk and no
// breakeven hit rate, and without spending a corrective call on any of it.
func TestMarketOnOpenIdeasShipWithoutATarget(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "no-target")
	cfg := testConfig(t, model.ModeIndependent)
	cfg.Indices = []string{"sp500"}

	complete, runErr, logs := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil || len(complete.Ideas.Ideas) == 0 {
		t.Fatalf("no market-on-open idea survived the gate:\n%s", strings.Join(logs, "\n"))
	}
	for _, l := range logs {
		if strings.Contains(l, "reward:risk") || strings.Contains(l, "target") && strings.HasPrefix(l, "risk:") {
			t.Errorf("a target-less idea drew a target finding: %s", l)
		}
	}
	var pack quant.Pack
	if ok, err := store.ReadQuantPack(runDir(t, cfg.RunsDir), &pack); !ok || err != nil {
		t.Fatalf("quant pack: ok=%v err=%v", ok, err)
	}
	// Five are written; the fixture's XOM has no domain scores and falls to
	// the evidence floor, which is not an entry-policy question.
	if len(complete.Ideas.Ideas) < 3 {
		t.Errorf("only %d market-on-open ideas shipped", len(complete.Ideas.Ideas))
	}
	for _, idea := range complete.Ideas.Ideas {
		m := pack.ByTicker[idea.Ticker]
		if idea.EntryType != model.EntryMarketOnOpen {
			t.Errorf("%s entry_type = %q, want %q", idea.Ticker, idea.EntryType, model.EntryMarketOnOpen)
		}
		if idea.Target != 0 || idea.RiskReward != 0 || idea.BreakevenWinRate != 0 {
			t.Errorf("%s carries target geometry it never stated: %+v", idea.Ticker, idea)
		}
		if math.Abs(idea.Entry-m.LastClose) > 0.006 {
			t.Errorf("%s entry %.2f is not the verified close %.2f", idea.Ticker, idea.Entry, m.LastClose)
		}
		floor := 2 * m.SigmaDaily * math.Sqrt(15) * m.LastClose
		if dist := idea.Entry - idea.Stop; dist < floor-1e-9 || dist > floor+0.02 {
			t.Errorf("%s stop distance %.2f, want the 2σ√15 floor %.2f", idea.Ticker, dist, floor)
		}
		if idea.Shares <= 0 || idea.RiskAmount <= 0 || idea.RiskAmount > 500 {
			t.Errorf("%s is not sized off its stop: %+v", idea.Ticker, idea)
		}
	}
	ideasJSON := readFile(t, filepath.Join(runDir(t, cfg.RunsDir), "ideas.json"))
	if !strings.Contains(ideasJSON, `"entry_type": "market_on_open"`) {
		t.Errorf("ideas.json does not record the entry type:\n%s", ideasJSON)
	}
	if !strings.Contains(strings.Join(complete.Meta.Warnings, "\n"), "catastrophe-stop floor") {
		t.Errorf("the widened stop is not in the run's warnings: %v", complete.Meta.Warnings)
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
	// be blending in the measured −0.35R rather than running on the prior alone.
	if !anyLogContains(logs, "expectancy blends the simulation with the measured -0.35R") {
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

func TestBaselinePersonaArmStillRuns(t *testing.T) {
	// agents.v1 is the frozen control arm of the persona A/B. If it silently
	// stopped loading — a renamed role, a block the old prompts don't carry —
	// the comparison would quietly become a one-arm study that still printed a
	// number. This is the guard against that.
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeIndependent)
	dir, err := filepath.Abs("../../agents.v1")
	if err != nil {
		t.Fatal(err)
	}
	cfg.AgentsDir = dir
	cfg.Indices = []string{"sp500"}

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("baseline arm errored: %s", runErr.Message)
	}
	if complete == nil || complete.Ideas == nil || len(complete.Ideas.Ideas) == 0 {
		t.Fatal("baseline arm produced no ideas")
	}

	meta := readMeta(t, runDir(t, cfg.RunsDir))
	if meta.PersonaSet != "agents.v1" {
		t.Errorf("persona set = %q, want agents.v1 — the arms are indistinguishable", meta.PersonaSet)
	}
	// Both arms must carry the same roles, or they are not comparable.
	for _, role := range []string{"scout", "news", "fundamentals", "quant", "sentiment", "macro", "chief-analyst"} {
		if meta.PersonaSHA[role] == "" {
			t.Errorf("baseline arm has no %s persona", role)
		}
	}
}

// syntheticBar returns the date and close of the bar `back` weekday sessions
// before the end of a symbol's synthetic series. Seeding a past run needs a
// generation date the fake price server can actually replay from, and the
// series is generated rather than fixtured, so the test reads it the same way
// the replay will.
func syntheticBar(t *testing.T, symbol string, back int) (date string, close float64) {
	t.Helper()
	var resp struct {
		Chart struct {
			Result []struct {
				Timestamp  []int64 `json:"timestamp"`
				Indicators struct {
					Quote []struct {
						Close []float64 `json:"close"`
					} `json:"quote"`
				} `json:"indicators"`
			} `json:"result"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(syntheticChart(symbol), &resp); err != nil {
		t.Fatalf("synthetic chart for %s: %v", symbol, err)
	}
	res := resp.Chart.Result[0]
	i := len(res.Timestamp) - 1 - back
	if i < 0 {
		t.Fatalf("synthetic series for %s has only %d bars", symbol, len(res.Timestamp))
	}
	return time.Unix(res.Timestamp[i], 0).UTC().Format("2006-01-02"), res.Indicators.Quote[0].Close[i]
}

// seedClosedTrades writes past run directories whose ideas all replay to a
// closed outcome, so a hermetic run reaches the post-mortem's closed-trade
// threshold without a network or a fixture of real history.
//
// The levels are chosen to make the outcome arithmetic rather than luck: the
// entry sits above the price so the first session after generation fills it at
// the open, and the stop and target sit ~4σ away over the holding period, so
// every trade runs to the end of its window and closes as `expired`. That is a
// closed trade — which is all the attribution counts — and it is the only
// outcome a random-walk fixture can be relied on to produce.
func seedClosedTrades(t *testing.T, runsDir string, tickers []string) {
	t.Helper()
	const perRun = 4
	for i := 0; i < len(tickers); i += perRun {
		end := min(i+perRun, len(tickers))
		batch := tickers[i:end]

		dir := filepath.Join(runsDir, fmt.Sprintf("2026-01-%02dT10-00-00", (i/perRun)+1))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var genAt string
		ideas := &model.IdeasResult{Mode: "independent", Notes: "seeded"}
		for rank, tk := range batch {
			date, px := syntheticBar(t, tk, 40)
			if genAt == "" {
				genAt = date + "T21:00:00Z"
			}
			ideas.Ideas = append(ideas.Ideas, model.TradeIdea{
				Rank: rank + 1, Ticker: tk, Index: "sp500", Direction: model.DirectionBuy,
				Confidence: 60, BaseConfidence: 58, Consensus: 0.7,
				Why:               "Seeded past idea for the post-mortem's own record.",
				Entry:             px * 1.02,
				Stop:              px * 0.85,
				Target:            px * 1.30,
				RiskReward:        2.0,
				TimeframeDays:     15,
				PriceAtGeneration: px,
				DomainScores:      map[string]int{"quant": 6, "news": 4, "sentiment": -2},
			})
		}
		ideas.GeneratedAt = genAt
		data, err := json.MarshalIndent(ideas, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "ideas.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// newestRunDir is runDir's equivalent for a RunsDir that was seeded with past
// runs: the fresh run is the last one by name, because names are timestamps.
func newestRunDir(t *testing.T, runsDir string) string {
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
	if len(dirs) == 0 {
		t.Fatal("no run dirs")
	}
	sort.Strings(dirs)
	return filepath.Join(runsDir, dirs[len(dirs)-1])
}

func TestPostMortemLessonsAreEnforcedAndReachTheChief(t *testing.T) {
	// The post-mortem is the one agent whose subject is the pipeline itself,
	// which makes it the easiest output in the system to invent: nothing in a
	// sentence about "wide-stop shorts" is checkable by reading it. So the
	// hermetic fake deliberately returns one lesson that counts, one that
	// overstates its own sample, and one about a cell nobody counted — and this
	// test is the record that the first survives, the second is corrected, and
	// the third does not reach the Chief.
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeIndependent)
	cfg.DataDir = t.TempDir() // private: this test writes its own postmortem.json
	cfg.Indices = []string{"sp500"}

	seedClosedTrades(t, cfg.RunsDir, []string{
		"NVDA", "JPM", "NKE", "XOM", "AAPL", "MSFT", "AMD", "TSLA", "COST", "MRK", "AMGN", "KO",
	})

	complete, runErr, logs := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}

	dir := newestRunDir(t, cfg.RunsDir)
	report := readFile(t, filepath.Join(dir, "post-mortem.md"))
	if !strings.Contains(report, "Fake post-mortem") {
		t.Fatalf("the post-mortem stage did not run:\n%s\n%s", report, strings.Join(logs, "\n"))
	}

	pm := scoreboard.LoadPostMortem(cfg.DataDir)
	if pm == nil {
		t.Fatal("no post-mortem stored")
	}
	if pm.NClosed < scoreboard.MinClosedForPostMortem {
		t.Fatalf("post-mortem ran on %d closed trades, below the %d threshold",
			pm.NClosed, scoreboard.MinClosedForPostMortem)
	}

	// The fabricated cell is gone, and its deletion is on the record rather
	// than silent.
	for _, l := range pm.Lessons {
		if strings.Contains(l.Cell, "on-a-tuesday") {
			t.Errorf("a lesson naming an uncounted cell survived: %+v", l)
		}
	}
	if !anyContains(pm.Rejected, "on-a-tuesday") {
		t.Errorf("the uncounted cell was dropped without saying so: %v", pm.Rejected)
	}
	if !anyLogContains(logs, "post-mortem: dropped") {
		t.Errorf("nothing in the run log reports the deletion:\n%s", strings.Join(logs, "\n"))
	}

	// The lesson that names a real cell survives, and carries the table's own
	// count rather than the one it claimed.
	if len(pm.Lessons) == 0 {
		t.Fatal("every lesson was rejected — the fake's counted cell should have survived")
	}
	counted := pm.Lessons[0]
	if counted.N != 12 {
		t.Errorf("kept lesson reports n=%d, want the table's 12", counted.N)
	}
	if !anyContains(pm.Rejected, "claimed n=62") {
		t.Errorf("the overstated sample was corrected without saying so: %v", pm.Rejected)
	}

	// Weight suggestions are advisory: a real domain survives to the run log, a
	// made-up one does not survive at all.
	for _, w := range pm.WeightSuggestions {
		if w.Domain == "astrology" {
			t.Errorf("a weight suggestion for a domain that does not exist survived: %+v", w)
		}
	}
	if !anyLogContains(logs, "post-mortem suggests weighting sentiment down") {
		t.Errorf("the advisory weight suggestion never reached the run log:\n%s", strings.Join(logs, "\n"))
	}

	// And the surviving lessons are in front of the Chief, which is the only
	// reason to compute them.
	chief := readFile(t, filepath.Join(dir, "chief-analyst.md"))
	if !strings.Contains(chief, "saw-post-mortem") {
		t.Errorf("the lessons never reached the chief prompt:\n%s", chief)
	}

	// The deletion is visible in the run's own metadata, next to every other
	// domain's corrected scores.
	meta := readMeta(t, dir)
	var pmStatus *model.DomainStatus
	for i := range meta.Domains {
		if meta.Domains[i].Domain == "post-mortem" {
			pmStatus = &meta.Domains[i]
		}
	}
	if pmStatus == nil {
		t.Fatal("metadata.json has no post-mortem row")
	}
	if !anyContains(pmStatus.CorrectedScores, "on-a-tuesday") {
		t.Errorf("metadata.json does not record the deleted lesson: %v", pmStatus.CorrectedScores)
	}
}

func anyContains(ss []string, substr string) bool {
	for _, s := range ss {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// A spent AlphaVantage free tier is one fact about one key. It reached the
// 2026-09-01 run as an identical data_error per ticker, so a key with nothing
// left read like a handful of unlucky names. It must be one run-level warning,
// and the count must be visible in the log before the run spends five minutes
// rediscovering it.
func TestSpentAlphaVantageBudgetIsOneWarningNotAScatterOfTickerErrors(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeSingle)
	cfg.Ticker = "AAPL"
	// Its own data dir: the limiter state below is a fixture, and sharedDataDir
	// is read by every other test in this binary.
	cfg.DataDir = t.TempDir()
	cfg.Providers.AlphaVantageKey = "test-key-not-a-real-credential"

	// A key whose whole day is already gone, exactly as .data/limiter-alphavantage.json
	// stood at 24 of 25 on the morning of the incident.
	state, err := json.Marshal(map[string]any{"daily_count": 25, "last_reset": time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "limiter-alphavantage.json"), state, 0o644); err != nil {
		t.Fatal(err)
	}

	complete, runErr, logs := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}

	// 1. The count is announced at run start, before anything is spent on it.
	if !anyLogContains(logs, "AlphaVantage daily budget") {
		t.Errorf("the run never logged the remaining AlphaVantage budget:\n%s", strings.Join(logs, "\n"))
	}

	// 2. Exactly one run-level warning names the exhaustion.
	meta := readMeta(t, runDir(t, cfg.RunsDir))
	spent := 0
	for _, w := range meta.Warnings {
		if strings.Contains(w, "AlphaVantage") && strings.Contains(w, "budget") {
			spent++
		}
	}
	if spent != 1 {
		t.Errorf("got %d AlphaVantage budget warnings, want exactly 1: %v", spent, meta.Warnings)
	}

	// 3. The per-ticker detail is not thrown away — it stays in data_errors.
	if !anyContains(meta.DataErrors, "exhausted") {
		t.Errorf("the per-ticker detail was removed from data_errors: %v", meta.DataErrors)
	}
}

// No key configured is not a failure and must not warn: AlphaVantage is
// optional enrichment on top of the keyless sources.
func TestNoAlphaVantageKeyNeitherLogsNorWarnsAboutABudget(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeSingle)
	cfg.Ticker = "AAPL"

	complete, runErr, logs := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}
	if anyLogContains(logs, "AlphaVantage daily budget") {
		t.Error("a keyless run reported a budget it never had")
	}
	meta := readMeta(t, runDir(t, cfg.RunsDir))
	if anyContains(meta.Warnings, "AlphaVantage") {
		t.Errorf("a keyless run warned about AlphaVantage: %v", meta.Warnings)
	}
}

// fakeAlpaca stands up a bars endpoint and counts requests. Returns the
// counter and the largest symbol count seen in one call, which is what
// distinguishes a batched pre-screen from the per-ticker loop it replaces.
func fakeAlpaca(t *testing.T) (hits *atomic.Int64, widest *atomic.Int64) {
	t.Helper()
	hits, widest = &atomic.Int64{}, &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		syms := strings.Split(r.URL.Query().Get("symbols"), ",")
		if n := int64(len(syms)); n > widest.Load() {
			widest.Store(n)
		}
		var b strings.Builder
		b.WriteString(`{"bars":{`)
		for i, sym := range syms {
			if i > 0 {
				b.WriteString(",")
			}
			// Reuse the synthetic chart's shape: enough bars for quant to work.
			fmt.Fprintf(&b, `%q:[`, sym)
			for j := 0; j < 400; j++ {
				if j > 0 {
					b.WriteString(",")
				}
				d := time.Now().AddDate(0, 0, -(400 - j)).Format("2006-01-02")
				px := 100.0 + float64(j)/10
				fmt.Fprintf(&b, `{"t":"%sT05:00:00Z","o":%.2f,"h":%.2f,"l":%.2f,"c":%.2f,"v":50000000,"n":10,"vw":%.2f}`,
					d, px, px*1.01, px*0.99, px, px)
			}
			b.WriteString("]")
		}
		b.WriteString(`},"next_page_token":null}`)
		fmt.Fprint(w, b.String())
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_ALPACA_BASE", srv.URL)
	return hits, widest
}

// Yahoo's chart endpoint is one request per ticker, so a universe-wide
// pre-screen fires one per constituent — ~272 a run, which is the shape that
// got this host 429'd in the first place. Alpaca serves many symbols per
// request, and the pre-screen must actually use that: one batched warm-up,
// then the existing per-ticker loop served entirely from cache.
func TestPrescreenBatchesUSNamesThroughAlpaca(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok")
	hits, widest := fakeAlpaca(t)
	cfg := testConfig(t, model.ModeIndependent)
	cfg.DataDir = t.TempDir() // own cache: the batch must be observable
	cfg.Indices = []string{"nq100"}
	cfg.Providers.AlpacaKeyID = "test-id"
	cfg.Providers.AlpacaSecret = "test-secret"

	_, runErr, logs := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}

	if widest.Load() < 10 {
		t.Errorf("widest Alpaca call carried %d symbols — the pre-screen is still going one at a time", widest.Load())
	}
	// nq100 is 57 names. Anything near that many requests means no batching.
	if hits.Load() > 20 {
		t.Errorf("made %d Alpaca requests for a 57-name index; batching is not working", hits.Load())
	}
	if !anyLogContains(logs, "pre-screen") {
		t.Errorf("no pre-screen log line:\n%s", strings.Join(logs, "\n"))
	}
}

// The regression that matters most: a run with no Alpaca key must behave
// exactly as it did before Alpaca existed.
func TestNoAlpacaKeyLeavesThePricePathUnchanged(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok")
	hits, _ := fakeAlpaca(t)
	cfg := testConfig(t, model.ModeSingle)
	cfg.Ticker = "AAPL"

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil {
		t.Fatal("no EventComplete received")
	}
	if hits.Load() != 0 {
		t.Errorf("an unconfigured Alpaca was called %d time(s)", hits.Load())
	}
}
