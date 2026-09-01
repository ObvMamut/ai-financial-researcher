package marketdata

import (
	"strings"
	"testing"
)

func sale(owner, title string, shares, price, after float64) form4Transaction {
	return form4Transaction{Owner: owner, Title: title, Shares: shares, Price: price, SharesAfter: after}
}

func buy(owner string, shares, price float64) form4Transaction {
	return form4Transaction{Owner: owner, Shares: shares, Price: price, Buy: true}
}

// TestClassifyInsiderActivityOnTheRunThatExposedIt is the 2026-09-01T14-47-26
// shortlist, with the Form 4 activity that run actually collected.
//
// The sentiment domain scored six of those eight names bearish and none bullish,
// almost entirely on scheduled disposals. These are the verdicts the arithmetic
// reaches on the same filings.
func TestClassifyInsiderActivityOnTheRunThatExposedIt(t *testing.T) {
	cases := []struct {
		name string
		txns []form4Transaction
		want InsiderBias
		says string
	}{{
		// AMGN: one SVP, two sales, $1.35M. The domain called this bearish 6 and
		// cost every AMGN long nine points for it.
		name: "AMGN — one officer trimming",
		txns: []form4Transaction{
			sale("Khosla Rachna", "SVP, Business Development", 2000, 412.57, 40000),
			sale("Khosla Rachna", "SVP, Business Development", 1252, 416.43, 38748),
		},
		want: InsiderNone,
		says: "routine disposal",
	}, {
		// MRK: 13 sales, $21.21M — a big number, but two sellers trimming a
		// fraction each. Size is not breadth and it is not depth.
		name: "MRK — large dollars, narrow and shallow",
		txns: []form4Transaction{
			sale("Zachary Jennifer", "EVP, General Counsel", 80315, 133.46, 400000),
			sale("DeLuca Richard R.", "EVP", 20784, 130.43, 300000),
			sale("DeLuca Richard R.", "EVP", 11099, 131.61, 288901),
		},
		want: InsiderNone,
		says: "routine disposal",
	}, {
		// IBM: one SVP, 25,000 shares, and nearly all of what they held.
		name: "IBM — an officer leaving the position",
		txns: []form4Transaction{
			sale("Thomas Robert David", "Senior Vice President", 25000, 230.32, 5000),
		},
		want: InsiderBearish,
		says: "of their own holding",
	}, {
		name: "ORCL — filings but no open-market lines",
		txns: nil,
		want: InsiderNone,
		says: "no open-market insider transactions",
	}, {
		name: "three sellers is breadth",
		txns: []form4Transaction{
			sale("A", "CFO", 1000, 100, 90000),
			sale("B", "COO", 1000, 100, 90000),
			sale("C", "Director", 1000, 100, 90000),
		},
		want: InsiderBearish,
		says: "distinct insiders sold",
	}, {
		name: "two open-market buyers is a cluster",
		txns: []form4Transaction{buy("A", 100, 50), buy("B", 100, 50)},
		want: InsiderBullish,
		says: "cluster",
	}, {
		// Buying is not cancelled by other insiders' scheduled selling.
		name: "one large buy outweighs concurrent routine sales",
		txns: []form4Transaction{
			buy("A", 10000, 100),
			sale("B", "EVP", 500, 100, 50000),
		},
		want: InsiderBullish,
		says: "single open-market purchase",
	}, {
		name: "a small lone buy is not a signal",
		txns: []form4Transaction{buy("A", 100, 50)},
		want: InsiderNone,
		says: "below the size at which one buy carries",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyInsiderActivity(tc.txns)
			if got.Bias != tc.want {
				t.Errorf("bias = %q, want %q (%s)", got.Bias, tc.want, got.Reason)
			}
			if !strings.Contains(got.Reason, tc.says) {
				t.Errorf("reason %q does not explain itself with %q", got.Reason, tc.says)
			}
		})
	}
}

