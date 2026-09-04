package orchestrator

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// Stage 0.5 — the universe-wide quant pre-screen.
//
// The scouts used to receive a bare list of ~99 tickers and were asked to name
// the best swing setups in it. They have no prices, no search on the HTTP
// engine, and no memory of the last quarter, so the only thing they could rank
// on was familiarity: the same mega-caps were nominated run after run, and the
// "recent breakout" justifying each one was invented. Computing the ranking
// here — from the same Yahoo bars Stage 1.5 already uses — turns screening into
// what it should have been: the model justifies and filters a data-derived
// ranking instead of conjuring one.
//
// Nothing here calls a model. It is arithmetic over daily bars.

// PrescreenParams records the knobs a pre-screen ran with. It is persisted with
// the rows so a run's ranking can be reproduced from its own artifact.
type PrescreenParams struct {
	TopPerIndex int `json:"top_per_index"`
	// PullbackPerIndex and BasePerIndex size the two archetype sections the
	// scout table gained alongside the top-of-ranking one. They are separate
	// knobs because the sections answer different questions and a run may want
	// more of one than the other.
	PullbackPerIndex int     `json:"pullback_per_index"`
	BasePerIndex     int     `json:"base_per_index"`
	BottomPerIndex   int     `json:"bottom_per_index"`
	ADVMinUSD        float64 `json:"adv_min_usd"`
	MinBars          int     `json:"min_bars"`
	VolTrendFlag     float64 `json:"vol_trend_flag"`
	Formula          string  `json:"formula"`
}

const (
	// prescreenBottomPerIndex is how many of each index's weakest names are put
	// in front of the scout as short candidates. The book has been almost
	// entirely long; the bottom of the ranking is where a defensible short comes
	// from, and it costs nothing to show.
	prescreenBottomPerIndex = 5
	// prescreenPullbackPerIndex and prescreenBasePerIndex size the two new
	// archetype sections. Smaller than the continuation table because both are
	// filtered shapes: an index rarely holds fifteen genuine pullbacks, and
	// padding the section with rows that only just clear the filter would undo
	// the point of having one.
	prescreenPullbackPerIndex = 8
	prescreenBasePerIndex     = 5
	// prescreenMinBars is the shortest history that still supports the 12-1
	// momentum term at all. Below it the composite is noise.
	prescreenMinBars = 60
	// prescreenVolTrendFlag marks names whose 20d realized vol is running far
	// above their 60d: the level arithmetic downstream assumes σ is stable.
	prescreenVolTrendFlag = 1.5
	// prescreenFormula is written into the artifact so a row's Score is legible
	// without reading this file.
	prescreenFormula = "0.5·z(mom12-1) + z(ret63d) − 0.5·strZ − 0.35·stretch21, each penalty only when that move runs with the composite; mom/ret63d z-scored within index, strZ and stretch21 already per-name sigma units"
	// defaultADVMinUSD is the tradeable-size floor, in 20-day average dollar
	// volume. A swing position sized off a real account cannot be entered or
	// exited in a name that trades a few million a day, so such names are
	// dropped before a model ever sees them. Config key: risk.adv_min_usd.
	defaultADVMinUSD = 20e6
	// stretch21Weight is what a full 21-day sigma of extension costs the
	// composite. Half the 5-day term's weight: a month of steady gains is the
	// trend this system trades, so it is a milder warning than the same
	// distance covered in a week, but AMGN's +1.2 still costs 0.42 of a point
	// where the 5-day term charged 0.095.
	stretch21Weight = 0.35
)

// Setup archetypes. The pre-screen's composite is built from trailing returns,
// so its top is by construction the names that have already run — on 2026-09-04
// eight or nine of every index's top fifteen sat within 5% of their 52-week
// high, and the shortlist that came out of it was three longs at 0.993, 0.982
// and 1.000 of theirs. Ranking harder does not fix that; the ranking is honest
// about what it measures. What was missing is that no *other* shape was ever
// put in front of a model: the table showed the top 15 and the bottom 5, and
// the 187 rows in between — median 0.78-0.89 of their highs, which is where a
// pullback or a base lives — were printed as "(mid-ranked names omitted)".
//
// So the archetypes are filters, not new composites. Each one selects a shape;
// Trend still ranks within it. That keeps every candidate on one scale, which
// is what lets the merge compare a pullback against a continuation at all.
const (
	SetupContinuation = "continuation"
	SetupPullback     = "pullback"
	SetupBase         = "base"
)

