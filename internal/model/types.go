// Package model holds the shared data types passed between the orchestrator,
// the agent layer, and the TUI. It has no dependencies on other internal packages
// so every layer can import it without creating cycles.
package model

import (
	"encoding/json"
	"time"
)

// Mode selects which pipeline the orchestrator runs.
type Mode string

const (
	ModeIndependent Mode = "independent" // screen the universe, return top N ideas
	ModeSingle      Mode = "single"      // analyze one user-supplied ticker
)

// RunRequest is what the UI (or CLI) asks the orchestrator to do.
type RunRequest struct {
	Mode    Mode
	Ticker  string   // single-stock mode only
	Indices []string // independent mode: index keys to screen; empty = all
}

// Direction is the trade direction in a final idea.
type Direction string

const (
	DirectionBuy  Direction = "BUY"
	DirectionSell Direction = "SELL"
)

// Bias is a per-domain directional lean reported by scouts and specialists.
type Bias string

const (
	BiasBullish Bias = "bullish"
	BiasBearish Bias = "bearish"
	BiasNeutral Bias = "neutral"
)

// AgentStatus tracks one agent subprocess through its lifecycle for the TUI.
type AgentStatus string

const (
	StatusQueued  AgentStatus = "queued"
	StatusRunning AgentStatus = "running"
	StatusDone    AgentStatus = "done"
	StatusFailed  AgentStatus = "failed"
)

// Stage names the phase of the pipeline an agent belongs to.
type Stage string

const (
	StageScreening Stage = "screening" // Gemini scouts
	StageAnalysis  Stage = "analysis"  // Gemini specialists
	StageSynthesis Stage = "synthesis" // Claude chief analyst
)

// CLI identifies which engine runs an agent: a subprocess binary (gemini/claude)
// or the native OpenAI-compatible HTTP engine (api).
type CLI string

const (
	CLIGemini CLI = "gemini"
	CLIClaude CLI = "claude"
	CLIApi    CLI = "api"   // OpenAI-compatible HTTP endpoint (see APIConfig)
	CLILocal  CLI = "local" // config selector: runs on the CLIApi HTTP engine pointed at a local server
)

// Constituent is one tradeable name in the universe.
type Constituent struct {
	Ticker   string `json:"ticker"` // canonical symbol (e.g. AAPL, ASML.AS, 7203.T)
	Name     string `json:"name"`
	Exchange string `json:"exchange"`
	Country  string `json:"country"`
	Sector   string `json:"sector"`
	Index    string `json:"index"` // index key: sp500 | nq100 | eu50 | asia100
}

// Candidate is a scout's nomination for the shortlist.
type Candidate struct {
	Ticker string `json:"ticker"`
	Name   string `json:"name"`
	Bias   Bias   `json:"bias"`
	Reason string `json:"reason"`
	Index  string `json:"index,omitempty"` // source index key (set by the orchestrator)
}

// ScoutResult is the structured tail a scout emits for one index.
type ScoutResult struct {
	Index      string      `json:"index"`
	Candidates []Candidate `json:"candidates"`
}

// DomainScore is one specialist's read on one ticker.
type DomainScore struct {
	Ticker   string `json:"ticker"`
	Bias     Bias   `json:"bias"`
	Strength int    `json:"strength"` // 0-10
	Note     string `json:"note"`
}

// SpecialistResult is the structured tail a specialist emits.
type SpecialistResult struct {
	Domain  string        `json:"domain"`
	Scores  []DomainScore `json:"scores"`
	Missing []string      `json:"missing"`
}

// Report is the outcome of a single agent subprocess run.
type Report struct {
	Agent    string      // role name, e.g. "technicals", "scout-sp500"
	CLI      CLI         // gemini | claude
	Stage    Stage       // screening | analysis | synthesis
	Status   AgentStatus // done | failed
	Path     string      // runs/<ts>/<agent>.md
	Stdout   string      // raw captured output
	Err      string      // error text when Status == failed
	Duration int64       // milliseconds
	Attempts int         // subprocess attempts actually made (≥1)
	// Tokens is the completion-token count the engine reported, when it reported
	// one. The CLI engines report none, so this stays 0 for them.
	Tokens int
}

