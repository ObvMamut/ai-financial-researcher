package orchestrator

import (
	"context"
	"fmt"
	"strings"

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
func buildQuantPack(ctx context.Context, ch chan<- Event, run *store.Run, yc *marketdata.YahooClient, shortlist []model.Candidate) *quant.Pack {
	pack := quant.NewPack()

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
		series, err := yc.History(ctx, c.Ticker)
		if err != nil {
			pack.Errors = append(pack.Errors, fmt.Sprintf("%s: %v", c.Ticker, err))
			continue
		}
		if err := run.WritePrices(c.Ticker, series); err != nil {
			log(ch, fmt.Sprintf("warn: write prices for %s: %v", c.Ticker, err))
		}
		m := quant.Compute(series, benchFor(c.Index))
		pack.ByTicker[strings.ToUpper(c.Ticker)] = m
		if m.AsOf > pack.AsOf {
			pack.AsOf = m.AsOf
		}
	}

	if err := run.WriteQuantPack(pack); err != nil {
		log(ch, fmt.Sprintf("warn: write quant.json: %v", err))
	}
	return pack
}
