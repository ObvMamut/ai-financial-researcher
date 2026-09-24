package scoreboard

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// Outcome is how a replayed position ended.
type Outcome string

const (
	OutcomeTarget   Outcome = "target"   // the target was reached first
	OutcomeStop     Outcome = "stop"     // the stop was reached first
	OutcomeExpired  Outcome = "expired"  // held to the end of the timeframe
	OutcomeOpen     Outcome = "open"     // not enough history yet; marked to market
	OutcomeUnfilled Outcome = "unfilled" // the limit entry never traded
	OutcomeError    Outcome = "error"    // no usable price history
)

// closed reports whether an outcome is a finished trade. Win rate is computed
// over these alone: an idea still open, or one whose limit never filled, is not
// a loss and counting it as one is what produced a 32% "win rate" from a
// scoreboard where a third of the rows were 0.00%.
func (o Outcome) closed() bool {
	return o == OutcomeTarget || o == OutcomeStop || o == OutcomeExpired
}

// DefaultFillWindowDays is how many sessions a limit entry stays live. Past
// that the setup the idea described is no longer the setup in front of you.
const DefaultFillWindowDays = 3

// defaultTimeframeDays matches the orchestrator's assumption for an idea that
// did not state a holding period.
const defaultTimeframeDays = 10

// Replay walks runsDir and simulates each idea forward through its own daily
// bars: does the limit fill, which barrier is touched first, and where does the
// position end. A market_on_open idea skips the first question: it fills at the
// next session's open. An idea with no entry_type predates the field and
// replays as the limit it was.
//
// The previous scoreboard compared price-at-generation to *today's* price, which
// answers a question nobody asked. An idea that reached its target and retraced
// scored as a loss; one whose stop was blown through weeks ago scored as
// whatever the stock has done since; and an entry that never filled counted as a
// flat trade that dragged the win rate toward zero. None of those describe the
// trade the idea actually specified.
func Replay(ctx context.Context, runsDir string, yc marketdata.PriceSource, fillWindow int) (*Summary, error) {
	runs, err := store.ListRuns(runsDir)
	if err != nil {
		return nil, err
	}
	if fillWindow <= 0 {
		fillWindow = DefaultFillWindowDays
	}

	sum := &Summary{Replay: true}
	series := &seriesCache{yc: yc, bySymbol: map[string]*quant.Series{}, byRun: map[string]*quant.Series{}}

	for _, r := range runs {
		ideas, err := store.LoadIdeas(r.Dir)
		if err != nil || ideas == nil {
			continue
		}
		// Which prompt set produced these ideas, and which sector each name was
		// screened as. Both read once per run: they are the same answer for
		// every idea in it. Sector lives on the shortlist rather than on the
		// idea, so this is the only place it can be recovered.
		meta, _ := store.LoadMeta(r.Dir)
		persona := personaKey(meta)
		sectors := map[string]string{}
		if meta != nil {
			for _, c := range meta.Shortlist {
				if c.Sector != "" {
					sectors[strings.ToUpper(c.Ticker)] = c.Sector
				}
			}
		}

		counted := false
		for _, idea := range ideas.Ideas {
			if idea.PriceAtGeneration <= 0 && idea.Entry <= 0 {
				sum.Skipped++
				continue
			}
			counted = true
			e := replayIdea(ctx, r, ideas.GeneratedAt, idea, series, fillWindow)
			e.PersonaSet = persona
			e.Sector = sectors[strings.ToUpper(idea.Ticker)]
			sum.Entries = append(sum.Entries, e)
		}
		if counted {
			sum.RunCount++
		}
	}

	sum.aggregate()
	return sum, nil
}

