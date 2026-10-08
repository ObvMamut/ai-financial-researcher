package backtest

import (
	"context"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// barsFromCloses builds daily bars on consecutive calendar days, so two series
// built from the same start share every date.
func barsFromCloses(closes []float64) []quant.Bar {
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]quant.Bar, len(closes))
	for i, c := range closes {
		out[i] = quant.Bar{Date: start.AddDate(0, 0, i).Format("2006-01-02"), Open: c, High: c, Low: c, Close: c, Volume: 1}
	}
	return out
}

func flatCloses(n int, c float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = c
	}
	return out
}

func TestMaxDailyReturn(t *testing.T) {
	if got := maxDailyReturn(barsFromCloses(flatCloses(30, 50)), 21); got != 0 {
		t.Errorf("flat closes: MAX = %v, want 0", got)
	}

	// A +8% spike five sessions ago, a +3% day, and a larger +20% day that sits
	// one bar outside the 21-session window and must not count.
	closes := flatCloses(40, 100)
	closes[17] = 120 // change 16→17 is +20%: bar 17 is outside the last 21 changes (19..39)
	for i := 18; i < 40; i++ {
		closes[i] = 120
	}
	closes[35] = 129.6 // +8% on bar 35
	for i := 36; i < 40; i++ {
		closes[i] = 129.6
	}
	closes[38] = 129.6 * 1.03 // +3%, then back down
	closes[39] = 129.6
	got := maxDailyReturn(barsFromCloses(closes), 21)
	if math.Abs(got-0.08) > 1e-12 {
		t.Errorf("spike: MAX = %v, want 0.08", got)
	}
	// Widening the window to 23 changes reaches bar 17's +20%.
	if got := maxDailyReturn(barsFromCloses(closes), 23); math.Abs(got-0.2) > 1e-12 {
		t.Errorf("wide window: MAX = %v, want 0.2", got)
	}

	// The window is the last 21 changes, so 22 bars is the minimum.
	if got := maxDailyReturn(barsFromCloses(flatCloses(21, 10)), 21); !math.IsNaN(got) {
		t.Errorf("21 bars: MAX = %v, want NaN", got)
	}
	if got := maxDailyReturn(barsFromCloses(flatCloses(22, 10)), 21); got != 0 {
		t.Errorf("22 bars: MAX = %v, want 0", got)
	}
	bad := flatCloses(30, 10)
	bad[25] = 0
	if got := maxDailyReturn(barsFromCloses(bad), 21); !math.IsNaN(got) {
		t.Errorf("non-positive close: MAX = %v, want NaN", got)
	}
}

// ivolFixture builds a benchmark and a stock whose last n log returns are
// exactly r = a + β·r_b + e, with e orthogonal to a constant and to r_b, so
// the OLS residuals are e itself. Earlier history follows a different model,
// which must not leak into the window.
func ivolFixture(n, extra int) (stock, bench *quant.Series, sigma float64) {
	const a, beta = 0.0002, 1.3
	total := n + extra
	rb := make([]float64, total)
	ra := make([]float64, total)
	for i := range rb {
		rb[i] = 0.01 * math.Sin(1.7*float64(i)+0.3)
	}
	e := make([]float64, n)
	for i := range e {
		e[i] = 0.004*math.Cos(2.9*float64(i)) + 0.002*math.Sin(0.37*float64(i*i))
	}
	// Project e off span{1, r_b(window)}.
	w := rb[extra:]
	var mb float64
	for _, x := range w {
		mb += x
	}
	mb /= float64(n)
	var me float64
	for _, x := range e {
		me += x
	}
	me /= float64(n)
	var sxy, sxx float64
	for i := range e {
		sxy += (w[i] - mb) * (e[i] - me)
		sxx += (w[i] - mb) * (w[i] - mb)
	}
	k := sxy / sxx
	var ss float64
	for i := range e {
		e[i] = (e[i] - me) - k*(w[i]-mb)
		ss += e[i] * e[i]
	}
	sigma = math.Sqrt(ss / float64(n-2))

	for i := 0; i < extra; i++ {
		ra[i] = -0.5*rb[i] + 0.03*math.Sin(float64(i))
	}
	for i := 0; i < n; i++ {
		ra[extra+i] = a + beta*w[i] + e[i]
	}
	cs, cb := make([]float64, total+1), make([]float64, total+1)
	cs[0], cb[0] = 50, 4000
	for i := 0; i < total; i++ {
		cs[i+1] = cs[i] * math.Exp(ra[i])
		cb[i+1] = cb[i] * math.Exp(rb[i])
	}
	return &quant.Series{Symbol: "S", Bars: barsFromCloses(cs)}, &quant.Series{Symbol: "B", Bars: barsFromCloses(cb)}, sigma
}

