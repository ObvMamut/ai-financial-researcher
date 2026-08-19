// Package orchestrator drives the two-stage (or three-stage) analysis pipeline.
// It spawns CLI subprocesses via a bounded worker pool and emits progress events
// over a channel consumed by the TUI. It never imports tui types.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/parse"
	"github.com/mamut/claude-financial-researcher/internal/store"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// EventType classifies a progress event.
type EventType int

const (
	EventStatus   EventType = iota // one agent changed status
	EventLog                       // free-text log line
	EventComplete                  // run finished, Ideas populated
	EventError                     // unrecoverable run error
)

// Event is the unit emitted on the progress channel.
type Event struct {
	Type    EventType
	Agent   string            // role name, relevant for EventStatus
	Status  model.AgentStatus // relevant for EventStatus
	Message string            // log text or error message
	Report  *model.Report     // set on EventStatus when done/failed
	Ideas   *model.IdeasResult // set on EventComplete
	Meta    *model.RunMeta     // set on EventComplete
}

// Config holds all run parameters.
type Config struct {
	Mode      model.Mode
	Ticker    string   // single-stock mode only
	Indices   []string // independent mode: index keys to screen; empty = all
	AgentsDir string   // path to agents/*.md
	RunsDir   string // base directory for run artifacts (default "runs")
	DataDir   string // path for cached market data
	Workers   int    // bounded pool size (default 4)

	Timeouts  model.StageTimeouts
	Retry     model.RetryPolicy
	Weights   model.DomainWeights
	Providers model.ProviderConfig
	Models    map[model.CLI]string // per-CLI model passed via --model; empty CLIClaude defaults to "opus"
	Binaries  map[model.CLI]string // per-CLI executable name; empty CLIGemini defaults to "agy", CLIClaude to "claude"

	// CheapEngine selects the engine for the cheap-research roles (scouts +
	// specialists): CLIGemini (the `agy` CLI, default), CLIApi (a remote
	// OpenAI-compatible endpoint, config in API), or CLILocal (a local
	// OpenAI-compatible server, config in Local). Synthesis always uses the
	// Claude CLI regardless.
	CheapEngine model.CLI
	// API configures the remote CLIApi engine; required when CheapEngine == CLIApi.
	API model.APIConfig
	// Local configures the local OpenAI-compatible server; required when
	// CheapEngine == CLILocal. The key is optional (local servers don't auth).
	Local model.APIConfig

	// GeminiConcurrency caps simultaneous Gemini (agy) subprocesses. Concurrent agy
	// processes contend on the OS keyring during auth and escalate to a browser login;
	// defaults to 1 when unset.
	GeminiConcurrency int
	// LocalConcurrency caps simultaneous local-model calls; one GPU can't run the
	// 5 specialists at once. Defaults to 1 when unset.
	LocalConcurrency int

	// KeepRuns is how many run directories CleanupOldRuns retains (default 100;
	// the scoreboard needs history).
	KeepRuns int
}

