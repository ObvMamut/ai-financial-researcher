package backtest

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// synthSeries is a geometric random walk with plausible OHLCV, one bar per
// weekday from start.
func synthSeries(sym string, start time.Time, n int, drift, vol float64, rng *rand.Rand) *quant.Series {
	s := &quant.Series{Symbol: sym}
	px := 100.0
	d := start
	for len(s.Bars) < n {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			d = d.AddDate(0, 0, 1)
			continue
		}
		open := px * math.Exp(vol/3*rng.NormFloat64())
		cl := open * math.Exp(drift+vol*rng.NormFloat64())
		hi := math.Max(open, cl) * (1 + vol/2*math.Abs(rng.NormFloat64()))
		lo := math.Min(open, cl) * (1 - vol/2*math.Abs(rng.NormFloat64()))
		s.Bars = append(s.Bars, quant.Bar{
			Date: d.Format("2006-01-02"), Open: open, High: hi, Low: lo, Close: cl,
			Volume: 1e6 * math.Exp(0.5*rng.NormFloat64()),
		})
		px = cl
		d = d.AddDate(0, 0, 1)
	}
	return s
}

// synthUniverse is two indices of 20 names each on their own benchmarks.
func synthUniverse(seed uint64, bars int) ([]Member, Data) {
	rng := rand.New(rand.NewPCG(seed, 1))
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	data := Data{Series: map[string]*quant.Series{}, Bench: map[string]*quant.Series{}}
	data.Bench["^A"] = synthSeries("^A", start, bars, 0.0003, 0.01, rng)
	data.Bench["^B"] = synthSeries("^B", start, bars, 0.0001, 0.012, rng)
	var members []Member
	for i := 0; i < 40; i++ {
		idx, bench := "sp500", "^A"
		if i >= 20 {
			idx, bench = "eu50", "^B"
		}
		t := fmt.Sprintf("T%02d", i)
		data.Series[t] = synthSeries(t, start, bars, 0.0004*rng.NormFloat64(), 0.01+0.02*rng.Float64(), rng)
		members = append(members, Member{
			Constituent: model.Constituent{Ticker: t, Name: t, Sector: []string{"Tech", "Energy", "Health"}[i%3], Index: idx},
			Bench:       bench,
		})
	}
	return members, data
}

func cutAt(s *quant.Series, date string) *quant.Series {
	return &quant.Series{Symbol: s.Symbol, Bars: append([]quant.Bar(nil), upTo(s, date).Bars...)}
}

func sameFloat(a, b float64) bool {
	return (math.IsNaN(a) && math.IsNaN(b)) || a == b
}

