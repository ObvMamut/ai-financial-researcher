package backtest

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// scopedPanel is 80 weekly dates of sp500 (30 names), nq100 (20 names, the
// first 10 of them also in sp500) and eu50 (30 names). drift is planted into
// the US names' beta-adjusted 10-session excess with strength us, and into
// the EU names' with strength eu, so the two regions can be made to disagree.
func scopedPanel(rng *rand.Rand, us, eu float64) []Record {
	var recs []Record
	start := time.Date(2022, 1, 7, 0, 0, 0, 0, time.UTC)
	add := func(ds, idx, tk string, strength float64) {
		r := Record{Date: ds, Index: idx, Ticker: tk, Region: Region(idx)}
		for s := range r.Sig {
			r.Sig[s] = rng.NormFloat64()
		}
		for h := range Horizons {
			r.XS[h] = 0.03 * rng.NormFloat64()
			r.BX[h] = strength*r.Sig[SigDrift] + 0.03*rng.NormFloat64()
		}
		recs = append(recs, r)
	}
	for w := 0; w < 80; w++ {
		ds := start.AddDate(0, 0, 7*w).Format("2006-01-02")
		for i := 0; i < 30; i++ {
			add(ds, "sp500", fmt.Sprintf("US%02d", i), us)
		}
		for i := 0; i < 20; i++ {
			tk := fmt.Sprintf("US%02d", i) // US00..US09 are cross-listed
			if i >= 10 {
				tk = fmt.Sprintf("NQ%02d", i)
			}
			add(ds, "nq100", tk, us)
		}
		for i := 0; i < 30; i++ {
			add(ds, "eu50", fmt.Sprintf("EU%02d", i), eu)
		}
	}
	return recs
}

func driftIC10(c cell) float64 { return c.bic[SigDrift][1] }

// A US-scoped test reads US cells only: an EU region planted the other way,
// which fails the every-region bar, neither moves the US statistic nor fails
// the scoped bar.
func TestEvaluateScopedIgnoresOtherRegions(t *testing.T) {
	recs := scopedPanel(rand.New(rand.NewPCG(11, 12)), 0.01, -0.01)
	mid := midDate(crossSections(recs))

	got := evaluateScoped(TestResult{ID: "x"}, scopedCells(recs, "US"), mid, +1, driftIC10)
	if !got.Pass || got.Scope != "US" {
		t.Fatalf("US-scoped: pass %v scope %q t %.2f verdict %q, want a pass scoped to US", got.Pass, got.Scope, got.T, got.Verdict)
	}
	if got.NDates != 80 || got.HalfNDates["H1"]+got.HalfNDates["H2"] != 80 {
		t.Errorf("n dates %d, halves %v: want 80 split across H1/H2", got.NDates, got.HalfNDates)
	}
	if every := evaluate(TestResult{ID: "x"}, crossSections(recs), mid, nwLags(10), driftIC10); every.Pass {
		t.Errorf("every-region evaluate passed with EU planted the other way: %s", every.Verdict)
	}

	// Dropping the EU rows entirely changes nothing in the US-scoped result.
	var usOnly []Record
	for _, r := range recs {
		if r.Region == "US" {
			usOnly = append(usOnly, r)
		}
	}
	again := evaluateScoped(TestResult{ID: "x"}, scopedCells(usOnly, "US"), mid, +1, driftIC10)
	if again.Mean != got.Mean || again.T != got.T || again.NDates != got.NDates {
		t.Errorf("without EU rows: mean %v t %v n %d, with: mean %v t %v n %d", again.Mean, again.T, again.NDates, got.Mean, got.T, got.NDates)
	}
}

// cellsWith is one US cell per date whose drift IC10β is v(i).
func cellsWith(n int, v func(i int) float64) []cell {
	start := time.Date(2022, 1, 7, 0, 0, 0, 0, time.UTC)
	out := make([]cell, n)
	for i := range out {
		out[i] = cell{date: start.AddDate(0, 0, 7*i).Format("2006-01-02"), index: "US", region: "US"}
		for s := range out[i].bic {
			for h := range out[i].bic[s] {
				out[i].bic[s][h] = math.NaN()
			}
		}
		out[i].bic[SigDrift][1] = v(i)
	}
	return out
}

