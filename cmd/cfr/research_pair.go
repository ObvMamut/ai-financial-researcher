package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/config"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/orchestrator"
	"github.com/mamut/claude-financial-researcher/internal/redact"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
	"github.com/mamut/claude-financial-researcher/internal/store"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

type pairRegistration struct {
	RoundTripCostBPS    *float64          `json:"assumed_round_trip_cost_bps,omitempty"`
	Version             int               `json:"version"`
	RegisteredAt        time.Time         `json:"registered_at"`
	Request             model.RunRequest  `json:"request"`
	Order               []string          `json:"arm_order"`
	Horizons            []int             `json:"horizons_sessions"`
	Scope               string            `json:"scope"`
	Settings            json.RawMessage   `json:"settings_without_credentials"`
	Files               map[string]string `json:"input_sha256"`
	AlphaVantageOmitted bool              `json:"alphavantage_omitted"`
}
type pairArmState struct {
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at,omitzero"`
	CompletedAt time.Time `json:"completed_at,omitzero"`
	Error       string    `json:"error,omitempty"`
}
type pairState struct {
	Status         string                  `json:"status"`
	Error          string                  `json:"error,omitempty"`
	AsOf           time.Time               `json:"as_of,omitzero"`
	SnapshotSHA256 string                  `json:"snapshot_sha256,omitempty"`
	Arms           map[string]pairArmState `json:"arms"`
}

func runResearchPair(settings *config.Settings, args []string) int {
	fs := flag.NewFlagSet("research-pair", flag.ContinueOnError)
	out := fs.String("out", "", "new experiment directory (must not exist)")
	ticker := fs.String("ticker", "", "single-stock paired evaluation")
	indices := fs.String("indices", "", "independent evaluation index keys; empty uses configured universe")
	evaluate := fs.String("evaluate", "", "evaluate an existing experiment without rerunning models")
	refresh := fs.Bool("refresh", false, "collect and retain outcome prices when evaluating")
	costText := fs.String("cost-bps", "", "register an assumed round-trip execution cost in basis points")
	omitAV := fs.Bool("omit-alphavantage", false, "exclude Alpha Vantage from collection; record this choice")
	limit := fs.Duration("timeout", 60*time.Minute, "total collection and paired execution deadline")
	if fs.Parse(args) != nil {
		return 2
	}
	if fs.NArg() != 0 || *limit <= 0 || (*evaluate == "" && (*out == "" || *refresh)) || (*evaluate != "" && (*out != "" || *ticker != "" || *indices != "" || *omitAV || *costText != "")) || (*ticker != "" && *indices != "") {
		fmt.Fprintln(os.Stderr, "choose --out with optional --ticker/--indices, or --evaluate with optional --refresh")
		return 2
	}
	var cost *float64
	if *costText != "" {
		value, err := strconv.ParseFloat(*costText, 64)
		if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			fmt.Fprintln(os.Stderr, "cost-bps must be finite and nonnegative")
			return 2
		}
		cost = &value
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *limit)
	defer cancel()
	if *evaluate != "" {
		if err := evaluateResearchPair(ctx, settings, *evaluate, *refresh); err != nil {
			fmt.Fprintln(os.Stderr, redact.String(err.Error()))
			return 1
		}
		return 0
	}
	req := model.RunRequest{Mode: model.ModeIndependent, Indices: append([]string(nil), settings.Indices...)}
	if *ticker != "" {
		req.Mode = model.ModeSingle
		req.Ticker = strings.ToUpper(strings.TrimSpace(*ticker))
		req.Indices = nil
	}
	if *indices != "" {
		req.Indices = nil
		for _, idx := range strings.Split(*indices, ",") {
			req.Indices = append(req.Indices, strings.ToLower(strings.TrimSpace(idx)))
		}
	}
	copy := *settings
	if *omitAV {
		copy.Providers.AlphaVantageKey = ""
	}
	code, err := collectAndRunPair(ctx, &copy, req, *out, *omitAV, cost, marketdata.CaptureResearchSnapshot, orchestrator.Run)
	if err != nil {
		fmt.Fprintln(os.Stderr, redact.String(err.Error()))
		return 1
	}
	fmt.Printf("Paired artifacts: %s\nEvaluate later: cfr research-pair --evaluate %s --refresh\n", *out, *out)
	return code
}

