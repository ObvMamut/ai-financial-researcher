package orchestrator

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// driftBars builds a flat series at 100 with one move of `jump` spread across
// the two sessions of the event window that opens on `reportIdx`, then `after`
// sessions of `postDaily` drift.
func driftBars(n, reportIdx int, jump, postDaily float64, after int) *quant.Series {
	s := &quant.Series{Symbol: "AAA"}
	price := 100.0
	day := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		switch {
		case i == reportIdx || i == reportIdx+1:
			price *= 1 + jump/2
		case i > reportIdx+1 && i <= reportIdx+1+after:
			price *= 1 + postDaily
		}
		s.Bars = append(s.Bars, quant.Bar{
			Date: day.Format("2006-01-02"),
			Open: price, High: price * 1.001, Low: price * 0.999, Close: price,
			Volume: 1_000_000,
		})
		day = day.AddDate(0, 0, 1)
	}
	return s
}

func TestComputeDriftSpansBothSidesOfTheFilingDay(t *testing.T) {
	// EDGAR's index carries the filing date and not the hour. A report filed
	// after the close is priced the *next* session, so measuring day 0 alone
	// would score every post-close filer at zero and read its pre-announcement
	// drift instead.
	s := driftBars(60, 40, 0.10, 0, 0)
	report := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 40)

	got, ok := computeDrift(s, nil, report, 0.02)
	if !ok {
		t.Fatal("computeDrift returned !ok on a well-formed series")
	}
	if got.GapZ <= 0 {
		t.Fatalf("GapZ = %+.2f, want positive for a +10%% reaction", got.GapZ)
	}
	// 10% over two sessions on a 2% daily σ is ~3.5σ.
	if math.Abs(got.GapZ-3.5) > 0.2 {
		t.Errorf("GapZ = %+.2f, want ≈ +3.5 (10%% over √2 × 2%%)", got.GapZ)
	}
}

func TestComputeDriftIsNetOfTheIndex(t *testing.T) {
	// A market that fell 5% on the day of a report did not say anything about
	// the report. Without the adjustment every filer in a bad week reads as a
	// disappointment.
	s := driftBars(60, 40, 0.06, 0, 0)
	bench := driftBars(60, 40, 0.06, 0, 0)
	bench.Symbol = "^BENCH"
	report := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 40)

	got, ok := computeDrift(s, bench, report, 0.02)
	if !ok {
		t.Fatal("computeDrift returned !ok")
	}
	if math.Abs(got.GapZ) > 0.05 {
		t.Errorf("GapZ = %+.2f, want ≈ 0 — the name moved exactly with its index", got.GapZ)
	}
}

func TestDriftScoreDecaysAcrossTheWindowAndDiesAtTheEnd(t *testing.T) {
	fresh := driftRead{GapZ: 4, Sessions: 0}
	mid := driftRead{GapZ: 4, Sessions: driftWindowSessions / 2}
	stale := driftRead{GapZ: 4, Sessions: driftWindowSessions + 1}

	if fresh.Score() <= mid.Score() {
		t.Errorf("a fresh report (%.2f) does not outrank a half-decayed one (%.2f)", fresh.Score(), mid.Score())
	}
	if math.Abs(mid.Score()-2) > 0.2 {
		t.Errorf("half-window score = %.2f, want ≈ 2 (half of a 4σ reaction)", mid.Score())
	}
	if stale.Score() != 0 {
		t.Errorf("score past the window = %.2f, want 0 — after a trading month it is not news", stale.Score())
	}
}

func TestDriftScoreIsZeroOnceTheReactionIsGivenBack(t *testing.T) {
	// There is nothing left to drift on, whatever the report said.
	given := driftRead{GapZ: 3, PostZ: -3.5, Sessions: 5}
	if !given.retraced() {
		t.Fatal("a reaction more than fully reversed is not reported as retraced")
	}
	if given.Score() != 0 {
		t.Errorf("score = %.2f, want 0", given.Score())
	}
	// A partial give-back is still a live setup.
	partial := driftRead{GapZ: 3, PostZ: -1, Sessions: 5}
	if partial.retraced() || partial.Score() == 0 {
		t.Errorf("a partially retraced reaction was written off: score %.2f", partial.Score())
	}
	// And a move *with* the reaction is drift working, not retracement.
	running := driftRead{GapZ: 3, PostZ: 2, Sessions: 5}
	if running.retraced() {
		t.Error("a reaction that kept going was reported as retraced")
	}
}

