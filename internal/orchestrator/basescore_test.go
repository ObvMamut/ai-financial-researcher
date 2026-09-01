package orchestrator

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// testWeights are the Phase 3.B horizon-matched defaults, stated here so these
// tests keep testing the arithmetic rather than the current config.
var testWeights = model.DomainWeights{
	Quant: 0.35, News: 0.25, Fundamentals: 0.15, Sentiment: 0.15, Macro: 0.10,
}

// domainReport builds a specialist report whose JSON tail scores the given
// tickers, in the shape the specialists actually emit.
func domainReport(domain string, scores ...string) agents.ReportContext {
	var sb strings.Builder
	sb.WriteString("Prose about " + domain + ".\n\n```json\n{\n  \"domain\": \"" + domain + "\",\n  \"scores\": [\n")
	for i, s := range scores {
		var tk, bias string
		var strength int
		fmt.Sscanf(s, "%s %s %d", &tk, &bias, &strength)
		sb.WriteString(fmt.Sprintf("    {\"ticker\": %q, \"bias\": %q, \"strength\": %d, \"note\": \"n\"}", tk, bias, strength))
		if i < len(scores)-1 {
			sb.WriteString(",")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("  ],\n  \"missing\": []\n}\n```\n")
	return agents.ReportContext{Domain: domain, Content: sb.String()}
}

func baseOf(t *testing.T, bases []BaseScore, ticker string) BaseScore {
	t.Helper()
	for _, b := range bases {
		if b.Ticker == ticker {
			return b
		}
	}
	t.Fatalf("%s missing from base scores %+v", ticker, bases)
	return BaseScore{}
}

// testUniverse is the real curated universe; the synthetic tickers in these
// tests are deliberately absent from it, so universe enrichment is a no-op and
// only the shortlist and base scores are under test.
func testUniverse(t *testing.T) *universe.Universe {
	t.Helper()
	uni, err := universe.Load()
	if err != nil {
		t.Fatal(err)
	}
	return uni
}

func hasWarning(ws []warning, ticker, substr string) bool {
	for _, w := range ws {
		if w.Ticker == ticker && strings.Contains(w.Message, substr) {
			return true
		}
	}
	return false
}

func shortlistOf(tickers ...string) []model.Candidate {
	out := make([]model.Candidate, 0, len(tickers))
	for _, t := range tickers {
		out = append(out, model.Candidate{Ticker: t, Name: t + " Inc", Index: "sp500"})
	}
	return out
}

func TestComputeBaseScoresIsWeightedOverTotalWeight(t *testing.T) {
	// AAA: quant +8 (.35), news +6 (.25), fundamentals −4 (.15), sentiment
	// neutral (.15, covered but unsigned), macro absent (.10, votes 0).
	//   weighted = .35·.8 + .25·.6 − .15·.4 + 0 = 0.37
	//   covered  = .35 + .25 + .15 + .15       = 0.90
	//   signed   = 0.37 / 1.00                 = 0.37 → 37
	reports := []agents.ReportContext{
		domainReport("quant", "AAA bullish 8"),
		domainReport("news", "AAA bullish 6"),
		domainReport("fundamentals", "AAA bearish 4"),
		domainReport("sentiment", "AAA neutral 5"),
	}
	bases := computeBaseScores(testWeights, reports, shortlistOf("AAA"))

	b := baseOf(t, bases, "AAA")
	if b.Direction != model.DirectionBuy {
		t.Errorf("direction = %q, want BUY", b.Direction)
	}
	if b.Confidence != 37 {
		t.Errorf("confidence = %d, want 37 (0.37 over the full weight)", b.Confidence)
	}
	if b.CoveredWeight < 0.899 || b.CoveredWeight > 0.901 {
		t.Errorf("covered weight = %.3f, want 0.90", b.CoveredWeight)
	}
	if b.Cap != 0 {
		t.Errorf("cap = %d, want none at 90%% coverage", b.Cap)
	}
	want := map[string]int{"quant": 8, "news": 6, "fundamentals": -4, "sentiment": 0}
	for d, v := range want {
		if b.Domains[d] != v {
			t.Errorf("Domains[%s] = %d, want %d", d, b.Domains[d], v)
		}
	}
	if _, ok := b.Domains["macro"]; ok {
		t.Errorf("a domain that never scored the ticker must be absent, got %v", b.Domains)
	}
}

func TestBaseScoreCapsThinCoverage(t *testing.T) {
	// A name only one domain looked at cannot be a 100-confidence idea however
	// loud that domain is. Dividing by the total weight enforces that on its own
	// — the strongest possible quant-only read is 0.35 — so the caps are recorded
	// but do not bind. They stay as a redundant floor if the weights change.
	cases := []struct {
		name     string
		reports  []agents.ReportContext
		wantCap  int
		wantConf int
	}{
		{"quant only, 0.35 covered", []agents.ReportContext{
			domainReport("quant", "AAA bullish 10")}, 40, 35},
		{"quant+fundamentals, 0.50 covered", []agents.ReportContext{
			domainReport("quant", "AAA bullish 10"), domainReport("fundamentals", "AAA bullish 10")}, 55, 50},
		{"quant+news, 0.60 covered", []agents.ReportContext{
			domainReport("quant", "AAA bullish 10"), domainReport("news", "AAA bullish 10")}, 0, 60},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := baseOf(t, computeBaseScores(testWeights, tc.reports, shortlistOf("AAA")), "AAA")
			if b.Cap != tc.wantCap {
				t.Errorf("cap = %d, want %d (covered %.2f)", b.Cap, tc.wantCap, b.CoveredWeight)
			}
			if b.Confidence != tc.wantConf {
				t.Errorf("confidence = %d, want %d", b.Confidence, tc.wantConf)
			}
		})
	}
}

