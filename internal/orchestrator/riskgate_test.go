package orchestrator

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
)

// gateMetrics is a name whose 10-day 1σ move is exactly $10 on a $100 stock:
// σ_daily 3.1623% × √10 × 100 = 10.00. Every level in these tests is stated in
// dollars so the σ arithmetic is legible.
func gateMetrics(sym string) quant.Metrics {
	m := quant.Metrics{
		Symbol: sym, AsOf: "2026-08-28", LastClose: 100,
		SigmaDaily: 0.1 / math.Sqrt(10), AvgDollarVol20: 500e6,
		Benchmark: "SPY", Beta: 1.0,
	}
	// A US listing, so the same figure in USD — but stated the way the pipeline
	// states it, through ApplyFX, rather than by leaving the currency blank.
	m.ApplyFX("USD", 1)
	return m
}

func gateVerified(t *testing.T, syms ...string) verified {
	t.Helper()
	qp := quant.NewPack()
	for _, s := range syms {
		qp.ByTicker[s] = gateMetrics(s)
	}
	return verified{Universe: testUniverse(t), Quant: qp}
}

// idea builds a 10-day BUY at 100 with the given stop and target.
func gateIdeaAt(ticker string, stop, target float64) model.TradeIdea {
	return model.TradeIdea{
		Ticker: ticker, Direction: model.DirectionBuy, Confidence: 70,
		Entry: 100, Stop: stop, Target: target, TimeframeDays: 10,
		PositionNote: "full size", Why: "momentum",
	}
}

func findingsFor(fs []riskFinding, ticker string) []string {
	var out []string
	for _, f := range fs {
		if f.Ticker == ticker {
			out = append(out, f.Message)
		}
	}
	return out
}

func hasHard(fs []riskFinding, ticker, substr string) bool {
	for _, f := range fs {
		if f.Hard && f.Ticker == ticker && strings.Contains(f.Message, substr) {
			return true
		}
	}
	return false
}

// hasFinding is hasHard without the severity, for checks that must be reported
// but must not cost the idea its place.
func hasFinding(fs []riskFinding, ticker, substr string) bool {
	for _, f := range fs {
		if f.Ticker == ticker && strings.Contains(f.Message, substr) {
			return true
		}
	}
	return false
}

func hasBookFinding(fs []riskFinding, substr string) bool {
	for _, f := range fs {
		if f.Ticker == "" && strings.Contains(f.Message, substr) {
			return true
		}
	}
	return false
}

func TestRiskGateEnforcesTheStopBand(t *testing.T) {
	// "A sound stop is usually 1–2 × σ√h" was a preference in a persona, and the
	// 2026-08 runs came back clustered at the cheap edge of it. 1σ over 10 days
	// is $10 here, so a $9 stop is 0.90σ and a $25 stop is 2.50σ.
	cases := []struct {
		name       string
		stop       float64
		target     float64
		wantSubstr string
	}{
		{"inside the band", 90, 120, ""},
		{"tighter than 1σ", 91, 120, "inside the 1.0σ floor"},
		{"wider than 2σ", 75, 190, "beyond the 2.0σ ceiling"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", tc.stop, tc.target)}}
			fs := applyRiskGate(res, gateVerified(t, "AAA"), model.RiskConfig{})
			got := hasHard(fs, "AAA", "the stop is")
			if tc.wantSubstr == "" && got {
				t.Errorf("a stop inside the band should pass: %v", findingsFor(fs, "AAA"))
			}
			if tc.wantSubstr != "" && !hasHard(fs, "AAA", tc.wantSubstr) {
				t.Errorf("want a hard finding containing %q, got %v", tc.wantSubstr, findingsFor(fs, "AAA"))
			}
		})
	}
}

func TestRiskGateEnforcesTheRewardRiskFloor(t *testing.T) {
	// The observed gaming band was 1.52–1.61 against a stated "≥ 1.5 preferred".
	// The floor is 1.8 and it is not a preference.
	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		gateIdeaAt("AAA", 90, 116), // RR 1.6
		gateIdeaAt("BBB", 90, 119), // RR 1.9
	}}
	fs := applyRiskGate(res, gateVerified(t, "AAA", "BBB"), model.RiskConfig{})
	if !hasHard(fs, "AAA", "reward:risk is 1.60") {
		t.Errorf("RR 1.6 must fail the 1.8 floor: %v", findingsFor(fs, "AAA"))
	}
	if hasHard(fs, "BBB", "reward:risk") {
		t.Errorf("RR 1.9 clears the floor: %v", findingsFor(fs, "BBB"))
	}
}

func TestRiskGateRejectsAnUnreachableTarget(t *testing.T) {
	// A stop at 1.5σ and a target at 4σ satisfies every ratio rule and is still
	// a 40% move in a fortnight.
	res := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 85, 140)}}
	fs := applyRiskGate(res, gateVerified(t, "AAA"), model.RiskConfig{})
	if !hasHard(fs, "AAA", "beyond the 3.5σ ceiling") {
		t.Errorf("a 4σ target must be rejected: %v", findingsFor(fs, "AAA"))
	}
}

func TestRiskGateRejectsALosingGeometry(t *testing.T) {
	// The expectancy check answers what the σ bands cannot: how often the near
	// barrier is touched before the far one, and what a gap through the stop
	// costs when it happens.
	//
	// A 0.5σ stop with a 1σ target satisfies reward:risk 2.0 and still loses
	// money — noise reaches the stop first, and often past it.
	bad := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 95, 110)}}
	fs := applyRiskGate(bad, gateVerified(t, "AAA"), model.RiskConfig{})
	if !hasHard(fs, "AAA", "expectancy") {
		t.Errorf("want an expectancy rejection, got %v", findingsFor(fs, "AAA"))
	}
	if got := bad.Ideas[0].ExpectancyBps; got >= 0 {
		t.Errorf("expectancy = %+.0f bps, want negative", got)
	}

	// A 1σ stop against a 2σ target clears it.
	good := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("BBB", 90, 120)}}
	fs = applyRiskGate(good, gateVerified(t, "BBB"), model.RiskConfig{})
	if hasHard(fs, "BBB", "expectancy") {
		t.Errorf("a sound geometry should survive: %v", findingsFor(fs, "BBB"))
	}
	// Breakeven win rate is risk / (risk + reward) = 10 / 30, rounded like every
	// neighbouring field in ideas.json rather than shipped as 0.3333333333333333.
	if got := good.Ideas[0].BreakevenWinRate; got != 0.3333 {
		t.Errorf("breakeven win rate = %v, want 0.3333", got)
	}
}

// The gate rejected only a negative expectancy, so it asked whether a geometry
// was provably suicidal rather than whether it was worth doing.
func TestRiskGateEnforcesAnExpectancyFloor(t *testing.T) {
	res := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 85, 130)}}
	cfg := model.RiskConfig{MinExpectancyR: 2.0}
	fs := applyRiskGate(res, gateVerified(t, "AAA"), cfg)

	if !hasHard(fs, "AAA", "under the 2.000R floor") {
		t.Errorf("a positive expectancy below the floor was accepted: %+.3fR, findings %v",
			res.Ideas[0].ExpectancyR, findingsFor(fs, "AAA"))
	}
	if got := res.Ideas[0].ExpectancyR; got <= 0 {
		t.Fatalf("fixture expectancy = %+.3fR; this test needs a *positive* one under the floor", got)
	}
	// The wording has to distinguish the two: "loses money" is false about a
	// geometry that earns something, it is merely not earning enough.
	if !hasHard(fs, "AAA", "indistinguishable from zero") {
		t.Errorf("a positive-but-thin expectancy was reported as a loss: %v", findingsFor(fs, "AAA"))
	}
	// The re-prompt has to name a lever that works. Telling a model to raise
	// reward:risk lowers this number, because a tighter stop is touched more
	// often — that was the advice the gate gave for three ideas it dropped.
	if !hasHard(fs, "AAA", "The lever is the holding period") {
		t.Errorf("the finding does not name a lever that moves it: %v", findingsFor(fs, "AAA"))
	}

	// The same geometry clears the default floor it actually beats.
	clear := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 85, 130)}}
	if fs := applyRiskGate(clear, gateVerified(t, "AAA"), model.RiskConfig{}); hasHard(fs, "AAA", "expectancy") {
		t.Errorf("a sound geometry was refused by the default floor: %v", findingsFor(fs, "AAA"))
	}
}