// The look-ahead test: everything the panel says at date d — every signal, the
// composite and its cross-sectional z-scores, beta and σ — must be identical
// whether or not the data holds bars after d, and whatever those bars are.
func TestSignalsIgnoreBarsAfterDate(t *testing.T) {
	members, full := synthUniverse(7, 700)
	d := time.Date(2021, 9, 17, 0, 0, 0, 0, time.UTC) // a Friday ~440 bars in
	ds := d.Format("2006-01-02")

	truncated := Data{Series: map[string]*quant.Series{}, Bench: map[string]*quant.Series{}}
	perturbed := Data{Series: map[string]*quant.Series{}, Bench: map[string]*quant.Series{}}
	rng := rand.New(rand.NewPCG(99, 2))
	scramble := func(s *quant.Series) *quant.Series {
		out := &quant.Series{Symbol: s.Symbol, Bars: append([]quant.Bar(nil), s.Bars...)}
		for i := range out.Bars {
			if out.Bars[i].Date > ds {
				f := math.Exp(0.3 * rng.NormFloat64())
				b := &out.Bars[i]
				b.Open, b.High, b.Low, b.Close, b.Volume = b.Open*f, b.High*f, b.Low*f, b.Close*f, b.Volume*5
			}
		}
		return out
	}
	for k, s := range full.Series {
		truncated.Series[k], perturbed.Series[k] = cutAt(s, ds), scramble(s)
	}
	for k, s := range full.Bench {
		truncated.Bench[k], perturbed.Bench[k] = cutAt(s, ds), scramble(s)
	}

	dates := []time.Time{d}
	base := BuildPanel(members, full, dates)
	if len(base) != len(members) {
		t.Fatalf("panel has %d rows at %s, want %d", len(base), ds, len(members))
	}
	for name, other := range map[string]Data{"truncated": truncated, "perturbed": perturbed} {
		got := BuildPanel(members, other, dates)
		if len(got) != len(base) {
			t.Fatalf("%s: %d rows, want %d", name, len(got), len(base))
		}
		for i := range base {
			a, b := base[i], got[i]
			if a.Ticker != b.Ticker || a.Index != b.Index {
				t.Fatalf("%s: row %d is %s/%s, want %s/%s", name, i, b.Index, b.Ticker, a.Index, a.Ticker)
			}
			for s := 0; s < NumSignals; s++ {
				if !sameFloat(a.Sig[s], b.Sig[s]) {
					t.Errorf("%s: %s %s = %v, want %v", name, a.Ticker, SignalNames[s], b.Sig[s], a.Sig[s])
				}
			}
			if !sameFloat(a.Beta, b.Beta) || !sameFloat(a.SigmaDaily, b.SigmaDaily) || a.Setup != b.Setup {
				t.Errorf("%s: %s beta/σ/setup differ", name, a.Ticker)
			}
		}
	}
	// The truncated data has no future at all, so the test would pass vacuously
	// if the panel never looked forward in the first place; the full panel must
	// carry forward returns that the truncated one cannot.
	cut := BuildPanel(members, truncated, dates)
	if math.IsNaN(base[0].XS[1]) || !math.IsNaN(cut[0].XS[1]) {
		t.Errorf("forward returns: full %v, truncated %v — want finite then NaN", base[0].XS[1], cut[0].XS[1])
	}
}

// A signal planted into the forward returns must come back as a strong IC with
// a large t; a signal of pure noise must not.
func TestPlantedSignalIsRecovered(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	var recs []Record
	day := time.Date(2022, 1, 7, 0, 0, 0, 0, time.UTC)
	for w := 0; w < 120; w++ {
		ds := day.AddDate(0, 0, 7*w).Format("2006-01-02")
		for _, idx := range []string{"sp500", "eu50", "asia100"} {
			for i := 0; i < 40; i++ {
				r := Record{Date: ds, Index: idx, Ticker: fmt.Sprintf("%s%d", idx, i), Region: Region(idx)}
				for s := range r.Sig {
					r.Sig[s] = rng.NormFloat64() // noise everywhere...
				}
				planted := rng.NormFloat64()
				r.Sig[SigMom12_1] = planted // ...except here
				for h := range Horizons {
					r.XS[h] = 0.004*planted + 0.03*rng.NormFloat64()
					r.BX[h] = r.XS[h]
				}
				r.SigmaDaily = math.NaN()
				recs = append(recs, r)
			}
		}
	}
	res := Analyze(recs, nil)
	all := res.Signals["all"]
	mom, noise := all[SigMom12_1], all[SigRev21]
	if !(mom.IC[1] > 0.08 && mom.TNW[1] > 8) {
		t.Errorf("planted signal: IC10 %.3f t %.2f, want IC > 0.08 and t > 8", mom.IC[1], mom.TNW[1])
	}
	if math.Abs(float64(noise.TNW[1])) > 3.5 || math.Abs(float64(noise.IC[1])) > 0.03 {
		t.Errorf("noise signal: IC10 %.3f t %.2f, want near zero", noise.IC[1], noise.TNW[1])
	}
	for _, reg := range Regions {
		if s := res.Signals[reg][SigMom12_1]; !(s.IC[1] > 0.05) {
			t.Errorf("%s: planted IC10 %.3f, want > 0.05", reg, s.IC[1])
		}
	}
	if mom.QS10Pct <= 0 {
		t.Errorf("planted quintile spread %.3f%%, want positive", mom.QS10Pct)
	}
	// NaN statistics must survive the JSON encoder as nulls.
	if _, err := json.Marshal(res); err != nil {
		t.Fatalf("report does not encode: %v", err)
	}
	if res.TestsRun != 3 {
		t.Errorf("tests run = %d, want 3 (C1, C3, C4; C2 untestable, C5 skipped)", res.TestsRun)
	}
}

