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
	// chiefCompaction is the one repair of an oversized research dossier. It
	// runs on the Chief engine because the cheap model measurably cannot make
	// the cut: 15 recorded deepseek-chat compactions landed at 0.61-0.99 of
	// their narrative size regardless of the requested budget, while the same
	// prompt on the Chief model met 12/12 per-field budgets (2026-09-23,
	// docs/plans/2026-09-23-compaction-on-chief-engine.md).
	chiefCompaction chiefPurpose = "compaction"
	// chiefResearch is a thesis-researcher call under
	// research.researcher_engine = "chief": opt-in, for when the cheap model's
	// grounding is what keeps dossiers off supported (six 2026-09-24 live runs;
	// docs/plans/2026-09-23-improvement-plan-v2.md).
	chiefResearch chiefPurpose = "research"
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
// tokens: apiengine.go's defaultMaxTokens.
//
// That derivation justifies the VALUE for the thesis pipeline; it does not
// justify applying it to the legacy pipeline's Chief, which reaches this same
// function through the same chiefTarget (orchestrator.go's run, the initial
// and corrective Chief calls) but has no research-role byte budget of its own
// to derive a cap from at all. The reused value is still the right default
// there — 8192 tokens covers legacy's five-report-plus-scores synthesis
// comfortably, and an operator who sets [chief_api].max_tokens explicitly
// always overrides this — but that is a separate, unstated justification
// from the one above, not a consequence of it. Reusing defaultMaxTokens
// rather than minting a second identical constant is still the considered
// choice for both pipelines: a second constant could only drift from this
// one later, never add information now.
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

// researchTarget is the target for a thesis research call in role. Only the
// researcher moves, and only when research.researcher_engine = "chief"; it
// then keeps the ordinary transient-retry budget, as compaction does, because
// synthesis_max_attempts is sized for a call with a fallback and research has
// none. Every other role — and the researcher by default — is cheap.
func (t *thesisRunner) researchTarget(role string) callTarget {
	if role != "thesis-researcher" || t.cfg.Research.Defaults().ResearcherEngine != model.ResearcherEngineChief {
		return t.cheapTarget()
	}
	target := chiefTarget(t.chief, t.cfg, chiefResearch)
	target.Retry = t.cfg.Retry
	target.Retry.NoRetryOnTimeout = true
	return target
}

// compactionTarget is the dossier-compaction call's target: the run's own
// resolved Chief engine and synthesis timeout — never the cheap pool, whose
// model cannot perform the cut. It keeps the ordinary transient-retry budget
// rather than synthesis_max_attempts: that one (default 1) is sized for the
// Chief's synthesis call, which has its own fallback; compaction has none,
// and a single connection reset would otherwise fail the company.
func (t *thesisRunner) compactionTarget() callTarget {
	target := chiefTarget(t.chief, t.cfg, chiefCompaction)
	target.Retry = t.cfg.Retry
	// A reset is retried; a timeout is not — at up to the synthesis timeout per
	// attempt, a second one would outlast the run's own deadline.
	target.Retry.NoRetryOnTimeout = true
	return target
}