type pairCapture func(context.Context, marketdata.SnapshotCaptureConfig) (*marketdata.ResearchSnapshot, error)
type pairRunner func(context.Context, orchestrator.Config) <-chan orchestrator.Event

func collectAndRunPair(ctx context.Context, settings *config.Settings, req model.RunRequest, dir string, omitAV bool, cost *float64, capture pairCapture, run pairRunner) (code int, err error) {
	if settings.CheapEngine != "api" && settings.CheapEngine != "local" {
		return 1, fmt.Errorf("frozen evaluation requires the configured API/local cheap engine; CLI browsing cannot be frozen")
	}
	if settings.CheapEngine == "api" && (settings.API.APIKey == "" || settings.API.Model == "") {
		return 1, fmt.Errorf("configure the existing API research engine before collecting a pair")
	}
	if settings.CheapEngine == "local" && settings.Local.Model == "" {
		return 1, fmt.Errorf("configure the existing local model before collecting a pair")
	}
	binary := settings.Binaries[model.CLIClaude]
	if binary == "" {
		binary = "claude"
	}
	if _, e := exec.LookPath(binary); e != nil {
		return 1, fmt.Errorf("Claude CLI unavailable: %w", e)
	}
	tickers, benchmarks, req, err := pairUniverse(req)
	if err != nil {
		return 1, err
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return 1, fmt.Errorf("reserve experiment: %w", err)
	}
	state := pairState{Status: "collecting", Arms: map[string]pairArmState{"legacy": {Status: "not_started"}, "thesis": {Status: "not_started"}}}
	defer func() {
		if err != nil {
			state.Status = "failed"
			state.Error = redact.String(err.Error())
		}
		if e := writePairJSON(filepath.Join(dir, "state.json"), state, false); e != nil && err == nil {
			err = e
			code = 1
		}
	}()
	copy := *settings
	copy.RunsDir = filepath.Join(dir, "runs")
	copy.KeepRuns, copy.DataCacheDays = 0, 0
	if err = os.Mkdir(copy.RunsDir, 0700); err != nil {
		return 1, err
	}
	files := map[string]string{}
	copy.AgentsDir = filepath.Join(dir, "personas")
	if err = os.Mkdir(copy.AgentsDir, 0700); err != nil {
		return 1, err
	}
	entries, err := os.ReadDir(settings.AgentsDir)
	if err != nil {
		return 1, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		rel := "personas/" + entry.Name()
		if err = copyPairInput(settings.AgentsDir+"/"+entry.Name(), dir, rel, files); err != nil {
			return 1, err
		}
	}
	for _, file := range []struct {
		src, rel string
		dst      *string
	}{
		{settings.Research.SourcesFile, "sources.csv", &copy.Research.SourcesFile},
		{settings.Research.HolidaysFile, "holidays.csv", &copy.Research.HolidaysFile},
	} {
		if file.src != "" {
			if err = copyPairInput(file.src, dir, file.rel, files); err != nil {
				return 1, err
			}
			*file.dst = filepath.Join(dir, file.rel)
		}
	}
	public := *settings
	public.Providers = model.ProviderConfig{}
	public.API.APIKey, public.Local.APIKey, public.ChiefFallback.APIKey = "", "", ""
	settingsJSON, err := json.Marshal(public)
	if err != nil {
		return 1, err
	}
	var choice [1]byte
	if _, err = rand.Read(choice[:]); err != nil {
		return 1, err
	}
	order := []string{"legacy", "thesis"}
	if choice[0]&1 == 1 {
		order[0], order[1] = order[1], order[0]
	}
	registered := time.Now().UTC().Truncate(time.Second)
	reg := pairRegistration{RoundTripCostBPS: cost, Version: 1, RegisteredAt: registered, Request: req, Order: order, Horizons: []int{10, 15}, Scope: "exploratory frozen-corpus pair; shared evidence access, no model tools, no history feedback, no parameter tuning or significance claim", Settings: settingsJSON, Files: files, AlphaVantageOmitted: omitAV}
	if err = writePairJSON(filepath.Join(dir, "registration.json"), reg, true); err != nil {
		return 1, err
	}
	for _, arm := range order {
		if err = os.Mkdir(filepath.Join(copy.RunsDir, arm), 0700); err != nil {
			return 1, err
		}
		stub := &store.Run{Dir: filepath.Join(copy.RunsDir, arm), TS: registered}
		if err = stub.WriteMeta(model.RunMeta{ResearchMode: arm, Mode: string(req.Mode), Ticker: req.Ticker, Indices: req.Indices, GeneratedAt: registered.Format(time.RFC3339), Outcome: "not_started"}); err != nil {
			return 1, err
		}
	}
	if err = writePairJSON(filepath.Join(dir, "state.json"), state, false); err != nil {
		return 1, err
	}
	snapshot, err := capture(ctx, marketdata.SnapshotCaptureConfig{Tickers: tickers, Benchmarks: benchmarks, Providers: copy.Providers, CacheDir: filepath.Join(dir, "collection-cache"), ProviderStateDir: settings.DataDir, SourcesFile: copy.Research.SourcesFile, Documents: max(1, copy.Research.Documents), Log: func(s string) { fmt.Fprintln(os.Stderr, s) }})
	if err != nil {
		return 1, err
	}
	if snapshot == nil || snapshot.AsOf.Before(registered) {
		return 1, fmt.Errorf("collector returned an invalid snapshot cutoff")
	}
	snapshot.AsOf = snapshot.AsOf.UTC().Truncate(time.Second)
	blob, err := json.Marshal(snapshot)
	if err != nil {
		return 1, err
	}
	blob = redact.Bytes(blob)
	if len(blob) > 256<<20 {
		return 1, fmt.Errorf("frozen corpus exceeds 256 MiB")
	}
	// Reload exactly the bytes whose hash is registered, including redaction.
	if err = json.Unmarshal(blob, &snapshot); err != nil {
		return 1, err
	}
	state.AsOf, state.SnapshotSHA256 = snapshot.AsOf, pairHash(blob)
	manifest := struct {
		Pairs []scoreboard.ResearchPairSpec `json:"pairs"`
	}{[]scoreboard.ResearchPairSpec{{ID: filepath.Base(dir), RegisteredAt: registered.Format(time.RFC3339Nano), LegacyRun: "legacy", ThesisRun: "thesis", MaxSkewSeconds: 0, SnapshotSHA256: map[string]string{"data/evaluation-snapshot.json": state.SnapshotSHA256}}}}
	if err = writePairJSON(filepath.Join(dir, "pairs.json"), manifest, true); err != nil {
		return 1, err
	}
	for _, arm := range order {
		r := &store.Run{Dir: filepath.Join(copy.RunsDir, arm), TS: snapshot.AsOf}
		if err = os.Mkdir(filepath.Join(r.Dir, "data"), 0700); err != nil {
			return 1, err
		}
		if err = os.WriteFile(filepath.Join(r.Dir, "data/evaluation-snapshot.json"), blob, 0600); err != nil {
			return 1, err
		}
		if err = r.WriteMeta(model.RunMeta{ResearchMode: arm, Mode: string(req.Mode), Ticker: req.Ticker, Indices: req.Indices, GeneratedAt: snapshot.AsOf.Format(time.RFC3339), Outcome: "not_started"}); err != nil {
			return 1, err
		}
	}
	state.Status = "running"
	code = 0
	for _, arm := range order {
		if err = ctx.Err(); err != nil {
			return 1, err
		}
		if err = verifyPairInputs(dir, files); err != nil {
			return 1, err
		}
		r := &store.Run{Dir: filepath.Join(copy.RunsDir, arm), TS: snapshot.AsOf}
		stored, e := os.ReadFile(filepath.Join(r.Dir, "data/evaluation-snapshot.json"))
		if e != nil {
			return 1, e
		}
		if pairHash(stored) != state.SnapshotSHA256 {
			return 1, fmt.Errorf("frozen snapshot changed before %s launch", arm)
		}
		var frozen marketdata.ResearchSnapshot
		if err = json.Unmarshal(stored, &frozen); err != nil {
			return 1, err
		}
		copy.ResearchMode, copy.DataDir = arm, filepath.Join(r.Dir, "cache")
		cfg := orchestratorConfig(&copy, req)
		cfg.Frozen, cfg.EvaluationRun = &frozen, r
		a := pairArmState{Status: "running", StartedAt: time.Now().UTC()}
		state.Arms[arm] = a
		if err = writePairJSON(filepath.Join(dir, "state.json"), state, false); err != nil {
			return 1, err
		}
		var attempted []model.DomainStatus
		for ev := range run(ctx, cfg) {
			switch ev.Type {
			case orchestrator.EventStatus:
				if report := ev.Report; report != nil {
					attempted = append(attempted, model.DomainStatus{Domain: report.Agent, Status: report.Status, Err: report.Err, Attempts: report.Attempts, Duration: report.Duration, Tokens: report.Tokens, Usage: report.Usage})
				}
			case orchestrator.EventLog:
				fmt.Fprintf(os.Stderr, "[%s] %s\n", arm, ev.Message)
			case orchestrator.EventError:
				a.Status = "failed"
				a.Error = redact.String(ev.Message)
			case orchestrator.EventComplete:
				if a.Status != "failed" && ev.Meta != nil {
					a.Status = ev.Meta.Outcome
				}
			}
		}
		if a.Status == "running" {
			a.Status = "failed"
			a.Error = "arm produced no completion event"
		}
		a.CompletedAt = time.Now().UTC()
		state.Arms[arm] = a
		if a.Status == "failed" {
			meta, loadErr := store.LoadMeta(r.Dir)
			if loadErr != nil {
				return 1, fmt.Errorf("load failed arm metadata: %w", loadErr)
			}
			meta.Outcome = "failed"
			meta.Duration = a.CompletedAt.Sub(a.StartedAt).Milliseconds()
			if len(meta.Domains) == 0 {
				meta.Domains = attempted
			}
			if err = r.WriteMeta(*meta); err != nil {
				return 1, err
			}
		}
		if a.Status != "complete" {
			code = 3
		}
		if err = writePairJSON(filepath.Join(dir, "state.json"), state, false); err != nil {
			return 1, err
		}
	}
	state.Status = "awaiting_outcomes"
	return code, nil
}