const (
	// pullbackMinBars is a full year, because the band below is stated against
	// the 52-week high and a shorter history does not have one.
	pullbackMinBars = 252
	// pullbackVolTrendMax keeps the dip from being a volatility event. A
	// pullback is the trend resting; expanding realized vol means something
	// happened, and the levels a trade would be built on are not stable.
	pullbackVolTrendMax = 1.2
	// pullbackHighMin/Max bound how deep a long pullback may be. Above 0.95 the
	// name has not actually pulled back; below 0.80 the trend it is pulling
	// back within is in question.
	pullbackHighMin = 0.80
	pullbackHighMax = 0.95
	// pullbackShortHighMax is the mirror for a bearish pullback: a bounce is
	// only a fresh short if the name is genuinely in a downtrend, and being 15%
	// or more below the 52-week high is the available evidence of that. There
	// is no "already collapsed" test here because the archetype's own
	// counter-trend condition is one — a bearish pullback requires a *positive*
	// 21-day return, which is exactly the bounce a short wants to sell into
	// rather than the capitulation it does not.
	pullbackShortHighMax = 0.85

	// baseVolTrendMax is the defining term: 20-day realized vol running below
	// 90% of the 60-day is a name coiling. baseStretchMax holds it flat over the
	// month, and baseHighMin keeps it in the upper part of its own range so this
	// selects consolidation rather than a long decline that has stopped moving.
	baseVolTrendMax = 0.9
	baseStretchMax  = 0.5
	baseHighMin     = 0.75
)

func defaultPrescreenParams() PrescreenParams {
	return PrescreenParams{
		TopPerIndex:      15,
		PullbackPerIndex: prescreenPullbackPerIndex,
		BasePerIndex:     prescreenBasePerIndex,
		BottomPerIndex:   prescreenBottomPerIndex,
		ADVMinUSD:        defaultADVMinUSD,
		MinBars:          prescreenMinBars,
		VolTrendFlag:     prescreenVolTrendFlag,
		Formula:          prescreenFormula,
	}
}

// PrescreenRow is one constituent's computed read. Every constituent gets a
// row, including the ones that were excluded or could not be fetched — the
// artifact should account for the whole universe it claims to have screened.
type PrescreenRow struct {
	Ticker string `json:"ticker"`
	Name   string `json:"name"`
	Sector string `json:"sector"`
	Index  string `json:"index"`

	AsOf  string  `json:"as_of,omitempty"`
	Bars  int     `json:"bars,omitempty"`
	Close float64 `json:"close,omitempty"`

	Mom12_1  float64 `json:"mom_12_1"`
	Ret63d   float64 `json:"ret_63d"`
	Ret21d   float64 `json:"ret_21d"`
	Ret5d    float64 `json:"ret_5d"`
	RS63     float64 `json:"rs_63"` // Ret63d less the index benchmark's 63d return
	STRZ     float64 `json:"str_z"`
	VolYZ20  float64 `json:"vol_yz_20"`
	VolTrend float64 `json:"vol_trend"`
	Regime   string  `json:"regime,omitempty"`
	// ADV is the 20d average turnover converted to USD; ADVLocal is the same
	// figure in the listing's own currency, which is what Close is quoted in.
	// The field used to be named adv_usd and hold the local number.
	ADV            float64 `json:"adv_usd"`
	ADVLocal       float64 `json:"adv_local,omitempty"`
	Currency       string  `json:"currency,omitempty"`
	PriceTo52wHigh float64 `json:"price_to_52w_high"`

	// Stretch21 is the trailing 21-day return in units of its own 21-day sigma
	// (Ret21d / (SigmaDaily·sqrt(21))). It is the same idea as STRZ — how far
	// this name has just travelled, measured against how far it normally
	// travels — over a month rather than a week.
	//
	// The distinction matters because STRZ was the *only* extension term, and a
	// week is not long enough to see a stock get extended. On 2026-09-04 AMGN
	// sat at 0.993 of its 52-week high after +47% on 12-1 and +29% on the
	// quarter, but its 5-day return was +1.6%, so STRZ read +0.19 and docked the
	// composite 0.095 of a point out of +1.82. The Chief read that ranking and
	// wrote "extension risk is absent". Over 21 days the same name reads +1.2.
	Stretch21 float64 `json:"stretch_21"`

	// Setup is the trade shape this row qualifies as, assigned first-match-wins
	// by classifySetups so the scout's three tables are disjoint: "pullback",
	// "base", or "continuation". Excluded rows carry none.
	Setup string `json:"setup,omitempty"`

	// Score is the within-index composite. Excluded rows keep 0 and are never
	// ranked; read Excluded before reading Score.
	Score float64 `json:"score"`
	// Trend is the composite before the extension penalties — 0.5·z(mom12-1) +
	// z(ret63d). It is what ranks rows *within* an archetype, so that a
	// pullback and a continuation name are compared on the same scale, and it
	// is the sign the extension gates test against.
	Trend    float64  `json:"trend"`
	Excluded string   `json:"excluded,omitempty"`
	Flags    []string `json:"flags,omitempty"`
}

// Prescreen is Stage 0.5's artifact: the parameters, every constituent's row,
// and whatever went wrong fetching them.
type Prescreen struct {
	AsOf    string          `json:"as_of"`
	Indices []string        `json:"indices"`
	Params  PrescreenParams `json:"params"`
	Rows    []PrescreenRow  `json:"rows"`
	Errors  []string        `json:"errors,omitempty"`
}

