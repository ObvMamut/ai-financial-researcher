package scoreboard

import (
	"sort"
	"strings"
	"time"
)

// Independence: how many *bets* does this history actually contain?
//
// The counts everywhere else in this package are counts of ideas, and an idea is
// not an observation. Five runs were fired on 2026-09-01 and three on
// 2026-08-29; September's 41 ideas are 22 distinct names; STLAM.MI SELL and
// AMGN BUY each appear five times. Re-proposing the same name in the same
// direction the same afternoon does not produce a second data point about
// whether that call was right — it produces the same data point again, and the
// win rate reads it as two.
//
// This matters more than it looks, because of what the counts are *for*:
// MinClosedForFeedback gates whether the Chief is shown a track record at all,
// and MinClosedForEdge swaps the risk gate's assumed edge for a measured one.
// Both thresholds exist to keep a thin sample from being read as a finding. A
// duplicated sample defeats them exactly — it reaches the threshold without ever
// reaching the evidence.
//
// So the cells are counted over deduplicated entries. Nothing is hidden: the
// display still lists every idea, and the dropped count is reported.

// DefaultDedupeWindowDays is how long one call speaks for. Seven calendar days
// is five sessions: a repeat inside that window is the same bet re-proposed
// before the first one had a chance to resolve.
//
// It is calendar days rather than sessions because entries carry a timestamp
// and not a bar index, and reconstructing an exchange calendar to save two days
// of resolution would be precision this number does not have.
const DefaultDedupeWindowDays = 7

// Dedupe returns the entries that count as independent observations, and how
// many were dropped.
//
// The rule is one observation per (ticker, direction) per window, keeping the
// *earliest* — the first time the pipeline made a call is when it made it, and
// keeping the last would silently let a re-proposal be scored off a better
// entry price than the one originally published.
//
// Entries with no parseable timestamp are kept. A missing date is not evidence
// of duplication, and dropping on it would quietly delete the oldest runs.
func Dedupe(entries []Entry, windowDays int) ([]Entry, int) {
	kept, dropped := DedupeMask(entries, windowDays)
	if dropped == 0 {
		return entries, 0
	}
	// Preserve the caller's original order: the scoreboard prints newest-run
	// first, and reordering its rows to suit an internal sort would be a
	// surprising side effect of counting.
	out := make([]Entry, 0, len(entries)-dropped)
	for i, e := range entries {
		if kept[i] {
			out = append(out, e)
		}
	}
	return out, dropped
}

// DedupeMask is Dedupe reported positionally: kept[i] says whether entries[i]
// counts as an independent observation. Callers that must keep every entry —
// the attribution's fill census counts all of them while its outcome cells
// count only these — index into it rather than matching values back to slots.
func DedupeMask(entries []Entry, windowDays int) (kept []bool, dropped int) {
	kept = make([]bool, len(entries))
	if windowDays <= 0 {
		for i := range kept {
			kept[i] = true
		}
		return kept, 0
	}
	window := time.Duration(windowDays) * 24 * time.Hour

	type key struct{ ticker, direction, research string }
	// Sort a copy by time so "earliest wins" is decided by the clock rather
	// than by directory order.
	idx := make([]int, len(entries))
	for i := range idx {
		idx[i] = i
	}
	at := make([]time.Time, len(entries))
	ok := make([]bool, len(entries))
	for i, e := range entries {
		at[i], ok[i] = parseGeneratedAt(e.GeneratedAt)
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ia, ib := idx[a], idx[b]
		if ok[ia] != ok[ib] {
			return ok[ia] // dated entries first; undated ones can never displace one
		}
		return at[ia].Before(at[ib])
	})

	last := map[key]time.Time{}
	for _, i := range idx {
		if !ok[i] {
			kept[i] = true
			continue
		}
		k := key{strings.ToUpper(strings.TrimSpace(entries[i].Ticker)), entries[i].Direction, entries[i].ResearchMode}
		if prev, seen := last[k]; seen && at[i].Sub(prev) < window {
			dropped++
			continue
		}
		last[k] = at[i]
		kept[i] = true
	}
	return kept, dropped
}

// parseGeneratedAt reads an entry's generation timestamp, accepting the
// full RFC3339 stamp ideas.json carries and the bare date some older runs wrote.
func parseGeneratedAt(ts string) (time.Time, bool) {
	if ts == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t, true
	}
	if len(ts) >= 10 {
		if t, err := time.Parse("2006-01-02", ts[:10]); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