func (c *Config) applyDefaults() {
	if c.AgentsDir == "" {
		c.AgentsDir = "agents"
	}
	if c.RunsDir == "" {
		c.RunsDir = "runs"
	}
	if c.DataDir == "" {
		c.DataDir = ".data"
	}
	if c.Workers <= 0 {
		c.Workers = 4
	}

	// Default model selection. Claude does the single heavy synthesis call;
	// pin it so the run never depends on the machine's global CLI default.
	if c.Models == nil {
		c.Models = map[model.CLI]string{}
	}
	if c.Models[model.CLIClaude] == "" {
		c.Models[model.CLIClaude] = "opus"
	}

	// Default CLI binaries. Google discontinued the free `gemini` CLI tier, so the
	// cheap-research role now shells out to `agy` (the Antigravity CLI), which exposes
	// the same -p/--model interface. Override per role via CFR_GEMINI_BIN/CFR_CLAUDE_BIN.
	if c.Binaries == nil {
		c.Binaries = map[model.CLI]string{}
	}
	if c.Binaries[model.CLIGemini] == "" {
		c.Binaries[model.CLIGemini] = "agy"
	}
	if c.Binaries[model.CLIClaude] == "" {
		c.Binaries[model.CLIClaude] = "claude"
	}

	// Cheap-research role defaults to the `agy` CLI engine so a fresh checkout
	// (and the hermetic fakebin tests) need no API key. Set CheapEngine=CLIApi
	// (remote) or CLILocal (local server) to route scouts+specialists to the
	// OpenAI-compatible HTTP engine.
	if c.CheapEngine == "" {
		c.CheapEngine = model.CLIGemini
	}
	if c.API.Model == "" {
		c.API.Model = "deepseek-chat"
	}
	// Local defaults target a stock Ollama install; the model must be set by the
	// user (it depends on what they have pulled), so it has no default.
	if c.Local.BaseURL == "" {
		c.Local.BaseURL = "http://localhost:11434/v1"
	}

	// Serialize agy auth by default to avoid keyring contention (see Config.GeminiConcurrency).
	if c.GeminiConcurrency <= 0 {
		c.GeminiConcurrency = 1
	}
	// Serialize local-model calls by default (one GPU, one generation at a time).
	if c.LocalConcurrency <= 0 {
		c.LocalConcurrency = 1
	}

	if c.KeepRuns <= 0 {
		c.KeepRuns = 100
	}

	// Default Timeouts
	if c.Timeouts.Screening == 0 {
		c.Timeouts.Screening = 5 * time.Minute
	}
	if c.Timeouts.Analysis == 0 {
		c.Timeouts.Analysis = 5 * time.Minute
	}
	if c.Timeouts.Synthesis == 0 {
		c.Timeouts.Synthesis = 5 * time.Minute
	}

	// Default Retry Policy
	if c.Retry.MaxAttempts <= 0 {
		c.Retry.MaxAttempts = 2 // 1 retry
	}
	if c.Retry.BaseDelay == 0 {
		c.Retry.BaseDelay = 1 * time.Second
	}

	// Default Weights (matches scoring.md)
	if c.Weights.Quant == 0 && c.Weights.News == 0 && c.Weights.Fundamentals == 0 && c.Weights.Macro == 0 && c.Weights.Sentiment == 0 {
		c.Weights = model.DomainWeights{
			Fundamentals: 0.30,
			Quant:        0.20,
			News:         0.20,
			Macro:        0.15,
			Sentiment:    0.15,
		}
	}
}

// resolveCheapEngine maps the user-facing CheapEngine selector onto the concrete
// runner engine, the APIConfig to hand the pool, and the throttle concurrency.
// It validates required fields and fails fast so a misconfig surfaces before the
// run rather than as a wave of per-call failures.
func resolveCheapEngine(cfg Config) (engine model.CLI, api model.APIConfig, conc int, err error) {
	switch cfg.CheapEngine {
	case model.CLIGemini:
		// CLI engine (agy by default, or a cheap binary override) — no extra config.
		return model.CLIGemini, model.APIConfig{}, cfg.GeminiConcurrency, nil
	case model.CLIApi:
		if cfg.API.BaseURL == "" || cfg.API.APIKey == "" || cfg.API.Model == "" {
			return "", model.APIConfig{}, 0, fmt.Errorf("cheap_engine=api requires api.base_url, api.model, and an API key (set CFR_API_KEY)")
		}
		// Remote APIs handle concurrency well; run the specialists in parallel.
		return model.CLIApi, cfg.API, 0, nil
	case model.CLILocal:
		if cfg.Local.BaseURL == "" || cfg.Local.Model == "" {
			return "", model.APIConfig{}, 0, fmt.Errorf("cheap_engine=local requires local.base_url and local.model (api_key optional)")
		}
		// Local runs on the same CLIApi HTTP path, throttled to one GPU generation.
		return model.CLIApi, cfg.Local, cfg.LocalConcurrency, nil
	default:
		return "", model.APIConfig{}, 0, fmt.Errorf("invalid cheap_engine %q (valid: gemini, api, local)", cfg.CheapEngine)
	}
}

// Run executes the full pipeline and streams Events. It closes the returned
// channel when the run completes (success or error). The caller must drain it.
func Run(ctx context.Context, cfg Config) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		cfg.applyDefaults()
		if err := run(ctx, cfg, ch); err != nil {
			ch <- Event{Type: EventError, Message: err.Error()}
		}
	}()
	return ch
}

func emit(ch chan<- Event, e Event) {
	select {
	case ch <- e:
	default: // drop if the TUI is too slow; never block pipeline
	}
}

func log(ch chan<- Event, msg string) {
	emit(ch, Event{Type: EventLog, Message: msg})
}

func agentStatus(ch chan<- Event, role string, status model.AgentStatus, r *model.Report) {
	emit(ch, Event{Type: EventStatus, Agent: role, Status: status, Report: r})
}

