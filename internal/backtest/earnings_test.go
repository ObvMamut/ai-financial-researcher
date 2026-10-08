package backtest

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func recordFor(t *testing.T, recs []Record, ticker string) Record {
	t.Helper()
	for _, r := range recs {
		if r.Ticker == ticker {
			return r
		}
	}
	t.Fatalf("no record for %s", ticker)
	return Record{}
}

// jumpUniverse is synthUniverse with a +20% repricing planted in T00 (sp500)
// from 2021-09-08, the date its earnings release is filed.
func jumpUniverse() ([]Member, Data) {
	members, data := synthUniverse(7, 700)
	for i := range data.Series["T00"].Bars {
		b := &data.Series["T00"].Bars[i]
		if b.Date >= "2021-09-08" {
			b.Open, b.High, b.Low, b.Close = b.Open*1.2, b.High*1.2, b.Low*1.2, b.Close*1.2
		}
	}
	return members, data
}

func driftAt(t *testing.T, members []Member, data Data, date string) float64 {
	t.Helper()
	return recordFor(t, BuildPanel(members, data, []time.Time{day(date)}), "T00").Sig[SigDrift]
}

// SigDrift reads the reaction to the last release only while it is inside the
// live drift window (25 sessions), and is NaN before the release and after.
func TestDriftSignalOnlyInsideTheWindow(t *testing.T) {
	members, data := jumpUniverse()
	data.Filings = map[string][]time.Time{"T00": {day("2021-09-08")}}

	if v := driftAt(t, members, data, "2021-09-03"); !math.IsNaN(v) {
		t.Errorf("before the release: drift %v, want NaN", v)
	}
	if v := driftAt(t, members, data, "2021-09-10"); !(v > 1) {
		t.Errorf("two sessions after a +20%% release: drift %v, want a large positive score", v)
	}
	if v := driftAt(t, members, data, "2021-11-19"); !math.IsNaN(v) {
		t.Errorf("~50 sessions after the release: drift %v, want NaN", v)
	}
}

// Point in time: a release dated on the rebalance session itself, or after it,
// is never read — SEC gives a filing date and no hour, so a release dated on
// the session may have landed after its close.
func TestDriftSignalIsPointInTime(t *testing.T) {
	members, data := jumpUniverse()

	data.Filings = map[string][]time.Time{"T00": {day("2021-09-10")}}
	if v := driftAt(t, members, data, "2021-09-10"); !math.IsNaN(v) {
		t.Errorf("release dated on the session: drift %v, want NaN", v)
	}
	data.Filings = map[string][]time.Time{"T00": {day("2021-09-09")}}
	if v := driftAt(t, members, data, "2021-09-10"); math.IsNaN(v) {
		t.Error("release the session before: drift NaN, want a value")
	}

	// Adding releases on and after the date changes no signal at it.
	d := day("2021-09-17")
	data.Filings = map[string][]time.Time{"T00": {day("2021-06-08"), day("2021-09-08")}}
	base := recordFor(t, BuildPanel(members, data, []time.Time{d}), "T00")
	data.Filings = map[string][]time.Time{"T00": {day("2021-06-08"), day("2021-09-08"), d, day("2021-10-01"), day("2021-12-08")}}
	got := recordFor(t, BuildPanel(members, data, []time.Time{d}), "T00")
	for s := 0; s < NumSignals; s++ {
		if !sameFloat(base.Sig[s], got.Sig[s]) {
			t.Errorf("%s = %v with later releases, %v without", SignalNames[s], got.Sig[s], base.Sig[s])
		}
	}
	if math.IsNaN(base.Sig[SigDrift]) || math.IsNaN(base.Sig[SigEarnWindow]) {
		t.Errorf("drift %v, earn_window %v: want both finite, or the comparison is vacuous", base.Sig[SigDrift], base.Sig[SigEarnWindow])
	}
}

// A name with no filing entry — every non-US name — has both earnings
// signals NaN.
func TestEarningsSignalsNaNWithoutFilings(t *testing.T) {
	members, data := jumpUniverse()
	data.Filings = map[string][]time.Time{"T00": {day("2021-09-08")}}
	recs := BuildPanel(members, data, []time.Time{day("2021-09-10")})
	for _, tk := range []string{"T01", "T20"} { // an sp500 name SEC did not resolve, an eu50 name
		r := recordFor(t, recs, tk)
		if !math.IsNaN(r.Sig[SigDrift]) || !math.IsNaN(r.Sig[SigEarnWindow]) {
			t.Errorf("%s: drift %v, earn_window %v, want NaN", tk, r.Sig[SigDrift], r.Sig[SigEarnWindow])
		}
	}
}

