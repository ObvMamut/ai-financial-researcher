package model

// TokenUsage records one attempted call. Nil counts are unavailable, including
// CLI calls without telemetry; an explicitly reported zero differs from missing.
// Cache and reasoning counts are subsets, never extra tokens added to totals.
type TokenUsage struct {
	FinishReason string `json:"finish_reason,omitempty"`
	// Incomplete marks known lower bounds (for example Claude main-loop-only
	// usage) even when all count fields are present.
	Incomplete        bool `json:"incomplete,omitempty"`
	PromptTokens      *int `json:"prompt_tokens,omitempty"`
	CompletionTokens  *int `json:"completion_tokens,omitempty"`
	TotalTokens       *int `json:"total_tokens,omitempty"`
	CacheHitTokens    *int `json:"prompt_cache_hit_tokens,omitempty"`
	CacheMissTokens   *int `json:"prompt_cache_miss_tokens,omitempty"`
	CompletionDetails *struct {
		ReasoningTokens *int `json:"reasoning_tokens,omitempty"`
	} `json:"completion_tokens_details,omitempty"`
}
