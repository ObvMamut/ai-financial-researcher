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

// ChiefProvenanceLine renders one human-readable line naming which engine the
// Chief Analyst was configured on, which engine(s) were actually attempted,
// and which one's output shipped. cmd/cfr's headless text renderer and the
// TUI results view both call this exact function on the same *RunMeta rather
// than composing their own strings, so the two surfaces cannot silently
// disagree about which engine actually produced a run's ideas.
//
// A historical run predating Task 6 has no ChiefEngine at all — chief_engine
// did not exist yet, so it could only ever have run on the claude CLI. That
// is display-labeled "claude (unrecorded)" rather than left blank or shown as
// a bare "claude", so it is never mistaken for a run whose provenance was
// actually confirmed.
func ChiefProvenanceLine(meta *RunMeta) string {
	if meta == nil {
		return ""
	}
	engine := meta.ChiefEngine
	if engine == "" {
		engine = "claude (unrecorded)"
	}
	line := "Chief: " + engine
	if meta.ChiefModel != "" {
		line += " (" + meta.ChiefModel + ")"
	}
	if meta.ChiefAttempted != "" && meta.ChiefAttempted != meta.ChiefEngine {
		line += " — attempted " + meta.ChiefAttempted
	}
	switch {
	case meta.ChiefAccepted == "":
		if meta.ChiefAttempted != "" {
			line += " — no engine's output accepted"
		}
	case meta.ChiefAccepted != meta.ChiefEngine:
		line += ", accepted from " + meta.ChiefAccepted
	}
	return line
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
