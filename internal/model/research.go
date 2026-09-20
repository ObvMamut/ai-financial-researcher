package model

import "time"

// ResearchConfig bounds the Go-managed research loop, independently of model tools.
type ResearchConfig struct {
	Budgets      ResearchBudgets `toml:"budgets" json:"budgets"`
	Rounds       int             `toml:"rounds" json:"rounds"`
	Documents    int             `toml:"documents" json:"documents"`
	Candidates   int             `toml:"candidates" json:"candidates"`
	Shortlist    int             `toml:"shortlist" json:"shortlist"`
	SourcesFile  string          `toml:"sources_file" json:"sources_file,omitempty"`
	HolidaysFile string          `toml:"holidays_file" json:"holidays_file,omitempty"`
}

func (c ResearchConfig) Defaults() ResearchConfig {
	c.Budgets = c.Budgets.Defaults()
	if c.Rounds == 0 {
		c.Rounds = 3
	}
	if c.Documents == 0 {
		c.Documents = 8
	}
	if c.Candidates == 0 {
		c.Candidates = 24
	}
	if c.Shortlist == 0 {
		c.Shortlist = 12
	}
	return c
}

// EvidenceDocument is a snapshot, not an endorsement of a source's interpretation.
// EventTime and ReportingPeriod must not be inferred from RetrievedAt.
type EvidenceDocument struct {
	NavigationOnly  bool           `json:"navigation_only,omitempty"` // discovery page, not a substantive source
	OmittedClaimIDs []string       `json:"omitted_claim_ids,omitempty"`
	Authority       string         `json:"authority,omitempty"` // Go-established issuer or depositary source; empty is unknown.
	SelectedSpans   []EvidenceSpan `json:"selected_spans,omitempty"`
	OmittedText     bool           `json:"omitted_text,omitempty"`
	ID              string         `json:"id"`
	Ticker          string         `json:"ticker"`
	URL             string         `json:"url,omitempty"`
	Title           string         `json:"title"`
	Kind            string         `json:"kind"` // headline, summary, document, fact, computed
	PublishedAt     time.Time      `json:"published_at,omitempty"`
	EventTime       string         `json:"event_time,omitempty"`
	ReportingPeriod string         `json:"reporting_period,omitempty"`
	RetrievedAt     time.Time      `json:"retrieved_at"`
	Text            string         `json:"text"`
	Links           []string       `json:"links,omitempty"`
	Error           string         `json:"error,omitempty"`
	Source          string         `json:"source,omitempty"`
	ParentID        string         `json:"parent_id,omitempty"`
	Truncated       bool           `json:"truncated,omitempty"`
}
type ResearchClaim struct {
	Comparison  *NumericalComparison `json:"comparison,omitempty"`
	ID          string               `json:"id"`
	Kind        string               `json:"kind"` // observation or inference
	Text        string               `json:"text"`
	EvidenceIDs []string             `json:"evidence_ids"`
	Passages    []ClaimPassage       `json:"passages,omitempty"`
}

// ClaimPassage preserves the words and issuer role used to ground a claim.
// Exact quotation can be checked mechanically; entailment still needs review.
type ClaimPassage struct {
	EvidenceID string `json:"evidence_id"`
	Quote      string `json:"quote,omitempty"`
	IssuerRole string `json:"issuer_role"`
}

// ResearchRequestKinds is the closed set of operations Go will act on. A kind
// outside it cannot be fulfilled by anything, so it is a schema error rather
// than a retrieval failure — the 2026-09-07 run spent 20 requests discovering
// that one at a time.
var ResearchRequestKinds = []string{"document", "news", "filings", "passage"}

func ValidRequestKind(kind string) bool {
	for _, k := range ResearchRequestKinds {
		if k == kind {
			return true
		}
	}
	return false
}

type ResearchRequest struct {
	ObservationDate string `json:"observation_date,omitempty"`
	Kind            string `json:"kind"` // document, news, filings, passage
	URL             string `json:"url,omitempty"`
	EvidenceID      string `json:"evidence_id,omitempty"`
	Query           string `json:"query,omitempty"` // literal text to locate in a saved document
	Question        string `json:"question"`
}

