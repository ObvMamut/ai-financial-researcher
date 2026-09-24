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
	// Setup is the pre-screen archetype this name was ranked under —
	// "continuation", "pullback" or "base". Set by the orchestrator from the
	// candidate's own pre-screen row, never read from the scout's JSON: the
	// scout chooses which table to nominate from, but the label is the
	// classifier's to assign, and a scout that mislabels a name would otherwise
	// win itself a reserved shortlist slot.
	Setup string `json:"setup,omitempty"`

	// Nominations is how many scouts put this name forward in the same
	// direction, filled in by universe.Dedupe when it merges their lists. One is
	// the normal case; two is the strongest cross-index agreement the screening
	// stage can produce, and until it was counted nothing downstream could see
	// it. On 2026-09-03 REGN and AMGN were each nominated by two independent
	// scouts, the merit sort ranked purely on the pre-screen composite, and both
	// were dropped for names one scout had mentioned once.
	Nominations int `json:"nominations,omitempty"`
	// NominatedBy names the indices that put this ticker forward in that
	// direction, in collection order. Nominations is a count and cannot answer
	// whether the scouts were looking at different evidence: 35 of the 56 names
	// in nq100 are also in sp500, so two scouts "agreeing" on MU may be two
	// readings of one row. The orchestrator collapses those before the merit
	// sort pays for them; this is what it collapses on.
	NominatedBy []string `json:"nominated_by,omitempty"`
	// Contested names the indices that nominated this ticker in the *opposite*
	// direction, when two scouts disagreed. Dedupe keeps the first-seen reading
	// and records the collision here rather than resolving it silently: on
	// 2026-09-03 QCOM was nominated bullish by nq100 and bearish by sp500, and
	// which one shipped was decided by the order the scouts were collected in.
	Contested []string `json:"contested,omitempty"`
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
	FailureKind string
	Prompt      *PromptProfile
	Agent       string      // role name, e.g. "technicals", "scout-sp500"
	CLI         CLI         // gemini | claude
	Stage       Stage       // screening | analysis | synthesis
	Status      AgentStatus // done | failed
	Path        string      // runs/<ts>/<agent>.md
	Stdout      string      // raw captured output
	Err         string      // error text when Status == failed
	Duration    int64       // milliseconds
	Attempts    int         // subprocess attempts actually made (≥1)
	// Tokens sums reported completion tokens across attempts, including failed
	// attempts. Usage distinguishes unknown CLI counts from reported zeroes.
	Tokens int
	Usage  []TokenUsage
	// Omitted names the mandatory prompt sections implicated in an
	// input-capacity refusal (FailureKind == "input_capacity"): what the
	// requirement was that could not fit, not sections actually dropped from
	// an assembled prompt (see PromptProfile.Omitted for that — a different
	// fact, and empty in practice because production never runs
	// assembleSections under a real limit). nil on every other outcome.
	Omitted []string
}

