package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// chiefAgreementFixture builds the one fixture run
// TestJSONTextAndTUIAgreeOnChiefAndResearchCounts uses to check that the text
// renderer (printIdeasText, below), the JSON artifact (metadata.json/
// ideas.json — simulated here by an actual json.Marshal/Unmarshal round trip)
// and the TUI results view (internal/tui/results_test.go's own copy of this
// test) all report the same facts about which Chief engine answered and how
// research went. The two test files build this exact fixture independently
// (cmd/cfr is package main and cannot be imported by internal/tui, nor vice
// versa) rather than sharing a helper across that boundary — keep them in
// sync if you change one.
func chiefAgreementFixture() (*model.RunMeta, *model.IdeasResult) {
	meta := &model.RunMeta{
		ChiefEngine:    "claude",
		ChiefModel:     "opus",
		ChiefAttempted: "claude,api",
		ChiefAccepted:  "api",
		Domains: []model.DomainStatus{
			{Domain: "chief-analyst", Status: model.StatusFailed, Err: "boom"},
			{Domain: "chief-analyst-fallback", Status: model.StatusDone},
		},
		ResearchOutcomes: []model.ResearchOutcome{
			{Transport: model.OutcomeOK, Parsing: model.OutcomeOK, Review: "supported"},
			{Transport: model.OutcomeOK, Parsing: model.OutcomeFailed, Review: model.ReviewUnavailable},
		},
	}
	ideas := &model.IdeasResult{
		ResearchMode: "thesis",
		Mode:         "independent",
		GeneratedAt:  "2026-09-17T00:00:00Z",
		Ideas:        []model.TradeIdea{},
		Decisions:    []model.SelectionDecision{{Ticker: "BBB", Status: "watchlist", Reason: "wait for confirmation"}},
	}
	return meta, ideas
}

// TestJSONTextAndTUIAgreeOnChiefAndResearchCounts asserts headless's text
// renderer reports exactly the facts the JSON artifact (metadata.json/
// ideas.json, simulated here by a real marshal/unmarshal round trip) carries:
// the primary engine (and what was attempted/accepted), recovered failures,
// and the research/decision counts. Before Task 6 neither surface said
// anything at all about which engine answered — this pins that they now
// agree, not just that each independently prints something.
func TestJSONTextAndTUIAgreeOnChiefAndResearchCounts(t *testing.T) {
	meta, ideas := chiefAgreementFixture()

	metaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrippedMeta model.RunMeta
	if err := json.Unmarshal(metaJSON, &roundTrippedMeta); err != nil {
		t.Fatal(err)
	}
	ideasJSON, err := json.Marshal(ideas)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrippedIdeas model.IdeasResult
	if err := json.Unmarshal(ideasJSON, &roundTrippedIdeas); err != nil {
		t.Fatal(err)
	}

	jsonLine := model.ChiefProvenanceLine(&roundTrippedMeta)
	if jsonLine == "" {
		t.Fatal("fixture produced an empty Chief provenance line")
	}
	jsonSummary := model.SummarizeResearch(roundTrippedMeta.ResearchOutcomes, roundTrippedIdeas.Decisions)
	if jsonSummary == nil {
		t.Fatal("fixture produced no research summary")
	}
	jsonIssues := model.ResearchRunIssues(&roundTrippedMeta)
	if len(jsonIssues) == 0 {
		t.Fatal("fixture produced no recovered-failure issues")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	printIdeasText(ideas, meta)
	w.Close()
	os.Stdout = saved
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)

	if !strings.Contains(text, jsonLine) {
		t.Errorf("text renderer missing Chief provenance line %q:\n%s", jsonLine, text)
	}
	if !strings.Contains(text, jsonSummary.String()) {
		t.Errorf("text renderer missing research summary %q:\n%s", jsonSummary.String(), text)
	}
	for _, issue := range jsonIssues {
		if !strings.Contains(text, issue) {
			t.Errorf("text renderer missing recovered-failure issue %q:\n%s", issue, text)
		}
	}
	for _, d := range roundTrippedIdeas.Decisions {
		if !strings.Contains(text, d.Ticker) || !strings.Contains(text, d.Reason) {
			t.Errorf("text renderer missing decision %+v:\n%s", d, text)
		}
	}
}