func TestResidualVol(t *testing.T) {
	stock, bench, want := ivolFixture(63, 120)
	got := residualVol(stock, bench, 63)
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("IVOL = %.15g, want %.15g (diff %g)", got, want, got-want)
	}
	if want <= 0 {
		t.Fatalf("fixture: σ(e) = %v", want)
	}

	// Exactly 63 pairs suffices; 62 does not.
	s63, b63, want63 := ivolFixture(63, 0)
	if got := residualVol(s63, b63, 63); math.Abs(got-want63) > 1e-12 {
		t.Errorf("63 pairs: IVOL = %v, want %v", got, want63)
	}
	short := &quant.Series{Symbol: "B", Bars: b63.Bars[1:]}
	if got := residualVol(s63, short, 63); !math.IsNaN(got) {
		t.Errorf("short benchmark (62 pairs): IVOL = %v, want NaN", got)
	}

	if got := residualVol(stock, nil, 63); !math.IsNaN(got) {
		t.Errorf("nil benchmark: IVOL = %v, want NaN", got)
	}
	if got := residualVol(stock, &quant.Series{Symbol: "B"}, 63); !math.IsNaN(got) {
		t.Errorf("empty benchmark: IVOL = %v, want NaN", got)
	}
	if got := residualVol(nil, bench, 63); !math.IsNaN(got) {
		t.Errorf("nil series: IVOL = %v, want NaN", got)
	}
	flat := &quant.Series{Symbol: "B", Bars: barsFromCloses(flatCloses(len(bench.Bars), 4000))}
	if got := residualVol(stock, flat, 63); !math.IsNaN(got) {
		t.Errorf("flat benchmark: IVOL = %v, want NaN", got)
	}
}

func TestFIPSignal(t *testing.T) {
	// 300 bars: n−1−252 = 47 and n−1−21 = 278, so the window's 231 changes are
	// bars 48..278 against their predecessors. Outside it the series moves
	// wildly, which must not count.
	const n = 300
	closes := make([]float64, n)
	closes[0] = 100
	for i := 1; i < n; i++ {
		switch {
		case i < 48 || i > 278:
			closes[i] = closes[i-1] * 0.9 // outside the window: all down
		case i-48 < 120:
			closes[i] = closes[i-1] * 1.01 // 120 up days
		case i-48 < 120+40:
			closes[i] = closes[i-1] * 0.99 // 40 down days
		default:
			closes[i] = closes[i-1] // 71 flat days
		}
	}
	bars := barsFromCloses(closes)
	up, down := 120.0, 40.0
	for _, mom := range []float64{0.25, -0.25} {
		sign := 1.0
		if mom < 0 {
			sign = -1
		}
		id := sign * (down - up) / 231
		want := -mom * id
		got := fipSignal(bars, mom)
		if math.Abs(got-want) > 1e-15 {
			t.Errorf("mom %v: FIP = %v, want %v", mom, got, want)
		}
		// Both reduce to |mom|·(%up − %down).
		if math.Abs(got-math.Abs(mom)*(up-down)/231) > 1e-15 {
			t.Errorf("mom %v: FIP = %v is not |mom|·(%%up−%%down)", mom, got)
		}
	}
	if got := fipSignal(bars, 0); got != 0 {
		t.Errorf("zero momentum: FIP = %v, want 0", got)
	}

	// The mirror image: more down days than up gives a negative reading.
	for i := 48; i <= 278; i++ {
		if closes[i] != closes[i-1] {
			bars[i].Close = bars[i-1].Close * (2 - closes[i]/closes[i-1]) // 1.01 ↔ 0.99
		} else {
			bars[i].Close = bars[i-1].Close
		}
	}
	if got, want := fipSignal(bars, -0.1), 0.1*(down-up)/231; math.Abs(got-want) > 1e-15 {
		t.Errorf("mirrored: FIP = %v, want %v", got, want)
	}

	if got := fipSignal(bars[:252], 0.25); !math.IsNaN(got) {
		t.Errorf("252 bars: FIP = %v, want NaN", got)
	}
	if got := fipSignal(bars[:253], 0.25); math.IsNaN(got) {
		t.Errorf("253 bars: FIP is NaN, want finite")
	}
	if got := fipSignal(bars, math.NaN()); !math.IsNaN(got) {
		t.Errorf("NaN momentum: FIP = %v, want NaN", got)
	}
}

