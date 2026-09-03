package orchestrator

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// regimePack builds a quant pack in which each ticker is measured against the
// named benchmark, and every named benchmark has computed regime metrics. It is
// what grounds the macro domain.
func regimePack(byTicker map[string]string) *quant.Pack {
	p := quant.NewPack()
	for t, bench := range byTicker {
		p.ByTicker[t] = quant.Metrics{Symbol: t, Benchmark: bench}
		if bench != "" {
			p.Benchmarks[bench] = quant.Metrics{Symbol: bench}
		}
	}
	return p
}

func packWith(domain string, covered map[string]bool, macro []marketdata.Fact) *marketdata.DataPack {
	p := marketdata.NewDataPack(domain)
	for t, ok := range covered {
		p.Coverage[t] = ok
		if ok {
			// A covered ticker carries at least one fact of the domain's own
			// evidence — coverage means evidence, not merely that a provider
			// answered. See marketdata.HasDomainEvidence.
			p.ByTicker[t] = marketdata.TickerData{Ticker: t, Facts: []marketdata.Fact{
				{Label: "Headline 1", Value: "something happened"},
			}}
		}
	}
	p.MacroFacts = macro
	return p
}

// grounded used to be `dataBlock != ""`, which was true for every role the
// moment any macro fact existed — so a sentiment pack holding literally nothing
// reported itself grounded and the run called that complete.
func TestGroundedForIsPerDomain(t *testing.T) {
	macro := []marketdata.Fact{{Label: "10-Year Treasury Rate", Value: "4.1"}}
	pack := packWith("sentiment", map[string]bool{"AAPL": false, "MSFT": false}, macro)

	if groundedFor("sentiment", pack, nil) {
		t.Error("a sentiment pack with no per-ticker rows is not grounded")
	}
	if !groundedFor("macro", pack, regimePack(map[string]string{"AAPL": "^GSPC"})) {
		t.Error("macro is grounded by the computed market regime, not per-ticker rows")
	}

	qp := &quant.Pack{ByTicker: map[string]quant.Metrics{"AAPL": {}}}
	if !groundedFor("quant", pack, qp) {
		t.Error("quant is grounded by the computed metrics pack")
	}
	if groundedFor("quant", pack, &quant.Pack{ByTicker: map[string]quant.Metrics{}}) {
		t.Error("an empty metrics pack does not ground the quant role")
	}
}

func TestUngroundedForListsTheGaps(t *testing.T) {
	pack := packWith("news", map[string]bool{"AAPL": true, "MSFT": false, "NVDA": false}, nil)
	got := ungroundedFor("news", pack, nil, []string{"AAPL", "MSFT", "NVDA"})
	if want := []string{"MSFT", "NVDA"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ungroundedFor = %v, want %v", got, want)
	}

	// Macro covers a name whose market this run computed a regime for.
	macroPack := packWith("macro", map[string]bool{"AAPL": false}, []marketdata.Fact{{Label: "CPI"}})
	qp := regimePack(map[string]string{"AAPL": "^GSPC", "MSFT": "^GSPC"})
	if got := ungroundedFor("macro", macroPack, qp, []string{"AAPL", "MSFT"}); len(got) != 0 {
		t.Errorf("the regime covers both names' market, got gaps %v", got)
	}
}

// Macro grounds on the market regime, which is computed per exchange from the
// benchmark's own daily bars — the same evidence agents/macro.md is handed and
// calls its primary source.
//
// It used to ground on `IsUSListing && FRED facts`, a rule from when FRED was
// the whole of its evidence. The regime block was added to the macro prompt
// afterwards and this predicate stayed behind, so on 2026-09-01 the domain was
// given evidence for twelve names, scored twelve, and had seven deleted for
// having none — past the confabulation threshold, which marked the run degraded
// on an enforcement error rather than an agent one.
func TestMacroGroundsOnTheComputedRegime(t *testing.T) {
	pack := packWith("macro", map[string]bool{"AAPL": false, "2330.TW": false},
		[]marketdata.Fact{{Label: "CPI"}})
	qp := regimePack(map[string]string{"AAPL": "^GSPC", "2330.TW": "^TWII"})

	if !coveredBy("macro", pack, qp, "AAPL") {
		t.Error("a US listing is covered by its own index regime")
	}
	if !coveredBy("macro", pack, qp, "2330.TW") {
		t.Error("a Taiwanese listing is covered by ^TWII, which this run priced")
	}
	if got := ungroundedFor("macro", pack, qp, []string{"AAPL", "2330.TW"}); len(got) != 0 {
		t.Errorf("ungroundedFor(macro) = %v, want none", got)
	}

	// A benchmark this run never priced is a real gap, and now registers as one
	// instead of passing silently as it did while macro was US-only.
	partial := regimePack(map[string]string{"AAPL": "^GSPC"})
	partial.ByTicker["2330.TW"] = quant.Metrics{Symbol: "2330.TW", Benchmark: "^TWII"}
	if coveredBy("macro", pack, partial, "2330.TW") {
		t.Error("a name whose benchmark was never priced is not covered")
	}
	if want := []string{"2330.TW"}; !reflect.DeepEqual(
		ungroundedFor("macro", pack, partial, []string{"AAPL", "2330.TW"}), want) {
		t.Errorf("an unpriced benchmark should be the only gap")
	}

	// No regime at all: nothing is covered, FRED facts or not.
	if coveredBy("macro", pack, nil, "AAPL") {
		t.Error("macro with no computed regime covers nothing")
	}
	if groundedFor("macro", pack, quant.NewPack()) {
		t.Error("macro with an empty quant pack is not grounded")
	}

	// And every listing is groundable, because the regime source is global.
	for _, tk := range []string{"AAPL", "2330.TW", "BMW.DE"} {
		if !groundableBy("macro", tk) {
			t.Errorf("macro should be groundable for %s: its benchmark is priced from the same global source as quant", tk)
		}
	}
}

