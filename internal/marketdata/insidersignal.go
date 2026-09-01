package marketdata

import (
	"fmt"
	"sort"
	"strings"
)

// The insider verdict is computed here rather than left to the sentiment agent,
// for the same reason the domain weighting, the multiples and the position sizing
// were moved into Go: an instruction a model can decline is not a rule.
//
// agents/sentiment.md has said since it was written that "routine, spread-out
// selling by many insiders is weak evidence of anything — executives sell for
// reasons unrelated to the stock". Across the four runs from 2026-08-31 the
// sentiment domain returned exactly one bullish score in 34, and on 2026-09-01 it
// scored AMGN bearish 6 on two routine disposals worth $1.35M. At 15% of the
// weight that is a flat ~9-point levy on every long in the book, which no long
// can offset and which no amount of rubric wording removed.
//
// The asymmetry is real and it is in the data, not in the model: officers receive
// stock and sell it on schedules set months in advance, so "0 buys vs N sales" is
// the resting state of almost every large-cap issuer. Open-market *buying* is the
// rare, discretionary act. Scoring the two symmetrically is what produced a domain
// that could only ever vote down.
const (
	// insiderBuyClusterOwners is how many distinct insiders buying on the open
	// market makes a cluster rather than one person's decision.
	insiderBuyClusterOwners = 2
	// insiderSingleBuyUSD is the size at which one open-market purchase is a
	// statement on its own.
	insiderSingleBuyUSD = 250_000
	// insiderSellBreadthOwners is how many distinct sellers it takes before
	// selling is the group's behaviour rather than one person's liquidity.
	insiderSellBreadthOwners = 3
	// insiderSellFraction is the share of an owner's own holding that makes a
	// sale a decision about the stock rather than a trim. Dollar totals cannot
	// carry this: $5.76M is a career-defining sale for one officer and a rounding
	// error against IBM's $1.2B of daily turnover.
	insiderSellFraction = 0.33
)

// InsiderBias is the computed direction of insider activity, or none.
type InsiderBias string

const (
	InsiderNone     InsiderBias = "none"
	InsiderBullish  InsiderBias = "bullish"
	InsiderBearish  InsiderBias = "bearish"
	insiderNoSignal             = "no directional signal"
)

// insiderSignal is the verdict plus the sentence that justifies it.
type insiderSignal struct {
	Bias   InsiderBias
	Reason string
}

// classifyInsiderActivity reduces a window of open-market Form 4 lines to one
// verdict. It is deliberately hard to make bearish and comparatively easy to make
// bullish, because that is how the underlying evidence behaves.
func classifyInsiderActivity(txns []form4Transaction) insiderSignal {
	if len(txns) == 0 {
		return insiderSignal{InsiderNone, insiderNoSignal + ": no open-market insider transactions in the window"}
	}

	var buyUSD, sellUSD, largestBuy float64
	buyers, sellers := map[string]bool{}, map[string]bool{}
	var deepest form4Transaction
	for _, t := range txns {
		if t.Buy {
			buyers[t.Owner] = true
			buyUSD += t.value()
			if v := t.value(); v > largestBuy {
				largestBuy = v
			}
			continue
		}
		sellers[t.Owner] = true
		sellUSD += t.value()
		if t.fractionOfHolding() > deepest.fractionOfHolding() {
			deepest = t
		}
	}

	// Buying first, and unconditionally: an insider putting their own money in is
	// the strongest thing this source can say, and it is not cancelled by other
	// insiders' scheduled selling.
	switch {
	case len(buyers) >= insiderBuyClusterOwners:
		return insiderSignal{InsiderBullish, fmt.Sprintf(
			"bullish: %d insiders bought on the open market ($%s total), which is a cluster, not one person's decision",
			len(buyers), usd(buyUSD))}
	case largestBuy >= insiderSingleBuyUSD:
		return insiderSignal{InsiderBullish, fmt.Sprintf(
			"bullish: a single open-market purchase of $%s, above the $%s bar for one buy to stand on its own",
			usd(largestBuy), usd(insiderSingleBuyUSD))}
	case len(buyers) > 0:
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: %d open-market purchase(s) totalling $%s, below the size at which one buy carries",
			insiderNoSignal, len(buyers), usd(buyUSD))}
	}

	if len(sellers) == 0 {
		return insiderSignal{InsiderNone, insiderNoSignal + ": no open-market purchases or sales in the window"}
	}

	// Selling needs breadth or depth. Neither is about the dollar total.
	if frac := deepest.fractionOfHolding(); frac >= insiderSellFraction {
		who := deepest.Owner
		if deepest.Title != "" {
			who += " (" + deepest.Title + ")"
		}
		return insiderSignal{InsiderBearish, fmt.Sprintf(
			"bearish: %s sold %.0f%% of their own holding ($%s) — a decision about the position, not a trim",
			who, frac*100, usd(deepest.value()))}
	}
	if len(sellers) >= insiderSellBreadthOwners {
		return insiderSignal{InsiderBearish, fmt.Sprintf(
			"bearish: %d distinct insiders sold ($%s total) — broad enough to be the group's behaviour rather than individual liquidity",
			len(sellers), usd(sellUSD))}
	}
	return insiderSignal{InsiderNone, fmt.Sprintf(
		"%s: %d insider(s) sold $%s, neither broad (under %d sellers) nor deep (under %.0f%% of a holding) — routine disposal",
		insiderNoSignal, len(sellers), usd(sellUSD), insiderSellBreadthOwners, insiderSellFraction*100)}
}