// anomalyCells is two US cells per date (sp500 and nq100), each with every
// N signal's beta-adjusted IC21 set to v(i, sig) and everything else NaN.
func anomalyCells(n int, v func(i, sig int) float64) []cell {
	start := time.Date(2018, 1, 5, 0, 0, 0, 0, time.UTC)
	var out []cell
	for i := 0; i < n; i++ {
		for _, idx := range []string{"sp500", "nq100"} {
			c := cell{date: start.AddDate(0, 0, 7*i).Format("2006-01-02"), index: idx, region: "US"}
			for s := range c.bic {
				for h := range c.bic[s] {
					c.bic[s][h], c.ic[s][h] = math.NaN(), math.NaN()
				}
			}
			for _, s := range []int{SigMax21, SigIVol63, SigFIP} {
				c.bic[s][h21] = v(i, s)
			}
			out = append(out, c)
		}
	}
	return out
}

// The registered bar: the scoped bar at nwLags(21) = 5 lags, positive
// direction, t > +2.5 and a positive mean in both halves.
func TestAnomalyTestsBar(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	const n = 120
	noise := make([]float64, n)
	for i := range noise {
		noise[i] = 0.01 * rng.NormFloat64()
	}
	cells := anomalyCells(n, func(i, sig int) float64 {
		switch sig {
		case SigMax21: // positive throughout: passes
			return 0.03 + noise[i]
		case SigIVol63: // strong H1, mildly negative H2: t clears, halves fail
			if i < n/2 {
				return 0.08 + noise[i]
			}
			return -0.004 + noise[i]
		}
		return math.NaN() // FIP: no finite statistic at all
	})
	mid := midDate(cells)
	got := anomalyTests(cells, mid)
	if len(got) != 3 || got[0].ID != "N1" || got[1].ID != "N2" || got[2].ID != "N3" {
		t.Fatalf("tests = %+v, want N1, N2, N3", got)
	}
	if nwLags(Horizons[h21]) != 5 {
		t.Fatalf("nwLags(21) = %d, want 5", nwLags(Horizons[h21]))
	}
	_, v := dateSeries(cells, func(cell) bool { return true }, func(c cell) float64 { return c.bic[SigMax21][h21] })
	if want := NeweyWestT(v, 5); !almostEqual(float64(got[0].T), want) {
		t.Errorf("N1 t = %v, want %v (5 lags)", got[0].T, want)
	}
	if want := oneSidedP(float64(got[0].T)); !almostEqual(float64(got[0].P), want) {
		t.Errorf("N1 p = %v, want one-sided %v", got[0].P, want)
	}
	if n1 := got[0]; n1.Status != "run" || !n1.Pass || n1.Scope != "US" || n1.NDates != n {
		t.Errorf("N1 status %q pass %v scope %q n %d, want a pass over %d dates: %s", n1.Status, n1.Pass, n1.Scope, n1.NDates, n, n1.Verdict)
	}
	n2 := got[1]
	if !(float64(n2.T) > adoptionT) {
		t.Fatalf("fixture: N2 t %.2f should clear the bar so only the halves fail it", n2.T)
	}
	if n2.Pass || !strings.Contains(n2.Verdict, "halves") {
		t.Errorf("N2 pass %v verdict %q, want a failure on the halves", n2.Pass, n2.Verdict)
	}
	if n3 := got[2]; n3.Status != "untestable" || n3.Pass || !math.IsNaN(float64(n3.P)) {
		t.Errorf("N3 status %q pass %v p %v, want untestable with NaN p", n3.Status, n3.Pass, n3.P)
	}
	for _, tr := range got {
		if !strings.Contains(tr.Statistic, "5 lags") {
			t.Errorf("%s statistic %q does not state its lags", tr.ID, tr.Statistic)
		}
	}
}

