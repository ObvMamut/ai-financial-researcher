package scoreboard

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestSelectionShadowArmsScoreTheChiefRankingAndTheVetoes(t *testing.T) {
	f := newControlFixture(t)
	up, next, third, loser := fixtureTicker(11), fixtureTicker(10), fixtureTicker(9), fixtureTicker(0)
	anchor := 150
	closeAt := func(tk string) float64 { return closeOnOrBefore(f.prices[tk], f.dates[anchor]) }
	shipped := model.TradeIdea{Rank: 1, Ticker: up, Index: "sp500", Direction: model.DirectionBuy, PriceAtGeneration: closeAt(up)}
	f.addRun(t, "2025-merit", anchor, "", []model.TradeIdea{shipped},
		[]model.Candidate{{Ticker: up, Index: "sp500", Bias: model.BiasBullish}}, nil)
	writeJSON(t, filepath.Join(f.runsDir, "2025-merit"), "data/selection.json", model.SelectionRecord{
		Policy: model.SelectionMeritVeto, TopN: 2,
		ShadowRank: []string{third, next, fixtureTicker(5), up},
		Rows: []model.SelectionRow{
			{Ticker: up, Index: "sp500", Direction: model.DirectionBuy, Close: closeAt(up), MeritRank: 1, Selected: true, ShippedRank: 1, ChiefShadowRank: 4},
			{Ticker: next, Index: "sp500", Direction: model.DirectionBuy, Close: closeAt(next), MeritRank: 2, ChiefShadowRank: 2, Excluded: "below_cut"},
			{Ticker: third, Index: "sp500", Direction: model.DirectionBuy, Close: closeAt(third), MeritRank: 3, ChiefShadowRank: 1, Excluded: "below_cut"},
			// No scout direction: ranked by the Chief but not a call either arm can score.
			{Ticker: fixtureTicker(5), Index: "sp500", ChiefShadowRank: 3, Excluded: "no_direction", Vetoed: true,
				Vetoes: []model.Veto{{Source: "news", Reason: model.VetoDataError}}},
			// A long on the name that falls hardest, vetoed: a veto doing its job.
			{Ticker: loser, Index: "sp500", Direction: model.DirectionBuy, Close: closeAt(loser), MeritRank: 4, Excluded: "vetoed", Vetoed: true,
				Vetoes: []model.Veto{{Source: "chief", Reason: model.VetoFraudOrLitigationShock}}},
		},
	})
	// A second week, so the difference has an interval to report.
	f.addRun(t, "2025-merit-b", anchor+7, "", []model.TradeIdea{{Rank: 1, Ticker: up, Index: "sp500",
		Direction: model.DirectionBuy, PriceAtGeneration: closeOnOrBefore(f.prices[up], f.dates[anchor+7])}}, nil, nil)
	// A chief-policy run carries a vetoed name but no shadow ranking.
	f.addRun(t, "2025-chief", anchor+14, "", nil, nil, nil)
	writeJSON(t, filepath.Join(f.runsDir, "2025-chief"), "data/selection.json", model.SelectionRecord{
		Policy: model.SelectionChief, TopN: 5,
		Rows: []model.SelectionRow{{Ticker: loser, Index: "sp500", Direction: model.DirectionBuy, MeritRank: 1, Vetoed: true,
			Vetoes: []model.Veto{{Source: "sentiment", Reason: model.VetoCorporateActionPending}}}},
	})

	rep, err := ControlWithOptions(context.Background(), f.runsDir, f.prices, ControlOptions{Horizon: 10})
	if err != nil {
		t.Fatal(err)
	}
	arms := map[string]ControlArm{}
	for _, a := range rep.Arms {
		arms[a.Name] = a
	}
	var shadow []string
	for _, e := range arms["chief-shadow"].Entries {
		shadow = append(shadow, e.Ticker)
	}
	if strings.Join(shadow, ",") != third+","+next {
		t.Errorf("chief-shadow = %v, want the Chief's top two directional names %s,%s", shadow, third, next)
	}
	v := arms["vetoed"]
	if v.Record.N != 2 || v.Entries[0].Ticker != loser || v.Entries[1].Ticker != loser {
		t.Fatalf("vetoed arm n=%d, want %s from both policies' runs, two weeks apart", v.Record.N, loser)
	}
	if v.Record.AvgExcess >= arms["shipped"].Record.AvgExcess {
		t.Errorf("the vetoed loser (%+.2f%%) should trail what shipped (%+.2f%%)", v.Record.AvgExcess, arms["shipped"].Record.AvgExcess)
	}
	diffs := map[string]ArmDiff{}
	for _, d := range rep.Diffs {
		diffs[d.Over+"-"+d.Under] = d
	}
	if _, ok := diffs["shipped-chief-shadow"]; !ok {
		t.Errorf("no shipped − chief-shadow difference in %+v", rep.Diffs)
	}
	if d, ok := diffs["vetoed-shipped"]; !ok || d.Excess >= 0 {
		t.Errorf("vetoed − shipped = %+v, want a negative difference", d)
	}
	text := rep.FormatText()
	for _, want := range []string{"chief-shadow", "vetoed", "Merit over the Chief's ranking:"} {
		if !strings.Contains(text, want) {
			t.Errorf("text report lacks %q:\n%s", want, text)
		}
	}
}
