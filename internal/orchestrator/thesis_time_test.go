package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

func TestReleaseSessionSkipsHolidayWeekendAndEarlyClose(t *testing.T) {
	for _, tc := range []struct{ at, want string }{
		{"2026-09-06T12:00:00-04:00", "2026-09-08"},
		{"2026-09-04T16:00:00-04:00", "2026-09-08"},
		{"2026-11-27T13:01:00-05:00", "2026-11-30"},
		{"2026-11-27T12:59:00-05:00", "2026-11-27"},
	} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		got, estimated := releaseSessionCalendar("AAA", at, marketdata.ResearchCalendar{})
		if got.Format("2006-01-02") != tc.want || estimated {
			t.Errorf("%s: got %s estimated=%v", tc.at, got, estimated)
		}
	}
}

func TestWeekendReleaseWithoutPricesStaysOnWatchlist(t *testing.T) {
	r := supportedResearch()
	quote := "On September 6, 2026 at 12:00 EDT, the issuer announced its quarterly earnings."
	r.Documents[0].Text = quote
	r.Dossier.Events = []model.ResearchEvent{{Kind: "earnings", OccurredAt: "2026-09-06T12:00:00-04:00", EvidenceID: r.Documents[0].ID, Passage: quote}}
	p := quant.NewPack()
	p.ByTicker["AAA"] = quant.Metrics{SigmaDaily: .02, Benchmark: "^GSPC"}
	s := &quant.Series{Bars: []quant.Bar{{Date: "2026-09-03", Close: 100}, {Date: "2026-09-04", Close: 101}}}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	addReleaseReactions(&r, p, map[string]*quant.Series{"AAA": s, "^GSPC": s}, now)
	if r.Eligibility != model.BlockedPrices || r.Dossier.Status != "watchlist" {
		t.Fatalf("missing reaction treated as research conclusion: %+v", r)
	}
	res := &model.IdeasResult{Decisions: []model.SelectionDecision{{Ticker: "AAA", Status: "rejected", Reason: "no reaction"}}}
	finalizeThesis(res, []thesisResearch{r}, nil, verified{}, Config{}, now)
	if res.Decisions[0].Status != "watchlist" || res.Decisions[0].Blocked != model.BlockedPrices {
		t.Fatalf("lost awaiting-prices condition: %+v", res.Decisions)
	}
}

func TestTemporalFactsAlibabaAndStellantisDates(t *testing.T) {
	// Hong Kong is already September 8: the local calendar has 12 future sessions.
	now := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		ticker, event string
		sessions      int
	}{
		{"9988.HK", "2026-09-24", 12}, {"STLAM.MI", "2026-09-08", 1},
	} {
		r := thesisResearch{Candidate: model.Candidate{Ticker: tc.ticker}, NextEvent: tc.event,
			Documents: []model.EvidenceDocument{{ID: "holdings", RetrievedAt: now}}}
		facts := temporalFacts(r, now, marketdata.ResearchCalendar{}, nil)
		e := facts.Events[0]
		if e.Ordering != "future" || !e.InsideWindow || e.SessionsAway != tc.sessions || !e.CalendarEstimated {
			t.Fatalf("incorrect ordering: %+v", e)
		}
		if facts.EvidenceAges[0].Hours != nil {
			t.Fatal("retrieval time became publication time")
		}
	}
}

type nearEarningsProvider struct{ researchFixtureProvider }

func (nearEarningsProvider) Fetch(_ context.Context, domain, ticker string) (marketdata.TickerData, error) {
	return marketdata.TickerData{Ticker: ticker, Facts: []marketdata.Fact{{Label: marketdata.EarningsFactLabel, Value: "2026-09-08", Source: "fixture"}}}, nil
}

func TestEarlyEarningsWatchlistSkipsModelResearch(t *testing.T) {
	runner, qp, close := thesisFixture(t, func(_ string, _ int) string { t.Error("blocked company reached model"); return "" })
	defer close()
	runner.run.TS = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	runner.svc = marketdata.NewService(nil, nearEarningsProvider{})
	r := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA"}, nil, qp, nil)
	if r.Eligibility != model.BlockedEvent || r.researchFailed() || len(r.Reports) != 0 || r.Outcome.Review != model.OutcomeNotRun {
		t.Fatalf("incorrect preflight: %+v", r)
	}
	res := &model.IdeasResult{Decisions: []model.SelectionDecision{{Ticker: "AAA", Status: "rejected", Reason: "no thesis"}}}
	finalizeThesis(res, []thesisResearch{r}, nil, verified{}, runner.cfg, runner.run.TS)
	if d := res.Decisions[0]; d.Status != "watchlist" || d.Blocked != model.BlockedEvent || !strings.Contains(d.Reason, "post-event") {
		t.Fatalf("lost post-event condition: %+v", d)
	}
}