// Row returns one constituent's row *within one index*. The scan is linear
// because the merge and the shortlist together look up a few dozen tickers
// against a few hundred rows; an index would be one more invariant to keep true
// for no gain.
//
// The index is part of the identity, not a filter of convenience. A name in two
// indices has two rows with two different composites, because the pre-screen
// standardises within each index — MU scored +3.73 in sp500 and +2.59 in nq100
// on 2026-09-01. Rows is sorted best-first, so a ticker-only scan returned
// whichever row scored *higher*, and meritScore negates the composite for a
// bearish nomination: the lookup handed every bullish dual-index nomination its
// best score and every bearish one its least negative. 35 of that run's 232
// rankable names sit in two indices, with gaps up to 1.14 in a merge whose whole
// shortlist spanned +1.79 to +3.73.
//
// An empty index falls back to the ticker-only scan, which is what single-stock
// mode has: a user-supplied ticker carries no index of its own.
func (p *Prescreen) Row(index, ticker string) (PrescreenRow, bool) {
	if p == nil {
		return PrescreenRow{}, false
	}
	t := strings.ToUpper(strings.TrimSpace(ticker))
	idx := strings.TrimSpace(index)
	for _, r := range p.Rows {
		if strings.ToUpper(r.Ticker) != t {
			continue
		}
		if idx == "" || r.Index == idx {
			return r, true
		}
	}
	return PrescreenRow{}, false
}

// Ranked returns one index's scorable rows, best first.
func (p *Prescreen) Ranked(index string) []PrescreenRow {
	if p == nil {
		return nil
	}
	var out []PrescreenRow
	for _, r := range p.Rows {
		if r.Index == index && r.Excluded == "" {
			out = append(out, r)
		}
	}
	return out
}

// Table renders the top and bottom of one index's ranking as the markdown table
// the scout screens from. Empty when the pre-screen produced nothing for the
// index, so the scout prompt degrades to the plain constituent list rather than
// carrying an empty heading.
func (p *Prescreen) Table(index string, params PrescreenParams) string {
	ranked := p.Ranked(index)
	if len(ranked) == 0 {
		return ""
	}

	shown := map[string]bool{}
	pick := func(setup string, n int) []PrescreenRow {
		var out []PrescreenRow
		for _, r := range ranked {
			if len(out) == n {
				break
			}
			if r.Setup != setup || shown[r.Ticker] {
				continue
			}
			shown[r.Ticker] = true
			out = append(out, r)
		}
		return out
	}

	var sb strings.Builder
	section := func(heading, blurb string, rows []PrescreenRow) {
		if len(rows) == 0 {
			return
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "**%s** — %s\n\n", heading, blurb)
		// `close` is in the listing's own currency (the ccy column) because that
		// is what an order is placed in; ADV is converted, because a floor
		// stated in dollars has to be met in dollars.
		sb.WriteString("| ticker | name | sector | close (as of) | ccy | score | trend | mom12-1 | 63d | 21d | 5d | str21 | RS63 | vol20 | volTrend | regime | ADV$M (USD) | p/52wH |\n")
		sb.WriteString("|---|---|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---:|---:|\n")
		for _, r := range rows {
			fmt.Fprintf(&sb, "| %s | %s | %s | %.2f (%s) | %s | %+.2f | %+.2f | %s | %s | %s | %s | %+.2f | %s | %.0f%% | %.2f | %s | %.0f | %.2f |\n",
				r.Ticker, r.Name, r.Sector, r.Close, r.AsOf, r.Currency, r.Score, r.Trend,
				pctStr(r.Mom12_1), pctStr(r.Ret63d), pctStr(r.Ret21d), pctStr(r.Ret5d), r.Stretch21,
				pctStr(r.RS63), r.VolYZ20*100, r.VolTrend, r.Regime, r.ADV/1e6, r.PriceTo52wHigh)
		}
	}

	section("Continuation", "strongest composites in the index — trends that are still running. `str21` is how many 21-day sigmas of that run are already behind it; a high `p/52wH` with a high `str21` is where trend-followers get filled last",
		pick(SetupContinuation, positiveOr(params.TopPerIndex, 1)))
	section("Pullback", "the composite and the last month disagree: an uptrend currently dipping, or a downtrend currently bouncing. These are counter-move entries into an established trend, and they are the rows the old single table never showed you",
		pick(SetupPullback, params.PullbackPerIndex))
	section("Base", "volatility contracting (`volTrend` below 0.9) while price goes nowhere. No directional claim — the shape says a range is tightening, and you supply the direction from the rest of the row",
		pick(SetupBase, params.BasePerIndex))

	// The weakest composites in the index, whatever shape they are. This is a
	// different question from the archetypes above — not "what setup is this"
	// but "what is this index's bottom" — so it is drawn from the whole ranking
	// and only skips names an earlier section already listed.
	if n := params.BottomPerIndex; n > 0 {
		var tail []PrescreenRow
		for i := len(ranked) - 1; i >= 0 && len(tail) < n; i-- {
			if shown[ranked[i].Ticker] {
				continue
			}
			shown[ranked[i].Ticker] = true
			tail = append(tail, ranked[i])
		}
		// Read back to worst-last so the section reads in ranking order.
		for i, j := 0, len(tail)-1; i < j; i, j = i+1, j-1 {
			tail[i], tail[j] = tail[j], tail[i]
		}
		section("Weakest", fmt.Sprintf("the bottom of the same %d-name ranking. A short candidate comes from here, but a name that has already collapsed (`5d` large and negative) is a bounce risk, not a fresh short — for that, read the bearish half of the Pullback table", len(ranked)), tail)
	}

	if sb.Len() == 0 {
		return ""
	}
	fmt.Fprintf(&sb, "\n*%d of this index's %d rankable names are not shown; they sat between these sections.*\n", len(ranked)-len(shown), len(ranked))
	return sb.String()
}

