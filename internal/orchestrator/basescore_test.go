package orchestrator

import (
	"fmt"
	"math"
	"reflect"
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
	//   weighted  = .35·.8 + .25·.6 − .15·.4 + 0 = 0.37
	//   covered   = .35 + .25 + .15 + .15        = 0.90
	//   signed    = 0.37 / 1.00                  = 0.37   (raw, auditable)
	//   reference = .35·.8+.25·.8+.15·.8+.15·.8+.10·.5 = 0.77
	//   scaled    = 0.37 / 0.77                  = 0.4805 → 48
	// The macro domain still votes 0 for having no data — the reference sums over
	// all five domains, so a missing one shrinks the score exactly as before.
	reports := []agents.ReportContext{
		domainReport("quant", "AAA bullish 8"),
		domainReport("news", "AAA bullish 6"),
		domainReport("fundamentals", "AAA bearish 4"),
		domainReport("sentiment", "AAA neutral 5"),
	}
	bases := computeBaseScores(testWeights, reports, shortlistOf("AAA"), nil)

	b := baseOf(t, bases, "AAA")
	if b.Direction != model.DirectionBuy {
		t.Errorf("direction = %q, want BUY", b.Direction)
	}
	if b.Confidence != 48 {
		t.Errorf("confidence = %d, want 48 (0.37 over the full weight, scored against the 0.77 reference)", b.Confidence)
	}
	if b.Signed < 0.369 || b.Signed > 0.371 {
		t.Errorf("Signed = %.4f, want the raw 0.37 kept for audit", b.Signed)
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
	// loud that domain is. Dividing by the total weight does most of that work on
	// its own; scoring against the reference then binds the caps at the extremes,
	// which is the job they are documented to do. A cap can only ever lower a
	// thin name, so it cannot lift one past a thick one.
	cases := []struct {
		name     string
		reports  []agents.ReportContext
		wantCap  int
		wantConf int
	}{
		// 0.35 raw → 0.35/0.77 = 45, capped to 40.
		{"quant only, 0.35 covered", []agents.ReportContext{
			domainReport("quant", "AAA bullish 10")}, 40, 40},
		// 0.50 raw → 0.50/0.77 = 65, capped to 55.
		{"quant+fundamentals, 0.50 covered", []agents.ReportContext{
			domainReport("quant", "AAA bullish 10"), domainReport("fundamentals", "AAA bullish 10")}, 55, 55},
		// 0.60 raw → 0.60/0.77 = 78, uncapped at 60% coverage.
		{"quant+news, 0.60 covered", []agents.ReportContext{
			domainReport("quant", "AAA bullish 10"), domainReport("news", "AAA bullish 10")}, 0, 78},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := baseOf(t, computeBaseScores(testWeights, tc.reports, shortlistOf("AAA"), nil), "AAA")
			if b.Cap != tc.wantCap {
				t.Errorf("cap = %d, want %d (covered %.2f)", b.Cap, tc.wantCap, b.CoveredWeight)
			}
			if b.Confidence != tc.wantConf {
				t.Errorf("confidence = %d, want %d", b.Confidence, tc.wantConf)
			}
		})
	}
}