// The floor this replaced was denominated in basis points of entry, which is
// proportional to the stop distance — so it graded volatility, not
// construction. On 2026-09-01 five ideas with near-identical normalised
// geometry were split by sigma_daily alone and the three calmest were dropped.
func TestExpectancyFloorDoesNotGradeVolatility(t *testing.T) {
	cfg := riskDefaults(model.RiskConfig{})
	// Same geometry in sigma units, two names four times apart in volatility.
	// stop 1.35 sigma over 15 days, target 2.57 sigma, R:R 1.90.
	const days = 15
	mk := func(sigma float64) (*model.TradeIdea, quant.Metrics) {
		unit := sigma * math.Sqrt(days) * 100
		return &model.TradeIdea{
			Ticker: "AAA", Direction: model.DirectionBuy,
			Entry: 100, Stop: 100 - 1.35*unit, Target: 100 + 2.57*unit, TimeframeDays: days,
		}, quant.Metrics{
			Symbol: "AAA", LastClose: 100, SigmaDaily: sigma,
			AvgDollarVol20: 5e8, AvgDollarVol20USD: 5e8, Currency: "USD", FXToUSD: 1,
		}
	}
	calmIdea, calmM := mk(0.0109) // BBVA.MC on that run: dropped at −3.1 bps
	loudIdea, loudM := mk(0.0249) // STLAM.MI: shipped at +28.7 bps

	calmBps, calmR := simulateExpectancy(calmIdea, calmM, cfg, effectiveCostBps(cfg, calmM), days)
	loudBps, loudR := simulateExpectancy(loudIdea, loudM, cfg, effectiveCostBps(cfg, loudM), days)

	if !(loudBps > 3*calmBps) {
		t.Fatalf("fixture does not reproduce the bias: %.1f vs %.1f bps", loudBps, calmBps)
	}
	// In R the same construction scores the same, within simulation noise.
	if spread := math.Abs(loudR - calmR); spread > 0.05 {
		t.Errorf("identical geometry scored %.3fR and %.3fR — the floor still grades volatility", calmR, loudR)
	}
	if calmR < cfg.MinExpectancyR {
		t.Errorf("the calm name is still refused: %+.3fR against a %.3fR floor", calmR, cfg.MinExpectancyR)
	}
}

// A liquid name does not pay a $20M-a-day name's costs, and because expectancy
// is a net-of-cost number the flat assumption taxed the largest names hardest
// relative to their smaller stop distances.
func TestCostScalesWithLiquidity(t *testing.T) {
	cfg := riskDefaults(model.RiskConfig{})
	thin := effectiveCostBps(cfg, quant.Metrics{AvgDollarVol20USD: 2.5e7})
	mid := effectiveCostBps(cfg, quant.Metrics{AvgDollarVol20USD: 3e8})
	mega := effectiveCostBps(cfg, quant.Metrics{AvgDollarVol20USD: 5e9})
	if !(thin > mid && mid > mega) {
		t.Errorf("cost does not fall with liquidity: %.1f / %.1f / %.1f bps", thin, mid, mega)
	}
	if thin != cfg.CostBps {
		t.Errorf("a name at the liquidity floor should pay the configured cost: %.1f, want %.1f", thin, cfg.CostBps)
	}
	// An unknown ADV is assumed to be the expensive kind, not the cheap one.
	if unknown := effectiveCostBps(cfg, quant.Metrics{}); unknown != cfg.CostBps {
		t.Errorf("an unknown ADV paid %.1f bps, want the full %.1f", unknown, cfg.CostBps)
	}
}

func TestRiskGateSimulationPaysItselfNothing(t *testing.T) {
	// Two ways this simulation used to flatter every trade it scored. Stepping
	// with exp(μ + σz) grows the expected *price* at μ + σ²/2, which is free
	// return in proportion to volatility — exactly backwards. And booking a
	// breached stop at the stop level assumes a perfect fill: a daily step from
	// 92 to 85 through a stop at 90 loses 15%, not 10%. Together they were worth
	// about +40 bps to every idea, which is more than the whole edge prior.
	//
	// With no assumed edge and no costs, a trade must be worth about nothing.
	res := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 90, 134)}}
	applyRiskGate(res, gateVerified(t, "AAA"), model.RiskConfig{EdgeSigmaDaily: 0.0001, CostBps: 0.01})
	if got := res.Ideas[0].ExpectancyBps; math.Abs(got) > 15 {
		t.Errorf("expectancy at a zero edge and zero cost = %+.0f bps, want near zero — "+
			"volatility alone must not pay the trade", got)
	}
}

func TestRiskGateExpectancyIsDeterministic(t *testing.T) {
	// A risk check that answers differently on a re-run is not a check.
	first := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 88, 124)}}
	second := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 88, 124)}}
	applyRiskGate(first, gateVerified(t, "AAA"), model.RiskConfig{})
	applyRiskGate(second, gateVerified(t, "AAA"), model.RiskConfig{})
	if first.Ideas[0].ExpectancyBps != second.Ideas[0].ExpectancyBps {
		t.Errorf("expectancy differs between identical runs: %v vs %v",
			first.Ideas[0].ExpectancyBps, second.Ideas[0].ExpectancyBps)
	}
}

func TestRiskGateRejectsIlliquidNames(t *testing.T) {
	v := gateVerified(t, "AAA")
	m := v.Quant.ByTicker["AAA"]
	m.AvgDollarVol20 = 5e6
	m.ApplyFX("USD", 1)
	v.Quant.ByTicker["AAA"] = m

	res := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 88, 124)}}
	fs := applyRiskGate(res, v, model.RiskConfig{})
	if !hasHard(fs, "AAA", "liquidity floor") {
		t.Errorf("a $5M-a-day name cannot be sized: %v", findingsFor(fs, "AAA"))
	}
}

func TestRiskGateComputesPositionSize(t *testing.T) {
	// "Half size" is not a position. On $100k equity at 0.5% risk, $500 is at
	// risk; a $12 stop distance buys 41 shares.
	res := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 88, 124)}}
	applyRiskGate(res, gateVerified(t, "AAA"), model.RiskConfig{})
	idea := res.Ideas[0]
	if idea.Shares != 41 {
		t.Errorf("shares = %d, want floor(500/12) = 41", idea.Shares)
	}
	if idea.Notional != 4100 {
		t.Errorf("notional = %.2f, want 4100", idea.Notional)
	}
	if idea.RiskAmount != 492 {
		t.Errorf("risk amount = %.2f, want 41 × 12 = 492", idea.RiskAmount)
	}
}

// The 2026-09-01 run shipped its first and third ideas — Tokyo Electron and
// SoftBank — with no share count, no notional and no risk amount, and said
// nothing about it: sizeIdea divided a USD budget by a JPY stop distance,
// floored to zero, and returned. All three fields are `omitempty`, so the ideas
// looked complete.
func TestRiskGateSizesInTheCurrencyTheLevelsAreQuotedIn(t *testing.T) {
	// Tokyo Electron as it actually shipped: ¥54,800 entry, ¥46,800 stop.
	// At ¥150/$ the $500 risk budget is ¥75,000, which buys 9 shares of the
	// ¥8,000 stop distance. The 25% cap allows ¥3.75M / ¥54,800 = 68, so the
	// budget binds.
	const yenPerUSD = 1.0 / 150
	m := quant.Metrics{Symbol: "8035.T", LastClose: 54980, SigmaDaily: 0.0368,
		AvgDollarVol20: 164.9e9}
	m.ApplyFX("JPY", yenPerUSD)

	v := verified{Universe: testUniverse(t), Quant: &quant.Pack{
		ByTicker: map[string]quant.Metrics{"8035.T": m}}}
	res := &model.IdeasResult{Ideas: []model.TradeIdea{{
		Ticker: "8035.T", Direction: model.DirectionBuy, Confidence: 40,
		Entry: 54800, Stop: 46800, Target: 70800, TimeframeDays: 10,
	}}}
	fs := applyRiskGate(res, v, model.RiskConfig{})

	idea := res.Ideas[0]
	if idea.Shares != 9 {
		t.Errorf("shares = %d, want floor(75000/8000) = 9", idea.Shares)
	}
	if idea.Currency != "JPY" {
		t.Errorf("currency = %q, want JPY — the levels are yen", idea.Currency)
	}
	// Exposure is reported in USD so a four-currency book adds up: 9 × ¥54,800.
	if want := 9 * 54800.0 * yenPerUSD; math.Abs(idea.Notional-want) > 0.01 {
		t.Errorf("notional = %.2f, want $%.2f", idea.Notional, want)
	}
	if want := 9 * 8000.0 * yenPerUSD; math.Abs(idea.RiskAmount-want) > 0.01 {
		t.Errorf("risk amount = %.2f, want $%.2f", idea.RiskAmount, want)
	}
	// ¥164.9bn a day is ~$1.1bn: comfortably over the floor once converted, and
	// the raw figure must not be what clears it.
	if hasHard(fs, "8035.T", "liquidity floor") {
		t.Errorf("a ¥164.9bn-a-day name is not illiquid: %v", findingsFor(fs, "8035.T"))
	}
}

