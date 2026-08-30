package scoreboard

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// closedEntry is a finished trade with the domain scores that argued for it.
func closedEntry(dir string, conf int, pnl, r float64, domains map[string]int) Entry {
	o := OutcomeTarget
	if pnl < 0 {
		o = OutcomeStop
	}
	return Entry{
		Direction: dir, Confidence: conf, Outcome: o, PnLPct: pnl, RiskAdjPnL: r,
		EntryFilled: 100, Stop: 95, BarsHeld: 8, DomainScores: domains,
	}
}

func calibrationOf(n int) *Calibration {
	s := &Summary{}
	for i := 0; i < n; i++ {
		// Alternate wins and losses so the record is a coherent ~50%.
		pnl, r := 6.0, 1.2
		if i%2 == 1 {
			pnl, r = -5.0, -1.0
		}
		conf := 45
		if i%3 == 0 {
			conf = 65
		}
		s.Entries = append(s.Entries, closedEntry("BUY", conf, pnl, r, map[string]int{"quant": 5, "macro": -4}))
	}
	s.aggregate()
	return Calibrate(s)
}

func TestCalibrateSummarisesTheClosedRecord(t *testing.T) {
	c := calibrationOf(10)
	if c.NClosed != 10 {
		t.Fatalf("n_closed = %d, want 10", c.NClosed)
	}
	if c.WinRate != 0.5 {
		t.Errorf("win rate = %.2f, want 0.50", c.WinRate)
	}
	if c.AvgR != round2((5*1.2-5*1.0)/10) {
		t.Errorf("avg R = %.2f, want %.2f", c.AvgR, round2((5*1.2-5*1.0)/10))
	}
	if c.AvgBarsHeld != 8 {
		t.Errorf("avg hold = %.1f, want 8", c.AvgBarsHeld)
	}
	// quant backed every long; macro's bearish read never did.
	if got := c.Domains["quant"]; got.N != 10 {
		t.Errorf("quant = %+v, want all 10 trades", got)
	}
	if _, ok := c.Domains["macro"]; ok {
		t.Error("macro is credited with trades it argued against")
	}
	if c.ComputedAt == "" {
		t.Error("calibration is undated — a stale record read as current is worse than none")
	}
}

func TestCalibrationBlockStaysQuietOnAThinRecord(t *testing.T) {
	// Nine closed trades cannot tell a 45% domain from a 55% one, and a chief
	// told otherwise will spend adjustments on noise.
	if got := calibrationOf(9).Block(); got != "" {
		t.Errorf("block rendered from 9 closed trades:\n%s", got)
	}
	if got := (*Calibration)(nil).Block(); got != "" {
		t.Errorf("nil calibration rendered a block: %q", got)
	}

	block := calibrationOf(12).Block()
	if block == "" {
		t.Fatal("no block from 12 closed trades")
	}
	for _, want := range []string{"### Track record", "12 closed", "By stated confidence", "quant"} {
		if !strings.Contains(block, want) {
			t.Errorf("block missing %q:\n%s", want, block)
		}
	}
	// It sits in a prompt beside five full reports; it has to stay small.
	if n := strings.Count(block, "\n"); n > 15 {
		t.Errorf("block is %d lines, want at most 15:\n%s", n, block)
	}
}

func TestCalibrationRealizedEdgeNeedsRealHistory(t *testing.T) {
	// Twenty closed trades is a mood, not a measurement; the risk gate keeps
	// its prior until there are enough of them.
	if _, ok := calibrationOf(20).RealizedEdge(); ok {
		t.Error("offered a realized edge from 20 closed trades")
	}
	r, ok := calibrationOf(30).RealizedEdge()
	if !ok {
		t.Fatal("withheld the realized edge at 30 closed trades")
	}
	if want := round2((15*1.2 - 15*1.0) / 30); r != want {
		t.Errorf("realized edge = %.2f R, want %.2f", r, want)
	}
}

