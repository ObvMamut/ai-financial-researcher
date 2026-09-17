package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestUsageRetainsTruncationWithoutUnchangedRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			fmt.Fprint(w, `{"choices":[{"finish_reason":"length","message":{"content":"partial"}}],"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33,"prompt_cache_hit_tokens":8,"completion_tokens_details":{"reasoning_tokens":10}}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"complete"}}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`)
	}))
	defer srv.Close()
	r := runAgent(context.Background(), model.CLIApi, "fixture", "analysis", "prompt", time.Second, model.RetryPolicy{MaxAttempts: 2}, "", "", model.APIConfig{BaseURL: srv.URL, Model: "fixture"})
	if r.Status != model.StatusFailed || r.FailureKind != "output_limit" || calls != 1 || r.Attempts != 1 || r.Tokens != 22 || len(r.Usage) != 1 {
		t.Fatalf("truncation usage lost or retried: %+v", r)
	}
	if *r.Usage[0].PromptTokens != 11 || *r.Usage[0].CompletionDetails.ReasoningTokens != 10 {
		t.Fatalf("usage details missing: %+v", r.Usage)
	}
	if got := reportStatus(r); len(got.Usage) != 1 || got.Tokens != 22 {
		t.Fatalf("metadata lost usage: %+v", got)
	}
}

func TestUsageDistinguishesMissingFromReportedZeroOnFailure(t *testing.T) {
	for _, usage := range []string{``, `,"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"choices":[]`+usage+`}`) }))
		r := runAgent(context.Background(), model.CLIApi, "fixture", "analysis", "prompt", time.Second, model.RetryPolicy{MaxAttempts: 1}, "", "", model.APIConfig{BaseURL: srv.URL, Model: "fixture"})
		srv.Close()
		if r.Status != model.StatusFailed || len(r.Usage) != 1 {
			t.Fatalf("missing attempt: %+v", r)
		}
		if (r.Usage[0].CompletionTokens == nil) != (usage == "") {
			t.Fatalf("unknown and zero collapsed: %+v", r.Usage)
		}
	}
}