// The half-sign rule: a t that clears the bar is not enough when one half's
// mean has the other sign; and the pre-registered direction decides which
// sign counts, where sign 0 accepts either as long as both halves agree.
func TestEvaluateScopedHalfSignRule(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	noise := make([]float64, 60)
	for i := range noise {
		noise[i] = 0.01 * rng.NormFloat64()
	}
	const n = 60
	mid := cellsWith(n, func(int) float64 { return 0 })[n/2].date

	both := cellsWith(n, func(i int) float64 { return 0.05 + noise[i] })
	if r := evaluateScoped(TestResult{}, both, mid, +1, driftIC10); !r.Pass {
		t.Errorf("positive in both halves: %s (t %.2f)", r.Verdict, r.T)
	}
	if r := evaluateScoped(TestResult{}, both, mid, -1, driftIC10); r.Pass {
		t.Errorf("positive series passed a negative-direction test: %s", r.Verdict)
	}
	if r := evaluateScoped(TestResult{}, both, mid, 0, driftIC10); !r.Pass {
		t.Errorf("two-sided, positive in both halves: %s", r.Verdict)
	}

	neg := cellsWith(n, func(i int) float64 { return -0.05 + noise[i] })
	if r := evaluateScoped(TestResult{}, neg, mid, -1, driftIC10); !r.Pass {
		t.Errorf("negative direction, negative in both halves: %s", r.Verdict)
	}

	// Strong in H1, mildly the other way in H2: overall t still clears 2.5.
	split := cellsWith(n, func(i int) float64 {
		if i < n/2 {
			return 0.1 + noise[i]
		}
		return -0.005 + noise[i]
	})
	r := evaluateScoped(TestResult{}, split, mid, +1, driftIC10)
	if !(float64(r.T) > adoptionT) {
		t.Fatalf("fixture: overall t %.2f should clear the bar so only the halves can fail it", r.T)
	}
	if r.Pass || !strings.Contains(r.Verdict, "halves") {
		t.Errorf("H2 negative: pass %v verdict %q, want a failure on the halves", r.Pass, r.Verdict)
	}
	if !(float64(r.Halves["H1"]) > 0 && float64(r.Halves["H2"]) < 0) {
		t.Errorf("halves %v, want H1 > 0 > H2", r.Halves)
	}
	if r := evaluateScoped(TestResult{}, split, mid, 0, driftIC10); r.Pass {
		t.Errorf("two-sided with halves disagreeing: %s", r.Verdict)
	}
}

// A name in both sp500 and nq100 enters the US scope once, as its sp500 row;
// the nq100 duplicate's values never reach the US cross-section.
func TestScopeRecordsDedupesCrossListedUSNames(t *testing.T) {
	var recs []Record
	for i := 0; i < 15; i++ {
		r := Record{Date: "2024-01-05", Index: "sp500", Ticker: fmt.Sprintf("S%02d", i), Region: "US"}
		r.Sig[SigDrift] = float64(i)
		r.BX[1] = 0.01 * float64(i)
		recs = append(recs, r)
	}
	// S00..S04 are also in nq100, with drift and returns that would invert
	// the ranking if they were read; N00 is nq100-only.
	for i := 0; i < 5; i++ {
		r := Record{Date: "2024-01-05", Index: "nq100", Ticker: fmt.Sprintf("S%02d", i), Region: "US"}
		r.Sig[SigDrift] = 100 - float64(i)
		r.BX[1] = -1
		recs = append(recs, r)
	}
	recs = append(recs, Record{Date: "2024-01-05", Index: "nq100", Ticker: "N00", Region: "US"})
	recs[len(recs)-1].Sig[SigDrift], recs[len(recs)-1].BX[1] = 15, 0.15
	recs = append(recs, Record{Date: "2024-01-05", Index: "eu50", Ticker: "E00", Region: "EU"})

	got := scopeRecords(recs, "US")
	if len(got) != 16 {
		t.Fatalf("US scope has %d rows, want 16 (15 sp500 + 1 nq100-only, no duplicates, no EU)", len(got))
	}
	seen := map[string]bool{}
	for _, r := range got {
		if seen[r.Ticker] {
			t.Errorf("%s appears twice on one date", r.Ticker)
		}
		seen[r.Ticker] = true
		if strings.HasPrefix(r.Ticker, "S") && r.Index != "sp500" {
			t.Errorf("%s kept from %s, want its sp500 row", r.Ticker, r.Index)
		}
	}

	cells := scopedCells(recs, "US")
	if len(cells) != 1 || cells[0].region != "US" {
		t.Fatalf("scoped cells %d (region %q), want one US cell for the one date", len(cells), cells[0].region)
	}
	// With the sp500 rows kept, drift and BX are perfectly co-monotone.
	if ic := cells[0].bic[SigDrift][1]; math.Abs(ic-1) > 1e-12 {
		t.Errorf("pooled IC10β = %v, want 1 (the nq100 duplicates must not be read)", ic)
	}
}