// TestBaseScoreScalingPreservesOrdering is the property that must survive any
// future change to the confidence scale.
//
// Scoring against referenceTotal is a positive scalar multiply, so it may change
// what the numbers are and must never change which name outranks which. Getting
// that wrong is not hypothetical: renormalising *per name* over the covered
// weight is what e0b7584 had to undo, because it let a lone loud domain outrank
// five that partly disagreed.
//
// The fixture is the real 2026-09-01T14-47-26 shortlist, scores taken from that
// run's own specialist tails.
func TestBaseScoreScalingPreservesOrdering(t *testing.T) {
	bases := computeBaseScores(testWeights, run20260901Reports(), shortlistOf(
		"AMGN", "MRK", "BBVA.MC", "STLAM.MI", "IBM", "QCOM", "TTD", "ORCL",
		"O39.SI", "2454.TW", "BAYN.DE", "BMW.DE"), nil)

	// Ranking by the raw weighted score must match the shipped ranking by the
	// scaled one. Anything the coverage caps touch is excluded: a cap only ever
	// lowers a thin name, which is its job, and is not part of the scaling claim.
	var prevRaw, prevConf = 2.0, 101
	for _, b := range bases {
		if b.Cap > 0 && b.Confidence == b.Cap {
			continue
		}
		raw := abs(b.Signed)
		if raw > prevRaw+1e-9 {
			t.Errorf("%s: scaled ranking disagrees with the raw weighted ranking (raw %.4f after %.4f)",
				b.Ticker, raw, prevRaw)
		}
		if b.Confidence > prevConf {
			t.Errorf("%s: bases came back unsorted (%d after %d)", b.Ticker, b.Confidence, prevConf)
		}
		prevRaw, prevConf = raw, b.Confidence
	}

	// The scale itself: the run shipped 39/38/36/36/32 and every idea rendered as
	// a red bar. These are the same reports read on the corrected scale.
	want := map[string]int{
		"MRK": 51, "AMGN": 50, "STLAM.MI": 49, "BBVA.MC": 47, "IBM": 45, "QCOM": 45,
		"O39.SI": 36, "2454.TW": 32, "BAYN.DE": 27, "BMW.DE": 23,
	}
	for ticker, conf := range want {
		if got := baseOf(t, bases, ticker).Confidence; got != conf {
			t.Errorf("%s = %d, want %d", ticker, got, conf)
		}
	}

	// Thin coverage still ranks below thick, which is the guarantee e0b7584 bought
	// and the reason the divisor is global rather than per-name.
	if thin, thick := baseOf(t, bases, "O39.SI"), baseOf(t, bases, "IBM"); thin.Confidence >= thick.Confidence {
		t.Errorf("quant-only O39.SI (%d) must not outrank fully covered IBM (%d)",
			thin.Confidence, thick.Confidence)
	}
}

// run20260901Reports reproduces the specialist tails of the 2026-09-01T14-47-26
// run, which is the run this scale change was diagnosed from.
func run20260901Reports() []agents.ReportContext {
	return []agents.ReportContext{
		domainReport("quant",
			"AMGN bullish 7", "MRK bullish 6", "BBVA.MC bullish 6", "STLAM.MI bearish 5",
			"IBM bearish 5", "QCOM bearish 5", "TTD bearish 4", "ORCL bearish 5",
			"O39.SI bullish 8", "2454.TW bullish 7", "BAYN.DE bullish 6", "BMW.DE bearish 5"),
		domainReport("news",
			"AMGN bullish 5", "MRK bullish 6", "BBVA.MC bullish 6", "STLAM.MI bearish 5",
			"IBM bearish 3", "QCOM neutral 3", "TTD bearish 5", "ORCL bullish 5"),
		domainReport("fundamentals",
			"AMGN bullish 5", "MRK bullish 6", "BBVA.MC neutral 2", "STLAM.MI neutral 2",
			"IBM neutral 4", "QCOM bearish 4", "TTD bullish 6", "ORCL bearish 5"),
		domainReport("sentiment",
			"AMGN bearish 6", "MRK bearish 6", "BBVA.MC neutral 3", "STLAM.MI bearish 5",
			"IBM bearish 5", "QCOM bearish 6", "TTD bearish 5", "ORCL neutral 4"),
		domainReport("macro",
			"AMGN bullish 3", "MRK bullish 3", "IBM bearish 2", "QCOM bearish 2",
			"TTD bearish 3", "ORCL bearish 3"),
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
	}, shortlistOf("BAYN.DE", "AMGN"), nil)

	thin := baseOf(t, bases, "BAYN.DE") // .35·.6                      = 0.21 → /0.77 → 27
	thick := baseOf(t, bases, "AMGN")   // .35·.7+.25·.5−.15·.4+.10·.4 = 0.35 → /0.77 → 45
	if thin.Confidence != 27 {
		t.Errorf("quant-only BAYN.DE = %d, want 27", thin.Confidence)
	}
	if thick.Confidence != 45 {
		t.Errorf("fully covered AMGN = %d, want 45", thick.Confidence)
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
	bases := computeBaseScores(testWeights, reports, shortlistOf("AAA"), nil)
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
		shortlistOf("AAA", "ZZZ"), nil)
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
		}, shortlistOf("AAA"), nil), "AAA")

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
	res := buildDegradedIdeas(cfg, reports, shortlist, nil)
	bases := computeBaseScores(testWeights, reports, shortlist, nil)

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

