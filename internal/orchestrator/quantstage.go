package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// buildQuantPack is Stage 1.5: fetch 2y daily history for every shortlisted
// ticker (plus the benchmarks of the indices involved), compute the quant
// metrics in-process, and persist both the raw series and the metrics into the
// run directory. Fetch failures degrade to an ungrounded prompt for that
// ticker — they never abort the run.
//
// It returns the metrics pack and the underlying series, which the risk gate
// needs for pairwise correlations without re-reading them off disk.
func buildQuantPack(ctx context.Context, ch chan<- Event, run *store.Run, yc *marketdata.YahooClient, shortlist []model.Candidate) (*quant.Pack, map[string]*quant.Series) {
	pack := quant.NewPack()
	series := map[string]*quant.Series{}

	// Fetch each benchmark once.
	benches := map[string]*quant.Series{}
	benchFor := func(indexKey string) *quant.Series {
		sym := universe.BenchmarkSymbol(indexKey)
		if s, ok := benches[sym]; ok {
			return s
		}
		s, err := yc.History(ctx, sym)
		if err != nil {
			pack.Errors = append(pack.Errors, fmt.Sprintf("benchmark %s: %v", sym, err))
			s = nil
		}
		benches[sym] = s
		return s
	}

	for _, c := range shortlist {
		s, err := yc.History(ctx, c.Ticker)
		if err != nil {
			pack.Errors = append(pack.Errors, fmt.Sprintf("%s: %v", c.Ticker, err))
			continue
		}
		series[strings.ToUpper(c.Ticker)] = s
	}

	// Every idea's entry, stop and target is computed to the cent off the last
	// close, so a name whose newest bar trails the rest of the shortlist is
	// priced off a session that has already been superseded. Give each such name
	// one forced refetch — a stale cache entry is the usual cause and this fixes
	// it outright — and flag whatever is still behind afterwards.
	newest := newestBar(series)
	if ref := lastTradingDay(time.Now()); ref > newest {
		// The whole pack trails the last completed session: no cross-ticker
		// comparison can see that, so measure against the calendar instead.
		newest = ref
	}
	for _, t := range staleTickers(asOfDates(series), newest) {
		fresh, err := yc.HistoryFresh(ctx, t)
		if err != nil {
			log(ch, fmt.Sprintf("warn: refetch %s for freshness: %v", t, err))
			continue
		}
		series[t] = fresh
	}

	stale := staleTickers(asOfDates(series), newest)
	staleSet := map[string]bool{}
	for _, t := range stale {
		staleSet[t] = true
	}
	if len(stale) > 0 {
		pack.Errors = append(pack.Errors, fmt.Sprintf(
			"prices stale for %s: newest bar is older than %s, which every level below is computed from",
			strings.Join(stale, ", "), newest))
		log(ch, fmt.Sprintf("warn: stale prices for %d name(s): %s (newest bar in this run: %s)",
			len(stale), strings.Join(stale, ", "), newest))
	}

	for _, c := range shortlist {
		t := strings.ToUpper(c.Ticker)
		s, ok := series[t]
		if !ok {
			continue
		}
		if err := run.WritePrices(c.Ticker, s); err != nil {
			log(ch, fmt.Sprintf("warn: write prices for %s: %v", c.Ticker, err))
		}
		m := quant.Compute(s, benchFor(c.Index))
		if staleSet[t] {
			// The Chief Analyst reads Flags in the compact block, so the
			// staleness travels with the price it undermines.
			m.Flags = append(m.Flags, fmt.Sprintf(
				"stale: last bar %s, behind this run's reference session %s — re-price before acting", m.AsOf, newest))
		}
		pack.ByTicker[t] = m
		if m.AsOf > pack.AsOf {
			pack.AsOf = m.AsOf
		}
	}

	// The benchmarks were fetched above for beta and relative strength. Compute
	// their own metrics too: that is the market regime, and it costs nothing.
	for sym, s := range benches {
		if s == nil {
			continue
		}
		pack.Benchmarks[sym] = quant.Compute(s, nil)
	}

	if err := run.WriteQuantPack(pack); err != nil {
		log(ch, fmt.Sprintf("warn: write quant.json: %v", err))
	}
	return pack, series
}

// asOfDates maps each symbol to the date of its newest bar.
func asOfDates(series map[string]*quant.Series) map[string]string {
	out := make(map[string]string, len(series))
	for t, s := range series {
		out[t] = s.AsOf()
	}
	return out
}

// newestBar is the most recent bar date anyone in the pack has.
func newestBar(series map[string]*quant.Series) string {
	var newest string
	for _, s := range series {
		if d := s.AsOf(); d > newest {
			newest = d
		}
	}
	return newest
}

// regimeSuffix appends the computed market regime to the Chief Analyst's quant
// reference. The Chief was told to weigh a macro report at 10–15% of the score
// while having no market-level prices of its own to check that report against.
func regimeSuffix(p *quant.Pack) string {
	if rb := p.RegimeBlock(); rb != "" {
		return "\n" + rb
	}
	return ""
}
