package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// decodeClaudeOutput unwraps the print-mode JSON envelope. Custom CLI wrappers
// that still return plain research text remain supported, with unknown usage.
// Envelope errors must never be mistaken for successful research prose.
func decodeClaudeOutput(out string) (string, model.TokenUsage, error) {
	var usage model.TokenUsage
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		return out, usage, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		return "", usage, fmt.Errorf("decode Claude output: %w", err)
	}
	var kind string
	_ = json.Unmarshal(fields["type"], &kind)
	if kind != "result" {
		return out, usage, nil
	}
	var result struct {
		Result     string                     `json:"result"`
		Subtype    string                     `json:"subtype"`
		IsError    bool                       `json:"is_error"`
		Errors     []string                   `json:"errors"`
		Usage      json.RawMessage            `json:"usage"`
		ModelUsage map[string]json.RawMessage `json:"modelUsage"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return "", usage, fmt.Errorf("decode Claude result: %w", err)
	}
	if len(result.ModelUsage) > 0 {
		// modelUsage includes nested-agent work. Its entries and usage describe
		// overlapping work: never add the two representations together.
		var inputs, outputs, reads, writes []*int
		for _, raw := range result.ModelUsage {
			var counts map[string]json.RawMessage
			_ = json.Unmarshal(raw, &counts)
			inputs = append(inputs, claudeCount(counts["inputTokens"]))
			outputs = append(outputs, claudeCount(counts["outputTokens"]))
			reads = append(reads, claudeCount(counts["cacheReadInputTokens"]))
			writes = append(writes, claudeCount(counts["cacheCreationInputTokens"]))
		}
		usage = normalizedClaudeUsage(sumClaudeCounts(inputs...), sumClaudeCounts(outputs...), sumClaudeCounts(reads...), sumClaudeCounts(writes...))
	} else {
		var counts map[string]json.RawMessage
		_ = json.Unmarshal(result.Usage, &counts)
		usage = normalizedClaudeUsage(claudeCount(counts["input_tokens"]), claudeCount(counts["output_tokens"]), claudeCount(counts["cache_read_input_tokens"]), claudeCount(counts["cache_creation_input_tokens"]))
		usage.Incomplete = true // usage excludes any nested-agent requests.
	}
	if result.Subtype == "error_during_execution" {
		// A crashed CLI can emit zeroed final accounting even after spending
		// tokens. Preserve the reported values without claiming completeness.
		usage.Incomplete = true
	}
	if result.IsError || result.Subtype != "success" {
		message := strings.Join(result.Errors, "; ")
		if message == "" {
			message = result.Result
		}
		return "", usage, fmt.Errorf("Claude result %s: %s", result.Subtype, message)
	}
	return result.Result, usage, nil
}

// Claude separates uncached input, cache writes and cache reads. Normalize them
// to the API engine's inclusive prompt total, retaining read/non-read subsets.
func normalizedClaudeUsage(input, output, read, write *int) model.TokenUsage {
	prompt := sumClaudeCounts(input, read, write)
	return model.TokenUsage{
		PromptTokens: prompt, CompletionTokens: output,
		TotalTokens:    sumClaudeCounts(prompt, output),
		CacheHitTokens: read, CacheMissTokens: sumClaudeCounts(input, write),
	}
}

func claudeCount(raw json.RawMessage) *int {
	var n *int
	if json.Unmarshal(raw, &n) != nil || n == nil || *n < 0 {
		return nil
	}
	return n
}

func sumClaudeCounts(counts ...*int) *int {
	if len(counts) == 0 {
		return nil
	}
	total := 0
	for _, count := range counts {
		if count == nil || *count < 0 || *count > int(^uint(0)>>1)-total {
			return nil
		}
		total += *count
	}
	return &total
}
