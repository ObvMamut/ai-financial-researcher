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
	// insiderOfficerBuyUSD is the same bar for an officer, set lower on purpose.
	//
	// The roles were pooled, and they do not carry the same information. A
	// director's holding is usually a fee paid in stock and their purchases are
	// often token; an officer buying their own company on the open market is
	// spending money they were already overexposed to the outcome of, having
	// seen the current quarter. A 10% owner is a third thing again — buying more
	// of a stake they already hold is accumulation, and it is the boundary where
	// this leg starts to overlap the activist one.
	insiderOfficerBuyUSD = 100_000
	// insiderSellBreadthOwners is how many distinct sellers it takes before
	// selling is the group's behaviour rather than one person's liquidity.
	insiderSellBreadthOwners = 3
	// insiderSellFraction is the share of an owner's own holding that makes a
	// sale a decision about the stock rather than a trim. Dollar totals cannot
	// carry this: $5.76M is a career-defining sale for one officer and a rounding
	// error against IBM's $1.2B of daily turnover.
	insiderSellFraction = 0.33
	// insiderSellDepthUSD is the floor under that fraction. It is the mirror of
	// insiderOfficerBuyUSD, which the buy side has always had.
	//
	// "Shares held following" on a Form 4 counts *direct* holdings only, so an
	// officer whose equity sits in RSUs, a trust or a 10b5-1 vehicle reports a
	// token direct position and trips 100% on a rounding error. On 2026-09-03
	// QCOM's whole sentiment domain went bearish on "sold 100% of their own
	// holding ($35k)" by an SVP — a third of a percent of one day's turnover,
	// carrying 15% of the score. A sale that is a decision about the stock is at
	// least the size of a purchase that would be.
	insiderSellDepthUSD = 100_000
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
// scheduledContext is what the same window's Form 144 notices say about the
// sales in a Form 4 window. The two forms describe the same event from opposite
// ends — a 144 announces the sale, a Form 4 reports it — so when every notice in
// the window is a 10b5-1 plan or vesting equity, the Form 4 sales are those
// notices and the "breadth" they show is the compensation calendar.
//
// This exists because of a live NKE read: raising form4MaxDocs from 5 to 12
// widened the window enough to find 5 distinct sellers, which tripped the breadth
// test — on $400k in total, at a company whose Form 144s for the same window were
// 5 notices, $400k, every one of them scheduled. The deeper window did not find
// new information; it found more of the payroll.
type scheduledContext struct {
	// ScheduledUSD and UnscheduledUSD are the window's Form 144 totals. Both zero
	// means no notices were read, which establishes nothing either way.
	ScheduledUSD, UnscheduledUSD float64
}

// coversSelling reports whether the scheduled notices account for enough of a
// Form 4 window's sales to say those sales were pre-declared. Half is the bar:
// Form 144 is only required for restricted and control securities over 5,000
// shares or $50k, so it will always cover somewhat less than the Form 4 total.
func (c scheduledContext) coversSelling(sellUSD float64) bool {
	if c.UnscheduledUSD > 0 || c.ScheduledUSD <= 0 || sellUSD <= 0 {
		return false
	}
	return c.ScheduledUSD >= sellUSD*0.5
}

func classifyInsiderActivity(txns []form4Transaction) insiderSignal {
	return classifyInsiderActivityWith(txns, scheduledContext{})
}

