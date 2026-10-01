package backtest

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"
	"time"
)

// H2 is 12-1 momentum's beta-adjusted IC at its own horizon, tested with
// Newey-West lags for that horizon (5 at 21 sessions, 13 at 63).
func TestHorizonTestsH2(t *testing.T) {
	recs := plantedSignalRecords(rand.New(rand.NewPCG(3, 4)))
	cells := crossSections(recs)
	mid := midDate(cells)
	rep := horizonTests(recs, cells, mid)
	if len(rep.Tests) != 4 || rep.Tests[2].ID != "H2-21" || rep.Tests[3].ID != "H2-63" {
		t.Fatalf("tests = %+v, want H1-21, H1-63, H2-21, H2-63", rep.Tests)
	}
	for i, h := range []int{h21, h63} {
		tr := rep.Tests[2+i]
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
		if !strings.Contains(tr.Statistic, "Newey-West t ("+map[int]string{h21: "5", h63: "13"}[h]+" lags)") {
			t.Errorf("%s statistic %q does not state its lags", tr.ID, tr.Statistic)
		}
	}
}

// h1Rec builds a record for the H1 book: only the fields topPicks and the book
// read, with BX at both long horizons set to bx.
func h1Rec(date, index, ticker string, score, bx float64) Record {
	r := Record{Date: date, Index: index, Ticker: ticker, Region: Region(index), SigmaDaily: 0.01}
	r.Sig[SigScore] = score
	r.XS[h21], r.BX[h21] = bx, bx
	r.XS[h63], r.BX[h63] = bx, bx
	return r
}

func nan() float64 { return math.NaN() }

// A hand-computed panel: per-date mean of dir·BX21 over every index's picks,
// less 30bp once; a NaN-BX pick leaves the mean; a date with none is dropped;
// a region with no pick on a date is dropped from that region's series.
func TestH1BookByHand(t *testing.T) {
	const d1, d2, d3, d4 = "2022-01-07", "2022-01-14", "2022-01-21", "2022-01-28"
	recs := []Record{
		h1Rec(d1, "sp500", "A", 2, 0.04), h1Rec(d1, "sp500", "B", -1, -0.02),
		h1Rec(d1, "eu50", "C", 1, 0.01), h1Rec(d1, "eu50", "D", -3, 0.03),
		h1Rec(d1, "asia100", "E", 1, 0.02),
		h1Rec(d2, "sp500", "A", 1, 0.02), h1Rec(d2, "sp500", "B", 1, nan()),
		h1Rec(d2, "eu50", "C", 1, 0.04), h1Rec(d2, "asia100", "E", -2, -0.01),
		h1Rec(d3, "sp500", "A", 1, nan()),
		h1Rec(d4, "sp500", "A", 1, 0.01),
	}
	cells := crossSections(recs)
	rep := horizonTests(recs, cells, d2)
	tr := rep.Tests[0]
	if tr.ID != "H1-21" || tr.Status != "run" {
		t.Fatalf("first test %s status %q, want H1-21 run", tr.ID, tr.Status)
	}
	series := []float64{0.012 - costPerLeg, 0.07/3 - costPerLeg, 0.01 - costPerLeg}
	wantMean, _ := meanSD(series)
	if tr.NDates != 3 || !almostEqual(float64(tr.Mean), wantMean) {
		t.Errorf("n %d mean %v, want 3 dates, mean %v", tr.NDates, tr.Mean, wantMean)
	}
	// Three dates are too few for five lags: both sides are NaN. The lag
	// count itself is checked on the long panel below.
	if want := NeweyWestT(series, nwLags(21)); !almostEqual(float64(tr.T), want) && !(math.IsNaN(float64(tr.T)) && math.IsNaN(want)) {
		t.Errorf("t = %v, want %v (nwLags(21) = %d)", tr.T, want, nwLags(21))
	}
	wantHalves := map[string]float64{"H1": series[0], "H2": (series[1] + series[2]) / 2}
	for k, w := range wantHalves {
		if !almostEqual(float64(tr.Halves[k]), w) {
			t.Errorf("half %s = %v, want %v", k, tr.Halves[k], w)
		}
	}
	// US d1 = mean(0.04, 0.02) = 0.03, d2 = 0.02, d4 = 0.01; EU d1 = mean(0.01, -0.03) = -0.01, d2 = 0.04;
	// Asia d1 = 0.02, d2 = 0.01.
	wantRegions := map[string]float64{
		"US":   (0.03 + 0.02 + 0.01 - 3*costPerLeg) / 3,
		"EU":   (-0.01 + 0.04 - 2*costPerLeg) / 2,
		"Asia": (0.02 + 0.01 - 2*costPerLeg) / 2,
	}
	for k, w := range wantRegions {
		if !almostEqual(float64(tr.Regions[k]), w) {
			t.Errorf("region %s = %v, want %v", k, tr.Regions[k], w)
		}
	}
	// Books: weeks priced 3, picks 5 + 3 + 1 over 3 weeks.
	b := rep.Books[1]
	if b.Horizon != 21 || b.Weeks != 3 || !almostEqual(float64(b.MeanPicks), 3) {
		t.Errorf("book21 = %+v, want 3 weeks, mean picks 3", b)
	}
	if !almostEqual(float64(b.NetPct), 100*wantMean) || !almostEqual(float64(b.GrossPct), 100*(wantMean+costPerLeg)) ||
		!almostEqual(float64(b.H1NetPct), 100*series[0]) || !almostEqual(float64(b.H2NetPct), 100*wantHalves["H2"]) {
		t.Errorf("book21 pcts = %+v", b)
	}
}

