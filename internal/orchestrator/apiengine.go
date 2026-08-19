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

// callAPIEngine runs one cheap-research call against an OpenAI-compatible chat
// completions endpoint (DeepSeek by default, but any provider that speaks the
// same protocol — OpenRouter, OpenAI, a local vLLM/Ollama gateway — works by
// changing BaseURL + Model). It is the HTTP analogue of a `<bin> -p <prompt>`
// subprocess: give it a prompt, get back the assistant's text.
//
// It uses only the standard library (same discipline as the marketdata
// providers — no model SDK). The caller (runAgent) owns retry and the per-call
// timeout via ctx, so this function makes exactly one request.
func callAPIEngine(ctx context.Context, api model.APIConfig, prompt string) (string, error) {
	if api.BaseURL == "" || api.APIKey == "" || api.Model == "" {
		return "", fmt.Errorf("api engine misconfigured: base_url, model, and key are all required")
	}

	reqBody := chatRequest{
		Model:    api.Model,
		Stream:   false,
		Messages: []chatMessage{{Role: "user", Content: prompt}},
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	url := strings.TrimRight(api.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+api.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// Read a bounded amount so a misbehaving endpoint can't stream forever.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode != http.StatusOK {
		// Include a truncated body snippet for diagnosis; never echo the key.
		return "", fmt.Errorf("api engine %s returned %d: %s", api.Model, resp.StatusCode, snippet(body))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("api engine: decode response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", fmt.Errorf("api engine error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("api engine: response contained no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}

type chatRequest struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
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