func classifyInsiderActivityWith(txns []form4Transaction, sched scheduledContext) insiderSignal {
	if len(txns) == 0 {
		return insiderSignal{InsiderNone, insiderNoSignal + ": no open-market insider transactions in the window"}
	}

	var buyUSD, sellUSD, largestBuy float64
	buyers, sellers := map[string]bool{}, map[string]bool{}
	var deepest, largestOfficerBuy, largestOwnerBuy form4Transaction
	for _, t := range txns {
		if t.Buy {
			buyers[t.Owner] = true
			buyUSD += t.value()
			if v := t.value(); v > largestBuy {
				largestBuy = v
			}
			switch t.role() {
			case roleOfficer:
				if t.value() > largestOfficerBuy.value() {
					largestOfficerBuy = t
				}
			case roleTenPercent:
				if t.value() > largestOwnerBuy.value() {
					largestOwnerBuy = t
				}
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
	case largestOfficerBuy.value() >= insiderOfficerBuyUSD:
		return insiderSignal{InsiderBullish, fmt.Sprintf(
			"bullish: %s bought $%s of their own company on the open market — an officer's purchase clears a lower bar ($%s) than a director's, because they are adding to an exposure they already cannot diversify",
			who(largestOfficerBuy), usd(largestOfficerBuy.value()), usd(insiderOfficerBuyUSD))}
	case largestOwnerBuy.value() >= insiderSingleBuyUSD:
		return insiderSignal{InsiderBullish, fmt.Sprintf(
			"bullish: a 10%% owner added $%s to an existing stake — accumulation by a holder already at the disclosure threshold",
			usd(largestOwnerBuy.value()))}
	case largestBuy >= insiderSingleBuyUSD:
		return insiderSignal{InsiderBullish, fmt.Sprintf(
			"bullish: a single open-market purchase of $%s, above the $%s bar for one buy to stand on its own",
			usd(largestBuy), usd(insiderSingleBuyUSD))}
	case len(buyers) > 0:
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: %d open-market purchase(s) totalling $%s, below the size at which one buy carries for the role that made it",
			insiderNoSignal, len(buyers), usd(buyUSD))}
	}

	if len(sellers) == 0 {
		return insiderSignal{InsiderNone, insiderNoSignal + ": no open-market purchases or sales in the window"}
	}

	// Selling needs breadth, or depth that is also worth something. The fraction
	// is the test; the dollar floor only keeps a 100%-of-a-token-holding sale
	// from carrying the domain on its own.
	frac := deepest.fractionOfHolding()
	switch {
	case frac >= insiderSellFraction && deepest.value() >= insiderSellDepthUSD:
		return insiderSignal{InsiderBearish, fmt.Sprintf(
			"bearish: %s sold %.0f%% of their own holding ($%s) — a decision about the position, not a trim",
			who(deepest), frac*100, usd(deepest.value()))}
	case frac >= insiderSellFraction:
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: %s sold %.0f%% of their own holding, but that holding was $%s — under the $%s a sale needs to be a decision rather than an artefact of how the equity is held, since a Form 4 counts only directly held shares",
			insiderNoSignal, who(deepest), frac*100, usd(deepest.value()), usd(insiderSellDepthUSD))}
	}
	if len(sellers) >= insiderSellBreadthOwners {
		// Breadth is only evidence when the timing was the sellers' to choose.
		// Depth above is not suppressed the same way: a plan that takes a third
		// of somebody's holding is still a third of their holding gone.
		if sched.coversSelling(sellUSD) {
			return insiderSignal{InsiderNone, fmt.Sprintf(
				"%s: %d distinct insiders sold $%s, but the same window's Form 144 notices declare $%s of scheduled sales and none unscheduled — this is the compensation calendar arriving together, not the group deciding",
				insiderNoSignal, len(sellers), usd(sellUSD), usd(sched.ScheduledUSD))}
		}
		return insiderSignal{InsiderBearish, fmt.Sprintf(
			"bearish: %d distinct insiders sold ($%s total) — broad enough to be the group's behaviour rather than individual liquidity",
			len(sellers), usd(sellUSD))}
	}
	return insiderSignal{InsiderNone, fmt.Sprintf(
		"%s: %d insider(s) sold $%s, neither broad (under %d sellers) nor deep (under %.0f%% of a holding) — routine disposal",
		insiderNoSignal, len(sellers), usd(sellUSD), insiderSellBreadthOwners, insiderSellFraction*100)}
}

// who names an insider with their reported relationship, which is the part that
// says how much the trade is worth reading.
func who(t form4Transaction) string {
	if t.Title == "" {
		return t.Owner
	}
	return t.Owner + " (" + t.Title + ")"
}

// optionsSignalThresholds bound where a put/call open-interest ratio stops being
// noise.
//
// They used to be 1.5 and 0.67, which is not where crowding starts — it is where
// a large-cap in an uptrend ordinarily sits. Across the 27 shipped ideas that
// carry per-domain scores, sentiment agreed with the quant read on 4 of 17
// (24%), against 95% for news and 90% for macro, and the mechanism was this
// band: a put/call under 0.67 is the resting state of a name people are long, so
// the domain read "contrarian bearish" on almost every long the pre-screen
// nominated. On 2026-09-01 it scored MRK bearish 7 on a 0.50 ratio against news
// bullish 7, and a name every one of the five domains could see finished with a
// base of 31.
//
// A genuine extreme is much further out. 0.45 and 2.0 are roughly the 10th and
// 90th percentiles of front-expiry put/call open interest across large caps —
// far enough that being there is a fact about the name rather than about the
// market.
const (
	putCallCrowdedPuts  = 2.0
	putCallCrowdedCalls = 0.45
	// putCallPeerRatio is how far from the shortlist's own median a name must
	// also sit before an extreme reads as crowding rather than as the level the
	// whole tape is at. A fixed band cannot tell "this name is crowded" from
	// "every name is crowded this month"; the cross-section can, and it needs no
	// history to do it — the same trick the pre-screen uses when it z-scores
	// within an index.
	putCallPeerRatio = 1.5
	// putCallPeerMin is how many names must carry a chain before their median
	// means anything. Below it the absolute band stands alone.
	putCallPeerMin = 3
)