// Only the five largest |score| per (date, index) are held, and the cost is
// charged once per name whatever the horizon.
func TestH1BookTopFiveAndCost(t *testing.T) {
	var recs []Record
	for i := 0; i < 5; i++ {
		recs = append(recs, h1Rec("2022-01-07", "sp500", string(rune('A'+i)), float64(10+i), 0.01))
	}
	recs = append(recs, h1Rec("2022-01-07", "sp500", "Z", 1, 9)) // sixth by |score|
	rep := horizonTests(recs, crossSections(recs), "2022-01-08")
	for i, k := range []int{h21, h63} {
		if got := float64(rep.Tests[i].Mean); !almostEqual(got, 0.01-costPerLeg) {
			t.Errorf("H1-%d mean %v, want %v", Horizons[k], got, 0.01-costPerLeg)
		}
	}
}

// h1Panel plants an edge in the score's own sign, one edge per region.
func h1Panel(rng *rand.Rand, edge map[string]float64) []Record {
	var recs []Record
	day := time.Date(2022, 1, 7, 0, 0, 0, 0, time.UTC)
	for w := 0; w < 100; w++ {
		ds := day.AddDate(0, 0, 7*w).Format("2006-01-02")
		for _, idx := range []string{"sp500", "eu50", "asia100"} {
			for i := 0; i < 12; i++ {
				score := rng.NormFloat64()
				r := h1Rec(ds, idx, fmt.Sprintf("%s%d", idx, i), score, 0)
				bx := edge[r.Region]*score + 0.02*rng.NormFloat64()
				for _, k := range []int{2, h21, h63} {
					r.XS[k], r.BX[k] = bx, bx
				}
				recs = append(recs, r)
			}
		}
	}
	return recs
}

func TestH1PassesAndRegionLegCanFlipIt(t *testing.T) {
	good := h1Panel(rand.New(rand.NewPCG(5, 6)), map[string]float64{"US": 0.02, "EU": 0.02, "Asia": 0.02})
	rep := horizonTests(good, crossSections(good), midDate(crossSections(good)))
	for _, i := range []int{0, 1} {
		if tr := rep.Tests[i]; !tr.Pass || tr.Verdict != "passes the adoption bar" {
			t.Errorf("%s: pass %v verdict %q t %v, want pass", tr.ID, tr.Pass, tr.Verdict, tr.T)
		}
	}
	// The t uses nwLags(h): rebuild the 21- and 63-session date series by hand.
	for i, k := range []int{h21, h63} {
		sum, n := map[string]float64{}, map[string]float64{}
		var dates []string
		for _, p := range topPicks(good) {
			if _, ok := n[p.Date]; !ok {
				dates = append(dates, p.Date)
			}
			sum[p.Date] += math.Copysign(1, p.Sig[SigScore]) * p.BX[k]
			n[p.Date]++
		}
		sort.Strings(dates)
		var series []float64
		for _, d := range dates {
			series = append(series, sum[d]/n[d]-costPerLeg)
		}
		if want := NeweyWestT(series, nwLags(Horizons[k])); !almostEqual(float64(rep.Tests[i].T), want) {
			t.Errorf("%s t = %v, want %v", rep.Tests[i].ID, rep.Tests[i].T, want)
		}
	}
	bad := h1Panel(rand.New(rand.NewPCG(5, 6)), map[string]float64{"US": -0.01, "EU": 0.03, "Asia": 0.03})
	rep = horizonTests(bad, crossSections(bad), midDate(crossSections(bad)))
	tr := rep.Tests[0]
	if !(tr.T > adoptionT) || tr.Regions["US"] > 0 {
		t.Fatalf("setup: t %v US %v, want overall t to pass and US mean <= 0", tr.T, tr.Regions["US"])
	}
	if tr.Pass || !strings.Contains(tr.Verdict, "sign not positive") {
		t.Errorf("pass %v verdict %q, want the sign failure", tr.Pass, tr.Verdict)
	}
}

// H1-15 is a reference, not a registered test: it sits outside Tests, which
// feeds TestsRun, and carries the comparison status.
func TestH1ReferenceIsNotATest(t *testing.T) {
	recs := h1Panel(rand.New(rand.NewPCG(5, 6)), map[string]float64{"US": 0.02, "EU": 0.02, "Asia": 0.02})
	rep := horizonTests(recs, crossSections(recs), midDate(crossSections(recs)))
	ref := rep.Reference
	if ref.ID != "H1-15" || ref.Status != "comparison" || ref.Pass || !strings.HasPrefix(ref.Verdict, "reference, not a test: ") {
		t.Errorf("reference = %+v", ref)
	}
	for _, tr := range rep.Tests {
		if tr.ID == "H1-15" {
			t.Error("H1-15 is in Tests")
		}
	}
	if len(rep.Books) != 3 || rep.Books[0].Horizon != 15 || rep.Books[1].Horizon != 21 || rep.Books[2].Horizon != 63 {
		t.Errorf("books = %+v, want horizons 15, 21, 63", rep.Books)
	}
	ids := []string{}
	for _, tr := range rep.Tests {
		ids = append(ids, tr.ID)
	}
	if strings.Join(ids, ",") != "H1-21,H1-63,H2-21,H2-63" {
		t.Errorf("order = %v", ids)
	}
}