// positiveOr floors a configured section size at a usable minimum, so a zero or
// negative setting shows one row rather than silently emptying the section.
func positiveOr(n, min int) int {
	if n < min {
		return min
	}
	return n
}

func pctStr(x float64) string { return fmt.Sprintf("%+.1f%%", x*100) }

// newPrescreenRow projects one constituent's computed metrics onto the columns
// the ranking and the scout table use. benchRet63 is the index benchmark's own
// 63d return, which turns a raw 63d move into relative strength.
func newPrescreenRow(c model.Constituent, m quant.Metrics, benchRet63 float64, params PrescreenParams) PrescreenRow {
	r := PrescreenRow{
		Ticker:         strings.ToUpper(c.Ticker),
		Name:           c.Name,
		Sector:         c.Sector,
		Index:          c.Index,
		AsOf:           m.AsOf,
		Bars:           m.Bars,
		Close:          m.LastClose,
		Mom12_1:        m.Mom12_1,
		Ret63d:         m.Ret63d,
		Ret21d:         m.Ret21d,
		Ret5d:          m.Ret5d,
		RS63:           m.Ret63d - benchRet63,
		STRZ:           m.STRZScore,
		VolYZ20:        m.VolYZ20,
		VolTrend:       m.VolTrend,
		Regime:         m.Regime,
		ADV:            m.AvgDollarVol20USD,
		ADVLocal:       m.AvgDollarVol20,
		Currency:       m.Currency,
		PriceTo52wHigh: m.PriceTo52wHigh,
		Stretch21:      m.Stretch21(),
	}
	if params.VolTrendFlag > 0 && m.VolTrend > params.VolTrendFlag {
		r.Flags = append(r.Flags, fmt.Sprintf("vol expanding (20d/60d %.2f)", m.VolTrend))
	}
	return r
}

// applyPrescreenExclusions marks the rows that must not be ranked at all. These
// are hard: a name that cannot be traded in size, or whose history is too short
// for the momentum term to mean anything, is not a candidate however it scores.
func applyPrescreenExclusions(rows []PrescreenRow, params PrescreenParams) {
	for i := range rows {
		if rows[i].Excluded != "" {
			continue
		}
		switch {
		case params.MinBars > 0 && rows[i].Bars < params.MinBars:
			rows[i].Excluded = fmt.Sprintf("insufficient history (%d bars, need %d)", rows[i].Bars, params.MinBars)
		case params.ADVMinUSD > 0 && rows[i].ADVLocal > 0 && rows[i].ADV <= 0:
			// The turnover is known but its currency is not convertible, so the
			// floor cannot be applied. Excluding is the conservative reading: a
			// name whose tradeable size cannot be established is not a candidate.
			rows[i].Excluded = fmt.Sprintf("liquidity not verifiable (20d ADV %.1fM %s, no USD rate)",
				rows[i].ADVLocal/1e6, rows[i].Currency)
		case params.ADVMinUSD > 0 && rows[i].ADV < params.ADVMinUSD:
			rows[i].Excluded = fmt.Sprintf("illiquid (20d ADV $%.1fM, floor $%.0fM)",
				rows[i].ADV/1e6, params.ADVMinUSD/1e6)
		}
	}
}

