package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func fastRetry() model.RetryPolicy {
	return model.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond}
}

// A 400 was retried exactly like a 503. Retrying a malformed request wastes the
// wall clock and, on a metered endpoint, the money.
func TestRunAgentDoesNotRetryPermanentFailures(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound} {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(code)
			fmt.Fprint(w, `{"error":{"message":"nope"}}`)
		}))

		r := runAgent(context.Background(), model.CLIApi, "news", "analysis", "p",
			time.Second, fastRetry(), "", "",
			model.APIConfig{BaseURL: srv.URL, Model: "m", APIKey: "k"})
		srv.Close()

		if r.Status != model.StatusFailed {
			t.Errorf("HTTP %d: status = %s, want failed", code, r.Status)
		}
		if got := atomic.LoadInt32(&hits); got != 1 {
			t.Errorf("HTTP %d: %d attempts, want 1 — a client error will not fix itself", code, got)
		}
	}
}

// Every real cfr run to date has hit the identical "signal: killed" pattern
// (601s ≈ 2 attempts x the old 300s Synthesis timeout), yet nothing exercised
// the CLI-subprocess timeout/SIGKILL branch — only the CLIApi httptest path was
// covered. A killed attempt is a "too slow," not "flaky," failure, so retrying
// it is pointless; this also proves the kill is prompt rather than waiting out
// the subprocess.
func TestRunAgentCLITimeoutKillsSubprocess(t *testing.T) {
	bin, err := filepath.Abs("../../testdata/fakebin/claude")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CFR_FAKE_MODE", "hang")

	timeout := 100 * time.Millisecond
	start := time.Now()
	r := runAgent(context.Background(), model.CLIClaude, "chief-analyst", "synthesis",
		"# Agent: Chief Analyst\nsynthesize", timeout,
		model.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond}, "", bin, model.APIConfig{})
	elapsed := time.Since(start)

	if r.Status != model.StatusFailed {
		t.Fatalf("status = %s, want failed", r.Status)
	}
	if !strings.Contains(r.Err, "signal: killed") {
		t.Errorf("err = %q, want it to contain %q (the real production error string)", r.Err, "signal: killed")
	}
	// The fake sleeps for 3600s; a duration anywhere near the timeout (and
	// nowhere near the sleep) proves the kill was prompt.
	if elapsed > 2*time.Second {
		t.Errorf("runAgent took %s to return, want close to the %s timeout — the kill was not prompt", elapsed, timeout)
	}
	if r.Duration > 2000 {
		t.Errorf("reported Duration = %dms, want close to the %s timeout", r.Duration, timeout)
	}
}

// Rate limiting and timeouts are exactly what retry is for.
func TestRunAgentRetriesTransientFailures(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusRequestTimeout} {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if atomic.AddInt32(&hits, 1) < 2 {
				w.WriteHeader(code)
				return
			}
			fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"recovered"}}]}`)
		}))

		r := runAgent(context.Background(), model.CLIApi, "news", "analysis", "p",
			time.Second, fastRetry(), "", "",
			model.APIConfig{BaseURL: srv.URL, Model: "m", APIKey: "k"})
		srv.Close()

		if r.Status != model.StatusDone {
			t.Errorf("HTTP %d: status = %s (%s), want done after retry", code, r.Status, r.Err)
		}
		if got := atomic.LoadInt32(&hits); got != 2 {
			t.Errorf("HTTP %d: %d attempts, want 2", code, got)
		}
	}
}