// TestCalendarYears checks that calendarYears/yearSlices bucket cells by the
// four-digit year prefix of their date, sorted ascending, and that each year's
// Slice.Keep admits only that year's cells — the same shape slices() returns
// for regions and halves, reused for E1's per-year report (and E3's later
// long/short split).
func TestCalendarYears(t *testing.T) {
	cells := []cell{
		{date: "2022-01-07"}, {date: "2022-12-30"},
		{date: "2023-01-06"}, {date: "2024-06-01"},
	}
	if got := strings.Join(calendarYears(cells), ","); got != "2022,2023,2024" {
		t.Fatalf("calendarYears = %v, want 2022,2023,2024", got)
	}
	want := map[string]int{"2022": 2, "2023": 1, "2024": 1}
	sls := yearSlices(cells)
	if len(sls) != 3 {
		t.Fatalf("yearSlices returned %d slices, want 3", len(sls))
	}
	for _, sl := range sls {
		var n int
		for _, c := range cells {
			if sl.Keep(c) {
				n++
				if c.date[:4] != sl.Label {
					t.Errorf("%s slice admitted %s", sl.Label, c.date)
				}
			}
		}
		if n != want[sl.Label] {
			t.Errorf("%s slice admitted %d cells, want %d", sl.Label, n, want[sl.Label])
		}
	}
}

// TestPerYearStats plants a signal in one calendar year only and leaves a
// second year pure noise: the per-year composite IC10 must tell the two
// years apart, each year's date count must be nonzero, and the barrier
// study's top-5 excess (plain and beta-adjusted) must average only the
// trades dated inside that year, ignoring a NaN beta-adjusted leg the plain
// leg still counts (E1, docs/workflow/backtest.md).
func TestPerYearStats(t *testing.T) {
	rng := rand.New(rand.NewPCG(21, 22))
	var recs []Record
	build := func(year int, planted bool) {
		day := time.Date(year, 1, 7, 0, 0, 0, 0, time.UTC)
		for w := 0; w < 40; w++ {
			ds := day.AddDate(0, 0, 7*w).Format("2006-01-02")
			if !strings.HasPrefix(ds, fmt.Sprint(year)) {
				continue // stay inside the calendar year being built
			}
			for i := 0; i < 30; i++ {
				r := Record{Date: ds, Index: "sp500", Ticker: fmt.Sprintf("T%d", i), Region: "US"}
				for s := range r.Sig {
					r.Sig[s] = rng.NormFloat64()
				}
				signal := rng.NormFloat64()
				r.Sig[SigScore] = signal
				for h := range Horizons {
					if planted {
						r.XS[h] = 0.01*signal + 0.03*rng.NormFloat64()
					} else {
						r.XS[h] = 0.03 * rng.NormFloat64()
					}
					r.BX[h] = r.XS[h]
				}
				r.SigmaDaily = math.NaN()
				recs = append(recs, r)
			}
		}
	}
	build(2022, true)
	build(2023, false)

	cells := crossSections(recs)
	stats := perYearStats(cells, nil)
	if len(stats) != 2 || stats[0].Year != "2022" || stats[1].Year != "2023" {
		t.Fatalf("years = %+v, want 2022 then 2023", stats)
	}
	if !(stats[0].IC10 > 0.1) {
		t.Errorf("2022 (planted) IC10 = %v, want > 0.1", stats[0].IC10)
	}
	if math.Abs(float64(stats[1].IC10)) > 0.1 {
		t.Errorf("2023 (noise) IC10 = %v, want near zero", stats[1].IC10)
	}
	if stats[0].NDates == 0 || stats[1].NDates == 0 {
		t.Errorf("expected a nonzero date count for both years, got %+v", stats)
	}
	// With no trades passed, the top-5 fields must be absent (NaN → JSON null),
	// not zero, and not mistaken for "no excess".
	if !math.IsNaN(float64(stats[0].Top5Pct)) || stats[0].Top5N != 0 {
		t.Errorf("no trades: top5 = %+v, want n=0 and NaN pct", stats[0])
	}

	trades := []trade{
		{date: "2022-03-04", dir: 1, xs15: 0.05, bx15: 0.04},
		{date: "2022-03-11", dir: -1, xs15: -0.02, bx15: math.NaN()},
		{date: "2023-04-01", dir: 1, xs15: 0.01, bx15: 0.02},
	}
	stats2 := perYearStats(cells, trades)
	if stats2[0].Top5N != 2 {
		t.Errorf("2022 top5 n = %d, want 2", stats2[0].Top5N)
	}
	if wantPct := 100 * ((1*0.05 + -1*-0.02) / 2); math.Abs(float64(stats2[0].Top5Pct)-wantPct) > 1e-9 {
		t.Errorf("2022 top5 pct = %v, want %v", stats2[0].Top5Pct, wantPct)
	}
	// Only one of 2022's two trades has a finite bx15; the NaN leg must be
	// dropped from the mean rather than poisoning it.
	if wantBeta := 100 * 0.04; math.Abs(float64(stats2[0].Top5BetaPct)-wantBeta) > 1e-9 {
		t.Errorf("2022 top5 beta pct = %v, want %v", stats2[0].Top5BetaPct, wantBeta)
	}
	if stats2[1].Top5N != 1 {
		t.Errorf("2023 top5 n = %d, want 1", stats2[1].Top5N)
	}
}