func TestIsDriftNeedsADatedReportAndARealReaction(t *testing.T) {
	live := PrescreenRow{ReportDate: "2026-08-26", GapZ: 3, DriftSessions: 4, Drift: 2.5}
	if !isDrift(live) {
		t.Error("a 3σ reaction four sessions old is not classified as drift")
	}
	// A name that reported and barely moved is not an event.
	quiet := PrescreenRow{ReportDate: "2026-08-26", GapZ: 0.4, DriftSessions: 4, Drift: 0.33}
	if isDrift(quiet) {
		t.Error("a 0.4σ reaction was classified as drift — that is an ordinary week")
	}
	// A name with no US filer has no report date and no drift signal at all,
	// rather than a wrong one.
	foreign := PrescreenRow{GapZ: 3, DriftSessions: 4, Drift: 2.5}
	if isDrift(foreign) {
		t.Error("a row with no report date was classified as drift")
	}
}

func TestClassifySetupsPutsDriftAheadOfTheTrailingReturnShapes(t *testing.T) {
	// A name that jumped on its own 10-Q last week is a name whose dominant fact
	// is the report. Labelling it "pullback" because its last month disagrees
	// with its last year names a mechanism that is not the one driving it, and
	// files it under a table the scout reads for a different reason.
	rows := []PrescreenRow{{
		Ticker: "AAA", Index: "sp500",
		// A textbook bullish pullback by every trailing-return test…
		Mom12_1: 0.5, Ret63d: 0.2, Ret21d: -0.05, PriceTo52wHigh: 0.9, VolTrend: 1.0,
		// …that also reported four sessions ago and jumped on it.
		ReportDate: "2026-08-26", GapZ: 3, DriftSessions: 4, Drift: 2.5,
	}}
	rows[0].Trend = 1.5
	classifySetups(rows)
	if rows[0].Setup != SetupDrift {
		t.Errorf("Setup = %q, want %q", rows[0].Setup, SetupDrift)
	}
}

func TestMeritMergesADriftCandidateOnItsDriftNotItsComposite(t *testing.T) {
	// The case that makes this necessary rather than tidy: a name that rallied
	// all year and then missed its quarter is a *short* with a strongly
	// positive composite. Merged on the composite its merit would be a large
	// negative number, so the single best short the event leg can find would
	// rank last — the drift table computed, shown to the scout, and then deleted
	// by the merge, exactly as happened to the pullback archetype.
	ps := &Prescreen{Rows: []PrescreenRow{{
		Ticker: "AAA", Index: "sp500", Setup: SetupDrift,
		Score: 2.8, Trend: 2.8, // ran all year
		ReportDate: "2026-08-26", GapZ: -3.2, DriftSessions: 5, Drift: -2.56, // and then missed
	}}}
	bearish := model.Candidate{Ticker: "AAA", Index: "sp500", Bias: model.BiasBearish, Setup: SetupDrift}

	got := meritComposite(ps, bearish)
	if got <= 0 {
		t.Fatalf("merit = %+.2f, want positive — the composite would have scored this -2.80", got)
	}
	if math.Abs(got-2.56) > 0.01 {
		t.Errorf("merit = %+.2f, want +2.56 (the drift score, sign-aligned)", got)
	}
}

func TestMeritStillUsesTheCompositeForEveryOtherArchetype(t *testing.T) {
	ps := &Prescreen{Rows: []PrescreenRow{{
		Ticker: "BBB", Index: "sp500", Setup: SetupPullback, Score: 1.9, Trend: 1.9,
		// Carrying stale drift numbers that no longer classify as an event.
		ReportDate: "2026-05-01", GapZ: 3, DriftSessions: 80, Drift: 0,
	}}}
	got := meritComposite(ps, model.Candidate{Ticker: "BBB", Index: "sp500", Bias: model.BiasBullish})
	if math.Abs(got-1.9) > 0.01 {
		t.Errorf("merit = %+.2f, want the composite +1.90", got)
	}
}