// The converse: a Tokyo name turning over ¥20M a day is about $135k and
// untradeable, but cleared a $20M floor by a factor of 150 while the comparison
// was made in mixed units.
func TestRiskGateAppliesTheLiquidityFloorInDollars(t *testing.T) {
	m := quant.Metrics{Symbol: "9999.T", LastClose: 1000, SigmaDaily: 0.02,
		AvgDollarVol20: 20e6} // ¥20M
	m.ApplyFX("JPY", 1.0/150)

	v := verified{Universe: testUniverse(t), Quant: &quant.Pack{
		ByTicker: map[string]quant.Metrics{"9999.T": m}}}
	res := &model.IdeasResult{Ideas: []model.TradeIdea{{
		Ticker: "9999.T", Direction: model.DirectionBuy, Confidence: 40,
		Entry: 1000, Stop: 940, Target: 1130, TimeframeDays: 10,
	}}}
	if fs := applyRiskGate(res, v, model.RiskConfig{}); !hasHard(fs, "9999.T", "liquidity floor") {
		t.Errorf("¥20M/day is $133k, far under the $20M floor: %v", findingsFor(fs, "9999.T"))
	}

	// And a name whose currency will not convert is not silently assumed to be
	// in dollars — it is reported as unverifiable.
	m.ApplyFX("JPY", 0)
	v.Quant.ByTicker["9999.T"] = m
	res = &model.IdeasResult{Ideas: []model.TradeIdea{{
		Ticker: "9999.T", Direction: model.DirectionBuy, Confidence: 40,
		Entry: 1000, Stop: 940, Target: 1130, TimeframeDays: 10,
	}}}
	if fs := applyRiskGate(res, v, model.RiskConfig{}); !hasHard(fs, "9999.T", "no JPY/USD rate") {
		t.Errorf("an unconvertible name must not be compared against a dollar floor: %v",
			findingsFor(fs, "9999.T"))
	}
}

// Sizing that cannot buy one whole share used to be a bare `return`.
func TestRiskGateSaysSoWhenItCannotSizeAnIdea(t *testing.T) {
	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		// A $600 stop distance against a $500 per-trade budget.
		{Ticker: "AAA", Direction: model.DirectionBuy, Entry: 5000, Stop: 4400,
			Target: 6200, TimeframeDays: 10},
	}}
	m := gateMetrics("AAA")
	m.LastClose = 5000
	v := verified{Universe: testUniverse(t), Quant: &quant.Pack{
		ByTicker: map[string]quant.Metrics{"AAA": m}}}

	fs := applyRiskGate(res, v, model.RiskConfig{})
	if res.Ideas[0].Shares != 0 {
		t.Fatalf("shares = %d, want 0 — the budget cannot buy one", res.Ideas[0].Shares)
	}
	if !hasFinding(fs, "AAA", "cannot be sized at one whole share") {
		t.Errorf("an unsizeable idea must say so, got %v", findingsFor(fs, "AAA"))
	}
	// Soft, not hard: it is a budget fact, not a bad trade.
	if hasHard(fs, "AAA", "cannot be sized at one whole share") {
		t.Errorf("an unsizeable idea should not be dropped outright")
	}
}

func TestRiskGateCapsOneIdeaAtAQuarterOfTheBook(t *testing.T) {
	// A very tight stop would otherwise size to many times the account.
	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		{Ticker: "AAA", Direction: model.DirectionBuy, Entry: 100, Stop: 99.9, Target: 101, TimeframeDays: 10},
	}}
	applyRiskGate(res, gateVerified(t, "AAA"), model.RiskConfig{})
	if got := res.Ideas[0].Notional; got > 25000.01 {
		t.Errorf("notional %.2f exceeds a quarter of $100k equity", got)
	}
}

func TestRiskGateFlagsAnUnacknowledgedEarningsDate(t *testing.T) {
	now := time.Now().UTC()
	inside := now.AddDate(0, 0, 7)
	outside := now.AddDate(0, 0, 60)

	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		gateIdeaAt("AAA", 88, 124),
		gateIdeaAt("BBB", 88, 124),
		gateIdeaAt("CCC", 88, 124),
	}}
	res.Ideas[1].PositionNote = "half size into earnings on " + inside.Format("Jan 2")
	v := gateVerified(t, "AAA", "BBB", "CCC")
	v.Events = map[string]time.Time{"AAA": inside, "BBB": inside, "CCC": outside}

	fs := applyRiskGate(res, v, model.RiskConfig{})
	if len(findingsFor(fs, "AAA")) == 0 || !strings.Contains(findingsFor(fs, "AAA")[0], "falls inside the 10-day window") {
		t.Errorf("holding through earnings unacknowledged must be flagged: %v", findingsFor(fs, "AAA"))
	}
	if res.Ideas[0].Confidence != 60 {
		t.Errorf("confidence = %d, want 70 − 10 for the unhedged event", res.Ideas[0].Confidence)
	}
	if res.Ideas[1].Confidence != 70 {
		t.Errorf("an acknowledged event is not a penalty, got %d", res.Ideas[1].Confidence)
	}
	if res.Ideas[2].Confidence != 70 {
		t.Errorf("an event 60 days out is not inside a 10-day window, got %d", res.Ideas[2].Confidence)
	}
}

func TestRiskGateFlagsADateNoFactSupports(t *testing.T) {
	res := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 88, 124), gateIdeaAt("BBB", 88, 124)}}
	res.Ideas[0].Why = "guidance raised at the 2026-09-15 investor day"
	res.Ideas[1].Why = "results confirmed on 2026-08-14"

	v := gateVerified(t, "AAA", "BBB")
	v.Dates = map[string]bool{"2026-08-14": true, "2026-08-28": true}

	fs := applyRiskGate(res, v, model.RiskConfig{})
	if len(findingsFor(fs, "AAA")) == 0 || !strings.Contains(findingsFor(fs, "AAA")[0], "2026-09-15") {
		t.Errorf("an unsupported date must be named: %v", findingsFor(fs, "AAA"))
	}
	if res.Ideas[0].Confidence != 60 {
		t.Errorf("confidence = %d, want 70 − 10 for the invented date", res.Ideas[0].Confidence)
	}
	if res.Ideas[1].Confidence != 70 {
		t.Errorf("a date the run actually collected is not a finding, got %d", res.Ideas[1].Confidence)
	}
	// With nothing verified to check against, silence beats a blanket accusation.
	clean := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 88, 124)}}
	clean.Ideas[0].Why = "guidance raised at the 2026-09-15 investor day"
	applyRiskGate(clean, gateVerified(t, "AAA"), model.RiskConfig{})
	if clean.Ideas[0].Confidence != 70 {
		t.Errorf("no verified date set means no accusation, got %d", clean.Ideas[0].Confidence)
	}
}

