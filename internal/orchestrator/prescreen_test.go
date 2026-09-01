package orchestrator

import (
	"math"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// row builds a scorable pre-screen row with sane defaults, so each test only
// states the field it is actually about.
func row(ticker, index string, mut func(*PrescreenRow)) PrescreenRow {
	r := PrescreenRow{
		Ticker: ticker,
		Name:   ticker + " Inc",
		Sector: "Technology",
		Index:  index,
		AsOf:   "2026-08-28",
		Bars:   500,
		Close:  100,
		ADV:    500e6,
	}
	if mut != nil {
		mut(&r)
	}
	return r
}

func scoreOf(t *testing.T, rows []PrescreenRow, ticker string) float64 {
	t.Helper()
	for _, r := range rows {
		if r.Ticker == ticker {
			return r.Score
		}
	}
	t.Fatalf("%s not in scored rows", ticker)
	return 0
}

func excludedReason(t *testing.T, rows []PrescreenRow, ticker string) string {
	t.Helper()
	for _, r := range rows {
		if r.Ticker == ticker {
			return r.Excluded
		}
	}
	t.Fatalf("%s not in scored rows", ticker)
	return ""
}

func TestPrescreenRanksOnMomentumComposite(t *testing.T) {
	rows := []PrescreenRow{
		row("HIGH", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 0.60; r.Ret63d = 0.20; r.RS63 = 0.12 }),
		row("MID", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 0.10; r.Ret63d = 0.04; r.RS63 = 0.01 }),
		row("LOW", "sp500", func(r *PrescreenRow) { r.Mom12_1 = -0.40; r.Ret63d = -0.15; r.RS63 = -0.20 }),
	}
	scorePrescreen(rows)

	hi, mid, lo := scoreOf(t, rows, "HIGH"), scoreOf(t, rows, "MID"), scoreOf(t, rows, "LOW")
	if !(hi > mid && mid > lo) {
		t.Errorf("composite not monotone in momentum: HIGH %.3f MID %.3f LOW %.3f", hi, mid, lo)
	}
	// z-scores are centred within the index, so the spread must straddle zero.
	if hi <= 0 || lo >= 0 {
		t.Errorf("z-scored composite should straddle zero: HIGH %.3f LOW %.3f", hi, lo)
	}
}

func TestPrescreenZScoresWithinIndex(t *testing.T) {
	// eu50's best name is weaker in absolute terms than sp500's worst. Ranking
	// on raw momentum would bury the whole index; z-scoring within the index is
	// what lets each index nominate its own leaders.
	rows := []PrescreenRow{
		row("US_A", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 0.50 }),
		row("US_B", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 0.30 }),
		row("EU_A", "eu50", func(r *PrescreenRow) { r.Mom12_1 = 0.05 }),
		row("EU_B", "eu50", func(r *PrescreenRow) { r.Mom12_1 = -0.05 }),
	}
	scorePrescreen(rows)

	if scoreOf(t, rows, "EU_A") <= 0 {
		t.Errorf("eu50's leader scored %.3f, want > 0 — z-scores must be per index", scoreOf(t, rows, "EU_A"))
	}
	if math.Abs(scoreOf(t, rows, "US_A")-scoreOf(t, rows, "EU_A")) > 1e-9 {
		t.Errorf("top of each index should z-score identically: %.3f vs %.3f",
			scoreOf(t, rows, "US_A"), scoreOf(t, rows, "EU_A"))
	}
}