// TestBaseScoreRanksThickCoverageAboveThinCoverage pins the defect the
// 2026-09-01 run shipped: three quant-only foreign listings ranked above the one
// name every domain had read.
//
// The numbers are that run's, from its own reports. BAYN.DE was scored by quant
// alone at +6 and reached 40 (its coverage cap) while AMGN — quant +7, news +5,
// fundamentals neutral, sentiment −4, macro +4 — reached 35, because dividing by
// the covered weight let one loud domain keep its full magnitude.
func TestBaseScoreRanksThickCoverageAboveThinCoverage(t *testing.T) {
	bases := computeBaseScores(testWeights, []agents.ReportContext{
		domainReport("quant", "BAYN.DE bullish 6", "AMGN bullish 7"),
		domainReport("news", "AMGN bullish 5"),
		domainReport("fundamentals", "AMGN neutral 4"),
		domainReport("sentiment", "AMGN bearish 4"),
		domainReport("macro", "AMGN bullish 4"),
	}, shortlistOf("BAYN.DE", "AMGN"))

	thin := baseOf(t, bases, "BAYN.DE") // .35·.6            = 0.21 → 21
	thick := baseOf(t, bases, "AMGN")   // .35·.7+.25·.5−.15·.4+.10·.4 = 0.35 → 35
	if thin.Confidence != 21 {
		t.Errorf("quant-only BAYN.DE = %d, want 21", thin.Confidence)
	}
	if thick.Confidence != 35 {
		t.Errorf("fully covered AMGN = %d, want 35", thick.Confidence)
	}
	if thin.Confidence >= thick.Confidence {
		t.Errorf("one domain at +6 (%d) must not outrank five domains averaging +3.5 (%d)",
			thin.Confidence, thick.Confidence)
	}
	if bases[0].Ticker != "AMGN" {
		t.Errorf("ranked %s first, want AMGN — the name every domain could read", bases[0].Ticker)
	}
}

func TestBaseScoreRejectsOffShortlistAndClampsStrength(t *testing.T) {
	reports := []agents.ReportContext{
		domainReport("quant", "AAA bullish 99", "GHOST bullish 10"),
	}
	bases := computeBaseScores(testWeights, reports, shortlistOf("AAA"))
	for _, b := range bases {
		if b.Ticker == "GHOST" {
			t.Fatalf("a ticker that was never on the shortlist reached the base scores: %+v", b)
		}
	}
	if got := baseOf(t, bases, "AAA").Domains["quant"]; got != 10 {
		t.Errorf("strength 99 clamped to %d, want 10", got)
	}
}