// replayIdea simulates one idea. It never returns an error: an idea whose
// history cannot be fetched is an `error` row, which is information.
func replayIdea(ctx context.Context, r store.RunSummary, generatedAt string, idea model.TradeIdea, cache *seriesCache, fillWindow int) Entry {
	e := Entry{
		ResearchMode: idea.ResearchMode, PlanStatus: idea.Status,
		RunName:       r.Name,
		GeneratedAt:   generatedAt,
		Ticker:        idea.Ticker,
		Index:         idea.Index,
		Direction:     string(idea.Direction),
		EntryType:     idea.EntryType,
		Confidence:    idea.Confidence,
		PriceAtGen:    idea.PriceAtGeneration,
		Stop:          idea.Stop,
		Target:        idea.Target,
		TimeframeDays: idea.TimeframeDays,
		DomainScores:  idea.DomainScores,
		// Everything the idea said about itself, so a post-mortem can ask why
		// rather than only whether. Sector is filled by the caller, which holds
		// the producing run's shortlist.
		Why:            idea.Why,
		PositionNote:   idea.PositionNote,
		BaseConfidence: idea.BaseConfidence,
		Consensus:      idea.Consensus,
		ExpectancyR:    idea.ExpectancyR,
		BreakevenWin:   idea.BreakevenWinRate,
		Outcome:        OutcomeError,
	}
	if e.TimeframeDays <= 0 {
		e.TimeframeDays = defaultTimeframeDays
	}

	s, err := cache.get(ctx, idea.Ticker, r.Dir)
	if err != nil || s == nil || len(s.Bars) == 0 {
		e.Err = "no price history"
		if err != nil {
			e.Err = err.Error()
		}
		return e
	}
	// Only bars strictly after the run: the generation-day bar is the one the
	// idea was priced off, so entering on it would be hindsight.
	genDate := dateOf(generatedAt)
	bars := barsAfter(s, genDate)

	// The call: the directional claim measured from the close it was priced off,
	// over the idea's own holding period. Computed before the fill simulation
	// because it must survive every early return below — an idea whose limit
	// never traded still said which way the stock would go.
	bench := benchmarkFor(idea.Index, idea.Ticker)
	if call := measureHorizon(ctx, cache, bars, idea.PriceAtGeneration,
		idea.Direction, e.TimeframeDays, bench, genDate); call.complete {
		e.CallDone = true
		e.CallPnLPct, e.CallBenchPct, e.CallExcessPct, e.CallEndDate = call.pct, call.bench, call.excess, call.endDate
	}

	entry := idea.Entry
	if entry <= 0 {
		entry = idea.PriceAtGeneration
	}
	e.EntryPlanned = entry

	var fillIdx int
	var fillPrice float64
	if idea.MarketOnOpen() {
		// A market-on-open idea has no limit and no fill window: it is bought
		// or sold at the first session's open after generation, whatever that
		// open is. It cannot be `unfilled`; with no session yet it is `open`.
		if len(bars) == 0 || bars[0].Open <= 0 {
			e.Outcome = OutcomeOpen
			return e
		}
		fillIdx, fillPrice = 0, bars[0].Open
	} else {
		fillBars := bars
		entryExpired := false
		if idea.Thesis != nil && idea.Thesis.EntryExpiresOn != "" {
			n := 0
			for n < len(bars) && bars[n].Date <= idea.Thesis.EntryExpiresOn {
				n++
			}
			fillBars = bars[:n]
			entryExpired = len(bars) > n || (len(bars) > 0 && bars[len(bars)-1].Date >= idea.Thesis.EntryExpiresOn)
		}
		var ok bool
		fillIdx, fillPrice, ok = simulateFill(fillBars, entry, idea.Direction, fillWindow)
		if !ok {
			// An idea whose fill window has not run out yet has not failed to
			// fill — it has not been given its chance. Calling that `unfilled`
			// would write off every idea generated in the last two days.
			if len(bars) < fillWindow && !entryExpired {
				e.Outcome = OutcomeOpen
				e.BarsHeld = len(bars)
				return e
			}
			e.Outcome = OutcomeUnfilled
			return e
		}
	}
	e.EntryFilled = round2(fillPrice)
	e.EntryDate = bars[fillIdx].Date

	// The trade: the same window anchored at the fill rather than at generation.
	// It answers what the account saw, where the call answers whether the read
	// was right, and the two differ by exactly the entry limit.
	tradeBars := bars[fillIdx:]
	hold := e.TimeframeDays
	expiryObserved := false
	if idea.Thesis != nil && idea.Thesis.ExpiresOn != "" {
		n := 0
		for n < len(tradeBars) && tradeBars[n].Date <= idea.Thesis.ExpiresOn {
			n++
		}
		if n == 0 {
			e.Outcome = OutcomeUnfilled
			e.EntryFilled = 0
			e.EntryDate = ""
			return e
		}
		if n < len(tradeBars) || tradeBars[n-1].Date == idea.Thesis.ExpiresOn {
			hold = n
			expiryObserved = true
		}
	}
	var trade horizonResult
	if idea.Thesis == nil {
		trade = measureHorizon(ctx, cache, bars[fillIdx+1:], fillPrice, idea.Direction, e.TimeframeDays, bench, e.EntryDate)
	} else if expiryObserved {
		// Include the fill session: even an expiry on that session has a
		// measurable fill-to-close return. A late fill never restarts the clock.
		trade = measureHorizon(ctx, cache, tradeBars, fillPrice, idea.Direction, hold, bench, e.EntryDate)
	}
	if trade.complete {
		e.TradeDone = true
		e.TradePnLPct, e.TradeExcessPct = trade.pct, trade.excess
	}
	exit := walkToExit(tradeBars, idea, hold)
	e.Outcome = exit.outcome
	e.ExitPrice = round2(exit.price)
	e.ExitDate = exit.date
	e.BarsHeld = exit.bars
	e.PnLPct = pnl(idea.Direction, fillPrice, exit.price)

	// R-multiples: the same P&L measured in units of what the trade actually
	// risked. A +3% win on a 1% stop and a +3% win on a 6% stop are not the
	// same result, and averaging percentages hides that.
	if risk := math.Abs(fillPrice - idea.Stop); risk > 0 && idea.Stop > 0 {
		e.RiskAdjPnL = round2((fillPrice * e.PnLPct / 100) / risk)
	}

	// Benchmark-relative return over the same window: a long that made 4% while
	// its index made 6% did not work.
	if b, err := cache.get(ctx, universe.BenchmarkFor(idea.Index, idea.Ticker), ""); err == nil && b != nil {
		if br, ok := returnBetween(b, e.EntryDate, e.ExitDate); ok {
			e.BenchmarkPnLPct = round2(br * 100)
			if idea.Direction == model.DirectionSell {
				e.ExcessPnLPct = round2(e.PnLPct + e.BenchmarkPnLPct)
			} else {
				e.ExcessPnLPct = round2(e.PnLPct - e.BenchmarkPnLPct)
			}
		}
	}
	return e
}