// TestVerifiedDatesIncludeTheScheduledEventItself is the 2026-09-01 regression.
//
// An earnings fact's AsOf is when the calendar was fetched; the date it names is
// in its value and in the pack's EventDates map. Collecting only AsOf meant the
// Chief citing a real, verified earnings date was accused of inventing it —
// which cost the idea 10 points, spent the run's one corrective re-prompt, and
// ended with three true dates deleted from the shipped output.
func TestVerifiedDatesIncludeTheScheduledEventItself(t *testing.T) {
	fetched := time.Date(2026, 9, 1, 8, 51, 51, 0, time.UTC)
	earnings := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)

	pack := marketdata.NewDataPack("news")
	pack.ByTicker["MRK"] = marketdata.TickerData{Ticker: "MRK", Facts: []marketdata.Fact{
		{Label: marketdata.EarningsFactLabel, Value: "2026-10-29", AsOf: fetched, Source: "AlphaVantage earnings calendar"},
	}}
	pack.EventDates["MRK"] = earnings

	dates := map[string]bool{}
	collectVerifiedDates(dates, pack)

	if !dates["2026-10-29"] {
		t.Error("the earnings date itself is not in the verified set — citing it will be called a fabrication")
	}
	if !dates["2026-09-01"] {
		t.Error("the fetch date went missing")
	}

	// End to end through the check that consumes it.
	res := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("MRK", 88, 124)}}
	res.Ideas[0].PositionNote = "Next earnings 2026-10-29 is outside the window."
	v := gateVerified(t, "MRK")
	v.Dates = dates

	fs := applyRiskGate(res, v, model.RiskConfig{})
	if got := findingsFor(fs, "MRK"); len(got) > 0 {
		t.Errorf("a verified earnings date was reported as unsupported: %v", got)
	}
	if res.Ideas[0].Confidence != 70 {
		t.Errorf("confidence = %d, want 70 — no penalty for citing a date the run collected", res.Ideas[0].Confidence)
	}
}

// Only the pack-level as_of — the newest bar across the whole shortlist — was
// registered, while CompactLine shows every ticker its *own* as_of, RegimeBlock
// shows every benchmark's, and the staleness flag names the session a name is
// trailing. The Chief reads all of them and is told to reason about them, so
// quoting one truthfully was scored as an invention. The 2026-09-01 run held
// both 2026-08-31 and 2026-09-01 and escaped only because an AlphaVantage
// headline happened to carry the older date.
func TestVerifiedDatesCoverEveryQuantDateTheChiefIsShown(t *testing.T) {
	qp := quant.NewPack()
	qp.AsOf = "2026-09-01"
	qp.ByTicker["MU"] = quant.Metrics{Symbol: "MU", AsOf: "2026-09-01"}
	qp.ByTicker["8035.T"] = quant.Metrics{Symbol: "8035.T", AsOf: "2026-08-31", Flags: []string{
		"stale: last bar 2026-08-31, behind 8035.T's last completed session 2026-08-28 — re-price before acting",
	}}
	qp.Benchmarks["^N225"] = quant.Metrics{Symbol: "^N225", AsOf: "2026-08-31"}

	dates := map[string]bool{}
	collectQuantDates(dates, qp)
	for _, want := range []string{"2026-09-01", "2026-08-31", "2026-08-28"} {
		if !dates[want] {
			t.Errorf("%s is shown to the Chief but is not verified", want)
		}
	}

	// End to end: an idea citing a per-ticker as_of that is not the pack maximum
	// must not be flagged.
	res := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 88, 124)}}
	res.Ideas[0].Why = "priced off the 2026-08-31 close"
	v := gateVerified(t, "AAA")
	v.Dates = dates
	if fs := applyRiskGate(res, v, model.RiskConfig{}); len(findingsFor(fs, "AAA")) > 0 {
		t.Errorf("a date the app itself put in the prompt was called a fabrication: %v", findingsFor(fs, "AAA"))
	}
	if res.Ideas[0].Confidence != 70 {
		t.Errorf("confidence = %d, want 70", res.Ideas[0].Confidence)
	}
}

// The prompt rendered a fact's as_of machine-local while the gate registered it
// in UTC. On this machine (+02:00) any fact collected between 22:00 and 24:00
// UTC was shown as one date and verified as the day before, so a truthful
// citation cost 10 points and the run's one corrective re-prompt.
func TestFactDatesAreShownAndVerifiedInTheSameZone(t *testing.T) {
	// 01:30 local on the 1st at +02:00 is 23:30 UTC on 2026-08-31 — the two
	// renderings disagree by a day, which is the whole bug. time.Format uses the
	// value's own location, so an explicit zone models the machine faithfully.
	collected := time.Date(2026, 9, 1, 1, 30, 0, 0, time.FixedZone("CEST", 2*60*60))
	if collected.Format("2006-01-02") == collected.UTC().Format("2006-01-02") {
		t.Fatal("fixture does not straddle midnight UTC, so it cannot see the bug")
	}
	pack := marketdata.NewDataPack("news")
	pack.ByTicker["AAA"] = marketdata.TickerData{Ticker: "AAA", Facts: []marketdata.Fact{
		{Label: "Headline", Value: "something happened", AsOf: collected, Source: "AlphaVantage"},
	}}
	pack.Coverage["AAA"] = true

	dates := map[string]bool{}
	collectVerifiedDates(dates, pack)

	shown := isoDate.FindAllString(pack.Markdown(), -1)
	if len(shown) == 0 {
		t.Fatal("the pack rendered no date at all")
	}
	for _, d := range shown {
		if !dates[d] {
			t.Errorf("the prompt shows %s but the gate verifies %v — a truthful citation scores as an invention", d, dates)
		}
	}
}

// checkFabricatedDates only ever read `why` and `position_note`. `notes` is the
// one place the model writes freely and the part a reader trusts most: the
// 2026-09-01 run shipped 1,400 characters of dated factual narrative there,
// entirely unchecked.
func TestRiskGateChecksTheDatesInTheBookNotes(t *testing.T) {
	res := &model.IdeasResult{
		Ideas: []model.TradeIdea{gateIdeaAt("AAA", 88, 124)},
		Notes: "Coverage was the binding constraint. Prices are as of 2026-08-31; " +
			"the Fed meets 2026-09-08 and 2026-11-04.",
	}
	v := gateVerified(t, "AAA")
	v.Dates = map[string]bool{"2026-08-31": true}

	fs := applyRiskGate(res, v, model.RiskConfig{})
	if !hasBookFinding(fs, "2026-09-08") || !hasBookFinding(fs, "2026-11-04") {
		t.Errorf("unverified dates in `notes` went unreported: %v", fs)
	}
	if hasBookFinding(fs, "2026-08-31") {
		t.Errorf("a verified date in `notes` was reported: %v", fs)
	}
	// It is book-level: there is no single idea to dock, so nothing loses
	// confidence and nothing is dropped for it.
	if res.Ideas[0].Confidence != 70 {
		t.Errorf("confidence = %d, want 70 — a note is not one idea's fault", res.Ideas[0].Confidence)
	}
	if dropped := dropViolating(res, fs); len(dropped) > 0 {
		t.Errorf("a book-level note finding dropped an idea: %v", dropped)
	}
	// But it is actionable: the Chief can rewrite its own notes.
	for _, f := range fs {
		if f.Ticker == "" && strings.Contains(f.Message, "2026-09-08") && !f.actionable() {
			t.Error("the notes finding cannot reach the corrective re-prompt")
		}
	}

	// Clean notes produce nothing.
	quiet := &model.IdeasResult{
		Ideas: []model.TradeIdea{gateIdeaAt("AAA", 88, 124)},
		Notes: "Coverage was the binding constraint; prices are as of 2026-08-31.",
	}
	if fs := applyRiskGate(quiet, v, model.RiskConfig{}); hasBookFinding(fs, "appear") {
		t.Errorf("clean notes produced a finding: %v", fs)
	}
}

