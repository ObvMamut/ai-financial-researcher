package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
)

func meritVetoConfig(t *testing.T) Config {
	t.Helper()
	cfg := testConfig(t, model.ModeIndependent)
	cfg.Selection = model.SelectionMeritVeto
	return cfg
}

func readSelection(t *testing.T, dir string) model.SelectionRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "data", "selection.json"))
	if err != nil {
		t.Fatalf("data/selection.json: %v", err)
	}
	var rec model.SelectionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("data/selection.json: %v", err)
	}
	return rec
}

func readIdeasFile(t *testing.T, dir string) model.IdeasResult {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "ideas.json"))
	if err != nil {
		t.Fatal(err)
	}
	var res model.IdeasResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("ideas.json: %v", err)
	}
	return res
}

func rowFor(rec model.SelectionRecord, ticker string) (model.SelectionRow, bool) {
	for _, r := range rec.Rows {
		if r.Ticker == ticker {
			return r, true
		}
	}
	return model.SelectionRow{}, false
}

// assertMeritOrder checks the book is exactly the top of the eligible names by
// merit: shipped in merit order, and no eligible name the cut passed over sits
// above the last one shipped.
func assertMeritOrder(t *testing.T, rec model.SelectionRecord, ideas []model.TradeIdea) {
	t.Helper()
	if len(ideas) == 0 {
		t.Fatal("merit_veto shipped nothing")
	}
	last := 0
	for i, idea := range ideas {
		r, ok := rowFor(rec, idea.Ticker)
		if !ok {
			t.Fatalf("shipped %s has no selection row", idea.Ticker)
		}
		if !r.Selected || r.ShippedRank != i+1 || r.Excluded != "" {
			t.Errorf("row for shipped %s not marked shipped at rank %d: %+v", idea.Ticker, i+1, r)
		}
		if r.MeritRank <= last {
			t.Errorf("%s shipped at merit rank %d after merit rank %d — the book is out of merit order", idea.Ticker, r.MeritRank, last)
		}
		last = r.MeritRank
		if idea.Direction != r.Direction {
			t.Errorf("%s shipped %s against the scout's %s", idea.Ticker, idea.Direction, r.Direction)
		}
	}
	for _, r := range rec.Rows {
		if r.Excluded == excludedBelowCut && r.MeritRank < last && len(ideas) == meritVetoTopN {
			t.Errorf("%s (merit rank %d) fell below a cut at rank %d", r.Ticker, r.MeritRank, last)
		}
		if r.Excluded == excludedBelowCut && len(ideas) < meritVetoTopN {
			t.Errorf("%s left below the cut of a short book", r.Ticker)
		}
	}
}

func TestMeritVetoRunShipsTheMeritBookAfterVetoes(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "veto")
	cfg := meritVetoConfig(t)

	complete, runErr, logs := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil || complete.Ideas == nil {
		t.Fatal("no EventComplete with ideas")
	}
	dir := runDir(t, cfg.RunsDir)
	rec := readSelection(t, dir)
	if rec.Policy != model.SelectionMeritVeto || rec.Chief != "accepted" {
		t.Errorf("selection policy/chief = %q/%q, want merit_veto/accepted", rec.Policy, rec.Chief)
	}

	// The news veto is a veto; quant's out-of-enum "veto" is not.
	nv, ok := rowFor(rec, "NVDA")
	if !ok {
		t.Fatal("NVDA missing from the selection record")
	}
	if !nv.Vetoed || nv.Selected || nv.Excluded != excludedVetoed || len(nv.Vetoes) != 1 ||
		nv.Vetoes[0].Source != "news" || nv.Vetoes[0].Reason != model.VetoBinaryEventInsideWindow {
		t.Errorf("NVDA veto not recorded as news/binary_event_inside_window: %+v", nv)
	}
	if nv.Labels["news"].MoveDriver != model.MoveDriverNews {
		t.Errorf("NVDA news labels lost: %+v", nv.Labels)
	}
	if jpm, ok := rowFor(rec, "JPM"); ok && jpm.Vetoed {
		t.Errorf("an out-of-enum reason vetoed JPM: %+v", jpm.Vetoes)
	}
	if !anyLogContains(logs, `"does not like it", which is not in the closed enum`) {
		t.Error("the refused quant veto was not logged")
	}

	ideas := complete.Ideas.Ideas
	for _, idea := range ideas {
		if idea.Ticker == "NVDA" {
			t.Error("the vetoed NVDA shipped")
		}
		if idea.EntryType != model.EntryMarketOnOpen || idea.Target != 0 || idea.TimeframeDays != meritVetoHoldDays || idea.Stop <= 0 || idea.Entry <= 0 {
			t.Errorf("%s is not a market-on-open catastrophe-stop idea: %+v", idea.Ticker, idea)
		}
		if !strings.HasPrefix(idea.Why, "Fake prose for "+idea.Ticker) {
			t.Errorf("%s did not get the Chief's prose: %q", idea.Ticker, idea.Why)
		}
	}
	assertMeritOrder(t, rec, ideas)
	for _, r := range rec.Rows {
		t.Logf("merit #%d %s %s merit %+.2f excluded=%q shipped=%d shadow=%d %s",
			r.MeritRank, r.Ticker, r.Direction, r.Merit, r.Excluded, r.ShippedRank, r.ChiefShadowRank, r.RiskGate)
	}

	// The Chief's ranking is recorded, not acted on.
	onDisk := readIdeasFile(t, dir)
	if len(onDisk.ShadowRank) == 0 || len(rec.ShadowRank) == 0 || onDisk.Selection != model.SelectionMeritVeto {
		t.Errorf("shadow rank / selection not persisted: ideas=%v/%q selection=%v", onDisk.ShadowRank, onDisk.Selection, rec.ShadowRank)
	}
	if first, ok := rowFor(rec, rec.ShadowRank[0]); !ok || first.ChiefShadowRank != 1 {
		t.Errorf("chief_shadow_rank not set on the rows: %+v", first)
	}

	// No macro call: no report, no status row. The Chief got the computed regime.
	meta := readMeta(t, dir)
	if meta.Selection != model.SelectionMeritVeto {
		t.Errorf("metadata selection = %q", meta.Selection)
	}
	for _, d := range meta.Domains {
		if d.Domain == "macro" {
			t.Error("merit_veto ran the macro specialist")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "macro.md")); err == nil {
		t.Error("merit_veto wrote a macro report")
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "chief-analyst.md")), "saw-computed-regime") {
		t.Error("the Chief was not given the computed regime block")
	}
	if len(rec.Regime) == 0 {
		t.Error("the computed regime was not recorded")
	}
}

