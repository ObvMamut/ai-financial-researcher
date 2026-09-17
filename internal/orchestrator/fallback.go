package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// chiefFallbackAllowed reports whether the DeepSeek Chief Analyst fallback may
// fire for this run, and — when it may — the credentials to use.
//
// It composes two gates on top of resolveChiefFallback's own credential
// validation (kept, not reimplemented):
//
//   - primary.CLI must be model.CLIClaude. The fallback is a resilience
//     measure for when the Claude primary call fails; under chief_engine="api"
//     there is no Claude primary to fall back from, so firing it anyway would
//     dispatch a second, near-identical, billable DeepSeek call for the exact
//     same prompt the primary just failed on. This mirrors
//     config.Settings.ChiefFallbackActive's own "chief_engine != claude ->
//     never active" rule, so the orchestrator's runtime gate cannot disagree
//     with what config reports as active.
//   - cfg.ChiefFallbackEnabled, a tri-state override: nil (the config key
//     omitted) preserves resolveChiefFallback's existing api_key-only gate —
//     today's documented behaviour, unchanged — true forces the fallback on
//     (still requiring credentials), and false forces it off even with
//     credentials present.
//
// An explicit enabled=true together with a non-claude primary is a
// configuration error that config.Settings.ValidateChiefEngine already
// rejects at load time (Task 3); resolved defensively here too, in case a
// caller builds an orchestrator.Config directly without going through
// config.Load.
func chiefFallbackAllowed(cfg Config, primary chiefEngine) (model.APIConfig, bool, error) {
	api, ok, err := resolveChiefFallback(cfg)
	if err != nil || !ok {
		return api, ok, err
	}
	explicitlyEnabled := cfg.ChiefFallbackEnabled != nil && *cfg.ChiefFallbackEnabled
	if primary.CLI != model.CLIClaude {
		if explicitlyEnabled {
			return model.APIConfig{}, false, fmt.Errorf("chief_fallback cannot be explicitly enabled when the Chief primary is not claude")
		}
		return model.APIConfig{}, false, nil
	}
	if cfg.ChiefFallbackEnabled != nil && !*cfg.ChiefFallbackEnabled {
		return model.APIConfig{}, false, nil
	}
	return api, true, nil
}

// attemptChiefFallback runs the optional DeepSeek resilience call after the
// primary Chief Analyst (claude CLI) call has already failed or its JSON
// failed to parse. It reuses the exact same agents/chief-analyst.md prompt —
// the DeepSeek response is expected to emit the same fenced ```json block —
// and, on success, runs it through the same parseIdeas → validateIdeas →
// applyRiskGate → dropViolating pipeline the primary call goes through, so a
// successful fallback still carries real entry/stop/target rather than only
// base scores. It does not get its own corrective re-prompt: one attempt is
// the budget for a call that is already a resilience measure.
//
// Returns ok=false on any failure — the engine unconfigured, the HTTP call
// failing after its own retries, or the response not parsing — so the caller
// falls through to the mechanical buildDegradedIdeas fallback exactly as it
// did before this existed.
func attemptChiefFallback(ctx context.Context, ch chan<- Event, run *store.Run, cfg Config, api model.APIConfig, prompt, primaryErr string, v verified) (ideas *model.IdeasResult, warnings []warning, status model.DomainStatus, ok bool) {
	log(ch, fmt.Sprintf("Chief Analyst failed (%s) — attempting DeepSeek fallback synthesis (%s)…", primaryErr, api.Model))
	agentStatus(ch, "chief-analyst-fallback", model.StatusRunning, nil)

	r := runAgent(ctx, model.CLIApi, "chief-analyst", string(model.StageSynthesis), prompt, cfg.Timeouts.SynthesisFallback, cfg.Retry, "", "", api)
	if err := run.WriteReport("chief-analyst-fallback", r.Stdout); err != nil {
		log(ch, fmt.Sprintf("warn: write chief-analyst-fallback report: %v", err))
	}
	status = model.DomainStatus{
		Domain: "chief-analyst-fallback", Status: r.Status, Err: r.Err,
		Duration: r.Duration, Attempts: r.Attempts, Tokens: r.Tokens, Usage: r.Usage,
	}

	if r.Status == model.StatusFailed {
		agentStatus(ch, "chief-analyst-fallback", model.StatusFailed, &r)
		log(ch, fmt.Sprintf("DeepSeek fallback synthesis failed: %s", r.Err))
		return nil, nil, status, false
	}

	parsed, err := parseIdeas(r.Stdout)
	if err != nil {
		status.Status = model.StatusFailed
		status.Err = fmt.Sprintf("parse fallback ideas: %v", err)
		agentStatus(ch, "chief-analyst-fallback", model.StatusFailed, &r)
		log(ch, fmt.Sprintf("warn: parse DeepSeek fallback ideas JSON: %v", err))
		return nil, nil, status, false
	}
	agentStatus(ch, "chief-analyst-fallback", model.StatusDone, &r)

	warnings = validateIdeas(parsed, cfg, v)
	findings := applyRiskGate(parsed, v, cfg.Risk)
	for _, f := range findings {
		log(ch, "risk: "+f.Message)
		msg := f.Message
		if f.Ticker != "" {
			msg = strings.TrimPrefix(msg, f.Ticker+": ")
		}
		warnings = append(warnings, warning{Ticker: f.Ticker, Message: "risk gate: " + msg})
	}
	if dropped := dropViolating(parsed, findings); len(dropped) > 0 {
		log(ch, fmt.Sprintf("Risk gate dropped %d idea(s) from the DeepSeek fallback", len(dropped)))
		parsed.Notes = strings.TrimSpace(parsed.Notes + fmt.Sprintf(
			" Risk gate dropped %d idea(s): %s.", len(dropped), strings.Join(dropped, "; ")))
	}

	parsed.Notes = strings.TrimSpace(fmt.Sprintf(
		"Primary Chief Analyst call failed (%s); synthesis completed via the DeepSeek fallback engine (%s). %s",
		primaryErr, api.Model, parsed.Notes))
	return parsed, warnings, status, true
}
