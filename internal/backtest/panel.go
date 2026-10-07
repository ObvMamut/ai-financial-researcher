// Package backtest is the lab: a point-in-time weekly replay of the pre-screen
// over every universe constituent, scored against what the market did next.
//
// It exists because the live record cannot decide anything soon — a 50bp
// fortnightly edge needs ~550 independent calls, and a run ships five. Here one
// replay yields ~55,000 (name, week) observations, each computed by the
// *shipping* code: quant.Compute on a series cut at the rebalance date, then
// orchestrator.NewPrescreenRow / ScorePrescreen on that date's cross-section.
// Nothing in this package calls a model, and nothing reads a bar after the
// date it is scoring (see TestSignalsIgnoreBarsAfterDate).
//
// The specification it was ported from is the scratch Python study in
// docs/research/2026-09-23-evidence/backtest/; docs/workflow/backtest.md is the
// behavioural source of truth and carries the pre-registered signal tests.
package backtest

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/orchestrator"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// numHorizons is len(Horizons), as a constant for the array types below.
const numHorizons = 5

// Horizons are the forward windows, in sessions, every signal is scored over.
// 21 and 63 are appended after the original 5/10/15, so indices 0..2 keep their
// meaning everywhere (bookHorizon, barrierHorizon, the C-series' h10).
var Horizons = [numHorizons]int{5, 10, 15, 21, 63}

// Signal identifiers. Every one is oriented so that a *higher* value is a
// bullish reading, which is what makes a positive rank IC mean "works".
const (
	SigScore       = iota // the shipped composite (orchestrator.ScorePrescreen)
	SigTrend              // the composite before its extension penalties
	SigMom12_1            // 12-1 momentum
	SigRet63              // 63-session return
	SigSTRZ               // 5-day return z-scored against its own year
	SigStretch21          // 21-day return in 21-day sigmas
	SigRev5               // −ret5d
	SigRev21              // −ret21d
	SigHi52               // price / 52-week high
	SigLowVol             // −σ of the last 63 daily log returns
	SigIndMom             // sector-mean ret63 within region and date
	SigIdioRev5           // −(5-day log return − β·benchmark 5-day log return)
	SigOvernight21        // Σ log(open/prev close), last 21 sessions
	SigIntraday21         // Σ log(close/open), last 21 sessions
	SigC1                 // pre-registered C1: composite with mom12-1 and ret63 weights swapped
	SigC3                 // pre-registered C3: news-conditioned residual reversal
	SigDrift              // post-earnings drift: the reaction to the last Item-2.02 release, decayed (US only)
	SigEarnWindow         // 1 when the cadence-predicted next Item-2.02 release falls inside the next 10 sessions, else 0 (US only)
	NumSignals
)

// SignalNames are the report labels, index-aligned with the Sig constants. The
// first fourteen match the Python spec's column names so the two reports can be
// read side by side.
var SignalNames = [NumSignals]string{
	"score", "trend", "mom12_1", "ret63", "strz", "stretch21", "rev5", "rev21",
	"hi52", "lowvol", "indmom", "idio_rev5", "overnight21", "intraday21",
	"c1_mom_weighted", "c3_news_rev", "drift", "earn_window",
}

// Region groups the four indices the way the adoption bar reads them.
func Region(index string) string {
	switch index {
	case "sp500", "nq100":
		return "US"
	case "eu50":
		return "EU"
	case "asia100":
		return "Asia"
	}
	return "Other"
}

// Regions is the report order.
var Regions = []string{"US", "EU", "Asia"}

// minHistory is the bar count a name needs at a rebalance date before it is
// scored: 253 bars, the full 12-1 momentum window. The Python spec's `p < 252`
// skip is the same rule.
const minHistory = 253

// staleDays drops a name from a week in which it printed no bar (a suspension,
// or a listing whose data ends): the last close it has is not this week's.
const staleDays = 5