// The prior register is the 18 tests already run, with exactly the p-values
// registered in docs/workflow/backtest.md.
func TestPriorRegister(t *testing.T) {
	want := map[string]float64{
		"C1": 0.0756, "C3": 0.168, "C4": 0.255, "E1": 0.726, "E2-1": 0.620, "E2-3": 0.541,
		"E2-off": 0.413, "E3": 1, "D1": 0.749, "D2": 0.512, "D3": 0.0329, "H1-21": 0.139,
		"H1-63": 0.0106, "H2-21": 0.248, "H2-63": 0.181,
		"PIT-IC10": 0.617, "PIT-H1-63": 0.0968, "PIT-E1": 0.945,
	}
	if len(priorRegister) != 18 {
		t.Fatalf("prior register has %d rows, want 18", len(priorRegister))
	}
	seen := map[string]bool{}
	for _, r := range priorRegister {
		if seen[r.ID] {
			t.Errorf("duplicate ID %s", r.ID)
		}
		seen[r.ID] = true
		p := float64(r.P)
		if !(p > 0 && p <= 1) {
			t.Errorf("%s p = %v, want in (0, 1]", r.ID, p)
		}
		if w, ok := want[r.ID]; !ok || p != w {
			t.Errorf("%s p = %v, want registered %v", r.ID, p, w)
		}
		if r.Note == "" {
			t.Errorf("%s has no source", r.ID)
		}
	}
	// PIT-E1 is the sign test on 3 of 10 years.
	if p := binomialSignP(3, 10); math.Abs(p-0.945) > 0.0005 {
		t.Errorf("binomialSignP(3,10) = %v, want 0.945", p)
	}
	if s := sidedness(TestResult{ID: "PIT-E1"}); s != "binomial sign test" {
		t.Errorf("PIT-E1 sidedness %q", s)
	}
}

// The register family is Holm over m = 21: the 18 prior rows plus N1–N3. A
// NaN-p N test counts as p = 1; a planted 1e-4 is the smallest of the 21 and
// is multiplied by 21.
func TestRegisterFamily(t *testing.T) {
	tests := []TestResult{
		{ID: "N1", Status: "run", P: Num(1e-4), T: Num(3.7)},
		{ID: "N2", Status: "untestable", P: Num(math.NaN()), T: Num(math.NaN())},
		{ID: "N3", Status: "run", P: Num(0.5), T: Num(0)},
	}
	before := make([]TestResult, len(priorRegister))
	copy(before, priorRegister)
	mt := registerFamily(tests)
	if mt.FamilySize != 21 || len(mt.Rows) != 21 {
		t.Fatalf("family %d rows %d, want 21", mt.FamilySize, len(mt.Rows))
	}
	near(t, "N1 Holm p", float64(tests[0].PHolm), 21e-4, 1e-15)
	if float64(tests[1].PHolm) != 1 {
		t.Errorf("NaN-p N2 Holm p = %v, want 1", tests[1].PHolm)
	}
	if mt.Rows[0].ID != "N1" {
		t.Errorf("rows not ranked by p: first %s", mt.Rows[0].ID)
	}
	// The prior table itself is never written to.
	for i := range priorRegister {
		a, b := float64(priorRegister[i].PHolm), float64(before[i].PHolm)
		if !(math.IsNaN(a) && math.IsNaN(b)) && a != b {
			t.Errorf("registerFamily wrote to priorRegister[%d]", i)
		}
	}
	ids := map[string]bool{}
	for _, r := range mt.Rows {
		ids[r.ID] = true
	}
	for _, r := range priorRegister {
		if !ids[r.ID] {
			t.Errorf("%s missing from the family", r.ID)
		}
	}
}