// earn_window is 1 only when the cadence-predicted window (last release + 91
// days ± 7, here 2021-08-24..2021-09-07) overlaps the next 10 sessions.
func TestEarnWindowIndicator(t *testing.T) {
	filings := []time.Time{day("2021-06-01")}
	for _, c := range []struct {
		session string
		want    float64
	}{
		{"2021-07-30", 0},          // horizon ends 08-13
		{"2021-08-09", 0},          // horizon ends 08-23, a day short
		{"2021-08-10", 1},          // horizon ends 08-24, the window's first day
		{"2021-08-13", 1},          // horizon ends 08-27
		{"2021-09-06", 1},          // inside the window, release still due
		{"2021-09-07", math.NaN()}, // the window's last day is today: it has passed without a release
		{"2021-09-10", math.NaN()},
		{"2021-06-01", math.NaN()}, // the release dated on the session is not yet known
	} {
		if got := earnWindow(filings, c.session); !sameFloat(got, c.want) {
			t.Errorf("%s: earn_window %v, want %v", c.session, got, c.want)
		}
	}
	// A release on or after the session is ignored: the prediction still
	// extrapolates from 06-01.
	if got := earnWindow(append(filings, day("2021-08-13")), "2021-08-13"); got != 1 {
		t.Errorf("with a release dated on the session: earn_window %v, want 1", got)
	}
	if got := earnWindow(nil, "2021-08-13"); !math.IsNaN(got) {
		t.Errorf("no releases: earn_window %v, want NaN", got)
	}
}

type fakeFilings struct {
	asked []string
	since time.Time
	hist  map[string]marketdata.FilingHistory
	warn  []string
}

func (f *fakeFilings) FilingHistory(_ context.Context, tickers []string, since time.Time) (map[string]marketdata.FilingHistory, []string) {
	f.asked, f.since = append(f.asked, tickers...), since
	out := map[string]marketdata.FilingHistory{}
	for _, t := range tickers {
		if h, ok := f.hist[t]; ok {
			out[t] = h
		}
	}
	return out, f.warn
}

// Only US members are asked for, each once, and the source's warnings come
// back unchanged.
func TestLoadFilingsAsksForUSNamesOnly(t *testing.T) {
	members, _ := synthUniverse(1, 10) // T00..T19 sp500, T20..T39 eu50
	members = append(members, Member{Constituent: members[0].Constituent, Bench: "^NDX"})
	members[len(members)-1].Constituent.Index = "nq100" // T00 again, via nq100
	src := &fakeFilings{
		hist: map[string]marketdata.FilingHistory{"T00": {Earnings: []time.Time{day("2021-09-08")}}},
		warn: []string{"T01: no CIK in SEC directory"},
	}
	got, warn := loadFilings(context.Background(), src, members, day("2021-01-01"))
	if len(src.asked) != 20 {
		t.Errorf("asked for %d tickers, want the 20 US ones once each: %v", len(src.asked), src.asked)
	}
	for _, a := range src.asked {
		if a >= "T20" {
			t.Errorf("asked for non-US %s", a)
		}
	}
	if !reflect.DeepEqual(got, map[string][]time.Time{"T00": {day("2021-09-08")}}) {
		t.Errorf("filings %v", got)
	}
	if !reflect.DeepEqual(warn, src.warn) {
		t.Errorf("warnings %v, want %v", warn, src.warn)
	}
}

// fakeLoader serves a deterministic synthetic series for any symbol.
type fakeLoader struct{}

func (fakeLoader) HistoryRange(_ context.Context, symbol, _ string, _ time.Duration) (*quant.Series, error) {
	h := fnv.New64a()
	h.Write([]byte(symbol))
	rng := rand.New(rand.NewPCG(h.Sum64(), 1))
	return synthSeries(symbol, day("2020-01-01"), 720, 0.0003*rng.NormFloat64(), 0.01+0.02*rng.Float64(), rng), nil
}

// failingBench is fakeLoader except that one symbol, a benchmark, fails the
// way Yahoo did on 2026-10-07 (HTTP 429 from every endpoint).
type failingBench struct{ symbol string }

func (f failingBench) HistoryRange(ctx context.Context, symbol, rng string, age time.Duration) (*quant.Series, error) {
	if symbol == f.symbol {
		return nil, errors.New("yahoo: HTTP 429")
	}
	return fakeLoader{}.HistoryRange(ctx, symbol, rng, age)
}

