package backtest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/orchestrator"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// Loader is the price seam: marketdata.YahooClient satisfies it. The lab reads
// long ranges through the shared cache and never through a model.
type Loader interface {
	HistoryRange(ctx context.Context, symbol, rng string, maxAge time.Duration) (*quant.Series, error)
}

// Config is one replay's settings.
type Config struct {
	Years       int           // rebalance span; default DefaultYears
	Indices     []string      // default: all four
	CacheMaxAge time.Duration // how old a cached long series may be; default 7 days
	Now         time.Time     // the replay ends on the last Friday on or before it
	Log         func(string)  // progress lines; may be nil
	// Filings resolves US earnings-release dates for the drift and
	// earn_window signals. nil — no SEC contact address — leaves both NaN and
	// says so in the report; everything else is unchanged.
	Filings marketdata.FilingHistorySource
	// HoldoutAfter is the last in-sample rebalance date: later dates are held
	// out of every ordinary replay. Zero means OOSHoldoutAfter.
	HoldoutAfter time.Time
	// EvaluateOOS is the registered out-of-sample run: only the held-out dates,
	// and only once OOSMatureDates of them have a matured 63-session window.
	EvaluateOOS bool
	// Universe is "pit" for the point-in-time universe; anything else is
	// today's sample.
	Universe string
	// AsOf prices point-in-time members (marketdata.AlpacaPrices). Required
	// in the point-in-time universe.
	AsOf AsOfLoader
	// Histories and Sectors override the embedded membership histories and
	// sector map (tests); nil loads the embedded ones.
	Histories map[string]*universe.History
	Sectors   map[string]string
}

// OOSHoldoutAfter is the last rebalance date the 2026-10-01 horizon run used.
// OOS-H1-63 (docs/workflow/backtest.md, registered 2026-10-07) is evaluated
// on later dates only, once, so no ordinary replay may read them first.
var OOSHoldoutAfter = time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)

// OOSMatureDates is how many held-out rebalance dates need a matured
// 63-session window before OOS-H1-63 may be computed.
const OOSMatureDates = 52

// OOSUniverse names the universe OOS-H1-63 registered: the four index files
// as they stood at registration, embedded byte for byte by
// universe.LoadFrozen. The live samples change after it and are refused.
const OOSUniverse = "oos-h1-63"

// DefaultYears is Config.Years' default, and the threshold LongHistorySurvivorship
// is measured against: past it, the per-year report (E1) is showing more history
// than the halves above it do.
const DefaultYears = 4

// holmAlpha is the Holm-adjusted p threshold the text report counts against.
const holmAlpha = 0.05

// Survivorship is printed on every report: the universe files are today's
// constituents, so names that fell out of the indices over the replay — the
// losers, disproportionately — are missing from every past cross-section.
const Survivorship = "SURVIVORSHIP: the universe is today's constituents only. Names that left the indices during the replay are absent from every past date, which flatters momentum and long-side returns. Treat every positive number here as an upper bound."

// LongHistorySurvivorship is printed alongside the per-year table whenever
// --years exceeds DefaultYears: Survivorship above worsens the further back the
// replay goes, because the names missing from an older cross-section are
// disproportionately past losers, not a random sample of departures (plan §7,
// E1's risk note). It says to read the per-year IC and beta-adjusted top-5
// excess over the raw long-only returns in the barrier study, not on their own.
const LongHistorySurvivorship = "LONG HISTORY: --years exceeds the default, so the survivorship above is worse than in the standard replay — the deeper a cross-section sits in the past, the more its missing names are past losers rather than a random sample. Weight the per-year IC and beta-adjusted top-5 excess over the barrier study's raw long-only returns; a good early year built mostly of today's survivors is a weaker signal than the same year would be with its casualties still in it."

// Departures lists where the lab's pre-screen knowingly differs from a live run.
var Departures = []string{
	"no liquidity floor: a USD turnover needs the FX rate at each past date, and converting at today's rate would be a look-ahead",
	"no drift archetype: row.ReportDate stays unset, as in a live run without an SEC contact address, so the composite and archetypes are unchanged; drift is measured instead as its own signal, from US 8-K Item 2.02 release dates (live: 10-Q/10-K filing dates)",
	"a name is scored only with the full 253 bars the 12-1 term needs (live: 60), and only in a week it printed a bar",
	"prices are Yahoo's adjusted daily bars for every symbol (live runs route US equities through Alpaca when keyed)",
}