// TradeIdea is one final deliverable: direction, confidence, rationale, and
// actionable trade mechanics. The mechanics fields are omitempty so ideas.json
// from runs predating them still parses.
type TradeIdea struct {
	ResearchMode string      `json:"research_mode,omitempty"`
	Status       string      `json:"status,omitempty"`
	Thesis       *ThesisPlan `json:"thesis,omitempty"`
	Rank         int         `json:"rank"`
	Ticker       string      `json:"ticker"`
	Name         string      `json:"name"`
	Index        string      `json:"index"`
	Direction    Direction   `json:"direction"`  // BUY | SELL
	Confidence   int         `json:"confidence"` // 0-100
	Why          string      `json:"why"`
	// Setup carries the pre-screen archetype through to the run artifact, so
	// the record can eventually answer whether pullback entries outperform
	// continuation ones. It is recorded, not yet scored on:
	// scoreboard.setupKey deliberately stays coarse until the closed-trade
	// count can fill the finer cells.
	Setup string `json:"setup,omitempty"`

	// EntryType says how the position is entered. EntryMarketOnOpen (every idea
	// generated since 2026-09-23) buys or sells at the next session's open, so
	// Entry is only the reference price the stop and sizing are measured from —
	// the verified last close. Absent means EntryLimit: an ideas.json written
	// before the field existed placed a resting limit at Entry, and the
	// scoreboard must keep replaying it that way.
	EntryType string  `json:"entry_type,omitempty"`
	Entry     float64 `json:"entry,omitempty"` // limit price, or the reference close for market_on_open
	// Stop is the protective stop. For a market_on_open idea it is a
	// catastrophe stop, never nearer than risk.catastrophe_stop_sigma·σ√h.
	Stop float64 `json:"stop,omitempty"`
	// Target is a take-profit limit for a limit idea. For a market_on_open idea
	// it is optional and informational: the position exits on time, because
	// every take-profit tested lowered the return (docs/workflow/scoring.md).
	Target        float64 `json:"target,omitempty"`
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
	// Consensus is how much of the evidence's magnitude survived the domains
	// disagreeing, |Σ w·sign·s| / Σ w·|s| ∈ [0, 1]. It is already inside
	// BaseConfidence and changes no ranking; it is recorded so the scoreboard
	// can ask whether thin-but-unanimous beats well-covered-but-split, which is
	// a question the single base number cannot be asked.
	Consensus float64 `json:"consensus,omitempty"`

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
	// ExpectancyR is the same quantity divided by the trade's own risk, which
	// is the unit the gate actually judges it in: expressed in basis points of
	// notional, expectancy is proportional to the stop distance, so the check
	// graded volatility rather than construction and refused the three
	// lowest-sigma names in the 2026-09-01 book while passing the two highest
	// on identical geometry.
	// BreakevenWinRate is the hit rate the geometry alone requires to break
	// even: risk / (risk + reward). An idea whose implied win rate is
	// implausible is a losing construction however good the thesis.
	ExpectancyBps    float64 `json:"expectancy_bps,omitempty"`
	ExpectancyR      float64 `json:"expectancy_r,omitempty"`
	BreakevenWinRate float64 `json:"breakeven_win_rate,omitempty"`
}

// Entry types. EntryLimit is also what an absent entry_type means.
const (
	EntryMarketOnOpen = "market_on_open"
	EntryLimit        = "limit"
)

