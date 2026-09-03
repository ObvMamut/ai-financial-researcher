package marketdata

import (
	"fmt"
	"math"
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

// optionsFact is the raw sentence the chain provider writes. The verdict is
// derived from it at pack level, where the run's cross-section is known, so a
// test that wants a leg builds the raw fact rather than a stored verdict.
func optionsFact(ratio float64) Fact {
	return Fact{
		Label: OptionsPositioningLabel,
		Value: fmt.Sprintf("put/call open interest %.2f (1000 puts vs %.0f calls) over the front 2 expiries",
			ratio, 1000/ratio),
		Source: "Yahoo Finance options",
	}
}

func TestClassifyOptionsPositioningIsContrarian(t *testing.T) {
	// With no cross-section to judge against, only a genuine extreme counts.
	cases := []struct {
		ratio float64
		want  InsiderBias
	}{
		{1.22, InsiderNone},    // AMGN — unremarkable
		{0.50, InsiderNone},    // MRK — low, but this is where a long name sits
		{0.67, InsiderNone},    // IBM — the old boundary, now ordinary
		{0.42, InsiderBearish}, // genuinely call-crowded
		{2.10, InsiderBullish}, // genuinely put-crowded
		{1.60, InsiderNone},    // the old put boundary, now ordinary
		{0, InsiderNone},       // no chain
	}
	for _, tc := range cases {
		if got := classifyOptionsPositioning(tc.ratio, 0); got.Bias != tc.want {
			t.Errorf("put/call %.2f = %q, want %q (%s)", tc.ratio, got.Bias, tc.want, got.Reason)
		}
	}
}

// Across the 27 shipped ideas carrying per-domain scores, sentiment agreed with
// the quant read on 4 of 17 — 24%, against 95% for news and 90% for macro. The
// mechanism was a band that called 0.67 "crowded": a put/call under 0.67 is
// where a large-cap in an uptrend ordinarily sits, so the domain read contrarian
// bearish on nearly every long the pre-screen nominated. On 2026-09-01 it scored
// MRK bearish 7 on a 0.50 ratio against news bullish 7, and a name all five
// domains could see finished at a base of 31.
func TestAnOrdinaryLongsPutCallIsNotCrowding(t *testing.T) {
	for _, ratio := range []float64{0.50, 0.57, 0.67} {
		if got := classifyOptionsPositioning(ratio, 0); got.Bias != InsiderNone {
			t.Errorf("put/call %.2f = %q — that is where a name people are long sits, not crowding (%s)",
				ratio, got.Bias, got.Reason)
		}
	}
}

// An extreme the whole shortlist shares is the tape's level, not one name's
// positioning. Reading it as crowding would put the same contrarian tilt on
// every idea in the book at once — and a fixed band cannot tell the two apart.
func TestCrowdingIsJudgedAgainstTheRunsOwnCrossSection(t *testing.T) {
	// A call-crowded month: every name is at 0.40, so no name is an outlier.
	if got := classifyOptionsPositioning(0.40, 0.42); got.Bias != InsiderNone {
		t.Errorf("0.40 against a 0.42 median = %q, want no signal — that is the tape (%s)", got.Bias, got.Reason)
	}
	// The same ratio in an ordinary month is a real outlier.
	if got := classifyOptionsPositioning(0.40, 1.05); got.Bias != InsiderBearish {
		t.Errorf("0.40 against a 1.05 median = %q, want bearish (%s)", got.Bias, got.Reason)
	}
	// And the same in the other direction.
	if got := classifyOptionsPositioning(2.20, 2.00); got.Bias != InsiderNone {
		t.Errorf("2.20 against a 2.00 median = %q, want no signal (%s)", got.Bias, got.Reason)
	}
	if got := classifyOptionsPositioning(2.20, 1.05); got.Bias != InsiderBullish {
		t.Errorf("2.20 against a 1.05 median = %q, want bullish (%s)", got.Bias, got.Reason)
	}
}

func TestPutCallMedianNeedsEnoughChainsToMeanAnything(t *testing.T) {
	two := map[string]TickerData{
		"A": {Facts: []Fact{optionsFact(0.5)}},
		"B": {Facts: []Fact{optionsFact(1.5)}},
	}
	if got := PutCallMedian(two); got != 0 {
		t.Errorf("PutCallMedian over 2 chains = %.2f, want 0 — too few to be a cross-section", got)
	}
	four := map[string]TickerData{
		"A": {Facts: []Fact{optionsFact(0.40)}},
		"B": {Facts: []Fact{optionsFact(0.80)}},
		"C": {Facts: []Fact{optionsFact(1.20)}},
		"D": {Facts: []Fact{optionsFact(2.00)}},
		"E": {Facts: []Fact{}}, // no chain: excluded, not counted as zero
	}
	if got := PutCallMedian(four); math.Abs(got-1.00) > 1e-9 {
		t.Errorf("PutCallMedian = %.2f, want 1.00", got)
	}
}

// TestPositioningSignalTellsTheAgentToAbstain checks the instruction reaches the
// prompt, since that is what makes the rule enforceable rather than advisory.
func TestPositioningSignalTellsTheAgentToAbstain(t *testing.T) {
	quiet := TickerData{Ticker: "AMGN", Facts: []Fact{
		signalFact(InsiderSignalLabel, classifyInsiderActivity(nil), "computed", ""),
		optionsFact(1.22),
	}}
	addPositioningSignal(&quiet, 0)
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
		optionsFact(0.30),
	}}
	addPositioningSignal(&loud, 0)
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
	addPositioningSignal(&orcl, 0)

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
		Value: "put/call open interest 0.30 (3000 puts vs 10000 calls) over the front 2 expiries",
	}}}
	addPositioningSignal(&crowded, 0)
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
	addPositioningSignal(&td, 0)

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