// simulateFill decides whether and where a limit entry traded, within the fill
// window. It returns the bar index and the fill price.
//
// A limit that never traded is not a flat trade. It is a trade that did not
// happen, and the old scoreboard counted every one of them as a 0.00% row.
func simulateFill(bars []quant.Bar, entry float64, dir model.Direction, window int) (int, float64, bool) {
	if entry <= 0 {
		return 0, 0, false
	}
	if window > len(bars) {
		window = len(bars)
	}
	for i := 0; i < window; i++ {
		b := bars[i]
		// A gap through the limit in the trader's favour fills at the open,
		// which is better than the limit — the one place optimism is correct.
		if dir == model.DirectionBuy {
			if b.Open <= entry {
				return i, b.Open, true
			}
			if b.Low <= entry && entry <= b.High {
				return i, entry, true
			}
		} else {
			if b.Open >= entry {
				return i, b.Open, true
			}
			if b.Low <= entry && entry <= b.High {
				return i, entry, true
			}
		}
	}
	return 0, 0, false
}

type exitResult struct {
	outcome Outcome
	price   float64
	date    string
	bars    int
}

// walkToExit walks forward from the fill bar to whichever comes first: the
// stop, the target, or the end of the holding period. A zero target — a
// market_on_open idea that stated none — is never touched, so the position
// ends at the stop or on time.
func walkToExit(bars []quant.Bar, idea model.TradeIdea, timeframe int) exitResult {
	buy := idea.Direction != model.DirectionSell
	for i, b := range bars {
		if i >= timeframe {
			break
		}
		hitStop := idea.Stop > 0 && ((buy && b.Low <= idea.Stop) || (!buy && b.High >= idea.Stop))
		hitTarget := idea.Target > 0 && ((buy && b.High >= idea.Target) || (!buy && b.Low <= idea.Target))

		// Both touched inside one daily bar: we cannot know the order, so
		// assume the stop. Assuming the target is how a backtest flatters
		// itself into a strategy nobody can trade.
		if hitStop {
			price := idea.Stop
			if buy && b.Open < idea.Stop {
				price = b.Open // gapped through: the fill is where it opened
			} else if !buy && b.Open > idea.Stop {
				price = b.Open
			}
			return exitResult{OutcomeStop, price, b.Date, i + 1}
		}
		if hitTarget {
			// A limit at the target fills at the target or better.
			price := idea.Target
			if buy && b.Open > idea.Target {
				price = b.Open
			} else if !buy && b.Open < idea.Target {
				price = b.Open
			}
			return exitResult{OutcomeTarget, price, b.Date, i + 1}
		}
	}
	if len(bars) >= timeframe {
		last := bars[timeframe-1]
		return exitResult{OutcomeExpired, last.Close, last.Date, timeframe}
	}
	last := bars[len(bars)-1]
	return exitResult{OutcomeOpen, last.Close, last.Date, len(bars)}
}

