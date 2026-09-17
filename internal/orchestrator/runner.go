package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

type frozenModelToolsKey struct{}

// Frozen evaluations may use only the evidence supplied by the orchestrator.
// Keep the restriction on the context so retries and corrective calls inherit it.
func withFrozenModelTools(ctx context.Context) context.Context {
	return context.WithValue(ctx, frozenModelToolsKey{}, true)
}

func claudeOutputArgs(ctx context.Context) []string {
	args := []string{"--output-format", "json"}
	if frozen, _ := ctx.Value(frozenModelToolsKey{}).(bool); frozen {
		args = append(args, "--tools", "", "--disallowedTools", "mcp__*",
			"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
			"--setting-sources", "", "--disable-slash-commands", "--safe-mode")
	}
	return args
}

// runAgent executes one agent call and returns a Report. Most engines are CLI
// subprocesses invoked as `<binary> -p <prompt> [--model <m>]`; when cli is
// CLIApi it instead calls an OpenAI-compatible HTTP endpoint (api). Either way
// this function owns the per-call timeout and retry-with-backoff loop.
func runAgent(ctx context.Context, cli model.CLI, role, stage string, prompt string, timeout time.Duration, retry model.RetryPolicy, cliModel, binary string, api model.APIConfig) model.Report {
	r := callAgent(ctx, cli, role, stage, prompt, timeout, retry, cliModel, binary, api)
	// One place, rather than each of callAgent's several exits: an engine's
	// stderr and a provider's error prose both reach metadata.json and the TUI
	// through this report, and either can quote the request it was sent.
	r.Err = redact.String(r.Err)
	r.Stdout = redact.String(r.Stdout)
	return r
}

func callAgent(ctx context.Context, cli model.CLI, role, stage string, prompt string, timeout time.Duration, retry model.RetryPolicy, cliModel, binary string, api model.APIConfig) model.Report {
	start := time.Now()
	if binary == "" {
		binary = string(cli)
	}
	// Prompts are assembled from persisted evidence and retrieval diagnostics,
	// and a provider failure can carry the query string it was sent. Redacting
	// here covers every role and every engine at one point, rather than trusting
	// each block builder to have sanitised its own input.
	prompt = redact.String(prompt)
	args := []string{"-p", prompt}
	if cliModel != "" {
		args = append(args, "--model", cliModel)
	}
	if cli == model.CLIClaude {
		args = append(args, claudeOutputArgs(ctx)...)
	}

	report := model.Report{
		Agent: role,
		CLI:   cli,
		Stage: model.Stage(stage),
	}

	// A budget of zero would skip the loop entirely and return a report whose
	// Status is the empty string — neither done nor failed. Callers test for
	// StatusFailed, so that report reads as a success carrying no output, and the
	// run proceeds to parse nothing. One attempt is the floor: a call that is not
	// worth making should not be made by the caller.
	if retry.MaxAttempts < 1 {
		retry.MaxAttempts = 1
	}

	for attempt := 1; attempt <= retry.MaxAttempts; attempt++ {
		// Check for early cancellation
		select {
		case <-ctx.Done():
			report.Status = model.StatusFailed
			report.Err = "context cancelled"
			return report
		default:
		}
		report.Attempts = attempt

		tctx, cancel := context.WithTimeout(ctx, timeout)
		var out, stderrStr string
		var usage model.TokenUsage
		var runErr error
		if cli == model.CLIApi {
			out, usage, runErr = callAPIEngineUsage(tctx, api, prompt)
		} else {
			var stdout, stderr bytes.Buffer
			callArgs := args
			// Claude accepts piped input; avoid the OS per-argument size limit
			// when a thesis synthesis contains multiple source dossiers.
			piped := cli == model.CLIClaude && len(prompt) > 90000
			if piped {
				callArgs = []string{"-p"}
				if cliModel != "" {
					callArgs = append(callArgs, "--model", cliModel)
				}
				callArgs = append(callArgs, claudeOutputArgs(ctx)...)
			}
			cmd := exec.CommandContext(tctx, binary, callArgs...)
			if piped {
				cmd.Stdin = strings.NewReader(prompt)
			}
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			runErr = cmd.Run()
			out = stdout.String()
			stderrStr = stderr.String()
			if cli == model.CLIClaude {
				var envelopeErr error
				out, usage, envelopeErr = decodeClaudeOutput(out)
				if runErr != nil {
					usage.Incomplete = true
				}
				runErr = errors.Join(runErr, envelopeErr)
			}
		}
		report.Usage = append(report.Usage, usage)
		if usage.CompletionTokens != nil && *usage.CompletionTokens >= 0 {
			report.Tokens += *usage.CompletionTokens
		}
		cancel()
		if cli == model.CLIClaude && runErr != nil && strings.Contains(stderrStr+runErr.Error(), "disabled Claude subscription access") {
			report.Status, report.FailureKind = model.StatusFailed, "authentication"
			report.Err = strings.TrimSpace(runErr.Error() + " " + stderrStr)
			report.Duration = time.Since(start).Milliseconds()
			return report
		}

		if runErr == nil && strings.TrimSpace(out) != "" {
			report.Stdout = out
			report.Status = model.StatusDone
			report.Duration = time.Since(start).Milliseconds()
			return report
		}

		// A permanent failure — a malformed request, a bad key, an unknown
		// model — will fail identically on every retry. Stop immediately rather
		// than spending the wall clock and, on a metered endpoint, the money.
		var perm permanentError
		var limit outputLimitError
		if errors.As(runErr, &perm) || errors.As(runErr, &limit) {
			switch {
			case errors.As(runErr, &limit):
				report.FailureKind = "output_limit"
			case perm.Status == http.StatusUnauthorized || perm.Status == http.StatusForbidden:
				// Distinguishable from an ordinary permanent failure the same
				// way the Claude disabled-subscription case already gets
				// "authentication" — a caller (fallback gating, provenance)
				// can tell a bad/expired key apart from a malformed request.
				report.FailureKind = "permanent_auth"
			}
			report.Status = model.StatusFailed
			report.Err = runErr.Error()
			report.Duration = time.Since(start).Milliseconds()
			return report
		}

		// Fail if it's the last attempt or context is done
		if attempt == retry.MaxAttempts {
			report.Status = model.StatusFailed
			if runErr != nil {
				report.Err = runErr.Error()
			} else {
				report.Err = "empty output from " + binary
			}
			if se := strings.TrimSpace(stderrStr); se != "" {
				report.Err += ": " + se
			}
			report.Duration = time.Since(start).Milliseconds()
			return report
		}

		// Exponential backoff
		delay := retry.BaseDelay * (1 << (attempt - 1))
		if retry.MaxDelay > 0 && delay > retry.MaxDelay {
			delay = retry.MaxDelay
		}
		if retry.Jitter {
			delay = time.Duration(float64(delay) * (0.8 + 0.4*rand.Float64()))
		}

		select {
		case <-ctx.Done():
			report.Status = model.StatusFailed
			report.Err = "context cancelled"
			return report
		case <-time.After(delay):
		}
	}

	return report
}
