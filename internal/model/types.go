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
//
// Ticker, Name, Sector and Index are authoritative: the orchestrator overwrites
// whatever the model wrote with the universe's own row for that symbol, so a
// nomination cannot rename a company or move it to another index. Bias and
// Reason are the scout's, and travel with the name into every downstream prompt
// — a specialist that knows *why* a ticker is on the shortlist can confirm or
// contradict the thesis instead of describing the company from scratch.
type Candidate struct {
	Ticker string `json:"ticker"`
	Name   string `json:"name"`
	Bias   Bias   `json:"bias"`
	Reason string `json:"reason"`
	Sector string `json:"sector,omitempty"` // from the universe row (set by the orchestrator)
	Index  string `json:"index,omitempty"`  // source index key (set by the orchestrator)
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

	// BaseConfidence is the deterministic weighted domain score this idea's
	// confidence was anchored to, and DomainScores the per-domain signed
	// strengths (−10…+10) behind it. Both are recorded at generation so the
	// scoreboard can later ask which domains were right, rather than only
	// whether the trade worked.
	BaseConfidence int            `json:"base_confidence,omitempty"`
	DomainScores   map[string]int `json:"domain_scores,omitempty"`

	// Position size, computed in Go from the account's risk budget and the
	// idea's own stop distance — never authored by the model. "Half size" is
	// not a position; a share count is.
	//
	// Entry, Stop and Target are in Currency — what an order is actually placed
	// in. Notional and RiskAmount are converted to USD, so a book spanning four
	// exchanges adds up to one exposure. An absent Shares means sizing could not
	// produce a whole share and the run says so in its warnings.
	Currency   string  `json:"currency,omitempty"` // ISO code the levels are quoted in
	Shares     int     `json:"shares,omitempty"`
	Notional   float64 `json:"notional,omitempty"`    // USD
	RiskAmount float64 `json:"risk_amount,omitempty"` // USD at risk if the stop fills

	// ExpectancyBps is the simulated expected value of the trade in basis
	// points of the entry price, net of costs, under an explicit small edge.
	// BreakevenWinRate is the hit rate the geometry alone requires to break
	// even: risk / (risk + reward). An idea whose implied win rate is
	// implausible is a losing construction however good the thesis.
	ExpectancyBps    float64 `json:"expectancy_bps,omitempty"`
	BreakevenWinRate float64 `json:"breakeven_win_rate,omitempty"`
}