// scorePrescreen fills in Score for every non-excluded row, z-scoring each term
// within its own index.
//
// Within-index standardisation is the point: eu50 in a flat quarter would never
// out-momentum nq100 in absolute terms, and a globally-ranked screen would hand
// every slot to one index. Each index nominates its own leaders and laggards,
// and the merge decides how many of each survive.
//
// The composite is a *signed long ranking*: high means a strong long, low means
// a strong short. Excluded rows are left at 0 and, more importantly, are left
// out of the mean and standard deviation — an illiquid microcap's 400% year
// would otherwise flatten every real name's z-score toward zero.
//
// There is no separate relative-strength term, and there never really was one.
// The formula read `z(mom) + 0.5·z(ret63d) + 0.5·z(rs63)`, but RS63 is
// `Ret63d − benchRet63` with a benchmark fetched *once per index*, and these
// z-scores are taken *within* an index — so every member had the same constant
// subtracted, and subtracting a constant leaves a z-score exactly unchanged.
// `z(rs63) ≡ z(ret63d)` identically: the 2026-09-01 artifact has `ret_63d − rs_63`
// taking one distinct value per index, to nine decimals. The third signal was the
// second signal counted twice, so the real weight on the 63-day return was 1.0
// rather than the documented 0.5. Standardising within the index *is* the
// relative-strength adjustment; RS63 remains a displayed column because it reads
// more directly than a z-score, but it cannot earn its own term here.
func scorePrescreen(rows []PrescreenRow) {
	byIndex := map[string][]int{}
	var order []string
	for i := range rows {
		if rows[i].Excluded != "" {
			rows[i].Score = 0
			continue
		}
		if _, ok := byIndex[rows[i].Index]; !ok {
			order = append(order, rows[i].Index)
		}
		byIndex[rows[i].Index] = append(byIndex[rows[i].Index], i)
	}

	for _, idx := range order {
		members := byIndex[idx]
		pick := func(f func(PrescreenRow) float64) []float64 {
			out := make([]float64, len(members))
			for k, i := range members {
				out[k] = f(rows[i])
			}
			return out
		}
		zMom := zscores(pick(func(r PrescreenRow) float64 { return r.Mom12_1 }))
		zR63 := zscores(pick(func(r PrescreenRow) float64 { return r.Ret63d }))

		for k, i := range members {
			// The 63-day return leads and the 12-1 momentum supports it, not the
			// other way round.
			//
			// This system holds for 5–20 sessions. A 12-month trend measured to a
			// month ago describes a name's last year; it is the classic
			// cross-sectional momentum factor, and it is right about the next
			// twelve months rather than the next fortnight. Weighted 1.0 against
			// 0.5 it decided the shortlist on its own — on 2026-09-03 the sp500
			// scout read its own ranked table and wrote that MU at rank 1 and
			// INTC at rank 2 "have already broken their trend; the score is stale
			// off the 12m return, not a fresh setup. Avoid both directions." The
			// merit sort took both anyway, at ranks 1 and 3, and dropped the four
			// health-care longs the same scout had nominated.
			//
			// Reversing the weights on the same run's table lifts MRK, AMGN and
			// REGN into the top twelve and drops the two extremes down it. The
			// short-term reversal penalty below is unchanged: it is what keeps a
			// heavier 63-day term from simply buying the most extended name.
			trend := 0.5*zMom[k] + zR63[k]
			rows[i].Trend = trend
			score := trend
			// Short-term reversal only argues against the trend when the recent
			// move ran *with* it: a name that has just spiked on top of an
			// uptrend is the classic thing that gives the spike back, while an
			// uptrend that just dipped is a pullback entry, not a warning. So
			// the penalty applies when the recent move shares the sign of the
			// trend, and is symmetric — a crashed name is a poor short for the
			// same reason an extended one is a poor long.
			//
			// The term is STRZ itself, not a cross-sectional z-score of it. STRZ
			// is *already* a z-score — quant.Compute standardises the trailing 5d
			// return against that name's own one-year distribution of 5d returns —
			// so it is unit-free and on the same scale as the terms above.
			// Re-standardising it within the index broke the sign the gate had
			// just tested: in a broad rally the index mean of STRZ is positive, so
			// a mildly extended name has a *negative* cross-sectional z, and
			// `score -= 0.5·z` paid it a bonus for extending. With four names on
			// an identical +40% trend, one that had run up 0.3σ scored +0.39
			// against +0.00 for one that had not moved at all — the reversal
			// penalty rewarding the extension it exists to punish.
			//
			// The sign is taken from `trend`, the composite itself, and not from
			// Mom12_1 alone. Keying it on the 12-month term let the most
			// extended shape in the table through untouched: a name whose last
			// year was poor but whose last quarter was vertical has a negative
			// Mom12_1 and a positive composite, so sameSign was false and no
			// penalty applied at all. On 2026-09-04 CRM ran +37.0% in 21 days
			// and +4.9% in 5, sat at 0.986 of its 52-week high on 60%
			// annualized vol, and was docked exactly nothing on a +1.72
			// composite — while REGN, up a third of that in 21 days, paid 0.386.
			if sameSign(rows[i].STRZ, trend) {
				score -= 0.5 * rows[i].STRZ
			}
			// The same test over 21 days. STRZ alone gave the composite a
			// five-day memory, which is shorter than the thing it is trying to
			// measure: getting extended is a move that takes weeks, and a name
			// can sit at its high for a month without ever printing a spiky
			// week. Weighted below the 5-day term because a month of steady
			// gains is a weaker warning than a sudden one — it is the trend
			// this system trades — but not by so much that it can be ignored.
			if sameSign(rows[i].Stretch21, trend) {
				score -= stretch21Weight * rows[i].Stretch21
			}
			// Both penalties discount the trend. Neither may reverse it.
			//
			// They are symmetric by design — a crashed name is a poor short for
			// the same reason an extended one is a poor long — and symmetric
			// means that for a *negative* composite both terms add. Unclamped
			// that does not stop at "poor short": it carries the name across
			// zero and ranks it as a strong long. On the 2026-09-04 eu50 table
			// ENEL.MI held a −0.47 composite and a −2.22 str21, and the two
			// bonuses lifted it to +1.42 — first of forty-seven names, a buy
			// recommendation generated entirely by having fallen.
			//
			// Neutral is the floor. "This trend is less attractive than it
			// looks" is the claim these terms are entitled to make; "take the
			// other side" is not.
			if trend > 0 && score < 0 {
				score = 0
			}
			if trend < 0 && score > 0 {
				score = 0
			}
			rows[i].Score = score
		}
	}
	// Classify in the same step that scores. The archetype tests read Trend, so
	// they cannot run earlier, and letting them run *later* made Setup a field
	// that was sometimes populated and sometimes not depending on which caller
	// you came through — with the only symptom being a scout table silently
	// missing its Continuation section. A row that has a Score has a Setup.
	classifySetups(rows)
}