// Result is the lab's report, written as JSON under .data/backtest/.
type Result struct {
	GeneratedAt   string         `json:"generated_at"`
	Range         string         `json:"yahoo_range"`
	Years         int            `json:"years"`
	Start         string         `json:"first_date"`
	End           string         `json:"last_date"`
	Mid           string         `json:"second_half_starts"`
	Dates         int            `json:"dates"`
	Rows          int            `json:"rows"`
	NamesPerIndex map[string]int `json:"names_per_index"`
	Unavailable   []string       `json:"unavailable,omitempty"`
	// RequestedStart is end - years, what --years asked the replay to reach
	// back to. Shortfall is set when Start falls materially later than that —
	// see checkShortfall.
	RequestedStart string `json:"requested_start,omitempty"`
	Shortfall      bool   `json:"shortfall,omitempty"`
	ShortfallNote  string `json:"shortfall_note,omitempty"`
	// FilingsNote says why the earnings signals are NaN throughout (no SEC
	// contact address); FilingsUnavailable names the US tickers whose filing
	// history could not be resolved, whose earnings signals are NaN.
	// HoldoutNote says the replay stopped at the OOS holdout date.
	HoldoutNote        string   `json:"holdout_note,omitempty"`
	FilingsNote        string   `json:"filings_note,omitempty"`
	FilingsUnavailable []string `json:"filings_unavailable,omitempty"`
	Survivorship       string   `json:"survivorship"`
	// Universe is "pit" when the replay used point-in-time membership.
	Universe   string   `json:"universe,omitempty"`
	Departures []string `json:"departures"`
	CostNote   string   `json:"cost_note"`
	// Signals maps a slice label (all, US, EU, Asia, H1, H2) to every signal's
	// stats against benchmark-excess returns; BetaAdjusted is the same against
	// r − β·r_bench (C4).
	Signals      map[string][]SignalStats `json:"signals"`
	BetaAdjusted map[string][]SignalStats `json:"beta_adjusted"`
	// PerYear is the composite's IC10/IC15 (plain and beta-adjusted) and the
	// barrier study's top-5 excess (plain and beta-adjusted), one entry per
	// calendar year (E1, docs/workflow/backtest.md).
	PerYear []YearStats   `json:"per_year"`
	Barrier BarrierReport `json:"barrier"`
	// Sides is E3: the barrier study's own picks, split long vs short, plain
	// and beta-adjusted, overall and per half (docs/workflow/backtest.md).
	Sides SidesReport `json:"sides"`
	// BookGrid is E2's live-shaped weekly book replayed under a grid of
	// max_per_sector values (book.go): lead 2 asked whether the sector cap
	// that dropped ORCL for SAP.DE costs or saves the book, and one run cannot
	// answer that — this replays the question across every week in the panel.
	BookGrid      BookGrid     `json:"book_grid"`
	Preregistered []TestResult `json:"preregistered"`
	// Decisions is E1 and E3's registered rules applied to PerYear and Sides.
	Decisions []TestResult `json:"decisions"`
	// USScoped is the US-scoped block (scoped.go): the registered Wave D tests
	// — D1, drift's beta-adjusted IC10 over the pooled sp500 ∪ nq100
	// cross-section; D2, the top-5-by-|drift| book; D3, earn_window's IC10 —
	// each against the scoped bar and counted in TestsRun.
	USScoped ScopedReport `json:"us_scoped"`
	// Horizon is the v5 block (horizon.go): the pre-registered horizon tests
	// (H1 and H2), counted in TestsRun.
	Horizon HorizonReport `json:"horizon"`
	// TestsRun counts every registered test this run performed — C-series,
	// E2's paired tests, the E1/E3 decisions, D1–D3 and the v5 horizon tests — so it equals the register in
	// docs/workflow/backtest.md, one look each per run. E1 counts only at
	// --years 10 or more (e1DecisionMinYears); below that its per-year table
	// is a comparison look, not a decision, and Status is "comparison" rather
	// than "run".
	TestsRun int `json:"tests_run"`
	// MultipleTesting is the Holm adjustment over exactly the TestsRun tests
	// (holm.go); reported, never a gate.
	MultipleTesting MultipleTesting `json:"multiple_testing"`
}

const costNote = "IC is per-date Spearman within index vs forward benchmark-excess return, averaged across indices per date. Quintile spreads pay 30bp round trip on each leg (60bp); top-quintile and barrier trades pay 30bp once."