func run(ctx context.Context, cfg Config, ch chan<- Event) error {
	start := time.Now()

	// Resolve + validate the cheap-research engine up front. Fail fast on a
	// misconfigured API/local engine rather than letting every cheap call fail
	// one-by-one (which would look like a silent provider outage). Note CLIClaude
	// is intentionally not a cheap engine: it shares model config with the synthesis
	// role, so routing the cheap roles to it would silently run them on Opus. To use
	// Claude cheaply, keep cheap_engine=gemini and override binaries.gemini=claude +
	// models.gemini=haiku.
	cheapCLI, cheapAPI, cheapConc, err := resolveCheapEngine(cfg)
	if err != nil {
		return err
	}

	// Load agent personas
	reg, err := agents.Load(cfg.AgentsDir)
	if err != nil {
		return fmt.Errorf("load agents: %w", err)
	}

	// Load universe
	uni, err := universe.Load()
	if err != nil {
		return fmt.Errorf("load universe: %w", err)
	}

	// Create run store
	run, err := store.New(cfg.RunsDir)
	if err != nil {
		return fmt.Errorf("create run dir: %w", err)
	}
	log(ch, fmt.Sprintf("Run directory: %s", run.Dir))
	_ = store.CleanupOldRuns(cfg.RunsDir, cfg.KeepRuns)

	// Start worker pool
	p := newPool(cfg.Workers, cfg.Models, cfg.Binaries, cheapAPI, cheapCLI, cheapConc)
	p.start(ctx)
	defer p.stop()

	// Initialize market data service
	cache := marketdata.NewCache(cfg.DataDir)
	dataSvc := marketdata.NewService(
		cache,
		marketdata.NewEdgarProvider(cfg.Providers.ContactEmail),
		marketdata.NewAlphaVantageProvider(cfg.Providers.AlphaVantageKey),
		marketdata.NewFredProvider(cfg.Providers.FredKey),
	)

	var shortlist []model.Candidate
	var domainStatuses []model.DomainStatus

	// ── Stage 1: Scouts (independent research only) ────────────────────────────
	var indices []string
	if cfg.Mode == model.ModeIndependent {
		indices = selectIndices(cfg.Indices)
		if len(indices) == 0 {
			return fmt.Errorf("no valid indices selected (known: %s)", strings.Join(universe.AllIndices(), ", "))
		}
		log(ch, fmt.Sprintf("Stage 1: screening %s with scouts…", strings.Join(indices, ", ")))
		resultChans := make([]<-chan model.Report, len(indices))

		for i, idx := range indices {
			cs := uni.Constituents(idx)
			prompt, err := reg.AssemblePrompt(agents.PromptParams{
				Role:                 "scout",
				Mode:                 cfg.Mode,
				RunTS:                run.TS,
				IndexKey:             idx,
				IndexConstituentList: universe.ConstituentList(cs),
			})
			if err != nil {
				log(ch, fmt.Sprintf("warn: assemble scout-%s prompt: %v", idx, err))
				continue
			}
			role := "scout-" + idx
			agentStatus(ch, role, model.StatusRunning, nil)
			resultChans[i] = p.submit(cheapCLI, role, string(model.StageScreening), prompt, cfg.Timeouts.Screening, cfg.Retry)
		}

		for i, idx := range indices {
			if resultChans[i] == nil {
				continue
			}
			role := "scout-" + idx
			r := <-resultChans[i]
			r.Path = fmt.Sprintf("%s/%s.md", run.Dir, role)
			if err := run.WriteReport(role, r.Stdout); err != nil {
				log(ch, fmt.Sprintf("warn: write %s report: %v", role, err))
			}
			if r.Status == model.StatusFailed {
				agentStatus(ch, role, model.StatusFailed, &r)
				log(ch, fmt.Sprintf("Scout %s failed (%s) — continuing", idx, r.Err))
				continue
			}
			agentStatus(ch, role, model.StatusDone, &r)
			candidates := parseScoutResult(r.Stdout, idx)
			shortlist = append(shortlist, candidates...)
		}

		shortlist = universe.Dedupe(shortlist)
		log(ch, fmt.Sprintf("Shortlist after dedup: %d names", len(shortlist)))
		if capped := universe.CapBalanced(shortlist, maxShortlist); len(capped) < len(shortlist) {
			log(ch, fmt.Sprintf("Shortlist capped to %d names (balanced across indices)", len(capped)))
			shortlist = capped
		}
		if err := run.WriteShortlist(shortlist); err != nil {
			log(ch, fmt.Sprintf("warn: write shortlist: %v", err))
		}

		if len(shortlist) == 0 {
			return fmt.Errorf("all scouts failed or returned no candidates — cannot continue")
		}
	} else {
		// Single-stock: build shortlist from user ticker
		c, _ := uni.Lookup(cfg.Ticker)
		shortlist = []model.Candidate{
			{Ticker: c.Ticker, Name: c.Name, Bias: model.BiasNeutral, Reason: "user-specified", Index: c.Index},
		}
		log(ch, fmt.Sprintf("Single-stock mode: analysing %s", c.Ticker))
	}

	// ── Stage 1.5: price history + quant metrics (in-process, no model) ───────
	log(ch, "Stage 1.5: fetching price history and computing quant metrics…")
	agentStatus(ch, "quant-data", model.StatusRunning, nil)
	quantPack := buildQuantPack(ctx, ch, run, marketdata.NewYahooClient(cache), shortlist)
	logPackErrors(ch, "quant", quantPack.Errors)
	if len(quantPack.ByTicker) > 0 {
		agentStatus(ch, "quant-data", model.StatusDone, nil)
		log(ch, fmt.Sprintf("Quant metrics computed for %d/%d tickers", len(quantPack.ByTicker), len(shortlist)))
	} else {
		agentStatus(ch, "quant-data", model.StatusFailed, nil)
		log(ch, "warn: no price data available — specialists will run ungrounded")
	}

	// ── Stage 2: Specialists (parallel) ────────────────────────────────────────
	log(ch, "Stage 2: running 5 specialist agents…")
	specialists := []struct {
		role string
		cli  model.CLI
	}{
		{"news", cheapCLI},
		{"fundamentals", cheapCLI},
		{"quant", cheapCLI},
		{"sentiment", cheapCLI},
		{"macro", cheapCLI},
	}

	specChans := make([]<-chan model.Report, len(specialists))
	grounded := make([]bool, len(specialists))
	for i, sp := range specialists {
		tickers := make([]string, len(shortlist))
		for j, c := range shortlist {
			tickers[j] = c.Ticker
		}
		pack := dataSvc.BuildPack(ctx, sp.role, tickers)
		logPackErrors(ch, sp.role, pack.Errors)
		if err := run.WriteDataPack(sp.role, pack); err != nil {
			log(ch, fmt.Sprintf("warn: write %s data pack: %v", sp.role, err))
		}

		// Assemble the verified-data block per role: the quant specialist gets
		// the full computed pack; news/sentiment get compact price context so
		// their narratives stay anchored to real closes.
		dataBlock := pack.Markdown()
		switch sp.role {
		case "quant":
			if qmd := quantPack.Markdown(); qmd != "" {
				dataBlock = qmd
			}
		case "news", "sentiment":
			if cb := quantPack.CompactBlock(); cb != "" {
				dataBlock += "\n### Verified price context (computed from daily OHLCV)\n\n" + cb
			}
		}
		grounded[i] = dataBlock != ""

		prompt, err := reg.AssemblePrompt(agents.PromptParams{
			Role:      sp.role,
			Mode:      cfg.Mode,
			RunTS:     run.TS,
			Shortlist: shortlist,
			Ticker:    cfg.Ticker,
			DataBlock: dataBlock,
		})
		if err != nil {
			log(ch, fmt.Sprintf("warn: assemble %s prompt: %v", sp.role, err))
			continue
		}
		agentStatus(ch, sp.role, model.StatusRunning, nil)
		specChans[i] = p.submit(sp.cli, sp.role, string(model.StageAnalysis), prompt, cfg.Timeouts.Analysis, cfg.Retry)
	}

	var specReports []agents.ReportContext
	var missingDomains []string

	for i, sp := range specialists {
		if specChans[i] == nil {
			missingDomains = append(missingDomains, sp.role)
			domainStatuses = append(domainStatuses, model.DomainStatus{Domain: sp.role, Status: model.StatusFailed, Err: "prompt assembly failed"})
			continue
		}
		r := <-specChans[i]
		r.Path = fmt.Sprintf("%s/%s.md", run.Dir, sp.role)
		if err := run.WriteReport(sp.role, r.Stdout); err != nil {
			log(ch, fmt.Sprintf("warn: write %s report: %v", sp.role, err))
		}
		
		// Grounded means we actually injected verified data into the prompt —
		// judged from the data we assembled, never from the model's own output.
		status := model.DomainStatus{
			Domain:   sp.role,
			Status:   r.Status,
			Err:      r.Err,
			Duration: r.Duration,
			Attempts: r.Attempts,
			Grounded: grounded[i],
		}

		if r.Status == model.StatusFailed {
			agentStatus(ch, sp.role, model.StatusFailed, &r)
			log(ch, fmt.Sprintf("Specialist %s failed — noting for chief analyst", sp.role))
			missingDomains = append(missingDomains, sp.role)
		} else {
			agentStatus(ch, sp.role, model.StatusDone, &r)
			specReports = append(specReports, agents.ReportContext{
				Domain:  sp.role,
				Content: r.Stdout,
			})
		}
		domainStatuses = append(domainStatuses, status)
	}

	// Minimum check
	if len(specReports) < 2 {
		return fmt.Errorf("fewer than 2 specialist reports succeeded — cannot synthesise")
	}

	// ── Stage 3: Chief Analyst (Claude) ───────────────────────────────────────
	log(ch, "Stage 3: Chief Analyst synthesising results…")

	prompt, err := reg.AssemblePrompt(agents.PromptParams{
		Role:       "chief-analyst",
		Mode:       cfg.Mode,
		RunTS:      run.TS,
		Shortlist:  shortlist,
		Ticker:     cfg.Ticker,
		Reports:    specReports,
		Missing:    missingDomains,
		Weights:    cfg.Weights,
		QuantBlock: quantPack.CompactBlock(),
	})
	if err != nil {
		return fmt.Errorf("assemble chief-analyst prompt: %w", err)
	}

	agentStatus(ch, "chief-analyst", model.StatusRunning, nil)
	r := runAgent(ctx, model.CLIClaude, "chief-analyst", string(model.StageSynthesis), prompt, cfg.Timeouts.Synthesis, cfg.Retry, cfg.Models[model.CLIClaude], cfg.Binaries[model.CLIClaude], model.APIConfig{})
	r.Path = fmt.Sprintf("%s/chief-analyst.md", run.Dir)
	if err := run.WriteReport("chief-analyst", r.Stdout); err != nil {
		log(ch, fmt.Sprintf("warn: write chief-analyst report: %v", err))
	}

	var ideas *model.IdeasResult
	var warnings []warning
	outcome := "complete"

	if r.Status == model.StatusFailed {
		agentStatus(ch, "chief-analyst", model.StatusFailed, &r)
		log(ch, "Chief analyst failed — building degraded fallback from specialist scores…")
		ideas = buildDegradedIdeas(cfg, specReports, shortlist)
		outcome = "degraded"
	} else {
		agentStatus(ch, "chief-analyst", model.StatusDone, &r)
		// Parse and validate ideas
		var parseErr error
		ideas, parseErr = parseIdeas(r.Stdout)
		if parseErr == nil {
			warnings = validateIdeas(ideas, cfg, uni, quantPack)

			// Corrective re-prompt for violations worth a second model call:
			// concentration and inverted stop/target levels.
			var repromptReasons []string
			for _, w := range warnings {
				if strings.Contains(w.Message, "diversification fail") {
					repromptReasons = append(repromptReasons, "your top ideas are over-concentrated: cover at least 3 different sectors")
				}
				if strings.Contains(w.Message, "level ordering") {
					repromptReasons = append(repromptReasons, fmt.Sprintf("fix %s: stop/entry/target must be ordered for the trade direction (BUY: stop < entry < target; SELL: target < entry < stop)", w.Ticker))
				}
			}

			if len(repromptReasons) > 0 {
				log(ch, "Validation failed — attempting corrective re-prompt…")
				reprompt := prompt + "\n\nCRITICAL: Your previous output failed validation. " + strings.Join(repromptReasons, "; ") + ". Re-emit the full JSON block with these problems fixed."
				r = runAgent(ctx, model.CLIClaude, "chief-analyst", string(model.StageSynthesis), reprompt, cfg.Timeouts.Synthesis, cfg.Retry, cfg.Models[model.CLIClaude], cfg.Binaries[model.CLIClaude], model.APIConfig{})
				if r.Status != model.StatusFailed {
					if newIdeas, err := parseIdeas(r.Stdout); err == nil {
						ideas = newIdeas
						warnings = validateIdeas(ideas, cfg, uni, quantPack)
					}
				}
			}
		} else {
			log(ch, fmt.Sprintf("warn: parse ideas JSON: %v", parseErr))
			ideas = buildDegradedIdeas(cfg, specReports, shortlist)
			ideas.Notes = fmt.Sprintf("JSON parse error: %v — %s", parseErr, ideas.Notes)
			outcome = "degraded"
		}
	}

	// Record the verified price each idea was generated at (scoreboard baseline).
	for i := range ideas.Ideas {
		if m, ok := quantPack.ByTicker[strings.ToUpper(ideas.Ideas[i].Ticker)]; ok && m.LastClose > 0 {
			ideas.Ideas[i].PriceAtGeneration = m.LastClose
		}
	}

	// Final persistence and metadata
	if err := run.WriteIdeas(ideas); err != nil {
		log(ch, fmt.Sprintf("warn: write ideas.json: %v", err))
	}

	warnMsgs := []string{} // serialize as [] rather than null in metadata.json
	for _, w := range warnings {
		msg := w.Message
		if w.Ticker != "" {
			msg = fmt.Sprintf("[%s] %s", w.Ticker, w.Message)
		}
		log(ch, "validation: "+msg)
		warnMsgs = append(warnMsgs, msg)
	}

	meta := model.RunMeta{
		Mode:        string(cfg.Mode),
		Ticker:      cfg.Ticker,
		Indices:     indices,
		GeneratedAt: ideas.GeneratedAt,
		Shortlist:   shortlist,
		Domains:     domainStatuses,
		Weights:     cfg.Weights,
		Warnings:    warnMsgs,
		Outcome:     outcome,
		Duration:    time.Since(start).Milliseconds(),
	}
	if err := run.WriteMeta(meta); err != nil {
		log(ch, fmt.Sprintf("warn: write metadata.json: %v", err))
	}

	log(ch, fmt.Sprintf("Done. %d trade idea(s) produced. Outcome: %s. Artifacts: %s", len(ideas.Ideas), outcome, run.Dir))
	// The terminal event must never be dropped: send blocking (the Run
	// contract requires the caller to drain the channel).
	ch <- Event{Type: EventComplete, Ideas: ideas, Meta: &meta, Message: run.Dir}
	return nil
}