// classifySetups labels every scorable row with the trade shape it qualifies
// as. It is the tail of scorePrescreen rather than a step of its own, because
// the tests read Trend and every caller that has a score needs a label.
//
// First match wins, pullback before base before continuation, so the three
// tables the scout reads are disjoint and their counts mean something. The
// order is by how specific the shape is: a pullback is a named entry, a base is
// a named condition, and continuation is what is left — which is also today's
// behaviour, so a row that matches nothing new keeps exactly the label the old
// single table would have given it.
func classifySetups(rows []PrescreenRow) {
	for i := range rows {
		if rows[i].Excluded != "" {
			rows[i].Setup = ""
			continue
		}
		switch {
		case isPullback(rows[i]):
			rows[i].Setup = SetupPullback
		case isBase(rows[i]):
			rows[i].Setup = SetupBase
		default:
			rows[i].Setup = SetupContinuation
		}
	}
}

// isPullback selects a trend that is currently resting against itself: the
// composite says one direction and the last month says the other. A zero trend
// has no direction to pull back from, and a zero 21-day return is not a
// counter-move, so both fall through to the next archetype.
func isPullback(r PrescreenRow) bool {
	if r.Bars < pullbackMinBars || r.Trend == 0 || r.Ret21d == 0 {
		return false
	}
	// The counter-trend condition. sameSign is the wrong helper here: it is
	// this test's negation, and writing it as such says so.
	if sameSign(r.Ret21d, r.Trend) {
		return false
	}
	if r.VolTrend > pullbackVolTrendMax {
		return false
	}
	if r.Trend > 0 {
		return r.PriceTo52wHigh >= pullbackHighMin && r.PriceTo52wHigh <= pullbackHighMax
	}
	return r.PriceTo52wHigh <= pullbackShortHighMax
}

// isBase selects volatility contraction — a name whose recent range is
// narrowing while it goes nowhere. Unlike a pullback this makes no claim about
// direction, which is why it is the one archetype that cannot be extended: a
// name that has just run is not flat over the month by construction.
func isBase(r PrescreenRow) bool {
	if r.VolTrend <= 0 || r.VolTrend >= baseVolTrendMax {
		return false
	}
	if math.Abs(r.Stretch21) >= baseStretchMax {
		return false
	}
	if r.Regime == "trending" {
		return false
	}
	return r.PriceTo52wHigh >= baseHighMin
}

