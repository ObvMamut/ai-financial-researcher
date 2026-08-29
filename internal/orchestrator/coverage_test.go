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
		{Domain: "sentiment", Grounded: true, Ungrounded: []string{"AIR.PA", "2330.TW"}},
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
	// sentiment missed only non-US names — it got everything it could get.
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
		{Domain: "news", Ungrounded: []string{"AIR.PA", "2330.TW", "000660.KS"}},
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
		{Ticker: "2330.TW"}, {Ticker: "hdfcbank.ns"},
	}
	got := quantOnlyNames(shortlist)
	if want := []string{"2330.TW", "AIR.PA", "HDFCBANK.NS"}; !reflect.DeepEqual(got, want) {
		t.Errorf("quantOnlyNames = %v, want %v", got, want)
	}

	if got := quantOnlyNames([]model.Candidate{{Ticker: "NVDA"}}); len(got) != 0 {
		t.Errorf("quantOnlyNames = %v on an all-US shortlist, want none", got)
	}
}
