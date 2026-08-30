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
// position end.
//
// The previous scoreboard compared price-at-generation to *today's* price, which
// answers a question nobody asked. An idea that reached its target and retraced
// scored as a loss; one whose stop was blown through weeks ago scored as
// whatever the stock has done since; and an entry that never filled counted as a
// flat trade that dragged the win rate toward zero. None of those describe the
// trade the idea actually specified.
func Replay(ctx context.Context, runsDir string, yc *marketdata.YahooClient, fillWindow int) (*Summary, error) {
	runs, err := store.ListRuns(runsDir)
	if err != nil {
		return nil, err
	}
	if fillWindow <= 0 {
		fillWindow = DefaultFillWindowDays
	}

	sum := &Summary{Replay: true}
	series := &seriesCache{yc: yc, bySymbol: map[string]*quant.Series{}}

	for _, r := range runs {
		ideas, err := store.LoadIdeas(r.Dir)
		if err != nil || ideas == nil {
			continue
		}
		counted := false
		for _, idea := range ideas.Ideas {
			if idea.PriceAtGeneration <= 0 && idea.Entry <= 0 {
				sum.Skipped++
				continue
			}
			counted = true
			e := replayIdea(ctx, r, ideas.GeneratedAt, idea, series, fillWindow)
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
		RunName:       r.Name,
		GeneratedAt:   generatedAt,
		Ticker:        idea.Ticker,
		Index:         idea.Index,
		Direction:     string(idea.Direction),
		Confidence:    idea.Confidence,
		PriceAtGen:    idea.PriceAtGeneration,
		Stop:          idea.Stop,
		Target:        idea.Target,
		TimeframeDays: idea.TimeframeDays,
		DomainScores:  idea.DomainScores,
		Outcome:       OutcomeError,
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

	entry := idea.Entry
	if entry <= 0 {
		entry = idea.PriceAtGeneration
	}
	e.EntryPlanned = entry

	fillIdx, fillPrice, ok := simulateFill(bars, entry, idea.Direction, fillWindow)
	if !ok {
		// An idea whose fill window has not run out yet has not failed to
		// fill — it has not been given its chance. Calling that `unfilled`
		// would write off every idea generated in the last two days.
		if len(bars) < fillWindow {
			e.Outcome = OutcomeOpen
			e.BarsHeld = len(bars)
			return e
		}
		e.Outcome = OutcomeUnfilled
		return e
	}
	e.EntryFilled = round2(fillPrice)
	e.EntryDate = bars[fillIdx].Date

	exit := walkToExit(bars[fillIdx:], idea, e.TimeframeDays)
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
	if b, err := cache.get(ctx, universe.BenchmarkSymbol(idea.Index), ""); err == nil && b != nil {
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
// stop, the target, or the end of the holding period.
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
type seriesCache struct {
	yc       *marketdata.YahooClient
	bySymbol map[string]*quant.Series
}

func (c *seriesCache) get(ctx context.Context, symbol, runDir string) (*quant.Series, error) {
	key := strings.ToUpper(symbol)
	if s, ok := c.bySymbol[key]; ok {
		if s == nil {
			return nil, fmt.Errorf("no history for %s", symbol)
		}
		return s, nil
	}
	s, err := c.yc.History(ctx, symbol)
	if err != nil || s == nil || len(s.Bars) == 0 {
		if runDir != "" {
			var saved quant.Series
			if ok, rerr := store.ReadPrices(runDir, symbol, &saved); ok && rerr == nil && len(saved.Bars) > 0 {
				c.bySymbol[key] = &saved
				return &saved, nil
			}
		}
		c.bySymbol[key] = nil
		if err == nil {
			err = fmt.Errorf("no history for %s", symbol)
		}
		return nil, err
	}
	c.bySymbol[key] = s
	return s, nil
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