// The insider leg's computed verdict wins wherever it exists: a fresh fetch
// carries both the raw summary and the verdict, and the verdict is the
// authority. (The options leg is the exception — it is always recomputed,
// because its answer depends on the run's own cross-section.)
func TestPositioningSignalPrefersTheComputedLeg(t *testing.T) {
	td := TickerData{Ticker: "IBM", Facts: []Fact{{
		Label: InsiderActivityLabel,
		Value: "0 open-market buys ($0) vs 1 sale ($5.76M) across 1 filing(s) in 45 days",
	}, signalFact(InsiderSignalLabel, classifyInsiderActivity([]form4Transaction{
		sale("Thomas Robert David", "SVP", 25000, 230.32, 5000)}), "computed", "")}}
	addPositioningSignal(&td, 0)

	v := td.Facts[len(td.Facts)-1].Value
	if strings.Contains(v, unresolvedLegVerdict) {
		t.Errorf("a leg with a computed verdict was treated as unreadable: %q", v)
	}
	if !HasPositioningSignal(td) {
		t.Errorf("the computed bearish verdict was discarded: %q", v)
	}
}

func TestASmallSaleOfAWholeDirectHoldingDoesNotCarryTheDomain(t *testing.T) {
	// QCOM, 2026-09-03: the sentiment domain's entire bearish basis was
	// "Grech Patricia Y (SVP, Chief Accounting Officer) sold 100% of their own
	// holding ($35k)". A Form 4's "shares held following" counts directly held
	// shares only, so an officer whose equity sits in RSUs or a trust reports a
	// token direct position and trips the depth test on a rounding error.
	tiny := sale("Grech Patricia Y", "SVP, Chief Accounting Officer", 200, 175, 0)
	got := classifyInsiderActivity([]form4Transaction{tiny})
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want no signal on a $35k sale: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "directly held shares") {
		t.Errorf("the reason does not explain the artefact: %s", got.Reason)
	}

	// The depth test still fires when the position sold is a real one.
	real := sale("Grech Patricia Y", "SVP, Chief Accounting Officer", 20_000, 175, 0)
	if got := classifyInsiderActivity([]form4Transaction{real}); got.Bias != InsiderBearish {
		t.Errorf("bias = %s, want bearish on a $3.5M sale of a whole holding: %s", got.Bias, got.Reason)
	}
}