// TestRiskGateSeparatesFindingsTheChiefCanActuponFromThoseItCannot pins which
// findings are allowed to spend the single corrective re-prompt.
func TestRiskGateSeparatesFindingsTheChiefCanActUponFromThoseItCannot(t *testing.T) {
	// A book of five longs against a benchmark, which trips noteDirectionBalance.
	qp := quant.NewPack()
	for _, s := range []string{"AAA", "BBB"} {
		qp.ByTicker[s] = gateMetrics(s)
	}
	qp.Benchmarks["SPY"] = quant.Metrics{Symbol: "SPY", Regime: "trending", Ret63d: 0.08}
	v := verified{Universe: testUniverse(t), Quant: qp}

	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		gateIdeaAt("AAA", 88, 124),
		gateIdeaAt("BBB", 88, 124),
	}}
	fs := applyRiskGate(res, v, model.RiskConfig{})

	var actionable, observational int
	for _, f := range fs {
		if f.actionable() {
			actionable++
			continue
		}
		observational++
		if !strings.HasPrefix(f.Message, "note: all ") {
			t.Errorf("unexpected observational finding: %q", f.Message)
		}
	}
	if observational != 1 {
		t.Errorf("the all-one-way note must be observational, got %d observational of %d findings", observational, len(fs))
	}
	if actionable != 0 {
		t.Errorf("two sound ideas produced %d actionable findings, so a re-prompt would fire on nothing fixable", actionable)
	}

	// A name with no price data: a fact about the run's fetch, not about the
	// output, so it must not be re-prompted either.
	bare := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("ZZZ", 88, 124)}}
	for _, f := range applyRiskGate(bare, gateVerified(t), model.RiskConfig{}) {
		if f.actionable() {
			t.Errorf("no-price-data findings must not spend the corrective call: %q", f.Message)
		}
	}
}

func TestRiskGateSpotsOneBetInTwoTickets(t *testing.T) {
	rets := make([]float64, 200)
	for i := range rets {
		rets[i] = 0.01 * math.Sin(float64(i))
	}
	v := gateVerified(t, "AAA", "BBB", "CCC")
	v.Series = map[string]*quant.Series{
		"AAA": seriesOf("AAA", 100, rets),
		"BBB": seriesOf("BBB", 50, rets), // identical path: ρ = 1
		"CCC": seriesOf("CCC", 20, shiftedReturns(rets)),
	}
	// A 1.5σ stop against a 3σ target, which all three clear on expectancy: the
	// point here is the correlation finding, and a geometry sitting near the
	// expectancy floor would drop an idea for an unrelated reason.
	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		gateIdeaAt("AAA", 85, 130), gateIdeaAt("BBB", 85, 130), gateIdeaAt("CCC", 85, 130),
	}}
	fs := applyRiskGate(res, v, model.RiskConfig{})
	if !hasBookFinding(fs, "one bet in two tickets") {
		t.Errorf("two perfectly correlated longs must be flagged: %v", fs)
	}
	for _, f := range fs {
		if f.Hard {
			t.Fatalf("fixture has a per-idea violation, which this test is not about: %s", f.Message)
		}
	}
	// A book-level finding never costs an idea its place: dropping a sound idea
	// because of its neighbour is not a risk control.
	if dropped := dropViolating(res, fs); len(dropped) != 0 {
		t.Errorf("correlation should not drop ideas, dropped %v", dropped)
	}
	if len(res.Ideas) != 3 {
		t.Errorf("ideas dropped by a book-level finding: %d left", len(res.Ideas))
	}
}

func TestRiskGateDoesNotFlagANegativelyCorrelatedPair(t *testing.T) {
	rets := make([]float64, 200)
	for i := range rets {
		rets[i] = 0.01 * math.Sin(float64(i))
	}
	inverse := make([]float64, len(rets))
	for i, r := range rets {
		inverse[i] = -r
	}
	v := gateVerified(t, "AAA", "BBB")
	v.Series = map[string]*quant.Series{
		"AAA": seriesOf("AAA", 100, rets),
		"BBB": seriesOf("BBB", 50, inverse), // exact mirror: ρ = -1
	}
	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		gateIdeaAt("AAA", 88, 124), gateIdeaAt("BBB", 88, 124),
	}}
	fs := applyRiskGate(res, v, model.RiskConfig{})
	// Two same-direction ideas whose returns move opposite to each other
	// diversify the book; they are not "one bet in two tickets".
	if hasBookFinding(fs, "one bet in two tickets") {
		t.Errorf("negatively correlated pair should not be flagged as concentrated: %v", fs)
	}
}

func TestRiskGateNotesAOneSidedBook(t *testing.T) {
	v := gateVerified(t, "AAA", "BBB")
	v.Quant.Benchmarks = map[string]quant.Metrics{
		"SPY": {Symbol: "SPY", Regime: "mean-reverting", Ret63d: -0.08},
	}
	res := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 88, 124), gateIdeaAt("BBB", 88, 124)}}
	fs := applyRiskGate(res, v, model.RiskConfig{})
	if !hasBookFinding(fs, "all 2 ideas are long") {
		t.Errorf("a one-sided book against the market read should be noted: %v", fs)
	}
	// Deliberately not a forced short: the note is an observation, not a quota.
	for _, f := range fs {
		if f.Hard && f.Ticker == "" {
			t.Errorf("direction balance must never be a hard finding: %+v", f)
		}
	}
}

func TestDropViolatingKeepsWhatSurvivesAndRenumbers(t *testing.T) {
	// Shipping fewer ideas is a success. The count was a target the system met
	// by constructing trades to fill it.
	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		{Rank: 1, Ticker: "AAA"}, {Rank: 2, Ticker: "BBB"}, {Rank: 3, Ticker: "CCC"},
	}}
	dropped := dropViolating(res, []riskFinding{
		{Ticker: "BBB", Hard: true, Message: "BBB: reward:risk is 1.20"},
		{Ticker: "CCC", Message: "CCC: soft note"},
	})
	if len(dropped) != 1 || !strings.Contains(dropped[0], "BBB") {
		t.Fatalf("dropped = %v, want the hard violation explained", dropped)
	}
	if len(res.Ideas) != 2 || res.Ideas[0].Ticker != "AAA" || res.Ideas[1].Ticker != "CCC" {
		t.Fatalf("kept = %+v", res.Ideas)
	}
	for i, idea := range res.Ideas {
		if idea.Rank != i+1 {
			t.Errorf("ranks not renumbered: %+v", res.Ideas)
		}
	}
}

// seriesOf builds a price series from log returns, for the correlation checks.
func seriesOf(sym string, start float64, rets []float64) *quant.Series {
	s := &quant.Series{Symbol: sym}
	price := start
	day := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, r := range rets {
		price *= math.Exp(r)
		s.Bars = append(s.Bars, quant.Bar{
			Date: day.Format("2006-01-02"), Open: price, High: price, Low: price, Close: price, Volume: 1e6,
		})
		day = day.AddDate(0, 0, 1)
	}
	return s
}

// shiftedReturns is a path uncorrelated with a sine of the same period.
func shiftedReturns(rets []float64) []float64 {
	out := make([]float64, len(rets))
	for i := range rets {
		out[i] = 0.01 * math.Cos(float64(i))
	}
	return out
}

func TestRealizedRecordMovesExpectancyTowardWhatWasMeasured(t *testing.T) {
	// The 0.02σ prior is an assumption. Once the pipeline has enough closed
	// trades the expectancy it reports should move toward what those trades
	// actually returned, weighted by how many of them there are.
	sim := 0.10

	few := blendRealized(sim, 0.50, scoreboard.MinClosedForEdge)
	if want := 0.30; math.Abs(few-want) > 1e-9 {
		t.Errorf("at n = the prior weight the record and the simulation should count equally: got %.4f, want %.4f", few, want)
	}
	many := blendRealized(sim, 0.50, 270)
	if !(many > few) {
		t.Errorf("more closed trades did not give the record more weight: %.4f vs %.4f", many, few)
	}
	if none := blendRealized(sim, 0.50, 0); none != sim {
		t.Errorf("with no record the simulation should stand alone: got %.4f, want %.4f", none, sim)
	}
}

