package orchestrator

import (
	"fmt"
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

func trendOf(t *testing.T, rows []PrescreenRow, ticker string) float64 {
	t.Helper()
	for _, r := range rows {
		if r.Ticker == ticker {
			return r.Trend
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

	table := ps.Table("sp500", PrescreenParams{TopPerIndex: 2, BottomPerIndex: 2})
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
	if got := ps.Table("sp500", defaultPrescreenParams()); got != "" {
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
	// Names on an identical trend, differing only in how far they have run.
	//
	// The anchors are load-bearing, and there are two on each side for a
	// reason. The gate asks whether the recent move runs with the *composite*,
	// and the composite is a within-index z-score, so the fixture needs real
	// cross-sectional spread or every name scores 0 and this test asserts
	// equal numbers against each other. One anchor per side is not enough:
	// zscores winsorises at one value from each end once the sample reaches
	// five, which clips a lone anchor away and collapses the spread again.
	anchors := []PrescreenRow{
		row("HI_1", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 1.00; r.Ret63d = 0.50 }),
		row("HI_2", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 1.00; r.Ret63d = 0.50 }),
		row("LO_1", "sp500", func(r *PrescreenRow) { r.Mom12_1 = -1.00; r.Ret63d = -0.50 }),
		row("LO_2", "sp500", func(r *PrescreenRow) { r.Mom12_1 = -1.00; r.Ret63d = -0.50 }),
	}
	subject := func(ticker string, strz float64) PrescreenRow {
		return row(ticker, "sp500", func(r *PrescreenRow) {
			r.Mom12_1, r.Ret63d, r.STRZ = 0.40, 0.40, strz
		})
	}
	// STRZ magnitudes are kept inside the trend so the ordering measures the
	// penalty rather than the clamp at neutral, which has its own test.
	rows := append(append([]PrescreenRow{}, anchors...),
		subject("HOT_A", 0.9), subject("HOT_B", 0.9), subject("MILD", 0.3), subject("FLAT", 0.0))
	scorePrescreen(rows)
	if trendOf(t, rows, "FLAT") <= 0 {
		t.Fatalf("fixture is degenerate: the test names must share a positive composite, got %+.4f",
			trendOf(t, rows, "FLAT"))
	}
	if scoreOf(t, rows, "HOT_A") <= 0 {
		t.Fatalf("fixture: HOT_A must stay above the neutral clamp to measure penalty size, got %+.4f",
			scoreOf(t, rows, "HOT_A"))
	}

	flat, mild, hot := scoreOf(t, rows, "FLAT"), scoreOf(t, rows, "MILD"), scoreOf(t, rows, "HOT_A")
	if mild > flat {
		t.Errorf("a name that ran up with its trend scored ABOVE one that did not move: MILD %+.4f vs FLAT %+.4f", mild, flat)
	}
	if hot > mild {
		t.Errorf("the penalty must grow with the extension: HOT %+.4f vs MILD %+.4f", hot, mild)
	}

	// Symmetric on the short side: a crashed name is a poor short, so its signed
	// long score is pushed back up toward neutral.
	shortSubject := func(ticker string, strz float64) PrescreenRow {
		return row(ticker, "sp500", func(r *PrescreenRow) {
			r.Mom12_1, r.Ret63d, r.STRZ = -0.40, -0.40, strz
		})
	}
	shorts := append(append([]PrescreenRow{}, anchors...),
		shortSubject("CRASHED", -0.9), shortSubject("STEADY", 0.0))
	scorePrescreen(shorts)
	if trendOf(t, shorts, "STEADY") >= 0 {
		t.Fatalf("fixture is degenerate: the two shorts must share a negative composite, got %+.4f",
			trendOf(t, shorts, "STEADY"))
	}
	if scoreOf(t, shorts, "CRASHED") <= scoreOf(t, shorts, "STEADY") {
		t.Errorf("a name that just crashed is a worse short than one drifting down: CRASHED %+.4f vs STEADY %+.4f",
			scoreOf(t, shorts, "CRASHED"), scoreOf(t, shorts, "STEADY"))
	}
}

func TestMeritScoreCountsScoutAgreementAndDisagreement(t *testing.T) {
	// The 2026-09-03 cut ran through twelve nominations inside 1.1 z of each
	// other, so the two things the screening stage knew and the composite did
	// not were worth more than their size suggests: REGN at 0.702 and AMGN at
	// 1.094 were the only names two scouts agreed on and both were dropped, and
	// QCOM shipped bearish only because sp500's list was walked first.
	ps := &Prescreen{Rows: sortPrescreenRows([]PrescreenRow{
		row("REGN", "sp500", func(r *PrescreenRow) { r.Score = 0.702 }),
		row("QCOM", "sp500", func(r *PrescreenRow) { r.Score = -1.770 }),
	})}

	solo := model.Candidate{Ticker: "REGN", Index: "sp500", Bias: model.BiasBullish, Nominations: 1}
	if got := meritScore(ps, solo); math.Abs(got-0.702) > 1e-9 {
		t.Errorf("one nomination scored %+.3f, want the bare composite +0.702", got)
	}

	agreed := solo
	agreed.Nominations = 2
	if got := meritScore(ps, agreed); math.Abs(got-(0.702+meritAgreementBonus)) > 1e-9 {
		t.Errorf("two agreeing scouts scored %+.3f, want +%.3f", got, 0.702+meritAgreementBonus)
	}
	if meritScore(ps, agreed) <= meritScore(ps, solo) {
		t.Error("a name two scouts wanted did not outrank the same name one scout wanted")
	}

	contested := model.Candidate{Ticker: "QCOM", Index: "sp500", Bias: model.BiasBearish,
		Nominations: 1, Contested: []string{"nq100"}}
	want := 1.770 - meritContestedPenalty
	if got := meritScore(ps, contested); math.Abs(got-want) > 1e-9 {
		t.Errorf("a contested nomination scored %+.3f, want %+.3f", got, want)
	}
	// The penalty is a tie-break, not a veto: a strongly-supported contested
	// name still outranks a weak uncontested one.
	if meritScore(ps, contested) <= meritScore(ps, solo) {
		t.Error("a 1.77 contested read fell below a 0.70 uncontested one — the penalty is a veto")
	}
}

// The composite is built from trailing returns, so its top is by construction
// the names that have already run. These are the archetypes that give the scout
// something else to look at.
func TestClassifySetups(t *testing.T) {
	// Spread so the composite has a real sign for every row to be tested
	// against; see TestPrescreenReversalPenaltyNeverPaysABonus.
	anchors := []PrescreenRow{
		row("ANCHOR_HI", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 1.00; r.Ret63d = 0.50 }),
		row("ANCHOR_LO", "sp500", func(r *PrescreenRow) { r.Mom12_1 = -1.00; r.Ret63d = -0.50 }),
	}

	cases := []struct {
		name string
		want string
		mut  func(*PrescreenRow)
	}{
		{
			// The shape the old single table could never show: an uptrend
			// resting. AMGN and REGN shipped at 0.993 and 0.982 of their highs
			// while rows like this sat in the 187 the table printed as omitted.
			name: "uptrend dipping is a pullback",
			want: SetupPullback,
			mut: func(r *PrescreenRow) {
				r.Mom12_1, r.Ret63d = 0.45, 0.25
				r.Ret21d, r.PriceTo52wHigh, r.VolTrend = -0.06, 0.88, 1.05
			},
		},
		{
			// The bearish half, which is where a fresh short comes from: the
			// book has run 86 longs to 17 shorts.
			name: "downtrend bouncing is a pullback",
			want: SetupPullback,
			mut: func(r *PrescreenRow) {
				r.Mom12_1, r.Ret63d = -0.45, -0.25
				r.Ret21d, r.PriceTo52wHigh, r.VolTrend = 0.07, 0.62, 1.05
			},
		},
		{
			// A dip on expanding vol is an event, not a rest, and the levels a
			// trade would be built on are not stable.
			name: "dip on expanding vol is not a pullback",
			want: SetupContinuation,
			mut: func(r *PrescreenRow) {
				r.Mom12_1, r.Ret63d = 0.45, 0.25
				r.Ret21d, r.PriceTo52wHigh, r.VolTrend = -0.06, 0.88, 1.60
			},
		},
		{
			// Above the band the name has not actually pulled back.
			name: "still at the high is not a pullback",
			want: SetupContinuation,
			mut: func(r *PrescreenRow) {
				r.Mom12_1, r.Ret63d = 0.45, 0.25
				r.Ret21d, r.PriceTo52wHigh, r.VolTrend = -0.01, 0.99, 1.05
			},
		},
		{
			name: "vol contracting while price goes nowhere is a base",
			want: SetupBase,
			mut: func(r *PrescreenRow) {
				r.Mom12_1, r.Ret63d = 0.30, 0.10
				r.Ret21d, r.Stretch21, r.PriceTo52wHigh, r.VolTrend = 0.004, 0.10, 0.92, 0.80
			},
		},
		{
			// A base is a range tightening, and a name the variance ratio calls
			// trending is not in one.
			name: "contracting vol in a trending regime is not a base",
			want: SetupContinuation,
			mut: func(r *PrescreenRow) {
				r.Mom12_1, r.Ret63d = 0.30, 0.10
				r.Ret21d, r.Stretch21, r.PriceTo52wHigh, r.VolTrend = 0.004, 0.10, 0.92, 0.80
				r.Regime = "trending"
			},
		},
		{
			name: "a name that has simply run is continuation",
			want: SetupContinuation,
			mut: func(r *PrescreenRow) {
				r.Mom12_1, r.Ret63d = 0.47, 0.29
				r.Ret21d, r.Stretch21, r.PriceTo52wHigh, r.VolTrend = 0.10, 1.20, 0.993, 0.84
			},
		},
		{
			// The band is quoted against the 52-week high, and under a year of
			// bars there is not one to quote.
			name: "short history cannot be a pullback",
			want: SetupContinuation,
			mut: func(r *PrescreenRow) {
				r.Bars = 200
				r.Mom12_1, r.Ret63d = 0.45, 0.25
				r.Ret21d, r.PriceTo52wHigh, r.VolTrend = -0.06, 0.88, 1.05
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := append(append([]PrescreenRow{}, anchors...), row("SUBJ", "sp500", tc.mut))
			scorePrescreen(rows)
			for _, r := range rows {
				if r.Ticker == "SUBJ" && r.Setup != tc.want {
					t.Errorf("setup = %q, want %q (trend %+.2f, 21d %+.1f%%, p/52wH %.2f, volTrend %.2f)",
						r.Setup, tc.want, r.Trend, r.Ret21d*100, r.PriceTo52wHigh, r.VolTrend)
				}
			}
		})
	}
}

// An excluded row is not a candidate in any shape, and labelling it would let it
// compete for a reserved shortlist slot.
func TestClassifySetupsLeavesExcludedRowsUnlabelled(t *testing.T) {
	rows := []PrescreenRow{
		row("OK", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 0.40 }),
		row("THIN", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 0.40; r.Excluded = "illiquid" }),
	}
	scorePrescreen(rows)
	for _, r := range rows {
		if r.Ticker == "THIN" && r.Setup != "" {
			t.Errorf("excluded row carries setup %q, want none", r.Setup)
		}
		if r.Ticker == "OK" && r.Setup == "" {
			t.Error("scorable row carries no setup; Score and Setup must be set together")
		}
	}
}

