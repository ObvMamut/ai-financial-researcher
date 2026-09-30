package marketdata

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Point-in-time US earnings and report dates, read from SEC's per-issuer
// submissions JSON.
//
// edgarreports.go answers a narrower question — each filer's single most recent
// 10-Q/10-K, walked forward day by day through SEC's *daily* index — because
// that is all the live pre-screen's drift leg needs. The backtest lab needs
// every earnings-release date over a multi-year replay window, and walking the
// daily index one session at a time to find them is thousands of small
// requests where SEC already publishes one per-issuer document with a
// complete filing history: `filings.recent` (up to the newest ~1,000 filings)
// plus, once an issuer's history is longer than that, the older
// `filings.files` pages. So this reads that document instead — a handful of
// requests per ticker rather than one per session.
//
// The earnings-release date itself is the 8-K's *filing* date, not an
// estimate: Item 2.02 ("Results of Operations and Financial Condition") is the
// item SEC's own instructions define for reporting an earnings release, and a
// company has four business days to furnish it — close enough to same-day for
// a system whose clock is weeks.

// FilingHistory is one ticker's dated filing events, both sorted oldest first.
type FilingHistory struct {
	// Earnings holds the filing date of every 8-K carrying Item 2.02
	// (earnings release) since the requested window opened.
	Earnings []time.Time
	// Reports holds the filing date of every 10-Q/10-K since the same window.
	Reports []time.Time
}

// FilingHistorySource resolves point-in-time filing history for a set of US
// tickers. It never fails the whole call: a ticker it cannot resolve (no CIK,
// no US line, a fetch that errors) is simply absent from the returned map and
// named in the returned warnings, exactly as every other EDGAR leg degrades
// per ticker rather than per run.
type FilingHistorySource interface {
	FilingHistory(ctx context.Context, tickers []string, since time.Time) (map[string]FilingHistory, []string)
}

// NewFilingHistorySource returns the point-in-time filing-history resolver,
// backed by the same SEC client, CIK directory, disk cache and rate limiter as
// the rest of the EDGAR providers. contactEmail is required by SEC and, when
// empty, disables it — the same rule every other EDGAR leg follows.
func NewFilingHistorySource(contactEmail string, cache *Cache) FilingHistorySource {
	p, _ := NewEdgarProvider(contactEmail, cache).(*edgarProvider)
	return p
}

// FilingHistory resolves FilingHistory for each of tickers, back to `since`.
func (p *edgarProvider) FilingHistory(ctx context.Context, tickers []string, since time.Time) (map[string]FilingHistory, []string) {
	out := map[string]FilingHistory{}
	if p == nil || !p.Available() {
		return out, []string{"EDGAR contact email not configured: filing history unavailable"}
	}
	p.ensureCIKMap(ctx)

	var warnings []string
	for _, t := range tickers {
		if err := ctx.Err(); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", t, err))
			break
		}
		symbol, ok := providerSymbol(t)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s: not a US listing and has no US line", t))
			continue
		}
		cik, ok := p.lookupCIK(symbol)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s: no CIK in SEC directory", t))
			continue
		}
		hist, err := p.filingHistoryForCIK(ctx, cik, since)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", t, err))
			continue
		}
		out[strings.ToUpper(strings.TrimSpace(t))] = hist
	}
	return out, warnings
}

// filingHistoryForCIK reads one issuer's full filing history back to `since`:
// the current `recent` page, plus every older `files` page whose own
// FilingFrom/FilingTo range cannot be ruled out from reaching the window.
//
// A failed `files` page fails the whole ticker (a wrapped error, never
// swallowed) rather than being dropped in place: unlike the other EDGAR legs
// (addPlannedSales, addActivistStakes), where a failed fetch degrades one
// visible fact, a silently short Earnings/Reports slice here is
// indistinguishable from a filer that genuinely had nothing to report in
// those years — the drift leg would read "no Item 2.02 in the window" as a
// fact about the company rather than about the fetch. Failing the ticker
// (absent from FilingHistory's map, named in its warnings) makes that
// distinction visible instead of hiding it inside a partial history that
// looks exactly like a complete one. Also covers `ctx` cancellation and
// limiter errors surfaced through filingsPage/getJSON.
func (p *edgarProvider) filingHistoryForCIK(ctx context.Context, cik string, since time.Time) (FilingHistory, error) {
	sub, err := p.submissionIndex(ctx, cik)
	if err != nil {
		return FilingHistory{}, err
	}
	var hist FilingHistory
	accumulateFilingHistory(&hist, sub.Filings.Recent, since)

	for _, f := range sub.Filings.Files {
		if pageBeforeWindow(f, since) {
			continue
		}
		page, err := p.filingsPage(ctx, f)
		if err != nil {
			return FilingHistory{}, fmt.Errorf("files page %s: %w", f.Name, err)
		}
		accumulateFilingHistory(&hist, page, since)
	}

	sortAsc(hist.Earnings)
	sortAsc(hist.Reports)
	return hist, nil
}

