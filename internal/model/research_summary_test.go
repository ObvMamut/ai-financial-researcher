package model

import (
	"strings"
	"testing"
)

func TestResearchSummarySeparatesFailureDeferralAndDecisions(t *testing.T) {
	outcomes := []ResearchOutcome{
		{Transport: OutcomeOK, Parsing: OutcomeOK, Contract: "compacted", Review: "supported"},
		{Transport: OutcomeOK, Parsing: OutcomeOK, Review: "reject"},
		{Transport: OutcomeOK, Parsing: OutcomeOK, Contract: OutcomeFailed, Review: ReviewUnavailable},
		{Transport: OutcomeNotRun, Parsing: OutcomeNotRun, Review: OutcomeNotRun},
	}
	s := SummarizeResearch(outcomes, []SelectionDecision{{Status: "conditional"}, {Status: "rejected"}, {Status: "watchlist"}, {Status: "watchlist"}})
	if s.Shortlisted != 4 || s.Researched != 2 || s.Reviewed != 2 || s.Supported != 1 || s.Conditional != 1 || s.Rejected != 1 || s.Failed != 1 || s.Deferred != 1 {
		t.Fatal(s)
	}
	failed := SummarizeResearch(outcomes[2:3], nil)
	if !strings.Contains(failed.String(), "Research failed for 1/1 companies; no independent reviews completed") {
		t.Fatal(failed.String())
	}
	if SummarizeResearch(nil, nil) != nil {
		t.Fatal("invented historical measurements")
	}
	missing := SummarizeResearch([]ResearchOutcome{{Transport: OutcomeOK, Parsing: OutcomeOK, Review: "supported", Evidence: EvidenceNone}}, nil)
	if missing.Failed != 1 || missing.Researched != 0 || missing.Supported != 0 {
		t.Fatalf("research over no evidence counted as usable: %+v", missing)
	}
}

func TestChiefProvenanceLineDistinguishesConfiguredAttemptedAccepted(t *testing.T) {
	if got := ChiefProvenanceLine(nil); got != "" {
		t.Fatalf("nil meta: got %q, want empty", got)
	}
	if got := ChiefProvenanceLine(&RunMeta{SynthesisModel: "opus"}); got != "Chief: claude (unrecorded)" {
		t.Fatalf("historical run with no ChiefEngine: got %q", got)
	}
	clean := ChiefProvenanceLine(&RunMeta{ChiefEngine: "claude", ChiefModel: "opus", ChiefAttempted: "claude", ChiefAccepted: "claude"})
	if clean != "Chief: claude (opus)" {
		t.Fatalf("clean primary success: got %q", clean)
	}
	fellBack := ChiefProvenanceLine(&RunMeta{ChiefEngine: "claude", ChiefModel: "deepseek-reasoner", ChiefAttempted: "claude,api", ChiefAccepted: "api"})
	for _, want := range []string{"Chief: claude", "deepseek-reasoner", "attempted claude,api", "accepted from api"} {
		if !strings.Contains(fellBack, want) {
			t.Fatalf("fallback line missing %q: %s", want, fellBack)
		}
	}
	nothingAccepted := ChiefProvenanceLine(&RunMeta{ChiefEngine: "claude", ChiefModel: "opus", ChiefAttempted: "claude,api", ChiefAccepted: ""})
	if !strings.Contains(nothingAccepted, "no engine's output accepted") {
		t.Fatalf("degraded run should say nothing was accepted: %s", nothingAccepted)
	}
}
