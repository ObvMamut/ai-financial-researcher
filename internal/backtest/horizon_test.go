package backtest

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// H2 is 12-1 momentum's beta-adjusted IC at its own horizon, tested with
// Newey-West lags for that horizon (5 at 21 sessions, 13 at 63).
func TestHorizonTestsH2(t *testing.T) {
	recs := plantedSignalRecords(rand.New(rand.NewPCG(3, 4)))
	cells := crossSections(recs)
	mid := midDate(cells)
	rep := horizonTests(cells, mid)
	if len(rep.Tests) != 2 || rep.Tests[0].ID != "H2-21" || rep.Tests[1].ID != "H2-63" {
		t.Fatalf("tests = %+v, want H2-21, H2-63", rep.Tests)
	}
	for i, h := range []int{3, 4} {
		tr := rep.Tests[i]
		if tr.Status != "run" {
			t.Errorf("%s status %q, want run", tr.ID, tr.Status)
		}
		_, v := dateSeries(cells, func(cell) bool { return true }, func(c cell) float64 { return c.bic[SigMom12_1][h] })
		lags := nwLags(Horizons[h])
		if want := []int{5, 13}[i]; lags != want {
			t.Fatalf("lags for %s = %d, want %d", tr.ID, lags, want)
		}
		if want := NeweyWestT(v, lags); !almostEqual(float64(tr.T), want) {
			t.Errorf("%s t = %v, want %v (%d lags)", tr.ID, tr.T, want, lags)
		}
		if !(tr.Mean > 0 && tr.T > adoptionT) || !tr.Pass {
			t.Errorf("%s mean %v t %v pass %v, want planted signal to pass", tr.ID, tr.Mean, tr.T, tr.Pass)
		}
		if !strings.Contains(tr.Statistic, "Newey-West t ("+map[int]string{3: "5", 4: "13"}[h]+" lags)") {
			t.Errorf("%s statistic %q does not state its lags", tr.ID, tr.Statistic)
		}
	}
}