func TestNeweyWestT(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	iid := make([]float64, 500)
	for i := range iid {
		iid[i] = 0.1 + rng.NormFloat64()
	}
	// Zero lags is the ordinary t with a population variance.
	m, _ := meanSD(iid)
	var ss float64
	for _, x := range iid {
		ss += (x - m) * (x - m)
	}
	want := m / math.Sqrt(ss/float64(len(iid))/float64(len(iid)))
	if got := NeweyWestT(iid, 0); math.Abs(got-want) > 1e-9 {
		t.Errorf("NW(0) = %v, want ordinary t %v", got, want)
	}
	// On a persistent series the HAC t must be well below the naive one: that
	// is the whole reason it is used on overlapping windows.
	ar := make([]float64, 500)
	for i := 1; i < len(ar); i++ {
		ar[i] = 0.9*ar[i-1] + 0.05 + rng.NormFloat64()
	}
	naive, hac := NeweyWestT(ar, 0), NeweyWestT(ar, 10)
	if !(hac < 0.6*naive) {
		t.Errorf("AR(1): NW(10) %.2f vs naive %.2f, want a much smaller HAC t", hac, naive)
	}
	if !math.IsNaN(NeweyWestT(iid[:9], 2)) {
		t.Error("fewer than ten observations must be NaN")
	}
	if nwLags(10) != 3 || nwLags(15) != 4 || nwLags(5) != 2 {
		t.Error("lags must be h/5+1")
	}
}

func TestRanksAndQuintiles(t *testing.T) {
	got := averageRanks([]float64{3, 1, 3, 2})
	want := []float64{3.5, 1, 3.5, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("averageRanks = %v, want %v", got, want)
		}
	}
	// pandas.qcut(range(1,16), 5, labels=False) → 0,0,0,1,1,1,2,2,2,3,3,3,4,4,4
	for r := 1; r <= 15; r++ {
		if q := quintileOf(float64(r), 15); q != (r-1)/3 {
			t.Errorf("quintileOf(%d, 15) = %d, want %d", r, q, (r-1)/3)
		}
	}
	x := make([]float64, 20)
	y := make([]float64, 20)
	for i := range x {
		x[i], y[i] = float64(i), float64(i)
	}
	if s := Spearman(x, y); math.Abs(s-1) > 1e-12 {
		t.Errorf("Spearman of identical ranks = %v, want 1", s)
	}
	if !math.IsNaN(Spearman(x[:14], y[:14])) {
		t.Error("fewer than 15 pairs must be NaN")
	}
	spread, top := quintileSpread(x, y)
	if spread != 16 || top != 17.5 {
		t.Errorf("quintileSpread = %v, %v; want 16, 17.5", spread, top)
	}
}

func bar(o, h, l, c float64) quant.Bar { return quant.Bar{Open: o, High: h, Low: l, Close: c} }

