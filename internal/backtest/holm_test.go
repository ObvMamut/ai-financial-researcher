package backtest

import (
	"math"
	"math/rand/v2"
	"testing"
)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.6f, want %.6f ± %g", name, got, want, tol)
	}
}

func TestNormalCDFAndPValues(t *testing.T) {
	near(t, "Φ(0)", normalCDF(0), 0.5, 1e-12)
	near(t, "Φ(1.96)", normalCDF(1.96), 0.975, 1e-4)
	near(t, "Φ(2.5)", normalCDF(2.5), 0.99379, 1e-5)
	near(t, "one-sided t=2.5", oneSidedP(2.5), 0.00621, 1e-5)
	near(t, "two-sided t=-0.5", twoSidedP(-0.5), 0.6171, 1e-4)
	near(t, "signed +1", signedP(2.5, 1), 0.00621, 1e-5)
	near(t, "signed -1", signedP(-2.5, -1), 0.00621, 1e-5)
	near(t, "signed 0", signedP(-0.5, 0), 0.6171, 1e-4)
	if !math.IsNaN(oneSidedP(math.NaN())) {
		t.Error("NaN t must give NaN p")
	}
}

func TestBinomialSignP(t *testing.T) {
	near(t, "5 of 11", binomialSignP(5, 11), 1486.0/2048, 1e-5)
	near(t, "11 of 11", binomialSignP(11, 11), 1.0/2048, 1e-9)
	near(t, "0 of 11", binomialSignP(0, 11), 1, 1e-12)
}

func TestHolm(t *testing.T) {
	got := holm([]float64{0.01, 0.04, 0.03, 0.005})
	want := []float64{0.03, 0.06, 0.06, 0.02}
	for i := range want {
		near(t, "adj", got[i], want[i], 1e-12)
	}
	// Capped at 1, monotone in sorted order, NaN ranked as 1.
	got = holm([]float64{0.9, math.NaN(), 0.6})
	for i, w := range []float64{1, 1, 1} {
		near(t, "capped", got[i], w, 1e-12)
	}
	got = holm([]float64{0.001, 0.2, 0.5})
	near(t, "first", got[0], 0.003, 1e-12)
	near(t, "second", got[1], 0.4, 1e-12)
	near(t, "third", got[2], 0.5, 1e-12)
}

func TestMultipleTestingMatchesTestsRun(t *testing.T) {
	recs := plantedSignalRecords(rand.New(rand.NewPCG(3, 4)))
	res := Analyze(recs, nil, 10)
	mt := res.MultipleTesting
	if mt.FamilySize != res.TestsRun || len(mt.Rows) != res.TestsRun || res.TestsRun != 15 {
		t.Fatalf("family %d rows %d tests run %d, want all 15", mt.FamilySize, len(mt.Rows), res.TestsRun)
	}
	for i, r := range mt.Rows {
		switch r.ID {
		case "H1-15", "C2", "C5":
			t.Errorf("%s must not be in the family", r.ID)
		}
		if float64(r.PHolm) < float64(r.P) {
			t.Errorf("%s: Holm p %v < p %v", r.ID, r.PHolm, r.P)
		}
		if i > 0 && float64(r.P) < float64(mt.Rows[i-1].P) {
			t.Errorf("rows not sorted by p at %s", r.ID)
		}
	}
	if !math.IsNaN(float64(res.Horizon.Reference.P)) {
		t.Error("H1-15 reference must carry no p")
	}
}
