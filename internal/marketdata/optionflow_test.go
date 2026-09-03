package marketdata

import (
	"strings"
	"testing"
)

// flowWith builds a chain out of explicit legs so each test states the exact
// shape it is about, rather than a fixture the reader has to decode.
func flowWith(spot float64, calls, puts []yahooOptionLeg) optionFlow {
	f := optionFlow{Spot: spot}
	for _, c := range calls {
		f.addCall(c)
	}
	for _, p := range puts {
		f.addPut(p)
	}
	return f
}

func TestFlowAbstainsOnAQuietChain(t *testing.T) {
	// Two retail orders in a name that barely trades options produce a perfect
	// 10:1 "skew". The volume floor is what stops that being a score.
	f := flowWith(100, []yahooOptionLeg{{Strike: 110, Volume: 300, OpenInterest: 10}}, nil)
	got := classifyUnusualOptions(f)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none — 300 contracts is not a signal: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "under the 2,000") {
		t.Errorf("the reason does not say what was missing: %s", got.Reason)
	}
}

func TestFlowAbstainsOnATwoWayDay(t *testing.T) {
	f := flowWith(100,
		[]yahooOptionLeg{{Strike: 110, Volume: 6000, OpenInterest: 500}},
		[]yahooOptionLeg{{Strike: 90, Volume: 6000, OpenInterest: 500}})
	got := classifyUnusualOptions(f)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none on a balanced day: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "two-way day") {
		t.Errorf("the reason does not name the balance: %s", got.Reason)
	}
}

func TestFlowSkewIsMeasuredInDollarsNotContracts(t *testing.T) {
	// Equal contract counts, wildly unequal money: 8,000 calls at a $10 strike
	// is $8M of notional against 8,000 puts at a $400 strike for $320M. Counting
	// contracts calls this balanced; it is a heavily put-side day.
	f := flowWith(400,
		[]yahooOptionLeg{{Strike: 10, Volume: 8000, OpenInterest: 100}},
		[]yahooOptionLeg{{Strike: 360, Volume: 8000, OpenInterest: 100}})
	got := classifyUnusualOptions(f)
	if got.Bias != InsiderBearish {
		t.Fatalf("bias = %s, want bearish — the money is all on the put side: %s", got.Bias, got.Reason)
	}
}

func TestFlowAbstainsWhenTheHeaviestStrikeIsOnlyChurn(t *testing.T) {
	// One-sided in dollars, but every contract traded sits inside an open
	// interest ten times its size: the book that existed this morning traded
	// among itself. Nothing was built.
	f := flowWith(100,
		[]yahooOptionLeg{{Strike: 110, Volume: 9000, OpenInterest: 90000}},
		[]yahooOptionLeg{{Strike: 90, Volume: 500, OpenInterest: 5000}})
	got := classifyUnusualOptions(f)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none on pure churn: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "book that already existed") {
		t.Errorf("the reason does not name the churn: %s", got.Reason)
	}
}

func TestFlowAbstainsAtTheMoney(t *testing.T) {
	// A one-sided, spiking call day struck exactly at spot. A covered-call
	// programme and a bullish bet are the same picture from here, and the leg
	// says so rather than picking one.
	f := flowWith(100,
		[]yahooOptionLeg{{Strike: 100, Volume: 9000, OpenInterest: 800}},
		[]yahooOptionLeg{{Strike: 100, Volume: 500, OpenInterest: 800}})
	got := classifyUnusualOptions(f)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none at the money: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "too close to tell") {
		t.Errorf("the reason does not name the ambiguity: %s", got.Reason)
	}
}

func TestFlowReadsUpsideCallBuildingAsBullish(t *testing.T) {
	f := flowWith(100,
		[]yahooOptionLeg{
			{Strike: 105, Volume: 4000, OpenInterest: 600},
			{Strike: 115, Volume: 9000, OpenInterest: 900},
		},
		[]yahooOptionLeg{{Strike: 95, Volume: 800, OpenInterest: 4000}})
	got := classifyUnusualOptions(f)
	if got.Bias != InsiderBullish {
		t.Fatalf("bias = %s, want bullish: %s", got.Bias, got.Reason)
	}
	// The sentence has to carry the evidence, not just the verdict: it is what
	// the agent reads and what a person checks it against.
	for _, want := range []string{"above spot", "115.00 strike", "10.0x"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("reason is missing %q: %s", want, got.Reason)
		}
	}
}

