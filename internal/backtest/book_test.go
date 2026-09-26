package backtest

import (
	"math"
	"testing"
)

// pick is a shorthand for one week's synthetic Record: only the fields
// buildWeeklyBook reads (index, ticker, sector, composite) are set.
func pick(index, ticker, sector string, score float64) Record {
	r := Record{Index: index, Ticker: ticker, Sector: sector}
	r.Sig[SigScore] = score
	return r
}

func tickers(book []BookPick) []string {
	out := make([]string, len(book))
	for i, p := range book {
		out[i] = p.Ticker
	}
	return out
}

func containsAll(got []string, want ...string) bool {
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// The sector cap is the step E2 exists to size (lead 2). This checks that a
// tight cap (1) actually excludes a higher-merit same-sector name in favour of
// a lower-merit one from another sector, that a looser cap (3) changes the mix
// again, and that "off" (0) is exactly the plain top-5 by |composite| — no
// cap at all, sector composition ignored.
func TestBuildWeeklyBookSectorCapBinds(t *testing.T) {
	// 6 Tech names (the dominant sector) and 2 Health names, 8 total — equal to
	// scoutNominationsPerIndex, so the nomination cut does not trim this set,
	// and maxPerIndex is passed loose so it does not either. Only the sector
	// cap varies between the three cases.
	week := []Record{
		pick("sp500", "T1", "Tech", 6.0),
		pick("sp500", "T2", "Tech", 5.5),
		pick("sp500", "T3", "Tech", 5.0),
		pick("sp500", "T4", "Tech", 4.5),
		pick("sp500", "T5", "Tech", 4.0),
		pick("sp500", "T6", "Tech", 3.5),
		pick("sp500", "H1", "Health", 3.0),
		pick("sp500", "H2", "Health", 2.5),
	}
	const looseMaxPerIndex = 20

	cap1 := buildWeeklyBook(week, looseMaxPerIndex, 1)
	if got := tickers(cap1); len(got) != 2 || !containsAll(got, "T1", "H1") {
		t.Errorf("max_per_sector=1: got %v, want exactly [T1 H1]", got)
	}

	cap3 := buildWeeklyBook(week, looseMaxPerIndex, 3)
	if got := tickers(cap3); len(got) != 5 || !containsAll(got, "T1", "T2", "T3", "H1", "H2") {
		t.Errorf("max_per_sector=3: got %v, want exactly [T1 T2 T3 H1 H2]", got)
	}

	off := buildWeeklyBook(week, looseMaxPerIndex, 0)
	if got := tickers(off); len(got) != 5 || !containsAll(got, "T1", "T2", "T3", "T4", "T5") {
		t.Errorf("max_per_sector=0 (off): got %v, want the plain top 5 [T1..T5], sector ignored", got)
	}
}

// max_per_index caps how many names one index contributes to the pool before
// the top-5 cut, independent of the sector cap. Ten distinct-sector candidates
// with max_per_index=3 must leave only 3 in the pool, so the book — even with
// no sector cap to stop it reaching further — cannot grow past 3.
func TestBuildWeeklyBookMaxPerIndexBinds(t *testing.T) {
	var week []Record
	scores := []float64{10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	for i, s := range scores {
		week = append(week, pick("sp500", string(rune('A'+i)), string(rune('a'+i)), s))
	}
	book := buildWeeklyBook(week, 3, 0)
	if got := tickers(book); len(got) != 3 || !containsAll(got, "A", "B", "C") {
		t.Errorf("max_per_index=3: got %v, want exactly [A B C] (ranks 1-3 by score)", got)
	}
}

// scoutNominationsPerIndex (8) is the stand-in for the scout call and binds
// before max_per_index and the sector cap ever run: with max_per_index loose
// (100) but a sector cap (1) that forces the walk past the dominant sector's
// duplicates, the two lowest-ranked names (9th, 10th) must never reach the
// book, because the nomination cut dropped them from the pool entirely —
// there is nothing left for the sector cap to reach for once it has consumed
// the two names the nomination cut did admit outside the dominant sector.
func TestBuildWeeklyBookNominationCapBinds(t *testing.T) {
	week := []Record{
		pick("sp500", "T1", "Tech", 10),
		pick("sp500", "T2", "Tech", 9),
		pick("sp500", "T3", "Tech", 8),
		pick("sp500", "T4", "Tech", 7),
		pick("sp500", "T5", "Tech", 6),
		pick("sp500", "T6", "Tech", 5),
		pick("sp500", "S7", "Sector7", 4),
		pick("sp500", "S8", "Sector8", 3),
		pick("sp500", "S9", "Sector9", 2),   // rank 9: cut by the nomination step
		pick("sp500", "S10", "Sector10", 1), // rank 10: cut by the nomination step
	}
	book := buildWeeklyBook(week, 100, 1)
	got := tickers(book)
	if len(got) != 3 || !containsAll(got, "T1", "S7", "S8") {
		t.Errorf("nomination cap: got %v, want exactly [T1 S7 S8] (S9/S10 never nominated)", got)
	}
	for _, bad := range []string{"S9", "S10"} {
		for _, g := range got {
			if g == bad {
				t.Errorf("nomination cap: %s should have been excluded before the sector cap ever ran", bad)
			}
		}
	}
}

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestWorst4WeekPct(t *testing.T) {
	cases := []struct {
		name string
		xs   []float64
		want float64 // math.NaN() checked separately
		nan  bool
	}{
		{"too short", []float64{0.01, 0.02, 0.03}, 0, true},
		{"exact four", []float64{0.02, -0.01, 0.03, -0.05}, -0.01, false},
		{"picks the worst window", []float64{0.02, -0.01, 0.03, -0.05, 0.01, 0.04}, -0.02, false},
		{"a NaN excludes only the windows touching it", []float64{0.02, math.NaN(), 0.03, -0.05, 0.01, 0.04}, 0.03, false},
		{"every window touches the NaN", []float64{0.02, math.NaN(), 0.03, -0.05}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := worst4WeekPct(c.xs)
			if c.nan {
				if !math.IsNaN(got) {
					t.Errorf("got %v, want NaN", got)
				}
				return
			}
			if !almostEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// BuildBookGrid must group by date, split H1/H2 at mid exactly like the rest
// of the report, and (since every week here has 5 distinct-sector picks, so
// no cap can bind) produce an identical result across the whole SectorCaps
// grid — a direct check that "off" is not a special case in the aggregation,
// only in buildWeeklyBook's own cap test.
func TestBuildBookGridWeeksAndHalves(t *testing.T) {
	weekly := map[string]float64{
		"2022-01-07": 0.02,
		"2022-01-14": -0.01,
		"2022-01-21": 0.03,
		"2022-01-28": 0.01,
	}
	dates := []string{"2022-01-07", "2022-01-14", "2022-01-21", "2022-01-28"}
	var recs []Record
	sectors := []string{"S1", "S2", "S3", "S4", "S5"}
	for _, d := range dates {
		v := weekly[d]
		for i, sec := range sectors {
			r := pick("sp500", d+sec, sec, float64(5-i)) // distinct positive scores, all long
			r.Date = d
			r.XS[bookHorizon], r.BX[bookHorizon] = v, v
			recs = append(recs, r)
		}
	}
	mid := "2022-01-21" // dates before this are H1, this one and after are H2

	grid := BuildBookGrid(recs, mid)
	if len(grid.Arms) != len(SectorCaps) {
		t.Fatalf("got %d arms, want %d (one per SectorCaps value)", len(grid.Arms), len(SectorCaps))
	}
	if grid.NominationsPerIndex != scoutNominationsPerIndex || grid.MaxPerIndex <= 0 {
		t.Errorf("grid geometry not populated: nominations=%d max_per_index=%d", grid.NominationsPerIndex, grid.MaxPerIndex)
	}

	wantSeries := []float64{0.02, -0.01, 0.03, 0.01}
	wantMean, wantSD := meanSD(wantSeries)
	wantWorst := worst4WeekPct(wantSeries)
	wantH1Mean, _ := meanSD(wantSeries[:2])
	wantH2Mean, _ := meanSD(wantSeries[2:])

	for _, a := range grid.Arms {
		if a.All.Weeks != 4 {
			t.Errorf("cap=%d: All.Weeks = %d, want 4 (every week has 5 distinct-sector picks, no cap can bind)", a.MaxPerSector, a.All.Weeks)
		}
		if !almostEqual(float64(a.All.MeanBetaAdjPct), 100*wantMean) {
			t.Errorf("cap=%d: All.MeanBetaAdjPct = %v, want %v", a.MaxPerSector, a.All.MeanBetaAdjPct, 100*wantMean)
		}
		if !almostEqual(float64(a.All.WeeklySDPct), 100*wantSD) {
			t.Errorf("cap=%d: All.WeeklySDPct = %v, want %v", a.MaxPerSector, a.All.WeeklySDPct, 100*wantSD)
		}
		if !almostEqual(float64(a.All.Worst4WeekPct), 100*wantWorst) {
			t.Errorf("cap=%d: All.Worst4WeekPct = %v, want %v", a.MaxPerSector, a.All.Worst4WeekPct, 100*wantWorst)
		}
		if a.H1.Weeks != 2 || a.H2.Weeks != 2 {
			t.Errorf("cap=%d: H1/H2 weeks = %d/%d, want 2/2", a.MaxPerSector, a.H1.Weeks, a.H2.Weeks)
		}
		if !almostEqual(float64(a.H1.MeanBetaAdjPct), 100*wantH1Mean) {
			t.Errorf("cap=%d: H1.MeanBetaAdjPct = %v, want %v", a.MaxPerSector, a.H1.MeanBetaAdjPct, 100*wantH1Mean)
		}
		if !almostEqual(float64(a.H2.MeanBetaAdjPct), 100*wantH2Mean) {
			t.Errorf("cap=%d: H2.MeanBetaAdjPct = %v, want %v", a.MaxPerSector, a.H2.MeanBetaAdjPct, 100*wantH2Mean)
		}
		// Only 2 weeks per half: below worst4WeekPct's 4-week minimum, so NaN.
		if !math.IsNaN(float64(a.H1.Worst4WeekPct)) || !math.IsNaN(float64(a.H2.Worst4WeekPct)) {
			t.Errorf("cap=%d: H1/H2 Worst4WeekPct should be NaN (only 2 weeks each)", a.MaxPerSector)
		}
	}
}
