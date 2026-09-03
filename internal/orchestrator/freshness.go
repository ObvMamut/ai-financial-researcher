package orchestrator

import (
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// lastTradingDay returns the ISO date of the most recent session whose daily bar
// should exist by now on a market closing at closeUTC. Weekends look back to
// Friday; a day before that market's close looks back to the previous weekday.
// Exchange holidays are not modelled — a holiday makes this one day optimistic,
// which produces a warning to check rather than a wrong price.
func lastTradingDay(now time.Time, closeUTC int) string {
	d := now.UTC()
	if d.Hour() < closeUTC {
		d = d.AddDate(0, 0, -1)
	}
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d.Format("2006-01-02")
}

// staleIdeas lists the shipped ideas priced off a stale bar, sorted. A stale
// name that no idea rests on is a data note; one that reaches the output is a
// caveat on a level a trader would act at.
func staleIdeas(p *quant.Pack, res *model.IdeasResult) []string {
	if p == nil || res == nil || len(p.Stale) == 0 {
		return nil
	}
	stale := make(map[string]bool, len(p.Stale))
	for _, t := range p.Stale {
		stale[strings.ToUpper(t)] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, idea := range res.Ideas {
		t := strings.ToUpper(strings.TrimSpace(idea.Ticker))
		if stale[t] && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// staleTickers lists the names whose newest bar trails the last completed
// session *on their own market*, sorted.
//
// The two runs on 2026-08-29 priced NVDA, MU and PLTR off Thursday closes while
// FCX and the Asian names carried Friday's — the US entries were cached before
// Friday's bar existed and the cache had no TTL to expire them. Nothing compared
// the two, so entry, stop and target were computed to the cent off a session-old
// close, mitigated only by "re-price before entering" in a note.
//
// The first fix compared every name against the newest bar *anyone* in the pack
// had, which swapped one error for another: markets do not close together. The
// 2026-09-01 run started at 12:56 UTC with Tokyo and Frankfurt already reporting
// that day and New York yet to open, so all seven US names were flagged stale
// against a Japanese session, refetched for nothing, and AMGN was docked three
// confidence points because agents/chief-analyst.md makes a `flags:` caveat a
// reason to lower one. Each market is now measured against its own clock, which
// is the only comparison that means anything.
func staleTickers(asOf map[string]string, now time.Time) []string {
	var out []string
	for t, d := range asOf {
		if d == "" { // an unknown date is not evidence of staleness
			continue
		}
		if d < lastTradingDay(now, marketdata.MarketCloseUTC(t)) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// staleFor reports whether one ticker's price series trails its own market's
// last completed session. It reads the same quant.Pack.Stale list the run's
// warnings and the metric's own flag are built from, so the risk gate, the
// Chief's prompt and metadata.json can never disagree about which names are
// stale.
func staleFor(v verified, ticker string) bool {
	if v.Quant == nil {
		return false
	}
	t := strings.ToUpper(strings.TrimSpace(ticker))
	for _, s := range v.Quant.Stale {
		if strings.ToUpper(s) == t {
			return true
		}
	}
	return false
}