// Research request outcomes. Every request gets one, so a model can tell an
// answered question from an unanswerable one and stop repeating it.
const (
	RequestFulfilled   = "fulfilled"   // new evidence was added
	RequestUnsupported = "unsupported" // the operation does not exist
	RequestUnavailable = "unavailable" // the operation exists; the source did not answer
	RequestAlreadyDone = "already attempted"
	RequestExhausted   = "budget exhausted"
)

// ResearchResult answers one ResearchRequest. It is persisted and returned to
// the model, so an unsupported operation is stated once rather than rediscovered
// every round.
type ResearchResult struct {
	Repeats int             `json:"repeats,omitempty"`
	Request ResearchRequest `json:"request"`
	Outcome string          `json:"outcome"`
	Detail  string          `json:"detail,omitempty"`
	// EvidenceIDs names what the request actually added. Empty on every outcome
	// but fulfilled — and an empty list under "fulfilled" is a contradiction the
	// validator reports.
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
}
type CandidateDossier struct {
	EntryConditions      []string          `json:"entry_conditions,omitempty"`
	Monitoring           []string          `json:"monitoring,omitempty"`
	ContractVersion      int               `json:"contract_version,omitempty"`
	ExpectationsClaimIDs []string          `json:"expectations_claim_ids,omitempty"`
	PricedInClaimIDs     []string          `json:"priced_in_claim_ids,omitempty"`
	LongCase             string            `json:"long_case"`
	ShortCase            string            `json:"short_case"`
	NoTradeCase          string            `json:"no_trade_case"`
	PreferredDirection   string            `json:"preferred_direction"` // BUY, SELL, NONE
	Events               []ResearchEvent   `json:"events,omitempty"`
	Ticker               string            `json:"ticker"`
	Status               string            `json:"status"` // supported, watchlist, rejected
	Hypothesis           string            `json:"hypothesis"`
	Changed              string            `json:"changed"`
	Expectations         string            `json:"expectations"`
	Underappreciated     string            `json:"underappreciated"`
	Mechanism            string            `json:"mechanism"`
	PricedIn             string            `json:"priced_in"`
	Counterargument      string            `json:"counterargument"`
	Invalidation         string            `json:"invalidation"`
	CatalystWindow       string            `json:"catalyst_window"`
	EvidenceQuality      string            `json:"evidence_quality"` // strong, mixed, insufficient
	Claims               []ResearchClaim   `json:"claims"`
	Unresolved           []string          `json:"unresolved"`
	Requests             []ResearchRequest `json:"requests,omitempty"`
}
type ThesisChallenge struct {
	ConditionsReviewed   bool              `json:"conditions_reviewed,omitempty"`
	CompactionAssessment string            `json:"compaction_assessment,omitempty"`
	PlanHash             string            `json:"plan_hash,omitempty"`
	TargetAssessment     string            `json:"target_assessment,omitempty"` // supported, disputed, unresolved; final plan reviews only
	ContractVersion      int               `json:"contract_version,omitempty"`
	DossierHash          string            `json:"dossier_hash,omitempty"`
	ClaimReviews         []ClaimReview     `json:"claim_reviews,omitempty"`
	Ticker               string            `json:"ticker"`
	Verdict              string            `json:"verdict"` // supported, revise, reject
	Reason               string            `json:"reason"`
	Claims               []ResearchClaim   `json:"claims"`
	MaterialIssues       []string          `json:"material_issues"`
	Requests             []ResearchRequest `json:"requests,omitempty"`
}
type SelectionDecision struct {
	Ticker      string   `json:"ticker"`
	Status      string   `json:"status"` // actionable, conditional, watchlist, rejected
	Reason      string   `json:"reason"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	// Blocked names why a candidate is not a plan, separately from the prose
	// reason: research_failure, review_reject, risk, or empty when the selector
	// simply preferred other names. A company whose research never completed is
	// not a company the pipeline examined and turned down, and recording both as
	// "rejected" is what made the 2026-09-07 board unreadable.
	Blocked string `json:"blocked,omitempty"`
	// ReviewReason preserves the independent challenge's own words when they
	// are not the decision's reason. Overwriting the selector's reasoning with
	// the challenger's reinstated timing claims the selector had already
	// corrected.
	ReviewReason string `json:"review_reason,omitempty"`
}

// Reasons a researched candidate is not a plan. Blocked is the machine-readable
// half of a decision; Reason stays the human one.
const (
	BlockedResearchFailure = "research_failure"
	BlockedReviewReject    = "review_reject"
	BlockedRisk            = "risk"
	BlockedEvent           = "awaiting_event"
	BlockedPrices          = "awaiting_prices"
)

// ResearchOutcome separates the ways one candidate can fail to reach a thesis.
// A call that never returned, a payload that would not decode, an evidence base
// that stayed empty, and a thesis a reviewer actually found wanting are four
// different facts. Recorded as one they all read as a rejection, and a run
// whose research half failed looked like a run that had considered twelve
// companies and disliked ten of them.
type ResearchOutcome struct {
	Contract           string `json:"contract,omitempty"` // ok, compacted, failed
	RepairAttempts     int    `json:"repair_attempts"`
	CompactionAttempts int    `json:"compaction_attempts"`
	Ticker             string `json:"ticker"`
	// Transport: ok | failed | not_attempted — whether the model calls
	// themselves completed. not_attempted means dispatch was refused before
	// any call was made (an oversized prompt caught by the input-capacity
	// check): no transport ever ran, which is a different fact from one that
	// ran and failed.
	Transport string `json:"transport"`
	// Parsing: ok | repaired | failed | not_attempted — whether their
	// payloads decoded, and whether that took the one bounded schema-repair
	// call. not_attempted mirrors Transport: no payload was ever received to
	// parse, so nothing about parsing failed either.
	Parsing string `json:"parsing"`
	// Evidence: documents | thin | none — what the retrieval loop actually
	// reached, independently of what the model then said about it.
	Evidence string `json:"evidence"`
	// Review: supported | revise | reject | unavailable — the independent
	// challenge's verdict. "unavailable" is not a rejection.
	Review string `json:"review"`
	// Decision is the status this candidate ended the run with.
	Decision string   `json:"decision"`
	Notes    []string `json:"notes,omitempty"`
}

const (
	OutcomeOK       = "ok"
	OutcomeNotRun   = "not_run"
	OutcomeFailed   = "failed"
	OutcomeRepaired = "repaired"
	// OutcomeNotAttempted marks a call Go refused to dispatch on capacity
	// grounds — never a subprocess started, never an HTTP request sent. It is
	// deliberately NOT OutcomeNotRun: OutcomeNotRun means research was never
	// *intended* (a company blocked by an event window, excluded from
	// failure/deferred counts as a policy deferral); OutcomeNotAttempted
	// means research was intended and the prompt was built and measured, but
	// dispatch itself was refused, so the company still failed — only the
	// transport/parsing claim about *how* is corrected. Reusing OutcomeNotRun
	// here would silently move zero-attempt capacity failures out of every
	// failure count and into deferrals.
	OutcomeNotAttempted = "not_attempted"
	EvidenceDocuments   = "documents"
	EvidenceThin        = "thin"
	EvidenceNone        = "none"
	ReviewUnavailable   = "unavailable"
)

type ThesisPlan struct {
	Monitoring        []string `json:"monitoring,omitempty"`
	TargetMethod      string   `json:"target_method,omitempty"` // external_comparison or thesis_scenario
	TargetClaimIDs    []string `json:"target_claim_ids,omitempty"`
	WhyNow            string   `json:"why_now"`
	Invalidation      string   `json:"invalidation"`
	CatalystWindow    string   `json:"catalyst_window"`
	EvidenceQuality   string   `json:"evidence_quality"`
	EvidenceIDs       []string `json:"evidence_ids"`
	EntryReason       string   `json:"entry_reason"`
	StopReason        string   `json:"stop_reason"`
	TargetReason      string   `json:"target_reason"`
	OutcomeLow        float64  `json:"outcome_low"`
	OutcomeHigh       float64  `json:"outcome_high"`
	ExpiresOn         string   `json:"expires_on"`
	EntryExpiresOn    string   `json:"entry_expires_on"`
	Prerequisites     []string `json:"prerequisites,omitempty"`
	CalendarEstimated bool     `json:"calendar_estimated,omitempty"`
}

// ResearchEvent identifies a release time from a source passage, separately
// from its SEC filing date. Unknown timing must remain absent.
type ResearchEvent struct {
	Kind       string `json:"kind"`
	OccurredAt string `json:"occurred_at"` // RFC3339 with explicit offset
	EvidenceID string `json:"evidence_id"`
	Passage    string `json:"passage"` // verbatim source excerpt containing the date/time
}

// EvidenceSpan uses rune offsets into the persisted parent source text.
type EvidenceSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