// The base score is one number doing two jobs — how much evidence, and how much
// agreement — and the two are not the same fact. On 2026-09-01 O39.SI scored 32
// on one loud domain and MRK scored 31 on five that disagreed; the number said
// they were equally good ideas, and no reader of it could tell which was which.
func TestConsensusSeparatesAgreementFromCoverage(t *testing.T) {
	// One domain, nothing to disagree with.
	lone := computeBaseScores(testWeights, []agents.ReportContext{
		domainReport("quant", "AAA bullish 7"),
	}, []model.Candidate{{Ticker: "AAA"}}, nil)
	if got := lone[0].Consensus; math.Abs(got-1) > 1e-9 {
		t.Errorf("a single domain's consensus = %.2f, want 1", got)
	}

	// Five domains, one of them pointing the other way with equal force.
	//   signed: .35·.4 + .25·.7 + .15·0 − .15·.7 + .10·.3 = 0.240
	//   gross:  .35·.4 + .25·.7 + .15·0 + .15·.7 + .10·.3 = 0.450
	//   agree:  0.240 / 0.450 = 0.533
	split := computeBaseScores(testWeights, []agents.ReportContext{
		domainReport("quant", "BBB bullish 4"),
		domainReport("news", "BBB bullish 7"),
		domainReport("fundamentals", "BBB neutral 0"),
		domainReport("sentiment", "BBB bearish 7"),
		domainReport("macro", "BBB bullish 3"),
	}, []model.Candidate{{Ticker: "BBB"}}, nil)
	if got := split[0].Consensus; math.Abs(got-0.533) > 0.005 {
		t.Errorf("consensus = %.3f, want 0.533", got)
	}
	if split[0].CoveredWeight != 1 {
		t.Errorf("covered weight = %.2f, want 1 — the disagreement is not a coverage gap", split[0].CoveredWeight)
	}
	// Full coverage and a base of 31; the lone name above has 35% coverage and a
	// base of 36. Without `agree` those two numbers are the whole story a reader
	// gets, and they say the thin one is the better idea.
	if split[0].Confidence > lone[0].Confidence {
		t.Fatalf("fixture does not reproduce the case: split %d, lone %d", split[0].Confidence, lone[0].Confidence)
	}

	// A name nothing scored has no consensus to report rather than a false 1.
	none := computeBaseScores(testWeights, nil, []model.Candidate{{Ticker: "CCC"}}, nil)
	if none[0].Consensus != 0 {
		t.Errorf("an unscored name reported consensus %.2f, want 0", none[0].Consensus)
	}
}

// Consensus is a second axis, not a second opinion: it must not perturb the
// ranking the evidence produced.
func TestConsensusDoesNotChangeTheOrdering(t *testing.T) {
	got := computeBaseScores(testWeights, []agents.ReportContext{
		domainReport("quant", "THIN bullish 8", "THICK bullish 4"),
		domainReport("news", "THICK bullish 6"),
		domainReport("fundamentals", "THICK bearish 2"),
	}, []model.Candidate{{Ticker: "THIN"}, {Ticker: "THICK"}}, nil)

	thin, thick := baseOf(t, got, "THIN"), baseOf(t, got, "THICK")
	// THIN is unanimous because only one domain spoke; THICK has a dissenter.
	if thin.Consensus <= thick.Consensus {
		t.Errorf("consensus does not distinguish them: THIN %.2f, THICK %.2f", thin.Consensus, thick.Consensus)
	}
	// The ordering is whatever `base` said and nothing else. Here that puts the
	// one-domain name first — 36 against 34 — which is the case `agree` exists to
	// make visible rather than to correct: re-ranking on it would be re-doing the
	// weighting, and the weighting is the thing this file is the authority on.
	for i := 1; i < len(got); i++ {
		if got[i-1].Confidence < got[i].Confidence {
			t.Fatalf("bases are not ordered by confidence: %+v", got)
		}
	}
	if got[0].Ticker != "THIN" {
		t.Errorf("ranking = %s first, want THIN (base 36 vs 34) — consensus must not reorder", got[0].Ticker)
	}
}