func TestSimulateExitRules(t *testing.T) {
	flat := func(n int, px float64) []quant.Bar {
		out := make([]quant.Bar, n)
		for i := range out {
			out[i] = bar(px, px, px, px)
		}
		return out
	}
	hold, stop2s, current := ExitRules[0], ExitRules[1], ExitRules[2]

	// Held to the time exit: the last close.
	p := flat(barrierHorizon, 100)
	p[barrierHorizon-1].Close = 110
	if r, k := simulate(trade{dir: 1, sigma: 0.01, path: p}, hold); k != 'T' || math.Abs(r-(0.10-costPerLeg)) > 1e-12 {
		t.Errorf("hold: %v %c", r, k)
	}
	// A long gapping through its 9% stop exits at the open, not at the stop.
	p = flat(barrierHorizon, 100)
	p[3] = bar(85, 86, 84, 85)
	if r, k := simulate(trade{dir: 1, sigma: 0.01, path: p}, current); k != 'S' || math.Abs(r-(-0.15-costPerLeg)) > 1e-12 {
		t.Errorf("gap stop: %v %c", r, k)
	}
	// A short touching its 15% target intraday books the target.
	p = flat(barrierHorizon, 100)
	p[5] = bar(99, 100, 84, 90)
	if r, k := simulate(trade{dir: -1, sigma: 0.01, path: p}, current); k != 'W' || math.Abs(r-(0.15-costPerLeg)) > 1e-12 {
		t.Errorf("short target: %v %c", r, k)
	}
	// A bar touching both is the stop.
	p = flat(barrierHorizon, 100)
	p[2] = bar(100, 116, 90, 100)
	if _, k := simulate(trade{dir: 1, sigma: 0.01, path: p}, current); k != 'S' {
		t.Errorf("double touch: %c, want stop", k)
	}
	// The σ stop sits at 2·σ·√15 from entry.
	p = flat(barrierHorizon, 100)
	dist := 2 * 0.02 * math.Sqrt(barrierHorizon)
	p[4] = bar(100, 100, 100*(1-dist)-0.01, 99)
	if r, k := simulate(trade{dir: 1, sigma: 0.02, path: p}, stop2s); k != 'S' || math.Abs(r-(-dist-costPerLeg)) > 1e-9 {
		t.Errorf("σ stop: %v %c", r, k)
	}
}

func TestWeeklyDatesAndRange(t *testing.T) {
	ds := WeeklyDates(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
	var got []string
	for _, d := range ds {
		got = append(got, d.Format("01-02"))
	}
	if strings.Join(got, ",") != "09-04,09-11,09-18" {
		t.Errorf("WeeklyDates = %v", got)
	}
	if lf := lastFriday(time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)); lf.Format("2006-01-02") != "2026-09-18" {
		t.Errorf("lastFriday = %s", lf)
	}
	if yahooRange(4) != "5y" || yahooRange(6) != "10y" || yahooRange(12) != "13y" {
		t.Error("yahooRange must pick the shortest covering range, and spell anything past 10y as an explicit <years>y span rather than range=max")
	}
}

func TestAbnormalVolume(t *testing.T) {
	bars := make([]quant.Bar, 30)
	for i := range bars {
		bars[i].Volume = 100
	}
	if news, ok := abnormalVolume(bars); !ok || news {
		t.Errorf("flat volume: news=%v ok=%v", news, ok)
	}
	bars[27].Volume = 200
	if news, _ := abnormalVolume(bars); !news {
		t.Error("a session at 2× the base must count as news")
	}
	if got := newsConditionedReversal(0.05, bars); got != 0.05 {
		t.Errorf("with news the move is followed: %v", got)
	}
	bars[27].Volume = 100
	if got := newsConditionedReversal(0.05, bars); got != -0.05 {
		t.Errorf("without news the move is faded: %v", got)
	}
	if _, ok := abnormalVolume(bars[:24]); ok {
		t.Error("too short a history must be unusable")
	}
}

// DropWarmupDates keeps only dates where every index has ≥70% of its peak count.
func TestDropWarmupDates(t *testing.T) {
	var recs []Record
	add := func(date, idx string, n int) {
		for i := 0; i < n; i++ {
			recs = append(recs, Record{Date: date, Index: idx, Ticker: fmt.Sprint(i)})
		}
	}
	add("2022-01-07", "sp500", 5)
	add("2022-01-07", "eu50", 10)
	add("2022-01-14", "sp500", 10)
	add("2022-01-14", "eu50", 10)
	got := DropWarmupDates(recs)
	for _, r := range got {
		if r.Date != "2022-01-14" {
			t.Fatalf("kept a warm-up date: %s", r.Date)
		}
	}
	if len(got) != 20 {
		t.Errorf("kept %d rows, want 20", len(got))
	}
}

