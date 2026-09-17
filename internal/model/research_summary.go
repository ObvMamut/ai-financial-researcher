package model

import "fmt"

// ResearchSummary is computed by Go, never trusted from the selector's response.
// Nil on older artifacts means these measurements were not persisted.
type ResearchSummary struct {
	Shortlisted int `json:"shortlisted"`
	Researched  int `json:"researched"`
	Reviewed    int `json:"reviewed"`
	Supported   int `json:"supported"`
	Actionable  int `json:"actionable"`
	Conditional int `json:"conditional"`
	Rejected    int `json:"rejected"`
	Failed      int `json:"failed"`
	Deferred    int `json:"deferred"`
}

func SummarizeResearch(outcomes []ResearchOutcome, decisions []SelectionDecision) *ResearchSummary {
	if outcomes == nil {
		return nil
	}
	s := &ResearchSummary{Shortlisted: len(outcomes)}
	for _, o := range outcomes {
		if o.Transport == OutcomeNotRun {
			s.Deferred++
			continue
		}
		readable := o.Transport == OutcomeOK && (o.Parsing == OutcomeOK || o.Parsing == OutcomeRepaired) && o.Contract != OutcomeFailed && o.Evidence != EvidenceNone
		if readable {
			s.Researched++
		}
		reviewed := o.Review == "supported" || o.Review == "revise" || o.Review == "reject"
		if reviewed {
			s.Reviewed++
		}
		if !readable || !reviewed {
			s.Failed++
		}
		if readable && o.Review == "supported" {
			s.Supported++
		}
	}
	for _, d := range decisions {
		switch d.Status {
		case "actionable":
			s.Actionable++
		case "conditional":
			s.Conditional++
		case "rejected":
			s.Rejected++
		}
	}
	return s
}

func (s ResearchSummary) String() string {
	counts := fmt.Sprintf("Shortlisted %d · researched %d · reviewed %d · supported %d · actionable %d · conditional %d · rejected %d · failed %d · deferred %d", s.Shortlisted, s.Researched, s.Reviewed, s.Supported, s.Actionable, s.Conditional, s.Rejected, s.Failed, s.Deferred)
	if s.Failed > 0 && s.Reviewed == 0 {
		return fmt.Sprintf("Research failed for %d/%d companies; no independent reviews completed.\n%s", s.Failed, s.Shortlisted, counts)
	}
	return counts
}

func ResearchRunIssues(meta *RunMeta) []string {
	if meta == nil {
		return nil
	}
	var issues []string
	for _, call := range meta.Domains {
		if call.Domain == "chief-analyst" && call.Status == StatusFailed {
			issues = append(issues, "Primary Chief unavailable: "+call.Err)
		}
		if call.Domain == "chief-analyst-fallback" && call.Status == StatusDone && call.Payload != "invalid" {
			issues = append(issues, "Configured Chief fallback completed; primary failure remains recorded.")
		}
	}
	return issues
}