func TestBaseScoreCoversEveryShortlistedTicker(t *testing.T) {
	// A name no domain scored is not absent from the block — it is present with
	// zero coverage, which is what tells the Chief it has nothing to reason from.
	bases := computeBaseScores(testWeights,
		[]agents.ReportContext{domainReport("quant", "AAA bullish 8")},
		shortlistOf("AAA", "ZZZ"))
	if len(bases) != 2 {
		t.Fatalf("got %d base scores, want one per shortlisted name", len(bases))
	}
	z := baseOf(t, bases, "ZZZ")
	if z.CoveredWeight != 0 || z.Confidence != 0 || z.Direction != "" {
		t.Errorf("uncovered name should score nothing: %+v", z)
	}
	if bases[0].Ticker != "AAA" {
		t.Errorf("base scores should rank by confidence, got %s first", bases[0].Ticker)
	}

	block := baseScoreBlock(bases, 10)
	for _, want := range []string{"AAA", "ZZZ", "quant", "±10"} {
		if !strings.Contains(block, want) {
			t.Errorf("base-score block missing %q:\n%s", want, block)
		}
	}
	if baseScoreBlock(nil, 10) != "" {
		t.Errorf("empty base scores should render no block")
	}
}

func TestBaseScoreForDirectionOpposesToZero(t *testing.T) {
	// The band anchors on the base *for the direction being proposed*. Every
	// domain reading bearish means a BUY starts from zero, not from 60.
	b := baseOf(t, computeBaseScores(testWeights,
		[]agents.ReportContext{
			domainReport("quant", "AAA bearish 8"),
			domainReport("news", "AAA bearish 6"),
			domainReport("fundamentals", "AAA bearish 6"),
			domainReport("sentiment", "AAA bearish 6"),
			domainReport("macro", "AAA bearish 6"),
		}, shortlistOf("AAA")), "AAA")

	if b.Direction != model.DirectionSell {
		t.Fatalf("direction = %q, want SELL", b.Direction)
	}
	if got := b.For(model.DirectionSell); got != b.Confidence {
		t.Errorf("For(SELL) = %d, want %d", got, b.Confidence)
	}
	if got := b.For(model.DirectionBuy); got != 0 {
		t.Errorf("For(BUY) = %d, want 0 — the domains all read the other way", got)
	}
}

func TestDegradedIdeasFollowTheBaseScores(t *testing.T) {
	// The degraded path and the Chief's base block must not be two different
	// opinions of the same reports: one is the other, capped.
	reports := []agents.ReportContext{
		domainReport("quant", "AAA bullish 9", "BBB bearish 7"),
		domainReport("news", "AAA bullish 7", "BBB bearish 5"),
		domainReport("fundamentals", "AAA bullish 6", "BBB bearish 6"),
		domainReport("sentiment", "AAA bullish 6", "BBB bearish 6"),
		domainReport("macro", "AAA bullish 6", "BBB bearish 6"),
	}
	shortlist := shortlistOf("AAA", "BBB")
	cfg := Config{Mode: model.ModeIndependent, Weights: testWeights}
	res := buildDegradedIdeas(cfg, reports, shortlist)
	bases := computeBaseScores(testWeights, reports, shortlist)

	if len(res.Ideas) != 2 {
		t.Fatalf("got %d degraded ideas, want 2", len(res.Ideas))
	}
	for _, idea := range res.Ideas {
		b := baseOf(t, bases, strings.ToUpper(idea.Ticker))
		if idea.Direction != b.Direction {
			t.Errorf("%s degraded direction %q vs base %q", idea.Ticker, idea.Direction, b.Direction)
		}
		want := b.Confidence
		if want > 55 {
			want = 55 // no cross-domain reasoning happened on this path
		}
		if idea.Confidence != want {
			t.Errorf("%s degraded confidence %d, want %d (base %d capped at 55)",
				idea.Ticker, idea.Confidence, want, b.Confidence)
		}
		if len(idea.DomainScores) != len(b.Domains) {
			t.Errorf("%s should carry its per-domain scores for later attribution, got %v",
				idea.Ticker, idea.DomainScores)
		}
	}
}

