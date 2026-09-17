package tui

import (
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestResearchFailureVisibleInHistoricalResults(t *testing.T) {
	m := newResultsModel(&model.IdeasResult{ResearchMode: "thesis", Ideas: []model.TradeIdea{}, Decisions: []model.SelectionDecision{{Ticker: "AAA", Status: "watchlist", Blocked: model.BlockedResearchFailure, Reason: "A model said wait", ReviewReason: "Response budget failed; zero repairs"}}}, "fixture")
	m.meta = &model.RunMeta{ResearchOutcomes: []model.ResearchOutcome{{Transport: model.OutcomeOK, Parsing: model.OutcomeFailed, Review: model.ReviewUnavailable}}}
	view := m.View()
	for _, want := range []string{"Research failed for 1/1 companies", "no independent reviews completed", "research failed: Response budget failed; zero repairs"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q: %s", want, view)
		}
	}
}