// optionsSignalThresholds bound where a put/call open-interest ratio stops being
// noise. agents/sentiment.md already says "near 1.0 is unremarkable"; these are
// that sentence as numbers.
const (
	putCallCrowdedPuts  = 1.5
	putCallCrowdedCalls = 0.67
)

// classifyOptionsPositioning turns a put/call open-interest ratio into a verdict.
//
// Crowding is one-sidedness of positioning, and the persona is explicit that it is
// a risk *to the crowded side*, not confirmation of it. That makes an extreme
// ratio a contrarian reading, and a middling one nothing at all — which is what
// the sentiment agent kept scoring as bearish in both directions: puts crowded
// read as bearish, and calls crowded read as "a squeeze risk", also bearish.
func classifyOptionsPositioning(putCallOI float64) insiderSignal {
	switch {
	case putCallOI <= 0:
		return insiderSignal{InsiderNone, insiderNoSignal + ": no option chain for this listing"}
	case putCallOI >= putCallCrowdedPuts:
		return insiderSignal{InsiderBullish, fmt.Sprintf(
			"contrarian bullish: put/call open interest %.2f is crowded on the put side, and crowding is a risk to the side holding it",
			putCallOI)}
	case putCallOI <= putCallCrowdedCalls:
		return insiderSignal{InsiderBearish, fmt.Sprintf(
			"contrarian bearish: put/call open interest %.2f is crowded on the call side, and crowding is a risk to the side holding it",
			putCallOI)}
	default:
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: put/call open interest %.2f is within the unremarkable %.2f–%.2f band",
			insiderNoSignal, putCallOI, putCallCrowdedCalls, putCallCrowdedPuts)}
	}
}

// The two legs are computed by their own providers — only they hold the raw
// transactions and the chain — and combined at pack level, where they meet. Each
// leg's verdict is carried in its fact's own text so it survives the disk cache
// without a parallel channel.
const (
	InsiderSignalLabel     = "Insider signal (computed)"
	OptionsSignalLabel     = "Options signal (computed)"
	PositioningSignalLabel = "Positioning signal (computed)"
)

// signalFact renders one leg's verdict. The bias leads the string so it can be
// read back without re-deriving it.
func signalFact(label string, s insiderSignal, source, url string) Fact {
	return Fact{Label: label, Value: s.Reason, Source: source, URL: url}
}

// biasOf reads a verdict back off a rendered fact.
func biasOf(value string) InsiderBias {
	switch {
	case strings.HasPrefix(value, string(InsiderBullish)), strings.HasPrefix(value, "contrarian bullish"):
		return InsiderBullish
	case strings.HasPrefix(value, string(InsiderBearish)), strings.HasPrefix(value, "contrarian bearish"):
		return InsiderBearish
	default:
		return InsiderNone
	}
}

// noPositioningSignal is the sentence HasPositioningSignal and the sentiment
// persona both key off.
const noPositioningSignal = "Both legs read no directional signal"

// addPositioningSignal combines whatever computed legs a ticker's facts carry
// into the single verdict the sentiment agent is required to obey, and appends
// it. It is a no-op for a ticker with neither leg.
//
// When both legs read "no directional signal" the name has no positioning
// evidence and belongs in the report's `missing` array rather than in `scores` —
// the same convention the coverage enforcement already applies. That instruction
// is written into the fact so it travels with the data instead of living only in
// a persona the model can decline.
func addPositioningSignal(td *TickerData) {
	var legs []string
	var sides []string
	seen := false
	for _, f := range td.Facts {
		var leg string
		switch f.Label {
		case InsiderSignalLabel:
			leg = "insider"
		case OptionsSignalLabel:
			leg = "options"
		default:
			continue
		}
		seen = true
		legs = append(legs, leg+" — "+f.Value)
		if b := biasOf(f.Value); b != InsiderNone {
			sides = append(sides, leg+" "+string(b))
		}
	}
	if !seen {
		return
	}

	var b strings.Builder
	b.WriteString(strings.Join(legs, "; "))
	if len(sides) == 0 {
		b.WriteString(". ")
		b.WriteString(noPositioningSignal)
		b.WriteString(", so this name has no positioning evidence: put it in `missing`, not in `scores`.")
	} else {
		sort.Strings(sides)
		fmt.Fprintf(&b, ". Directional evidence: %s.", strings.Join(sides, " and "))
	}
	td.Facts = append(td.Facts, Fact{Label: PositioningSignalLabel, Value: b.String(), Source: "computed"})
}

// HasPositioningSignal reports whether a ticker's facts carry a computed
// positioning verdict with a direction in it. It is what the orchestrator's
// enforcement pass uses to decide whether a sentiment score was earned.
func HasPositioningSignal(td TickerData) bool {
	for _, f := range td.Facts {
		if f.Label == PositioningSignalLabel {
			return !strings.Contains(f.Value, noPositioningSignal)
		}
	}
	// No computed verdict at all (an older cache entry, or a provider error):
	// stay out of the way rather than silence a domain on a technicality.
	return true
}