// TradeIdea is one final deliverable: direction, confidence, rationale, and
// actionable trade mechanics. The mechanics fields are omitempty so ideas.json
// from runs predating them still parses.
type TradeIdea struct {
	Rank       int       `json:"rank"`
	Ticker     string    `json:"ticker"`
	Name       string    `json:"name"`
	Index      string    `json:"index"`
	Direction  Direction `json:"direction"`  // BUY | SELL
	Confidence int       `json:"confidence"` // 0-100
	Why        string    `json:"why"`

	Entry         float64 `json:"entry,omitempty"`          // suggested entry price
	Stop          float64 `json:"stop,omitempty"`           // protective stop
	Target        float64 `json:"target,omitempty"`         // profit target
	RiskReward    float64 `json:"risk_reward,omitempty"`    // |target−entry| / |entry−stop|
	TimeframeDays int     `json:"timeframe_days,omitempty"` // expected holding period
	PositionNote  string  `json:"position_note,omitempty"`  // sizing/hedging guidance

	// PriceAtGeneration is the verified last close when the idea was produced
	// (from the quant pack); the scoreboard measures P&L against it.
	PriceAtGeneration float64 `json:"price_at_generation,omitempty"`
}

// IdeasResult is the Chief Analyst's final JSON payload.
type IdeasResult struct {
	Mode        string      `json:"mode"`
	GeneratedAt string      `json:"generated_at"`
	Ideas       []TradeIdea `json:"ideas"`
	Notes       string      `json:"notes"`
}

// StageTimeouts defines per-stage durations.
type StageTimeouts struct {
	Screening time.Duration
	Analysis  time.Duration
	Synthesis time.Duration
}

// RetryPolicy defines how to handle subprocess failures.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Jitter      bool
}

// DomainWeights defines the relative importance of each analysis domain.
// Quant replaced the former "technicals" domain; UnmarshalJSON still accepts
// the legacy key so old metadata.json files load.
type DomainWeights struct {
	Quant        float64 `json:"quant"`
	News         float64 `json:"news"`
	Fundamentals float64 `json:"fundamentals"`
	Macro        float64 `json:"macro"`
	Sentiment    float64 `json:"sentiment"`
}

// UnmarshalJSON accepts both the current keys and the legacy
// "Technicals"/"technicals" (and capitalized pre-tag) spellings.
func (w *DomainWeights) UnmarshalJSON(data []byte) error {
	var raw map[string]float64
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	pick := func(keys ...string) float64 {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				return v
			}
		}
		return 0
	}
	w.Quant = pick("quant", "technicals", "Technicals")
	w.News = pick("news", "News")
	w.Fundamentals = pick("fundamentals", "Fundamentals")
	w.Macro = pick("macro", "Macro")
	w.Sentiment = pick("sentiment", "Sentiment")
	return nil
}

// ProviderConfig holds API keys and contact info for data providers.
type ProviderConfig struct {
	ContactEmail    string
	AlphaVantageKey string
	FredKey         string
}

// APIConfig configures the OpenAI-compatible cheap-research engine (CLIApi).
// It is used only when the cheap-research role is routed to CLIApi; the heavy
// synthesis role always stays on the Claude CLI.
type APIConfig struct {
	BaseURL string // e.g. https://api.deepseek.com (up to, not incl. /chat/completions)
	Model   string // e.g. deepseek-chat
	APIKey  string // Bearer token; sourced from env/TOML, never logged
	// MaxTokens bounds the completion. Sending none left the limit to the
	// provider's default, and a report cut off at that limit arrived as ordinary
	// text with its JSON tail missing — which the pipeline read as a domain that
	// scored nobody rather than as a failure. 0 means the engine's default.
	MaxTokens int
}