// TestInsiderSellingIsNotSymmetricWithBuying states the asymmetry outright,
// because it is the whole point and a future tidy-up would otherwise "fix" it.
func TestInsiderSellingIsNotSymmetricWithBuying(t *testing.T) {
	twoBuyers := []form4Transaction{buy("A", 100, 50), buy("B", 100, 50)}
	twoSellers := []form4Transaction{
		sale("A", "EVP", 100, 50, 10000),
		sale("B", "EVP", 100, 50, 10000),
	}
	if got := classifyInsiderActivity(twoBuyers).Bias; got != InsiderBullish {
		t.Errorf("two open-market buyers = %q, want bullish", got)
	}
	if got := classifyInsiderActivity(twoSellers).Bias; got != InsiderNone {
		t.Errorf("two routine sellers = %q, want no signal — officers are paid in stock and sell on a schedule", got)
	}
}

func TestClassifyOptionsPositioningIsContrarian(t *testing.T) {
	// The run's own ratios. Every one of these was read as bearish by the agent:
	// crowded puts "against further upside", crowded calls "a squeeze risk".
	cases := []struct {
		ratio float64
		want  InsiderBias
	}{
		{1.22, InsiderNone},    // AMGN — inside the unremarkable band
		{0.50, InsiderBearish}, // MRK — call-crowded, a risk to the calls
		{0.67, InsiderBearish}, // IBM — at the boundary
		{0.42, InsiderBearish}, // STLAM.MI
		{1.60, InsiderBullish}, // put-crowded, a risk to the puts
		{0, InsiderNone},       // no chain
	}
	for _, tc := range cases {
		if got := classifyOptionsPositioning(tc.ratio); got.Bias != tc.want {
			t.Errorf("put/call %.2f = %q, want %q (%s)", tc.ratio, got.Bias, tc.want, got.Reason)
		}
	}
}

// TestPositioningSignalTellsTheAgentToAbstain checks the instruction reaches the
// prompt, since that is what makes the rule enforceable rather than advisory.
func TestPositioningSignalTellsTheAgentToAbstain(t *testing.T) {
	quiet := TickerData{Ticker: "AMGN", Facts: []Fact{
		signalFact(InsiderSignalLabel, classifyInsiderActivity(nil), "computed", ""),
		signalFact(OptionsSignalLabel, classifyOptionsPositioning(1.22), "computed", ""),
	}}
	addPositioningSignal(&quiet)
	last := quiet.Facts[len(quiet.Facts)-1]
	if last.Label != PositioningSignalLabel {
		t.Fatalf("no combined verdict appended, got %q", last.Label)
	}
	if !strings.Contains(last.Value, "put it in `missing`, not in `scores`") {
		t.Errorf("verdict does not instruct abstention: %q", last.Value)
	}
	if HasPositioningSignal(quiet) {
		t.Error("a name with neither leg directional reports positioning evidence")
	}

	loud := TickerData{Ticker: "IBM", Facts: []Fact{
		signalFact(InsiderSignalLabel, classifyInsiderActivity([]form4Transaction{
			sale("Thomas Robert David", "SVP", 25000, 230.32, 5000)}), "computed", ""),
		signalFact(OptionsSignalLabel, classifyOptionsPositioning(0.67), "computed", ""),
	}}
	addPositioningSignal(&loud)
	if !HasPositioningSignal(loud) {
		t.Error("a name with two directional legs reports no positioning evidence")
	}
	if v := loud.Facts[len(loud.Facts)-1].Value; !strings.Contains(v, "insider bearish") {
		t.Errorf("verdict does not name its directional legs: %q", v)
	}

	// No verdict of any kind — a provider that returned nothing at all. The
	// domain has not earned a score for this name.
	if HasPositioningSignal(TickerData{Ticker: "X", Facts: []Fact{{Label: "Implied volatility (ATM, front expiry)"}}}) {
		t.Error("a ticker with no positioning verdict at all was credited with one")
	}
}

