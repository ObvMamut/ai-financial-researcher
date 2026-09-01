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
	TopPerIndex    int     `json:"top_per_index"`
	BottomPerIndex int     `json:"bottom_per_index"`
	ADVMinUSD      float64 `json:"adv_min_usd"`
	MinBars        int     `json:"min_bars"`
	VolTrendFlag   float64 `json:"vol_trend_flag"`
	Formula        string  `json:"formula"`
}

const (
	// prescreenBottomPerIndex is how many of each index's weakest names are put
	// in front of the scout as short candidates. The book has been almost
	// entirely long; the bottom of the ranking is where a defensible short comes
	// from, and it costs nothing to show.
	prescreenBottomPerIndex = 5
	// prescreenMinBars is the shortest history that still supports the 12-1
	// momentum term at all. Below it the composite is noise.
	prescreenMinBars = 60
	// prescreenVolTrendFlag marks names whose 20d realized vol is running far
	// above their 60d: the level arithmetic downstream assumes σ is stable.
	prescreenVolTrendFlag = 1.5
	// prescreenFormula is written into the artifact so a row's Score is legible
	// without reading this file.
	prescreenFormula = "z(mom12-1) + 0.5·z(ret63d) − 0.5·z(strZ) when the recent move runs with the trend; z-scores within index"
	// defaultADVMinUSD is the tradeable-size floor, in 20-day average dollar
	// volume. A swing position sized off a real account cannot be entered or
	// exited in a name that trades a few million a day, so such names are
	// dropped before a model ever sees them. Config key: risk.adv_min_usd.
	defaultADVMinUSD = 20e6
)

func defaultPrescreenParams() PrescreenParams {
	return PrescreenParams{
		TopPerIndex:    15,
		BottomPerIndex: prescreenBottomPerIndex,
		ADVMinUSD:      defaultADVMinUSD,
		MinBars:        prescreenMinBars,
		VolTrendFlag:   prescreenVolTrendFlag,
		Formula:        prescreenFormula,
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

	// Score is the within-index composite. Excluded rows keep 0 and are never
	// ranked; read Excluded before reading Score.
	Score    float64  `json:"score"`
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

// Row returns one constituent's row. The scan is linear because the merge and
// the shortlist together look up a few dozen tickers against a few hundred
// rows; an index would be one more invariant to keep true for no gain.
func (p *Prescreen) Row(ticker string) (PrescreenRow, bool) {
	if p == nil {
		return PrescreenRow{}, false
	}
	t := strings.ToUpper(strings.TrimSpace(ticker))
	for _, r := range p.Rows {
		if strings.ToUpper(r.Ticker) == t {
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
func (p *Prescreen) Table(index string, top, bottom int) string {
	ranked := p.Ranked(index)
	if len(ranked) == 0 {
		return ""
	}
	if top <= 0 {
		top = 1
	}
	if bottom < 0 {
		bottom = 0
	}

	head := ranked
	if len(head) > top {
		head = head[:top]
	}
	var tail []PrescreenRow
	if n := len(ranked); bottom > 0 && n > len(head) {
		start := n - bottom
		if start < len(head) {
			start = len(head)
		}
		tail = ranked[start:]
	}

	var sb strings.Builder
	// `close` is in the listing's own currency (the ccy column) because that is
	// what an order is placed in; ADV is converted, because a floor stated in
	// dollars has to be met in dollars.
	sb.WriteString("| rank | ticker | name | sector | close (as of) | ccy | score | mom12-1 | 63d | 21d | 5d | RS63 | vol20 | volTrend | regime | ADV$M (USD) | p/52wH |\n")
	sb.WriteString("|---:|---|---|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|---:|---:|\n")
	writeRows := func(rows []PrescreenRow, offset int) {
		for i, r := range rows {
			sb.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %.2f (%s) | %s | %+.2f | %s | %s | %s | %s | %s | %.0f%% | %.2f | %s | %.0f | %.2f |\n",
				offset+i+1, r.Ticker, r.Name, r.Sector, r.Close, r.AsOf, r.Currency, r.Score,
				pctStr(r.Mom12_1), pctStr(r.Ret63d), pctStr(r.Ret21d), pctStr(r.Ret5d), pctStr(r.RS63),
				r.VolYZ20*100, r.VolTrend, r.Regime, r.ADV/1e6, r.PriceTo52wHigh))
		}
	}
	writeRows(head, 0)
	if len(tail) > 0 {
		sb.WriteString(fmt.Sprintf("| … | *(%d mid-ranked names omitted)* | | | | | | | | | | | | | | | |\n",
			len(ranked)-len(head)-len(tail)))
		writeRows(tail, len(ranked)-len(tail))
	}
	return sb.String()
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
		zSTR := zscores(pick(func(r PrescreenRow) float64 { return r.STRZ }))

		for k, i := range members {
			score := zMom[k] + 0.5*zR63[k]
			// Short-term reversal only argues against the trend when the recent
			// move ran *with* it: a name that has just spiked on top of an
			// uptrend is the classic thing that gives the spike back, while an
			// uptrend that just dipped is a pullback entry, not a warning. So
			// the penalty applies when the 5d move shares the sign of the 12-1
			// trend, and is symmetric — a crashed name is a poor short for the
			// same reason an extended one is a poor long.
			if sameSign(rows[i].STRZ, rows[i].Mom12_1) {
				score -= 0.5 * zSTR[k]
			}
			rows[i].Score = score
		}
	}
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
	if sd == 0 || math.IsNaN(sd) || math.IsInf(sd, 0) {
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
func runPrescreen(ctx context.Context, ch chan<- Event, yc *marketdata.YahooClient, fx *marketdata.FXRates, uni *universe.Universe, indices []string, params PrescreenParams) *Prescreen {
	ps := &Prescreen{Indices: indices, Params: params}

	// One benchmark fetch per distinct symbol; several indices may share one.
	benchRet63 := map[string]float64{}
	benchFor := func(indexKey string) float64 {
		sym := universe.BenchmarkSymbol(indexKey)
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

	var rows []PrescreenRow
	for _, idx := range indices {
		cs := uni.Constituents(idx)
		if len(cs) == 0 {
			continue
		}
		bench := benchFor(idx)
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
			r := newPrescreenRow(c, m, bench, params)
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
// A name with no computed row — an off-table pick, or one the pre-screen could
// not price — scores 0: neither confirmed nor contradicted by the data, which
// puts it between the two and lets the scout's own reasoning stand on its own.
// A neutral nomination scores 0 for the same reason: the composite is
// directional, and there is no direction to align it with.
func meritScore(ps *Prescreen, c model.Candidate) float64 {
	r, ok := ps.Row(c.Ticker)
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
