package orchestrator

import (
	"fmt"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// chiefPurpose names why a particular Chief call is being made. It carries no
// execution semantics by itself — every Chief call, on every engine and for
// every purpose, uses the same synthesis timeout, retry budget and Stage (see
// chiefTarget) — but keeps the three Chief report names traceable to the call
// that produced them, for whatever provenance recording Task 6 adds.
type chiefPurpose string

const (
	chiefInitial    chiefPurpose = "synthesis"
	chiefCorrective chiefPurpose = "corrective"
	chiefFallback   chiefPurpose = "fallback"
)

// chiefEngine is the Chief Analyst's dispatch target, resolved once per run
// (in run(), before Stage 0.5's universe-wide fetch) so a misconfigured
// [chief_api] fails fast rather than after real spend. It deliberately never
// shares the cheap-research pool's own APIConfig: chief_engine="api" and
// cheap_engine="api"/"local" (itself CLIApi under the hood — see
// resolveCheapEngine) can both resolve to model.CLIApi while pointing at
// completely different endpoints, models and credentials.
type chiefEngine struct {
	CLI    model.CLI       // CLIClaude or CLIApi
	Model  string          // the actual model name, for provenance
	Binary string          // CLI only
	API    model.APIConfig // API only; never the cheap pool's config
}

// resolveChiefEngine reads cfg.ChiefEngine and returns a fully populated
// engine or an error — never a partial one, so a caller cannot dispatch on a
// zero-value APIConfig believing it is configured. "" and "claude" are
// equivalent (applyDefaults normalizes "" to "claude" before this runs in the
// production path, but both are accepted here too, matching
// Settings.ValidateChiefEngine's own contract for any caller that resolves
// ahead of defaulting, e.g. a test that builds a bare Config{}).
func resolveChiefEngine(cfg Config) (chiefEngine, error) {
	switch cfg.ChiefEngine {
	case "", "claude":
		return chiefEngine{
			CLI:    model.CLIClaude,
			Model:  cfg.Models[model.CLIClaude],
			Binary: cfg.Binaries[model.CLIClaude],
		}, nil
	case "api":
		if cfg.ChiefAPI.BaseURL == "" || cfg.ChiefAPI.Model == "" || cfg.ChiefAPI.APIKey == "" {
			return chiefEngine{}, fmt.Errorf("chief_engine=api requires chief_api.base_url, chief_api.model, and an API key (set CFR_CHIEF_API_KEY)")
		}
		return chiefEngine{
			CLI:   model.CLIApi,
			Model: cfg.ChiefAPI.Model,
			API:   cfg.ChiefAPI,
		}, nil
	default:
		return chiefEngine{}, fmt.Errorf("invalid chief_engine %q (valid: claude, api)", cfg.ChiefEngine)
	}
}

// outputTokens is the response token cap a Chief call on the API engine
// should request. This is a deliberate choice, not an inherited accident:
// [chief_api].max_tokens is deliberately left 0 by default (Task 3), and the
// thesis pipeline's own Chief role budget already caps the Chief's response
// at 24576 bytes (model.ResearchBudgets.Defaults().Chief.ResponseBytes) —
// which, at this package's own ~3-bytes/token heuristic, is exactly 8192
// tokens: apiengine.go's defaultMaxTokens. That constant is documented as
// bounding "a specialist report," but the Chief's response — the board plus
// already-condensed evidence, read once — is not larger in kind, and reusing
// the existing constant is the considered choice: minting a second constant
// with the identical value could only drift from this one later, never add
// information now. An operator who sets [chief_api].max_tokens explicitly
// always overrides this.
func (e chiefEngine) outputTokens() int {
	if e.API.MaxTokens > 0 {
		return e.API.MaxTokens
	}
	return defaultMaxTokens
}

// callTarget is everything one model call needs to reach its engine, so the
// engine type alone no longer implies whether a call is cheap research or
// Chief synthesis. thesis.go used to decide both the timeout/retry policy and
// the stage label from `cli == model.CLIClaude`, which broke the moment the
// Chief could also run on CLIApi (indistinguishable, by CLI alone, from the
// cheap engine also running on CLIApi). Field order mirrors runAgent's
// parameters (runner.go:38): runAgent(ctx, t.CLI, role, string(t.Stage),
// prompt, t.Timeout, t.Retry, t.Model, t.Binary, t.API).
type callTarget struct {
	CLI     model.CLI
	Model   string
	Binary  string
	API     model.APIConfig
	Stage   model.Stage // model.StageAnalysis | model.StageSynthesis
	Timeout time.Duration
	Retry   model.RetryPolicy

	// throttled routes the call through the shared pool (pool.submit) instead
	// of calling runAgent directly, so the pool's cheap-engine throttle (agy
	// keyring contention / a single local GPU) still applies. Only
	// cheapTarget sets it. A Chief target must never set it: the pool holds
	// exactly one APIConfig, for the cheap engine, and when chief_engine="api"
	// the Chief's CLI value can equal the cheap engine's (model.CLIApi) while
	// pointing at a completely different endpoint — routing it through the
	// pool would silently run the Chief on the cheap engine's credentials.
	throttled bool
}

// cheapTarget is the call target for every specialist/scout/research call the
// thesis pipeline makes: the cheap engine's own resolved CLI, model, binary
// and API, at the analysis stage, cfg.Timeouts.Analysis and cfg.Retry. It
// always routes through the shared pool, preserving the pool's cheap-engine
// throttle.
func (t *thesisRunner) cheapTarget() callTarget {
	return callTarget{
		CLI:       t.cheap,
		Model:     t.pool.models[t.cheap],
		Binary:    t.pool.binaries[t.cheap],
		API:       t.pool.api,
		Stage:     model.StageAnalysis,
		Timeout:   t.cfg.Timeouts.Analysis,
		Retry:     t.cfg.Retry,
		throttled: true,
	}
}

// chiefTarget is the call target for a Chief Analyst call of the given
// purpose, on the resolved engine e. It always uses the synthesis stage,
// cfg.Timeouts.Synthesis and cfg.SynthesisMaxAttempts — for every purpose and
// both engines, never the cheap engine's pool, and never the cheap engine's
// APIConfig.
func chiefTarget(e chiefEngine, cfg Config, purpose chiefPurpose) callTarget {
	// purpose has no execution effect today (every purpose and both engines
	// share the same stage/timeout/retry); it is threaded through so Task 6's
	// provenance recording has a stable label per call without another signature change.
	retry := cfg.Retry
	retry.MaxAttempts = cfg.SynthesisMaxAttempts
	api := e.API
	if e.CLI == model.CLIApi {
		api.MaxTokens = e.outputTokens()
	}
	return callTarget{
		CLI:     e.CLI,
		Model:   e.Model,
		Binary:  e.Binary,
		API:     api,
		Stage:   model.StageSynthesis,
		Timeout: cfg.Timeouts.Synthesis,
		Retry:   retry,
	}
}
