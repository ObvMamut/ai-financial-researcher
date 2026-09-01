package orchestrator

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// gateMetrics is a name whose 10-day 1σ move is exactly $10 on a $100 stock:
// σ_daily 3.1623% × √10 × 100 = 10.00. Every level in these tests is stated in
// dollars so the σ arithmetic is legible.
func gateMetrics(sym string) quant.Metrics {
	return quant.Metrics{
		Symbol: sym, AsOf: "2026-08-28", LastClose: 100,
		SigmaDaily: 0.1 / math.Sqrt(10), AvgDollarVol20: 500e6,
		Benchmark: "SPY", Beta: 1.0,
	}
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
	// Breakeven win rate is risk / (risk + reward) = 10 / 30.
	if got := good.Ideas[0].BreakevenWinRate; math.Abs(got-1.0/3.0) > 1e-6 {
		t.Errorf("breakeven win rate = %.4f, want %.4f", got, 1.0/3.0)
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
	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		gateIdeaAt("AAA", 88, 124), gateIdeaAt("BBB", 88, 124), gateIdeaAt("CCC", 88, 124),
	}}
	fs := applyRiskGate(res, v, model.RiskConfig{})
	if !hasBookFinding(fs, "one bet in two tickets") {
		t.Errorf("two perfectly correlated longs must be flagged: %v", fs)
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
		"AAA": {Symbol: "AAA", LastClose: 100, SigmaDaily: 0.02, AvgDollarVol20: 5e8},
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
