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

func factValueFor(t *testing.T, p *marketdata.DataPack, ticker, label string) string {
	t.Helper()
	for _, f := range p.ByTicker[ticker].Facts {
		if f.Label == label {
			return f.Value
		}
	}
	t.Fatalf("no fact labelled %q on %s; have %+v", label, ticker, p.ByTicker[ticker].Facts)
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

func TestEnrichFundamentalsConvertsAForeignCloseToUSD(t *testing.T) {
	// BBVA.MC, 2026-09-03: "Market cap (computed) = $143.24B (5708968700 shares
	// × close 25.09)". The close is €25.09, so that figure is €143.24B — and the
	// rate to fix it, 1.16036, was already on the metric. Every figure EDGAR
	// files is USD, so an unconverted close also mixes units into P/E and P/S.
	pack := marketdata.NewDataPack("fundamentals")
	pack.ByTicker["BBVA.MC"] = marketdata.TickerData{Ticker: "BBVA.MC", Facts: []marketdata.Fact{
		{Label: marketdata.FactShares, Value: "5708968700", AsOf: day("2025-12-31")},
		{Label: marketdata.FactEPSDiluted, Value: "1.45", AsOf: day("2025-12-31")},
	}}
	qp := &quant.Pack{ByTicker: map[string]quant.Metrics{
		"BBVA.MC": {Symbol: "BBVA.MC", LastClose: 25.09, AsOf: "2026-09-01",
			Currency: "EUR", FXToUSD: 1.16036},
	}}
	enrichFundamentals(pack, qp)

	got := factValueFor(t, pack, "BBVA.MC", "Market cap (computed)")
	if !strings.Contains(got, "166.21B") {
		t.Errorf("market cap = %q, want 5.709e9 × €25.09 × 1.16036 = $166.21B", got)
	}
	if !strings.Contains(got, "EUR→USD at 1.1604") {
		t.Errorf("the line does not show the conversion it applied: %q", got)
	}
	// P/E divides a USD close into USD-filed EPS: 25.09 × 1.16036 ÷ 1.45 = 20.1.
	if pe := factValueFor(t, pack, "BBVA.MC", "P/E (computed, trailing diluted)"); !strings.HasPrefix(pe, "20.1") {
		t.Errorf("P/E = %q, want 20.1 on the converted close", pe)
	}
}

func TestEnrichFundamentalsRefusesTheMultiplesWithNoRate(t *testing.T) {
	pack := marketdata.NewDataPack("fundamentals")
	pack.ByTicker["O39.SI"] = marketdata.TickerData{Ticker: "O39.SI", Facts: []marketdata.Fact{
		{Label: marketdata.FactShares, Value: "4500000000", AsOf: day("2025-12-31")},
	}}
	qp := &quant.Pack{ByTicker: map[string]quant.Metrics{
		"O39.SI": {Symbol: "O39.SI", LastClose: 31.88, AsOf: "2026-09-02", Currency: "SGD"},
	}}
	enrichFundamentals(pack, qp)

	for _, f := range pack.ByTicker["O39.SI"].Facts {
		if strings.HasPrefix(f.Label, "Market cap (computed)") {
			t.Errorf("a market cap was printed with no rate to convert it: %s", f.Value)
		}
	}
	if got := factValueFor(t, pack, "O39.SI", "Valuation multiples"); !strings.Contains(got, "SGD") {
		t.Errorf("the refusal does not name the currency: %q", got)
	}
}