// sortPrescreenRows orders rows best-composite-first, with excluded rows last
// and ties broken by ticker so the artifact is stable across runs.
func sortPrescreenRows(rows []PrescreenRow) []PrescreenRow {
	out := make([]PrescreenRow, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool {
		ei, ej := out[i].Excluded != "", out[j].Excluded != ""
		if ei != ej {
			return ej
		}
		if ei && ej {
			return out[i].Ticker < out[j].Ticker
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Ticker < out[j].Ticker
	})
	return out
}

// winsorFrac is the tail clipped from each end before standardising. Cross-
// sectional factor scores are routinely dominated by one extreme value: on
// 2026-08-28 Micron's 12-1 momentum read +644% against an S&P sample whose
// next-best was +74%, which scored it +7.0 — three times the runner-up — while
// inflating the standard deviation enough to squash every other name toward
// zero. Clipping the top and bottom 2% keeps the outlier ranked first, where it
// belongs, without letting it set the scale for the other ninety-odd names.
const winsorFrac = 0.02

// zscores standardises xs, winsorised at winsorFrac. A degenerate sample (fewer
// than two values, or zero spread) returns zeros rather than NaNs: "no
// information" must not poison the composite of every name in the index.
func zscores(xs []float64) []float64 {
	out := make([]float64, len(xs))
	if len(xs) < 2 {
		return out
	}
	xs = winsorize(xs, winsorFrac)
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	var ss float64
	for _, x := range xs {
		d := x - mean
		ss += d * d
	}
	sd := math.Sqrt(ss / float64(len(xs)-1))
	// "No spread" has to be judged with a tolerance, not against exact zero.
	// Summing six copies of 0.4 yields 2.4 and a mean of 0.39999999999999997,
	// so every deviation is ~5.6e-17 instead of 0 and sd lands at 6.1e-17 —
	// non-zero, straight past an `sd == 0` guard, and every member of the index
	// then gets the *same* meaningless z of 0.913 rather than 0. The guard is
	// already trying to catch exactly this; it just could not see it.
	//
	// The tolerance is relative to the mean because these are returns: an index
	// of large numbers carries proportionally larger residue.
	if math.IsNaN(sd) || math.IsInf(sd, 0) || sd <= 1e-12*math.Max(1, math.Abs(mean)) {
		return out
	}
	for i, x := range xs {
		out[i] = (x - mean) / sd
	}
	return out
}

// winsorize returns xs with values outside the [frac, 1−frac] quantiles pulled
// back to those bounds. Order is preserved; the input is not modified. With
// fewer than five values there is no tail to trim and xs is returned as-is.
func winsorize(xs []float64, frac float64) []float64 {
	n := len(xs)
	if n < 5 || frac <= 0 {
		return xs
	}
	sorted := make([]float64, n)
	copy(sorted, xs)
	sort.Float64s(sorted)
	k := int(frac * float64(n))
	if k < 1 {
		k = 1
	}
	lo, hi := sorted[k], sorted[n-1-k]
	if lo > hi {
		return xs
	}
	out := make([]float64, n)
	for i, x := range xs {
		switch {
		case x < lo:
			out[i] = lo
		case x > hi:
			out[i] = hi
		default:
			out[i] = x
		}
	}
	return out
}

// sameSign reports whether a and b point the same way, treating zero as "no
// direction" so a flat term never triggers the reversal penalty.
func sameSign(a, b float64) bool {
	return (a > 0 && b > 0) || (a < 0 && b < 0)
}

// runPrescreen fetches daily history for every constituent of the selected
// indices, computes the metrics in-process and ranks them. Fetch failures
// degrade to an excluded row and a recorded error; they never abort the run.
//
// The price series are deliberately not written into runs/ — that would be a
// few hundred JSON files per run for names that never reach the shortlist. They
// land in the shared data cache, where Stage 1.5 reads the dozen it needs back
// for free.
func runPrescreen(ctx context.Context, ch chan<- Event, yc marketdata.PriceSource, fx *marketdata.FXRates, uni *universe.Universe, indices []string, params PrescreenParams) *Prescreen {
	ps := &Prescreen{Indices: indices, Params: params}

	// One benchmark fetch per distinct symbol; several indices, and several
	// exchanges within asia100, may share one.
	benchRet63 := map[string]float64{}
	benchFor := func(indexKey, ticker string) float64 {
		sym := universe.BenchmarkFor(indexKey, ticker)
		if v, ok := benchRet63[sym]; ok {
			return v
		}
		v := 0.0
		if s, err := yc.History(ctx, sym); err != nil {
			ps.Errors = append(ps.Errors, fmt.Sprintf("benchmark %s: %v", sym, err))
		} else {
			v = quant.Compute(s, nil).Ret63d
		}
		benchRet63[sym] = v
		return v
	}

	// One batched warm-up before the per-ticker loop below.
	//
	// That loop is one HTTP request per constituent, and a universe-wide
	// pre-screen therefore fires a few hundred of them per run — which is the
	// shape that got this host answered with 429 on every Yahoo endpoint.
	// Alpaca serves many symbols per request, so warming the cache first turns
	// the US half of the loop into disk reads and leaves Yahoo only the foreign
	// names it alone can answer. Ineligible symbols and an unconfigured Alpaca
	// both prefetch nothing, and the loop below is unchanged either way.
	var warm []string
	for _, idx := range indices {
		for _, c := range uni.Constituents(idx) {
			warm = append(warm, c.Ticker)
		}
	}
	if n := yc.Prefetch(ctx, warm); n > 0 {
		log(ch, fmt.Sprintf("pre-screen: %d of %d symbols pre-fetched in batch", n, len(warm)))
	}

	var rows []PrescreenRow
	for _, idx := range indices {
		cs := uni.Constituents(idx)
		if len(cs) == 0 {
			continue
		}
		fetched, failed := 0, 0
		for _, c := range cs {
			if err := ctx.Err(); err != nil {
				ps.Errors = append(ps.Errors, fmt.Sprintf("pre-screen cancelled after %d of %s: %v", fetched, idx, err))
				break
			}
			s, err := yc.History(ctx, c.Ticker)
			if err != nil {
				failed++
				ps.Errors = append(ps.Errors, fmt.Sprintf("%s: %v", c.Ticker, err))
				rows = append(rows, PrescreenRow{
					Ticker: strings.ToUpper(c.Ticker), Name: c.Name, Sector: c.Sector,
					Index: idx, Excluded: "no price history",
				})
				continue
			}
			fetched++
			m := quant.Compute(s, nil)
			m.ApplyFX(fxFor(ctx, fx, c.Ticker))
			// RS63 is a displayed column, not a term in the composite (see
			// scorePrescreen), so measuring each name against its own market
			// changes what the scout reads without moving the ranking.
			r := newPrescreenRow(c, m, benchFor(idx, c.Ticker), params)
			if r.AsOf > ps.AsOf {
				ps.AsOf = r.AsOf
			}
			rows = append(rows, r)
		}
		log(ch, fmt.Sprintf("pre-screen %s: %d of %d names priced%s", idx, fetched, len(cs),
			plural(failed, ", %d unavailable")))
	}
	ps.Errors = append(ps.Errors, fx.Failures()...)

	applyPrescreenExclusions(rows, params)
	scorePrescreen(rows)
	ps.Rows = sortPrescreenRows(rows)
	return ps
}

// fxFor resolves one ticker's currency and USD rate in the argument order
// quant.Metrics.ApplyFX wants. A failed lookup yields a zero rate, which ApplyFX
// records as "not computable" rather than silently treating the local figure as
// dollars.
func fxFor(ctx context.Context, fx *marketdata.FXRates, ticker string) (string, float64) {
	rate, code, err := fx.ToUSD(ctx, ticker)
	if err != nil {
		return code, 0
	}
	return code, rate
}

// plural renders an optional count clause, or nothing when the count is zero.
func plural(n int, format string) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(format, n)
}