// Record is one (rebalance date, index, ticker) observation.
type Record struct {
	Date   string
	Index  string
	Ticker string
	Sector string
	Region string
	Bench  string
	// P is the position of the rebalance bar in the ticker's full series.
	P int
	// Sig holds every signal at the rebalance date; NaN where not computable.
	Sig [NumSignals]float64
	// XS is the forward benchmark-excess return over Horizons[k]: the name's
	// close-to-close return less its benchmark's over the same dates. BX is the
	// beta-adjusted version, r − β·r_bench (pre-registered test C4). NaN when the
	// window runs past the data.
	XS [numHorizons]float64
	BX [numHorizons]float64
	// Beta is quant.Compute's 252-session beta against Bench; NaN if not computable.
	Beta float64
	// SigmaDaily is quant.Compute's Yang-Zhang daily σ, the unit of the σ stop.
	SigmaDaily float64
	// Setup is the archetype ScorePrescreen assigned. Never "drift" here: the
	// row's ReportDate stays unset, so classifySetups and the composite are the
	// ones a live run with no SEC contact address computes. The earnings event
	// is measured separately, as SigDrift and SigEarnWindow.
	Setup string
}

// Member is one index constituent in the replay.
type Member struct {
	Constituent model.Constituent
	Bench       string
}

// Data is everything the panel is built from: each ticker's full daily series
// and each benchmark's, and each US ticker's earnings-release dates. It is
// read-only once built.
type Data struct {
	Series map[string]*quant.Series
	Bench  map[string]*quant.Series
	// Filings holds each US ticker's 8-K Item 2.02 (earnings release) filing
	// dates, oldest first, keyed by upper-case ticker. A ticker with no entry —
	// every non-US name, and any US name SEC could not resolve — has SigDrift
	// and SigEarnWindow NaN. Only dates strictly before a rebalance session are
	// ever read at it (lastFilingBefore).
	Filings map[string][]time.Time
}

// WeeklyDates returns every Friday from start through end inclusive.
func WeeklyDates(start, end time.Time) []time.Time {
	d := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	for d.Weekday() != time.Friday {
		d = d.AddDate(0, 0, 1)
	}
	var out []time.Time
	for !d.After(end) {
		out = append(out, d)
		d = d.AddDate(0, 0, 7)
	}
	return out
}