func TestMeritVetoChiefFailureShipsTheSelectionWithoutProse(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "chief-fail")
	cfg := meritVetoConfig(t)

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil || complete.Ideas == nil || len(complete.Ideas.Ideas) == 0 {
		t.Fatal("a Chief failure must not cost the selection")
	}
	for _, idea := range complete.Ideas.Ideas {
		if !strings.HasPrefix(idea.Why, "Merit-veto selection:") {
			t.Errorf("%s carries prose nobody wrote: %q", idea.Ticker, idea.Why)
		}
	}
	dir := runDir(t, cfg.RunsDir)
	if meta := readMeta(t, dir); meta.Outcome != "degraded" || meta.ChiefAccepted != "" {
		t.Errorf("outcome/accepted = %q/%q, want degraded and nothing accepted", meta.Outcome, meta.ChiefAccepted)
	}
	rec := readSelection(t, dir)
	if rec.Chief != "failed" || len(rec.ShadowRank) != 0 {
		t.Errorf("selection record chief=%q shadow=%v, want failed and no shadow", rec.Chief, rec.ShadowRank)
	}
	assertMeritOrder(t, rec, complete.Ideas.Ideas)
}

// The 2026-09-25 investigation into plan finding F4 (data/selection.json on
// the 2026-09-24T12-58-48 artifact appearing to lack `excluded`) found the
// merit_veto code path sound — runMeritVeto mutates the same rows slice that
// becomes SelectionRecord.Rows, and that artifact's own file, read back byte
// for byte, does carry excluded="sector_cap" on its sector-capped name — but
// also found no test exercised sector_cap or below_cut through the real
// pipeline (`grep excludedSectorCap *_test.go` found nothing before this
// test). This one drives all three reasons through one run and reads the
// persisted file back, the way the shortlist actually behaves: three
// Information Technology names (NVDA, MSFT and AMD, cap 2) so the third is
// sector-capped, a lowest-merit otherwise-eligible name (NKE) left below the
// cut, and a quant veto (6758.T, which only quant — always priced — can see)
// so a genuine veto is recorded too.
func TestMeritVetoRecordsSectorCapBelowCutAndVetoed(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "sector-cap")
	cfg := meritVetoConfig(t)
	// MSFT is a fourth sp500 nomination on top of the usual three; without
	// one more shortlist slot the merge's own trim, not merit_veto, would be
	// what dropped the weakest name (NKE) before selection ever saw it.
	cfg.MaxShortlist = 13

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	if complete == nil || complete.Ideas == nil {
		t.Fatal("no EventComplete with ideas")
	}
	dir := runDir(t, cfg.RunsDir)
	rec := readSelection(t, dir)

	wantShipped := map[string]bool{"XOM": true, "NVDA": true, "MSFT": true, "JPM": true, "TSLA": true}
	if len(complete.Ideas.Ideas) != len(wantShipped) {
		t.Fatalf("shipped %d ideas, want %d: %+v", len(complete.Ideas.Ideas), len(wantShipped), complete.Ideas.Ideas)
	}
	for _, idea := range complete.Ideas.Ideas {
		if !wantShipped[idea.Ticker] {
			t.Errorf("shipped %s, not one of the expected book", idea.Ticker)
		}
	}

	if amd, ok := rowFor(rec, "AMD"); !ok || amd.Excluded != excludedSectorCap || amd.Selected {
		t.Errorf("AMD (the third Information Technology name, after NVDA and MSFT) = %+v, want excluded=%q", amd, excludedSectorCap)
	}
	if nke, ok := rowFor(rec, "NKE"); !ok || nke.Excluded != excludedBelowCut || nke.Selected {
		t.Errorf("NKE (lowest merit, otherwise eligible) = %+v, want excluded=%q", nke, excludedBelowCut)
	}
	vetoed, ok := rowFor(rec, "6758.T")
	if !ok || vetoed.Excluded != excludedVetoed || vetoed.Selected || !vetoed.Vetoed ||
		len(vetoed.Vetoes) != 1 || vetoed.Vetoes[0].Source != "quant" || vetoed.Vetoes[0].Reason != model.VetoHaltedOrIlliquid {
		t.Errorf("6758.T = %+v, want excluded=%q vetoed by quant/%s", vetoed, excludedVetoed, model.VetoHaltedOrIlliquid)
	}
	assertMeritOrder(t, rec, complete.Ideas.Ideas)

	// readSelection above already went through store.Run.WriteDataPack (the
	// pipeline's own persistence call) and read data/selection.json back from
	// disk into model.SelectionRecord; confirm the raw bytes on disk carry
	// all three reasons literally, not just a struct field that survived
	// marshal/unmarshal in memory.
	raw, err := os.ReadFile(filepath.Join(dir, "data", "selection.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"excluded": "sector_cap"`, `"excluded": "below_cut"`, `"excluded": "vetoed"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("data/selection.json does not contain %q:\n%s", want, raw)
		}
	}
}

