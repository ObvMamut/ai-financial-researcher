package model

import "fmt"

// PromptProfile measures the exact redacted prompt, independently of provider usage.
type PromptProfile struct {
	VisibleEvidence  map[string][]string `json:"visible_evidence,omitempty"`
	Version          int                 `json:"version"`
	Bytes            int                 `json:"bytes"`
	EstimatedTokens  int                 `json:"estimated_tokens"`
	EstimateMethod   string              `json:"estimate_method"`
	InputLimit       int                 `json:"input_limit_bytes"`
	ResponseLimit    int                 `json:"response_limit_bytes"`
	OutputTokenLimit int                 `json:"output_token_limit,omitempty"`
	SHA256           string              `json:"sha256"`
	// Components is a flat, non-overlapping partition of the prompt's bytes:
	// "instructions" (the persona wrapper and response-contract line, i.e.
	// everything in the prompt that is not the data block) plus one entry per
	// named promptSection assembleSections placed in the data block — never a
	// coarser aggregate alongside its own children. sum(Components) always
	// equals Bytes exactly, with no residual and no double-counted overlap.
	// A section assembleSections dropped for capacity has no entry here; see
	// Omitted for its name instead.
	Components map[string]int `json:"components_bytes"`
	// Omitted names, in drop order, every optional promptSection assembleSections
	// removed from the data block to make it fit. Unlike the numeric fields
	// on this struct, a name list has no "measured zero" distinct from
	// "unrecorded" — there is nothing a dropped section could contribute
	// other than its name, so an absent or empty Omitted means exactly what
	// it says: nothing was dropped, whether because none needed to be or
	// because this profile predates the field.
	Omitted     []string `json:"omitted,omitempty"`
	Compactions []string `json:"compactions,omitempty"`

	// ResponseContractVersion and Response are additive: a profile stored by a
	// run that predates them has neither field, and an absent
	// ResponseContractVersion means "unrecorded" — this response was never
	// measured under a versioned contract — never "measured zero" or "measured
	// under version 0". Do not backfill either field onto a historical
	// artifact; a past run's acceptance is never recomputed under a later
	// rule. This is NOT the profile's own Version above, which is the prompt
	// profile's schema version, currently 1, and stays 1.
	ResponseContractVersion int              `json:"response_contract_version,omitempty"`
	Response                *ResponseMeasure `json:"response,omitempty"`
}

// ResponseMeasure records both byte counts a response can be judged by: the
// complete raw response (RawBytes, exactly len(stdout)) and the normalized
// size of the fenced JSON payload it actually carries (PayloadBytes) — the
// bytes json.Compact produces from that payload, which is what the response
// capacity budget bounds as of ResponseContractVersion 2. An agent's
// formatting whitespace (indentation, wrapped narrative lines) inflates
// RawBytes without adding any information the model spent budget producing,
// so gating on RawBytes alone charged a response for whitespace it never
// asked to be measured on.
//
// Normalized is false when no fenced JSON payload could be extracted, or
// extraction succeeded but the extracted bytes did not compact (malformed
// JSON, e.g. an unescaped raw newline inside a string). In either case
// PayloadBytes falls back to RawBytes: a response that cannot be normalized
// must not gain a spurious capacity pass by appearing smaller than it is.
// That fallback is a gate value, not a provenance claim: the hash and method
// fields carry no equivalent fallback (see below), because reporting a
// payload hash/method for a response that has no payload would assert that
// the payload IS the raw response, which is not a fact.
//
// RawSHA256 hashes the complete raw response (stdout) and is always set.
// PayloadSHA256 hashes the compacted payload bytes and Method names the
// normalization ("json.Compact of the fenced payload"); both are set only
// when Normalized is true, and are the empty string otherwise — absent,
// never a fabricated equivalence with the raw response.
type ResponseMeasure struct {
	RawBytes      int    `json:"raw_bytes"`
	PayloadBytes  int    `json:"payload_bytes"`
	Method        string `json:"normalization"` // "json.Compact of the fenced payload"; "" when !Normalized
	RawSHA256     string `json:"raw_sha256"`
	PayloadSHA256 string `json:"payload_sha256"` // "" when !Normalized; never falls back to RawSHA256
	Normalized    bool   `json:"normalized"`     // false when no payload could be extracted or compacted
}