// The extension penalty keyed on Mom12_1, which let the most extended shape in
// the table through untouched: a name whose last year was poor but whose last
// quarter was vertical has a negative Mom12_1 and a positive composite, so the
// sign test failed and nothing was charged. CRM on 2026-09-04 ran +37.0% in 21
// days at 0.986 of its 52-week high and was docked exactly nothing.
func TestExtensionPenaltyFollowsTheCompositeNotTheYearOldMomentum(t *testing.T) {
	mk := func(ticker string, mom, ret63, stretch float64) PrescreenRow {
		return row(ticker, "sp500", func(r *PrescreenRow) {
			r.Mom12_1, r.Ret63d, r.Stretch21 = mom, ret63, stretch
			// Hold the archetype fixed so this measures the penalty alone.
			r.PriceTo52wHigh, r.VolTrend, r.Ret21d = 0.99, 1.10, 0.30
		})
	}
	rows := []PrescreenRow{
		mk("ANCHOR_LO", -1.00, -0.50, 0),
		mk("CRM_LIKE", -0.24, 0.40, 1.60), // bad year, vertical quarter
		mk("CALM", -0.24, 0.40, 0.00),     // same trend, no extension
	}
	scorePrescreen(rows)

	crm, calm := scoreOf(t, rows, "CRM_LIKE"), scoreOf(t, rows, "CALM")
	if trendOf(t, rows, "CRM_LIKE") <= 0 {
		t.Fatalf("fixture: CRM_LIKE must carry a positive composite despite negative 12-1, got %+.4f",
			trendOf(t, rows, "CRM_LIKE"))
	}
	if crm >= calm {
		t.Errorf("a name up 1.6 sigma on the month scored at or above an unextended one on the same trend: %+.4f vs %+.4f", crm, calm)
	}
	if want := calm - stretch21Weight*1.60; math.Abs(crm-want) > 1e-9 {
		t.Errorf("penalty = %+.4f, want %+.4f (%.2f x 1.60 sigma)", calm-crm, calm-want, stretch21Weight)
	}
}