func TestAChiefVetoIsRefilledFromTheReserves(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "chief-veto")
	cfg := meritVetoConfig(t)

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil {
		t.Fatalf("unexpected EventError: %s", runErr.Message)
	}
	rec := readSelection(t, runDir(t, cfg.RunsDir))
	var vetoed *model.SelectionRow
	for i, r := range rec.Rows {
		for _, v := range r.Vetoes {
			if v.Source == "chief" {
				vetoed = &rec.Rows[i]
			}
		}
	}
	if vetoed == nil {
		t.Fatal("the Chief's veto was not recorded")
	}
	if vetoed.Selected || vetoed.Excluded != excludedVetoed {
		t.Errorf("the Chief-vetoed %s still shipped: %+v", vetoed.Ticker, vetoed)
	}
	for _, idea := range complete.Ideas.Ideas {
		if idea.Ticker == vetoed.Ticker {
			t.Errorf("%s shipped after the Chief vetoed it", idea.Ticker)
		}
	}
	assertMeritOrder(t, rec, complete.Ideas.Ideas)
}

// The chief policy is the control arm: its labels are recorded all the same, so
// the veto and label shadow arms fill up whichever policy is running.
func TestChiefSelectionStillRecordsLabels(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "veto")
	cfg := testConfig(t, model.ModeIndependent)

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr != nil || complete == nil {
		t.Fatalf("run failed: %v", runErr)
	}
	dir := runDir(t, cfg.RunsDir)
	if _, err := os.Stat(filepath.Join(dir, "macro.md")); err != nil {
		t.Error("the chief policy must still run macro")
	}
	rec := readSelection(t, dir)
	if rec.Policy != model.SelectionChief || len(rec.ShadowRank) != 0 {
		t.Errorf("policy/shadow = %q/%v", rec.Policy, rec.ShadowRank)
	}
	nv, _ := rowFor(rec, "NVDA")
	if !nv.Vetoed {
		t.Errorf("the NVDA veto label is not recorded under the chief policy: %+v", nv)
	}
	shipped := 0
	for _, r := range rec.Rows {
		if r.Selected {
			shipped++
		}
	}
	if shipped != len(complete.Ideas.Ideas) {
		t.Errorf("%d rows marked shipped, %d ideas shipped", shipped, len(complete.Ideas.Ideas))
	}
	if complete.Ideas.Selection != model.SelectionChief {
		t.Errorf("ideas selection = %q", complete.Ideas.Selection)
	}
}

// TestExcludedSectorCapMatchesScoreboard guards against the two packages'
// copies of "sector_cap" drifting apart. scoreboard.ExcludedSectorCap exists
// only because scoreboard cannot import this package's unexported
// excludedSectorCap (orchestrator already imports scoreboard, so the reverse
// would cycle); this is the other half of that mirror, run from the side that
// can see both.
func TestExcludedSectorCapMatchesScoreboard(t *testing.T) {
	if excludedSectorCap != scoreboard.ExcludedSectorCap {
		t.Errorf("excludedSectorCap = %q, scoreboard.ExcludedSectorCap = %q; the scoreboard's sector-capped arm reads a different value than this package writes",
			excludedSectorCap, scoreboard.ExcludedSectorCap)
	}
}