// classifyOptionsPositioning turns a put/call open-interest ratio into a verdict.
//
// Crowding is one-sidedness of positioning, and the persona is explicit that it is
// a risk *to the crowded side*, not confirmation of it. That makes an extreme
// ratio a contrarian reading, and a middling one nothing at all — which is what
// the sentiment agent kept scoring as bearish in both directions: puts crowded
// read as bearish, and calls crowded read as "a squeeze risk", also bearish.
//
// peerMedian is the median ratio across the shortlist's other names, or zero when
// the run has too few chains for one. An extreme that the whole shortlist shares
// is the market's level, not this name's positioning, and reading it as crowding
// would put the same contrarian tilt on every idea in the book at once.
//
// The comparison is deliberately *cross-sectional* — this name against today's
// shortlist — rather than against the name's own recent put/call history, which
// is the other obvious way to define "extreme" and the one originally specified.
// Nothing here stores a per-name put/call series, and a historical read would
// need one persisted across runs before it could say anything on the first run
// of a name. The cross-section answers the same question the same day, from data
// the run already has. Revisit only alongside that persistence, not before.
func classifyOptionsPositioning(putCallOI, peerMedian float64) insiderSignal {
	peers := peerMedian > 0
	switch {
	case putCallOI <= 0:
		return insiderSignal{InsiderNone, insiderNoSignal + ": no option chain for this listing"}
	case putCallOI >= putCallCrowdedPuts:
		if peers && putCallOI < peerMedian*putCallPeerRatio {
			return insiderSignal{InsiderNone, fmt.Sprintf(
				"%s: put/call open interest %.2f is high in absolute terms but close to the %.2f median across this run's names — that is the tape's level, not this name's positioning",
				insiderNoSignal, putCallOI, peerMedian)}
		}
		return insiderSignal{InsiderBullish, fmt.Sprintf(
			"contrarian bullish: put/call open interest %.2f is crowded on the put side%s, and crowding is a risk to the side holding it",
			putCallOI, peerNote(peers, peerMedian))}
	case putCallOI <= putCallCrowdedCalls:
		if peers && putCallOI > peerMedian/putCallPeerRatio {
			return insiderSignal{InsiderNone, fmt.Sprintf(
				"%s: put/call open interest %.2f is low in absolute terms but close to the %.2f median across this run's names — that is the tape's level, not this name's positioning",
				insiderNoSignal, putCallOI, peerMedian)}
		}
		return insiderSignal{InsiderBearish, fmt.Sprintf(
			"contrarian bearish: put/call open interest %.2f is crowded on the call side%s, and crowding is a risk to the side holding it",
			putCallOI, peerNote(peers, peerMedian))}
	default:
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: put/call open interest %.2f is within the unremarkable %.2f–%.2f band",
			insiderNoSignal, putCallOI, putCallCrowdedCalls, putCallCrowdedPuts)}
	}
}

func peerNote(peers bool, median float64) string {
	if !peers {
		return ""
	}
	return fmt.Sprintf(" and %.1fx clear of this run's %.2f median", putCallPeerRatio, median)
}