func TestDriftTableSplitsByTheReactionNotTheRanking(t *testing.T) {
	// The drift halves cannot reuse the split the other archetypes use. Those
	// are ranked by the composite and its sign is their direction, so walking
	// the ranking from one end yields one side. A drift name's side is the sign
	// of its own gap, and here both candidates sit at the *top* of the ranking.
	rows := []PrescreenRow{
		{Ticker: "BEAT", Index: "sp500", Name: "Beat Co", Close: 100, Score: 2.5, Trend: 2.5,
			Setup: SetupDrift, ReportDate: "2026-08-26", GapZ: 3, DriftSessions: 4, Drift: 2.5},
		{Ticker: "MISS", Index: "sp500", Name: "Miss Co", Close: 100, Score: 2.4, Trend: 2.4,
			Setup: SetupDrift, ReportDate: "2026-08-26", GapZ: -3, DriftSessions: 4, Drift: -2.5},
	}
	ps := &Prescreen{Rows: rows}
	table := ps.Table("sp500", defaultPrescreenParams())

	long := strings.Index(table, "Drift (long)")
	short := strings.Index(table, "Drift (short)")
	if long < 0 || short < 0 {
		t.Fatalf("both drift halves must render:\n%s", table)
	}
	if !strings.Contains(table[long:short], "BEAT") {
		t.Errorf("the long half does not carry BEAT:\n%s", table[long:short])
	}
	if !strings.Contains(table[short:], "MISS") {
		t.Errorf("the short half does not carry MISS — a name at the top of the ranking that fell on its report:\n%s", table[short:])
	}
	if strings.Contains(table[long:short], "MISS") {
		t.Error("MISS was rendered as a long: the split followed the composite instead of the reaction")
	}
}

func TestIsDriftReleasesANameOnceItsReportIsHistory(t *testing.T) {
	// Without a floor on the *decayed* score, a name stays classified as drift
	// for the full window however stale the event is — and because the
	// archetypes are disjoint and first-match-wins, that pulls it out of the
	// pullback and continuation tables while leaving it last in the drift one.
	// On the live sp500 ranking of 2026-09-04, eight of twelve drift names were
	// 22-24 sessions old and scored between -0.23 and +0.37. AAPL's real
	// -5.38σ reaction was 24 sessions old — history — and the classification
	// made it invisible in every table at once.
	stale := PrescreenRow{ReportDate: "2026-07-31", GapZ: -5.38, DriftSessions: 24}
	stale.Drift = driftRead{GapZ: stale.GapZ, Sessions: stale.DriftSessions}.Score()
	if isDrift(stale) {
		t.Errorf("a 24-session-old event still claims the drift archetype (decayed score %+.2f)", stale.Drift)
	}

	// A larger event stays relevant longer, which is the point of testing the
	// decayed score rather than the sessions elapsed.
	big := PrescreenRow{ReportDate: "2026-08-19", GapZ: 9.31, DriftSessions: 15}
	big.Drift = driftRead{GapZ: big.GapZ, Sessions: big.DriftSessions}.Score()
	if !isDrift(big) {
		t.Errorf("a 9σ reaction 15 sessions old was released (decayed score %+.2f)", big.Drift)
	}
}

func TestDriftBlockGivesTheChiefTheEventItCouldNotOtherwiseSee(t *testing.T) {
	// Until this block the drift leg was invisible above the funnel: it selected
	// a name and the Chief was handed the archetype label with no magnitude, no
	// date and no read on whether the move was still standing.
	ps := &Prescreen{Rows: []PrescreenRow{
		{Ticker: "CRM", Index: "sp500", Setup: SetupDrift, ReportDate: "2026-08-27",
			GapZ: 4.49, PostZ: 0.20, DriftSessions: 5, Drift: 3.59},
		{Ticker: "SNPS", Index: "nq100", Setup: SetupDrift, ReportDate: "2026-08-26",
			GapZ: 3.28, PostZ: -2.27, DriftSessions: 6, Drift: 2.50},
		{Ticker: "MU", Index: "nq100", Setup: SetupContinuation, Score: 1.65},
	}}
	shortlist := []model.Candidate{
		{Ticker: "MU", Index: "nq100"},
		{Ticker: "SNPS", Index: "nq100"},
		{Ticker: "CRM", Index: "sp500"},
	}

	got := driftBlock(ps, shortlist)
	if !strings.Contains(got, "CRM") || !strings.Contains(got, "2026-08-27") || !strings.Contains(got, "+4.49") {
		t.Errorf("the event's magnitude and date are missing:\n%s", got)
	}
	// The give-back has to be visible: a reaction two thirds returned is a
	// different proposition from one still standing, and the classifier's own
	// test only asks whether the *whole* move has gone.
	if !strings.Contains(got, "-2.27") {
		t.Errorf("SNPS's give-back is not shown:\n%s", got)
	}
	// A name that did not report carries no row rather than a row of zeroes.
	if strings.Contains(got, "MU") {
		t.Errorf("a name with no report date was given a line:\n%s", got)
	}
	// Ordered by the size of the live signal, so the strongest event leads.
	if strings.Index(got, "CRM") > strings.Index(got, "SNPS") {
		t.Errorf("rows are not ordered by |drift|:\n%s", got)
	}
}

func TestDriftBlockIsEmptyWithoutAPrescreen(t *testing.T) {
	// Single-stock mode skips Stage 0.5 entirely.
	if got := driftBlock(nil, []model.Candidate{{Ticker: "AAA"}}); got != "" {
		t.Errorf("got %q, want nothing without a pre-screen", got)
	}
}