// The Chief has to be able to see the difference, or the figure changes nothing.
func TestBaseScoreBlockShowsAgreement(t *testing.T) {
	block := baseScoreBlock(computeBaseScores(testWeights, []agents.ReportContext{
		domainReport("quant", "AAA bullish 6"),
		domainReport("news", "AAA bearish 6"),
	}, []model.Candidate{{Ticker: "AAA"}}, nil), 10)

	if !strings.Contains(block, "| agree |") {
		t.Errorf("no agreement column in the base score table:\n%s", block)
	}
	if !strings.Contains(block, "Do not adjust for it") {
		t.Errorf("the table does not say the figure is already inside base:\n%s", block)
	}
}

// A domain that looked at a name and measured the market's resting state is not
// a domain that failed to reach it. Sentiment's computed positioning verdict
// abstains on roughly seven names in ten by design, and pricing that as a gap
// charged every one of them the domain's full weight for working correctly.
func TestAMeasuredAbstentionLeavesTheConfidenceScale(t *testing.T) {
	// Both names carry the identical signed evidence:
	//   weighted = .35·.8 + .25·.6 + .15·.4 = 0.49
	//   covered  = .35 + .25 + .15          = 0.75
	// ABSTAIN's sentiment stood down; GAP's sentiment never reached it.
	//   GAP:      0.49 / 0.77                       = 0.636 → 64
	//   ABSTAIN:  0.49 / (0.77 − .15·.8) = 0.49/0.65 = 0.754 → 75
	reports := []agents.ReportContext{
		domainReport("quant", "ABSTAIN bullish 8", "GAP bullish 8"),
		domainReport("news", "ABSTAIN bullish 6", "GAP bullish 6"),
		domainReport("fundamentals", "ABSTAIN bullish 4", "GAP bullish 4"),
	}
	bases := computeBaseScores(testWeights, reports, shortlistOf("ABSTAIN", "GAP"),
		map[string]map[string]bool{"ABSTAIN": {"sentiment": true}})

	ab, gap := baseOf(t, bases, "ABSTAIN"), baseOf(t, bases, "GAP")
	if gap.Confidence != 64 {
		t.Errorf("GAP confidence = %d, want 64 — a domain with no data still votes 0 against the full scale", gap.Confidence)
	}
	if ab.Confidence != 75 {
		t.Errorf("ABSTAIN confidence = %d, want 75 — sentiment's weight leaves the scale, it does not vote against it", ab.Confidence)
	}
	if !reflect.DeepEqual(ab.Abstained, []string{"sentiment"}) {
		t.Errorf("Abstained = %v, want [sentiment] recorded so the artifact says which domain left the scale", ab.Abstained)
	}
	if gap.Abstained != nil {
		t.Errorf("GAP recorded abstentions %v, want none — it is a coverage gap", gap.Abstained)
	}

	// Only the scale moved. The raw weighted vote and the coverage share are the
	// auditable figures and both must read the same for two names holding the
	// same evidence.
	if math.Abs(ab.Signed-gap.Signed) > 1e-9 {
		t.Errorf("Signed diverged: ABSTAIN %.4f, GAP %.4f — the relief is to the denominator only", ab.Signed, gap.Signed)
	}
	if math.Abs(ab.CoveredWeight-gap.CoveredWeight) > 1e-9 {
		t.Errorf("CoveredWeight diverged: ABSTAIN %.3f, GAP %.3f — coverage is still measured against the full weight",
			ab.CoveredWeight, gap.CoveredWeight)
	}
}