// A name in two indices has two rows with two different composites, because the
// pre-screen standardises within each index. Prescreen.Row used to scan on the
// ticker alone and return the first match, and Rows is sorted best-first — so the
// lookup returned whichever index scored the name *higher*, whatever index the
// scout actually nominated it from.
//
// meritScore negates the composite for a bearish nomination, which makes the bug
// asymmetric: a bullish dual-index nomination collected max(z), and a bearish one
// collected the least negative score available. It systematically promoted
// bullish dual-index names and demoted bearish ones. On 2026-09-01, 35 of 232
// rankable names sat in two indices, gaps averaging 0.30 and reaching 1.14, in a
// merge whose whole shortlist spanned +1.79 to +3.73.
func TestMeritScoreUsesTheCandidatesOwnIndex(t *testing.T) {
	// MU's two rows from that run, in the order the artifact holds them.
	ps := &Prescreen{Rows: sortPrescreenRows([]PrescreenRow{
		row("MU", "sp500", func(r *PrescreenRow) { r.Score = 3.73 }),
		row("MU", "nq100", func(r *PrescreenRow) { r.Score = 2.59 }),
	})}
	if ps.Rows[0].Index != "sp500" {
		t.Fatalf("fixture assumes the higher-scoring row sorts first, got %s", ps.Rows[0].Index)
	}

	bull := model.Candidate{Ticker: "MU", Index: "nq100", Bias: model.BiasBullish}
	if got := meritScore(ps, bull); math.Abs(got-2.59) > 1e-9 {
		t.Errorf("bullish nq100 nomination scored %+.2f, want +2.59 — it took sp500's row", got)
	}

	// The bearish half of the asymmetry: negating the *right* index's score.
	bear := model.Candidate{Ticker: "MU", Index: "nq100", Bias: model.BiasBearish}
	if got := meritScore(ps, bear); math.Abs(got+2.59) > 1e-9 {
		t.Errorf("bearish nq100 nomination scored %+.2f, want -2.59", got)
	}
	if got := meritScore(ps, model.Candidate{Ticker: "MU", Index: "sp500", Bias: model.BiasBearish}); math.Abs(got+3.73) > 1e-9 {
		t.Errorf("bearish sp500 nomination scored %+.2f, want -3.73", got)
	}

	// Single-stock mode has no index to look up under, and must still find a row.
	if _, ok := ps.Row("", "MU"); !ok {
		t.Error("an empty index must fall back to the ticker-only scan")
	}
	// A row that exists, but not in the index the candidate was nominated from,
	// is not this candidate's row.
	if _, ok := ps.Row("eu50", "MU"); ok {
		t.Error("Row returned a row from an index the candidate is not in")
	}
}

func TestPrescreenPenalisesShortTermExtensionOnly(t *testing.T) {
	// Two names with identical trend; one has just spiked (STR z high positive).
	// The reversal penalty applies to the spiked one because its recent move
	// runs *with* the trend and is therefore what mean-reverts.
	mk := func(str float64) []PrescreenRow {
		return []PrescreenRow{
			row("SPIKED", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 0.40; r.STRZ = str }),
			row("CALM", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 0.40; r.STRZ = 0 }),
			row("WEAK", "sp500", func(r *PrescreenRow) { r.Mom12_1 = -0.40; r.STRZ = 0 }),
		}
	}
	with := mk(2.5)
	scorePrescreen(with)
	if scoreOf(t, with, "SPIKED") >= scoreOf(t, with, "CALM") {
		t.Errorf("an extended name must score below an identical calm one: %.3f vs %.3f",
			scoreOf(t, with, "SPIKED"), scoreOf(t, with, "CALM"))
	}

	// A *counter*-trend recent move is not the reversal case: an uptrend that
	// just dipped is a pullback entry, and must not be penalised.
	against := mk(-2.5)
	scorePrescreen(against)
	if scoreOf(t, against, "SPIKED") < scoreOf(t, against, "CALM")-1e-9 {
		t.Errorf("a counter-trend dip must not be penalised: %.3f vs %.3f",
			scoreOf(t, against, "SPIKED"), scoreOf(t, against, "CALM"))
	}
}

func TestPrescreenExcludesIlliquidAndShortHistory(t *testing.T) {
	rows := []PrescreenRow{
		row("GOOD", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 0.20 }),
		row("THIN", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 5.0; r.ADV = 1e6 }),
		row("NEW", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 5.0; r.Bars = 40 }),
		row("ALSOGOOD", "sp500", func(r *PrescreenRow) { r.Mom12_1 = -0.20 }),
	}
	applyPrescreenExclusions(rows, defaultPrescreenParams())
	scorePrescreen(rows)

	if got := excludedReason(t, rows, "THIN"); !strings.Contains(got, "illiquid") {
		t.Errorf("THIN excluded reason = %q, want an illiquidity reason", got)
	}
	if got := excludedReason(t, rows, "NEW"); !strings.Contains(got, "history") {
		t.Errorf("NEW excluded reason = %q, want a short-history reason", got)
	}
	if got := excludedReason(t, rows, "GOOD"); got != "" {
		t.Errorf("GOOD excluded = %q, want kept", got)
	}
	// An excluded outlier must not distort the z-scores of the names that count:
	// GOOD and ALSOGOOD are symmetric about the mean of the two kept rows.
	if s := scoreOf(t, rows, "GOOD") + scoreOf(t, rows, "ALSOGOOD"); math.Abs(s) > 1e-9 {
		t.Errorf("kept rows should centre on zero, sum = %.6f — excluded rows leaked into the mean", s)
	}
	if s := scoreOf(t, rows, "THIN"); s != 0 {
		t.Errorf("excluded row scored %.3f, want 0", s)
	}
}