// yahooRange is the shortest Yahoo range covering the replay plus the year of
// history the first rebalance needs. Spans "5y"/"10y" cannot cover are spelled
// "<years>y" — not Yahoo's own range= vocabulary — which
// marketdata.YahooClient.chartSpan reads as a request for an explicit
// period1/period2 window instead of range=max: probed 2026-09-25, Yahoo
// answers range=max&interval=1d with meta.dataGranularity="3mo" (169 bars back
// to 1984 for AAPL), which starved every rebalance after the first ~2.3 years
// of the 253 daily bars the 12-1 momentum term needs and silently shrank a
// requested 10-year replay to 20 weekly rebalances.
func yahooRange(years int) string {
	switch {
	case years+1 <= 5:
		return "5y"
	case years+1 <= 10:
		return "10y"
	}
	return fmt.Sprintf("%dy", years+1)
}

// Run fetches (cache-first) every constituent and benchmark, builds the panel
// and analyses it.
func Run(ctx context.Context, loader Loader, uni *universe.Universe, cfg Config) (*Result, error) {
	if cfg.Years <= 0 {
		cfg.Years = DefaultYears
	}
	if len(cfg.Indices) == 0 {
		cfg.Indices = universe.AllIndices()
	}
	if cfg.CacheMaxAge <= 0 {
		cfg.CacheMaxAge = 7 * 24 * time.Hour
	}
	if cfg.Now.IsZero() {
		cfg.Now = time.Now()
	}
	logf := func(format string, args ...any) {
		if cfg.Log != nil {
			cfg.Log(fmt.Sprintf(format, args...))
		}
	}
	rng := yahooRange(cfg.Years)
	pit := cfg.Universe == UniversePIT
	if pit && cfg.AsOf == nil {
		return nil, fmt.Errorf("the point-in-time universe needs an asof price source (Alpaca keys)")
	}

	if cfg.EvaluateOOS && uni.Frozen() != OOSUniverse {
		return nil, fmt.Errorf("OOS-H1-63 is evaluated on the universe frozen at its registration (universe.LoadFrozen(%q)), not the live samples", OOSUniverse)
	}

	holdout := cfg.HoldoutAfter
	if holdout.IsZero() {
		holdout = OOSHoldoutAfter
	}
	end := lastFriday(cfg.Now)
	start := end.AddDate(-cfg.Years, 0, 0)
	var holdoutNote string
	switch {
	case cfg.EvaluateOOS:
		start = holdout.AddDate(0, 0, 1)
	case end.After(holdout):
		end = holdout
		start = end.AddDate(-cfg.Years, 0, 0)
		holdoutNote = fmt.Sprintf("HOLDOUT: rebalance dates after %s are reserved for OOS-H1-63 and are not replayed; the one evaluation runs with --evaluate-oos once %d of them have matured", holdout.Format("2006-01-02"), OOSMatureDates)
	}
	dates := WeeklyDates(start, end)
	if len(dates) == 0 {
		return nil, fmt.Errorf("no rebalance dates between %s and %s", start.Format("2006-01-02"), end.Format("2006-01-02"))
	}

	var members []Member
	var sectors map[string]string
	for _, idx := range cfg.Indices {
		cs := uni.Constituents(idx)
		if len(cs) == 0 {
			return nil, fmt.Errorf("unknown or empty index %q", idx)
		}
		if pit {
			h := cfg.Histories[idx]
			if h == nil && hasHistory(idx) {
				var err error
				if h, err = universe.LoadHistory(idx); err != nil {
					return nil, err
				}
			}
			if h != nil {
				if sectors == nil {
					if sectors = cfg.Sectors; sectors == nil {
						var err error
						if sectors, err = universe.HistorySectors(); err != nil {
							return nil, err
						}
					}
				}
				members = append(members, pitMembers(idx, h, cs, sectors, dates[0], dates[len(dates)-1])...)
				continue
			}
		}
		for _, c := range cs {
			members = append(members, Member{Constituent: c, Bench: universe.BenchmarkFor(idx, c.Ticker)})
		}
	}

	data := Data{Series: map[string]*quant.Series{}, Bench: map[string]*quant.Series{}}
	var unavailable []string
	fetch := func(sym string) *quant.Series {
		s, err := loader.HistoryRange(ctx, sym, rng, cfg.CacheMaxAge)
		if err != nil {
			unavailable = append(unavailable, fmt.Sprintf("%s: %v", sym, err))
			return nil
		}
		return s
	}
	for _, m := range members {
		if _, ok := data.Bench[m.Bench]; !ok {
			data.Bench[m.Bench] = fetch(m.Bench)
		}
	}
	// Every registered statistic is beta-adjusted against these series. A
	// benchmark that failed to load turns each into NaN while a report still
	// prints, and a printed report spends a registration; stop instead (the
	// one-run rule lets a crash before any statistic be repeated).
	for b, s := range data.Bench {
		if s == nil || len(s.Bars) == 0 {
			return nil, fmt.Errorf("benchmark %s is unavailable, so nothing is computed: %s", b, strings.Join(unavailable, "; "))
		}
	}
	for i, m := range members {
		if m.Interval != nil {
			continue // priced through the asof source below
		}
		t := m.SeriesKey()
		if _, ok := data.Series[t]; ok {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data.Series[t] = fetch(m.Constituent.Ticker)
		if (i+1)%50 == 0 {
			logf("prices: %d of %d members", i+1, len(members))
		}
	}
	if pit {
		// Warm-up: a full momentum window before the first rebalance.
		from := dates[0].AddDate(-1, -3, 0)
		missing, err := loadPIT(ctx, cfg.AsOf, members, data, from, cfg.Now)
		if err != nil {
			return nil, err
		}
		unavailable = append(unavailable, missing...)
	}
	logf("prices: %d symbols, %d unavailable", len(data.Series)+len(data.Bench), len(unavailable))

	var filingsNote string
	var filingsUnavailable []string
	switch {
	case pit:
		// Filing histories are keyed by today's ticker; a departed or reused
		// one would resolve to the wrong filer.
		filingsNote = pitFilingsNote
	case cfg.Filings == nil:
		filingsNote = noFilingsNote
	default:
		data.Filings, filingsUnavailable = loadFilings(ctx, cfg.Filings, members, dates[0].AddDate(0, 0, -filingLookbackDays))
		logf("filings: %d US names resolved, %d unavailable", len(data.Filings), len(filingsUnavailable))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	recs := DropWarmupDates(BuildPanel(members, data, dates))
	if len(recs) == 0 {
		return nil, fmt.Errorf("the panel is empty: no member had %d bars at any rebalance date", minHistory)
	}
	logf("panel: %d rows", len(recs))
	if cfg.EvaluateOOS {
		if n := maturedDates(recs, len(Horizons)-1); n < OOSMatureDates {
			return nil, fmt.Errorf("OOS-H1-63 is not mature: %d of %d held-out rebalance dates after %s have a matured %d-session window, so nothing is computed",
				n, OOSMatureDates, holdout.Format("2006-01-02"), Horizons[len(Horizons)-1])
		}
	}
	res := Analyze(recs, data.Series, cfg.Years)
	res.GeneratedAt = cfg.Now.UTC().Format(time.RFC3339)
	res.Range, res.Unavailable = rng, unavailable
	res.FilingsNote, res.FilingsUnavailable = filingsNote, filingsUnavailable
	res.HoldoutNote = holdoutNote
	if pit {
		res.Universe, res.Survivorship = UniversePIT, PITSurvivorship
	}
	checkShortfall(res, cfg.Years, end)
	return res, nil
}

// pitFilingsNote is the report's line in the point-in-time universe.
const pitFilingsNote = "point-in-time universe: earnings-release dates are not fetched (SEC resolves today's tickers only), so drift and earn_window are NaN for every name"

// noFilingsNote is the report's line when no filing source was configured.
const noFilingsNote = "no SEC contact_email configured: no earnings-release dates were fetched, so drift and earn_window are NaN for every name"

// filingLookbackDays is how far before the first rebalance the filing history
// reaches: past earnWindow's longest reach back (a release up to
// earnCadenceDays+earnSlackDays before the date still predicts one), so the
// first rebalance reads the same history every later one does.
const filingLookbackDays = 183

// loadFilings fetches the earnings-release dates of every US member (sp500 and
// nq100 samples), each ticker once. Non-US members are never asked for: SEC
// has nothing on a foreign listing, and their earnings signals stay NaN. A
// ticker the source could not resolve is absent and named in the warnings.
func loadFilings(ctx context.Context, src marketdata.FilingHistorySource, members []Member, since time.Time) (map[string][]time.Time, []string) {
	var tickers []string
	seen := map[string]bool{}
	for _, m := range members {
		t := strings.ToUpper(m.Constituent.Ticker)
		if Region(m.Constituent.Index) != "US" || seen[t] {
			continue
		}
		seen[t] = true
		tickers = append(tickers, t)
	}
	if len(tickers) == 0 {
		return nil, nil
	}
	hist, warnings := src.FilingHistory(ctx, tickers, since)
	out := make(map[string][]time.Time, len(hist))
	for t, h := range hist {
		out[strings.ToUpper(t)] = h.Earnings
	}
	return out, warnings
}

// shortfallWarnWeeks is how much later than requested the first surviving
// rebalance may fall before checkShortfall calls it out. 8 weeks comfortably
// clears the ordinary slop from holidays and DropWarmupDates' 70% threshold —
// the 2026-09-23 run's default 4-year replay missed its requested start by
// only 19 days (2022-10-07 vs 2022-09-18) — while catching the Task 5b bug,
// which shrank a 10-year request to about 2.3 years (roughly 400 weeks short).
const shortfallWarnWeeks = 8

// checkShortfall flags a replay whose first surviving rebalance date falls
// materially later than end - years, the way range=max silently did for spans
// "5y"/"10y" couldn't cover (see yahooRange): Yahoo answered 3-month bars, few
// enough names ever reached the 253-bar minimum, and DropWarmupDates was left
// dropping most of the requested window rather than just its first year's
// ordinary warm-up. Nothing else in the pipeline would have reported that —
// Run still returns a populated, internally consistent Result.
func checkShortfall(res *Result, years int, end time.Time) {
	requested := end.AddDate(-years, 0, 0)
	res.RequestedStart = requested.Format("2006-01-02")
	if res.Start == "" {
		return
	}
	actual, err := time.Parse("2006-01-02", res.Start)
	if err != nil {
		return
	}
	gap := actual.Sub(requested)
	if gap <= shortfallWarnWeeks*7*24*time.Hour {
		return
	}
	res.Shortfall = true
	res.ShortfallNote = fmt.Sprintf(
		"replay covers only %s..%s (%d weekly rebalances) — %.0f weeks short of the %d year(s) requested (wanted from %s)",
		res.Start, res.End, res.Dates, gap.Hours()/(7*24), years, res.RequestedStart)
}

// Analyze turns a filtered panel into the report. Exposed for tests, which
// build panels from synthetic series. replayYears is the run's requested
// --years (Config.Years), recorded on the result and used to gate E1's
// decision (e1Decision, e1DecisionMinYears).
func Analyze(recs []Record, series map[string]*quant.Series, replayYears int) *Result {
	cells := crossSections(recs)
	mid := midDate(cells)
	res := &Result{
		Mid: mid, Rows: len(recs), NamesPerIndex: map[string]int{}, Years: replayYears,
		Survivorship: Survivorship, Departures: Departures, CostNote: costNote,
		Signals: map[string][]SignalStats{}, BetaAdjusted: map[string][]SignalStats{},
	}
	dates := map[string]bool{}
	names := map[string]map[string]bool{}
	for _, r := range recs {
		dates[r.Date] = true
		if names[r.Index] == nil {
			names[r.Index] = map[string]bool{}
		}
		names[r.Index][r.Ticker] = true
		if res.Start == "" || r.Date < res.Start {
			res.Start = r.Date
		}
		if r.Date > res.End {
			res.End = r.Date
		}
	}
	res.Dates = len(dates)
	for idx, ns := range names {
		res.NamesPerIndex[idx] = len(ns)
	}
	for _, sl := range slices(mid) {
		res.Signals[sl.Label] = summarize(cells, sl.Keep, false)
		res.BetaAdjusted[sl.Label] = summarize(cells, sl.Keep, true)
	}
	trades := pickTrades(recs, series)
	res.Barrier = barrierStudy(trades, mid)
	res.PerYear = perYearStats(cells, trades)
	res.Sides = sidesStudy(trades, mid)
	res.BookGrid = BuildBookGrid(recs, mid)
	res.Preregistered = preregistered(cells, mid)
	res.Decisions = []TestResult{e1Decision(res.PerYear, replayYears), e3Decision(res.Sides)}
	res.USScoped = usScoped(recs, mid)
	res.Horizon = horizonTests(recs, cells, mid)
	// One iteration is both the count and the Holm family: a test that did not
	// run has no p-value and is outside both.
	var family []*TestResult
	for _, list := range [][]TestResult{res.Preregistered, res.BookGrid.PairedTests, res.Decisions, res.USScoped.Tests, res.Horizon.Tests} {
		for i := range list {
			t := &list[i]
			if t.Status == "run" {
				res.TestsRun++
				family = append(family, t)
			} else {
				t.P, t.PHolm = Num(math.NaN()), Num(math.NaN())
			}
		}
	}
	res.MultipleTesting = buildHolm(family)
	return res
}

// Save writes the report as <dir>/<timestamp>.json and returns the path.
func (r *Result) Save(dir string, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, now.UTC().Format("2006-01-02T15-04-05")+".json")
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, b, 0o644)
}

