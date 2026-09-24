package scoreboard

import (
	"sort"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// The selection shadow arms (docs/workflow/scoreboard.md, "Selection arms").
//
// Under merit_veto selection the models stopped ranking: Go ships the top of the
// shortlist by merit, and the specialists and the Chief may only veto. Two
// questions decide whether that was right, and both are answered from
// data/selection.json, which records every shortlisted name whether it shipped
// or not:
//
//	chief-shadow  the Chief's own top names by its recorded shadow_rank, at the
//	              scout's direction — the book it would have shipped. shipped −
//	              chief-shadow is what taking the ranking away from it cost or
//	              saved.
//	vetoed        every shortlisted name a specialist or the Chief vetoed, at the
//	              scout's direction. vetoed − shipped below zero is a veto that
//	              removes losers; at or above zero it is removing nothing.
//
// A veto is recorded under either policy, so the vetoed arm fills from chief
// runs too; the chief-shadow arm only exists where a shadow ranking was asked for.

// readSelection loads a run's data/selection.json, or nil when it has none —
// every run before merit_veto existed, and every thesis or single-stock run.
func readSelection(dir string) *model.SelectionRecord {
	var rec model.SelectionRecord
	if err := store.ReadDataPack(dir, "selection", &rec); err != nil || len(rec.Rows) == 0 {
		return nil
	}
	return &rec
}

// chiefShadowCalls is the Chief's shadow book: its topN highest-ranked names
// that carry a scout direction, in that direction.
func chiefShadowCalls(rec *model.SelectionRecord) []call {
	if rec == nil {
		return nil
	}
	n := rec.TopN
	if n <= 0 {
		n = controlArmSize
	}
	ranked := make([]model.SelectionRow, 0, len(rec.Rows))
	for _, r := range rec.Rows {
		if r.ChiefShadowRank > 0 && r.Direction != "" {
			ranked = append(ranked, r)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].ChiefShadowRank < ranked[j].ChiefShadowRank })
	if len(ranked) > n {
		ranked = ranked[:n]
	}
	return selectionCalls(ranked)
}

// vetoedCalls is every vetoed shortlisted name with a scout direction.
func vetoedCalls(rec *model.SelectionRecord) []call {
	if rec == nil {
		return nil
	}
	var rows []model.SelectionRow
	for _, r := range rec.Rows {
		if r.Vetoed && r.Direction != "" {
			rows = append(rows, r)
		}
	}
	return selectionCalls(rows)
}

func selectionCalls(rows []model.SelectionRow) []call {
	out := make([]call, 0, len(rows))
	for _, r := range rows {
		out = append(out, call{ticker: r.Ticker, name: r.Name, index: r.Index,
			direction: r.Direction, anchor: r.Close, setup: r.Setup, confidence: r.BaseConfidence})
	}
	return out
}
