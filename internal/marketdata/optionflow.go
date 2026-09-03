package marketdata

import (
	"fmt"
	"math"
	"strings"
)

// The flow leg reads option *volume*; the crowding leg reads open interest.
// They are deliberately separate signals from the same chain, because they are
// not the same fact.
//
// Open interest is the position that already exists — a stock of past decisions,
// which is why the crowding read is contrarian: a side everybody is already on
// is a side with nobody left to buy it. Volume is the position being taken now.
// A strike trading several times its own open interest is not churn in an
// existing book; it is a book being built today, and on a 5–20 day horizon that
// is the more timely of the two.
//
// What this cannot see is who initiated. Yahoo publishes volume, not the
// trade-side breakdown, so heavy call volume is a bet on the upside *or*
// somebody writing calls against stock they hold. The classifier answers that
// the only way the data allows — by refusing to read a direction unless the
// weight of the volume also sits on the side of spot that a directional bet
// would sit on — and abstaining wherever it does not. Abstention is first-class
// here; a wrong direction on 15% of the domain weight is expensive, and the
// insider leg beside it already carries the day when this one is quiet.
const (
	// optionContractSize is the shares one listed US contract controls, and the
	// multiplier that turns contracts into the dollar figure a skew is honest in.
	// 12,000 contracts at a $20 strike and 12,000 at a $400 strike are not the
	// same trade, and counting contracts says they are.
	optionContractSize = 100

	// flowMinContracts is the total two-expiry volume below which the chain is
	// too quiet to read anything from. A name that trades a few hundred
	// contracts a day produces a 5:1 "skew" out of two retail orders.
	flowMinContracts = 2_000
	// flowSkewRatio is how far one side's *dollar* volume must exceed the
	// other's before the day is one-sided rather than two-way.
	flowSkewRatio = 2.5
	// flowSpikeVolOI is the volume-to-open-interest ratio at which a single
	// strike stops being turnover in an existing position and starts being a new
	// one. Below 1 the day did not even replace the book that was there.
	flowSpikeVolOI = 3.0
	// flowSpikeMinContracts keeps a spike from being a thin strike with 40
	// contracts of open interest and one 200-lot through it.
	flowSpikeMinContracts = 500
	// flowStrikeOffset is how far the dominant side's volume-weighted strike
	// must sit from spot, as a fraction of spot, before its placement says
	// anything. At the money the two readings — a directional bet and a hedge —
	// are indistinguishable, so nothing is claimed.
	flowStrikeOffset = 0.01
)

// strikePrint is the single heaviest strike on one side of the chain.
type strikePrint struct {
	Strike       float64
	Volume       float64
	OpenInterest float64
}

// volOI is how many times the strike's own standing position traded, and ok is
// false when there is no standing position to divide by.
//
// It used to answer flowSpikeVolOI — the spike threshold itself — for a strike
// with zero open interest, on the reasoning that a position built where none
// existed is position-building by definition. That reasoning is sound about a
// genuinely new strike and catastrophic about a missing field, and the two are
// indistinguishable from here. On 2026-09-03 Yahoo returned no open interest on
// any strike of any name, and because the fallback returned exactly the value
// spike() tests for, every heaviest print in the run passed the gate and three
// names carried a directional flow verdict citing "3.0x its open interest".
//
// So it abstains instead. An unknown denominator is the one case where this leg
// cannot tell a new book from an old one turning over, and the leg's whole
// design is to be silent when it cannot tell.
func (s strikePrint) volOI() (float64, bool) {
	if s.Volume <= 0 || s.OpenInterest <= 0 {
		return 0, false
	}
	return s.Volume / s.OpenInterest, true
}

// spike reports whether this print is new positioning rather than churn. An
// unknown ratio is not a spike.
func (s strikePrint) spike() bool {
	r, ok := s.volOI()
	return ok && s.Volume >= flowSpikeMinContracts && r >= flowSpikeVolOI
}

