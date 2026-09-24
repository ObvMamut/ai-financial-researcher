package orchestrator

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func dossierLimits(d *model.CandidateDossier) []string {
	if len(d.Requests) > 3 {
		return []string{"request budget: at most 3 requests per round"}
	}
	return nil
}

// Writing targets, sized so a complete dossier fits its response budget
// without compaction: 58% of researched names in the September 23 record never
// produced a readable dossier, and shrinking what is asked for replaced
// engineering ever more compaction around a twelve-claim, 400-character ask.
const (
	writingTargetClaims    = 6
	writingTargetNarrative = 250 // characters per narrative field
	writingTargetPassages  = 1   // cited sources, each quoted, per claim
	writingTargetQuote     = 300 // characters per quotation
)

// Writing targets are diagnostics, not evidence or schema failures. The total
// byte budget bounds every field together; one extra character cannot erase a thesis.
func dossierWritingDiagnostics(d *model.CandidateDossier) []string {
	var out []string
	if len(d.Claims) > writingTargetClaims {
		out = append(out, fmt.Sprintf("claims: %d (writing target %d)", len(d.Claims), writingTargetClaims))
	}
	narratives := dossierNarratives(*d)
	for _, field := range dossierNarrativeFields {
		s := narratives[field]
		if n := utf8.RuneCountInString(s); n > writingTargetNarrative {
			out = append(out, fmt.Sprintf("%s: %d characters (writing target %d)", field, n, writingTargetNarrative))
		}
	}
	for _, c := range d.Claims {
		if len(c.Passages) > writingTargetPassages {
			out = append(out, fmt.Sprintf("claims[%s].passages: %d (writing target %d)", c.ID, len(c.Passages), writingTargetPassages))
		}
		for i, p := range c.Passages {
			if n := utf8.RuneCountInString(p.Quote); n > writingTargetQuote {
				out = append(out, fmt.Sprintf("claims[%s].passages[%d].quote: %d characters (writing target %d)", c.ID, i, n, writingTargetQuote))
			}
		}
	}
	return out
}

// issue is a material issue Go raises itself, always with its category.
func issue(category, text string) model.MaterialIssue {
	return model.MaterialIssue{Category: category, Issue: text}
}

// appendIssue adds m unless an issue with the same text is already present.
func appendIssue(xs []model.MaterialIssue, m model.MaterialIssue) []model.MaterialIssue {
	for _, x := range xs {
		if x.Issue == m.Issue {
			return xs
		}
	}
	return append(xs, m)
}

// compactReviewProblems checks a contract-v2 review against the dossier it
// names. Every problem it raises is categorised: a malformed review is `form`,
// and a claim the review disputes — or a core claim it cannot support — is
// `grounding` or `attribution`. All of those block. A non-core claim the review
// leaves unresolved raises nothing here; it is a disclosed risk
// (disclosedRisks), which is what lets a thesis with one unanswerable side
// question ship with that question stated rather than not ship at all.
func compactReviewProblems(d model.CandidateDossier, c model.ThesisChallenge) []model.MaterialIssue {
	var out []model.MaterialIssue
	if c.ContractVersion != 2 {
		return []model.MaterialIssue{issue(model.IssueForm, "unsupported review contract version")}
	}
	if c.DossierHash != dossierHash(d) {
		out = append(out, issue(model.IssueForm, "review does not match current dossier hash"))
	}
	if c.Verdict == "supported" && (len(d.EntryConditions) > 0 || len(d.Monitoring) > 0) && !c.ConditionsReviewed {
		out = append(out, issue(model.IssueForm, "entry conditions and monitoring require explicit review of core evidence sufficiency"))
	}
	claims := map[string]bool{}
	for _, v := range d.Claims {
		claims[v.ID] = true
	}
	core := model.CoreClaimIDs(d)
	for _, extra := range c.Claims {
		if claims[extra.ID] {
			out = append(out, issue(model.IssueForm, "additional review claim reuses dossier claim ID: "+extra.ID))
		}
	}
	seen := map[string]bool{}
	for _, r := range c.ClaimReviews {
		if !claims[r.ClaimID] || seen[r.ClaimID] {
			out = append(out, issue(model.IssueForm, "unknown or repeated review claim: "+r.ClaimID))
		}
		seen[r.ClaimID] = true
		if strings.TrimSpace(r.Reason) == "" {
			out = append(out, issue(model.IssueForm, "review must explain assessment: "+r.ClaimID))
		}
		if r.Assessment != "supported" && r.Assessment != "disputed" && r.Assessment != "unresolved" {
			out = append(out, issue(model.IssueForm, "invalid claim assessment"))
		}
		if r.Attribution != "confirmed" && r.Attribution != "disputed" && r.Attribution != "unresolved" {
			out = append(out, issue(model.IssueForm, "invalid attribution assessment"))
		}
		// No claim may be disputed, core or not: a disputed claim is evidence
		// the dossier misread, not uncertainty it disclosed.
		if r.Assessment == "disputed" {
			out = append(out, issue(model.IssueGrounding, "review disputes claim "+r.ClaimID))
		}
		if r.Attribution == "disputed" {
			out = append(out, issue(model.IssueAttribution, "review disputes the attribution of claim "+r.ClaimID))
		}
		// Only core claims must be supported and attribution-confirmed.
		if core[r.ClaimID] && r.Assessment == "unresolved" {
			out = append(out, issue(model.IssueGrounding, "core claim "+r.ClaimID+" is not supported by the review"))
		}
		if core[r.ClaimID] && r.Attribution == "unresolved" {
			out = append(out, issue(model.IssueAttribution, "core claim "+r.ClaimID+" attribution is not confirmed by the review"))
		}
	}
	// Every claim still has to be addressed, core or not.
	for id := range claims {
		if !seen[id] {
			out = append(out, issue(model.IssueForm, "review did not address claim "+id))
		}
	}
	return out
}