// MarketOnOpen reports whether the idea enters at the next session's open
// rather than at a resting limit. An idea with no entry_type predates the
// field and was a limit.
func (t TradeIdea) MarketOnOpen() bool { return t.EntryType == EntryMarketOnOpen }

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
	// EntryPatienceSigma bounds a limit entry placed on the *waiting* side of
	// the last close — a long bidding below it, a short offering above — in
	// units of sigma_daily x sqrt(5). An entry on the other side is chasing and
	// keeps the tighter EntryChaseSigma.
	//
	// The two used to be one number, and one number could only be set to the
	// chasing width, since that is the side with a real cost. That is how the
	// 2026-09-04 run came to bid 442 for a stock at 444.12 sitting on its
	// 52-week high: waiting for a genuine pullback was not expressible.
	EntryPatienceSigma float64 `json:"entry_patience_sigma"`
	// EntryChaseSigma bounds a limit entry placed on the side the position is
	// already moving toward. Kept tight on purpose.
	//
	// Both entry bands bind only when EntryType is "limit". A market_on_open
	// idea has no limit to place: its entry is the verified last close and
	// its fill is the next open.
	EntryChaseSigma float64 `json:"entry_chase_sigma"`
	// EntryType is how new ideas enter: "market_on_open" (the default) or
	// "limit", the pre-2026-09-23 behaviour, kept selectable. In the live
	// record, limit ideas that never filled made +3.09% on the call while the
	// ones that did fill lost 0.90%: a patient limit fills exactly when the
	// move goes against it.
	EntryType string `json:"entry_type,omitempty"`
	// CatastropheStopSigma is the nearest a market_on_open idea's stop may sit,
	// in units of sigma_daily x sqrt(h) x the reference close. A tighter stop is
	// widened to it in Go. In a 4,080-trade backtest a stop at 2σ√h cost about
	// 0.1% a trade against no stop at all, while 1σ√h cost 0.22% and the old
	// ~9%/15% stop/target cost 0.41%.
	CatastropheStopSigma float64 `json:"catastrophe_stop_sigma,omitempty"`
	// ADVMinUSD is the 20-day average dollar volume below which a name is not
	// tradeable in size. It gates both the pre-screen and the final ideas.
	ADVMinUSD float64 `json:"adv_min_usd"`
	// MaxPairCorr is the pairwise return correlation above which two
	// same-direction ideas are treated as one position.
	MaxPairCorr float64 `json:"max_pair_corr"`
	// MaxPortfolioBeta bounds both the average absolute beta of the book and its
	// net signed beta.
	MaxPortfolioBeta float64 `json:"max_portfolio_beta"`
	// MaxPerSector is how many of the shipped ideas may share a sector before
	// the book is one bet in several tickets.
	//
	// It was a private constant in the risk gate, which meant the funnel that
	// selects the shortlist had no way to know the limit it was feeding. On
	// 2026-09-05 the merit sort returned a shortlist seven-twelfths Information
	// Technology, seven of the eight names that cleared the evidence floor were
	// IT, and this limit then cut the book to two ideas. Exported here so
	// universe.MeritCaps.PerSector can be derived from it and the two cannot
	// drift apart.
	MaxPerSector int `json:"max_per_sector"`
	// EdgeSigmaDaily is the daily expected return assumed in the expectancy
	// simulation, in units of sigma_daily. A driftless check is vacuous —
	// gambler's ruin makes EV about minus costs for any geometry — and an
	// optimistic one is vacuous the other way, so the prior is explicit and
	// configurable rather than hidden. P5 replaces it with realized hit rates.
	EdgeSigmaDaily float64 `json:"edge_sigma_daily"`
	// MinExpectancyR is the simulated expectancy, as a multiple of the trade's
	// own risk and net of costs, below which a geometry is refused. This is the
	// gate's primary expectancy test.
	//
	// It replaced a floor denominated in basis points of entry, which could not
	// do the job it was given. Expectancy in bps is proportional to the stop
	// distance, so the check reduced to roughly 200*sigma_daily(%)*days - cost:
	// on 2026-09-01 five ideas with near-identical geometry (stop ~1.3 sigma,
	// target ~2.6 sigma, R:R ~1.9) scored +28.7 bps down to -3.1 bps purely by
	// volatility, and the three calmest were dropped. Worse, the only lever the
	// re-prompt offered — "move the target out or the stop in" — makes the
	// number *worse*, because a tighter stop is touched more often.
	MinExpectancyR float64 `json:"min_expectancy_r"`
	// MinExpectancyBps is a secondary floor in basis points of entry, kept so an
	// operator who set one explicitly still gets it. Its default is zero: at
	// zero it refuses only a geometry that loses money outright, which is the
	// one thing the bps figure can honestly say.
	MinExpectancyBps float64 `json:"min_expectancy_bps"`

	// Explicit names the risk keys an operator set by hand, spelled as their
	// TOML keys ("cost_bps", "rr_min", …). Every field above is a float64 whose
	// zero value is also a legal setting, so without this the struct cannot tell
	// "the operator wants no cost assumption" from "nobody filled this in" — and
	// both the loader and riskDefaults resolved that ambiguity the same wrong
	// way, replacing an explicit `cost_bps = 0` with 30. A key listed here keeps
	// the value it was given, zero included.
	//
	// It is plumbing between config.Load and riskDefaults, not part of any
	// artifact, so it stays out of the JSON.
	Explicit map[string]bool `json:"-"`
}

// Set reports whether key was explicitly provided by the operator, and is the
// only thing that distinguishes a deliberate zero from an absent one.
func (c RiskConfig) Set(key string) bool { return c.Explicit[key] }

