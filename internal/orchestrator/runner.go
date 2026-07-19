package orchestrator

import (
	"bytes"
	"context"
	"math/rand"
	"os/exec"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// runAgent executes one agent CLI subprocess and returns a Report.
// It supports per-call timeouts and retries with exponential backoff.
func runAgent(ctx context.Context, cli model.CLI, role, stage string, prompt string, timeout time.Duration, retry model.RetryPolicy, cliModel, binary string) model.Report {
	start := time.Now()
	if binary == "" {
		binary = string(cli)
	}
	args := []string{"-p", prompt}
	if cliModel != "" {
		args = append(args, "--model", cliModel)
	}

	report := model.Report{
		Agent: role,
		CLI:   cli,
		Stage: model.Stage(stage),
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
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(tctx, binary, args...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		cancel()

		out := stdout.String()
		if runErr == nil && strings.TrimSpace(out) != "" {
			report.Stdout = out
			report.Status = model.StatusDone
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
			if se := strings.TrimSpace(stderr.String()); se != "" {
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