func TestRealizedRecordCannotSwitchTheCheckOffOrOn(t *testing.T) {
	// The record used to arrive as a drift — avgR·riskFrac/(σ·days), clamped at
	// 0.05 — and for any realistic avgR that expression lands on the clamp. The
	// 30th closed trade would have flipped the gate from rejecting most of a
	// book to never firing, with no regime in between. A blend has no cliff: an
	// extraordinary record still leaves the simulation a share of the answer.
	sim := 0.10
	for _, n := range []int{scoreboard.MinClosedForEdge, 60, 120} {
		got := blendRealized(sim, 5.0, n)
		ceiling := 5.0
		if got >= ceiling {
			t.Errorf("n=%d: a +5R record produced %.3fR, at or above the record itself", n, got)
		}
		if got <= sim {
			t.Errorf("n=%d: a +5R record did not raise expectancy above the simulation's %.3fR", n, sim)
		}
	}
}

func TestRealizedNegativeRecordMakesTheGateStopShipping(t *testing.T) {
	// If the measured record is a losing one, the honest response is to reject
	// geometries that only worked under an assumed edge — not to keep assuming.
	idea := &model.TradeIdea{
		Ticker: "AAA", Direction: model.DirectionBuy,
		Entry: 100, Stop: 96, Target: 110, TimeframeDays: 10,
	}
	m := quant.Metrics{Symbol: "AAA", LastClose: 100, SigmaDaily: 0.02}
	cfg := riskDefaults(model.RiskConfig{})

	_, optimistic := simulateExpectancy(idea, m, cfg, cfg.CostBps, 10)
	pessimistic := blendRealized(optimistic, -0.4, 60)

	if pessimistic >= optimistic {
		t.Errorf("a losing record did not lower expectancy: %.3fR vs %.3fR", pessimistic, optimistic)
	}
	if pessimistic >= 0 {
		t.Errorf("expectancy is %+.3fR under a −0.4R record — the gate would keep shipping", pessimistic)
	}
}

func TestGateIdeaUsesTheRealizedEdgeWhenOneIsAvailable(t *testing.T) {
	// The measured edge has to reach the expectancy check, not just exist.
	mk := func() *model.TradeIdea {
		return &model.TradeIdea{
			Ticker: "AAA", Direction: model.DirectionBuy,
			Entry: 100, Stop: 96, Target: 110, TimeframeDays: 10,
		}
	}
	v := verified{Quant: &quant.Pack{ByTicker: map[string]quant.Metrics{
		"AAA": {Symbol: "AAA", LastClose: 100, SigmaDaily: 0.02,
			AvgDollarVol20: 5e8, AvgDollarVol20USD: 5e8, Currency: "USD", FXToUSD: 1},
	}}}
	cfg := riskDefaults(model.RiskConfig{})

	prior := mk()
	gateIdea(prior, v, cfg)

	losing := -0.4
	v.RealizedR = &losing
	v.RealizedN = 60
	measured := mk()
	findings := gateIdea(measured, v, cfg)

	if measured.ExpectancyR >= prior.ExpectancyR {
		t.Fatalf("realized record did not reach the expectancy check: %.3f vs %.3fR",
			measured.ExpectancyR, prior.ExpectancyR)
	}
	if !hasHard(findings, "AAA", "expectancy") {
		t.Errorf("a negative-expectancy idea was not flagged: %+v", findings)
	}
}

// TestGateStillChecksDatesWhenVolatilityIsUnavailable closes a hole where an
// idea nothing could be verified about shipped looking exactly like one that
// passed every check.
//
// The σ-band block began `unit := σ·√h·close; if unit <= 0 { return out }`, and
// the earnings-window and fabricated-date checks sat *after* it. Neither has
// anything to do with volatility, so a name whose σ could not be computed lost
// all four checks and produced no finding at all.
func TestGateStillChecksDatesWhenVolatilityIsUnavailable(t *testing.T) {
	// A name with a price but no usable volatility — what Compute produces when
	// the OHLC window is degenerate.
	qp := quant.NewPack()
	m := gateMetrics("AAA")
	m.SigmaDaily = 0
	qp.ByTicker["AAA"] = m
	v := verified{Universe: testUniverse(t), Quant: qp}
	v.Dates = map[string]bool{"2026-08-14": true}
	v.Events = map[string]time.Time{"AAA": time.Now().UTC().AddDate(0, 0, 3)}

	res := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 88, 124)}}
	res.Ideas[0].Why = "guidance raised at the 2026-09-15 investor day"
	res.Ideas[0].PositionNote = "full size"

	fs := applyRiskGate(res, v, model.RiskConfig{})
	joined := strings.Join(findingsFor(fs, "AAA"), " | ")

	if !strings.Contains(joined, "2026-09-15") {
		t.Errorf("the invented date went unchecked when σ was unavailable: %s", joined)
	}
	if !strings.Contains(joined, "falls inside the 10-day window") {
		t.Errorf("the earnings event went unchecked when σ was unavailable: %s", joined)
	}
	// Both penalties still land: 70 − 10 − 10.
	if res.Ideas[0].Confidence != 50 {
		t.Errorf("confidence = %d, want 50 (both penalties applied)", res.Ideas[0].Confidence)
	}
	// And the gap itself is recorded rather than silent — but as something the
	// Chief cannot fix, so it does not spend the corrective call.
	var sawNote bool
	for _, f := range fs {
		if strings.Contains(f.Message, "realized volatility is not computable") {
			sawNote = true
			if f.actionable() {
				t.Error("an unavailable volatility is not something a re-emitted JSON block can fix")
			}
		}
	}
	if !sawNote {
		t.Error("the unchecked geometry was not recorded at all")
	}
}

// The book the gate refused. On 2026-09-01 the Chief returned these five ideas
// and the expectancy check dropped three of them, leaving a run of two. Their
// normalised geometry is near-identical — stop ~1.3σ, target ~2.6σ, R:R ~1.9,
// breakeven ~34.5% — so whatever separated them was not construction.
//
// Kept as a regression fixture with the run's own verified σ and ADV: if a
// future change to the expectancy check refuses any of these again, it should
// have to say so here first.
func TestTheBookTheExpectancyGateRefused(t *testing.T) {
	cases := []struct {
		ticker    string
		dir       model.Direction
		entry     float64
		stop      float64
		target    float64
		days      int
		sigma     float64
		advUSD    float64
		lastClose float64
	}{
		{"BBVA.MC", model.DirectionBuy, 24.7, 23.25, 27.45, 15, 0.01091567071394929, 164076757.53954, 24.860000610351562},
		{"STLAM.MI", model.DirectionSell, 4.7, 5.29, 3.59, 12, 0.02486345779433801, 218571024.20592287, 4.641499996185303},
		{"2330.TW", model.DirectionBuy, 2400.0, 2225.0, 2735.0, 15, 0.01380239988283415, 1447837949.3950777, 2405},
		{"AMGN", model.DirectionBuy, 434.0, 397.0, 505.0, 15, 0.0177746176729581, 1017881450.1277466, 436.25},
		{"O39.SI", model.DirectionBuy, 31.4, 29.4, 35.2, 15, 0.012287565120319272, 164401640.84516317, 31.520000457763672},
	}
	cfg := riskDefaults(model.RiskConfig{})
	var scores []float64
	for _, c := range cases {
		idea := &model.TradeIdea{
			Ticker: c.ticker, Direction: c.dir,
			Entry: c.entry, Stop: c.stop, Target: c.target, TimeframeDays: c.days,
		}
		m := quant.Metrics{
			Symbol: c.ticker, LastClose: c.lastClose, SigmaDaily: c.sigma,
			AvgDollarVol20: c.advUSD, AvgDollarVol20USD: c.advUSD, Currency: "USD", FXToUSD: 1,
		}
		evBps, evR := simulateExpectancy(idea, m, cfg, effectiveCostBps(cfg, m), c.days)
		t.Logf("%-9s %+.4fR (%+.1f bps, cost %.1f bps)", c.ticker, evR, evBps, effectiveCostBps(cfg, m))
		if evR < cfg.MinExpectancyR {
			t.Errorf("%s: %+.3fR is under the %.3fR floor — the gate would drop it again",
				c.ticker, evR, cfg.MinExpectancyR)
		}
		scores = append(scores, evR)
	}
	// The residual spread is real rather than an artefact: a fixed round-trip
	// cost is a larger share of a 5.9% stop than of a 12.6% one, so the calm
	// names genuinely keep less of their edge. What is gone is the sign change —
	// in bps these ran +28.7 down to −3.1, a range wider than the floor itself
	// and ordered exactly by σ_daily.
	lo, hi := scores[0], scores[0]
	for _, s := range scores {
		lo, hi = math.Min(lo, s), math.Max(hi, s)
	}
	if hi-lo > 0.05 {
		t.Errorf("five near-identical geometries still spread %.3fR (%.3f…%.3f)", hi-lo, lo, hi)
	}
	if lo <= 0 {
		t.Errorf("the weakest of the five is %+.3fR — still not paying for its own costs", lo)
	}
}

