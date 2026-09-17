package orchestrator

import (
	"fmt"
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

// Writing targets are diagnostics, not evidence or schema failures. The total
// byte budget bounds every field together; one extra character cannot erase a thesis.
func dossierWritingDiagnostics(d *model.CandidateDossier) []string {
	var out []string
	if len(d.Claims) > 12 {
		out = append(out, fmt.Sprintf("claims: %d (writing target 12)", len(d.Claims)))
	}
	narratives := dossierNarratives(*d)
	for _, field := range dossierNarrativeFields {
		s := narratives[field]
		if n := utf8.RuneCountInString(s); n > 400 {
			out = append(out, fmt.Sprintf("%s: %d characters (writing target 400)", field, n))
		}
	}
	for _, c := range d.Claims {
		if len(c.Passages) > 2 {
			out = append(out, fmt.Sprintf("claims[%s].passages: %d (writing target 2)", c.ID, len(c.Passages)))
		}
		for i, p := range c.Passages {
			if n := utf8.RuneCountInString(p.Quote); n > 300 {
				out = append(out, fmt.Sprintf("claims[%s].passages[%d].quote: %d characters (writing target 300)", c.ID, i, n))
			}
		}
	}
	return out
}

func compactReviewProblems(d model.CandidateDossier, c model.ThesisChallenge) []string {
	var out []string
	if c.ContractVersion != 2 {
		return []string{"unsupported review contract version"}
	}
	if c.DossierHash != dossierHash(d) {
		out = append(out, "review does not match current dossier hash")
	}
	if c.Verdict == "supported" && (len(d.EntryConditions) > 0 || len(d.Monitoring) > 0) && !c.ConditionsReviewed {
		out = append(out, "entry conditions and monitoring require explicit review of core evidence sufficiency")
	}
	claims := map[string]bool{}
	for _, v := range d.Claims {
		claims[v.ID] = true
	}
	for _, extra := range c.Claims {
		if claims[extra.ID] {
			out = append(out, "additional review claim reuses dossier claim ID: "+extra.ID)
		}
	}
	seen := map[string]bool{}
	for _, r := range c.ClaimReviews {
		if !claims[r.ClaimID] || seen[r.ClaimID] {
			out = append(out, "unknown or repeated review claim: "+r.ClaimID)
		}
		seen[r.ClaimID] = true
		if strings.TrimSpace(r.Reason) == "" {
			out = append(out, "review must explain assessment: "+r.ClaimID)
		}
		if r.Assessment != "supported" && r.Assessment != "disputed" && r.Assessment != "unresolved" {
			out = append(out, "invalid claim assessment")
		}
		if r.Attribution != "confirmed" && r.Attribution != "disputed" && r.Attribution != "unresolved" {
			out = append(out, "invalid attribution assessment")
		}
		if c.Verdict == "supported" && (r.Assessment != "supported" || r.Attribution != "confirmed") {
			out = append(out, "supported review retains disputed or unresolved claim: "+r.ClaimID)
		}
	}
	for id := range claims {
		if !seen[id] {
			out = append(out, "review did not address claim "+id)
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