// seriesCache fetches each symbol's daily bars once, preferring the live series
// and falling back to whatever the run itself saved. The saved copy is what
// makes an old run scorable after a ticker is delisted or renamed.
//
// The two caches are keyed differently on purpose. A live series is the same
// series whoever asks for it, so it is keyed by symbol alone. A saved snapshot
// belongs to one run and holds only the bars that run had, so it is keyed by
// run as well — and the fallback used to share the symbol-keyed map, which meant
// a ticker appearing in two runs got exactly one shot at a saved copy. Whichever
// run asked first had its snapshot reused to replay the *other* run's idea, and
// an older snapshot necessarily lacks the bars a newer idea needs: it replayed
// as `open` forever, which is a closed trade quietly missing from the record the
// Chief and the risk gate both read.
type seriesCache struct {
	yc       marketdata.PriceSource
	bySymbol map[string]*quant.Series
	byRun    map[string]*quant.Series
}

func (c *seriesCache) get(ctx context.Context, symbol, runDir string) (*quant.Series, error) {
	key := strings.ToUpper(symbol)
	live, tried := c.bySymbol[key]
	if tried && live != nil {
		return live, nil
	}
	if !tried && c.yc != nil {
		if c.bySymbol == nil {
			c.bySymbol = map[string]*quant.Series{}
		}
		s, err := c.yc.History(ctx, symbol)
		if err == nil && s != nil && len(s.Bars) > 0 {
			c.bySymbol[key] = s
			return s, nil
		}
		c.bySymbol[key] = nil
	}

	// The live fetch has failed, now or on an earlier call. Ask this run for its
	// own snapshot — every run gets asked, not just the first.
	if runDir == "" {
		return nil, fmt.Errorf("no history for %s", symbol)
	}
	runKey := key + "\x00" + runDir
	if saved, ok := c.byRun[runKey]; ok {
		if saved == nil {
			return nil, fmt.Errorf("no history for %s", symbol)
		}
		return saved, nil
	}
	if c.byRun == nil {
		c.byRun = map[string]*quant.Series{}
	}
	var saved quant.Series
	if ok, err := store.ReadPrices(runDir, symbol, &saved); ok && err == nil && len(saved.Bars) > 0 {
		c.byRun[runKey] = &saved
		return &saved, nil
	}
	c.byRun[runKey] = nil
	return nil, fmt.Errorf("no history for %s", symbol)
}

// barsAfter returns the bars strictly later than date (YYYY-MM-DD).
func barsAfter(s *quant.Series, date string) []quant.Bar {
	if date == "" {
		return s.Bars
	}
	i := sort.Search(len(s.Bars), func(i int) bool { return s.Bars[i].Date > date })
	return s.Bars[i:]
}

// returnBetween is the simple return of a series between two dates, using the
// first bar on or after each.
func returnBetween(s *quant.Series, from, to string) (float64, bool) {
	if from == "" || to == "" {
		return 0, false
	}
	at := func(d string) (float64, bool) {
		i := sort.Search(len(s.Bars), func(i int) bool { return s.Bars[i].Date >= d })
		if i >= len(s.Bars) || s.Bars[i].Close <= 0 {
			return 0, false
		}
		return s.Bars[i].Close, true
	}
	a, ok1 := at(from)
	b, ok2 := at(to)
	if !ok1 || !ok2 || a <= 0 {
		return 0, false
	}
	return b/a - 1, true
}

func dateOf(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ""
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
