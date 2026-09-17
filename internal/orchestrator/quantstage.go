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
//
// It returns the metrics pack and the underlying series, which the risk gate
// needs for pairwise correlations without re-reading them off disk.
func buildQuantPack(ctx context.Context, ch chan<- Event, run *store.Run, yc marketdata.PriceSource, fx *marketdata.FXRates, shortlist []model.Candidate) (*quant.Pack, map[string]*quant.Series) {
	pack := quant.NewPack()
	series := map[string]*quant.Series{}

	// Fetch each benchmark once. Keyed by symbol, not by index: asia100 resolves
	// to one benchmark per exchange, and several of those are shared.
	benches := map[string]*quant.Series{}
	benchFor := func(indexKey, ticker string) *quant.Series {
		sym := universe.BenchmarkFor(indexKey, ticker)
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
	// close, so a name whose newest bar trails its own market's last completed
	// session is priced off a session that has already been superseded. Give each
	// such name one forced refetch — a stale cache entry is the usual cause and
	// this fixes it outright — and flag whatever is still behind afterwards.
	now := researchTime(ctx)
	for _, t := range staleTickers(asOfDates(series), now) {
		fresh, err := yc.HistoryFresh(ctx, t)
		if err != nil {
			log(ch, fmt.Sprintf("warn: refetch %s for freshness: %v", t, err))
			continue
		}
		series[t] = fresh
	}

	stale := staleTickers(asOfDates(series), now)
	pack.Stale = stale
	staleSet := map[string]bool{}
	for _, t := range stale {
		staleSet[t] = true
	}
	if len(stale) > 0 {
		var detail []string
		for _, t := range stale {
			detail = append(detail, fmt.Sprintf("%s (%s, last session %s)",
				t, series[t].AsOf(), lastTradingDay(now, marketdata.MarketCloseUTC(t))))
		}
		pack.Errors = append(pack.Errors, fmt.Sprintf(
			"prices stale for %s: the newest bar trails the last completed session on that name's own market, which every level below is computed from",
			strings.Join(detail, ", ")))
		log(ch, fmt.Sprintf("warn: stale prices for %d name(s): %s", len(stale), strings.Join(detail, ", ")))
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
		m := quant.Compute(s, benchFor(c.Index, c.Ticker))
		m.ApplyFX(fxFor(ctx, fx, c.Ticker))
		if staleSet[t] {
			// The Chief Analyst reads Flags in the compact block, so the
			// staleness travels with the price it undermines.
			m.Flags = append(m.Flags, fmt.Sprintf(
				"stale: last bar %s, behind %s's last completed session %s — re-price before acting",
				m.AsOf, t, lastTradingDay(now, marketdata.MarketCloseUTC(t))))
		}
		pack.ByTicker[t] = m
		if m.AsOf > pack.AsOf {
			pack.AsOf = m.AsOf
		}
	}

	// The benchmarks were fetched above for beta and relative strength. Compute
	// their own metrics too: that is the market regime, and it costs nothing.
	// They get no FX: an index level has no currency and its "turnover" is a
	// meaningless sum, so converting it would only lend it credibility.
	for sym, s := range benches {
		if s == nil {
			continue
		}
		pack.Benchmarks[sym] = quant.Compute(s, nil)
		series[sym] = s
		if err := run.WritePrices(sym, s); err != nil {
			log(ch, fmt.Sprintf("warn: write benchmark %s: %v", sym, err))
		}
	}
	pack.Errors = append(pack.Errors, fx.Failures()...)

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

// regimeSuffix appends the computed market regime to the Chief Analyst's quant
// reference. The Chief was told to weigh a macro report at 10–15% of the score
// while having no market-level prices of its own to check that report against.
func regimeSuffix(p *quant.Pack) string {
	if rb := p.RegimeBlock(); rb != "" {
		return "\n" + rb
	}
	return ""
}

// dropStalePriced removes from the shortlist, and from the pack, any name whose
// newest bar still trails its own market's last completed session after the
// forced refetch. It returns what it removed so the caller can log it.
//
// The staleness was already detected here, one stage before the specialists run,
// and then used only to append a caveat. Everything downstream still spent five
// specialist reports on the name, the Chief still ranked it and priced it, and
// the risk gate only raised the problem afterwards — as a *soft* finding asking
// the Chief to "re-price it against the newest bar in the quant block". On
// 2026-09-05 there was no newer bar in that block, so the only answer the
// corrective pass could give was to delete the idea, and the run shipped two
// instead of three. A name that cannot be priced cannot produce an idea; the
// place to say so is before the research is paid for, not after.
//
// Two limits. Single-stock mode never drops: the user named the ticker, there is
// no alternative to fall back to, and the flag on the price is the honest answer
// there. And a drop that would empty the shortlist is refused — a run that ships
// flagged ideas is worth more than one that ships none.
func dropStalePriced(mode model.Mode, shortlist []model.Candidate, pack *quant.Pack) ([]model.Candidate, []string) {
	if mode == model.ModeSingle || pack == nil || len(pack.Stale) == 0 {
		return shortlist, nil
	}
	stale := make(map[string]bool, len(pack.Stale))
	for _, t := range pack.Stale {
		stale[strings.ToUpper(t)] = true
	}
	kept := make([]model.Candidate, 0, len(shortlist))
	var dropped []string
	for _, c := range shortlist {
		if stale[strings.ToUpper(c.Ticker)] {
			dropped = append(dropped, c.Ticker)
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) == 0 {
		return shortlist, nil
	}
	for _, t := range dropped {
		delete(pack.ByTicker, strings.ToUpper(t))
	}
	return kept, dropped
}

// pluralNames renders "it"/"them" for the drop message.
func pluralNames(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// specialistDataBlock assembles one specialist's verified-data block: its own
// provider pack, plus whichever computed block that role's persona is written
// against.
//
// It is a function rather than an inline switch because what a role is shown is
// the whole of what it can honestly say, and every one of these branches is
// there to repair a domain that was being asked a question its prompt could not
// answer.
func specialistDataBlock(role string, pack *marketdata.DataPack, quantPack *quant.Pack, ps *Prescreen, shortlist []model.Candidate) string {
	block := pack.Markdown()
	switch role {
	case "quant":
		if qmd := quantPack.Markdown(); qmd != "" {
			// Append rather than replace: replacing made quant the only
			// specialist that lost the macro backdrop every other role got.
			block = qmd
			if mm := pack.MacroMarkdown(); mm != "" {
				block += "\n### Verified macro backdrop\n\n" + mm
			}
		}
	case "news", "sentiment", "fundamentals":
		// Fundamentals needs the price to say anything about a multiple:
		// without it the domain could report a revenue figure but never a
		// P/E, and "expensive" was an assertion about a number it had not
		// been shown.
		if cb := quantPack.CompactBlock(); cb != "" {
			block += "\n### Verified price context (computed from daily OHLCV)\n\n" + cb
		}
		// And fundamentals gets the market's reaction to the last filing. Its
		// evidence is otherwise a single-period snapshot plus one year-over-year
		// growth rate — a description of a company, on a clock where the one
		// documented fundamental effect is post-earnings drift. The reaction is
		// the only fact in this domain's prompt that acts inside three weeks,
		// and without it the persona had nothing horizon-matched to score, so it
		// scored the multiple and charged every momentum long for it.
		//
		// It is not a blinding violation: fundamentals already reads the
		// nominated direction off the shortlist (agents.blindToDirection covers
		// quant and macro alone). See agents/fundamentals.md for the strength cap
		// that stops it re-voting the funnel's own drift ranking.
		if role == "fundamentals" {
			block += driftBlock(ps, shortlist)
		}
	case "macro":
		// The macro domain's question at this horizon is the market regime,
		// and the benchmark prices answer it. FRED alone never could.
		if rb := quantPack.RegimeBlock(); rb != "" {
			block += "\n" + rb
		}
	}
	return block
}
