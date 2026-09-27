package marketdata

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Report dates: when did each US filer last file a 10-Q or a 10-K?
//
// The pipeline already knew the *next* earnings date and used it as a risk — a
// scheduled event inside the holding window that a position note has to address.
// It knew nothing about the last one, and the last one is where the only
// well-documented effect whose horizon matches this system lives: prices drift
// in the direction of an earnings surprise for weeks after it, which is exactly
// the two-to-three-week window every idea here is written for.
//
// Everything else the pre-screen ranks on is a trailing return over three or
// twelve months. Those are real factors and they are right about the next
// quarter or the next year; the code that computes them says so. This is the one
// input in the funnel whose clock matches the question.
//
// # Why the daily index
//
// The obvious source is one submissions document per issuer, which this provider
// already fetches for the shortlist. Universe-wide that is ~150 requests and a
// few hundred megabytes of JSON to extract one date each, every run.
//
// The quarterly full index is one request but 55 MB.
//
// The daily index is ~1 MB per session and, once a day has been published, it
// never changes again. So the parsed result caches permanently (Cache.
// GetPermanent/SetPermanent — keyed on the day only, no calendar date, so the
// entry survives past the day it was written), a run fetches only the
// sessions that have appeared since the last one — normally a single file —
// and the first build of a lookback window costs a couple of dozen small
// requests spread over SEC's rate limiter.
//
// Only US filers appear. A foreign listing with no US line has no row here, and
// carries no drift signal at all rather than a wrong one.

// ReportDateSource resolves the most recent periodic-report filing date for each
// of the given tickers.
//
// It never returns an error. The drift leg is additive — a name without a report
// date simply has no drift signal — so a failed fetch must degrade the signal,
// never the pre-screen that carries it.
type ReportDateSource interface {
	ReportDates(ctx context.Context, tickers []string, since time.Time) map[string]time.Time
}

// reportForms are the filings that mark a periodic report. Exact matches only:
// "10-Q/A" is an amendment to a report already counted and would move the date
// to a restatement, and "NT 10-Q" is notice that the report is *late* and has a
// different first field entirely.
var reportForms = map[string]bool{"10-K": true, "10-Q": true}

// maxDailyIndex bounds one daily index read. The files run around 1 MB; the cap
// is generous enough for the busiest day of a reporting season and small enough
// that a wrong URL answered with something enormous cannot exhaust memory.
const maxDailyIndex = 32 << 20

// NewEdgarReportDates returns the report-date index, backed by the same SEC
// client, CIK directory, disk cache and rate limiter as the fundamentals
// provider. contactEmail is required by SEC and, when empty, disables it — the
// same rule every other EDGAR leg follows.
func NewEdgarReportDates(contactEmail string, cache *Cache) ReportDateSource {
	p, _ := NewEdgarProvider(contactEmail, cache).(*edgarProvider)
	return p
}

// ReportDates returns ticker → the most recent 10-Q or 10-K filing date at or
// after `since`. A ticker with no filing in the window is absent from the map.
func (p *edgarProvider) ReportDates(ctx context.Context, tickers []string, since time.Time) map[string]time.Time {
	out := map[string]time.Time{}
	if p == nil || !p.Available() || len(tickers) == 0 {
		return out
	}
	p.ensureCIKMap(ctx)

	// CIK → the tickers asking about it. Share classes share a filer, so this is
	// one-to-many; both classes get the same report date, which is correct.
	wanted := map[string][]string{}
	p.mu.Lock()
	for _, t := range tickers {
		if cik, ok := p.cikMap[secTicker(t)]; ok {
			wanted[cik] = append(wanted[cik], strings.ToUpper(strings.TrimSpace(t)))
		}
	}
	p.mu.Unlock()
	if len(wanted) == 0 {
		return out
	}

	// Walk forward so a later filing overwrites an earlier one and the map ends
	// holding each filer's most recent report.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for d := since.UTC().Truncate(24 * time.Hour); !d.After(today); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out
		}
		for _, cik := range p.reportFilersOn(ctx, d) {
			for _, t := range wanted[cik] {
				out[t] = d
			}
		}
	}
	return out
}

// reportFilersOn returns the CIKs that filed a periodic report on one session.
//
// A published day's index is immutable, so the parsed result goes through
// Cache.GetPermanent/SetPermanent — no date in the key, so tomorrow's run
// reads back the same file instead of re-walking it under a key that rotated
// with the calendar. (Cache.Get/Set would do exactly that: their key hashes in
// today's date, so an entry written today is already a miss tomorrow.) The
// current day is the exception: it is still being written, so it is re-read
// each run, and never cached at all — permanently or otherwise.
func (p *edgarProvider) reportFilersOn(ctx context.Context, day time.Time) []string {
	key := day.Format("20060102")
	fresh := day.Equal(time.Now().UTC().Truncate(24 * time.Hour))

	var cached []string
	if p.cache != nil && !fresh {
		if ok, err := p.cache.GetPermanent(p.tickersBase, p.Name(), "reportdates", key, &cached); ok && err == nil {
			return cached
		}
	}

	url := fmt.Sprintf("%s/Archives/edgar/daily-index/%d/QTR%d/form.%s.idx",
		p.tickersBase, day.Year(), (int(day.Month())-1)/3+1, key)
	body, err := p.getRaw(ctx, url, maxDailyIndex)
	if err != nil {
		// A missing day is the normal case for a market holiday, and a failed
		// one costs this session's filers and nothing else. Neither is cached:
		// caching an empty day would make a transient failure permanent.
		return nil
	}
	ciks := parseReportFilers(string(body))
	if p.cache != nil && !fresh {
		_ = p.cache.SetPermanent(p.tickersBase, p.Name(), "reportdates", key, ciks)
	}
	return ciks
}

// parseReportFilers reads the CIKs filing a periodic report out of one daily
// index.
//
// The file is fixed-width and the company name contains spaces, so the columns
// are read from the ends: the form type is the first field and the CIK is the
// third from last. Splitting on whitespace and indexing forward would put a
// two-word company name's filing under the wrong filer.
func parseReportFilers(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || !reportForms[f[0]] {
			continue
		}
		cik := padCIK(f[len(f)-3])
		if cik == "" || seen[cik] {
			continue
		}
		seen[cik] = true
		out = append(out, cik)
	}
	return out
}

// reportDatesOnce memoises one ReportDates call for the life of a run, so the
// pre-screen and anything else that wants the same window share one walk of the
// index rather than repeating it.
type reportDatesOnce struct {
	src  ReportDateSource
	mu   sync.Mutex
	done bool
	out  map[string]time.Time
}

// NewReportDatesOnce wraps a source so repeated calls with the same window are
// answered from the first result.
func NewReportDatesOnce(src ReportDateSource) ReportDateSource { return &reportDatesOnce{src: src} }

func (r *reportDatesOnce) ReportDates(ctx context.Context, tickers []string, since time.Time) map[string]time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return r.out
	}
	r.done = true
	if r.src != nil {
		r.out = r.src.ReportDates(ctx, tickers, since)
	}
	if r.out == nil {
		r.out = map[string]time.Time{}
	}
	return r.out
}
