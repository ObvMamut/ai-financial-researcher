package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// TestBuildRevisionIsRecordedWhenBuiltWithGoBuild is A3's fakebin/hermetic
// assertion that metadata.json's build provenance is actually populated for a
// binary built the way an operator's `cfr` is: `go build`, not `go test`.
// model.CurrentBuildInfo returns zero values under `go test` in this module
// (runtime/debug.ReadBuildInfo carries no vcs.* settings for a test binary
// here) — asserting through Run() in-process, as the rest of the suite does,
// would only prove the field exists, not that it is ever non-empty in the one
// binary shape that matters. This builds the real cfr binary, runs it as a
// real subprocess against the hermetic fakebin CLIs and a fake Yahoo server
// (never the network, never a model), and reads metadata.json back.
//
// External endpoints the subprocess can reach, after HOME is pointed at an
// empty scratch dir below: none. internal/config.Load's only two config
// sources are filepath.Join(os.UserHomeDir(), ".config", "cfr", "config.toml")
// and "./cfr.toml" (cwd) — both resolve to nonexistent files here (the run's
// own cwd is also a fresh t.TempDir()), so no ambient key of any kind, keyed
// or not, reaches this process; the subprocess's env is an explicit list
// below, never inherited os.Environ(), so a key sitting in the actual shell's
// environment cannot leak in either. CFR_YAHOO_BASE covers both the chart and
// the news-search calls (single handler below), and also disables the
// separate cookie+crumb handshake the option chain would otherwise need
// (yahoocrumb.go's newYahooAuth sets skip=true whenever the base URL is
// rerouted, so the hardcoded https://fc.yahoo.com/ request is never made).
// CFR_SEC_BASE/CFR_AV_BASE/CFR_FRED_BASE/CFR_ALPACA_BASE are pointed at a
// closed local port rather than left unset: EDGAR, AlphaVantage, FRED and
// Alpaca are all unconfigured (no contact email, no keys) so every call site
// gates on Provider.Available() before making a request (pack.go:306/351,
// fred.go:63, alphavantage.go:102, edgarreports.go:76, prices.go's Alpaca
// routing) and none of these should ever be dialed — routing them at a closed
// port instead of a real host turns a future regression that bypasses that
// gate into an immediate connection-refused failure instead of a silent call
// to a live provider.
func TestBuildRevisionIsRecordedWhenBuiltWithGoBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a real cfr binary as a subprocess (~15-20s); skipped with -short")
	}
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	agy := filepath.Join(repoRoot, "testdata", "fakebin", "agy")
	claudeBin := filepath.Join(repoRoot, "testdata", "fakebin", "claude")
	agentsDir := filepath.Join(repoRoot, "agents")
	for _, p := range []string{agy, claudeBin, agentsDir} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("fixture missing: %v", err)
		}
	}

	binPath := filepath.Join(t.TempDir(), "cfr")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/cfr: %v\n%s", err, out)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/finance/search") {
			w.Write(fakeNews("AAPL"))
			return
		}
		w.Write(fakeChart("AAPL"))
	}))
	defer srv.Close()

	scratch := t.TempDir()
	// A fresh, empty HOME: internal/config.Load resolves the global config at
	// filepath.Join(os.UserHomeDir(), ".config", "cfr", "config.toml") before
	// it ever looks at cwd's ./cfr.toml, so the real $HOME would let this
	// subprocess pick up whatever the operator's actual ~/.config/cfr/
	// config.toml carries — a provider key there would then make this test
	// dial a live, possibly metered, provider on every `go test ./...`.
	// XDG_CONFIG_HOME is set for the same reason defensively; Load only reads
	// $HOME today (verified in internal/config/config.go), never
	// XDG_CONFIG_HOME, but setting it costs nothing and keeps this fixture
	// inert if that ever changes.
	fakeHome := t.TempDir()
	// A closed local port: never a real host, so a provider call that
	// bypasses its Available() gate fails fast instead of reaching a live
	// endpoint. See the endpoint inventory in the function doc comment above.
	const closedPort = "http://127.0.0.1:1"

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + fakeHome,
		"XDG_CONFIG_HOME=" + filepath.Join(fakeHome, ".config"),
		"CFR_CHEAP_ENGINE=gemini",
		"CFR_CHIEF_ENGINE=claude",
		"CFR_GEMINI_BIN=" + agy,
		"CFR_CLAUDE_BIN=" + claudeBin,
		"CFR_AGENTS_DIR=" + agentsDir,
		"CFR_YAHOO_BASE=" + srv.URL,
		"CFR_SEC_BASE=" + closedPort,
		"CFR_AV_BASE=" + closedPort,
		"CFR_FRED_BASE=" + closedPort,
		"CFR_ALPACA_BASE=" + closedPort,
		"CFR_FAKE_MODE=ok",
	}

	run := exec.Command(binPath, "run", "--ticker", "AAPL", "--json", "--quiet")
	run.Dir = scratch
	run.Env = env
	out, runErr := run.CombinedOutput()
	// 0 = complete, 3 = degraded — both still write metadata.json in full;
	// 1/2 mean the fixture itself is broken and there is nothing to read.
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); !ok || (exitErr.ExitCode() != 0 && exitErr.ExitCode() != 3) {
			t.Fatalf("cfr run --ticker AAPL failed: %v\n%s", runErr, out)
		}
	}

	runsDir := filepath.Join(scratch, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		t.Fatalf("no runs/ directory produced: %v\n%s", err, out)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) != 1 {
		t.Fatalf("expected exactly 1 run dir, got %v\n%s", dirs, out)
	}

	raw, err := os.ReadFile(filepath.Join(runsDir, dirs[0], "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta model.RunMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("metadata.json: %v", err)
	}

	if meta.BuildRevision == "" {
		t.Errorf("build_revision is empty in a go-build binary's metadata.json: %s", raw)
	}
	if meta.BuildDirty == nil {
		t.Errorf("build_dirty is absent in a go-build binary's metadata.json: %s", raw)
	}
	if !strings.Contains(string(raw), `"build_revision"`) {
		t.Errorf("metadata.json on disk carries no build_revision key at all:\n%s", raw)
	}
}

