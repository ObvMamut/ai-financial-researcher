package orchestrator

import (
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestEngineWebSearch(t *testing.T) {
	// The HTTP engine sends only model + messages: no tool declarations, so no
	// search. Personas that assume otherwise get a corrected capability block.
	if engineWebSearch(model.CLIApi) {
		t.Error("the OpenAI-compatible API engine has no search tool")
	}
	for _, e := range []model.CLI{model.CLIGemini, model.CLIClaude} {
		if !engineWebSearch(e) {
			t.Errorf("%s runs an agentic CLI and can search", e)
		}
	}
}

// The baseline run cited anandtech.com dated 2026-08-25 for a site that shut
// down in 2025. Tags the run's own data cannot vouch for get rewritten.
func TestScrubCitationsStripsInventedSources(t *testing.T) {
	citable := map[string]bool{"reuters.com": true, "data.sec.gov": true}
	report := strings.Join([]string{
		"NVDA rallied on strong demand [source:reuters.com 2026-08-28].",
		"Margins expanded [source:anandtech.com 2026-08-25].",
		"Filing confirms revenue [source:https://data.sec.gov/api/x 2026-08-01].",
		"Analysts agree [source:anandtech.com 2026-08-20].",
		"Something vague [source:].",
	}, "\n")

	cleaned, fabricated := scrubCitations(report, citable)

	if !strings.Contains(cleaned, "[source:reuters.com 2026-08-28]") {
		t.Error("a citation the pack vouches for must survive untouched")
	}
	if !strings.Contains(cleaned, "[source:https://data.sec.gov/api/x 2026-08-01]") {
		t.Error("a full URL whose host is citable must survive")
	}
	if strings.Contains(cleaned, "anandtech.com") {
		t.Errorf("an invented source survived scrubbing:\n%s", cleaned)
	}
	if n := strings.Count(cleaned, "[unverified]"); n != 3 {
		t.Errorf("want 3 rewritten tags, got %d:\n%s", n, cleaned)
	}

	// Each invented domain is reported once, sorted, for the run log.
	if len(fabricated) != 2 {
		t.Fatalf("fabricated = %v, want two distinct entries", fabricated)
	}
	if fabricated[0] != "(empty)" || fabricated[1] != "anandtech.com" {
		t.Errorf("fabricated = %v, want [(empty) anandtech.com]", fabricated)
	}
}

// A scout gets no data pack at all, so on a search-less engine every tag it
// writes is invented — and the shortlist would be picked on that invention.
func TestScrubCitationsWithNoPackStripsEverything(t *testing.T) {
	cleaned, fabricated := scrubCitations("Pick ADBE [source:barrons.com 2026-08-27].", nil)
	if strings.Contains(cleaned, "barrons.com") {
		t.Errorf("with no pack, nothing is citable:\n%s", cleaned)
	}
	if len(fabricated) != 1 {
		t.Errorf("fabricated = %v, want one entry", fabricated)
	}
}

func TestScrubCitationsLeavesCleanReportsAlone(t *testing.T) {
	report := "Momentum is 12.4% [verified]. No external sourcing was available."
	cleaned, fabricated := scrubCitations(report, map[string]bool{"reuters.com": true})
	if cleaned != report {
		t.Errorf("report was rewritten with no citations present:\n%s", cleaned)
	}
	if fabricated != nil {
		t.Errorf("fabricated = %v, want none", fabricated)
	}
}
