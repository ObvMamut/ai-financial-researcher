# Codex project guidance

## Start here

You are developing Claude Financial Researcher, a Go terminal application for
swing-trade research. Read `CLAUDE.md` before changing code: it is the shared,
detailed architecture and development guide, including the model-access constraints.
Keep that file available for Claude Code. This file adds the Codex entry point and
a compact navigation guide rather than duplicating the evolving pipeline spec.

Read the relevant files in `docs/workflow/` for behavioral changes; they are the
source of truth for the pipeline, scoring, and output contracts. Keep code, runtime
personas, and those specs consistent when changing behavior.

`agents/*.md` are application runtime prompts, not instructions for the coding
assistant. `GEMINI.md` describes a runtime research subprocess role. Do not adopt
those roles while developing the application. Preserve `CLAUDE.md`, `GEMINI.md`,
and `.claude/` when maintaining Codex support.

## Repository map

- `cmd/cfr/main.go`: loads settings, dispatches TUI/headless/scoreboard/postmortem
  commands, and maps settings to orchestrator configuration.
- `internal/tui/`: Bubble Tea screens; consumes orchestrator events. Keep UI types
  out of the runner and pipeline.
- `internal/orchestrator/orchestrator.go`: pipeline coordination and engine
  selection. `runner.go` handles subprocesses, timeouts, retries, and stderr;
  `pool.go` bounds concurrency; `apiengine.go` implements the optional HTTP engine.
- `internal/orchestrator/`: pre-screening, quant staging, evidence coverage,
  base scoring, enforcement, risk gates, corrective prompts, and degraded results
  live in separate files beside the driver.
- `internal/agents/agents.go`: loads persona Markdown, records hashes, and
  assembles prompts with verified data and engine capabilities.
- `internal/marketdata/`: providers, price routing, caching, FX, exchange sessions,
  SEC filings, earnings, and computed positioning signals.
- `internal/quant/`: statistical calculations and verified packs; use locally
  computed statistics, not chart-pattern technical analysis.
- `internal/universe/data/*.csv`: curated index samples, not full memberships.
  `internal/universe/` loads, validates, and deduplicates them.
- `internal/model/`: shared data types and scoring scale. `internal/parse/`:
  defensive extraction of structured model output.
- `internal/store/`: run artifacts and history under `runs/`.
- `internal/scoreboard/`: trade-path replay, horizon evaluation, deduplication,
  controls, calibration, attribution, and grounded postmortem lessons.
- `internal/config/config.go` and `cfr.toml.example`: configuration implementation
  and documented settings. Precedence is defaults, user config, project config,
  `CFR_*` environment variables, then command flags.
- `agents/`: active runtime personas. `agents.v1/`: frozen A/B control personas;
  do not edit the control arm.
- `testdata/fakebin/`: fake model CLIs used by integration and runner tests.

## Pipeline and invariants

Independent mode follows pre-screen → scouts → merged shortlist → verified quant
and market data → five specialist reports → computed base scores → Chief Analyst
→ enforcement and risk checks, with corrective/degraded paths. Single-stock mode
starts from the supplied ticker and skips scouting. The target is up to five ideas
in independent mode and one in single-stock mode; risk checks may remove ideas.

- The Chief Analyst engine is selected by `chief_engine` (`claude` | `api`; omitted
  means `claude`), and the cheap-research engine by `cheap_engine` (gemini/api/local).
  An API Chief needs dedicated credentials (`[chief_api]` / `CFR_CHIEF_API_*`), never
  inherited from `[api]` or `[local]`. The optional Chief API fallback is Claude-only
  and has its own credentials. Using Codex to develop this repository does not change
  these runtime engine contracts.
- Preserve graceful degradation, cancellation, bounded concurrency, and defensive
  JSON parsing. Model output must pass Go-side validation.
- Quant and Macro are blinded to scout direction; Macro is contextual and carries
  zero scoring weight. Missing evidence and computed abstentions differ.
- When changing confidence semantics, review `internal/model/scoring.go`,
  orchestrator base-score enforcement, TUI confidence display, scoreboard buckets,
  `agents/chief-analyst.md`, and `docs/workflow/scoring.md` together.
- Runtime persona edits take effect without rebuilding. Do not put development
  instruction files in persona directories: the loader treats Markdown files
  other than README.md as personas.

## Development and validation

Run commands from the repository root. Use the Go toolchain required by `go.mod`
(currently Go 1.26.3 or newer).

```sh
go build ./...
go test ./...
go vet ./...
```

For focused work, run the relevant package tests first, for example
`go test ./internal/orchestrator ./internal/agents`. Format changed Go files with
`gofmt`. Integration tests use fake model CLIs and local HTTP fixtures; model
accounts and API keys are not required. Tests that use `httptest` need loopback
socket access. If sandbox restrictions block them, report that distinction from
an actual assertion failure.

Live commands include `go run ./cmd/cfr` and
`go run ./cmd/cfr run --ticker AAPL --json`. They use configured model/data
providers and write artifacts; use the test suite for routine validation.
Headless exit codes are 0 complete, 3 degraded, 1 error, and 2 usage.

Inspect `git status` before editing and preserve existing user changes. Keep
generated `runs/`, `.data/`, local `cfr.toml`, credentials, and personal assistant
settings out of commits. Use `cfr.toml.example` when documenting configuration.