// TestPositioningSignalFromStaleCacheShape is the bug this guardrail actually
// had. The computed legs are written on the *fetch* path, and the whole
// TickerData — facts included — is what the disk cache stores. The key hashed
// source, provider, domain, ticker and the date, nothing about the payload, so an
// entry written earlier the same day by a binary without the computed legs came
// back without them and HasPositioningSignal failed open.
//
// On 2026-09-01 that inverted the guardrail exactly: the domain scored the three
// tickers served from cache (ORCL bearish 5, TTD bearish 4, STLAM.MI bearish 5)
// and abstained on the two fetched fresh. ORCL's own facts say zero open-market
// trades and a put/call of 0.79 — inside the unremarkable band — so both legs are
// InsiderNone and the score should never have survived.
//
// Every existing test builds its facts through signalFact, so none of them can
// see this shape.
func TestPositioningSignalFromStaleCacheShape(t *testing.T) {
	orcl := TickerData{Ticker: "ORCL", Facts: []Fact{{
		Label:  InsiderActivityLabel,
		Value:  "0 open-market buys ($0) vs 0 sales ($0) across 1 filing(s) in 45 days",
		Source: "SEC EDGAR",
	}, {
		Label:  OptionsPositioningLabel,
		Value:  "put/call open interest 0.79 (168289 puts vs 212455 calls) over the front 2 expiries",
		Source: "Yahoo Finance options",
	}}}
	addPositioningSignal(&orcl)

	last := orcl.Facts[len(orcl.Facts)-1]
	if last.Label != PositioningSignalLabel {
		t.Fatalf("no verdict reconstructed from raw facts, last label = %q", last.Label)
	}
	if !strings.Contains(last.Value, "put it in `missing`, not in `scores`") {
		t.Errorf("verdict does not instruct abstention: %q", last.Value)
	}
	if HasPositioningSignal(orcl) {
		t.Error("ORCL's zero trades and 0.79 put/call were read as positioning evidence")
	}

	// The reconstruction is the classifier's, not a weaker paraphrase: a
	// call-crowded chain still reads contrarian bearish off the raw sentence.
	crowded := TickerData{Ticker: "MRK", Facts: []Fact{{
		Label: OptionsPositioningLabel,
		Value: "put/call open interest 0.50 (5000 puts vs 10000 calls) over the front 2 expiries",
	}}}
	addPositioningSignal(&crowded)
	if !HasPositioningSignal(crowded) {
		t.Error("a call-crowded chain reconstructed from its raw fact lost its direction")
	}
	if v := crowded.Facts[len(crowded.Facts)-1].Value; !strings.Contains(v, "options bearish") {
		t.Errorf("verdict does not name the reconstructed leg: %q", v)
	}
}

// A Form 4 window with real trades in it cannot be re-classified from the
// summary sentence: breadth and depth never reached it. The honest outcome is
// "unsettled", which sends the name to `missing` rather than inventing a verdict
// that might contradict what the fetch path computed.
func TestPositioningSignalUnresolvedLegAbstains(t *testing.T) {
	td := TickerData{Ticker: "IBM", Facts: []Fact{{
		Label: InsiderActivityLabel,
		Value: "0 open-market buys ($0) vs 3 sales ($21.21M) across 5 filing(s) in 45 days",
	}, {
		Label: OptionsPositioningLabel,
		Value: "put/call open interest 1.22 (100 puts vs 82 calls) over the front 2 expiries",
	}}}
	addPositioningSignal(&td)

	v := td.Facts[len(td.Facts)-1].Value
	if !strings.Contains(v, unresolvedLegVerdict) {
		t.Errorf("verdict does not admit the unreadable leg: %q", v)
	}
	if strings.Contains(v, noPositioningSignal) {
		t.Errorf("verdict claims both legs were read when one was not: %q", v)
	}
	if HasPositioningSignal(td) {
		t.Error("an unsettled name was credited with positioning evidence")
	}
}

// The computed leg wins wherever it exists: a fresh fetch carries both the raw
// summary and the verdict, and the verdict is the authority.
func TestPositioningSignalPrefersTheComputedLeg(t *testing.T) {
	td := TickerData{Ticker: "IBM", Facts: []Fact{{
		Label: InsiderActivityLabel,
		Value: "0 open-market buys ($0) vs 1 sale ($5.76M) across 1 filing(s) in 45 days",
	}, signalFact(InsiderSignalLabel, classifyInsiderActivity([]form4Transaction{
		sale("Thomas Robert David", "SVP", 25000, 230.32, 5000)}), "computed", "")}}
	addPositioningSignal(&td)

	v := td.Facts[len(td.Facts)-1].Value
	if strings.Contains(v, unresolvedLegVerdict) {
		t.Errorf("a leg with a computed verdict was treated as unreadable: %q", v)
	}
	if !HasPositioningSignal(td) {
		t.Errorf("the computed bearish verdict was discarded: %q", v)
	}
}