// A five-day window is shorter than the thing it is measuring. AMGN sat at
// 0.993 of its 52-week high on +47% 12-1 and +29% on the quarter, printed a
// +1.6% week, and was docked 0.095 of a point out of +1.82 — which the Chief
// then read as "extension risk is absent".
func TestStretch21SeesExtensionTheFiveDayTermMisses(t *testing.T) {
	// AMGN's own numbers: 21d +10% on sigma_daily 1.8%.
	if got := quant.Stretch(0.10, 0.018, 21); got < 1.0 {
		t.Errorf("stretch21 = %+.2f, want above 1.0 sigma — the 5d term read this same name at +0.19", got)
	}
	if got := quant.Stretch(0.10, 0, 21); got != 0 {
		t.Errorf("unknown sigma yielded %+.2f, want 0", got)
	}
}

// The scout used to be handed the top 15 and the bottom 5 of one ranking, with
// everything between them printed as "(mid-ranked names omitted)" — on
// 2026-09-04 that was 187 of 267 names, median 0.78-0.89 of their highs.
func TestPrescreenTableRendersEachArchetypeSection(t *testing.T) {
	rows := []PrescreenRow{
		row("RUNNER", "sp500", func(r *PrescreenRow) {
			r.Mom12_1, r.Ret63d, r.PriceTo52wHigh, r.VolTrend = 0.60, 0.30, 0.99, 1.10
		}),
		row("DIPPER", "sp500", func(r *PrescreenRow) {
			r.Mom12_1, r.Ret63d = 0.45, 0.25
			r.Ret21d, r.PriceTo52wHigh, r.VolTrend = -0.06, 0.88, 1.05
		}),
		row("COILED", "sp500", func(r *PrescreenRow) {
			r.Mom12_1, r.Ret63d = 0.30, 0.10
			r.Ret21d, r.Stretch21, r.PriceTo52wHigh, r.VolTrend = 0.004, 0.10, 0.92, 0.80
		}),
		// The counter-trend halves. BOUNCER is a downtrend that has just rallied
		// — the fresh short — and SETTLED is a downtrend that has stopped moving.
		row("BOUNCER", "sp500", func(r *PrescreenRow) {
			r.Mom12_1, r.Ret63d = -0.45, -0.25
			r.Ret21d, r.PriceTo52wHigh, r.VolTrend = 0.07, 0.62, 1.05
		}),
		row("SETTLED", "sp500", func(r *PrescreenRow) {
			r.Mom12_1, r.Ret63d = -0.30, -0.12
			r.Ret21d, r.Stretch21, r.PriceTo52wHigh, r.VolTrend = -0.004, -0.10, 0.80, 0.80
		}),
		row("SINKER", "sp500", func(r *PrescreenRow) {
			r.Mom12_1, r.Ret63d, r.PriceTo52wHigh, r.VolTrend = -0.70, -0.45, 0.40, 1.10
		}),
	}
	scorePrescreen(rows)
	ps := &Prescreen{Rows: sortPrescreenRows(rows)}
	// Sized to the fixture: at six names a 15-deep Continuation section would
	// swallow rows the later sections need, which is not what happens against an
	// index of fifty-odd.
	table := ps.Table("sp500", PrescreenParams{
		TopPerIndex: 1, PullbackPerIndex: 5, BasePerIndex: 3, BottomPerIndex: 1,
	})

	for _, want := range []string{
		"**Continuation**", "**Pullback (long)**", "**Pullback (short)**",
		"**Base (long)**", "**Base (short)**", "**Weakest**",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("table missing %s section:\n%s", want, table)
		}
	}
	// str21 is a scoring term now, so the scout has to be able to cite it.
	if !strings.Contains(table, "str21") {
		t.Errorf("table omits the str21 column:\n%s", table)
	}
	// Sections are disjoint: a name appears once, under its own archetype.
	for _, tk := range []string{"RUNNER", "DIPPER", "COILED", "BOUNCER", "SETTLED", "SINKER"} {
		if n := strings.Count(table, "| "+tk+" |"); n != 1 {
			t.Errorf("%s appears %d times, want exactly 1:\n%s", tk, n, table)
		}
	}
	// And each counter-trend name is under the half that matches its direction.
	for _, c := range []struct{ ticker, section string }{
		{"DIPPER", "Pullback (long)"}, {"BOUNCER", "Pullback (short)"},
		{"COILED", "Base (long)"}, {"SETTLED", "Base (short)"},
	} {
		if !inSection(table, c.section, c.ticker) {
			t.Errorf("%s is not under %s:\n%s", c.ticker, c.section, table)
		}
	}
}

