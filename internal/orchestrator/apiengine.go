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
	"github.com/mamut/claude-financial-researcher/internal/redact"
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
	text, usage, err := callAPIEngineUsage(ctx, api, prompt)
	tokens := 0
	if usage.CompletionTokens != nil {
		tokens = *usage.CompletionTokens
	}
	return text, tokens, err
}

func callAPIEngineUsage(ctx context.Context, api model.APIConfig, prompt string) (string, model.TokenUsage, error) {
	var usage model.TokenUsage
	// A key is required for remote providers but omitted for local servers
	// (Ollama/llama.cpp), which don't authenticate — so only base_url + model
	// are mandatory here. Callers validate the key where it matters (remote).
	if api.BaseURL == "" || api.Model == "" {
		return "", usage, fmt.Errorf("api engine misconfigured: base_url and model are required")
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
		return "", usage, err
	}

	url := strings.TrimRight(api.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", usage, err
	}
	req.Header.Set("Content-Type", "application/json")
	if api.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+api.APIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", usage, err
	}
	defer resp.Body.Close()

	// Read a bounded amount so a misbehaving endpoint can't stream forever.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if readErr != nil {
		return "", usage, fmt.Errorf("api engine: read response: %w", readErr)
	}
	if len(body) > 4<<20 {
		return "", usage, fmt.Errorf("api engine: response exceeds 4 MiB")
	}
	var parsed chatResponse
	decodeErr := json.Unmarshal(body, &parsed)
	if decodeErr == nil {
		usage = parsed.Usage
	}

	if resp.StatusCode != http.StatusOK {
		// Include a truncated body snippet for diagnosis; never echo the key.
		// The response body is the provider's own prose. An endpoint that quotes
		// the request back would otherwise put the bearer token in the report.
		err := fmt.Errorf("api engine %s returned %d: %s", api.Model, resp.StatusCode, redact.String(snippet(body)))
		if isPermanentStatus(resp.StatusCode) {
			return "", usage, permanentError{err}
		}
		return "", usage, err
	}

	if decodeErr != nil {
		return "", usage, fmt.Errorf("api engine: decode response: %w", decodeErr)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", usage, fmt.Errorf("api engine error: %s", redact.String(parsed.Error.Message))
	}
	if len(parsed.Choices) == 0 {
		return "", usage, fmt.Errorf("api engine: response contained no choices")
	}
	// A completion cut off at the token limit has lost its structured tail. It
	// used to be returned as though it were a finished report: the tail was
	// simply absent, which the pipeline read as a domain that scored nobody.
	// The typed error stops unchanged retries and preserves usage on failure.
	if parsed.Choices[0].FinishReason == "length" {
		usage.FinishReason = "length"
		return "", usage, outputLimitError{maxTokens}
	}
	usage.FinishReason = parsed.Choices[0].FinishReason
	return parsed.Choices[0].Message.Content, usage, nil
}

// Output exhaustion is not a transient failure. Partial JSON is never usable.
type outputLimitError struct{ Limit int }

func (e outputLimitError) Error() string {
	return fmt.Sprintf("api engine: response truncated at the %d-token limit; unchanged retry suppressed", e.Limit)
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
	Usage model.TokenUsage `json:"usage"`
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