// The drift book: each week, the top 5 US names by |drift| (cross-listings
// once, other regions never), each held at drift's sign, equal-weighted
// beta-adjusted 15-session excess less the lab's 30bp.
func TestSignalBookNetOfCostArithmetic(t *testing.T) {
	start := time.Date(2022, 1, 7, 0, 0, 0, 0, time.UTC)
	nan := math.NaN()
	var recs []Record
	var wantNet []float64
	const weeks = 12
	for w := 0; w < weeks; w++ {
		ds := start.AddDate(0, 0, 7*w).Format("2006-01-02")
		shift := 0.001 * float64(w)
		mk := func(idx, tk string, drift, bx15 float64) {
			r := Record{Date: ds, Index: idx, Ticker: tk, Region: Region(idx)}
			for s := range r.Sig {
				r.Sig[s] = nan
			}
			r.Sig[SigDrift] = drift
			r.BX = [5]float64{nan, nan, bx15, nan, nan}
			r.XS = [5]float64{nan, nan, bx15, nan, nan}
			recs = append(recs, r)
		}
		mk("sp500", "A", 3, 0.02+shift)     // long, +0.02
		mk("sp500", "B", -2.5, -0.01+shift) // short, +0.01
		mk("nq100", "C", 2, 0.00+shift)     // long, 0
		mk("sp500", "D", 1, -0.01+shift)    // long, −0.01
		mk("sp500", "E", -0.5, 0.03+shift)  // short, −0.03
		mk("sp500", "F", 0.2, 0.50)         // sixth by |drift|: not held
		mk("sp500", "G", nan, 0.90)         // no event: not held
		mk("sp500", "H", 0, 0.90)           // zero drift has no direction: not held
		mk("nq100", "A", -9, -0.90)         // A's nq100 row: never read
		mk("eu50", "Z", 50, 0.90)           // another region: never read
		dirs := []float64{1, -1, 1, 1, -1}
		bxs := []float64{0.02 + shift, -0.01 + shift, shift, -0.01 + shift, 0.03 + shift}
		gross := 0.0
		for i := range dirs {
			gross += dirs[i] * bxs[i]
		}
		wantNet = append(wantNet, gross/5-costPerLeg)
	}
	mid := start.AddDate(0, 0, 7*weeks/2).Format("2006-01-02")

	test, book := signalBookTest(TestResult{ID: "b"}, recs, "US", SigDrift, mid, +1)
	if book.Weeks != weeks || book.FullWeeks != weeks || float64(book.MeanNames) != 5 {
		t.Fatalf("weeks %d full %d mean names %v, want %d/%d/5", book.Weeks, book.FullWeeks, book.MeanNames, weeks, weeks)
	}
	wm, _ := meanSD(wantNet)
	if !almostEqual(float64(test.Mean), wm) || !almostEqual(float64(book.MeanNetPct), 100*wm) {
		t.Errorf("net mean %v (%v%%), want %v", test.Mean, book.MeanNetPct, wm)
	}
	if !almostEqual(float64(book.MeanGrossPct)-float64(book.MeanNetPct), 100*costPerLeg) {
		t.Errorf("gross %v%% − net %v%% should be the 30bp paid once", book.MeanGrossPct, book.MeanNetPct)
	}
	if wt := NeweyWestT(wantNet, nwLags(Horizons[bookHorizon])); !almostEqual(float64(test.T), wt) {
		t.Errorf("t %v, want the NW t of the weekly net series %v", test.T, wt)
	}
	h1, _ := meanSD(wantNet[:weeks/2])
	h2, _ := meanSD(wantNet[weeks/2:])
	if !almostEqual(float64(test.Halves["H1"]), h1) || !almostEqual(float64(test.Halves["H2"]), h2) {
		t.Errorf("halves %v, want H1 %v H2 %v", test.Halves, h1, h2)
	}
	// The weekly net is −0.0086 + 0.0002·w: negative in every week, so the
	// book cannot pass a positive-direction test.
	if test.Pass || test.Scope != "US" || test.NDates != weeks {
		t.Errorf("pass %v scope %q n %d: want a US-scoped failure over %d weeks", test.Pass, test.Scope, test.NDates, weeks)
	}
}