// The two legs are computed by their own providers — only they hold the raw
// transactions and the chain — and combined at pack level, where they meet. Each
// leg's verdict is carried in its fact's own text so it survives the disk cache
// without a parallel channel.
const (
	InsiderSignalLabel     = "Insider signal (computed)"
	OptionsSignalLabel     = "Options signal (computed)"
	PositioningSignalLabel = "Positioning signal (computed)"
	// OptionsFlowSignalLabel is the flow leg's verdict, written by the same
	// provider and in the same pass as the raw sentence below it. Unlike the
	// crowding leg it is adopted rather than recomputed: the flow read depends
	// only on the name's own chain, so a stored verdict is still the right
	// answer for the data it was computed from.
	OptionsFlowSignalLabel = "Options flow signal (computed)"
	// PlannedSalesSignalLabel is the Form 144 leg's verdict, written together
	// with its raw sentence for the same reason as the flow leg's.
	PlannedSalesSignalLabel = "Planned insider sales signal (computed)"
	// ActivistStakeSignalLabel is the 13D/G leg's verdict.
	ActivistStakeSignalLabel = "Activist stake signal (computed)"
	// InstitutionalSignalLabel is the 13F leg's verdict.
	InstitutionalSignalLabel = "Institutional holdings signal (computed)"
	// The raw facts the computed legs are derived from. Named here because
	// addPositioningSignal reads them back when a leg's computed verdict is
	// missing — the shape a cache entry written by an older binary has.
	InsiderActivityLabel    = "Insider activity (SEC Form 4)"
	OptionsPositioningLabel = "Options positioning"
	UnusualOptionsLabel     = "Unusual option activity"
	PlannedSalesLabel       = "Planned insider sales (SEC Form 144)"
	ActivistStakeLabel      = "5%+ ownership schedules (SEC 13D/13G)"
	InstitutionalLabel      = "Tracked institutional holdings (SEC 13F)"
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
func addPositioningSignal(td *TickerData, peerMedian float64) {
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
		case InsiderActivityLabel:
			rebuild("insider", f.Value, reconstructInsiderLeg)
		case OptionsFlowSignalLabel:
			// Adopt-only, with no reconstruction path beside it. The raw
			// sentence and this verdict are written together by one provider in
			// one version, and factSchemaVersion is bumped whenever the pair
			// changes shape — so unlike the two original legs there is no
			// cached entry that can carry one without the other.
			adopt("flow", f)
		case PlannedSalesSignalLabel:
			adopt("planned sales", f)
		case ActivistStakeSignalLabel:
			adopt("activist stakes", f)
		case InstitutionalSignalLabel:
			adopt("institutional", f)
		case OptionsPositioningLabel:
			// Always recomputed, never adopted from a stored verdict: the
			// options leg depends on the run's own cross-section, so a value
			// written under a different shortlist is a different answer.
			rebuild("options", f.Value, func(v string) (insiderSignal, bool) {
				return reconstructOptionsLeg(v, peerMedian)
			})
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
func reconstructOptionsLeg(value string, peerMedian float64) (insiderSignal, bool) {
	ratio, ok := parsePutCall(value)
	if !ok {
		return insiderSignal{}, false
	}
	return classifyOptionsPositioning(ratio, peerMedian), true
}

// optionsPutCall extracts the put/call open-interest ratio a ticker's facts
// carry, or 0 when it has no chain. It is what the cross-sectional median is
// taken over.
func optionsPutCall(td TickerData) float64 {
	for _, f := range td.Facts {
		if f.Label != OptionsPositioningLabel {
			continue
		}
		if r, ok := parsePutCall(f.Value); ok {
			return r
		}
	}
	return 0
}

// PutCallMedian is the median put/call open-interest ratio across the names in a
// pack that have a chain, or 0 when fewer than putCallPeerMin do. It is the
// cross-section classifyOptionsPositioning judges one name's crowding against.
func PutCallMedian(byTicker map[string]TickerData) float64 {
	var rs []float64
	for _, td := range byTicker {
		if r := optionsPutCall(td); r > 0 {
			rs = append(rs, r)
		}
	}
	if len(rs) < putCallPeerMin {
		return 0
	}
	sort.Float64s(rs)
	mid := len(rs) / 2
	if len(rs)%2 == 1 {
		return rs[mid]
	}
	return (rs[mid-1] + rs[mid]) / 2
}

// parsePutCall reads the ratio out of the positioning sentence, which leads with
// it.
func parsePutCall(value string) (float64, bool) {
	const marker = "put/call open interest "
	i := strings.Index(value, marker)
	if i < 0 {
		return 0, false
	}
	rest := value[i+len(marker):]
	if j := strings.IndexFunc(rest, func(r rune) bool {
		return r != '.' && (r < '0' || r > '9')
	}); j >= 0 {
		rest = rest[:j]
	}
	r, err := strconv.ParseFloat(rest, 64)
	if err != nil {
		return 0, false
	}
	return r, true
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