// coverageGaps used to skip regime domains outright, so a run whose own macro
// report listed all 12 tickers missing still recorded `outcome: complete`.
func TestCoverageGapsIncludeMacro(t *testing.T) {
	statuses := []model.DomainStatus{
		{Domain: "macro", Grounded: true, Ungrounded: []string{"AAPL", "2330.TW"}},
	}
	gaps := coverageGaps(statuses)
	if len(gaps) != 1 || gaps[0].Domain != "macro" {
		t.Fatalf("coverageGaps = %+v, want one macro gap", gaps)
	}
	// Both are groundable now — the regime is computed per market from a global
	// price source — so a macro miss on either is a real gap.
	if want := []string{"2330.TW", "AAPL"}; !reflect.DeepEqual(gaps[0].Missing, want) {
		t.Errorf("macro gap = %v, want %v", gaps[0].Missing, want)
	}
}

// Only agent failure used to degrade a run, so ~50 provider errors and 0/12
// sentiment coverage still reported `complete`.
func TestCoverageGapsDegradeTheRun(t *testing.T) {
	statuses := []model.DomainStatus{
		{Domain: "quant", Grounded: true},
		{Domain: "news", Grounded: true, Ungrounded: []string{"GE", "AIR.PA"}},
		{Domain: "fundamentals", Grounded: false, Ungrounded: []string{"NVDA", "GE", "AIR.PA"}},
		{Domain: "sentiment", Grounded: true, Ungrounded: []string{"AIR.PA", "000660.KS"}},
		{Domain: "macro", Grounded: false, Ungrounded: []string{"NVDA", "GE", "AIR.PA"}},
	}
	got := coverageGaps(statuses)
	want := []domainGap{
		{Domain: "fundamentals", Missing: []string{"GE", "NVDA"}},
		{Domain: "macro", Missing: []string{"AIR.PA", "GE", "NVDA"}},
		{Domain: "news", Missing: []string{"GE"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("coverageGaps = %v, want %v", got, want)
	}
	// sentiment and news missed only names with no US line — an option chain is
	// a US instrument, and a headline search that answers a foreign symbol from
	// a generic fallback set is not coverage of that company. Neither is a gap.
	for _, g := range got {
		if g.Domain == "sentiment" {
			t.Errorf("%s should not be a gap: %v", g.Domain, g.Missing)
		}
		if g.Domain == "news" && contains(g.Missing, "AIR.PA") {
			t.Errorf("news was charged with a foreign listing it has no feed for: %v", g.Missing)
		}
	}
	// Macro is computed per market from the benchmark's own bars, so a foreign
	// name it missed is a real gap rather than a structural excuse.
	for _, g := range got {
		if g.Domain == "macro" && !contains(g.Missing, "AIR.PA") {
			t.Errorf("macro missed AIR.PA and its source is global — that is a gap: %v", g.Missing)
		}
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// 6/12 coverage is complete when the other 6 are names the domain's sources
// structurally cannot reach. The run that exposed this reported `complete` at
// 4/12 — 4 of 6 groundable — because two US names were lost to AlphaVantage
// rate limiting.
func TestCoverageGapsMeasureTheAchievableSubset(t *testing.T) {
	allAchievable := []model.DomainStatus{
		{Domain: "sentiment", Ungrounded: []string{"AIR.PA", "005930.KS", "000660.KS"}},
		{Domain: "macro"},
	}
	if got := coverageGaps(allAchievable); len(got) != 0 {
		t.Errorf("coverageGaps = %v, want none: every groundable name was covered", got)
	}

	// Yahoo is global, so quant has no structural excuse for a foreign name.
	quantMissedForeign := []model.DomainStatus{{Domain: "quant", Ungrounded: []string{"AIR.PA"}}}
	got := coverageGaps(quantMissedForeign)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Missing, []string{"AIR.PA"}) {
		t.Errorf("coverageGaps = %v, want quant missing AIR.PA (Yahoo covers foreign listings)", got)
	}
}

// TestConfabulationIsReportedAndDegradesTheRun is the 2026-09-01 metadata.json:
// domains[macro].corrected_scores held six names, and the same file said
// "warnings": [] and "outcome": "complete". Half a domain's output was invented
// and the run's headline reported it as fine.
func TestConfabulationIsReportedAndDegradesTheRun(t *testing.T) {
	macro := model.DomainStatus{
		Domain:      "macro",
		ScoredNames: 12,
		CorrectedScores: []string{
			"8035.T", "ASML.AS", "BAYN.DE", "BMW.DE", "NESTE.HE", "STLAM.MI",
		},
	}
	got := confabulations([]model.DomainStatus{{Domain: "quant", ScoredNames: 12}, macro})
	if len(got) != 1 || got[0].Domain != "macro" {
		t.Fatalf("confabulations = %v, want macro alone", got)
	}
	c := got[0]
	if c.invented() != 6 {
		t.Errorf("invented = %d, want 6", c.invented())
	}
	if !c.severe() {
		t.Error("six of twelve scores invented did not degrade the run")
	}
	msgs := c.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "6 of its 12") || !strings.Contains(msgs[0], "BAYN.DE") {
		t.Errorf("messages = %v, want one line naming the count and the names", msgs)
	}

	// A domain that overreached on a couple of names out of many is reported but
	// does not degrade: the threshold is about a report being more assertion than
	// evidence, not about any correction at all.
	mild := confabulations([]model.DomainStatus{
		{Domain: "news", ScoredNames: 12, CorrectedScores: []string{"GE"}},
	})
	if len(mild) != 1 {
		t.Fatalf("a single corrected score must still be reported, got %v", mild)
	}
	if mild[0].severe() {
		t.Error("one corrected score in twelve degraded the run")
	}

	// A clean domain produces nothing at all.
	if got := confabulations([]model.DomainStatus{{Domain: "quant", ScoredNames: 12}}); len(got) != 0 {
		t.Errorf("a clean domain produced %v", got)
	}
}

// An abstention is not a confabulation. Sentiment scoring a name whose computed
// verdict said to stand down is a disagreement about a verdict — the data was
// there — while macro scoring a name it had no data for is an invention. Both
// lose their scores; only one is evidence the run went wrong.
func TestAbstentionOverrideIsReportedButNeverDegrades(t *testing.T) {
	got := confabulations([]model.DomainStatus{{
		Domain:          "sentiment",
		ScoredNames:     4,
		Abstained:       []string{"AMGN", "MRK", "ORCL"},
		CorrectedScores: []string{"AMGN", "MRK", "ORCL"},
	}})
	if len(got) != 1 {
		t.Fatalf("an override went unreported: %v", got)
	}
	c := got[0]
	if c.invented() != 0 {
		t.Errorf("invented = %d, want 0 — the data was there", c.invented())
	}
	if len(c.Overridden) != 3 {
		t.Errorf("Overridden = %v, want all three", c.Overridden)
	}
	if c.severe() {
		t.Error("three of four abstention overrides degraded the run — a healthy sentiment run would never be complete")
	}
	if msgs := c.messages(); len(msgs) != 1 || !strings.Contains(msgs[0], "stand down") {
		t.Errorf("messages = %v, want the override worded as an override", msgs)
	}
}

// Each kind of removal gets its own line, because one sentence covering all of
// them would say the untrue thing about at least two.
func TestConfabulationSeparatesItsKinds(t *testing.T) {
	got := confabulations([]model.DomainStatus{{
		Domain:                 "news",
		ScoredNames:            6,
		Abstained:              []string{"MRK"},
		CorrectedScores:        []string{"GE", "MRK"},
		OffShortlistScores:     []string{"FAKE"},
		SelfContradictedScores: []string{"ORCL"},
	}})
	if len(got) != 1 {
		t.Fatalf("confabulations = %v", got)
	}
	c := got[0]
	if !reflect.DeepEqual(c.NoData, []string{"GE"}) {
		t.Errorf("NoData = %v, want [GE] — MRK was an abstention", c.NoData)
	}
	if c.invented() != 3 {
		t.Errorf("invented = %d, want 3 (GE, FAKE, ORCL)", c.invented())
	}
	if !c.severe() {
		t.Error("three of six invented is at the threshold and must degrade")
	}
	if got := len(c.messages()); got != 4 {
		t.Errorf("messages = %d lines, want one per kind of removal", got)
	}
}

func TestExpectedCoverageCountsTheDomainsThatCanReachAName(t *testing.T) {
	w := model.DefaultDomainWeights()

	// A US listing reaches everything.
	if got := expectedCoverage(w, "NVDA"); got != 1 {
		t.Errorf("expectedCoverage(NVDA) = %.2f, want 1", got)
	}
	// A foreign listing with a US line reaches everything too: 2330.TW trades
	// as TSM, so SEC filings and an option chain exist for it.
	if got := expectedCoverage(w, "2330.TW"); got != 1 {
		t.Errorf("expectedCoverage(2330.TW) = %.2f, want 1", got)
	}
	// One without a US line keeps quant (.35) and macro (.10), the two computed
	// from its own bars, and loses fundamentals, sentiment and news. News used
	// to be counted here on the strength of a keyless headline search that takes
	// the local symbol — but on 2026-09-03 that search answered all five such
	// names with the same eight untagged stories, the domain recorded every one
	// as missing, and 0.70 had already put them above the thin-coverage floor.
	if got := expectedCoverage(w, "AIR.PA"); math.Abs(got-0.45) > 1e-9 {
		t.Errorf("expectedCoverage(AIR.PA) = %.2f, want 0.45", got)
	}
}

// The run's own report of what it could not reach has to be in the same unit as
// the score. `quant_only` asked whether *any* provider could see a name, which
// became true of every listing once news and macro went global — the field would
// have been empty on every run while two of five domains were still standing
// down on some names.
func TestThinlyCoveredNames(t *testing.T) {
	shortlist := []model.Candidate{
		{Ticker: "NVDA"}, {Ticker: "AIR.PA"}, {Ticker: "GE"},
		{Ticker: "2330.TW"}, {Ticker: "hdfcbank.ns"}, {Ticker: "000660.KS"},
	}
	w := model.DefaultDomainWeights()

	// The floor has to bite on something, or MaxThinlyCovered is decoration.
	// While news was counted for every listing, the least-covered name in the
	// universe expected 0.70 and this returned empty on every run — which is
	// exactly what metadata.json recorded on 2026-09-03: "thinly_covered": null,
	// with three of five shipped ideas scored by one domain or two.
	got := thinlyCoveredNames(shortlist, w, thinCoverage)
	if want := []string{"000660.KS", "AIR.PA"}; !reflect.DeepEqual(got, want) {
		t.Errorf("thinlyCoveredNames = %v at the %.2f floor, want %v", got, thinCoverage, want)
	}
	// A listing with a US line is not thin: 2330.TW trades as TSM.
	for _, name := range got {
		if name == "2330.TW" || name == "NVDA" || name == "GE" {
			t.Errorf("%s reaches every domain and was called thinly covered", name)
		}
	}
}

// sentimentPack builds a sentiment pack whose tickers carry the computed
// positioning verdict, the way marketdata.BuildPack assembles one.
func sentimentPack(verdicts map[string]bool) *marketdata.DataPack {
	p := marketdata.NewDataPack("sentiment")
	for t, directional := range verdicts {
		verdict := "insider — no directional signal: routine disposal; options — no directional " +
			"signal: put/call open interest 1.22 is within the unremarkable band. " +
			"Both legs read no directional signal, so this name has no positioning evidence: " +
			"put it in `missing`, not in `scores`."
		if directional {
			verdict = "insider — bearish: an officer sold 83% of their own holding; options — " +
				"no directional signal. Directional evidence: insider bearish."
		}
		p.ByTicker[t] = marketdata.TickerData{Ticker: t, Facts: []marketdata.Fact{
			{Label: marketdata.PositioningSignalLabel, Value: verdict},
		}}
		p.Coverage[t] = true
	}
	return p
}

// TestSentimentIsGroundedByItsVerdictNotByItsFetch is the fix for a domain that
// always had an opinion.
//
// Nearly every US issuer has recent Form 4 filings and a listed option chain, so
// the fetch almost always succeeds — and treating that as evidence gave the
// sentiment domain one bullish score in 34 across four runs, taxing every long
// about nine points on scheduled insider selling. Presence of data is not a
// signal; the computed verdict is.
func TestSentimentIsGroundedByItsVerdictNotByItsFetch(t *testing.T) {
	pack := sentimentPack(map[string]bool{"AMGN": false, "IBM": true})

	if coveredBy("sentiment", pack, nil, "AMGN") {
		t.Error("a name whose positioning is quiet must not count as sentiment evidence")
	}
	if !coveredBy("sentiment", pack, nil, "IBM") {
		t.Error("a name with a directional verdict must count")
	}

	// The same rows under any other domain are ordinary coverage: the verdict is
	// sentiment's alone.
	newsPack := marketdata.NewDataPack("news")
	newsPack.Coverage["AMGN"] = true
	newsPack.ByTicker["AMGN"] = marketdata.TickerData{Ticker: "AMGN", Facts: []marketdata.Fact{
		{Label: "Headline 1", Value: "something happened"},
	}}
	if !coveredBy("news", newsPack, nil, "AMGN") {
		t.Error("the positioning rule leaked into another domain")
	}
}

// The news domain draws on two independent AlphaVantage calls, and the free
// key's 25-a-day budget runs out. On 2026-09-01 2330.TW was recorded grounded
// for news carrying one fact — an earnings date — with every headline lost to
// rate limiting, so it never appeared in coverageGaps and the run's own verdict
// could not see the starvation.
func TestNewsIsNotGroundedByAnEarningsDateAlone(t *testing.T) {
	pack := marketdata.NewDataPack("news")
	for _, tk := range []string{"AAPL", "2330.TW"} {
		pack.Coverage[tk] = true
	}
	pack.ByTicker["AAPL"] = marketdata.TickerData{Ticker: "AAPL", Facts: []marketdata.Fact{
		{Label: marketdata.EarningsFactLabel, Value: "2026-09-08"},
		{Label: "Headline 1", Value: "the flow itself"},
	}}
	// Everything the calendar and the ADR note can supply, and no headline.
	pack.ByTicker["2330.TW"] = marketdata.TickerData{Ticker: "2330.TW", Facts: []marketdata.Fact{
		{Label: marketdata.EarningsFactLabel, Value: "2026-10-15"},
		{Label: marketdata.USLineFactLabel, Value: "TSM — news below is coverage of TSM"},
	}}

	if !coveredBy("news", pack, nil, "AAPL") {
		t.Error("a name with headlines is covered for news")
	}
	if coveredBy("news", pack, nil, "2330.TW") {
		t.Error("an earnings date is a fact about the calendar, not a read on the flow")
	}
	if want := []string{"2330.TW"}; !reflect.DeepEqual(
		ungroundedFor("news", pack, nil, []string{"AAPL", "2330.TW"}), want) {
		t.Errorf("the starved name must show as a gap the run can see")
	}
	// The date itself is not discarded — it still gates the trade.
	if !marketdata.HasDomainEvidence("fundamentals", pack.ByTicker["2330.TW"]) {
		t.Error("the exclusion is news-specific; other domains read their own facts")
	}
}

// TestAbstentionIsNotACoverageGap keeps a healthy run from being marked degraded.
//
// An abstention and a gap both send the name to `missing`, but they are opposite
// events: one is the system declining to read noise as a signal, the other is a
// fetch that should have worked and did not. Counting the first as the second
// would degrade every run in which insider activity was unremarkable — which is
// most of them.
func TestAbstentionIsNotACoverageGap(t *testing.T) {
	pack := sentimentPack(map[string]bool{"AMGN": false, "IBM": true})
	tickers := []string{"AMGN", "IBM", "MRK"} // MRK was never fetched at all

	ungrounded := ungroundedFor("sentiment", pack, nil, tickers)
	if !reflect.DeepEqual(ungrounded, []string{"AMGN", "MRK"}) {
		t.Errorf("ungrounded = %v, want the abstention and the true gap — both belong in `missing`", ungrounded)
	}
	abstained := abstainedFor("sentiment", pack, tickers)
	if !reflect.DeepEqual(abstained, []string{"AMGN"}) {
		t.Errorf("abstained = %v, want only AMGN — MRK has no data at all, which is a gap", abstained)
	}
	if got := abstainedFor("news", pack, tickers); got != nil {
		t.Errorf("only sentiment abstains on a computed verdict, got %v", got)
	}

	gaps := coverageGaps([]model.DomainStatus{
		{Domain: "sentiment", Ungrounded: ungrounded, Abstained: abstained},
	})
	if len(gaps) != 1 || !reflect.DeepEqual(gaps[0].Missing, []string{"MRK"}) {
		t.Errorf("coverage gaps = %+v, want only the name that was never fetched", gaps)
	}
}