// --- Phase 1.5: riskDefaults is the second zero-sentinel layer ---
//
// Fixing only the loader would have accomplished nothing: riskDefaults replaces
// anything <= 0 with its default, so an explicit `cost_bps = 0` that survived
// parsing was overwritten one call later. Presence has to travel the whole way.

func TestRiskDefaultsKeepsAnExplicitlyZeroedFloor(t *testing.T) {
	cfg := riskDefaults(model.RiskConfig{
		Explicit: map[string]bool{"cost_bps": true, "rr_min": true, "adv_min_usd": true, "stop_sigma_min": true},
	})
	if cfg.CostBps != 0 {
		t.Errorf("CostBps = %v, want 0 — the operator asked for a frictionless book", cfg.CostBps)
	}
	if cfg.RRMin != 0 {
		t.Errorf("RRMin = %v, want 0 — the reward:risk floor was explicitly disabled", cfg.RRMin)
	}
	if cfg.ADVMinUSD != 0 {
		t.Errorf("ADVMinUSD = %v, want 0 — the liquidity floor was explicitly disabled", cfg.ADVMinUSD)
	}
	if cfg.StopSigmaMin != 0 {
		t.Errorf("StopSigmaMin = %v, want 0", cfg.StopSigmaMin)
	}
}

func TestRiskDefaultsStillFillsAnAbsentPolicy(t *testing.T) {
	// The whole point of riskDefaults: a zero-valued struct — a test, or a run
	// with no [risk] block — must never leave the gate silently disabled.
	cfg := riskDefaults(model.RiskConfig{})
	for _, c := range []struct {
		name string
		got  float64
		want float64
	}{
		{"AccountEquity", cfg.AccountEquity, 100000},
		{"RiskPerTradePct", cfg.RiskPerTradePct, 0.5},
		{"CostBps", cfg.CostBps, 30},
		{"RRMin", cfg.RRMin, 1.8},
		{"StopSigmaMin", cfg.StopSigmaMin, 1.0},
		{"StopSigmaMax", cfg.StopSigmaMax, 2.0},
		{"TargetSigmaMax", cfg.TargetSigmaMax, 3.5},
		{"ADVMinUSD", cfg.ADVMinUSD, defaultADVMinUSD},
		{"MaxPairCorr", cfg.MaxPairCorr, 0.75},
		{"MaxPortfolioBeta", cfg.MaxPortfolioBeta, 1.5},
		{"EdgeSigmaDaily", cfg.EdgeSigmaDaily, defaultEdgeSigmaDaily},
		{"MinExpectancyR", cfg.MinExpectancyR, defaultMinExpectancyR},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want default %v", c.name, c.got, c.want)
		}
	}
}

func TestRiskDefaultsKeepsAMeasuredNegativeEdge(t *testing.T) {
	// A losing system has a negative edge. The loader already accepted one;
	// riskDefaults then replaced it with the optimistic default, so the one
	// regime the prior exists to describe was unreachable.
	cfg := riskDefaults(model.RiskConfig{
		EdgeSigmaDaily: -0.02,
		Explicit:       map[string]bool{"edge_sigma_daily": true},
	})
	if cfg.EdgeSigmaDaily != -0.02 {
		t.Errorf("EdgeSigmaDaily = %v, want -0.02 kept", cfg.EdgeSigmaDaily)
	}
}

func TestAnExplicitlyZeroedCostReachesTheExpectancySimulation(t *testing.T) {
	// The end-to-end point of the presence rule: cost_bps = 0 has to arrive at
	// the number it is charged against, not just survive parsing.
	cfg := riskDefaults(model.RiskConfig{Explicit: map[string]bool{"cost_bps": true}})
	m := quant.Metrics{AvgDollarVol20USD: 5e9}
	if got := effectiveCostBps(cfg, m); got != 0 {
		t.Errorf("effectiveCostBps = %v, want 0 for an explicitly frictionless policy", got)
	}
}

// An inverted σ band is the same footgun class as the zero ceiling validateRisk
// already refuses: it loads clean and then rejects every idea the Chief writes,
// reported as an ordinary run of risk-gate drops. The config loader cannot catch
// it, because it runs before riskDefaults fills the side the operator left
// unset — so the check lives where both effective values exist.
func TestInvertedSigmaBandsAreRefusedBeforeTheRunStarts(t *testing.T) {
	explicit := func(keys ...string) map[string]bool {
		m := map[string]bool{}
		for _, k := range keys {
			m[k] = true
		}
		return m
	}

	cases := []struct {
		name string
		risk model.RiskConfig
		want string // substring the message must carry; "" = must be accepted
	}{
		{
			name: "both sides written and inverted",
			risk: model.RiskConfig{
				StopSigmaMin: 2.5, StopSigmaMax: 2.0, TargetSigmaMax: 3.5,
				Explicit: explicit("stop_sigma_min", "stop_sigma_max", "target_sigma_max"),
			},
			want: "stop_sigma_min",
		},
		{
			// The realistic one: only the floor is written, and it crosses a
			// default ceiling the operator never saw.
			name: "one side written, crossing the default",
			risk: model.RiskConfig{
				StopSigmaMin: 2.5,
				Explicit:     explicit("stop_sigma_min"),
			},
			want: "default",
		},
		{
			name: "target ceiling inside the stop floor",
			risk: model.RiskConfig{
				StopSigmaMin: 2.0, StopSigmaMax: 3.0, TargetSigmaMax: 1.5,
				Explicit: explicit("stop_sigma_min", "stop_sigma_max", "target_sigma_max"),
			},
			want: "target_sigma_max",
		},
		{
			name: "an ordinary policy is accepted",
			risk: model.RiskConfig{
				StopSigmaMin: 1.0, StopSigmaMax: 2.0, TargetSigmaMax: 3.5,
				Explicit: explicit("stop_sigma_min", "stop_sigma_max", "target_sigma_max"),
			},
		},
		{
			name: "a config with no [risk] block at all is accepted",
			risk: model.RiskConfig{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRiskPolicy(riskDefaults(tc.risk))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validateRiskPolicy rejected a workable policy: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("an unsatisfiable σ band loaded clean; every idea would have been dropped as an ordinary risk-gate finding")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message does not name %q, so the operator cannot tell which key to fix: %v", tc.want, err)
			}
		})
	}
}

// And it has to actually fail the run, before Stage 0.5 spends anything.
func TestInvertedSigmaBandFailsTheRunNotTheBook(t *testing.T) {
	t.Setenv("CFR_FAKE_MODE", "ok")
	cfg := testConfig(t, model.ModeSingle)
	cfg.Ticker = "AAPL"
	cfg.Risk = model.RiskConfig{
		StopSigmaMin: 2.5,
		StopSigmaMax: 2.0,
		Explicit:     map[string]bool{"stop_sigma_min": true, "stop_sigma_max": true},
	}

	complete, runErr, _ := drain(t, Run(context.Background(), cfg))
	if runErr == nil {
		t.Fatalf("the run completed with an unsatisfiable stop band; ideas: %+v", complete)
	}
	if !strings.Contains(runErr.Message, "stop_sigma") {
		t.Errorf("the run failed without naming the inverted keys: %s", runErr.Message)
	}
}

