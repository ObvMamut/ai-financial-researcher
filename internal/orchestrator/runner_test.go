package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
