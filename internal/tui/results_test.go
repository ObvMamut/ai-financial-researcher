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
		ResearchMode:  "thesis",
		Mode:          "independent",
		GeneratedAt:   "2026-09-17T00:00:00Z",
		Ideas:         []model.TradeIdea{},
		Decisions:     []model.SelectionDecision{{Ticker: "BBB", Status: "watchlist", Reason: "wait for confirmation"}},
		ChiefEngine:   meta.ChiefEngine,
		ChiefAccepted: meta.ChiefAccepted,
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

// TestResultsSayTheScreenHasNoEdge holds E1's and Wave D's consequence: a
// legacy independent run's ideas are the pre-screen's picks, and the view says
// no signal tested — that screen held 15, 21 or 63 sessions, 12-1 momentum,
// the model stages, drift — has shown an edge net of cost. Thesis ideas are not ranked
// by the screen, and an empty run has nothing ranked, so neither carries it.
func TestResultsSayTheScreenHasNoEdge(t *testing.T) {
	idea := model.TradeIdea{Rank: 1, Ticker: "AAA", Direction: model.DirectionBuy, Confidence: 50}
	legacy := newResultsModel(&model.IdeasResult{Mode: "independent", Ideas: []model.TradeIdea{idea}}, "runs/x").View()
	for _, want := range []string{"No signal tested has shown an edge", "held 15, 21 or 63 sessions", "survivorship-free US replay"} {
		if !strings.Contains(legacy, want) {
			t.Errorf("legacy independent results lack %q:\n%s", want, legacy)
		}
	}
	for name, ideas := range map[string]*model.IdeasResult{
		"thesis": {Mode: "independent", ResearchMode: "thesis", Ideas: []model.TradeIdea{idea}},
		"single": {Mode: "single", Ideas: []model.TradeIdea{idea}},
		"empty":  {Mode: "independent"},
	} {
		if v := newResultsModel(ideas, "runs/x").View(); strings.Contains(v, "has shown an edge") {
			t.Errorf("%s results carry the no-edge line", name)
		}
	}
}