// volOIText renders the ratio for a sentence, saying so when it is unknown
// rather than printing a number nobody computed.
func (s strikePrint) volOIText() string {
	if r, ok := s.volOI(); ok {
		return fmt.Sprintf("%.1fx", r)
	}
	return "open interest not reported"
}

// optionFlow accumulates one name's traded volume across the fetched expiries.
type optionFlow struct {
	Spot float64

	CallVolume, PutVolume float64
	CallDollar, PutDollar float64
	// Volume-weighted strike sums, divided out by callVWStrike/putVWStrike.
	callStrikeVol, putStrikeVol float64

	TopCall, TopPut strikePrint
}

func (f *optionFlow) addCall(l yahooOptionLeg) {
	if l.Volume <= 0 || l.Strike <= 0 {
		return
	}
	f.CallVolume += l.Volume
	f.CallDollar += l.Volume * l.Strike * optionContractSize
	f.callStrikeVol += l.Volume * l.Strike
	if l.Volume > f.TopCall.Volume {
		f.TopCall = strikePrint{Strike: l.Strike, Volume: l.Volume, OpenInterest: l.OpenInterest}
	}
}

func (f *optionFlow) addPut(l yahooOptionLeg) {
	if l.Volume <= 0 || l.Strike <= 0 {
		return
	}
	f.PutVolume += l.Volume
	f.PutDollar += l.Volume * l.Strike * optionContractSize
	f.putStrikeVol += l.Volume * l.Strike
	if l.Volume > f.TopPut.Volume {
		f.TopPut = strikePrint{Strike: l.Strike, Volume: l.Volume, OpenInterest: l.OpenInterest}
	}
}

func (f optionFlow) callVWStrike() float64 {
	if f.CallVolume <= 0 {
		return 0
	}
	return f.callStrikeVol / f.CallVolume
}

func (f optionFlow) putVWStrike() float64 {
	if f.PutVolume <= 0 {
		return 0
	}
	return f.putStrikeVol / f.PutVolume
}

func (f optionFlow) total() float64 { return f.CallVolume + f.PutVolume }

// summary is the human-readable sentence stored beside the verdict. It leads
// with the dollar figures because that is what the skew is judged on, and it
// carries the heaviest print on each side because a single strike is often the
// whole story.
func (f optionFlow) summary(expiries int) (string, bool) {
	if f.total() <= 0 {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "call volume %s contracts ($%s notional) vs put volume %s ($%s) over the front %d %s",
		contracts(f.CallVolume), usd(f.CallDollar), contracts(f.PutVolume), usd(f.PutDollar),
		expiries, plural(expiries, "expiry", "expiries"))
	if f.TopCall.Volume > 0 {
		fmt.Fprintf(&b, "; heaviest call print %s at the %.2f strike on %s open interest (%s)",
			contracts(f.TopCall.Volume), f.TopCall.Strike, contracts(f.TopCall.OpenInterest), f.TopCall.volOIText())
	}
	if f.TopPut.Volume > 0 {
		fmt.Fprintf(&b, "; heaviest put print %s at the %.2f strike on %s open interest (%s)",
			contracts(f.TopPut.Volume), f.TopPut.Strike, contracts(f.TopPut.OpenInterest), f.TopPut.volOIText())
	}
	if f.Spot > 0 {
		fmt.Fprintf(&b, "; spot %.2f", f.Spot)
	}
	return b.String(), true
}

