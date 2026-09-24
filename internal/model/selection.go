package model

import "strings"

// Selection policies for legacy independent research: who decides which
// shortlisted names ship.
//
// SelectionMeritVeto ranks the shortlist by the pre-screen merit the funnel
// already computed, removes the names a specialist or the Chief vetoed and the
// names the risk gate rejects, and ships the top of what is left in the scout's
// direction. The Chief writes the prose and records its own ranking as a shadow.
// SelectionChief is the old behaviour — the Chief ranks, prices and picks —
// kept selectable so the two can be compared.
const (
	SelectionMeritVeto = "merit_veto"
	SelectionChief     = "chief"
)

// ValidSelection reports whether s names a selection policy.
func ValidSelection(s string) bool {
	return s == SelectionMeritVeto || s == SelectionChief
}

// Closed veto reasons. A veto outside this list is not a veto: an open reason
// field is how a labeller turns into a picker again.
const (
	VetoBinaryEventInsideWindow = "binary_event_inside_window"
	VetoCorporateActionPending  = "corporate_action_pending"
	VetoHaltedOrIlliquid        = "halted_or_illiquid"
	VetoDataError               = "data_error"
	VetoFraudOrLitigationShock  = "fraud_or_litigation_shock"
)

// VetoReasons lists the closed enum in a stable order, for prompts and docs.
var VetoReasons = []string{
	VetoBinaryEventInsideWindow,
	VetoCorporateActionPending,
	VetoHaltedOrIlliquid,
	VetoDataError,
	VetoFraudOrLitigationShock,
}

// NormalizeVetoReason returns the canonical reason, or "" when s is not one.
func NormalizeVetoReason(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, r := range VetoReasons {
		if s == r {
			return r
		}
	}
	return ""
}

// Move drivers: what a specialist says moved the name recently.
const (
	MoveDriverNews     = "news"
	MoveDriverEarnings = "earnings"
	MoveDriverNone     = "none"
	MoveDriverUnknown  = "unknown"
)

// NormalizeMoveDriver maps anything outside the enum to "unknown".
func NormalizeMoveDriver(s string) string {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case MoveDriverNews, MoveDriverEarnings, MoveDriverNone:
		return v
	default:
		return MoveDriverUnknown
	}
}

// BinaryEvent is a specialist's read on a scheduled binary event inside the
// holding window. Present is nil when the specialist did not say.
type BinaryEvent struct {
	Present *bool  `json:"present,omitempty"`
	Date    string `json:"date,omitempty"` // YYYY-MM-DD, only when stated and well-formed
}

// NameLabels are one specialist's structured labels for one shortlisted name.
// Every field has an "unknown" state, and unknown is never a veto.
type NameLabels struct {
	MoveDriver         string       `json:"move_driver"`
	PendingBinaryEvent *BinaryEvent `json:"pending_binary_event,omitempty"`
	CorporateAction    *bool        `json:"corporate_action,omitempty"`
	Veto               bool         `json:"veto,omitempty"`
	VetoReason         string       `json:"veto_reason,omitempty"`
	Note               string       `json:"note,omitempty"`
}

// Veto is one recorded refusal of a name: who refused it and why.
type Veto struct {
	Source string `json:"source"` // a specialist domain, or "chief"
	Reason string `json:"reason"` // one of VetoReasons
	Note   string `json:"note,omitempty"`
}

// SelectionRow is everything the selection stage knew about one shortlisted
// name. It is persisted for every name — shipped or not — so the scoreboard can
// build shadow arms over the whole shortlist rather than the five that shipped.
type SelectionRow struct {
	Ticker string `json:"ticker"`
	Name   string `json:"name,omitempty"`
	Index  string `json:"index,omitempty"`
	Sector string `json:"sector,omitempty"`
	Setup  string `json:"setup,omitempty"`
	// Direction is the scout's direction; empty for a neutral nomination.
	Direction Direction `json:"direction,omitempty"`
	// Close is the verified last close the name was selected off.
	Close     float64 `json:"close,omitempty"`
	Merit     float64 `json:"merit"`
	MeritRank int     `json:"merit_rank"`
	// Labels maps a specialist domain to the labels it gave this name.
	Labels map[string]NameLabels `json:"labels,omitempty"`
	Vetoes []Veto                `json:"vetoes,omitempty"`
	Vetoed bool                  `json:"vetoed"`
	// RiskGate is the hard risk-gate finding that made the name ineligible.
	RiskGate string `json:"risk_gate,omitempty"`
	// Excluded says why an unshipped name did not ship: no_direction,
	// vetoed, risk_gate, sector_cap or below_cut. Empty for a shipped name.
	Excluded        string         `json:"excluded,omitempty"`
	Selected        bool           `json:"selected"`
	ShippedRank     int            `json:"shipped_rank,omitempty"`
	ChiefShadowRank int            `json:"chief_shadow_rank,omitempty"`
	DomainScores    map[string]int `json:"domain_scores,omitempty"`
	BaseConfidence  int            `json:"base_confidence,omitempty"`
}

// SelectionRecord is data/selection.json: the selection stage's full account
// of one legacy independent run.
type SelectionRecord struct {
	Policy string `json:"policy"`
	TopN   int    `json:"top_n"`
	// Chief is what became of the Chief call in merit_veto mode: accepted,
	// failed, unparseable, or skipped. Empty under the chief policy.
	Chief string `json:"chief,omitempty"`
	// ShadowRank is the Chief's own full ranking of the shortlist, recorded and
	// never acted on.
	ShadowRank []string       `json:"shadow_rank,omitempty"`
	Regime     []string       `json:"regime,omitempty"`
	Rows       []SelectionRow `json:"rows"`
}