// The relief must not become the covered-weight renormalisation this file's own
// comment records as an inversion: on 2026-09-01 dividing by what was actually
// present put three quant-only foreign listings above the one name all five
// domains had read.
//
// Coverage is still measured against the *full* weight, so the scarce/thin caps
// keep their grip on a name carried by one loud domain — which is the case those
// caps were kept for. Three abstaining domains is not a shape today's data path
// can produce (only sentiment abstains), and that is the point: the bound has to
// hold if the weights or the abstaining domains change.
func TestAbstentionReliefCannotInvertThinAgainstThickCoverage(t *testing.T) {
	got := computeBaseScores(testWeights, []agents.ReportContext{
		domainReport("quant", "THIN bullish 8", "THICK bullish 4"),
		domainReport("news", "THICK bullish 6"),
		domainReport("fundamentals", "THICK bearish 2"),
		domainReport("sentiment", "THICK bullish 5"),
	}, shortlistOf("THIN", "THICK"), map[string]map[string]bool{
		"THIN": {"news": true, "fundamentals": true, "sentiment": true},
	})

	thin, thick := baseOf(t, got, "THIN"), baseOf(t, got, "THICK")
	// THIN's relieved scale is 0.33 and its 0.28 vote scores 85 against it — the
	// runaway. Its covered weight is 0.35, under scarceCoverage, so the cap binds
	// at 40 and it stays below the four-domain name.
	if thin.Cap != scarceCap {
		t.Fatalf("THIN cap = %d, want the scarce cap %d — coverage must still be read off the full weight", thin.Cap, scarceCap)
	}
	if thin.Confidence != scarceCap {
		t.Errorf("THIN confidence = %d, want it held at the scarce cap %d", thin.Confidence, scarceCap)
	}
	if thick.Confidence <= thin.Confidence {
		t.Errorf("relief inverted the ordering: THIN %d, THICK %d", thin.Confidence, thick.Confidence)
	}
	if got[0].Ticker != "THICK" {
		t.Errorf("ranking = %s first, want THICK — four domains read it and one read THIN", got[0].Ticker)
	}
}

// A domain that scored the name after all keeps its vote: the score is a
// stronger statement than the verdict that said it would have nothing to say,
// and the enforcement pass has already deleted the ones that contradicted it.
func TestAScoredDomainIsNotRelievedEvenIfItWasListedAsAbstaining(t *testing.T) {
	with := computeBaseScores(testWeights, []agents.ReportContext{
		domainReport("quant", "AAA bullish 8"),
		domainReport("sentiment", "AAA bullish 6"),
	}, shortlistOf("AAA"), map[string]map[string]bool{"AAA": {"sentiment": true}})
	without := computeBaseScores(testWeights, []agents.ReportContext{
		domainReport("quant", "AAA bullish 8"),
		domainReport("sentiment", "AAA bullish 6"),
	}, shortlistOf("AAA"), nil)

	if with[0].Confidence != without[0].Confidence {
		t.Errorf("a scored domain was relieved anyway: %d vs %d", with[0].Confidence, without[0].Confidence)
	}
	if with[0].Abstained != nil {
		t.Errorf("Abstained = %v, want none — sentiment voted", with[0].Abstained)
	}
}

// The Chief has to be able to tell the two apart, or the arithmetic says one
// thing and the table it reads says another.
func TestBaseScoreBlockDistinguishesAnAbstentionFromAnAbsence(t *testing.T) {
	block := baseScoreBlock(computeBaseScores(testWeights, []agents.ReportContext{
		domainReport("quant", "AAA bullish 6"),
	}, shortlistOf("AAA"), map[string]map[string]bool{"AAA": {"sentiment": true}}), 10)

	// quant scored, sentiment stood down, news and fundamentals had nothing.
	if !strings.Contains(block, "| +6 | · | · | ~ |") {
		t.Errorf("the row does not separate a stand-down from a gap:\n%s", block)
	}
	if !strings.Contains(block, "absence of a reading") {
		t.Errorf("the header does not tell the Chief what `~` means:\n%s", block)
	}
}

// The header the Chief reads has to describe the scale the arithmetic actually
// produces. It used to promise that a well-supported idea "lands in the 50s–70s
// rather than the 30s" while the code's own note recorded the highest base
// anywhere as 45.5 — so the Chief was told every board it ever saw was weak.
func TestBaseScoreBlockStatesTheMeasuredRange(t *testing.T) {
	block := baseScoreBlock(computeBaseScores(testWeights, []agents.ReportContext{
		domainReport("quant", "AAA bullish 6"),
		domainReport("news", "AAA bullish 6"),
	}, shortlistOf("AAA"), nil), 10)

	if !strings.Contains(block, "41–67") {
		t.Errorf("the header does not state the measured top-of-board range:\n%s", block)
	}
	if strings.Contains(block, "50s–70s") {
		t.Errorf("the header still carries the range no run has produced:\n%s", block)
	}
	// And it has to say which way to read a number in that range, or restating
	// the range changes nothing about how the Chief uses it.
	if !strings.Contains(block, "Rank on the spread between them") {
		t.Errorf("the header does not tell the Chief how to read the scale:\n%s", block)
	}
}