// fakeChart is a minimal two-year daily Yahoo chart response: enough bars,
// spread and volume for the pre-screen/quant metrics to compute without
// tripping the short-history or illiquidity exclusions. Unlike
// internal/orchestrator's own syntheticChart (unexported, and this is a
// different package), this only needs to carry one ticker through a
// single-stock run, so it skips that fixture's cross-name ranking concerns.
func fakeChart(symbol string) []byte {
	h := fnv.New32a()
	h.Write([]byte(symbol))
	rng := rand.New(rand.NewSource(int64(h.Sum32())))

	const bars = 400
	price := 180.0
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
		ts[bars-1-i] = dates[i].Unix()
	}
	for i := 0; i < bars; i++ {
		o := price
		price *= math.Exp(0.0005 + 0.01*rng.NormFloat64())
		open[i] = o
		closes[i] = price
		high[i] = math.Max(o, price) * 1.005
		low[i] = math.Min(o, price) * 0.995
		volume[i] = 3e6 + rng.Float64()*1e6
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

// fakeNews tags two stories to the ticker, matching the shape the news
// domain's coverage check needs to keep a score rather than strip it as
// ungrounded.
func fakeNews(symbol string) []byte {
	now := time.Now()
	item := func(title string, ageHours int) string {
		return fmt.Sprintf(`{"uuid":%q,"title":%q,"publisher":"Reuters","link":"https://example.test/x",
			"providerPublishTime":%d,"type":"STORY","relatedTickers":[%q]}`,
			title, title, now.Add(-time.Duration(ageHours)*time.Hour).Unix(), symbol)
	}
	return []byte(fmt.Sprintf(`{"news":[%s,%s],"quotes":[]}`,
		item(symbol+" reports a quarter ahead of guidance", 20),
		item(symbol+" names a new chief financial officer", 44)))
}
