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

func TestSectorCappedArmAndShippedSplitByPolicy(t *testing.T) {
	f := newControlFixture(t)
	up, capped, loser := fixtureTicker(11), fixtureTicker(10), fixtureTicker(0)
	anchor := 150
	closeAt := func(tk string, at int) float64 { return closeOnOrBefore(f.prices[tk], f.dates[at]) }

	// A merit_veto run: one idea ships, one sector_cap row carries a scout
	// direction (a call the arm can score), one carries none (not a call).
	f.addRun(t, "2025-merit", anchor, "", []model.TradeIdea{
		{Rank: 1, Ticker: up, Index: "sp500", Direction: model.DirectionBuy, PriceAtGeneration: closeAt(up, anchor)},
	}, []model.Candidate{{Ticker: up, Index: "sp500", Bias: model.BiasBullish}}, nil)
	writeJSON(t, filepath.Join(f.runsDir, "2025-merit"), "ideas.json", model.IdeasResult{
		GeneratedAt: f.dates[anchor] + "T22:00:00Z",
		Ideas:       []model.TradeIdea{{Rank: 1, Ticker: up, Index: "sp500", Direction: model.DirectionBuy, PriceAtGeneration: closeAt(up, anchor)}},
		Selection:   model.SelectionMeritVeto,
	})
	writeJSON(t, filepath.Join(f.runsDir, "2025-merit"), "data/selection.json", model.SelectionRecord{
		Policy: model.SelectionMeritVeto, TopN: 1,
		Rows: []model.SelectionRow{
			{Ticker: up, Index: "sp500", Direction: model.DirectionBuy, Close: closeAt(up, anchor), MeritRank: 1, Selected: true, ShippedRank: 1},
			{Ticker: capped, Index: "sp500", Direction: model.DirectionBuy, Close: closeAt(capped, anchor), MeritRank: 2, Excluded: "sector_cap"},
			// No scout direction: the sector cap dropped it, but neither arm can score it.
			{Ticker: loser, Index: "sp500", MeritRank: 3, Excluded: "sector_cap"},
		},
	})
	// A second week, so the sector-capped arm carries more than one bet.
	f.addRun(t, "2025-merit-b", anchor+7, "", nil, nil, nil)
	writeJSON(t, filepath.Join(f.runsDir, "2025-merit-b"), "data/selection.json", model.SelectionRecord{
		Policy: model.SelectionMeritVeto, TopN: 1,
		Rows: []model.SelectionRow{
			{Ticker: loser, Index: "sp500", Direction: model.DirectionSell, Close: closeAt(loser, anchor+7), MeritRank: 1, Excluded: "sector_cap"},
		},
	})
	// A chief-policy run: no sector cap fires here, but its shipped idea must
	// land in the chief split, not merit_veto's.
	f.addRun(t, "2025-chief", anchor+14, "", nil, nil, nil)
	writeJSON(t, filepath.Join(f.runsDir, "2025-chief"), "ideas.json", model.IdeasResult{
		GeneratedAt: f.dates[anchor+14] + "T22:00:00Z",
		Ideas:       []model.TradeIdea{{Rank: 1, Ticker: up, Index: "sp500", Direction: model.DirectionBuy, PriceAtGeneration: closeAt(up, anchor+14)}},
		Selection:   model.SelectionChief,
	})
	// A pre-field run: ships without ever recording a selection policy.
	f.addRun(t, "2025-prefield", anchor+21, "", []model.TradeIdea{
		{Rank: 1, Ticker: up, Index: "sp500", Direction: model.DirectionBuy, PriceAtGeneration: closeAt(up, anchor+21)},
	}, nil, nil)
	// A single-stock run: also never records a selection policy, but it is not
	// an "unrecorded" independent run — the field does not apply to it at all.
	f.addRun(t, "2025-single", anchor+28, "", []model.TradeIdea{
		{Rank: 1, Ticker: up, Index: "sp500", Direction: model.DirectionBuy, PriceAtGeneration: closeAt(up, anchor+28)},
	}, nil, nil)
	writeJSON(t, filepath.Join(f.runsDir, "2025-single"), "ideas.json", model.IdeasResult{
		GeneratedAt: f.dates[anchor+28] + "T22:00:00Z",
		Mode:        string(model.ModeSingle),
		Ideas:       []model.TradeIdea{{Rank: 1, Ticker: up, Index: "sp500", Direction: model.DirectionBuy, PriceAtGeneration: closeAt(up, anchor+28)}},
	})

	rep, err := ControlWithOptions(context.Background(), f.runsDir, f.prices, ControlOptions{Horizon: 10})
	if err != nil {
		t.Fatal(err)
	}
	arms := map[string]ControlArm{}
	for _, a := range rep.Arms {
		arms[a.Name] = a
	}

	sc := arms["sector-capped"]
	var scTickers []string
	for _, e := range sc.Entries {
		scTickers = append(scTickers, e.Ticker)
	}
	// ListRuns walks newest-first; "2025-merit-b" sorts after "2025-merit" and
	// so is scored first.
	if strings.Join(scTickers, ",") != loser+","+capped {
		t.Errorf("sector-capped entries = %v, want %s,%s (the no-direction row dropped)", scTickers, loser, capped)
	}

	if n := arms["shipped-merit_veto"].Record.N; n != 1 || arms["shipped-merit_veto"].Entries[0].Ticker != up {
		t.Errorf("shipped-merit_veto = %+v, want exactly the one merit_veto run's idea", arms["shipped-merit_veto"])
	}
	if n := arms["shipped-chief"].Record.N; n != 1 {
		t.Errorf("shipped-chief n=%d, want 1", n)
	}
	if n := arms["shipped-unrecorded"].Record.N; n != 1 {
		t.Errorf("shipped-unrecorded n=%d, want 1 (only the pre-field independent run, not the single-stock one)", n)
	}
	if n := arms["shipped-single"].Record.N; n != 1 {
		t.Errorf("shipped-single n=%d, want 1 (the single-stock run, kept out of shipped-unrecorded)", n)
	}
	// The pooled shipped arm still carries all four runs undivided.
	if n := arms["shipped"].Record.N; n != 4 {
		t.Errorf("pooled shipped n=%d, want 4 (all policies and modes pooled)", n)
	}

	diffs := map[string]bool{}
	for _, d := range rep.Diffs {
		diffs[d.Over+"-"+d.Under] = true
	}
	if !diffs["sector-capped-shipped-merit_veto"] {
		t.Errorf("no sector-capped − shipped-merit_veto difference in %+v", rep.Diffs)
	}
	text := rep.FormatText()
	for _, want := range []string{"sector-capped", "shipped-merit_veto", "shipped-chief", "shipped-single", "shipped-unrecorded", "Sector cap cost"} {
		if !strings.Contains(text, want) {
			t.Errorf("text report lacks %q:\n%s", want, text)
		}
	}
}