// pageBeforeWindow reports whether a filings.files entry's own date range is
// entirely before `since`, so fetching it cannot add anything. An entry whose
// range cannot be parsed is never skipped this way — fetching one page too
// many costs a request; wrongly skipping one drops real filings from the
// window.
func pageBeforeWindow(f submissionsFilePage, since time.Time) bool {
	to, err := time.Parse("2006-01-02", strings.TrimSpace(f.FilingTo))
	return err == nil && to.Before(since)
}

// filingsPage fetches one filings.files page. Once published, an older page
// never changes — SEC pages an issuer's history out of `recent` only after it
// closes over — so it is read through the permanent cache (A2's
// GetPermanent/SetPermanent): the first backtest run to reach a given page
// pays for it once, and every later run of any span reads it back for free.
//
// The cache key is the page name together with the range SEC advertises for
// it (name|filingFrom|filingTo), not the name alone: if SEC ever repaginates
// and reuses a name for a shifted range, the stale page becomes a miss and is
// refetched instead of silently dropping the filings that moved.
func (p *edgarProvider) filingsPage(ctx context.Context, f submissionsFilePage) (filingsPage, error) {
	name := f.Name
	key := name + "|" + f.FilingFrom + "|" + f.FilingTo
	var page filingsPage
	if p.cache != nil {
		if ok, err := p.cache.GetPermanent(p.factsBase, p.Name(), "filingspage", key, &page); ok && err == nil {
			return page, nil
		}
	}
	url := fmt.Sprintf("%s/submissions/%s", p.factsBase, name)
	if err := p.getJSON(ctx, url, &page); err != nil {
		return filingsPage{}, err
	}
	if p.cache != nil {
		_ = p.cache.SetPermanent(p.factsBase, p.Name(), "filingspage", key, page)
	}
	return page, nil
}

// accumulateFilingHistory appends page's earnings and report dates on or after
// since into hist.
//
// 8-K/A (an amendment) is deliberately excluded from Earnings: the market
// reacted to the original 8-K's filing date, and an amendment is very often
// filed weeks later for an unrelated correction — counting it would print a
// second "earnings release" for the same quarter at a date nothing happened.
// This mirrors reportForms' exact-match exclusion of "10-Q/A" in
// edgarreports.go for the identical reason.
func accumulateFilingHistory(hist *FilingHistory, page filingsPage, since time.Time) {
	for i, rawForm := range page.Form {
		if i >= len(page.FilingDate) || i >= len(page.AccessionNumber) {
			// A row whose parallel arrays don't reach this index is one we
			// cannot trust the alignment of, not one to guess at.
			continue
		}
		filed, err := time.Parse("2006-01-02", strings.TrimSpace(page.FilingDate[i]))
		if err != nil || filed.Before(since) {
			continue
		}
		form := strings.TrimSpace(rawForm)
		switch {
		case reportForms[form]:
			hist.Reports = append(hist.Reports, filed)
		case form == "8-K":
			if i < len(page.Items) && hasItem202(page.Items[i]) {
				hist.Earnings = append(hist.Earnings, filed)
			}
		}
	}
}

// hasItem202 reports whether an 8-K's comma-separated Items field
// ("2.02,9.01") names Item 2.02, the earnings-release item.
func hasItem202(items string) bool {
	for _, it := range strings.Split(items, ",") {
		if strings.TrimSpace(it) == "2.02" {
			return true
		}
	}
	return false
}

func sortAsc(ts []time.Time) {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
}