// IdeasResult is the Chief Analyst's final JSON payload.
type IdeasResult struct {
	ResearchSummary *ResearchSummary    `json:"research_summary,omitempty"`
	SchemaVersion   int                 `json:"schema_version,omitempty"`
	ResearchMode    string              `json:"research_mode,omitempty"`
	Decisions       []SelectionDecision `json:"decisions,omitempty"`
	Mode            string              `json:"mode"`
	GeneratedAt     string              `json:"generated_at"`
	Ideas           []TradeIdea         `json:"ideas"`
	Notes           string              `json:"notes"`

	// Selection names the policy that picked Ideas in a legacy independent
	// run (SelectionMeritVeto or SelectionChief); empty on runs that predate
	// it, which were all Chief-selected.
	Selection string `json:"selection,omitempty"`
	// ShadowRank is the Chief's own full ranking of the shortlist under
	// merit_veto selection. It is recorded, never acted on: the scoreboard's
	// chief-shadow arm scores its top names against what shipped.
	ShadowRank []string `json:"shadow_rank,omitempty"`

	// ChiefEngine and ChiefAccepted are Go-computed run metadata attached to
	// the Chief's parsed output, the same way ResearchSummary already is:
	// `cfr run --json` encodes only this struct on stdout (never RunMeta), so
	// without these two fields an operator reading that stdout had no way to
	// tell "five ideas" from "five ideas, from DeepSeek after Claude failed."
	// This is a deliberately compact subset of RunMeta's full four-field
	// provenance quartet (ChiefEngine/ChiefModel/ChiefAttempted/ChiefAccepted)
	// — an operator on stdout needs the configured primary and which engine's
	// output shipped; the model name and the full attempt trail stay in
	// RunMeta/metadata.json rather than being duplicated here.
	//
	// ChiefEngine present with ChiefAccepted empty is a known fact: a Chief
	// ran (or was deliberately skipped, e.g. thesis's all-research-failed
	// path) and nothing was accepted. ChiefEngine ALSO empty is a different
	// fact: this run predates these fields entirely and is unrecorded — see
	// RunMeta.ChiefEngine's own doc comment for the same distinction.
	ChiefEngine   string `json:"chief_engine,omitempty"`
	ChiefAccepted string `json:"chief_accepted,omitempty"`
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
	// NoRetryOnTimeout makes an attempt that ran out its per-call timeout
	// final (FailureKind "timeout") while other transient failures still
	// retry — for calls where slow means "too slow", not "flaky".
	NoRetryOnTimeout bool
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
	AlpacaKeyID     string
	AlpacaSecret    string
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
	// ReasoningEffort is sent as the request's reasoning_effort when set, and
	// omitted otherwise: not every OpenAI-compatible provider accepts it.
	ReasoningEffort string
	// CompactionEffort is the [chief_api] compaction_reasoning_effort value:
	// "" (provider default), "low", "high" or "max". Only dossier compaction
	// reads it; see compactionStatedTarget.
	CompactionEffort string
}

// DomainStatus tracks the outcome of a specialist research run.
type DomainStatus struct {
	Contract           string            `json:"contract,omitempty"` // ok, compacted, failed; separate from JSON parsing
	Recovery           string            `json:"recovery,omitempty"` // schema_repair or compaction
	WritingDiagnostics []string          `json:"writing_diagnostics,omitempty"`
	OriginalNarratives map[string]string `json:"original_narratives,omitempty"` // retained for independent compaction review
	// Allowance records what measureCompaction computed for a compaction
	// attempt: the measured per-field byte budget the model was given, not
	// the fixed character count earlier runs used. nil for every report that
	// is not a compaction (Recovery != "compaction").
	Allowance   *CompactionAllowance `json:"allowance,omitempty"`
	FailureKind string               `json:"failure_kind,omitempty"`
	Prompt      *PromptProfile       `json:"prompt,omitempty"`
	Domain      string               `json:"domain"`
	Status      AgentStatus          `json:"status"`
	Err         string               `json:"err,omitempty"`
	Grounded    bool                 `json:"grounded"` // true if per-ticker verified data was used
	Attempts    int                  `json:"attempts"`
	Duration    int64                `json:"duration_ms"`
	// Tokens is the compatibility total of reported completion tokens across
	// attempts. Usage retains per-attempt counts and missing-count information.
	Tokens int          `json:"tokens,omitempty"`
	Usage  []TokenUsage `json:"usage,omitempty"`

	// Payload distinguishes receiving a response from obtaining usable research:
	// "ok", "repaired" (the one bounded schema-repair call decoded it), or
	// "invalid" (it never decoded). Every call in the 2026-09-07 thesis run was
	// recorded `done` while eight of them carried nothing a decoder could read,
	// so metadata said the research had completed and the dossiers were empty.
	Payload string `json:"payload,omitempty"`

	// Omitted names the mandatory prompt requirements implicated when this
	// call was refused on input capacity before it ever dispatched
	// (FailureKind == "input_capacity", Attempts == 0) — what the September
	// 15 audit could not say without a byte-level re-derivation of the saved
	// prompt. It is not the list of sections actually dropped to make a
	// prompt fit (see PromptProfile.Omitted for that): production never
	// exercises real section-dropping, so that list is empty here in
	// practice, and Omitted names the mandatory content that made dropping
	// anything else pointless instead. Empty or absent means nothing to
	// report — every other outcome, and every artifact predating this field.
	Omitted []string `json:"omitted,omitempty"`

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
	// NeutralScores lists tickers the agent scored `neutral` on evidence it did
	// have. Nothing is deleted — a genuine standoff is a legitimate verdict —
	// but sign 0 contributes nothing to the weighted score while consuming the
	// domain's full weight, so it costs more than a gap and earns none of the
	// coverage cap relief a gap would. It is recorded because it was otherwise
	// indistinguishable from a name the domain simply had nothing on.
	NeutralScores []string `json:"neutral_scores,omitempty"`
	// FabricatedCitations lists [source:] domains the agent cited on a
	// search-less engine. Non-empty means the report's sourcing was invented and
	// the orchestrator has rewritten those tags to [unverified].
	FabricatedCitations []string `json:"fabricated_citations,omitempty"`
	// ScoredNames is how many names the agent's structured tail scored before
	// any were removed. It is the denominator the corrected lists above are only
	// meaningful against: six deletions out of six is a domain that invented its
	// entire output, and six out of forty is a domain that overreached.
	ScoredNames int `json:"scored_names,omitempty"`
	// Corrective records what became of the one corrective re-prompt, when one
	// was spent: "applied", "unparseable" or "failed". Empty means none was
	// attempted.
	//
	// The re-prompt used to be able to fail in total silence. When the second
	// call returned but its JSON did not parse there was no branch at all — no
	// log, no warning — and the status row still read `status: done, attempts:
	// 2`. On 2026-09-01 that spent a five-minute synthesis call for nothing
	// while ideas.json announced "after one corrective re-prompt" beside a book
	// that was in fact the uncorrected first pass.
	Corrective string `json:"corrective,omitempty"`
}

