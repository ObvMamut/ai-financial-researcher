package tui

import (
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// chiefAgreementFixture is the internal/tui half of
// TestJSONTextAndTUIAgreeOnChiefAndResearchCounts. It must build the exact
// same run cmd/cfr/headless_test.go's own chiefAgreementFixture does — the two
// live in different packages (cmd/cfr is package main and cannot be imported
// here, nor can this package be imported there) so they cannot share a
// helper, but both tests assert the same facts derived through the same
// shared model.ChiefProvenanceLine/model.SummarizeResearch/
// model.ResearchRunIssues functions. Keep them in sync if you change one.
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

// TestJSONTextAndTUIAgreeOnChiefAndResearchCounts asserts the TUI results
// view reports exactly the facts model.ChiefProvenanceLine/
// model.SummarizeResearch/model.ResearchRunIssues compute from the same
// meta/ideas — the same functions cmd/cfr's text renderer calls (see that
// package's own test of the same name), so the two views cannot silently
// disagree about which engine answered or how research went.
func TestJSONTextAndTUIAgreeOnChiefAndResearchCounts(t *testing.T) {
	meta, ideas := chiefAgreementFixture()

	wantLine := model.ChiefProvenanceLine(meta)
	if wantLine == "" {
		t.Fatal("fixture produced an empty Chief provenance line")
	}
	wantSummary := model.SummarizeResearch(meta.ResearchOutcomes, ideas.Decisions)
	if wantSummary == nil {
		t.Fatal("fixture produced no research summary")
	}
	wantIssues := model.ResearchRunIssues(meta)
	if len(wantIssues) == 0 {
		t.Fatal("fixture produced no recovered-failure issues")
	}

	m := newResultsModel(ideas, "fixture")
	m.meta = meta
	view := m.View()

	if !strings.Contains(view, wantLine) {
		t.Errorf("TUI view missing Chief provenance line %q:\n%s", wantLine, view)
	}
	if !strings.Contains(view, wantSummary.String()) {
		t.Errorf("TUI view missing research summary %q:\n%s", wantSummary.String(), view)
	}
	for _, issue := range wantIssues {
		if !strings.Contains(view, issue) {
			t.Errorf("TUI view missing recovered-failure issue %q:\n%s", issue, view)
		}
	}
	for _, d := range ideas.Decisions {
		if !strings.Contains(view, d.Ticker) || !strings.Contains(view, d.Reason) {
			t.Errorf("TUI view missing decision %+v:\n%s", d, view)
		}
	}
}