// inSection reports whether ticker's row falls under the given section heading.
func inSection(table, heading, ticker string) bool {
	rest := table
	if i := strings.Index(rest, "**"+heading+"**"); i >= 0 {
		rest = rest[i+len(heading)+4:]
	} else {
		return false
	}
	if j := strings.Index(rest, "\n**"); j >= 0 {
		rest = rest[:j]
	}
	return strings.Contains(rest, "| "+ticker+" |")
}

// A section ranked by the composite is a long-only section, because the
// composite's sign *is* the direction: a bearish candidate's merit is -score,
// so the best shorts carry the most negative scores and sit at the far end of a
// best-first walk. Taking the first N of an archetype therefore returned its
// bullish half and nothing else. On 2026-09-04, 32 bearish pullbacks existed
// across the four indices and 8 were shown; sp500 showed 0 of its 11, its scout
// nominated six longs and no shorts, and ORCL (-2.84) and QCOM (-2.55) -- which
// as shorts would have been the two highest-merit names in the whole run -- were
// never put in front of it.
func TestPrescreenTableShowsBothSidesOfEachCounterTrendArchetype(t *testing.T) {
	var rows []PrescreenRow
	// Enough bullish pullbacks to fill the section on their own, which is what
	// used to crowd the bearish ones out entirely.
	for i, mom := range []float64{0.80, 0.75, 0.70, 0.65, 0.60, 0.55, 0.50} {
		rows = append(rows, row(fmt.Sprintf("UP%d", i), "sp500", func(r *PrescreenRow) {
			r.Mom12_1, r.Ret63d = mom, mom/2
			r.Ret21d, r.PriceTo52wHigh, r.VolTrend = -0.05, 0.88, 1.05
		}))
	}
	for i, mom := range []float64{-0.80, -0.60} {
		rows = append(rows, row(fmt.Sprintf("DOWN%d", i), "sp500", func(r *PrescreenRow) {
			r.Mom12_1, r.Ret63d = mom, mom/2
			r.Ret21d, r.PriceTo52wHigh, r.VolTrend = 0.06, 0.55, 1.05
		}))
	}
	scorePrescreen(rows)
	ps := &Prescreen{Rows: sortPrescreenRows(rows)}
	table := ps.Table("sp500", PrescreenParams{
		TopPerIndex: 1, PullbackPerIndex: 3, BasePerIndex: 3, BottomPerIndex: 0,
	})

	for _, tk := range []string{"DOWN0", "DOWN1"} {
		if !inSection(table, "Pullback (short)", tk) {
			t.Errorf("%s is a bearish pullback but is not in the short half:\n%s", tk, table)
		}
	}
	// The strongest short leads its own section, exactly as the strongest long
	// leads the other -- the bearish half is read from the bottom of the
	// ranking, so its trends run most-negative-first.
	prev := math.Inf(-1)
	for _, tr := range sectionTrends(table, "Pullback (short)") {
		if prev != math.Inf(-1) && tr < prev {
			t.Errorf("short half is not ordered strongest-first: %+.2f follows %+.2f\n%s", tr, prev, table)
		}
		prev = tr
	}
	// And the long half is still capped at its own size rather than borrowing
	// the short half's slots.
	if n := len(sectionTrends(table, "Pullback (long)")); n != 3 {
		t.Errorf("long half rendered %d rows, want 3:\n%s", n, table)
	}
}