// RiskConfig is the deterministic risk policy applied after synthesis. Every
// number here was a sentence in a persona that the model could satisfy at its
// own edge: "a sound stop is usually 1-2 sigma" produced a run of 1.02-sigma
// stops, and "risk_reward >= 1.5 preferred" produced a run of 1.52s.
type RiskConfig struct {
	// AccountEquity and RiskPerTradePct set the position size: the currency at
	// risk per trade is equity x pct/100.
	AccountEquity   float64 `json:"account_equity"`
	RiskPerTradePct float64 `json:"risk_per_trade_pct"`
	// CostBps is the round-trip cost assumption (spread + commission + slippage)
	// charged against every expectancy calculation.
	CostBps float64 `json:"cost_bps"`
	// RRMin is the hard reward:risk floor.
	RRMin float64 `json:"rr_min"`
	// StopSigmaMin/Max bound the stop distance in units of sigma_daily x sqrt(h).
	StopSigmaMin float64 `json:"stop_sigma_min"`
	StopSigmaMax float64 `json:"stop_sigma_max"`
	// TargetSigmaMax bounds how far a target may sit from entry in the same units.
	TargetSigmaMax float64 `json:"target_sigma_max"`
	// ADVMinUSD is the 20-day average dollar volume below which a name is not
	// tradeable in size. It gates both the pre-screen and the final ideas.
	ADVMinUSD float64 `json:"adv_min_usd"`
	// MaxPairCorr is the pairwise return correlation above which two
	// same-direction ideas are treated as one position.
	MaxPairCorr float64 `json:"max_pair_corr"`
	// MaxPortfolioBeta bounds both the average absolute beta of the book and its
	// net signed beta.
	MaxPortfolioBeta float64 `json:"max_portfolio_beta"`
	// EdgeSigmaDaily is the daily expected return assumed in the expectancy
	// simulation, in units of sigma_daily. A driftless check is vacuous —
	// gambler's ruin makes EV about minus costs for any geometry — and an
	// optimistic one is vacuous the other way, so the prior is explicit and
	// configurable rather than hidden. P5 replaces it with realized hit rates.
	EdgeSigmaDaily float64 `json:"edge_sigma_daily"`
	// MinExpectancyBps is the simulated expectancy, in basis points of entry and
	// net of costs, below which a geometry is refused. The gate rejected only
	// `ev <= 0`, which let the 2026-09-01 run ship ideas at +3.0 and +5.7 bps —
	// numbers indistinguishable from zero against a 30 bps cost assumption and a
	// prior for the edge. A floor makes the check say "this geometry has to earn
	// something" rather than "this geometry must not be provably suicidal".
	MinExpectancyBps float64 `json:"min_expectancy_bps"`
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
	// SynthesisFallback bounds the DeepSeek resilience call attempted when the
	// primary Chief Analyst call fails or its JSON fails to parse. Zero at
	// defaulting time means "same as Synthesis" — one tunable, not two, until
	// real data says the fallback needs its own budget.
	SynthesisFallback time.Duration
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

// APIConfig configures the OpenAI-compatible engine (CLIApi). Most often this
// is the cheap-research role routed off the Claude CLI, but the same struct
// also configures the optional Chief Analyst DeepSeek fallback
// (orchestrator.Config.ChiefFallback): the heavy synthesis role's primary
// engine always stays the Claude CLI, and the fallback only fires as a
// last-resort resilience measure when that primary call fails.
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
	// Abstained lists the tickers this domain's sources answered for but had
	// nothing directional to say about. They are a subset of Ungrounded — both
	// end up in the report's `missing` array — but they are not coverage gaps
	// and do not degrade the run: sentiment declining to read a month of
	// scheduled insider disposals as a signal is the system working.
	Abstained []string `json:"abstained,omitempty"`
	// CorrectedScores lists shortlisted tickers the agent scored without any
	// verified data. The orchestrator deleted those scores from the report's
	// structured tail and moved the names into its `missing` array, so the
	// Chief Analyst never saw them. A long list means the model confabulated.
	CorrectedScores []string `json:"corrected_scores,omitempty"`
	// OffShortlistScores lists tickers the agent scored that were never on the
	// shortlist — hallucinated symbols, also deleted from the tail.
	OffShortlistScores []string `json:"off_shortlist_scores,omitempty"`
	// SelfContradictedScores lists tickers the agent placed in both `scores` and
	// its own `missing` array. The score is deleted: a report that disclaims its
	// own number should not have that number weighted.
	SelfContradictedScores []string `json:"self_contradicted_scores,omitempty"`
	// FabricatedCitations lists [source:] domains the agent cited on a
	// search-less engine. Non-empty means the report's sourcing was invented and
	// the orchestrator has rewritten those tags to [unverified].
	FabricatedCitations []string `json:"fabricated_citations,omitempty"`
	// ScoredNames is how many names the agent's structured tail scored before
	// any were removed. It is the denominator the corrected lists above are only
	// meaningful against: six deletions out of six is a domain that invented its
	// entire output, and six out of forty is a domain that overreached.
	ScoredNames int `json:"scored_names,omitempty"`
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

	// SynthesisFallbackEngine names the model the DeepSeek resilience fallback
	// actually ran on, set only when attemptChiefFallback was invoked (the
	// primary Chief Analyst call failed or its JSON did not parse). Empty means
	// it never fired — either unconfigured, or the primary call succeeded.
	SynthesisFallbackEngine string `json:"synthesis_fallback_engine,omitempty"`

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
	// PersonaSet names the directory those personas were loaded from ("agents",
	// "agents.v1"). The hashes identify the prompts; this says which arm of an
	// A/B comparison a run belongs to in words a person can read.
	PersonaSet string `json:"persona_set,omitempty"`
}