// RunMeta captures all parameters and outcomes of a run for audit.
type RunMeta struct {
	SchemaVersion int    `json:"schema_version,omitempty"`
	ResearchMode  string `json:"research_mode,omitempty"`
	// Selection is the legacy independent selection policy the run used
	// (merit_veto or chief); empty for thesis, single-stock and older runs.
	Selection   string         `json:"selection,omitempty"`
	Research    ResearchConfig `json:"research,omitempty"`
	Mode        string         `json:"mode"`
	Ticker      string         `json:"ticker,omitempty"`
	Indices     []string       `json:"indices,omitempty"` // indices screened (independent mode)
	GeneratedAt string         `json:"generated_at"`
	Shortlist   []Candidate    `json:"shortlist"`
	Domains     []DomainStatus `json:"domains"`
	Weights     DomainWeights  `json:"weights"`
	// ThinlyCovered names the shortlisted tickers the run's sources can ground
	// less than 60% of the domain weight for: SEC filings and listed option
	// chains are US instruments, so fundamentals and sentiment cannot reach a
	// foreign listing with no US line. This is a known structural limit,
	// deliberately kept out of Warnings — an expected limit is not a warning.
	//
	// It was `quant_only` until news and macro became globally groundable, at
	// which point "no provider reaches this at all" was true of nothing and the
	// field would have been empty on every run while three of five domains were
	// still missing on some names.
	ThinlyCovered []string `json:"thinly_covered,omitempty"`
	Warnings      []string `json:"warnings"`
	Outcome       string   `json:"outcome"` // complete | degraded | failed
	Duration      int64    `json:"total_duration_ms"`

	// Engine names the cheap-research engine the scouts and specialists ran on
	// ("gemini" | "api"), and EngineModel the model it was pointed at.
	// SynthesisModel is the model the Chief Analyst call whose output shipped
	// actually ran on — Claude's configured model on an ordinary run, or the
	// DeepSeek fallback's model when the primary failed and the fallback
	// rescued it. It is no longer assumed to be Claude: before Task 6 this was
	// unconditionally cfg.Models[model.CLIClaude], which reported "opus" for a
	// run chief_engine="api" or the fallback actually answered. Without these,
	// a run's artifacts do not record what produced them: two runs five hours
	// apart with wildly different macro reads were indistinguishable in
	// metadata.json.
	Engine         string `json:"engine,omitempty"`
	EngineModel    string `json:"engine_model,omitempty"`
	SynthesisModel string `json:"synthesis_model,omitempty"`

	// ChiefEngine, ChiefModel, ChiefAttempted and ChiefAccepted are Task 6's
	// additive provenance quartet, kept alongside SynthesisModel (never
	// replacing it — old readers and the scoreboard cohort key keep working
	// unchanged) because "which engine produced this run's ideas" is three
	// genuinely different facts, not one:
	//
	//   - ChiefEngine is the CONFIGURED primary ("claude" or "api", from
	//     resolveChiefEngine/cfg.ChiefEngine). It never changes because a
	//     fallback happened to rescue the run — a successful DeepSeek rescue
	//     does not retroactively make "api" the primary.
	//   - ChiefModel is the model name of whichever engine's output actually
	//     shipped: the primary's resolved model normally, or the fallback's
	//     model when the primary failed and the fallback's output was
	//     accepted. Equal to SynthesisModel going forward; grouped here with
	//     the other three for a reader who wants the whole trail in one place.
	//   - ChiefAttempted lists, comma-joined in call order, every engine
	//     actually dispatched: just the primary ("claude") on an ordinary run,
	//     or "claude,api" when the primary failed (or its JSON did not parse)
	//     and the DeepSeek fallback was attempted after it. Empty when the
	//     Chief was never dispatched at all (e.g. thesis's all-research-failed
	//     path, which skips synthesis entirely).
	//   - ChiefAccepted names the single engine whose output shipped — empty
	//     when neither the primary nor an attempted fallback produced usable
	//     ideas (the mechanical buildDegradedIdeas/empty-board path, or
	//     thesis's all-research-failed path, which skips the Chief entirely).
	//
	// A successful fallback must never erase the primary's failure: that stays
	// recorded in Domains ("chief-analyst", status failed) exactly as before;
	// these fields are additive context, not a replacement for it.
	//
	// An empty ChiefAccepted is deliberately not disambiguated by giving it a
	// second value (e.g. a "none" sentinel) — empty plus omitempty already
	// means "absent," and a second vocabulary word would need defining
	// everywhere the field is read. The disambiguation instead lives in
	// ChiefEngine, which the reader MUST check alongside ChiefAccepted rather
	// than inferring from ChiefAccepted alone:
	//
	//   - ChiefEngine present, ChiefAccepted empty: a KNOWN fact — a Chief
	//     ran (or was deliberately skipped, e.g. thesis's all-failed path)
	//     and nothing was accepted. The run recorded provenance; nothing
	//     shipped from a model.
	//   - ChiefEngine ALSO empty: this run predates these fields entirely —
	//     UNRECORDED, never backfilled, never a default substituted after the
	//     fact. Historical runs could only ever have run the Chief on the
	//     claude CLI (chief_engine did not exist yet), so this case is
	//     display-labeled "claude (unrecorded)" by callers rather than shown
	//     as a bare "claude" or left to look like the first case — see
	//     ChiefProvenanceLine and the scoreboard cohort key.
	ChiefEngine    string `json:"chief_engine,omitempty"`
	ChiefModel     string `json:"chief_model,omitempty"`
	ChiefAttempted string `json:"chief_attempted,omitempty"`
	ChiefAccepted  string `json:"chief_accepted,omitempty"`

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

	// ResearchOutcomes records, per researched candidate, whether its calls
	// completed, whether their payloads decoded, what evidence retrieval
	// actually reached and what the independent review said. Those are four
	// separate facts; a single final status cannot carry them, and reading a
	// research failure as a rejection is what this separates.
	ResearchOutcomes []ResearchOutcome `json:"research_outcomes,omitempty"`

	// DataErrors collects every provider failure encountered while assembling
	// the packs. These previously lived only in data/<domain>.json, so a run
	// that lost eight tickers to rate limiting read the same as one that lost
	// none.
	DataErrors        []string           `json:"data_errors,omitempty"`
	SourceDiagnostics []SourceDiagnostic `json:"source_diagnostics,omitempty"`

	// PersonaSHA maps each agent role to a short hash of the persona file used.
	// Personas are runtime data, editable without a code change, so this is what
	// makes a run's outcome attributable to the prompts that produced it.
	PersonaSHA map[string]string `json:"persona_sha,omitempty"`
	// PersonaSet names the directory those personas were loaded from ("agents",
	// "agents.v1"). The hashes identify the prompts; this says which arm of an
	// A/B comparison a run belongs to in words a person can read.
	PersonaSet string `json:"persona_set,omitempty"`
}