func TestCalibrationRoundTripsThroughDisk(t *testing.T) {
	dir := t.TempDir()
	c := calibrationOf(12)
	if err := c.Save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := LoadCalibration(dir)
	if got == nil {
		t.Fatal("loaded nothing back")
	}
	if got.NClosed != c.NClosed || got.AvgR != c.AvgR {
		t.Errorf("round trip = %+v, want n=%d avgR=%.2f", got, c.NClosed, c.AvgR)
	}
	if LoadCalibration(filepath.Join(dir, "nope")) != nil {
		t.Error("a missing calibration file is an absence, not a value")
	}
}

func TestPersonaKeyIdentifiesTheExactPromptSet(t *testing.T) {
	base := &model.RunMeta{
		PersonaSet: "agents",
		PersonaSHA: map[string]string{"scout": "aaa111", "macro": "bbb222"},
	}
	same := &model.RunMeta{
		PersonaSet: "agents",
		PersonaSHA: map[string]string{"macro": "bbb222", "scout": "aaa111"}, // map order differs
	}
	if personaKey(base) != personaKey(same) {
		t.Errorf("same personas keyed differently: %q vs %q", personaKey(base), personaKey(same))
	}
	if !strings.HasPrefix(personaKey(base), "agents@") {
		t.Errorf("key = %q, want it to name the directory it came from", personaKey(base))
	}

	// One edited persona is a different arm, even in a directory of the same
	// name. Attributing its results to the old prompts would be the whole
	// experiment gone wrong.
	edited := &model.RunMeta{
		PersonaSet: "agents",
		PersonaSHA: map[string]string{"scout": "aaa111", "macro": "ccc333"},
	}
	if personaKey(edited) == personaKey(base) {
		t.Error("an edited persona did not change the key")
	}

	// A different directory is a different arm.
	other := &model.RunMeta{
		PersonaSet: "agents.v1",
		PersonaSHA: map[string]string{"scout": "aaa111", "macro": "bbb222"},
	}
	if personaKey(other) == personaKey(base) {
		t.Error("agents.v1 and agents share a key")
	}

	// Runs that recorded nothing cannot be attributed, and must not be pooled
	// into some default arm.
	if got := personaKey(&model.RunMeta{}); got != "" {
		t.Errorf("key from empty metadata = %q, want none", got)
	}
	if got := personaKey(nil); got != "" {
		t.Errorf("key from nil metadata = %q, want none", got)
	}
}

func TestPersonaComparisonWithholdsAVerdictOnThinArms(t *testing.T) {
	// Fifteen closed ideas per arm is the floor for saying anything. Below it
	// the comparison still prints — you want to see it filling up — but it says
	// plainly that it is not a result yet.
	s := &Summary{}
	add := func(persona string, n int, wins int) {
		for i := 0; i < n; i++ {
			pnl, r := 6.0, 1.2
			if i >= wins {
				pnl, r = -5.0, -1.0
			}
			e := closedEntry("BUY", 60, pnl, r, nil)
			e.PersonaSet = persona
			s.Entries = append(s.Entries, e)
		}
	}
	add("agents@abc123", 16, 9)
	add("agents.v1@def456", 8, 2)
	s.aggregate()

	if got := s.ByPersona["agents@abc123"]; got.N != 16 || got.Wins != 9 {
		t.Fatalf("arm A = %+v, want N=16 W=9", got)
	}

	s.Replay = true
	out := s.FormatText()
	if !strings.Contains(out, "agents@abc123") || !strings.Contains(out, "agents.v1@def456") {
		t.Errorf("comparison missing an arm:\n%s", out)
	}
	if !strings.Contains(out, "too thin") {
		t.Errorf("an 8-idea arm was reported without qualification:\n%s", out)
	}
	if strings.Contains(out, "agents@abc123 · too thin") {
		t.Errorf("a 16-idea arm was called thin:\n%s", out)
	}
}
