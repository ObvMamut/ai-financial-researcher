package scoreboard

import (
	"sort"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// The selection shadow arms (docs/workflow/scoreboard.md, "Selection arms").
//
// Under merit_veto selection the models stopped ranking: Go ships the top of the
// shortlist by merit, and the specialists and the Chief may only veto. Three
// questions decide whether that was right, and all three are answered from
// data/selection.json, which records every shortlisted name whether it shipped
// or not:
//
//	chief-shadow   the Chief's own top names by its recorded shadow_rank, at the
//	               scout's direction — the book it would have shipped. shipped −
//	               chief-shadow is what taking the ranking away from it cost or
//	               saved.
//	vetoed         every shortlisted name a specialist or the Chief vetoed, at the
//	               scout's direction. vetoed − shipped below zero is a veto that
//	               removes losers; at or above zero it is removing nothing.
//	sector-capped  every shortlisted name max_per_sector dropped, at the scout's
//	               direction — the live counterpart of experiment E2, which
//	               modelled the cap as a mechanical cut over the composite
//	               ranking rather than over what the scouts actually nominated
//	               (plan §7). sector-capped − shipped-merit_veto at or above zero
//	               is a cap giving up return it did not need to.
//
// A veto is recorded under either policy, so the vetoed arm fills from chief
// runs too; the chief-shadow and sector-capped arms only exist under merit_veto
// (only merit_veto records a shadow rank or excludes on the sector cap).

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

// rowsWithDirection filters rec's rows by match, keeping only rows the scouts
// gave a direction — a neutral nomination is not a call either arm can score.
// vetoedCalls and sectorCappedCalls share this rather than each re-walking
// rec.Rows with their own copy of the same guard.
func rowsWithDirection(rec *model.SelectionRecord, match func(model.SelectionRow) bool) []model.SelectionRow {
	if rec == nil {
		return nil
	}
	var rows []model.SelectionRow
	for _, r := range rec.Rows {
		if match(r) && r.Direction != "" {
			rows = append(rows, r)
		}
	}
	return rows
}

// vetoedCalls is every vetoed shortlisted name with a scout direction.
func vetoedCalls(rec *model.SelectionRecord) []call {
	return selectionCalls(rowsWithDirection(rec, func(r model.SelectionRow) bool { return r.Vetoed }))
}

// ExcludedSectorCap mirrors internal/orchestrator/selection.go's unexported
// excludedSectorCap constant, which scoreboard cannot import directly:
// orchestrator already imports scoreboard (calibration.go, postmortem.go),
// and the reverse would cycle. Exported so orchestrator can assert the two
// stay equal instead of drifting silently — see
// TestExcludedSectorCapMatchesScoreboard in internal/orchestrator.
const ExcludedSectorCap = "sector_cap"

// sectorCappedCalls is every shortlisted name max_per_sector excluded, at the
// scout's direction — the live counterpart of experiment E2 (plan §7).
func sectorCappedCalls(rec *model.SelectionRecord) []call {
	return selectionCalls(rowsWithDirection(rec, func(r model.SelectionRow) bool { return r.Excluded == ExcludedSectorCap }))
}

func selectionCalls(rows []model.SelectionRow) []call {
	out := make([]call, 0, len(rows))
	for _, r := range rows {
		out = append(out, call{ticker: r.Ticker, name: r.Name, index: r.Index,
			direction: r.Direction, anchor: r.Close, setup: r.Setup, confidence: r.BaseConfidence})
	}
	return out
}
