package universe

import (
	"embed"
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Point-in-time index membership, for the lab's survivorship-free universe.
//
// The universe CSVs are today's sample of each index, so a replay over them
// never sees the companies that left: disproportionately the losers, which
// flatters every momentum and long-side number the lab has produced. The
// history files record who was actually in the index on each date, with the
// tickers as they traded then. data/history/build_sp500.py documents how
// sp500_changes.csv is built and why it does not rest on the hanshof snapshot
// file alone.

//go:embed data/history/*_changes.csv data/history/history_sectors.csv
var historyFS embed.FS

// Interval is one continuous membership of one ticker: From is the first day
// in the index, To the first day out (zero while still a member). A ticker
// handed to another company, or renamed, closes one interval and its successor
// opens another, so each interval is one company under one symbol.
type Interval struct {
	Ticker string
	From   time.Time
	To     time.Time
}

// Contains reports whether d falls inside the interval.
func (iv Interval) Contains(d time.Time) bool {
	return !d.Before(iv.From) && (iv.To.IsZero() || d.Before(iv.To))
}

// History is one index's membership over time.
type History struct {
	Index     string
	Baseline  time.Time
	Intervals []Interval
}

// HistoryIndices are the indices that have a point-in-time history.
func HistoryIndices() []string { return []string{"sp500", "nq100"} }

// LoadHistory reads data/history/<index>_changes.csv.
func LoadHistory(index string) (*History, error) {
	f, err := historyFS.Open("data/history/" + index + "_changes.csv")
	if err != nil {
		return nil, fmt.Errorf("no point-in-time history for %q", index)
	}
	defer f.Close()
	return parseHistory(index, f)
}

func parseHistory(index string, rd io.Reader) (*History, error) {
	r := csv.NewReader(rd)
	r.Comment = '#'
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s history: %w", index, err)
	}
	if len(rows) < 2 || len(rows[0]) < 3 || rows[0][0] != "date" {
		return nil, fmt.Errorf("%s history: missing header or baseline", index)
	}
	h := &History{Index: index}
	open := map[string]time.Time{}
	closeIv := func(t string, d time.Time) {
		h.Intervals = append(h.Intervals, Interval{Ticker: t, From: open[t], To: d})
		delete(open, t)
	}
	var prev time.Time
	for i, row := range rows[1:] {
		d, err := time.Parse("2006-01-02", row[0])
		if err != nil {
			return nil, fmt.Errorf("%s history row %d: %w", index, i+2, err)
		}
		if i > 0 && !d.After(prev) {
			return nil, fmt.Errorf("%s history row %d: %s is not after %s", index, i+2, row[0], prev.Format("2006-01-02"))
		}
		prev = d
		field := func(k int) []string {
			if k < len(row) {
				return strings.Fields(row[k])
			}
			return nil
		}
		if i == 0 {
			h.Baseline = d
		}
		// One date's order: renames, then removals, then additions.
		for _, p := range field(3) {
			old, nw, ok := strings.Cut(p, ">")
			if !ok {
				return nil, fmt.Errorf("%s history %s: malformed rename %q", index, row[0], p)
			}
			if _, in := open[old]; !in {
				return nil, fmt.Errorf("%s history %s: renames %s, which is not a member", index, row[0], old)
			}
			closeIv(old, d)
			open[nw] = d
		}
		for _, t := range field(2) {
			if _, in := open[t]; !in {
				return nil, fmt.Errorf("%s history %s: removes %s, which is not a member", index, row[0], t)
			}
			closeIv(t, d)
		}
		for _, t := range field(1) {
			if _, in := open[t]; in {
				return nil, fmt.Errorf("%s history %s: adds %s, which is already a member", index, row[0], t)
			}
			open[t] = d
		}
	}
	for t, from := range open {
		h.Intervals = append(h.Intervals, Interval{Ticker: t, From: from})
	}
	sort.Slice(h.Intervals, func(i, j int) bool {
		a, b := h.Intervals[i], h.Intervals[j]
		if a.Ticker != b.Ticker {
			return a.Ticker < b.Ticker
		}
		return a.From.Before(b.From)
	})
	return h, nil
}

// MembersOn returns the tickers in the index on d, sorted.
func (h *History) MembersOn(d time.Time) []string {
	var out []string
	for _, iv := range h.Intervals {
		if iv.Contains(d) {
			out = append(out, iv.Ticker)
		}
	}
	sort.Strings(out)
	return out
}

// HistorySectors maps a ticker the histories name to its GICS sector, where
// one is known: today's members and the earlier tickers of renamed members
// (data/history/build_sectors.py). A company that has left the index is
// absent; the lab treats it as sector-unknown.
func HistorySectors() (map[string]string, error) {
	f, err := historyFS.Open("data/history/history_sectors.csv")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comment = '#'
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("history sectors: %w", err)
	}
	out := make(map[string]string, len(rows))
	for i, row := range rows {
		if i == 0 || len(row) < 2 {
			continue
		}
		out[row[0]] = row[1]
	}
	return out, nil
}