// CompactionAllowance records the measured room a compaction call actually
// has, replacing a fixed "aim for under 400 characters each" instruction that
// was, on the six real responses that needed it, between 121 bytes too
// generous and 668 bytes too tight — never the right number for any of them
// (see internal/orchestrator/thesis_compaction.go's measureCompaction).
//
// PayloadBytes and Limit describe the ORIGINAL oversized response being
// compacted (PayloadBytes is that response's own normalized payload size,
// the same measure ResponseMeasure.PayloadBytes uses). ProtectedBytes is the
// floor: that same payload with every one of the twelve narrative fields
// emptied — the bytes a compaction call may never touch. NarrativeBudget is
// what remains for those twelve fields combined, after Limit, ProtectedBytes
// and a measured headroom are accounted for; PerField splits it across the
// fields that are currently non-empty. Feasible is false when even the
// per-field floors cannot fit inside NarrativeBudget — a call that must not
// be dispatched, because no rewrite of the narrative fields alone can reach
// the budget.
type CompactionAllowance struct {
	PayloadBytes    int            `json:"payload_bytes"`
	Limit           int            `json:"limit"`
	ProtectedBytes  int            `json:"protected_bytes"`  // payload with all 12 narratives emptied
	Excess          int            `json:"original_excess"`  // PayloadBytes - Limit
	NarrativeBudget int            `json:"narrative_budget"` // Limit - ProtectedBytes - headroom
	PerField        map[string]int `json:"per_field"`        // bytes, not characters
	Feasible        bool           `json:"feasible"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"` // effort the compaction call was sent with, if any
}

type RoleBudget struct {
	InputBytes    int `toml:"input_bytes" json:"input_bytes"`
	ResponseBytes int `toml:"response_bytes" json:"response_bytes"`
}

type ResearchBudgets struct {
	Triage     RoleBudget `toml:"triage" json:"triage"`
	Researcher RoleBudget `toml:"researcher" json:"researcher"`
	Challenger RoleBudget `toml:"challenger" json:"challenger"`
	Chief      RoleBudget `toml:"chief" json:"chief"`
}

func (b ResearchBudgets) Defaults() ResearchBudgets {
	for _, v := range []struct {
		p       *RoleBudget
		in, out int
	}{{&b.Triage, 96 << 10, 12 << 10}, {&b.Researcher, 96 << 10, 32 << 10}, {&b.Challenger, 96 << 10, 16 << 10}, {&b.Chief, 192 << 10, 24 << 10}} {
		if v.p.InputBytes == 0 {
			v.p.InputBytes = v.in
		}
		if v.p.ResponseBytes == 0 {
			v.p.ResponseBytes = v.out
		}
	}
	return b
}
func (b ResearchBudgets) ForRole(role string) RoleBudget {
	b = b.Defaults()
	switch role {
	case "thesis-triage":
		return b.Triage
	case "thesis-chief":
		return b.Chief
	case "thesis-challenger":
		return b.Challenger
	default:
		return b.Researcher
	}
}
func (b ResearchBudgets) Validate() error {
	for _, v := range []RoleBudget{b.Triage, b.Researcher, b.Challenger, b.Chief} {
		if v.InputBytes < 4096 || v.InputBytes > 2<<20 || v.ResponseBytes < 1024 || v.ResponseBytes > 128<<10 || v.ResponseBytes >= v.InputBytes {
			return fmt.Errorf("research role budgets require 4096–2097152 input bytes, 1024–131072 response bytes, and response < input")
		}
	}
	return nil
}

// ClaimReview references the exact dossier hash instead of duplicating its prose.
type ClaimReview struct {
	ClaimID     string `json:"claim_id"`
	Assessment  string `json:"assessment"`  // supported, disputed, unresolved
	Attribution string `json:"attribution"` // confirmed, disputed, unresolved
	Reason      string `json:"reason"`
}

// NumericalComparison stores inputs. Go supplies NormalizedTarget and UpsidePct.
// OrdinarySharesPerADS is directional, never an ambiguous conversion multiplier.
type NumericalComparison struct {
	SourceTicker         string       `json:"source_ticker"`
	TargetTicker         string       `json:"target_ticker"`
	SourceCurrency       string       `json:"source_currency"`
	TargetCurrency       string       `json:"target_currency"`
	SourceUnit           string       `json:"source_unit"` // major or minor
	TargetUnit           string       `json:"target_unit"`
	SourceBasis          string       `json:"source_basis"` // share or ADS
	TargetBasis          string       `json:"target_basis"`
	Target               float64      `json:"target"`
	TargetPublishedOn    string       `json:"target_published_on"`
	TargetHorizon        string       `json:"target_horizon"`
	PriceDate            string       `json:"price_date"`
	OrdinarySharesPerADS float64      `json:"ordinary_shares_per_ads,omitempty"`
	RatioEffectiveOn     string       `json:"ratio_effective_on,omitempty"`
	RatioEvidenceID      string       `json:"ratio_evidence_id,omitempty"`
	RatioPassage         string       `json:"ratio_passage,omitempty"`
	NormalizedTarget     *float64     `json:"normalized_target,omitempty"`
	UpsidePct            *float64     `json:"upside_pct,omitempty"`
	FX                   []ResearchFX `json:"fx,omitempty"`
	Problem              string       `json:"problem,omitempty"`
}
type ResearchFX struct {
	Currency   string  `json:"currency"`
	USDPerUnit float64 `json:"usd_per_unit"`
	Date       string  `json:"date"`
	Source     string  `json:"source"`
}