// Gating: the N tests are "run", with a register family, only on a
// point-in-time replay of US indices; anywhere else they are a comparison
// with no p-value.
func TestAnomalyGating(t *testing.T) {
	mk := func() *Result {
		return &Result{Anomalies: AnomalyReport{Tests: []TestResult{
			{ID: "N1", Status: "run", P: Num(0.01), Pass: true, Verdict: "passes the US-scoped bar"},
			{ID: "N2", Status: "untestable", P: Num(math.NaN()), Verdict: "not run"},
			{ID: "N3", Status: "run", P: Num(0.2), Verdict: "fails: t does not clear +2.5"},
		}}}
	}
	for _, c := range []struct {
		pit     bool
		indices []string
		run     bool
	}{
		{true, []string{"sp500", "nq100"}, true},
		{true, []string{"nq100"}, true},
		{false, []string{"sp500", "nq100"}, false},
		{true, []string{"sp500", "eu50"}, false},
		{true, nil, false},
	} {
		r := mk()
		gateAnomalies(r, c.pit, c.indices)
		if c.run {
			if r.Anomalies.RegisterFamily.FamilySize != 21 || r.Anomalies.Tests[0].Status != "run" || !(float64(r.Anomalies.Tests[0].PHolm) > 0) {
				t.Errorf("%v %v: family %d status %q, want a run with m = 21", c.pit, c.indices, r.Anomalies.RegisterFamily.FamilySize, r.Anomalies.Tests[0].Status)
			}
			continue
		}
		if r.Anomalies.RegisterFamily.FamilySize != 0 {
			t.Errorf("%v %v: register family built on a comparison run", c.pit, c.indices)
		}
		for _, tr := range r.Anomalies.Tests {
			if tr.ID != "N2" && tr.Status != "comparison" {
				t.Errorf("%v %v: %s status %q, want comparison", c.pit, c.indices, tr.ID, tr.Status)
			}
			if !math.IsNaN(float64(tr.P)) || !math.IsNaN(float64(tr.PHolm)) || tr.Pass {
				t.Errorf("%v %v: %s p %v Holm %v pass %v, want NaN and no pass", c.pit, c.indices, tr.ID, tr.P, tr.PHolm, tr.Pass)
			}
			if tr.Status == "comparison" && !strings.HasPrefix(tr.Verdict, "comparison, registered on the point-in-time US universe only: ") {
				t.Errorf("%s verdict %q", tr.ID, tr.Verdict)
			}
		}
	}
}

// A sample-universe run computes the N tests, labels them comparisons, and
// leaves tests_run and the in-report Holm family exactly as they were.
func TestSampleRunMarksAnomaliesComparison(t *testing.T) {
	uni, err := universe.Load()
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), fakeLoader{}, uni, Config{Years: 1, Indices: []string{"nq100"}, Now: day("2022-09-30")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Anomalies.Tests) != 3 {
		t.Fatalf("anomaly tests %d, want 3", len(res.Anomalies.Tests))
	}
	for _, tr := range res.Anomalies.Tests {
		if tr.Status == "run" {
			t.Errorf("%s status run on the sample universe", tr.ID)
		}
	}
	if res.Anomalies.RegisterFamily.FamilySize != 0 {
		t.Errorf("register family built on the sample universe")
	}
	want := 0
	for _, list := range [][]TestResult{res.Preregistered, res.BookGrid.PairedTests, res.Decisions, res.USScoped.Tests, res.Horizon.Tests} {
		for _, tr := range list {
			if tr.Status == "run" {
				want++
			}
		}
	}
	if res.TestsRun != want || res.MultipleTesting.FamilySize != want {
		t.Errorf("tests run %d family %d, want %d", res.TestsRun, res.MultipleTesting.FamilySize, want)
	}
	for _, row := range res.MultipleTesting.Rows {
		if strings.HasPrefix(row.ID, "N") {
			t.Errorf("%s in the in-report Holm family", row.ID)
		}
	}
	if !strings.Contains(res.Text(), "=== v8 anomalies N1–N3") {
		t.Errorf("text report has no anomaly section")
	}
}