// maxShortlist caps the merged scout shortlist (spec: 8–12 names).
const maxShortlist = 12

// selectIndices validates the requested index keys against the universe,
// preserving canonical order. Empty input means all indices.
func selectIndices(requested []string) []string {
	all := universe.AllIndices()
	if len(requested) == 0 {
		return all
	}
	want := make(map[string]bool, len(requested))
	for _, k := range requested {
		want[strings.ToLower(strings.TrimSpace(k))] = true
	}
	var out []string
	for _, k := range all {
		if want[k] {
			out = append(out, k)
		}
	}
	return out
}

// logPackErrors surfaces market-data provider failures instead of silently
// running ungrounded. Long lists are summarized to keep the log readable.
func logPackErrors(ch chan<- Event, domain string, errs []string) {
	const show = 3
	for i, e := range errs {
		if i == show {
			log(ch, fmt.Sprintf("data[%s]: … and %d more provider errors", domain, len(errs)-show))
			break
		}
		log(ch, fmt.Sprintf("data[%s]: %s", domain, e))
	}
}

// ── Parsing helpers ─────────────────────────────────────────────────────────

// extractLastJSON pulls the last ```json ... ``` block from text.
func extractLastJSON(text string) (string, bool) {
	return parse.LastJSONBlock(text)
}

func parseIdeas(stdout string) (*model.IdeasResult, error) {
	raw, ok := extractLastJSON(stdout)
	if !ok {
		return nil, fmt.Errorf("no fenced ```json block found in chief-analyst output")
	}
	var result model.IdeasResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &result, nil
}

// parseScoutResult extracts a scout's candidates. The index key comes from the
// orchestrator's own loop, not the model output — provenance must be trusted
// for the balanced shortlist cap.
func parseScoutResult(stdout, index string) []model.Candidate {
	raw, ok := extractLastJSON(stdout)
	if !ok {
		return nil
	}
	// Try to unmarshal as ScoutResult
	var sr model.ScoutResult
	if err := json.Unmarshal([]byte(raw), &sr); err != nil {
		return nil
	}
	out := make([]model.Candidate, 0, len(sr.Candidates))
	for _, c := range sr.Candidates {
		if strings.TrimSpace(c.Ticker) == "" {
			continue
		}
		c.Index = index
		out = append(out, c)
	}
	return out
}
