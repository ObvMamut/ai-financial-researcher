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

// The sentiment agent asserted short interest and options skew for all 12 names
// off an empty pack and then declared `"missing": []`.
func TestOverclaimedCoverage(t *testing.T) {
	report := "…analysis…\n```json\n{\"missing\": [\"MSFT\"]}\n```"
	got := overclaimedCoverage(report, []string{"MSFT", "NVDA"})
	if want := []string{"NVDA"}; !reflect.DeepEqual(got, want) {
		t.Errorf("overclaimed = %v, want %v", got, want)
	}

	honest := "…\n```json\n{\"missing\": [\"msft\", \"NVDA\"]}\n```"
	if got := overclaimedCoverage(honest, []string{"MSFT", "NVDA"}); len(got) != 0 {
		t.Errorf("a complete, case-insensitive missing array is honest, got %v", got)
	}

	// Nothing was ungrounded, so nothing can be overclaimed.
	if got := overclaimedCoverage(report, nil); got != nil {
		t.Errorf("overclaimed = %v with no gaps, want nil", got)
	}
	// An unparseable tail is a parse problem, reported elsewhere.
	if got := overclaimedCoverage("no json here", []string{"NVDA"}); got != nil {
		t.Errorf("overclaimed = %v on an unparseable report, want nil", got)
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
		{Domain: "news", Missing: []string{"GE"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("coverageGaps = %v, want %v", got, want)
	}
	// sentiment missed only non-US names — it got everything it could get.
	// macro is not per-ticker at all, so its Ungrounded list means nothing.
	for _, g := range got {
		if g.Domain == "sentiment" || g.Domain == "macro" {
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