// lastFriday is the most recent Friday on or before t.
func lastFriday(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	for d.Weekday() != time.Friday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// barAtOrBefore is the index of the last bar dated on or before date, or -1.
func barAtOrBefore(bars []quant.Bar, date string) int {
	i := sort.Search(len(bars), func(i int) bool { return bars[i].Date > date })
	return i - 1
}

// closeAsOf is the last close on or before date, NaN if there is none.
func closeAsOf(s *quant.Series, date string) float64 {
	if s == nil {
		return math.NaN()
	}
	i := barAtOrBefore(s.Bars, date)
	if i < 0 {
		return math.NaN()
	}
	return s.Bars[i].Close
}

// upTo returns s cut at date: the bars dated on or before it, sharing storage.
// This is the only way the panel hands a series to the shipping code, and it is
// what the look-ahead test holds it to.
func upTo(s *quant.Series, date string) *quant.Series {
	if s == nil {
		return nil
	}
	return &quant.Series{Symbol: s.Symbol, Bars: s.Bars[:barAtOrBefore(s.Bars, date)+1]}
}

// BuildPanel computes every member's signals at every date and its forward
// excess returns after it. It returns the records unfiltered; see
// DropWarmupDates.
//
// The pre-screen parameters are the live defaults with two departures, both
// forced by what history can say: the liquidity floor is off (a USD turnover
// needs the FX rate *at that date*, and a current-rate conversion would be a
// look-ahead of its own), and the minimum history is the full 253 bars rather
// than 60, so every scored name carries the 12-1 term exactly as the spec did.
func BuildPanel(members []Member, data Data, dates []time.Time) []Record {
	params := orchestrator.DefaultPrescreenParams()
	params.ADVMinUSD = 0
	params.MinBars = minHistory

	type obs struct {
		rec Record
		row orchestrator.PrescreenRow
	}
	byDate := make([][]obs, len(dates))

	benchRet63 := map[string]float64{} // bench|date → 63d return, as the live screen feeds RS63
	for _, m := range members {
		s := data.Series[strings.ToUpper(m.Constituent.Ticker)]
		b := data.Bench[m.Bench]
		if s == nil || len(s.Bars) < minHistory {
			continue
		}
		for di, d := range dates {
			ds := d.Format("2006-01-02")
			p := barAtOrBefore(s.Bars, ds)
			if p < minHistory-1 {
				continue
			}
			last, err := time.Parse("2006-01-02", s.Bars[p].Date)
			if err != nil || d.Sub(last) > staleDays*24*time.Hour {
				continue
			}
			rec, row := observe(m, s, b, p, params, benchRet63, data.Filings[strings.ToUpper(m.Constituent.Ticker)])
			rec.Date = ds
			byDate[di] = append(byDate[di], obs{rec, row})
		}
	}

	var out []Record
	for _, day := range byDate {
		if len(day) == 0 {
			continue
		}
		rows := make([]orchestrator.PrescreenRow, len(day))
		swapped := make([]orchestrator.PrescreenRow, len(day))
		for i, o := range day {
			rows[i] = o.row
			// C1 re-weights by swapping the two trend inputs: the shipped trend is
			// 0.5·z(mom12-1) + z(ret63d), so feeding ret63d in the momentum slot and
			// mom12-1 in the return slot yields 1.0·z(mom12-1) + 0.5·z(ret63d) with
			// the same total weight, penalties and clamps — the shipping arithmetic,
			// not a re-implementation of it.
			swapped[i] = o.row
			swapped[i].Mom12_1, swapped[i].Ret63d = o.row.Ret63d, o.row.Mom12_1
		}
		orchestrator.ApplyPrescreenExclusions(rows, params)
		orchestrator.ScorePrescreen(rows)
		orchestrator.ApplyPrescreenExclusions(swapped, params)
		orchestrator.ScorePrescreen(swapped)
		for i := range day {
			r := day[i].rec
			if rows[i].Excluded == "" {
				r.Sig[SigScore] = rows[i].Score
				r.Sig[SigTrend] = rows[i].Trend
				r.Setup = rows[i].Setup
			}
			if swapped[i].Excluded == "" {
				r.Sig[SigC1] = swapped[i].Score
			}
			out = append(out, r)
		}
	}
	addIndustryMomentum(out)
	return out
}

// observe computes one member's record at bar p of its series. The score and
// trend fields are filled in later, once the whole date's cross-section exists.
func observe(m Member, s, bench *quant.Series, p int, params orchestrator.PrescreenParams, benchRet63 map[string]float64, filings []time.Time) (Record, orchestrator.PrescreenRow) {
	date := s.Bars[p].Date
	cut := &quant.Series{Symbol: s.Symbol, Bars: s.Bars[:p+1]}
	bcut := upTo(bench, date)
	mx := quant.Compute(cut, bcut)

	key := m.Bench + "|" + date
	br63, ok := benchRet63[key]
	if !ok {
		br63 = 0
		if bcut != nil && len(bcut.Bars) > 0 {
			br63 = quant.Compute(bcut, nil).Ret63d
		}
		benchRet63[key] = br63
	}
	row := orchestrator.NewPrescreenRow(m.Constituent, mx, br63, params)

	nan := math.NaN()
	rec := Record{
		Index: m.Constituent.Index, Ticker: strings.ToUpper(m.Constituent.Ticker),
		Sector: m.Constituent.Sector, Region: Region(m.Constituent.Index), Bench: m.Bench, P: p,
		Beta: nan, SigmaDaily: nan,
	}
	for k := range rec.Sig {
		rec.Sig[k] = nan
	}
	if mx.Benchmark != "" {
		rec.Beta = mx.Beta
	}
	if mx.SigmaDaily > 0 {
		rec.SigmaDaily = mx.SigmaDaily
	}

	rec.Sig[SigMom12_1] = mx.Mom12_1
	rec.Sig[SigRet63] = mx.Ret63d
	rec.Sig[SigSTRZ] = mx.STRZScore
	rec.Sig[SigStretch21] = mx.Stretch21()
	rec.Sig[SigRev5] = -mx.Ret5d
	rec.Sig[SigRev21] = -mx.Ret21d
	rec.Sig[SigHi52] = mx.PriceTo52wHigh
	rec.Sig[SigLowVol] = -stdLogReturns(cut.Bars, 63)
	rec.Sig[SigOvernight21], rec.Sig[SigIntraday21] = overnightIntraday(cut.Bars, 21)

	// Residual 5-day move: the name's 5-session log return less beta times its
	// benchmark's over the same dates. Feeds idio_rev5 and C3.
	resid := nan
	if p >= 5 && !math.IsNaN(rec.Beta) {
		c0, c1 := s.Bars[p-5].Close, s.Bars[p].Close
		b0, b1 := closeAsOf(bench, s.Bars[p-5].Date), closeAsOf(bench, date)
		if c0 > 0 && c1 > 0 && b0 > 0 && b1 > 0 {
			resid = math.Log(c1/c0) - rec.Beta*math.Log(b1/b0)
		}
	}
	rec.Sig[SigIdioRev5] = -resid
	rec.Sig[SigC3] = newsConditionedReversal(resid, cut.Bars)

	// The earnings leg. row.ReportDate is deliberately left unset, so the
	// composite and the archetypes stay what they were; drift is scored as a
	// signal of its own beside them.
	if last, ok := lastFilingBefore(filings, date); ok && rec.SigmaDaily > 0 {
		if v, ok := orchestrator.EarningsDrift(cut, bcut, last, rec.SigmaDaily); ok {
			rec.Sig[SigDrift] = v
		}
	}
	rec.Sig[SigEarnWindow] = earnWindow(filings, date)

	for k, h := range Horizons {
		rec.XS[k], rec.BX[k] = nan, nan
		if p+h >= len(s.Bars) {
			continue
		}
		r := s.Bars[p+h].Close/s.Bars[p].Close - 1
		b0, b1 := closeAsOf(bench, date), closeAsOf(bench, s.Bars[p+h].Date)
		if b0 > 0 && b1 > 0 {
			br := b1/b0 - 1
			rec.XS[k] = r - br
			if !math.IsNaN(rec.Beta) {
				rec.BX[k] = r - rec.Beta*br
			}
		}
	}
	return rec, row
}

// lastFilingBefore is the latest filing dated strictly before session, the
// point-in-time rule for both earnings signals. SEC's submissions carry a
// filing *date* and no acceptance time, so a release dated on the session
// itself may have landed after its close and is not yet known at it.
func lastFilingBefore(filings []time.Time, session string) (time.Time, bool) {
	var last time.Time
	found := false
	for _, f := range filings {
		if f.Format("2006-01-02") < session && (!found || f.After(last)) {
			last, found = f, true
		}
	}
	return last, found
}

// The earnings-window prediction (C2): the next release is expected one
// quarter after the last, give or take a week. SEC gives past release dates
// only, so the expected date is extrapolated from the filer's own cadence —
// an approximation, and the only point-in-time one available.
const (
	earnCadenceDays    = 91
	earnSlackDays      = 7
	earnWindowSessions = 10
)

// earnWindow is 1 when the predicted next release window — the last release
// strictly before session plus earnCadenceDays ± earnSlackDays — overlaps the
// next earnWindowSessions sessions after it, and 0 when it does not. It is
// NaN when there is no past release to extrapolate from, or when the whole
// predicted window has already passed without one: the cadence has broken
// (a skipped quarter, a changed filer) and predicts nothing. Sessions are
// counted as weekdays, so a holiday stretches the horizon by a day.
func earnWindow(filings []time.Time, session string) float64 {
	t, err := time.Parse("2006-01-02", session)
	if err != nil {
		return math.NaN()
	}
	last, ok := lastFilingBefore(filings, session)
	if !ok {
		return math.NaN()
	}
	lo := last.AddDate(0, 0, earnCadenceDays-earnSlackDays)
	hi := last.AddDate(0, 0, earnCadenceDays+earnSlackDays)
	if !hi.After(t) {
		return math.NaN()
	}
	if lo.After(addWeekdays(t, earnWindowSessions)) {
		return 0
	}
	return 1
}

// addWeekdays is t moved forward n weekdays.
func addWeekdays(t time.Time, n int) time.Time {
	for n > 0 {
		t = t.AddDate(0, 0, 1)
		if t.Weekday() != time.Saturday && t.Weekday() != time.Sunday {
			n--
		}
	}
	return t
}

// Abnormal-volume proxy for news (pre-registered test C3): a session in the
// last abnormalWindow whose volume is at least abnormalMultiple times the mean
// of the abnormalBase sessions before that window.
const (
	abnormalWindow   = 5
	abnormalBase     = 20
	abnormalMultiple = 2.0
)

// newsConditionedReversal is C3's signal: fade a residual 5-day move that came
// without abnormal volume (−resid), follow one that came with it (+resid).
func newsConditionedReversal(resid float64, bars []quant.Bar) float64 {
	news, ok := abnormalVolume(bars)
	if math.IsNaN(resid) || !ok {
		return math.NaN()
	}
	if news {
		return resid
	}
	return -resid
}

// abnormalVolume reports whether any of the last abnormalWindow bars traded at
// least abnormalMultiple times the mean volume of the abnormalBase bars before
// them. The bool is false when the base volume is unusable.
func abnormalVolume(bars []quant.Bar) (bool, bool) {
	n := len(bars)
	if n < abnormalWindow+abnormalBase {
		return false, false
	}
	base := 0.0
	for _, b := range bars[n-abnormalWindow-abnormalBase : n-abnormalWindow] {
		base += b.Volume
	}
	base /= abnormalBase
	if base <= 0 {
		return false, false
	}
	for _, b := range bars[n-abnormalWindow:] {
		if b.Volume >= abnormalMultiple*base {
			return true, true
		}
	}
	return false, true
}

// stdLogReturns is the sample σ of the last n daily close-to-close log returns.
func stdLogReturns(bars []quant.Bar, n int) float64 {
	if len(bars) < n+1 {
		return math.NaN()
	}
	xs := make([]float64, 0, n)
	for i := len(bars) - n; i < len(bars); i++ {
		xs = append(xs, math.Log(bars[i].Close/bars[i-1].Close))
	}
	_, sd := meanSD(xs)
	return sd
}

// overnightIntraday splits the last n sessions' return into its overnight
// (open over previous close) and intraday (close over open) log legs.
func overnightIntraday(bars []quant.Bar, n int) (float64, float64) {
	if len(bars) < n+1 {
		return math.NaN(), math.NaN()
	}
	var on, id float64
	for i := len(bars) - n; i < len(bars); i++ {
		on += math.Log(bars[i].Open / bars[i-1].Close)
		id += math.Log(bars[i].Close / bars[i].Open)
	}
	return on, id
}

// addIndustryMomentum sets indmom: the mean 63-session return of the record's
// sector within its region on its date, each ticker counted once — sp500 and
// nq100 share 35 names, and counting those twice would weight the US sectors
// toward whatever nq100 over-represents.
func addIndustryMomentum(recs []Record) {
	type key struct{ date, region, sector string }
	type acc struct {
		sum  float64
		n    int
		seen map[string]bool
	}
	sums := map[key]*acc{}
	for _, r := range recs {
		v := r.Sig[SigRet63]
		// A name with no known sector (a departed point-in-time member) joins
		// no industry: pooling the unknowns would invent one.
		if math.IsNaN(v) || r.Sector == "" {
			continue
		}
		k := key{r.Date, r.Region, r.Sector}
		a := sums[k]
		if a == nil {
			a = &acc{seen: map[string]bool{}}
			sums[k] = a
		}
		if a.seen[r.Ticker] {
			continue
		}
		a.seen[r.Ticker] = true
		a.sum += v
		a.n++
	}
	for i := range recs {
		if recs[i].Sector == "" {
			continue
		}
		if a := sums[key{recs[i].Date, recs[i].Region, recs[i].Sector}]; a != nil && a.n > 0 {
			recs[i].Sig[SigIndMom] = a.sum / float64(a.n)
		}
	}
}

// DropWarmupDates removes the dates on which any index has fewer than 70% of
// the most names it ever has scorable — the first weeks, where the 253-bar
// requirement admits only the longest-listed names and a cross-section of a
// dozen is not the index.
func DropWarmupDates(recs []Record) []Record {
	count := map[string]map[string]int{} // date → index → n
	maxN := map[string]int{}
	for _, r := range recs {
		if count[r.Date] == nil {
			count[r.Date] = map[string]int{}
		}
		count[r.Date][r.Index]++
	}
	for _, byIdx := range count {
		for idx, n := range byIdx {
			if n > maxN[idx] {
				maxN[idx] = n
			}
		}
	}
	good := map[string]bool{}
	for d, byIdx := range count {
		ok := true
		for idx, mx := range maxN {
			if float64(byIdx[idx]) < 0.7*float64(mx) {
				ok = false
				break
			}
		}
		good[d] = ok
	}
	var out []Record
	for _, r := range recs {
		if good[r.Date] {
			out = append(out, r)
		}
	}
	return out
}