// meritScore aligns a candidate's pre-screen composite with the direction the
// scout nominated it in. The composite is a signed long ranking, so a bearish
// nomination is strongest exactly where the composite is most negative.
//
// The row is looked up under the candidate's *own* index, because the composite
// is a within-index z-score and a dual-listed name has one per index. See
// Prescreen.Row.
//
// A name with no computed row — an off-table pick, or one the pre-screen could
// not price — scores 0: neither confirmed nor contradicted by the data, which
// puts it between the two and lets the scout's own reasoning stand on its own.
// A neutral nomination scores 0 for the same reason: the composite is
// directional, and there is no direction to align it with.
//
// On top of that sits the one thing the screening stage knows that the
// pre-screen does not: whether more than one scout wanted the name.
//
//   - Agreement pays meritAgreementBonus per extra nomination. Two scouts
//     reaching the same name from different index tables is independent
//     evidence, and the composite cannot contain it — it is computed from
//     prices, and both scouts read the same prices. On 2026-09-03 REGN and AMGN
//     were the only two names any pair of scouts agreed on, and the merit sort,
//     ranking on the composite alone, dropped both.
//   - A contested name is charged meritContestedPenalty. Two scouts nominating
//     opposite directions is not a signal with a sign; it is the screening stage
//     saying it does not know. That belongs below an uncontested read of similar
//     strength, not above it on whichever bias happened to be collected first.
//
// Both are in z-score units, so they are on the composite's own scale: one extra
// scout is worth about a third of a standard deviation of relative strength, and
// a contradiction costs about two thirds. Neither is large enough to lift a name
// the data argues against, which is the point — this breaks ties between names
// the composite has already ranked closely, and this run's cut ran through
// twelve nominations inside 1.1 z of each other.
const (
	meritAgreementBonus   = 0.35
	meritContestedPenalty = 0.65
)

func meritScore(ps *Prescreen, c model.Candidate) float64 {
	base := meritComposite(ps, c)
	if n := c.Nominations; n > 1 {
		base += float64(n-1) * meritAgreementBonus
	}
	if len(c.Contested) > 0 {
		base -= meritContestedPenalty
	}
	return base
}

// meritComposite is the direction-aligned pre-screen composite, before the
// screening stage's own agreement is counted.
func meritComposite(ps *Prescreen, c model.Candidate) float64 {
	r, ok := ps.Row(c.Index, c.Ticker)
	if !ok || r.Excluded != "" {
		return 0
	}
	switch c.Bias {
	case model.BiasBullish:
		return r.Score
	case model.BiasBearish:
		return -r.Score
	default:
		return 0
	}
}

// orUnknown renders an unset setup label for the run log. A candidate has no
// setup when the pre-screen could not price it — an off-table scout pick, or a
// name whose history failed to fetch — which is a different thing from being
// classified as continuation, and the log should not conflate them.
func orUnknown(setup string) string {
	if setup == "" {
		return "no pre-screen row"
	}
	return setup
}