func TestPrescreenTableCarriesTopAndBottom(t *testing.T) {
	var rows []PrescreenRow
	for i, tk := range []string{"A", "B", "C", "D", "E", "F", "G"} {
		mom := 0.30 - float64(i)*0.10
		rows = append(rows, row(tk, "sp500", func(r *PrescreenRow) { r.Mom12_1 = mom; r.Close = 100 + float64(i) }))
	}
	rows = append(rows, row("Z", "nq100", func(r *PrescreenRow) { r.Mom12_1 = 0.9 }))
	scorePrescreen(rows)
	ps := &Prescreen{Rows: sortPrescreenRows(rows)}

	table := ps.Table("sp500", 2, 2)
	for _, want := range []string{"A", "B", "F", "G"} {
		if !strings.Contains(table, "| "+want+" ") {
			t.Errorf("table missing %s:\n%s", want, table)
		}
	}
	for _, unwanted := range []string{"| C ", "| D ", "| Z "} {
		if strings.Contains(table, unwanted) {
			t.Errorf("table should not contain %q:\n%s", unwanted, table)
		}
	}
	if !strings.Contains(table, "mom12-1") || !strings.Contains(table, "ADV$M") {
		t.Errorf("table header missing the columns the scout is told to cite:\n%s", table)
	}
}

// The composite used to read `z(mom) + 0.5·z(ret63d) + 0.5·z(rs63)`, which was
// two terms wearing three names. RS63 is Ret63d minus a benchmark fetched once
// per index, and these z-scores are taken within an index, so every member had
// the same constant subtracted — and a z-score is invariant to that. The 63-day
// return was silently carrying weight 1.0 rather than the documented 0.5.
//
// This test would have caught it: shifting the benchmark changes every RS63 and
// must change no score at all.
func TestPrescreenIsInvariantToTheBenchmarkLevel(t *testing.T) {
	build := func(bench float64) []PrescreenRow {
		var rows []PrescreenRow
		for i, tk := range []string{"A", "B", "C", "D", "E", "F"} {
			mom, r63 := 0.50-float64(i)*0.15, 0.30-float64(i)*0.08
			rows = append(rows, row(tk, "sp500", func(r *PrescreenRow) {
				r.Mom12_1, r.Ret63d, r.RS63 = mom, r63, r63-bench
			}))
		}
		scorePrescreen(rows)
		return rows
	}
	flat, bull := build(0.0), build(0.25)
	for i := range flat {
		if math.Abs(flat[i].Score-bull[i].Score) > 1e-12 {
			t.Errorf("%s scored %+.6f against a flat benchmark and %+.6f against a +25%% one — "+
				"a within-index z-score cannot depend on a constant shift",
				flat[i].Ticker, flat[i].Score, bull[i].Score)
		}
		if flat[i].RS63 == bull[i].RS63 {
			t.Fatalf("the test is not exercising anything: RS63 unchanged at %.4f", flat[i].RS63)
		}
	}
	// And the formula recorded in every artifact must describe what ran.
	if strings.Contains(defaultPrescreenParams().Formula, "rs63") {
		t.Errorf("prescreenFormula still advertises an rs63 term: %q", defaultPrescreenParams().Formula)
	}
}

func TestPrescreenTableEmptyWithoutRows(t *testing.T) {
	ps := &Prescreen{}
	if got := ps.Table("sp500", 15, 5); got != "" {
		t.Errorf("empty pre-screen rendered %q, want no table at all", got)
	}
}

func TestPrescreenRowFromMetrics(t *testing.T) {
	m := quant.Metrics{
		Symbol: "AAPL", AsOf: "2026-08-28", Bars: 500, LastClose: 231.5,
		Ret5d: 0.01, Ret21d: 0.03, Ret63d: 0.12, Mom12_1: 0.44,
		STRZScore: 1.2, VolYZ20: 0.28, VolTrend: 1.9, Regime: "trending",
		AvgDollarVol20: 4.2e9, PriceTo52wHigh: 0.98,
	}
	c := model.Constituent{Ticker: "AAPL", Name: "Apple Inc.", Sector: "Technology", Index: "sp500"}
	r := newPrescreenRow(c, m, 0.05, defaultPrescreenParams())

	if r.RS63 != m.Ret63d-0.05 {
		t.Errorf("RS63 = %.4f, want %.4f (63d return less the benchmark's)", r.RS63, m.Ret63d-0.05)
	}
	if r.Name != "Apple Inc." || r.Sector != "Technology" || r.Index != "sp500" {
		t.Errorf("identity not carried from the constituent: %+v", r)
	}
	if len(r.Flags) == 0 {
		t.Errorf("volTrend %.2f above the flag threshold should be flagged, flags = %v", m.VolTrend, r.Flags)
	}
}

