package backtest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// The point-in-time universe (`cfr backtest --universe pit`).
//
// The sample universe is today's constituents, so every past cross-section is
// missing the companies that left: disproportionately the losers. In this
// mode a US index's members on each rebalance date are the companies actually
// in it then (internal/universe/data/history), priced through Alpaca's asof so
// each membership interval is the company that held the ticker in it. EU and
// Asia have no history source and stay on the sample.

// UniversePIT is Config.Universe's point-in-time value; anything else is the
// sample.
const UniversePIT = "pit"

// AsOfLoader is the point-in-time price seam: marketdata.AlpacaPrices
// satisfies it.
type AsOfLoader interface {
	HistoryAsOf(ctx context.Context, symbols []string, start, end, asof time.Time) (map[string]*quant.Series, error)
}

// PITSurvivorship replaces the sample's survivorship banner in this mode.
const PITSurvivorship = "POINT-IN-TIME: sp500 and nq100 members are those in the index on each rebalance date (Wikipedia + n100tickers histories, priced through Alpaca's asof), so their departed names are in every past cross-section. eu50 and asia100 have no history source and remain today's constituents: survivorship-exposed. Departed names have no sector, so indmom and any sector cap see only the names a sector could be found for."

// pitMembers returns one Member per membership interval of idx that overlaps
// [first, last]. Sector comes from today's sample when the ticker is in it,
// then from the history sector map; a departed company has none.
func pitMembers(idx string, h *universe.History, sample []model.Constituent, sectors map[string]string, first, last time.Time) []Member {
	sampleSector := map[string]string{}
	for _, c := range sample {
		sampleSector[strings.ToUpper(c.Ticker)] = c.Sector
	}
	var out []Member
	for i := range h.Intervals {
		iv := h.Intervals[i]
		if iv.From.After(last) || (!iv.To.IsZero() && !iv.To.After(first)) {
			continue
		}
		sector := sampleSector[iv.Ticker]
		if sector == "" {
			sector = sectors[iv.Ticker]
		}
		out = append(out, Member{
			Constituent: model.Constituent{Ticker: iv.Ticker, Sector: sector, Index: idx},
			Bench:       universe.BenchmarkFor(idx, iv.Ticker),
			Interval:    &iv,
			Key:         iv.Ticker + "@" + iv.From.Format("2006-01-02"),
		})
	}
	return out
}

// pitAsOf is the date each interval is priced as: its last day in the index,
// when its ticker certainly named it, or now for a current member.
func pitAsOf(iv *universe.Interval, now time.Time) time.Time {
	if iv.To.IsZero() {
		return now
	}
	return iv.To.AddDate(0, 0, -1)
}

// loadPIT fills data.Series for the point-in-time members, batching the
// members that share an asof date into one request set. A member Alpaca has
// no bars for is named in the returned list.
func loadPIT(ctx context.Context, loader AsOfLoader, members []Member, data Data, start, now time.Time) ([]string, error) {
	groups := map[time.Time][]Member{}
	for _, m := range members {
		if m.Interval == nil {
			continue
		}
		a := pitAsOf(m.Interval, now)
		groups[a] = append(groups[a], m)
	}
	asofs := make([]time.Time, 0, len(groups))
	for a := range groups {
		asofs = append(asofs, a)
	}
	sort.Slice(asofs, func(i, j int) bool { return asofs[i].Before(asofs[j]) })

	var unavailable []string
	for _, a := range asofs {
		if err := ctx.Err(); err != nil {
			return unavailable, err
		}
		g := groups[a]
		syms := make([]string, 0, len(g))
		seen := map[string]bool{}
		for _, m := range g {
			if !seen[m.Constituent.Ticker] {
				seen[m.Constituent.Ticker] = true
				syms = append(syms, m.Constituent.Ticker)
			}
		}
		got, err := loader.HistoryAsOf(ctx, syms, start, now, a)
		if err != nil {
			return unavailable, fmt.Errorf("point-in-time prices as of %s: %w", a.Format("2006-01-02"), err)
		}
		for _, m := range g {
			s := got[m.Constituent.Ticker]
			if (s == nil || len(s.Bars) == 0) && m.Interval.From.After(start) {
				// A reused ticker can come back empty when the window reaches the
				// previous holder's years. DOW (Dow Chemical to 2017-08, Dow Inc.
				// from 2019-04) did, in a 100-symbol batch from 2016-06-29; the
				// same batch from 2019-04-02 priced it. Ask once more, alone, from
				// the interval's own start and under the same asof, so the mapping
				// is never switched off and no earlier holder's bars are spliced in.
				again, err := loader.HistoryAsOf(ctx, []string{m.Constituent.Ticker}, m.Interval.From, now, a)
				if err != nil {
					return unavailable, fmt.Errorf("point-in-time prices for %s as of %s: %w", m.SeriesKey(), a.Format("2006-01-02"), err)
				}
				s = again[m.Constituent.Ticker]
			}
			if s == nil || len(s.Bars) == 0 {
				unavailable = append(unavailable, fmt.Sprintf("%s: no Alpaca bars as of %s", m.SeriesKey(), a.Format("2006-01-02")))
				continue
			}
			data.Series[m.SeriesKey()] = s
		}
	}
	sort.Strings(unavailable)
	return unavailable, nil
}
