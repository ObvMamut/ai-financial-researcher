package marketdata

import (
	"fmt"
	"sort"
	"strconv"
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
	// The raw facts the computed legs are derived from. Named here because
	// addPositioningSignal reads them back when a leg's computed verdict is
	// missing — the shape a cache entry written by an older binary has.
	InsiderActivityLabel    = "Insider activity (SEC Form 4)"
	OptionsPositioningLabel = "Options positioning"
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
// persona both key off. directionalEvidence is its counterpart: the marker that
// says a direction was actually found, and the one HasPositioningSignal tests
// for — a positive marker cannot be widened by a new kind of abstention the way
// a growing list of negative phrases can.
const (
	noPositioningSignal  = "Both legs read no directional signal"
	directionalEvidence  = "Directional evidence: "
	unresolvedLegVerdict = "no computed verdict for this leg in this run's data"
)

// positioningLeg is one side of the combined verdict while it is being assembled.
type positioningLeg struct {
	sig insiderSignal
	// computed marks a verdict taken from the provider's own signal fact, which
	// is authoritative. A reconstructed one is not: it is re-derived from the raw
	// summary and only trusted where that summary is complete enough.
	computed bool
	// resolved is false for a leg whose raw fact is present but whose verdict
	// cannot be re-derived from it.
	resolved bool
}

// addPositioningSignal combines whatever legs a ticker's facts carry into the
// single verdict the sentiment agent is required to obey, and appends it. It is a
// no-op for a ticker with neither leg.
//
// When both legs read "no directional signal" the name has no positioning
// evidence and belongs in the report's `missing` array rather than in `scores` —
// the same convention the coverage enforcement already applies. That instruction
// is written into the fact so it travels with the data instead of living only in
// a persona the model can decline.
//
// A leg's computed verdict is authoritative where it exists. Where it does not —
// a cache entry written before the computed legs existed — the verdict is
// re-derived from the raw fact the provider wrote beside it, because that is
// where the inputs are: the put/call ratio, and the Form 4 buy/sale counts.
// Returning early instead was how ORCL kept a bearish 5 on a put/call of 0.79 and
// zero open-market trades.
func addPositioningSignal(td *TickerData) {
	legs := map[string]*positioningLeg{}
	var order []string
	leg := func(name string) *positioningLeg {
		l, ok := legs[name]
		if !ok {
			l = &positioningLeg{}
			legs[name] = l
			order = append(order, name)
		}
		return l
	}
	// A computed fact always follows its raw one from the same provider, so a
	// single pass reaches the raw fact first and the computed one overwrites it.
	adopt := func(name string, f Fact) {
		l := leg(name)
		l.sig, l.computed, l.resolved = insiderSignal{biasOf(f.Value), f.Value}, true, true
	}
	rebuild := func(name string, value string, from func(string) (insiderSignal, bool)) {
		l := leg(name)
		if l.computed {
			return
		}
		l.sig, l.resolved = from(value)
	}
	for _, f := range td.Facts {
		switch f.Label {
		case InsiderSignalLabel:
			adopt("insider", f)
		case OptionsSignalLabel:
			adopt("options", f)
		case InsiderActivityLabel:
			rebuild("insider", f.Value, reconstructInsiderLeg)
		case OptionsPositioningLabel:
			rebuild("options", f.Value, reconstructOptionsLeg)
		}
	}
	if len(order) == 0 {
		return
	}

	var parts, sides []string
	unresolved := false
	for _, name := range order {
		l := legs[name]
		if !l.resolved {
			unresolved = true
			parts = append(parts, name+" — "+unresolvedLegVerdict)
			continue
		}
		parts = append(parts, name+" — "+l.sig.Reason)
		if l.sig.Bias != InsiderNone {
			sides = append(sides, name+" "+string(l.sig.Bias))
		}
	}

	var b strings.Builder
	b.WriteString(strings.Join(parts, "; "))
	switch {
	case len(sides) > 0:
		sort.Strings(sides)
		fmt.Fprintf(&b, ". %s%s.", directionalEvidence, strings.Join(sides, " and "))
	case unresolved:
		// Neither "quiet" nor "directional" is established. Abstaining is the
		// cheap error — it costs the name a score and does not degrade the run —
		// while scoring on a verdict nothing computed is the expensive one.
		b.WriteString(". No leg established a direction and at least one could not be read at all, so this name's positioning is unsettled: put it in `missing`, not in `scores`.")
	default:
		b.WriteString(". ")
		b.WriteString(noPositioningSignal)
		b.WriteString(", so this name has no positioning evidence: put it in `missing`, not in `scores`.")
	}
	td.Facts = append(td.Facts, Fact{Label: PositioningSignalLabel, Value: b.String(), Source: "computed"})
}

// reconstructInsiderLeg re-derives the insider verdict from the Form 4 summary
// sentence, for facts that predate the computed leg.
//
// It resolves only the cases the sentence carries in full. Breadth (how many
// distinct owners sold) and depth (how much of one owner's holding a sale moved)
// never reached that sentence, so a window with actual trades in it cannot be
// re-classified here — and guessing a direction that contradicts what the fetch
// path would have computed is worse than admitting the gap.
func reconstructInsiderLeg(value string) (insiderSignal, bool) {
	if strings.Contains(value, "no Form 4 filings") {
		return classifyInsiderActivity(nil), true
	}
	buys, sells, ok := openMarketCounts(value)
	if !ok || buys > 0 || sells > 0 {
		return insiderSignal{}, false
	}
	return classifyInsiderActivity(nil), true
}

// openMarketCounts reads the buy and sale counts back out of the Form 4 summary
// ("0 open-market buys ($0) vs 0 sales ($0) across 1 filing(s) in 45 days").
func openMarketCounts(value string) (buys, sells int, ok bool) {
	fields := strings.Fields(value)
	if len(fields) < 2 || fields[1] != "open-market" {
		return 0, 0, false
	}
	b, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, false
	}
	for i, f := range fields {
		if f != "vs" || i+1 >= len(fields) {
			continue
		}
		s, err := strconv.Atoi(fields[i+1])
		if err != nil {
			return 0, 0, false
		}
		return b, s, true
	}
	return 0, 0, false
}