func TestPrescreenIsRobustToOutliers(t *testing.T) {
	// Real data has these: on 2026-08-28 Micron's 12-1 momentum read +644%
	// against an index whose next-best was +74%. An unwinsorized z-score gave it
	// a composite of +7.0 — nearly 3× the runner-up — while inflating the
	// standard deviation enough to flatten every honest name toward zero. One
	// name that scores three times the field is not a ranking.
	var rows []PrescreenRow
	for i := 0; i < 20; i++ {
		mom := 0.30 - float64(i)*0.03
		rows = append(rows, row(string(rune('A'+i)), "sp500", func(r *PrescreenRow) { r.Mom12_1 = mom }))
	}
	clean := make([]PrescreenRow, len(rows))
	copy(clean, rows)
	scorePrescreen(clean)

	withOutlier := append([]PrescreenRow{row("MU", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 6.44 })}, rows...)
	scorePrescreen(withOutlier)

	top := scoreOf(t, withOutlier, "MU")
	second := scoreOf(t, withOutlier, "A")
	if top > 2*second {
		t.Errorf("outlier scored %.2f against a runner-up of %.2f — the composite is being decided by one name", top, second)
	}

	// And the names below it keep their spread: the outlier must not compress
	// the rest of the index into a band around zero.
	cleanSpread := scoreOf(t, clean, "A") - scoreOf(t, clean, "T")
	dirtySpread := scoreOf(t, withOutlier, "A") - scoreOf(t, withOutlier, "T")
	if dirtySpread < 0.8*cleanSpread {
		t.Errorf("outlier compressed the rest of the ranking: spread %.2f → %.2f", cleanSpread, dirtySpread)
	}
}

// TestPrescreenReversalPenaltyNeverPaysABonus pins the sign of the reversal term.
//
// The gate tested the raw STRZ ("did the last 5 days run with the trend?") but
// the penalty was scaled by a *cross-sectional* z-score of STRZ within the index.
// Those two quantities do not share a sign. In a broad rally the index mean of
// STRZ is positive, so a mildly extended name has a negative cross-sectional z
// and `score -= 0.5·z` paid it a bonus for extending — the reversal penalty
// rewarding exactly what it exists to punish. On the 2026-09-01 universe the gate
// fired on 97 names and 5 of them were paid rather than charged.
//
// STRZ is already a z-score (quant.Compute standardises the trailing 5d return
// against the name's own one-year distribution), so it is used directly and gate
// and magnitude agree by construction.
func TestPrescreenReversalPenaltyNeverPaysABonus(t *testing.T) {
	// Four names on an identical trend, differing only in how far they have run.
	rows := []PrescreenRow{
		row("HOT_A", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 0.40; r.STRZ = 3.0 }),
		row("HOT_B", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 0.40; r.STRZ = 3.0 }),
		row("MILD", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 0.40; r.STRZ = 0.3 }),
		row("FLAT", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 0.40; r.STRZ = 0.0 }),
	}
	scorePrescreen(rows)

	flat, mild, hot := scoreOf(t, rows, "FLAT"), scoreOf(t, rows, "MILD"), scoreOf(t, rows, "HOT_A")
	if mild > flat {
		t.Errorf("a name that ran up with its trend scored ABOVE one that did not move: MILD %+.4f vs FLAT %+.4f", mild, flat)
	}
	if hot > mild {
		t.Errorf("the penalty must grow with the extension: HOT %+.4f vs MILD %+.4f", hot, mild)
	}

	// Symmetric on the short side: a crashed name is a poor short, so its signed
	// long score is pushed back up toward neutral.
	shorts := []PrescreenRow{
		row("CRASHED", "sp500", func(r *PrescreenRow) { r.Mom12_1 = -0.40; r.STRZ = -3.0 }),
		func() PrescreenRow {
			return row("STEADY", "sp500", func(r *PrescreenRow) { r.Mom12_1 = -0.40; r.STRZ = 0.0 })
		}(),
	}
	scorePrescreen(shorts)
	if scoreOf(t, shorts, "CRASHED") <= scoreOf(t, shorts, "STEADY") {
		t.Errorf("a name that just crashed is a worse short than one drifting down: CRASHED %+.4f vs STEADY %+.4f",
			scoreOf(t, shorts, "CRASHED"), scoreOf(t, shorts, "STEADY"))
	}
}
