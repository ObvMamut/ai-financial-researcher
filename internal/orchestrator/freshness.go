package orchestrator

import (
	"sort"
	"time"
)

// usMarketCloseUTC is the hour by which a US session's daily bar is published.
// The NYSE close is 20:00 UTC in summer and 21:00 in winter; Yahoo's daily bar
// settles shortly after. 22:00 clears both without needing a tz database.
const usMarketCloseUTC = 22

// lastTradingDay returns the ISO date of the most recent session whose daily bar
// should exist by now. Weekends look back to Friday; a weekday before the close
// looks back to the previous weekday. Exchange holidays are not modelled — a
// holiday makes this one day optimistic, which produces a warning to check
// rather than a wrong price.
func lastTradingDay(now time.Time) string {
	d := now.UTC()
	if d.Hour() < usMarketCloseUTC {
		d = d.AddDate(0, 0, -1)
	}
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d.Format("2006-01-02")
}

// staleTickers lists the names whose newest bar trails the newest bar anyone in
// the pack has, sorted.
//
// The two runs on 2026-08-29 priced NVDA, MU and PLTR off Thursday closes while
// FCX and the Asian names carried Friday's — the US entries were cached before
// Friday's bar existed and the cache had no TTL to expire them. Nothing compared
// the two, so entry, stop and target were computed to the cent off a session-old
// close, mitigated only by "re-price before entering" in a note.
//
// newest is passed in rather than derived so the caller can compare against the
// last completed trading day instead, catching a pack that is uniformly stale.
func staleTickers(asOf map[string]string, newest string) []string {
	var out []string
	for t, d := range asOf {
		if d == "" { // an unknown date is not evidence of staleness
			continue
		}
		if d < newest {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}