// Every registered statistic is beta-adjusted against a benchmark, so a run
// whose benchmark failed to load must stop before computing anything rather
// than print NaN figures that would spend the registration.
func TestRunStopsWhenABenchmarkIsUnavailable(t *testing.T) {
	uni, err := universe.Load()
	if err != nil {
		t.Fatal(err)
	}
	bench := universe.BenchmarkSymbol("nq100")
	res, err := Run(context.Background(), failingBench{symbol: bench}, uni,
		Config{Years: 1, Indices: []string{"nq100"}, Now: day("2022-09-30")})
	if err == nil || !strings.Contains(err.Error(), bench) {
		t.Fatalf("Run error = %v (report returned: %t); want an error naming %s and no report", err, res != nil, bench)
	}
	if res != nil {
		t.Error("a report was produced without its benchmark")
	}
}

// Run wires the filing source into the panel: with it the earnings signals
// carry per-date ICs and its warnings are reported; without it they are NaN
// throughout and the report says why.
func TestRunWiresFilings(t *testing.T) {
	uni, err := universe.Load()
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeFilings{hist: map[string]marketdata.FilingHistory{}, warn: []string{"XOM: stub"}}
	for i, c := range uni.Constituents("nq100") {
		var e []time.Time
		for d := day("2021-01-04").AddDate(0, 0, i%40); d.Before(day("2023-01-01")); d = d.AddDate(0, 0, 91) {
			e = append(e, d)
		}
		src.hist[strings.ToUpper(c.Ticker)] = marketdata.FilingHistory{Earnings: e}
	}
	cfg := Config{Years: 1, Indices: []string{"nq100"}, Now: day("2022-09-30")}

	cfg.Filings = src
	res, err := Run(context.Background(), fakeLoader{}, uni, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilingsNote != "" || !reflect.DeepEqual(res.FilingsUnavailable, src.warn) {
		t.Errorf("note %q, unavailable %v", res.FilingsNote, res.FilingsUnavailable)
	}
	if want := day("2021-10-01").AddDate(0, 0, -filingLookbackDays); !src.since.Equal(want) {
		t.Errorf("since %s, want %s", src.since.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	all := res.Signals["all"]
	if all[SigDrift].NDates == 0 || all[SigEarnWindow].NDates == 0 {
		t.Errorf("drift %d dates, earn_window %d dates, want both > 0", all[SigDrift].NDates, all[SigEarnWindow].NDates)
	}

	cfg.Filings = nil
	res, err = Run(context.Background(), fakeLoader{}, uni, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilingsNote == "" || !strings.Contains(res.Text(), res.FilingsNote) {
		t.Errorf("no-source note %q missing from the result or its text", res.FilingsNote)
	}
	all = res.Signals["all"]
	if all[SigDrift].NDates != 0 || all[SigEarnWindow].NDates != 0 {
		t.Errorf("without a source: drift %d dates, earn_window %d dates, want 0", all[SigDrift].NDates, all[SigEarnWindow].NDates)
	}
}

// Rebalance dates after the OOS-H1-63 registration's last in-sample date are
// held out of every ordinary replay, or each later `cfr backtest` would
// quietly spend the out-of-sample weeks the registration reserved.
func TestRunHoldsOutTheOutOfSampleWeeks(t *testing.T) {
	uni, err := universe.Load()
	if err != nil {
		t.Fatal(err)
	}
	holdout := day("2022-06-24")
	cfg := Config{Years: 1, Indices: []string{"nq100"}, Now: day("2022-09-30"), HoldoutAfter: holdout}
	res, err := Run(context.Background(), fakeLoader{}, uni, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.End > "2022-06-24" {
		t.Errorf("ordinary replay ends %s, past the holdout", res.End)
	}
	if res.HoldoutNote == "" || !strings.Contains(res.Text(), res.HoldoutNote) {
		t.Errorf("holdout note %q missing from the result or its text", res.HoldoutNote)
	}

	// The one evaluation refuses until 52 rebalance dates have a matured
	// 63-session window.
	cfg.EvaluateOOS = true
	if _, err := Run(context.Background(), fakeLoader{}, uni, cfg); err == nil || !strings.Contains(err.Error(), "of 52") {
		t.Errorf("an immature evaluation = %v, want a refusal counting matured dates of 52", err)
	}

	// With enough matured weeks it runs, on the held-out weeks only.
	cfg.HoldoutAfter = day("2020-12-25")
	cfg.Years = 2
	res, err = Run(context.Background(), fakeLoader{}, uni, cfg)
	if err != nil {
		t.Fatalf("a mature evaluation refused: %v", err)
	}
	if res.Start <= "2020-12-25" {
		t.Errorf("the evaluation starts %s, inside the in-sample period", res.Start)
	}
}