func TestBookBetaCeilingCannotBeDilutedByPadding(t *testing.T) {
	// 2026-09-03: the first pass failed the beta check at 1.62 and the Chief's
	// corrective was to drop ORCL (base 38, five domains) and add 035720.KS
	// (base 27, quant only), because "the slot had to be filled by something
	// low-beta rather than left empty." Under a per-idea average that worked.
	// It must not: a risk limit an extra position can satisfy is not a limit.
	qp := quant.NewPack()
	qp.ByTicker["MU"] = quant.Metrics{Symbol: "MU", Benchmark: "^GSPC", Beta: 2.87}
	qp.ByTicker["INTC"] = quant.Metrics{Symbol: "INTC", Benchmark: "^GSPC", Beta: 2.41}
	qp.ByTicker["035720.KS"] = quant.Metrics{Symbol: "035720.KS", Benchmark: "^KS11", Beta: 0.48}
	v := verified{Universe: testUniverse(t), Quant: qp}
	cfg := model.RiskConfig{AccountEquity: 100_000, MaxPortfolioBeta: 1.5}

	betaFindings := func(ideas ...model.TradeIdea) []string {
		var out []string
		for _, f := range gateBook(&model.IdeasResult{Ideas: ideas}, v, cfg) {
			if strings.Contains(f.Message, "beta-adjusted") {
				out = append(out, f.Message)
			}
		}
		return out
	}

	// Σ|beta × notional| = (2.87 + 2.41) × 25,000 = 132,000 → 1.32× equity on
	// the gross leg, under the ceiling; both are BUYs, so the net leg is the
	// same 1.32 and also passes.
	hot := []model.TradeIdea{
		{Ticker: "MU", Direction: model.DirectionBuy, Notional: 25_000},
		{Ticker: "INTC", Direction: model.DirectionBuy, Notional: 25_000},
	}
	if got := betaFindings(hot...); len(got) != 0 {
		t.Fatalf("1.32× the account is inside the 1.5 ceiling: %v", got)
	}

	// Add a third high-beta name and it breaches at 2.04×.
	over := append(append([]model.TradeIdea{}, hot...),
		model.TradeIdea{Ticker: "MU", Direction: model.DirectionBuy, Notional: 25_000})
	breach := betaFindings(over...)
	if len(breach) == 0 {
		t.Fatal("2.04× the account did not breach the 1.5 ceiling")
	}
	if !strings.Contains(breach[0], "drop the highest-beta idea") {
		t.Errorf("the finding names no remedy: %s", breach[0])
	}

	// The padding move: adding a low-beta name must not clear it. Under the old
	// average it took the mean from 2.72 to 2.16 and would have kept going.
	padded := append(append([]model.TradeIdea{}, over...),
		model.TradeIdea{Ticker: "035720.KS", Direction: model.DirectionBuy, Notional: 25_000})
	if got := betaFindings(padded...); len(got) < len(breach) {
		t.Errorf("adding a 0.48-beta idea removed a finding — the ceiling is still dilutable: %v", got)
	}

	// Dropping exposure is what works.
	if got := betaFindings(hot[1]); len(got) != 0 {
		t.Errorf("one 2.41-beta position at 25%% of the account breached: %v", got)
	}
}

func TestBookBetaCountsIdeasItCouldNotSize(t *testing.T) {
	// An idea with no notional has unknown exposure, not zero. Counting it as
	// zero would let an unsized high-beta name sit in the book invisibly.
	qp := quant.NewPack()
	qp.ByTicker["MU"] = quant.Metrics{Symbol: "MU", Benchmark: "^GSPC", Beta: 2.87}
	qp.ByTicker["INTC"] = quant.Metrics{Symbol: "INTC", Benchmark: "^GSPC", Beta: 2.41}
	v := verified{Universe: testUniverse(t), Quant: qp}
	cfg := model.RiskConfig{AccountEquity: 100_000, MaxPortfolioBeta: 1.5}

	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		{Ticker: "MU", Direction: model.DirectionBuy, Notional: 60_000},
		{Ticker: "INTC", Direction: model.DirectionBuy}, // sizing produced no whole share
	}}
	var found string
	for _, f := range gateBook(res, v, cfg) {
		if strings.Contains(f.Message, "beta-adjusted") {
			found = f.Message
		}
	}
	if found == "" {
		t.Fatal("1.72× the account did not breach")
	}
	if !strings.Contains(found, "could not be sized") {
		t.Errorf("the finding hides that an idea was left out of the sum: %s", found)
	}
}

// The evidence floor. On 2026-09-04 BAYN.DE and DSFIR.AS shipped at ranks 4 and
// 5 scored by quant and macro alone — the composite that selected them, and a
// regime read that had agreed with it on all twelve names — and both reported
// 100% agreement to the Chief. `max_thinly_covered` caps such names on the
// shortlist and the base score prices them low, but neither reaches the output.
func TestRiskGateRefusesAnIdeaWhoseOnlyEvidenceIsItsOwnPriceHistory(t *testing.T) {
	book := func(scores ...map[string]int) *model.IdeasResult {
		res := &model.IdeasResult{Mode: string(model.ModeIndependent)}
		for i, sc := range scores {
			res.Ideas = append(res.Ideas, model.TradeIdea{
				Ticker: fmt.Sprintf("T%d", i), Direction: model.DirectionBuy,
				Entry: 100, Stop: 95, Target: 110, TimeframeDays: 15, DomainScores: sc,
			})
		}
		return res
	}
	v := verified{Universe: testUniverse(t)}
	find := func(res *model.IdeasResult, ticker string) *riskFinding {
		for _, f := range applyRiskGate(res, v, model.RiskConfig{}) {
			if f.Ticker == ticker && strings.Contains(f.Message, "scored by") {
				g := f
				return &g
			}
		}
		return nil
	}

	res := book(
		map[string]int{"quant": 7, "news": 4}, // T0: real evidence
		map[string]int{"quant": 7},            // T1: the composite restated
		nil,                                   // T2: nothing scored it at all
	)
	if f := find(res, "T0"); f != nil {
		t.Errorf("an idea with news behind it was refused: %s", f.Message)
	}
	f1 := find(res, "T1")
	if f1 == nil {
		t.Fatal("a quant-only idea was allowed to ship")
	}
	if !f1.Hard {
		t.Error("the finding is not hard, so the idea would survive the corrective re-prompt")
	}
	if f2 := find(res, "T2"); f2 == nil {
		t.Error("an idea no domain scored was allowed to ship — it is the same failure one step further along")
	}
}

func TestRiskGateKeepsTheBookWhenNothingWasScored(t *testing.T) {
	// If no idea in the book has any domain score, the scoring stage did not run
	// — the degraded path, or a synthesis with no base scores to anchor to. That
	// is a fault in the run, not in each idea, and dropping the whole book on it
	// replaces a warning with an empty result.
	res := &model.IdeasResult{Mode: string(model.ModeIndependent), Ideas: []model.TradeIdea{
		{Ticker: "AAA", Direction: model.DirectionBuy, Entry: 100, Stop: 95, Target: 110, TimeframeDays: 15},
		{Ticker: "BBB", Direction: model.DirectionBuy, Entry: 50, Stop: 47, Target: 56, TimeframeDays: 15},
	}}
	for _, f := range applyRiskGate(res, verified{Universe: testUniverse(t)}, model.RiskConfig{}) {
		if strings.Contains(f.Message, "scored by no domain") {
			t.Errorf("a book with no scoring stage was dropped idea by idea: %s", f.Message)
		}
	}
}

func TestRiskGateAnswersASingleStockEvenOnThinEvidence(t *testing.T) {
	// The user named this ticker. Refusing to answer because the news feed had
	// nothing is a non-answer, not a risk control — the pipeline has no other
	// name to offer here, which is the whole difference from independent mode.
	res := &model.IdeasResult{Mode: string(model.ModeSingle), Ideas: []model.TradeIdea{
		{Ticker: "AAA", Direction: model.DirectionBuy, Entry: 100, Stop: 95, Target: 110,
			TimeframeDays: 15, DomainScores: map[string]int{"quant": 6}},
	}}
	for _, f := range applyRiskGate(res, verified{Universe: testUniverse(t)}, model.RiskConfig{}) {
		if strings.Contains(f.Message, "scored by") {
			t.Errorf("single-stock mode refused the ticker it was asked about: %s", f.Message)
		}
	}
}