// A date the app printed into a prompt must not read to the gate as an
// invention. checkFabricatedDates docks 10 points and spends the run's one
// corrective re-prompt for a date outside the verified set, and the drift block
// is built from the pre-screen — so nothing that walks the data packs sees it.
func TestDriftFilingDatesCountAsVerified(t *testing.T) {
	ps := &Prescreen{Rows: []PrescreenRow{
		{Ticker: "CRM", Index: "sp500", Setup: SetupDrift, ReportDate: "2026-08-27",
			GapZ: 4.49, PostZ: 0.20, DriftSessions: 5, Drift: 3.59},
		{Ticker: "MU", Index: "nq100", Setup: SetupContinuation, Score: 1.65},
		// Shortlisted nowhere, so its date never reaches a prompt and must not
		// be waved through either.
		{Ticker: "ZZZ", Index: "sp500", Setup: SetupDrift, ReportDate: "2019-01-02", GapZ: 2, Drift: 1},
	}}
	shortlist := []model.Candidate{{Ticker: "MU", Index: "nq100"}, {Ticker: "CRM", Index: "sp500"}}

	dates := map[string]bool{}
	collectDriftDates(dates, ps, shortlist)

	if !dates["2026-08-27"] {
		t.Errorf("CRM's filing date is not verified, but the block prints it: %v", dates)
	}
	if dates["2019-01-02"] {
		t.Errorf("a date no prompt carried was registered: %v", dates)
	}

	// And the gate agrees: the Chief quoting it keeps its confidence.
	idea := &model.TradeIdea{Ticker: "CRM", Confidence: 50,
		Why: "reported 2026-08-27 and gapped +4.49σ; the reaction is still standing"}
	if f := checkFabricatedDates(idea, dates); len(f) != 0 {
		t.Errorf("quoting the block's own date was flagged: %+v", f)
	}
	if idea.Confidence != 50 {
		t.Errorf("confidence = %d, want 50 — no penalty for a date the app printed", idea.Confidence)
	}
}

// Single-stock mode skips Stage 0.5, so there is no pre-screen to walk.
func TestCollectDriftDatesToleratesNoPrescreen(t *testing.T) {
	dates := map[string]bool{}
	collectDriftDates(dates, nil, []model.Candidate{{Ticker: "AAA"}})
	if len(dates) != 0 {
		t.Errorf("got %v, want nothing without a pre-screen", dates)
	}
}

// The fundamentals domain's own evidence is a single-period filing snapshot and
// one year-over-year growth rate — a description of a company, on a horizon
// where the documented fundamental effect is post-earnings drift. Without the
// reaction it had nothing horizon-matched to score, so it scored the multiple:
// bearish on 6 of 7 covered names on 2026-09-05, a standing ~13-point levy on
// every momentum long at 18% of the weight.
func TestFundamentalsSeesTheEarningsReactionAndTheBlindedDomainsDoNot(t *testing.T) {
	ps := &Prescreen{Rows: []PrescreenRow{
		{Ticker: "CRM", Index: "sp500", Setup: SetupDrift, ReportDate: "2026-08-27",
			GapZ: 4.49, PostZ: 0.20, DriftSessions: 5, Drift: 3.59},
	}}
	shortlist := []model.Candidate{{Ticker: "CRM", Index: "sp500"}}
	qp := quant.NewPack()
	qp.ByTicker["CRM"] = quant.Metrics{Symbol: "CRM", LastClose: 259.23, AsOf: "2026-09-04", SigmaDaily: 0.02}
	pack := &marketdata.DataPack{Domain: "fundamentals"}

	const heading = "Verified earnings reactions"
	if got := specialistDataBlock("fundamentals", pack, qp, ps, shortlist); !strings.Contains(got, heading) ||
		!strings.Contains(got, "2026-08-27") || !strings.Contains(got, "+4.49") {
		t.Errorf("fundamentals did not receive the reaction table:\n%s", got)
	}

	// Quant and macro are blinded (agents.blindToDirection) precisely so they do
	// not read the reason a name was selected back to the Chief as independent
	// confirmation. The reaction is that reason for a drift name.
	for _, role := range []string{"quant", "macro", "news", "sentiment"} {
		p := &marketdata.DataPack{Domain: role}
		if got := specialistDataBlock(role, p, qp, ps, shortlist); strings.Contains(got, heading) {
			t.Errorf("%s received the reaction table; only fundamentals and the Chief read it:\n%s", role, got)
		}
	}
}
