package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

func fundamentalsPack(facts ...marketdata.Fact) *marketdata.DataPack {
	p := marketdata.NewDataPack("fundamentals")
	p.ByTicker["AAPL"] = marketdata.TickerData{Ticker: "AAPL", Facts: facts}
	return p
}

func factValue(t *testing.T, p *marketdata.DataPack, label string) string {
	t.Helper()
	for _, f := range p.ByTicker["AAPL"].Facts {
		if f.Label == label {
			return f.Value
		}
	}
	t.Fatalf("no fact labelled %q; have %+v", label, p.ByTicker["AAPL"].Facts)
	return ""
}

func day(s string) time.Time {
	d, _ := time.Parse("2006-01-02", s)
	return d
}

func TestEnrichFundamentalsComputesTheMultiples(t *testing.T) {
	// The specialist was asked whether a name was expensive while being shown
	// filed dollar amounts and no price at all, so "rich multiple" was an
	// assertion about a number nobody had computed. Dividing is arithmetic.
	pack := fundamentalsPack(
		marketdata.Fact{Label: marketdata.FactShares, Value: "15000000000", AsOf: day("2025-12-31")},
		marketdata.Fact{Label: marketdata.FactEPSDiluted, Value: "6.50", AsOf: day("2025-12-31")},
		marketdata.Fact{Label: marketdata.FactRevenue, Value: "400000000000", AsOf: day("2025-12-31")},
	)
	qp := &quant.Pack{ByTicker: map[string]quant.Metrics{
		"AAPL": {Symbol: "AAPL", LastClose: 260, AsOf: "2026-08-28"},
	}}
	enrichFundamentals(pack, qp)

	if got := factValue(t, pack, "Market cap (computed)"); !strings.Contains(got, "3.90T") {
		t.Errorf("market cap = %q, want 15e9 × 260 = $3.90T", got)
	}
	if got := factValue(t, pack, "P/E (computed, trailing diluted)"); !strings.HasPrefix(got, "40.0") {
		t.Errorf("P/E = %q, want 260 ÷ 6.50 = 40.0", got)
	}
	if got := factValue(t, pack, "P/S (computed)"); !strings.HasPrefix(got, "9.8") {
		t.Errorf("P/S = %q, want 3.9T ÷ 400B = 9.8", got)
	}
	// Every computed line must show its own inputs, or it is one more number to
	// take on trust.
	if got := factValue(t, pack, "P/E (computed, trailing diluted)"); !strings.Contains(got, "2025-12-31") {
		t.Errorf("P/E line does not date its input: %q", got)
	}
}

func TestEnrichFundamentalsSaysWhenAMultipleIsNotComputable(t *testing.T) {
	// An absent multiple gets filled in from recollection. A line stating that
	// it is not computable, and why, does not.
	pack := fundamentalsPack(
		marketdata.Fact{Label: marketdata.FactShares, Value: "15000000000", AsOf: day("2019-12-31")},
		marketdata.Fact{Label: marketdata.FactEPSDiluted, Value: "6.50", AsOf: day("2019-12-31")},
	)
	qp := &quant.Pack{ByTicker: map[string]quant.Metrics{
		"AAPL": {Symbol: "AAPL", LastClose: 260, AsOf: "2026-08-28"},
	}}
	enrichFundamentals(pack, qp)

	for _, label := range []string{"Market cap", "P/E"} {
		got := factValue(t, pack, label)
		if !strings.Contains(got, "not computable") {
			t.Errorf("%s = %q, want an explicit not-computable line", label, got)
		}
		if !strings.Contains(got, "years") {
			t.Errorf("%s should say how stale the input is: %q", label, got)
		}
	}
	for _, f := range pack.ByTicker["AAPL"].Facts {
		if strings.HasPrefix(f.Label, "P/S") {
			t.Errorf("P/S must not be computed off an uncomputable market cap: %+v", f)
		}
	}
}

func TestEnrichFundamentalsHandlesALoss(t *testing.T) {
	pack := fundamentalsPack(
		marketdata.Fact{Label: marketdata.FactEPSDiluted, Value: "-1.20", AsOf: day("2025-12-31")},
	)
	enrichFundamentals(pack, &quant.Pack{ByTicker: map[string]quant.Metrics{
		"AAPL": {Symbol: "AAPL", LastClose: 260, AsOf: "2026-08-28"},
	}})
	if got := factValue(t, pack, "P/E"); !strings.Contains(got, "not meaningful") {
		t.Errorf("P/E on negative earnings = %q, want it called not meaningful", got)
	}
}

func TestEnrichFundamentalsSkipsNamesWithNoPrice(t *testing.T) {
	pack := fundamentalsPack(
		marketdata.Fact{Label: marketdata.FactEPSDiluted, Value: "6.50", AsOf: day("2025-12-31")},
	)
	before := len(pack.ByTicker["AAPL"].Facts)
	enrichFundamentals(pack, quant.NewPack())
	if got := len(pack.ByTicker["AAPL"].Facts); got != before {
		t.Errorf("added %d fact(s) for a ticker with no verified price", got-before)
	}
}
