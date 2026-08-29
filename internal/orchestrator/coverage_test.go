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
func TestZeroCoverageDegradesTheRun(t *testing.T) {
	statuses := []model.DomainStatus{
		{Domain: "quant", Grounded: true},
		{Domain: "news", Grounded: true},
		{Domain: "fundamentals", Grounded: false},
		{Domain: "sentiment", Grounded: false},
		{Domain: "macro", Grounded: false},
	}
	got := zeroCoverage(statuses)
	if want := []string{"fundamentals", "sentiment"}; !reflect.DeepEqual(got, want) {
		t.Errorf("zeroCoverage = %v, want %v (macro is not per-ticker)", got, want)
	}

	allGood := []model.DomainStatus{{Domain: "news", Grounded: true}, {Domain: "macro", Grounded: true}}
	if got := zeroCoverage(allGood); len(got) != 0 {
		t.Errorf("zeroCoverage = %v on a fully grounded run, want none", got)
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
