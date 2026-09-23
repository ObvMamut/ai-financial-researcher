package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// canned OpenAI-compatible success body.
const cannedChatResponse = `{"choices":[{"message":{"role":"assistant","content":"ANALYSIS: buy signal"}}]}`

func TestCallAPIEngineParsesContentAndSendsAuth(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cannedChatResponse))
	}))
	defer srv.Close()

	api := model.APIConfig{BaseURL: srv.URL, Model: "deepseek-chat", APIKey: "sk-test"}
	out, _, err := callAPIEngine(context.Background(), api, "screen the EU50")
	if err != nil {
		t.Fatalf("callAPIEngine: %v", err)
	}
	if out != "ANALYSIS: buy signal" {
		t.Errorf("content = %q, want the assistant message content", out)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
	if gotBody.Model != "deepseek-chat" {
		t.Errorf("request model = %q, want deepseek-chat", gotBody.Model)
	}
	if len(gotBody.Messages) != 1 || gotBody.Messages[0].Content != "screen the EU50" {
		t.Errorf("request messages = %+v, want single user message with the prompt", gotBody.Messages)
	}
	if gotBody.Stream {
		t.Errorf("stream = true, want false")
	}
}

func TestCallAPIEngineKeylessLocal(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		_, _ = w.Write([]byte(cannedChatResponse))
	}))
	defer srv.Close()

	// No APIKey — a local server (Ollama/llama.cpp) doesn't authenticate.
	api := model.APIConfig{BaseURL: srv.URL, Model: "qwen2.5:7b-instruct"}
	out, _, err := callAPIEngine(context.Background(), api, "screen the EU50")
	if err != nil {
		t.Fatalf("keyless callAPIEngine: %v", err)
	}
	if out != "ANALYSIS: buy signal" {
		t.Errorf("content = %q, want the assistant message content", out)
	}
	if hadAuth {
		t.Errorf("Authorization header was sent for a keyless (local) call; want none")
	}
}

func TestCallAPIEngineErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	api := model.APIConfig{BaseURL: srv.URL, Model: "deepseek-chat", APIKey: "sk-test"}
	_, _, err := callAPIEngine(context.Background(), api, "hi")
	if err == nil {
		t.Fatal("expected an error on HTTP 429, got nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error %q should mention the status code", err.Error())
	}
	// The API key must never leak into an error string.
	if strings.Contains(err.Error(), "sk-test") {
		t.Errorf("error text leaked the API key: %q", err.Error())
	}
}

func TestCallAPIEngineRequiresConfig(t *testing.T) {
	_, _, err := callAPIEngine(context.Background(), model.APIConfig{Model: "m", APIKey: "k"}, "hi")
	if err == nil {
		t.Fatal("expected an error when base_url is empty")
	}
}

// TestRunAgentAPIEngine exercises the CLIApi branch inside runAgent end-to-end
// (branch + report contract) with no network and no subprocess.
func TestRunAgentAPIEngine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(cannedChatResponse))
	}))
	defer srv.Close()

	api := model.APIConfig{BaseURL: srv.URL, Model: "deepseek-chat", APIKey: "sk-test"}
	retry := model.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond}

	r := runAgent(context.Background(), model.CLIApi, "news", string(model.StageAnalysis),
		"prompt", 5*time.Second, retry, "", "", api)

	if r.Status != model.StatusDone {
		t.Fatalf("status = %q (err=%q), want done", r.Status, r.Err)
	}
	if r.Stdout != "ANALYSIS: buy signal" {
		t.Errorf("stdout = %q, want the assistant content", r.Stdout)
	}
	if r.CLI != model.CLIApi {
		t.Errorf("report CLI = %q, want api", r.CLI)
	}
}

// TestRunAgentAPIEngineFailsClosed confirms a non-200 drives runAgent's failure
// path (so the specialist is marked failed rather than hanging).
func TestRunAgentAPIEngineFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	api := model.APIConfig{BaseURL: srv.URL, Model: "deepseek-chat", APIKey: "sk-test"}
	retry := model.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond}

	r := runAgent(context.Background(), model.CLIApi, "news", string(model.StageAnalysis),
		"prompt", 5*time.Second, retry, "", "", api)

	if r.Status != model.StatusFailed {
		t.Fatalf("status = %q, want failed", r.Status)
	}
}

// The engine sent no max_tokens and never looked at finish_reason, so a
// response cut off mid-report arrived as ordinary text. Its JSON tail was gone,
// which the pipeline then read as "this domain scored nobody".
func TestAPIEngineRejectsTruncatedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"half a rep"}}]}`)
	}))
	defer srv.Close()

	_, _, err := callAPIEngine(context.Background(),
		model.APIConfig{BaseURL: srv.URL, Model: "m", APIKey: "k"}, "prompt")
	if err == nil {
		t.Fatal("a length-truncated response must be an error, not a short report")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("err = %v, want it to name truncation", err)
	}
}

func TestAPIEngineSendsMaxTokens(t *testing.T) {
	var got chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":11,"completion_tokens":22}}`)
	}))
	defer srv.Close()

	out, _, err := callAPIEngine(context.Background(),
		model.APIConfig{BaseURL: srv.URL, Model: "m", APIKey: "k", MaxTokens: 4096}, "prompt")
	if err != nil {
		t.Fatalf("callAPIEngine: %v", err)
	}
	if out != "ok" {
		t.Errorf("content = %q, want ok", out)
	}
	if got.MaxTokens != 4096 {
		t.Errorf("max_tokens = %d, want 4096", got.MaxTokens)
	}
}

// A missing finish_reason must not be treated as truncation: not every
// OpenAI-compatible server sends one.
func TestAPIEngineAcceptsMissingFinishReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"fine"}}]}`)
	}))
	defer srv.Close()

	out, _, err := callAPIEngine(context.Background(),
		model.APIConfig{BaseURL: srv.URL, Model: "m"}, "prompt")
	if err != nil {
		t.Fatalf("callAPIEngine: %v", err)
	}
	if out != "fine" {
		t.Errorf("content = %q, want fine", out)
	}
}

// A run's cost was invisible: metadata.json recorded no tokens at all.
func TestAPIEngineReportsCompletionTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}],"usage":{"prompt_tokens":11,"completion_tokens":345}}`)
	}))
	defer srv.Close()

	_, tokens, err := callAPIEngine(context.Background(),
		model.APIConfig{BaseURL: srv.URL, Model: "m"}, "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if tokens != 345 {
		t.Errorf("tokens = %d, want 345", tokens)
	}
}

func TestReasoningEffortIsSentOnlyWhenSet(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "ok"}}}})
	}))
	defer srv.Close()
	for _, effort := range []string{"low", ""} {
		if _, _, err := callAPIEngineUsage(context.Background(), model.APIConfig{BaseURL: srv.URL, Model: "m", ReasoningEffort: effort}, "p"); err != nil {
			t.Fatal(err)
		}
	}
	if bodies[0]["reasoning_effort"] != "low" {
		t.Fatalf("effort not sent: %v", bodies[0])
	}
	if _, present := bodies[1]["reasoning_effort"]; present {
		t.Fatalf("empty effort must be omitted, not sent: %v", bodies[1])
	}
}
