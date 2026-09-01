package orchestrator

import (
	"reflect"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

func packWith(domain string, covered map[string]bool, macro []marketdata.Fact) *marketdata.DataPack {
	p := marketdata.NewDataPack(domain)
	for t, ok := range covered {
		p.Coverage[t] = ok
		if ok {
			p.ByTicker[t] = marketdata.TickerData{Ticker: t}
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
	if !groundedFor("macro", pack, nil) {
		t.Error("macro is grounded by the regime facts, not per-ticker rows")
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

	// Macro grounds every name or none; it is never partially covered.
	macroPack := packWith("macro", map[string]bool{"AAPL": false}, []marketdata.Fact{{Label: "CPI"}})
	if got := ungroundedFor("macro", macroPack, nil, []string{"AAPL", "MSFT"}); len(got) != 0 {
		t.Errorf("macro facts cover the whole shortlist, got gaps %v", got)
	}
}

// Macro's evidence is four US FRED series. Treating it as regime data that
// grounds every name let a Taiwanese semiconductor score "strongest macro read"
// off the US 10-year and CPI, at full 15% weight, with no gap ever recorded.
func TestMacroCoverageIsRegional(t *testing.T) {
	pack := packWith("macro", map[string]bool{"AAPL": false, "2330.TW": false},
		[]marketdata.Fact{{Label: "CPI"}})

	if !coveredBy("macro", pack, nil, "AAPL") {
		t.Error("a US listing is covered by the US macro backdrop")
	}
	if coveredBy("macro", pack, nil, "2330.TW") {
		t.Error("a Taiwanese listing is not covered by four US FRED series")
	}

	// No FRED facts at all: nothing is covered, not even the US names.
	dry := packWith("macro", map[string]bool{"AAPL": false}, nil)
	if coveredBy("macro", dry, nil, "AAPL") {
		t.Error("macro with no facts covers nothing")
	}
	if groundedFor("macro", dry, nil) {
		t.Error("macro with no facts is not grounded")
	}

	got := ungroundedFor("macro", pack, nil, []string{"AAPL", "2330.TW"})
	if want := []string{"2330.TW"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ungroundedFor(macro) = %v, want %v", got, want)
	}
	if groundableBy("macro", "2330.TW") {
		t.Error("no configured source carries a Taiwan macro backdrop")
	}
	if !groundableBy("macro", "AAPL") {
		t.Error("FRED can ground a US listing")
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
	// 2330.TW was never groundable, so only the US name it actually missed counts.
	if want := []string{"AAPL"}; !reflect.DeepEqual(gaps[0].Missing, want) {
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
		{Domain: "macro", Grounded: false, Ungrounded: []string{"NVDA", "GE"}},
	}
	got := coverageGaps(statuses)
	want := []domainGap{
		{Domain: "fundamentals", Missing: []string{"GE", "NVDA"}},
		{Domain: "macro", Missing: []string{"GE", "NVDA"}},
		{Domain: "news", Missing: []string{"GE"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("coverageGaps = %v, want %v", got, want)
	}
	// sentiment missed only names with no US line — it got everything it could get.
	for _, g := range got {
		if g.Domain == "sentiment" {
			t.Errorf("%s should not be a gap: %v", g.Domain, g.Missing)
		}
	}
}

// 6/12 coverage is complete when the other 6 are non-US listings no provider
// here can reach. The run that exposed this reported `complete` at 4/12 — 4 of
// 6 groundable — because two US names were lost to AlphaVantage rate limiting.
func TestCoverageGapsMeasureTheAchievableSubset(t *testing.T) {
	allAchievable := []model.DomainStatus{
		{Domain: "news", Ungrounded: []string{"AIR.PA", "005930.KS", "000660.KS"}},
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

func TestQuantOnlyNames(t *testing.T) {
	shortlist := []model.Candidate{
		{Ticker: "NVDA"}, {Ticker: "AIR.PA"}, {Ticker: "GE"},
		{Ticker: "2330.TW"}, {Ticker: "hdfcbank.ns"}, {Ticker: "000660.KS"},
	}
	// 2330.TW and HDFCBANK.NS trade as TSM and HDB, so the US-only providers
	// reach them; AIR.PA and 000660.KS have no US line and are quant-only.
	got := quantOnlyNames(shortlist)
	if want := []string{"000660.KS", "AIR.PA"}; !reflect.DeepEqual(got, want) {
		t.Errorf("quantOnlyNames = %v, want %v", got, want)
	}

	if got := quantOnlyNames([]model.Candidate{{Ticker: "NVDA"}}); len(got) != 0 {
		t.Errorf("quantOnlyNames = %v on an all-US shortlist, want none", got)
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
	newsPack.ByTicker["AMGN"] = marketdata.TickerData{Ticker: "AMGN"}
	if !coveredBy("news", newsPack, nil, "AMGN") {
		t.Error("the positioning rule leaked into another domain")
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
