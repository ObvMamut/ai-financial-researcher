package orchestrator

import (
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// With gateMetrics a 10-day 1σ is $10 on a $100 close, so the default 2σ√h
// catastrophe floor is $20 and its ceiling $40.
func mooIdea(ticker string, dir model.Direction, stop, target float64) model.TradeIdea {
	idea := gateIdeaAt(ticker, stop, target)
	idea.Direction = dir
	idea.EntryType = model.EntryMarketOnOpen
	return idea
}

func TestMarketOnOpenIdeaPassesTheGateWithoutATarget(t *testing.T) {
	v := gateVerified(t, "AAA")
	idea := mooIdea("AAA", model.DirectionBuy, 80, 0)
	for _, f := range gateIdea(&idea, v, riskDefaults(model.RiskConfig{})) {
		if !f.Observational {
			t.Errorf("target-less market-on-open idea drew a finding: %s", f.Message)
		}
	}
	// Sized off the stop distance: $500 of risk over a $20 stop.
	if idea.Shares != 25 || idea.RiskAmount != 500 {
		t.Errorf("sizing = %d shares / $%.2f at risk, want 25 / $500", idea.Shares, idea.RiskAmount)
	}
	if idea.BreakevenWinRate != 0 {
		t.Errorf("breakeven win rate %v implies a target that does not exist", idea.BreakevenWinRate)
	}
	// walkPath used to treat a zero target as already touched on the first
	// step, booking −100% on every long path.
	if idea.ExpectancyBps < -500 || idea.ExpectancyR == 0 {
		t.Errorf("expectancy diagnostic not computed sanely: %+.1f bps / %+.3fR", idea.ExpectancyBps, idea.ExpectancyR)
	}
}

// A limit idea — every ideas.json from before entry_type, and any run with
// risk.entry_type = "limit" — still needs all three levels.
func TestLimitIdeaStillNeedsATarget(t *testing.T) {
	v := gateVerified(t, "AAA")
	idea := gateIdeaAt("AAA", 85, 0)
	if fs := gateIdea(&idea, v, riskDefaults(model.RiskConfig{})); !hasHard(fs, "AAA", "every idea needs all three") {
		t.Errorf("a limit idea without a target passed: %v", findingsFor(fs, "AAA"))
	}
}

func TestMarketOnOpenIgnoresRewardRiskAndTargetBands(t *testing.T) {
	v := gateVerified(t, "AAA")
	cfg := riskDefaults(model.RiskConfig{})
	for _, target := range []float64{101, 200} { // reward:risk 0.05, and a 10σ target
		idea := mooIdea("AAA", model.DirectionBuy, 80, target)
		for _, f := range gateIdea(&idea, v, cfg) {
			if !f.Observational {
				t.Errorf("target %.0f: an informational target drew a finding: %s", target, f.Message)
			}
		}
	}
}

func TestMarketOnOpenStopCeiling(t *testing.T) {
	v := gateVerified(t, "AAA")
	idea := mooIdea("AAA", model.DirectionBuy, 55, 0) // 4.5σ
	if fs := gateIdea(&idea, v, riskDefaults(model.RiskConfig{})); !hasHard(fs, "AAA", "beyond 4.0σ") {
		t.Errorf("a 4.5σ stop passed: %v", findingsFor(fs, "AAA"))
	}
	idea = mooIdea("AAA", model.DirectionBuy, 95, 0) // 0.5σ, never re-based
	if fs := gateIdea(&idea, v, riskDefaults(model.RiskConfig{})); !hasHard(fs, "AAA", "catastrophe-stop floor") {
		t.Errorf("a stop inside the floor that reached the gate passed: %v", findingsFor(fs, "AAA"))
	}
}

// The simulated-expectancy floor judges a stop/target geometry. A losing
// assumed edge must still refuse a limit idea and must not refuse a
// market-on-open one, whose only barrier is a floor the backtest chose.
func TestExpectancyFloorDoesNotRejectMarketOnOpen(t *testing.T) {
	v := gateVerified(t, "AAA")
	cfg := riskDefaults(model.RiskConfig{EdgeSigmaDaily: -0.1, Explicit: map[string]bool{"edge_sigma_daily": true}})
	moo := mooIdea("AAA", model.DirectionBuy, 80, 0)
	for _, f := range gateIdea(&moo, v, cfg) {
		if strings.Contains(f.Message, "expectancy") {
			t.Errorf("market-on-open refused on expectancy: %s", f.Message)
		}
	}
	if moo.ExpectancyR >= 0 {
		t.Errorf("a losing edge should still show in the diagnostic: %+.3fR", moo.ExpectancyR)
	}
	limit := gateIdeaAt("AAA", 85, 120)
	if fs := gateIdea(&limit, v, cfg); !hasHard(fs, "AAA", "simulated expectancy") {
		t.Errorf("a limit idea at a losing edge passed: %v", findingsFor(fs, "AAA"))
	}
}

func TestEntryPolicyRebasesAndFloorsTheStop(t *testing.T) {
	v := gateVerified(t, "AAA")
	cases := []struct {
		name      string
		dir       model.Direction
		entry     float64
		stop      float64
		wantStop  float64
		wantMsgs  int
		wantWiden bool
	}{
		{"long inside the floor", model.DirectionBuy, 100, 95, 80, 1, true},
		{"short inside the floor", model.DirectionSell, 100, 105, 120, 1, true},
		{"long stop on the wrong side", model.DirectionBuy, 100, 110, 80, 1, true},
		{"long with no stop", model.DirectionBuy, 100, 0, 80, 1, true},
		{"wider stop kept", model.DirectionBuy, 100, 70, 70, 0, false},
		{"entry re-based to the close", model.DirectionBuy, 101.5, 70, 70, 1, false},
	}
	for _, c := range cases {
		idea := model.TradeIdea{Ticker: "AAA", Direction: c.dir, Entry: c.entry, Stop: c.stop, TimeframeDays: 10}
		msgs := applyEntryPolicy(&idea, v, model.RiskConfig{})
		if idea.EntryType != model.EntryMarketOnOpen {
			t.Errorf("%s: entry_type = %q", c.name, idea.EntryType)
		}
		if idea.Entry != 100 {
			t.Errorf("%s: entry = %.2f, want the verified close 100", c.name, idea.Entry)
		}
		if idea.Stop != c.wantStop {
			t.Errorf("%s: stop = %.2f, want %.2f", c.name, idea.Stop, c.wantStop)
		}
		if len(msgs) != c.wantMsgs {
			t.Errorf("%s: messages = %q, want %d", c.name, msgs, c.wantMsgs)
		}
		if widened := len(msgs) > 0 && strings.Contains(strings.Join(msgs, " "), "widened"); widened != c.wantWiden {
			t.Errorf("%s: widened = %v, want %v (%q)", c.name, widened, c.wantWiden, msgs)
		}
	}

	// The floor scales with the holding period: 2σ√15 on this name is $24.49.
	idea := model.TradeIdea{Ticker: "AAA", Direction: model.DirectionBuy, Entry: 100, Stop: 90, TimeframeDays: 15}
	applyEntryPolicy(&idea, v, model.RiskConfig{})
	if idea.Stop != 75.5 {
		t.Errorf("15-day floor stop = %.2f, want 75.50", idea.Stop)
	}

	// risk.entry_type = "limit" keeps the old behaviour untouched.
	limit := model.TradeIdea{Ticker: "AAA", Direction: model.DirectionBuy, Entry: 98, Stop: 90, Target: 115, TimeframeDays: 10}
	if msgs := applyEntryPolicy(&limit, v, model.RiskConfig{EntryType: model.EntryLimit}); len(msgs) != 0 || limit.Entry != 98 || limit.Stop != 90 || limit.EntryType != model.EntryLimit {
		t.Errorf("limit policy changed the idea: %+v %q", limit, msgs)
	}
}

func TestPlanReviewTargetTestOnlyBindsAStatedTarget(t *testing.T) {
	moo := model.TradeIdea{EntryType: model.EntryMarketOnOpen}
	withTarget := model.TradeIdea{EntryType: model.EntryMarketOnOpen, Target: 110}
	limit := model.TradeIdea{Target: 110}
	cases := []struct {
		idea       model.TradeIdea
		assessment string
		want       bool
	}{
		{moo, "", true},
		{moo, "unresolved", true},
		{moo, "disputed", false},
		{withTarget, "", false},
		{withTarget, "supported", true},
		{limit, "", false},
		{limit, "supported", true},
	}
	for _, c := range cases {
		if got := planTargetSupported(c.idea, model.ThesisChallenge{TargetAssessment: c.assessment}); got != c.want {
			t.Errorf("%+v with %q = %v, want %v", c.idea, c.assessment, got, c.want)
		}
	}
}

// Through validateIdeas, as the legacy pipeline calls it: a market-on-open idea
// with no target is not an incomplete one, and a wrong-side informational
// target is removed without spending the corrective re-prompt on it.
func TestValidateIdeasAcceptsTargetlessMarketOnOpen(t *testing.T) {
	v := gateVerified(t, "AAA", "BBB")
	cfg := Config{Mode: model.ModeIndependent, ChiefAdjustBand: 10}
	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		{Ticker: "AAA", Direction: model.DirectionBuy, Entry: 100, Stop: 90, TimeframeDays: 10},
		{Ticker: "BBB", Direction: model.DirectionBuy, Entry: 100, Stop: 90, Target: 95, TimeframeDays: 10},
	}}
	ws := validateIdeas(res, cfg, v)
	for _, w := range ws {
		if strings.Contains(w.Message, "level ordering") || strings.Contains(w.Message, "limit-entry band") {
			t.Errorf("%s: %s", w.Ticker, w.Message)
		}
	}
	for _, idea := range res.Ideas {
		if idea.EntryType != model.EntryMarketOnOpen || idea.Stop != 80 || idea.Target != 0 || idea.RiskReward != 0 {
			t.Errorf("%s not normalised: %+v", idea.Ticker, idea)
		}
	}
}
