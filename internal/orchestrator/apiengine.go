package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// callAPIEngine runs one call against an OpenAI-compatible chat completions
// endpoint (DeepSeek by default, but any provider that speaks the same
// protocol — OpenRouter, OpenAI, a local vLLM/Ollama gateway — works by
// changing BaseURL + Model). It is the HTTP analogue of a `<bin> -p <prompt>`
// subprocess: give it a prompt, get back the assistant's text.
//
// Most callers are the cheap-research role (cheap_engine = "api"/"local"), but
// it is reused unmodified for the optional Chief Analyst DeepSeek fallback
// (fallback.go's attemptChiefFallback) — same engine, a different APIConfig.
//
// It uses only the standard library (same discipline as the marketdata
// providers — no model SDK). The caller (runAgent) owns retry and the per-call
// timeout via ctx, so this function makes exactly one request. It returns the
// assistant's text and the completion-token count the provider reported (0 when
// it reported none), which the run records per domain in metadata.json.
func callAPIEngine(ctx context.Context, api model.APIConfig, prompt string) (string, int, error) {
	// A key is required for remote providers but omitted for local servers
	// (Ollama/llama.cpp), which don't authenticate — so only base_url + model
	// are mandatory here. Callers validate the key where it matters (remote).
	if api.BaseURL == "" || api.Model == "" {
		return "", 0, fmt.Errorf("api engine misconfigured: base_url and model are required")
	}

	maxTokens := api.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	reqBody := chatRequest{
		Model:     api.Model,
		Stream:    false,
		MaxTokens: maxTokens,
		Messages:  []chatMessage{{Role: "user", Content: prompt}},
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, err
	}

	url := strings.TrimRight(api.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if api.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+api.APIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	// Read a bounded amount so a misbehaving endpoint can't stream forever.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode != http.StatusOK {
		// Include a truncated body snippet for diagnosis; never echo the key.
		err := fmt.Errorf("api engine %s returned %d: %s", api.Model, resp.StatusCode, snippet(body))
		if isPermanentStatus(resp.StatusCode) {
			return "", 0, permanentError{err}
		}
		return "", 0, err
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", 0, fmt.Errorf("api engine: decode response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", 0, fmt.Errorf("api engine error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", 0, fmt.Errorf("api engine: response contained no choices")
	}
	// A completion cut off at the token limit has lost its structured tail. It
	// used to be returned as though it were a finished report: the tail was
	// simply absent, which the pipeline read as a domain that scored nobody.
	// Returning an error puts it through the ordinary retry path instead.
	if parsed.Choices[0].FinishReason == "length" {
		return "", 0, fmt.Errorf("api engine: response truncated at the %d-token limit — raise api.max_tokens", maxTokens)
	}
	return parsed.Choices[0].Message.Content, parsed.Usage.CompletionTokens, nil
}

// defaultMaxTokens bounds a specialist report. A full five-domain report with a
// structured tail runs well under this; the limit exists so a runaway
// generation fails loudly rather than silently losing its tail.
const defaultMaxTokens = 8192

type chatRequest struct {
	Model     string        `json:"model"`
	Stream    bool          `json:"stream"`
	MaxTokens int           `json:"max_tokens,omitempty"`
	Messages  []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// snippet trims a response body to a short, single-line diagnostic string.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// permanentError marks a failure that will recur identically however many times
// it is retried. The retry loop used to treat a 400 exactly like a 503: three
// attempts at a malformed request, three delays, and on a metered endpoint three
// charges.
type permanentError struct{ error }

func (e permanentError) Unwrap() error { return e.error }

// isPermanentStatus reports whether an HTTP status describes a request that
// cannot succeed on repetition. 429 (rate limited) and 408 (request timeout) are
// client-class codes that are nonetheless transient, and are excluded.
func isPermanentStatus(code int) bool {
	if code == http.StatusTooManyRequests || code == http.StatusRequestTimeout {
		return false
	}
	return code >= 400 && code < 500
}