func TestFlowReadsDownsidePutBuildingAsBearish(t *testing.T) {
	f := flowWith(100,
		[]yahooOptionLeg{{Strike: 105, Volume: 700, OpenInterest: 3000}},
		[]yahooOptionLeg{
			{Strike: 95, Volume: 5000, OpenInterest: 700},
			{Strike: 85, Volume: 8000, OpenInterest: 900},
		})
	got := classifyUnusualOptions(f)
	if got.Bias != InsiderBearish {
		t.Fatalf("bias = %s, want bearish: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "below spot") {
		t.Errorf("reason does not place the strikes: %s", got.Reason)
	}
}

func TestFlowAbstainsWhenOpenInterestIsUnreported(t *testing.T) {
	// This used to read a zero-open-interest strike as position-building by
	// definition and answer the spike threshold itself, which made spike() true
	// for every strike whose open interest was simply missing. On 2026-09-03
	// that was every strike of every name in the run, and three of them shipped
	// a directional flow verdict citing "3.0x its open interest".
	p := strikePrint{Strike: 120, Volume: 5000, OpenInterest: 0}
	if p.spike() {
		t.Error("a strike with no reported open interest reads as a spike")
	}
	if _, ok := p.volOI(); ok {
		t.Error("volOI claims a ratio it has no denominator for")
	}
	if got := p.volOIText(); !strings.Contains(got, "not reported") {
		t.Errorf("volOIText = %q, want it to say the open interest is absent", got)
	}

	// And the whole classifier abstains rather than reading a direction: a
	// one-sided, well-placed day is not enough on its own.
	f := flowWith(100,
		[]yahooOptionLeg{{Strike: 110, Volume: 9000, OpenInterest: 0}},
		[]yahooOptionLeg{{Strike: 95, Volume: 200, OpenInterest: 0}})
	got := classifyUnusualOptions(f)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want no signal on a chain with no open interest: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "no open interest to measure against") {
		t.Errorf("reason does not name the missing denominator: %s", got.Reason)
	}
}

func TestFlowSummaryCarriesBothSidesAndTheHeaviestPrints(t *testing.T) {
	f := flowWith(100,
		[]yahooOptionLeg{{Strike: 115, Volume: 9000, OpenInterest: 900}},
		[]yahooOptionLeg{{Strike: 95, Volume: 800, OpenInterest: 4000}})
	summary, ok := f.summary(2)
	if !ok {
		t.Fatal("a chain with volume produced no summary")
	}
	for _, want := range []string{"call volume 9,000", "put volume 800", "heaviest call print", "spot 100.00"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary is missing %q: %s", want, summary)
		}
	}
	if _, ok := (optionFlow{}).summary(2); ok {
		t.Error("an empty chain produced a summary")
	}
}

func TestFlowLegJoinsThePositioningVerdict(t *testing.T) {
	// The leg is only real if it reaches the combined sentence the sentiment
	// agent is bound by.
	td := TickerData{Ticker: "NVDA", Facts: []Fact{
		{Label: UnusualOptionsLabel, Value: "call volume 9,000 contracts ($10.4M notional) vs put volume 800 ($0.8M)"},
		signalFact(OptionsFlowSignalLabel, classifyUnusualOptions(flowWith(100,
			[]yahooOptionLeg{{Strike: 115, Volume: 9000, OpenInterest: 900}},
			[]yahooOptionLeg{{Strike: 95, Volume: 800, OpenInterest: 4000}})), "Yahoo Finance options", ""),
	}}
	addPositioningSignal(&td, 0)

	var verdict string
	for _, f := range td.Facts {
		if f.Label == PositioningSignalLabel {
			verdict = f.Value
		}
	}
	if verdict == "" {
		t.Fatal("no combined positioning verdict")
	}
	if !strings.Contains(verdict, "flow —") {
		t.Errorf("the flow leg is not in the combined verdict: %s", verdict)
	}
	if !strings.Contains(verdict, directionalEvidence+"flow bullish") {
		t.Errorf("the flow leg's direction did not carry: %s", verdict)
	}
	if !HasPositioningSignal(td) {
		t.Error("a directional flow leg does not ground the sentiment domain")
	}
}