// classifyUnusualOptions turns one name's traded option volume into a verdict.
//
// Every condition below has to hold at once, and the order is not arbitrary:
// enough volume to mean anything, a one-sided day in dollars, at least one
// strike being built rather than churned, and the weight of that side's volume
// sitting where a directional bet would put it. Any one of them failing is an
// abstention, not a weaker score — this leg's job is to be silent on the many
// names whose chains say nothing and loud on the few whose chains do.
//
// Note the direction is *not* contrarian, unlike the crowding leg. Crowding is a
// stock of positions with nobody left to add; a day's flow is the adding itself.
func classifyUnusualOptions(f optionFlow) insiderSignal {
	if f.total() < flowMinContracts {
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: %s option contracts traded across the front expiries, under the %s a one-sided day needs to mean anything",
			insiderNoSignal, contracts(f.total()), contracts(flowMinContracts))}
	}

	callSide := f.CallDollar >= f.PutDollar*flowSkewRatio
	putSide := f.PutDollar >= f.CallDollar*flowSkewRatio
	if !callSide && !putSide {
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: $%s of calls against $%s of puts is a two-way day, under the %.1fx skew one side needs",
			insiderNoSignal, usd(f.CallDollar), usd(f.PutDollar), flowSkewRatio)}
	}

	top, side := f.TopCall, "call"
	if putSide {
		top, side = f.TopPut, "put"
	}
	if !top.spike() {
		if _, ok := top.volOI(); !ok {
			return insiderSignal{InsiderNone, fmt.Sprintf(
				"%s: the day leans %s in dollars, but its heaviest strike reports no open interest to measure against — without a standing position there is no way to tell a book being built from one turning over",
				insiderNoSignal, side)}
		}
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: the day leans %s in dollars, but its heaviest strike traded %s its own open interest — that is turnover in a book that already existed, not a new one",
			insiderNoSignal, side, top.volOIText())}
	}

	// Where the volume sits relative to spot is the only thing separating a
	// directional bet from a hedge, and it is a weak separator, so it is applied
	// strictly: upside calls or downside puts, or nothing.
	if f.Spot <= 0 {
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: a one-sided %s day with a %s spike, but no spot price to place the strikes against",
			insiderNoSignal, side, top.volOIText())}
	}
	vws := f.callVWStrike()
	if putSide {
		vws = f.putVWStrike()
	}
	offset := (vws - f.Spot) / f.Spot
	switch {
	case callSide && offset >= flowStrikeOffset:
		return insiderSignal{InsiderBullish, fmt.Sprintf(
			"bullish: $%s of call volume against $%s of puts (%.1fx), concentrated %.1f%% above spot at a volume-weighted %.2f strike, with %s contracts through the %.2f strike on %s its open interest — positions being opened to the upside, not an existing book turning over",
			usd(f.CallDollar), usd(f.PutDollar), safeRatio(f.CallDollar, f.PutDollar),
			offset*100, vws, contracts(top.Volume), top.Strike, top.volOIText())}
	case putSide && offset <= -flowStrikeOffset:
		return insiderSignal{InsiderBearish, fmt.Sprintf(
			"bearish: $%s of put volume against $%s of calls (%.1fx), concentrated %.1f%% below spot at a volume-weighted %.2f strike, with %s contracts through the %.2f strike on %s its open interest — downside being bought, not hedged into an existing book",
			usd(f.PutDollar), usd(f.CallDollar), safeRatio(f.PutDollar, f.CallDollar),
			math.Abs(offset)*100, vws, contracts(top.Volume), top.Strike, top.volOIText())}
	}
	// One-sided and spiking, but at the money or on the wrong side of it. A
	// covered-call programme and a bullish bet look identical from here.
	return insiderSignal{InsiderNone, fmt.Sprintf(
		"%s: a one-sided %s day with a %s spike, but its volume-weighted %.2f strike sits %.1f%% from the %.2f spot — too close to tell a directional bet from a hedge",
		insiderNoSignal, side, top.volOIText(), vws, offset*100, f.Spot)}
}

// safeRatio is a/b with a zero denominator reported as the skew threshold rather
// than as an infinity in a sentence a person has to read.
func safeRatio(a, b float64) float64 {
	if b <= 0 {
		return flowSkewRatio
	}
	return a / b
}

// contracts formats a contract count with thousands separators. Option volume
// spans three orders of magnitude across a shortlist and "48200" is unreadable
// next to "482".
func contracts(n float64) string {
	s := fmt.Sprintf("%.0f", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