// Look-ahead with filings present: drift and earn_window at a date must not
// move when the bars after it are cut or scrambled, nor when the filings
// dated on or after it are removed or replaced. Unlike
// TestSignalsIgnoreBarsAfterDate (which runs without filings, so both
// earnings signals are NaN there), this one has them finite.
func TestEarningsSignalsIgnoreBarsAndFilingsAfterDate(t *testing.T) {
	members, full := synthUniverse(7, 700)
	d := time.Date(2021, 9, 17, 0, 0, 0, 0, time.UTC)
	ds := d.Format("2006-01-02")

	past := map[string][]time.Time{}
	for i := 0; i < 20; i++ { // the sp500 half of synthUniverse
		tk := fmt.Sprintf("T%02d", i)
		if i%2 == 0 { // a release within 25 sessions: drift finite, earn_window 0
			past[tk] = []time.Time{day("2021-05-20"), day("2021-08-20").AddDate(0, 0, i/2)}
		} else { // last release ~3 months back: drift NaN, earn_window 1
			past[tk] = []time.Time{day("2021-06-15").AddDate(0, 0, i)}
		}
	}
	withFuture := func(extra ...time.Time) map[string][]time.Time {
		out := map[string][]time.Time{}
		for k, v := range past {
			out[k] = append(append([]time.Time(nil), v...), extra...)
		}
		return out
	}
	full.Filings = withFuture(d, day("2021-10-01"), day("2021-11-19"))

	truncated := Data{Series: map[string]*quant.Series{}, Bench: map[string]*quant.Series{}, Filings: withFuture()}
	perturbed := Data{Series: map[string]*quant.Series{}, Bench: map[string]*quant.Series{}, Filings: withFuture(day("2021-09-20"), day("2021-09-27"), day("2021-12-17"))}
	rng := rand.New(rand.NewPCG(99, 3))
	scramble := func(s *quant.Series) *quant.Series {
		out := &quant.Series{Symbol: s.Symbol, Bars: append([]quant.Bar(nil), s.Bars...)}
		for i := range out.Bars {
			if out.Bars[i].Date > ds {
				f := math.Exp(0.3 * rng.NormFloat64())
				b := &out.Bars[i]
				b.Open, b.High, b.Low, b.Close = b.Open*f, b.High*f, b.Low*f, b.Close*f
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
	finiteDrift, windowOn := 0, 0
	for _, r := range base {
		if !math.IsNaN(r.Sig[SigDrift]) {
			finiteDrift++
		}
		if r.Sig[SigEarnWindow] == 1 {
			windowOn++
		}
	}
	if finiteDrift < 5 || windowOn < 5 {
		t.Fatalf("fixture: %d finite drift, %d earn_window=1 at %s — the comparison would be vacuous", finiteDrift, windowOn, ds)
	}
	for name, other := range map[string]Data{"truncated": truncated, "perturbed": perturbed} {
		got := BuildPanel(members, other, dates)
		if len(got) != len(base) {
			t.Fatalf("%s: %d rows, want %d", name, len(got), len(base))
		}
		for i := range base {
			for _, s := range []int{SigDrift, SigEarnWindow} {
				if !sameFloat(base[i].Sig[s], got[i].Sig[s]) {
					t.Errorf("%s: %s %s = %v, want %v", name, base[i].Ticker, SignalNames[s], got[i].Sig[s], base[i].Sig[s])
				}
			}
		}
	}
}

// D1–D3 are registered: Analyze reports them in us_scoped with Status "run"
// and counts them in TestsRun. Without any earnings-release history (drift and
// earn_window NaN everywhere, as with no SEC contact address) they are
// "untestable" instead of failed runs, and TestsRun drops by three.
func TestWaveDTestsAreRegistered(t *testing.T) {
	recs := scopedPanel(rand.New(rand.NewPCG(21, 22)), 0.01, 0.01)
	res := Analyze(recs, nil, 10)
	var ids []string
	for _, tr := range res.USScoped.Tests {
		ids = append(ids, tr.ID)
		if tr.Status != "run" || tr.Scope != "US" {
			t.Errorf("%s: status %q scope %q, want run/US", tr.ID, tr.Status, tr.Scope)
		}
	}
	if got := strings.Join(ids, ","); got != "D1,D2,D3" {
		t.Fatalf("us_scoped tests = %s, want D1,D2,D3", got)
	}
	if !res.USScoped.Tests[0].Pass {
		t.Errorf("D1 on planted drift: %s, want a pass", res.USScoped.Tests[0].Verdict)
	}

	for i := range recs {
		recs[i].Sig[SigDrift], recs[i].Sig[SigEarnWindow] = math.NaN(), math.NaN()
	}
	blind := Analyze(recs, nil, 10)
	for _, tr := range blind.USScoped.Tests {
		if tr.Status != "untestable" || tr.Pass {
			t.Errorf("%s without filings: status %q pass %v, want untestable", tr.ID, tr.Status, tr.Pass)
		}
	}
	if res.TestsRun-blind.TestsRun != 3 {
		t.Errorf("tests run %d with filings, %d without; want a difference of 3", res.TestsRun, blind.TestsRun)
	}
}