// sectionTrends reads the trend column out of one section's rows, in order.
func sectionTrends(table, heading string) []float64 {
	i := strings.Index(table, "**"+heading+"**")
	if i < 0 {
		return nil
	}
	rest := table[i+len(heading)+4:]
	if j := strings.Index(rest, "\n**"); j >= 0 {
		rest = rest[:j]
	}
	var out []float64
	for _, ln := range strings.Split(rest, "\n") {
		if !strings.HasPrefix(ln, "| ") || strings.Contains(ln, "ticker |") || strings.Contains(ln, "---") {
			continue
		}
		f := strings.Split(ln, "|")
		if len(f) < 8 {
			continue
		}
		var v float64
		if _, err := fmt.Sscanf(strings.TrimSpace(f[7]), "%f", &v); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// The extension penalties are symmetric — a crashed name is a poor short for
// the same reason an extended one is a poor long — and symmetric means that for
// a negative composite both terms add. Unclamped they do not stop at "poor
// short": they carry the name across zero and rank it as a strong long. On the
// 2026-09-04 eu50 table ENEL.MI held a -0.47 composite and a -2.22 str21, and
// the bonuses lifted it to +1.42, first of forty-seven names — a buy generated
// entirely by having fallen.
func TestExtensionPenaltyCannotReverseTheTrend(t *testing.T) {
	anchors := []PrescreenRow{
		row("HI_1", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 1.00; r.Ret63d = 0.50 }),
		row("HI_2", "sp500", func(r *PrescreenRow) { r.Mom12_1 = 1.00; r.Ret63d = 0.50 }),
		row("LO_1", "sp500", func(r *PrescreenRow) { r.Mom12_1 = -1.00; r.Ret63d = -0.50 }),
		row("LO_2", "sp500", func(r *PrescreenRow) { r.Mom12_1 = -1.00; r.Ret63d = -0.50 }),
	}
	// ENEL.MI's shape: a mildly negative composite and a hard recent fall.
	rows := append(append([]PrescreenRow{}, anchors...),
		row("FALLEN", "sp500", func(r *PrescreenRow) {
			r.Mom12_1, r.Ret63d = 0.30, -0.20
			r.STRZ, r.Stretch21 = -1.8, -2.2
		}),
	)
	scorePrescreen(rows)

	trend, score := trendOf(t, rows, "FALLEN"), scoreOf(t, rows, "FALLEN")
	if trend >= 0 {
		t.Fatalf("fixture: FALLEN must carry a negative composite, got %+.4f", trend)
	}
	if score > 0 {
		t.Errorf("a name with a %+.2f composite scored %+.2f — the penalty turned a weak short into a long", trend, score)
	}
	if score < trend {
		t.Errorf("score %+.4f is below the unpenalised trend %+.4f; the bonus must move toward neutral, not away", score, trend)
	}
	// And it must still rank below anything genuinely positive.
	if score > scoreOf(t, rows, "HI_1") {
		t.Errorf("FALLEN %+.4f outranks a real uptrend %+.4f", score, scoreOf(t, rows, "HI_1"))
	}
}

// zscores guards against a sample with no spread, but compared sd against exact
// zero. Six copies of 0.4 sum to 2.4 and mean 0.39999999999999997, so the
// deviations are ~5.6e-17 and sd lands at 6.1e-17 — past the guard, and every
// member of the index then receives the same meaningless z of 0.913.
func TestZScoresTreatsAConstantSampleAsNoInformation(t *testing.T) {
	cases := map[string][]float64{
		"exactly representable": {0.5, 0.5, 0.5, 0.5, 0.5, 0.5},
		"with binary residue":   {0.4, 0.4, 0.4, 0.4, 0.4, 0.4},
		"large values":          {1234.56, 1234.56, 1234.56, 1234.56, 1234.56, 1234.56},
		// Winsorising clips one from each end at n>=5, so a lone outlier on
		// each side leaves a constant interior — the case that actually reached
		// this in a test fixture.
		"constant after winsorising": {1.0, -1.0, 0.4, 0.4, 0.4, 0.4},
	}
	for name, xs := range cases {
		t.Run(name, func(t *testing.T) {
			for i, z := range zscores(xs) {
				if z != 0 {
					t.Errorf("z[%d] = %v, want 0 — a sample with no spread carries no information", i, z)
				}
			}
		})
	}

	// The guard must not swallow a real, small spread.
	got := zscores([]float64{0.40, 0.41, 0.42, 0.43, 0.44, 0.45})
	if got[0] >= 0 || got[len(got)-1] <= 0 {
		t.Errorf("a genuine spread was flattened: %v", got)
	}
}