// checkShortfall must fire when a replay comes back much shorter than
// --years asked for — the Task 5b bug: range=max answered 3-month bars, so
// only names past the first ~2.3 years of data (not the ordinary first year of
// warm-up) ever reached the 253-bar minimum and DropWarmupDates left just the
// tail of the requested window. Reproduced here with a synthetic panel whose
// series carry far fewer daily bars than the requested span needs, rather than
// with a live long-span fetch (no live fetch in tests).
func TestShortfallWarningFiresWhenTheReplayComesBackShort(t *testing.T) {
	end := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	years := 4
	rng := rand.New(rand.NewPCG(51, 6))
	start := end.AddDate(0, 0, -560) // ~400 weekdays: under a fifth of the ~1460 the 4-year request implies
	data := Data{Series: map[string]*quant.Series{}, Bench: map[string]*quant.Series{}}
	data.Bench["^A"] = synthSeries("^A", start, 400, 0.0002, 0.01, rng)
	var members []Member
	for i := 0; i < 20; i++ {
		tk := fmt.Sprintf("S%02d", i)
		data.Series[tk] = synthSeries(tk, start, 400, 0.0003*rng.NormFloat64(), 0.01+0.01*rng.Float64(), rng)
		members = append(members, Member{
			Constituent: model.Constituent{Ticker: tk, Name: tk, Sector: "Tech", Index: "sp500"},
			Bench:       "^A",
		})
	}
	dates := WeeklyDates(end.AddDate(-years, 0, 0), end)
	recs := DropWarmupDates(BuildPanel(members, data, dates))
	if len(recs) == 0 {
		t.Fatal("panel is empty; the synthetic series must still produce at least one scorable week")
	}
	res := Analyze(recs, data.Series)
	checkShortfall(res, years, end)

	if !res.Shortfall {
		t.Fatalf("replay runs %s..%s (%d rebalances) against a %d-year request ending %s — want it flagged short",
			res.Start, res.End, res.Dates, years, end.Format("2006-01-02"))
	}
	if res.ShortfallNote == "" {
		t.Error("Shortfall is true but ShortfallNote is empty")
	}
	if res.RequestedStart != end.AddDate(-years, 0, 0).Format("2006-01-02") {
		t.Errorf("RequestedStart = %s, want %s", res.RequestedStart, end.AddDate(-years, 0, 0).Format("2006-01-02"))
	}
	if !strings.Contains(res.ShortfallNote, res.Start) || !strings.Contains(res.ShortfallNote, res.End) {
		t.Errorf("ShortfallNote = %q, want it to name the actual span %s..%s", res.ShortfallNote, res.Start, res.End)
	}
}

// checkShortfall must not fire on an ordinary replay: the first rebalance
// naturally lands a little after end-years (holidays, DropWarmupDates'
// threshold) — the 2026-09-23 run's default 4-year replay missed it by only 19
// days — so a few weeks of slop must stay under the grace window and clearing
// it (here, 9 weeks) must still fire.
func TestNoShortfallWarningWithinTheOrdinaryWarmupSlop(t *testing.T) {
	end := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	res := &Result{
		Start: end.AddDate(-4, 0, 0).AddDate(0, 0, 4*7).Format("2006-01-02"), // 4 weeks late
		End:   end.Format("2006-01-02"),
		Dates: 200,
	}
	checkShortfall(res, 4, end)
	if res.Shortfall {
		t.Errorf("shortfall flagged at %s, only 4 weeks after the requested start — want it within the grace window", res.Start)
	}

	res2 := &Result{
		Start: end.AddDate(-4, 0, 0).AddDate(0, 0, 9*7).Format("2006-01-02"), // 9 weeks late
		End:   end.Format("2006-01-02"),
		Dates: 20,
	}
	checkShortfall(res2, 4, end)
	if !res2.Shortfall {
		t.Errorf("shortfall not flagged at %s, 9 weeks after the requested start — want it past the grace window", res2.Start)
	}
}