func pairUniverse(req model.RunRequest) ([]string, []string, model.RunRequest, error) {
	u, err := universe.Load()
	if err != nil {
		return nil, nil, req, err
	}
	stocks, benches := map[string]bool{}, map[string]bool{}
	if req.Mode == model.ModeSingle {
		if req.Ticker == "" || strings.ContainsAny(req.Ticker, "/\\ \t\n") {
			return nil, nil, req, fmt.Errorf("invalid single ticker")
		}
		stocks[req.Ticker] = true
		c, _ := u.Lookup(req.Ticker)
		benches[universe.BenchmarkFor(c.Index, req.Ticker)] = true
	} else {
		if len(req.Indices) == 0 {
			req.Indices = append([]string(nil), universe.AllIndices()...)
		}
		seen := map[string]bool{}
		for _, idx := range req.Indices {
			if seen[idx] || len(u.Constituents(idx)) == 0 {
				return nil, nil, req, fmt.Errorf("unknown or duplicate index %q", idx)
			}
			seen[idx] = true
			for _, c := range u.Constituents(idx) {
				stocks[c.Ticker] = true
				benches[universe.BenchmarkFor(idx, c.Ticker)] = true
			}
		}
	}
	var tickers, benchmarks []string
	for t := range stocks {
		tickers = append(tickers, t)
	}
	for t := range benches {
		benchmarks = append(benchmarks, t)
	}
	sort.Strings(tickers)
	sort.Strings(benchmarks)
	return tickers, benchmarks, req, nil
}
func pairHash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func copyPairInput(src, root, rel string, files map[string]string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	b = redact.Bytes(b)
	if err = os.WriteFile(filepath.Join(root, rel), b, 0600); err != nil {
		return err
	}
	files[rel] = pairHash(b)
	return nil
}
func verifyPairInputs(dir string, files map[string]string) error {
	for name, want := range files {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if pairHash(b) != want {
			return fmt.Errorf("registered input changed: %s", name)
		}
	}
	return nil
}
func writePairJSON(path string, value any, exclusive bool) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = redact.Bytes(b)
	if exclusive {
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(b)
		closeErr := f.Close()
		if e != nil {
			return e
		}
		return closeErr
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pair-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func readPairJSON(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (256<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 256<<20 {
		return fmt.Errorf("pair artifact exceeds 256 MiB")
	}
	return json.Unmarshal(data, dst)
}