func TestValidateIdeasClampsConfidenceToTheBand(t *testing.T) {
	// Confidence used to be whatever the Chief asserted. It is now an
	// adjustment: the computed base ± the band, with the reason named. A model
	// that writes 88 over a base of 41 is not being confident, it is being
	// unaccountable.
	bases := []BaseScore{
		{Ticker: "AAA", Direction: model.DirectionBuy, Confidence: 41, CoveredWeight: 0.9,
			Domains: map[string]int{"quant": 8}},
		{Ticker: "BBB", Direction: model.DirectionSell, Confidence: 60, CoveredWeight: 0.5, Cap: 55,
			Domains: map[string]int{"quant": -8}},
	}
	cfg := Config{Mode: model.ModeIndependent, Weights: testWeights, ChiefAdjustBand: 10}
	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		{Ticker: "AAA", Direction: model.DirectionBuy, Confidence: 88},
		{Ticker: "BBB", Direction: model.DirectionSell, Confidence: 55},
		{Ticker: "CCC", Direction: model.DirectionBuy, Confidence: 70},
	}}
	ws := validateIdeas(res, cfg, verified{Universe: testUniverse(t), Shortlist: shortlistOf("AAA", "BBB", "CCC"), Bases: bases})

	if got := res.Ideas[0].Confidence; got != 51 {
		t.Errorf("AAA confidence = %d, want 51 (base 41 + band 10)", got)
	}
	if res.Ideas[0].BaseConfidence != 41 || res.Ideas[0].DomainScores["quant"] != 8 {
		t.Errorf("the base an idea was anchored to must travel with it: %+v", res.Ideas[0])
	}
	if got := res.Ideas[1].Confidence; got != 55 {
		t.Errorf("BBB confidence = %d, want 55 — inside the band, left alone", got)
	}
	if !hasWarning(ws, "AAA", "outside the computed base 41") {
		t.Errorf("clamping must be visible in the warnings, got %+v", ws)
	}
	// A name with no computed base is reported, not silently trusted.
	if !hasWarning(ws, "CCC", "no computed base score") {
		t.Errorf("an idea with no base should warn, got %+v", ws)
	}
	if res.Ideas[2].Confidence != 70 {
		t.Errorf("an unbaselined idea must not be clamped to zero, got %d", res.Ideas[2].Confidence)
	}
}

func TestValidateIdeasClampsAContrarianCallToTheFloor(t *testing.T) {
	// Every domain reading bearish is not evidence for a BUY. The base for the
	// proposed direction is zero, so the most the Chief may claim is the band.
	bases := []BaseScore{{Ticker: "AAA", Direction: model.DirectionSell, Confidence: 70, CoveredWeight: 1}}
	cfg := Config{Mode: model.ModeIndependent, Weights: testWeights, ChiefAdjustBand: 10}
	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		{Ticker: "AAA", Direction: model.DirectionBuy, Confidence: 75},
	}}
	validateIdeas(res, cfg, verified{Universe: testUniverse(t), Shortlist: shortlistOf("AAA"), Bases: bases})
	if got := res.Ideas[0].Confidence; got != 10 {
		t.Errorf("contrarian confidence = %d, want 10 (base 0 for BUY + band)", got)
	}
}

func TestValidateIdeasHonoursTheCoverageCap(t *testing.T) {
	// The band may not be used to climb over a coverage cap: 55 + 10 is still 55.
	bases := []BaseScore{{Ticker: "AAA", Direction: model.DirectionBuy, Confidence: 55, Cap: 55, CoveredWeight: 0.5}}
	cfg := Config{Mode: model.ModeIndependent, Weights: testWeights, ChiefAdjustBand: 10}
	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		{Ticker: "AAA", Direction: model.DirectionBuy, Confidence: 90},
	}}
	validateIdeas(res, cfg, verified{Universe: testUniverse(t), Shortlist: shortlistOf("AAA"), Bases: bases})
	if got := res.Ideas[0].Confidence; got != 55 {
		t.Errorf("confidence = %d, want 55 — the coverage cap binds over the band", got)
	}
}