// Text renders the human summary.
func (r *Result) Text() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Backtest lab — %d weekly rebalances, %s .. %s (second half from %s); %d rows\n",
		r.Dates, r.Start, r.End, r.Mid, r.Rows)
	if r.Shortfall {
		fmt.Fprintf(&sb, "WARNING: %s\n", r.ShortfallNote)
	}
	if r.HoldoutNote != "" {
		sb.WriteString(r.HoldoutNote + "\n")
	}
	idx := make([]string, 0, len(r.NamesPerIndex))
	for k := range r.NamesPerIndex {
		idx = append(idx, k)
	}
	sort.Strings(idx)
	sb.WriteString("names per index:")
	for _, k := range idx {
		fmt.Fprintf(&sb, " %s %d", k, r.NamesPerIndex[k])
	}
	sb.WriteString("\n\n" + r.Survivorship + "\n\n")

	table := func(title string, rows []SignalStats) {
		fmt.Fprintf(&sb, "=== %s ===\n", title)
		fmt.Fprintf(&sb, "%-16s %7s %7s %7s %7s %7s %7s %7s %7s %7s %7s %7s %7s %8s %8s %7s\n",
			"signal", "IC5", "tNW5", "IC10", "IR10", "tNW10", "IC15", "tNW15", "IC21", "tNW21", "IC63", "tNW63", "QS10%", "QS10net", "TopQnet", "tQS10")
		for _, s := range rows {
			fmt.Fprintf(&sb, "%-16s %7.3f %7.2f %7.3f %7.3f %7.2f %7.3f %7.2f %7.3f %7.2f %7.3f %7.2f %7.3f %8.3f %8.3f %7.2f\n",
				s.Signal, s.IC[0], s.TNW[0], s.IC[1], s.IR[1], s.TNW[1], s.IC[2], s.TNW[2], s.IC[3], s.TNW[3], s.IC[4], s.TNW[4],
				s.QS10Pct, s.QS10NetPct, s.Top10NetPct, s.TQS10)
		}
		sb.WriteString("\n")
	}
	for _, l := range []string{"all", "US", "EU", "Asia", "H1", "H2"} {
		table("benchmark-excess, "+l, r.Signals[l])
	}
	table("beta-adjusted excess (C4), all", r.BetaAdjusted["all"])
	sb.WriteString(r.CostNote + "\n\n")

	if len(r.PerYear) > 0 {
		fmt.Fprintf(&sb, "=== Per-calendar-year (E1: long history) ===\n")
		fmt.Fprintf(&sb, "%-6s %8s %7s %9s %7s %9s %7s %8s %10s %9s %13s\n",
			"year", "n_dates", "IC10", "IC10beta", "IC15", "IC15beta", "top5n", "top5%", "top5beta%", "top5net%", "top5betanet%")
		for _, y := range r.PerYear {
			year := y.Year
			if !y.FullYear {
				year += "*"
			}
			fmt.Fprintf(&sb, "%-6s %8d %7.3f %9.3f %7.3f %9.3f %7d %8.3f %10.3f %9.3f %13.3f\n",
				year, y.NDates, y.IC10, y.IC10Beta, y.IC15, y.IC15Beta, y.Top5N, y.Top5Pct, y.Top5BetaPct, y.Top5NetPct, y.Top5BetaNetPct)
		}
		sb.WriteString("* partial year. top5% columns are gross; the net columns pay the barrier study's 30bp once.\n")
		if r.Years > DefaultYears {
			sb.WriteString(LongHistorySurvivorship + "\n")
		}
		sb.WriteString("\n")
	}

	b := r.Barrier
	fmt.Fprintf(&sb, "=== Barrier study: top-%d |composite| per index per week, next-open entry, H=%d, 30bp ===\n", picksPerIndex, barrierHorizon)
	fmt.Fprintf(&sb, "trades %d (longs %d)\n", b.Trades, b.Longs)
	fmt.Fprintf(&sb, "%-32s %8s %8s %8s %8s %8s %6s %6s %6s %6s\n", "exit rule", "all%", "H1%", "H2%", "long%", "short%", "hit", "stop", "tgt", "time")
	for _, a := range b.Arms {
		fmt.Fprintf(&sb, "%-32s %8.3f %8.3f %8.3f %8.3f %8.3f %6.3f %6.3f %6.3f %6.3f\n", a.Rule.Label,
			a.All.MeanPct, a.H1.MeanPct, a.H2.MeanPct, a.Long.MeanPct, a.Short.MeanPct,
			a.All.Hit, a.All.StopShare, a.All.TgtShare, a.All.TimeShare)
	}
	fmt.Fprintf(&sb, "same picks, 15-session directional benchmark excess (gross): %.3f%% (long %.3f%%, short %.3f%%)\n\n",
		b.XS15GrossPct, b.XS15LongGrossPct, b.XS15ShortGrossPct)

	fmt.Fprintf(&sb, "=== E3: same picks' %d-session excess by side, plain / beta-adjusted (gross) ===\n", barrierHorizon)
	fmt.Fprintf(&sb, "%-5s %8s %8s %8s %8s %6s %6s\n", "slice", "long%", "short%", "longBX%", "shortBX%", "nLong", "nShort")
	sideRow := func(label string, s SideStats) {
		fmt.Fprintf(&sb, "%-5s %8.3f %8.3f %8.3f %8.3f %6d %6d\n", label, s.LongPlainPct, s.ShortPlainPct, s.LongBetaPct, s.ShortBetaPct, s.NLong, s.NShort)
	}
	sideRow("all", r.Sides.All)
	sideRow("H1", r.Sides.H1)
	sideRow("H2", r.Sides.H2)
	sb.WriteString("\n")

	g := r.BookGrid
	fmt.Fprintf(&sb, "=== E2: sector-cap grid (nominations/index %d, max_per_index %d, top %d, H=%d) ===\n",
		g.NominationsPerIndex, g.MaxPerIndex, picksPerIndex, Horizons[bookHorizon])
	fmt.Fprintf(&sb, "%-14s %6s %10s %10s %13s %10s %10s %10s\n",
		"max_per_sector", "weeks", "mean_bx%", "sd_bx%", "worst4wkOvl%", "mean_bx%H1", "mean_bx%H2", "mean_xs%")
	for _, a := range g.Arms {
		fmt.Fprintf(&sb, "%-14s %6d %10.3f %10.3f %13.3f %10.3f %10.3f %10.3f\n",
			capLabel(a.MaxPerSector), a.All.Weeks, a.All.MeanBetaAdjPct, a.All.WeeklySDPct, a.All.Worst4WkOverlapPct,
			a.H1.MeanBetaAdjPct, a.H2.MeanBetaAdjPct, a.All.MeanExcessPct)
	}
	fmt.Fprintf(&sb, "worst4wkOvl%% sums 4 consecutive weekly 15-session-hold returns, which already overlap — not a capital-scaled book drawdown.\n")
	fmt.Fprintf(&sb, "paired vs the live default (max_per_sector=%d), bar: |t| > %.1f, same sign in both halves and every region:\n",
		orchestrator.DefaultMaxPerSector, adoptionT)
	for _, t := range g.PairedTests {
		fmt.Fprintf(&sb, "%s mean %+.4f t %.2f (n=%d) | H1 %+.4f H2 %+.4f | US %+.4f EU %+.4f Asia %+.4f → %s\n",
			t.ID, t.Mean, t.T, t.NDates, t.Halves["H1"], t.Halves["H2"],
			t.Regions["US"], t.Regions["EU"], t.Regions["Asia"], t.Verdict)
	}
	sb.WriteString("\n")

	sb.WriteString("=== Decision rules (E1, E3) ===\n")
	for _, t := range r.Decisions {
		fmt.Fprintf(&sb, "%s → %s\n", t.ID, t.Verdict)
	}
	sb.WriteString("\n")

	u := r.USScoped
	fmt.Fprintf(&sb, "=== US-scoped tests D1–D3 (registered, in the tests-run count; bar: t > +%.1f, same sign in both halves, beta-adjusted) ===\n", adoptionT)
	fmt.Fprintf(&sb, "%s; %d dates\n", u.Scope, u.Dates)
	for _, t := range u.Tests {
		if t.Status != "run" {
			fmt.Fprintf(&sb, "%s %s — %s: %s\n", t.ID, t.Status, t.Title, t.Verdict)
			continue
		}
		fmt.Fprintf(&sb, "%s mean %+.4f t %.2f (n=%d of %d; H1 n=%d, H2 n=%d) | H1 %+.4f H2 %+.4f → %s\n",
			t.ID, t.Mean, t.T, t.NDates, u.Dates, t.HalfNDates["H1"], t.HalfNDates["H2"], t.Halves["H1"], t.Halves["H2"], t.Verdict)
	}
	db := u.DriftBook
	fmt.Fprintf(&sb, "drift book: %d weeks (%d with all %d names, mean %.2f), beta-adjusted excess gross %.3f%% net %.3f%% | H1 net %.3f%% H2 net %.3f%%\n\n",
		db.Weeks, db.FullWeeks, picksPerIndex, db.MeanNames, db.MeanGrossPct, db.MeanNetPct, db.H1NetPct, db.H2NetPct)

	fmt.Fprintf(&sb, "=== v5 horizon tests (H1-15 reference, H1, H2; bar: t > +%.1f at nwLags(h), positive in both halves and every region) ===\n", adoptionT)
	ref := r.Horizon.Reference
	ref.Status = "run" // printed in full, its verdict already says it is a reference
	for _, t := range append([]TestResult{ref}, r.Horizon.Tests...) {
		writeTestLine(&sb, t)
	}
	for _, b := range r.Horizon.Books {
		fmt.Fprintf(&sb, "H1 book %d sessions: %d weeks, mean %.2f picks, beta-adjusted gross %.3f%% net %.3f%% | 1st half net %.3f%% 2nd half net %.3f%%\n",
			b.Horizon, b.Weeks, b.MeanPicks, b.GrossPct, b.NetPct, b.H1NetPct, b.H2NetPct)
	}
	sb.WriteString("\n")

	fmt.Fprintf(&sb, "=== Pre-registered tests (%d run in all: the C-series below, E2, the decisions run above, D1–D3 and the v5 tests above; C-series bar: t > +%.1f, positive in both halves and every region) ===\n", r.TestsRun, adoptionT)
	for _, t := range r.Preregistered {
		writeTestLine(&sb, t)
	}
	mt := r.MultipleTesting
	fmt.Fprintf(&sb, "\n=== Multiple testing: Holm over all %d tests run (family size %d) ===\n", r.TestsRun, mt.FamilySize)
	sig := 0
	for _, row := range mt.Rows {
		verdict := "fail"
		if row.Pass {
			verdict = "pass"
		}
		fmt.Fprintf(&sb, "%-7s %-22s t %6.2f  p %.4f  Holm p %.4f  %s\n", row.ID, row.Sided, row.T, row.P, row.PHolm, verdict)
		if float64(row.PHolm) < holmAlpha {
			sig++
		}
	}
	fmt.Fprintf(&sb, "%d of %d tests have Holm p < %.2f.\n", sig, mt.FamilySize, holmAlpha)
	if len(r.Unavailable) > 0 {
		fmt.Fprintf(&sb, "\n%d symbol(s) unavailable: %s\n", len(r.Unavailable), strings.Join(r.Unavailable, "; "))
	}
	if r.FilingsNote != "" {
		fmt.Fprintf(&sb, "\nNOTE: %s\n", r.FilingsNote)
	}
	if len(r.FilingsUnavailable) > 0 {
		fmt.Fprintf(&sb, "\n%d filing history lookup(s) unavailable (drift/earn_window NaN): %s\n", len(r.FilingsUnavailable), strings.Join(r.FilingsUnavailable, "; "))
	}
	return sb.String()
}

