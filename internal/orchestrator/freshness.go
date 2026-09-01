package orchestrator

import (
	"sort"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
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