// reconstructOptionsLeg re-derives the options verdict from the positioning
// sentence. The ratio is the whole input to classifyOptionsPositioning, and the
// sentence leads with it, so this reproduces the fetch path exactly.
func reconstructOptionsLeg(value string) (insiderSignal, bool) {
	const marker = "put/call open interest "
	i := strings.Index(value, marker)
	if i < 0 {
		return insiderSignal{}, false
	}
	rest := value[i+len(marker):]
	if j := strings.IndexFunc(rest, func(r rune) bool {
		return r != '.' && (r < '0' || r > '9')
	}); j >= 0 {
		rest = rest[:j]
	}
	ratio, err := strconv.ParseFloat(rest, 64)
	if err != nil {
		return insiderSignal{}, false
	}
	return classifyOptionsPositioning(ratio), true
}

// HasPositioningSignal reports whether a ticker's facts carry a computed
// positioning verdict with a direction in it. It is what the orchestrator's
// enforcement pass uses to decide whether a sentiment score was earned.
func HasPositioningSignal(td TickerData) bool {
	for _, f := range td.Facts {
		if f.Label == PositioningSignalLabel {
			return strings.Contains(f.Value, directionalEvidence)
		}
	}
	// No verdict at all. This used to return true — "stay out of the way rather
	// than silence a domain on a technicality" — and the technicality turned out
	// to be the common case: a stale cache entry served without the computed legs
	// took this branch and the domain kept every unearned score. With the cache
	// keyed by schema version and the legs re-derivable from the raw facts, an
	// absent verdict now means the provider genuinely returned nothing, and
	// Coverage[t] is already false for that name — so this silences nothing that
	// was working.
	return false
}
