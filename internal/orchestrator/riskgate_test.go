package orchestrator

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
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
// was provably suicidal rather than whether it was worth doing. The 2026-09-01
// run shipped ideas at +3.0 and +5.7 bps against a 30 bps cost assumption.
func TestRiskGateEnforcesAnExpectancyFloor(t *testing.T) {
	res := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 85, 130)}}
	cfg := model.RiskConfig{MinExpectancyBps: 100}
	fs := applyRiskGate(res, gateVerified(t, "AAA"), cfg)

	if !hasHard(fs, "AAA", "under the 100 bps floor") {
		t.Errorf("a positive expectancy below the floor was accepted: %+.1f bps, findings %v",
			res.Ideas[0].ExpectancyBps, findingsFor(fs, "AAA"))
	}
	if got := res.Ideas[0].ExpectancyBps; got <= 0 {
		t.Fatalf("fixture expectancy = %+.1f bps; this test needs a *positive* one under the floor", got)
	}
	// The wording has to distinguish the two: "loses money" is false about a
	// geometry earning +33 bps, it is merely not earning enough.
	if !hasHard(fs, "AAA", "indistinguishable from zero") {
		t.Errorf("a positive-but-thin expectancy was reported as a loss: %v", findingsFor(fs, "AAA"))
	}

	// The same geometry clears a floor it actually beats.
	clear := &model.IdeasResult{Ideas: []model.TradeIdea{gateIdeaAt("AAA", 85, 130)}}
	if fs := applyRiskGate(clear, gateVerified(t, "AAA"), model.RiskConfig{}); hasHard(fs, "AAA", "expectancy") {
		t.Errorf("a sound geometry was refused by the default floor: %v", findingsFor(fs, "AAA"))
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

func TestRealizedEdgeReplacesThePriorOnceThereIsARecord(t *testing.T) {
	// The 0.02 prior is an assumption. Once the pipeline has enough closed
	// trades, the drift the simulation assumes should be the drift the pipeline
	// has actually delivered.
	idea := &model.TradeIdea{
		Ticker: "AAA", Direction: model.DirectionBuy,
		Entry: 100, Stop: 96, Target: 110, TimeframeDays: 10,
	}
	m := quant.Metrics{Symbol: "AAA", LastClose: 100, SigmaDaily: 0.02}

	// risk is 4% of entry; over 10 days at σ=2%/day the simulation accumulates
	// edge·σ·days, so an average of +0.2R per trade implies edge = 0.2·0.04 / 0.2.
	got, ok := realizedEdgeSigma(idea, m, 0.2, 10)
	if !ok {
		t.Fatal("no edge computed from a usable geometry")
	}
	if want := 0.04; math.Abs(got-want) > 1e-9 {
		t.Errorf("edge = %.4f, want %.4f", got, want)
	}
	// And it is a measurement, not the prior: a different record moves it.
	if same, _ := realizedEdgeSigma(idea, m, 0.1, 10); same == got {
		t.Error("the edge does not depend on the measured record")
	}
}

func TestRealizedEdgeIsClampedInBothDirections(t *testing.T) {
	// A finite sample can produce a number that would make the expectancy check
	// vacuous (measured: at 0.05 every permitted geometry already passes) or
	// reject everything outright. Neither belongs in a gate.
	idea := &model.TradeIdea{
		Ticker: "AAA", Direction: model.DirectionBuy,
		Entry: 100, Stop: 96, Target: 110, TimeframeDays: 10,
	}
	m := quant.Metrics{Symbol: "AAA", LastClose: 100, SigmaDaily: 0.02}

	if got, _ := realizedEdgeSigma(idea, m, 5.0, 10); got != edgeSigmaCap {
		t.Errorf("a +5R record produced edge %.4f, want the cap %.4f", got, edgeSigmaCap)
	}
	if got, _ := realizedEdgeSigma(idea, m, -5.0, 10); got != -edgeSigmaCap {
		t.Errorf("a −5R record produced edge %.4f, want %.4f", got, -edgeSigmaCap)
	}
}

func TestRealizedNegativeEdgeMakesTheGateStopShipping(t *testing.T) {
	// If the measured record is a losing one, the honest response is to reject
	// geometries that only worked under an assumed edge — not to keep assuming.
	idea := &model.TradeIdea{
		Ticker: "AAA", Direction: model.DirectionBuy,
		Entry: 100, Stop: 96, Target: 110, TimeframeDays: 10,
	}
	m := quant.Metrics{Symbol: "AAA", LastClose: 100, SigmaDaily: 0.02}
	cfg := riskDefaults(model.RiskConfig{})

	optimistic := simulateExpectancy(idea, m, cfg, 10)

	losing := cfg
	losing.EdgeSigmaDaily, _ = realizedEdgeSigma(idea, m, -0.4, 10)
	pessimistic := simulateExpectancy(idea, m, losing, 10)

	if pessimistic >= optimistic {
		t.Errorf("a losing record did not lower expectancy: %.1f bps vs %.1f bps", pessimistic, optimistic)
	}
	if pessimistic >= 0 {
		t.Errorf("expectancy is %+.1f bps under a −0.4R record — the gate would keep shipping", pessimistic)
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
	measured := mk()
	findings := gateIdea(measured, v, cfg)

	if measured.ExpectancyBps >= prior.ExpectancyBps {
		t.Fatalf("realized edge did not reach the simulation: %.1f vs %.1f bps",
			measured.ExpectancyBps, prior.ExpectancyBps)
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