// reviewBlockers applies the categorised definition of a supported review and
// returns why it does not hold, or nothing when it does. It reads the issues
// recorded on the review; callers append the consistency problems
// (validateReviewConsistency) when a review arrives, because the dossier hash
// those check is the one the review was written against.
//
// Supported means: no blocking-category issue, no pending request, every core
// claim supported and attribution-confirmed and no claim disputed (both raised
// as issues by compactReviewProblems). A contract-v2 `revise` whose remaining
// issues are all disclosed risks passes: the verdict word is the challenger's,
// the gate is Go's, and an objection no free source can answer is disclosed
// rather than resolved. `reject`, an unavailable review, and any historical
// review without a contract version keep their old meaning — only `supported`
// passes those, and their uncategorised issues all block.
func reviewBlockers(c model.ThesisChallenge) []string {
	var out []string
	switch {
	case c.Verdict == "supported":
	case c.Verdict == "revise" && c.ContractVersion == 2:
	default:
		out = append(out, "review verdict is "+strconv.Quote(c.Verdict))
	}
	for _, m := range c.MaterialIssues {
		if m.Blocking() {
			out = appendUnique(out, m.Issue)
		}
	}
	if len(c.Requests) > 0 {
		out = append(out, "review requests remain unanswered")
	}
	if !reviewHasClaims(c) {
		out = append(out, "review addressed no claims")
	}
	return out
}

// finalReviewBlockers is reviewBlockers for a review nothing follows — the
// final research challenge and the plan review. Their requests cannot be
// served: no retrieval round comes after them. MRK's supported final review on
// 2026-09-24 still asked two questions and was held off supported by them
// alone. They are disclosed as unanswered questions (disclosedRisks) instead.
func finalReviewBlockers(c model.ThesisChallenge) []string {
	c.Requests = nil
	return reviewBlockers(c)
}

func hasBlockingIssue(c model.ThesisChallenge) bool {
	for _, m := range c.MaterialIssues {
		if m.Blocking() {
			return true
		}
	}
	return false
}

// disclosedRisks is the uncertainty a supported dossier ships with: its own
// unresolved gaps, Go's non-blocking validation notes, the review's
// disclosed-risk issues and every non-core claim the review left unresolved.
// Hidden uncertainty is what the gate forbids; disclosed uncertainty is what
// this list carries into the plan.
func disclosedRisks(d model.CandidateDossier, c model.ThesisChallenge, notes []string) []string {
	var out []string
	for _, s := range append(append([]string(nil), d.Unresolved...), notes...) {
		if s = strings.TrimSpace(s); s != "" {
			out = appendUnique(out, s)
		}
	}
	for _, m := range c.MaterialIssues {
		if !m.Blocking() {
			out = appendUnique(out, m.Category+": "+m.Issue)
		}
	}
	for _, r := range c.Requests {
		if q := strings.TrimSpace(r.Question); q != "" {
			out = appendUnique(out, "unanswered_request: "+q)
		}
	}
	core := model.CoreClaimIDs(d)
	for _, r := range c.ClaimReviews {
		if core[r.ClaimID] || r.Assessment == "disputed" || r.Attribution == "disputed" {
			continue
		}
		if r.Assessment == "unresolved" || r.Attribution == "unresolved" {
			out = appendUnique(out, "claim "+r.ClaimID+" unresolved: "+r.Reason)
		}
	}
	return out
}

func reviewHasClaims(c model.ThesisChallenge) bool {
	return len(c.Claims) > 0 || c.ContractVersion == 2 && len(c.ClaimReviews) > 0
}

func reasoningReferences(d model.CandidateDossier) []string {
	if d.ContractVersion < 2 {
		return nil
	} // historical artifacts remain readable
	var out []string
	by := map[string]bool{}
	for _, c := range d.Claims {
		by[c.ID] = true
	}
	for label, ids := range map[string][]string{"expectations": d.ExpectationsClaimIDs, "priced_in": d.PricedInClaimIDs} {
		if len(ids) == 0 {
			out = append(out, label+" needs cited observation or explicitly labeled inference claim references")
		}
		for _, id := range ids {
			if !by[id] {
				out = append(out, fmt.Sprintf("%s references unknown claim %s", label, id))
			}
		}
	}
	return out
}

func targetProvenanceProblems(d model.CandidateDossier, plan model.ThesisPlan) []string {
	var out []string
	if d.ContractVersion == 2 {
		switch plan.TargetMethod {
		case "external_comparison":
			if len(plan.TargetClaimIDs) == 0 {
				out = append(out, "external target comparison requires target_claim_ids")
			}
		case "thesis_scenario":
			if len(plan.TargetClaimIDs) > 0 {
				out = append(out, "numerical comparison references require external_comparison target_method")
			}
		default:
			out = append(out, "target_method must explicitly identify external_comparison or thesis_scenario")
		}
	}
	by := map[string]model.ResearchClaim{}
	for _, c := range d.Claims {
		by[c.ID] = c
	}
	for _, id := range plan.TargetClaimIDs {
		c, ok := by[id]
		if !ok || c.Comparison == nil || c.Comparison.NormalizedTarget == nil || c.Comparison.Problem != "" {
			out = append(out, "target references an unresolved numerical comparison: "+id)
		}
	}
	return out
}