// writeTestLine prints one pre-registered test in the report's one-line format;
// a test that did not run gets its note instead of numbers.
func writeTestLine(sb *strings.Builder, t TestResult) {
	if t.Status != "run" {
		fmt.Fprintf(sb, "%s %s — %s: %s\n", t.ID, t.Status, t.Title, t.Note)
		return
	}
	fmt.Fprintf(sb, "%s mean %+.4f t %.2f (n=%d) | H1 %+.4f H2 %+.4f | US %+.4f EU %+.4f Asia %+.4f → %s\n    %s\n",
		t.ID, t.Mean, t.T, t.NDates, t.Halves["H1"], t.Halves["H2"],
		t.Regions["US"], t.Regions["EU"], t.Regions["Asia"], t.Verdict, t.Title)
}

// maturedDates counts the distinct rebalance dates on which at least one record
// has a forward return at horizon index k.
func maturedDates(recs []Record, k int) int {
	seen := map[string]bool{}
	for _, r := range recs {
		if !math.IsNaN(r.BX[k]) {
			seen[r.Date] = true
		}
	}
	return len(seen)
}

// hasHistory reports whether idx has an embedded point-in-time history.
func hasHistory(idx string) bool {
	for _, h := range universe.HistoryIndices() {
		if h == idx {
			return true
		}
	}
	return false
}
