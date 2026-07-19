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

// CLI identifies which subprocess binary runs an agent.
type CLI string

const (
	CLIGemini CLI = "gemini"
	CLIClaude CLI = "claude"
)

// Constituent is one tradeable name in the universe.
type Constituent struct {
	Ticker   string `json:"ticker"`   // canonical symbol (e.g. AAPL, ASML.AS, 7203.T)
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

// DomainStatus tracks the outcome of a specialist research run.
type DomainStatus struct {
	Domain   string      `json:"domain"`
	Status   AgentStatus `json:"status"`
	Err      string      `json:"err,omitempty"`
	Grounded bool        `json:"grounded"` // true if verified data was used
	Attempts int         `json:"attempts"`
	Duration int64       `json:"duration_ms"`
}

// RunMeta captures all parameters and outcomes of a run for audit.
type RunMeta struct {
	Mode        string          `json:"mode"`
	Ticker      string          `json:"ticker,omitempty"`
	Indices     []string        `json:"indices,omitempty"` // indices screened (independent mode)
	GeneratedAt string          `json:"generated_at"`
	Shortlist   []Candidate     `json:"shortlist"`
	Domains     []DomainStatus  `json:"domains"`
	Weights     DomainWeights   `json:"weights"`
	Warnings    []string        `json:"warnings"`
	Outcome     string          `json:"outcome"` // complete | degraded | failed
	Duration    int64           `json:"total_duration_ms"`
}