// DomainStatus tracks the outcome of a specialist research run.
type DomainStatus struct {
	Domain   string      `json:"domain"`
	Status   AgentStatus `json:"status"`
	Err      string      `json:"err,omitempty"`
	Grounded bool        `json:"grounded"` // true if per-ticker verified data was used
	Attempts int         `json:"attempts"`
	Duration int64       `json:"duration_ms"`
	// Tokens counts the completion tokens the engine reported for this call,
	// when it reported any (the CLI engines do not).
	Tokens int `json:"tokens,omitempty"`

	// Ungrounded lists the shortlisted tickers this domain found no verified
	// data for. A domain whose whole shortlist is ungrounded is a degraded run,
	// however confident the report reads.
	Ungrounded []string `json:"ungrounded,omitempty"`
	// CorrectedScores lists shortlisted tickers the agent scored without any
	// verified data. The orchestrator deleted those scores from the report's
	// structured tail and moved the names into its `missing` array, so the
	// Chief Analyst never saw them. A long list means the model confabulated.
	CorrectedScores []string `json:"corrected_scores,omitempty"`
	// OffShortlistScores lists tickers the agent scored that were never on the
	// shortlist — hallucinated symbols, also deleted from the tail.
	OffShortlistScores []string `json:"off_shortlist_scores,omitempty"`
	// FabricatedCitations lists [source:] domains the agent cited on a
	// search-less engine. Non-empty means the report's sourcing was invented and
	// the orchestrator has rewritten those tags to [unverified].
	FabricatedCitations []string `json:"fabricated_citations,omitempty"`
}

// RunMeta captures all parameters and outcomes of a run for audit.
type RunMeta struct {
	Mode        string         `json:"mode"`
	Ticker      string         `json:"ticker,omitempty"`
	Indices     []string       `json:"indices,omitempty"` // indices screened (independent mode)
	GeneratedAt string         `json:"generated_at"`
	Shortlist   []Candidate    `json:"shortlist"`
	Domains     []DomainStatus `json:"domains"`
	Weights     DomainWeights  `json:"weights"`
	// QuantOnly names the shortlisted tickers no per-ticker provider can reach:
	// SEC EDGAR and AlphaVantage are US-only, so a non-US listing is graded on
	// quant alone. This is a known structural limit, deliberately kept out of
	// Warnings — an expected limit is not a warning.
	QuantOnly []string `json:"quant_only,omitempty"`
	Warnings  []string `json:"warnings"`
	Outcome   string   `json:"outcome"` // complete | degraded | failed
	Duration  int64    `json:"total_duration_ms"`

	// Engine names the cheap-research engine the scouts and specialists ran on
	// ("gemini" | "api"), and EngineModel the model it was pointed at.
	// SynthesisModel is the Claude model the Chief Analyst used. Without these,
	// a run's artifacts do not record what produced them: two runs five hours
	// apart with wildly different macro reads were indistinguishable in
	// metadata.json.
	Engine         string `json:"engine,omitempty"`
	EngineModel    string `json:"engine_model,omitempty"`
	SynthesisModel string `json:"synthesis_model,omitempty"`

	// Stages records wall-clock milliseconds per pipeline stage (screening,
	// quant, analysis, synthesis). 79% of one run's 298s was unattributed
	// because only per-agent durations were kept and the in-process stages had
	// none at all.
	Stages map[string]int64 `json:"stages,omitempty"`

	// DataErrors collects every provider failure encountered while assembling
	// the packs. These previously lived only in data/<domain>.json, so a run
	// that lost eight tickers to rate limiting read the same as one that lost
	// none.
	DataErrors []string `json:"data_errors,omitempty"`

	// PersonaSHA maps each agent role to a short hash of the persona file used.
	// Personas are runtime data, editable without a code change, so this is what
	// makes a run's outcome attributable to the prompts that produced it.
	PersonaSHA map[string]string `json:"persona_sha,omitempty"`
}
