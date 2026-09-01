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

	// A ticker with no computed leg at all — an older cache entry — must not be
	// silenced on a technicality.
	if !HasPositioningSignal(TickerData{Ticker: "X", Facts: []Fact{{Label: "Options positioning"}}}) {
		t.Error("a ticker with no computed verdict was treated as having abstained")
	}
}
