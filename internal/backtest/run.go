package backtest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
}

// DefaultYears is Config.Years' default, and the threshold LongHistorySurvivorship
// is measured against: past it, the per-year report (E1) is showing more history
// than the halves above it do.
const DefaultYears = 4

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
	"no drift archetype: no point-in-time 10-Q/10-K dates are cached, as in a live run without an SEC contact address; the composite does not use drift",
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
	RequestedStart string   `json:"requested_start,omitempty"`
	Shortfall      bool     `json:"shortfall,omitempty"`
	ShortfallNote  string   `json:"shortfall_note,omitempty"`
	Survivorship   string   `json:"survivorship"`
	Departures     []string `json:"departures"`
	CostNote       string   `json:"cost_note"`
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
	TestsRun      int          `json:"tests_run"`
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

	var members []Member
	for _, idx := range cfg.Indices {
		cs := uni.Constituents(idx)
		if len(cs) == 0 {
			return nil, fmt.Errorf("unknown or empty index %q", idx)
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
	for i, m := range members {
		t := strings.ToUpper(m.Constituent.Ticker)
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
	logf("prices: %d symbols, %d unavailable", len(data.Series)+len(data.Bench), len(unavailable))

	end := lastFriday(cfg.Now)
	dates := WeeklyDates(end.AddDate(-cfg.Years, 0, 0), end)
	recs := DropWarmupDates(BuildPanel(members, data, dates))
	if len(recs) == 0 {
		return nil, fmt.Errorf("the panel is empty: no member had %d bars at any rebalance date", minHistory)
	}
	logf("panel: %d rows", len(recs))
	res := Analyze(recs, data.Series)
	res.GeneratedAt = cfg.Now.UTC().Format(time.RFC3339)
	res.Range, res.Years, res.Unavailable = rng, cfg.Years, unavailable
	checkShortfall(res, cfg.Years, end)
	return res, nil
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
// build panels from synthetic series.
func Analyze(recs []Record, series map[string]*quant.Series) *Result {
	cells := crossSections(recs)
	mid := midDate(cells)
	res := &Result{
		Mid: mid, Rows: len(recs), NamesPerIndex: map[string]int{},
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
	for _, t := range res.Preregistered {
		if t.Status == "run" {
			res.TestsRun++
		}
	}
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
		fmt.Fprintf(&sb, "%-16s %7s %7s %7s %7s %7s %7s %7s %7s %8s %8s %7s\n",
			"signal", "IC5", "tNW5", "IC10", "IR10", "tNW10", "IC15", "tNW15", "QS10%", "QS10net", "TopQnet", "tQS10")
		for _, s := range rows {
			fmt.Fprintf(&sb, "%-16s %7.3f %7.2f %7.3f %7.3f %7.2f %7.3f %7.2f %7.3f %8.3f %8.3f %7.2f\n",
				s.Signal, s.IC[0], s.TNW[0], s.IC[1], s.IR[1], s.TNW[1], s.IC[2], s.TNW[2],
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
		fmt.Fprintf(&sb, "%-6s %8s %7s %9s %7s %9s %7s %8s %10s\n",
			"year", "n_dates", "IC10", "IC10beta", "IC15", "IC15beta", "top5n", "top5%", "top5beta%")
		for _, y := range r.PerYear {
			fmt.Fprintf(&sb, "%-6s %8d %7.3f %9.3f %7.3f %9.3f %7d %8.3f %10.3f\n",
				y.Year, y.NDates, y.IC10, y.IC10Beta, y.IC15, y.IC15Beta, y.Top5N, y.Top5Pct, y.Top5BetaPct)
		}
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

	fmt.Fprintf(&sb, "=== Pre-registered tests (%d run; bar: t > +%.1f, positive in both halves and every region) ===\n", r.TestsRun, adoptionT)
	for _, t := range r.Preregistered {
		if t.Status != "run" {
			fmt.Fprintf(&sb, "%s %s — %s: %s\n", t.ID, t.Status, t.Title, t.Note)
			continue
		}
		fmt.Fprintf(&sb, "%s mean %+.4f t %.2f (n=%d) | H1 %+.4f H2 %+.4f | US %+.4f EU %+.4f Asia %+.4f → %s\n    %s\n",
			t.ID, t.Mean, t.T, t.NDates, t.Halves["H1"], t.Halves["H2"],
			t.Regions["US"], t.Regions["EU"], t.Regions["Asia"], t.Verdict, t.Title)
	}
	if len(r.Unavailable) > 0 {
		fmt.Fprintf(&sb, "\n%d symbol(s) unavailable: %s\n", len(r.Unavailable), strings.Join(r.Unavailable, "; "))
	}
	return sb.String()
}
