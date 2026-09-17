package marketdata

import (
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Market names an exchange calendar. It is coarser than the currency table:
// every US venue keeps one holiday schedule, and a listing's suffix is enough
// to say which schedule applies to it.
type Market string

const (
	MarketUS      Market = "US"
	MarketUnknown Market = ""
)

// marketCalendar is a published closure list together with the window it is
// known to cover. The window is the point: a list that ends on 2027-12-31 says
// nothing about 2028, and a projection past its end must stay estimated rather
// than inherit the confidence of the dates before it.
type marketCalendar struct {
	from, through string
	closures      map[string]bool
}

// nyseClosures is the NYSE/Nasdaq holiday schedule, which both exchanges publish
// years ahead (https://www.nyse.com/trade/hours-calendars). The 2026-09-07 run
// projected sessions straight through Labor Day because nothing here knew the US
// market was shut that day — and then reported the resulting dates as estimates,
// which was true but uninformative.
//
// Early closes are deliberately absent: a half session is a session, and this
// table answers "does the market trade that day", not "for how long".
var nyseClosures = marketCalendar{
	from: "2026-01-01", through: "2027-12-31",
	closures: closureSet(
		// 2026
		"2026-01-01", // New Year's Day
		"2026-01-19", // Martin Luther King, Jr. Day
		"2026-02-16", // Washington's Birthday
		"2026-04-03", // Good Friday
		"2026-05-25", // Memorial Day
		"2026-06-19", // Juneteenth
		"2026-07-03", // Independence Day (observed; July 4 is a Saturday)
		"2026-09-07", // Labor Day
		"2026-11-26", // Thanksgiving Day
		"2026-12-25", // Christmas Day
		// 2027
		"2027-01-01", // New Year's Day
		"2027-01-18", // Martin Luther King, Jr. Day
		"2027-02-15", // Washington's Birthday
		"2027-03-26", // Good Friday
		"2027-05-31", // Memorial Day
		"2027-06-18", // Juneteenth (observed; June 19 is a Saturday)
		"2027-07-05", // Independence Day (observed; July 4 is a Sunday)
		"2027-09-06", // Labor Day
		"2027-11-25", // Thanksgiving Day
		"2027-12-24", // Christmas Day (observed; December 25 is a Saturday)
	),
}

func closureSet(dates ...string) map[string]bool {
	out := make(map[string]bool, len(dates))
	for _, d := range dates {
		out[d] = true
	}
	return out
}

// bundledCalendars maps a market to its published schedule. Only markets whose
// schedule is actually bundled appear; every other listing keeps weekday-only
// projection and stays labelled estimated. Guessing a Tokyo or Frankfurt
// calendar would produce dates that look verified and are not.
var bundledCalendars = map[Market]marketCalendar{MarketUS: nyseClosures}

// MarketFor resolves a ticker to its exchange calendar. Only listings whose
// calendar this package actually carries resolve to a named market; everything
// else is MarketUnknown, which is the honest answer and the one that keeps a
// projected date labelled as an estimate.
func MarketFor(ticker string) Market {
	t := strings.ToUpper(strings.TrimSpace(ticker))
	if t == "" || strings.HasPrefix(t, "^") || strings.ContainsAny(t, "=/") {
		return MarketUnknown
	}
	i := strings.LastIndex(t, ".")
	if i <= 0 {
		return MarketUS
	}
	switch t[i+1:] {
	case "A", "B": // US share classes: BRK.B, BF.B
		return MarketUS
	}
	return MarketUnknown
}

// ResearchCalendar accepts ticker,date exchange closures. A '*' closure applies
// to every market. It layers over the bundled published schedules: a supplied
// row adds a closure, and never grants verification — a list cannot prove its
// own completeness, which is the whole reason the bundled tables carry an
// explicit coverage window.
//
// Replay uses actual bars and always respects the absolute expiry even on an
// estimated date.
type ResearchCalendar struct{ Closures map[string]map[string]bool }

func LoadResearchCalendar(path string) (ResearchCalendar, error) {
	c := ResearchCalendar{Closures: map[string]map[string]bool{}}
	if path == "" {
		return c, nil
	}
	f, e := os.Open(path)
	if e != nil {
		return c, e
	}
	defer f.Close()
	rows, e := csv.NewReader(f).ReadAll()
	if e != nil {
		return c, e
	}
	for _, r := range rows {
		if len(r) != 2 {
			return c, fmt.Errorf("holidays require ticker,date")
		}
		if r[0] == "ticker" {
			continue
		}
		if _, e = time.Parse("2006-01-02", r[1]); e != nil {
			return c, e
		}
		t := strings.ToUpper(strings.TrimSpace(r[0]))
		if c.Closures[t] == nil {
			c.Closures[t] = map[string]bool{}
		}
		c.Closures[t][r[1]] = true
	}
	return c, nil
}

// closed reports whether a ticker's market trades on a date.
func (c ResearchCalendar) closed(ticker, date string, d time.Time) bool {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		return true
	}
	if c.Closures[ticker][date] || c.Closures["*"][date] {
		return true
	}
	return bundledCalendars[MarketFor(ticker)].closures[date]
}

// SessionDate projects n trading sessions forward from an anchor and reports
// whether the projection is an estimate.
//
// It is an estimate unless a published schedule for this market covers the whole
// projected window. Beyond that window, and for every market whose schedule is
// not bundled, the walk is weekday-only and says so — a date that skips weekends
// and nothing else is a guess about a market that closes ten days a year.
func (c ResearchCalendar) SessionDate(ticker string, anchor time.Time, sessions int) (string, bool) {
	d := anchor.UTC()
	for n := 0; n < sessions; {
		d = d.AddDate(0, 0, 1)
		if c.closed(ticker, d.Format("2006-01-02"), d) {
			continue
		}
		n++
	}
	date := d.Format("2006-01-02")
	return date, !c.verified(ticker, anchor.UTC().Format("2006-01-02"), date)
}

// verified reports whether a published schedule covers [from, through] for this
// ticker's market.
func (c ResearchCalendar) verified(ticker, from, through string) bool {
	cal, ok := bundledCalendars[MarketFor(ticker)]
	return ok && from >= cal.from && through <= cal.through
}

// Sessions lists the projected dates of sessions 1..n, and whether that whole
// projection is an estimate. It is the block a model reads instead of counting
// weekdays itself: the 2026-09-07 run produced two dated errors in final output,
// one calling a date thirteen weekdays out "outside the window" and one calling
// tomorrow "already passed".
func (c ResearchCalendar) Sessions(ticker string, anchor time.Time, n int) ([]string, bool) {
	out := make([]string, 0, n)
	d := anchor.UTC()
	for len(out) < n {
		d = d.AddDate(0, 0, 1)
		date := d.Format("2006-01-02")
		if c.closed(ticker, date, d) {
			continue
		}
		out = append(out, date)
	}
	if len(out) == 0 {
		return out, true
	}
	return out, !c.verified(ticker, anchor.UTC().Format("2006-01-02"), out[len(out)-1])
}

// SessionsBetween counts trading sessions strictly after from, up to and
// including to. A negative count means to is before from. It answers "is this
// date inside the holding window" without asking a model to count weekdays.
func (c ResearchCalendar) SessionsBetween(ticker string, from, to time.Time) int {
	from, to = calendarDay(from), calendarDay(to)
	if to.Before(from) {
		return -c.SessionsBetween(ticker, to, from)
	}
	n := 0
	for d := from.AddDate(0, 0, 1); !d.After(to); d = d.AddDate(0, 0, 1) {
		if !c.closed(ticker, d.Format("2006-01-02"), d) {
			n++
		}
	}
	return n
}

func calendarDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// OnOrAfter maps a date label to a session, retaining uncertainty for markets
// and years not covered by the bundled schedule.
func (c ResearchCalendar) OnOrAfter(ticker string, date time.Time) (time.Time, bool) {
	d := calendarDay(date)
	for c.closed(ticker, d.Format("2006-01-02"), d) {
		d = d.AddDate(0, 0, 1)
	}
	return d, !c.verified(ticker, date.Format("2006-01-02"), d.Format("2006-01-02"))
}

// Before returns the last session strictly before an event date.
func (c ResearchCalendar) Before(ticker string, date time.Time) (time.Time, bool) {
	d := calendarDay(date).AddDate(0, 0, -1)
	for c.closed(ticker, d.Format("2006-01-02"), d) {
		d = d.AddDate(0, 0, -1)
	}
	return d, !c.verified(ticker, d.Format("2006-01-02"), date.Format("2006-01-02"))
}

// EarlyCloseMinutes supplies verified exceptions to regular US equity hours.
// A false result means no bundled exception, not verified hours for other markets.
func (c ResearchCalendar) EarlyCloseMinutes(ticker, date string) (int, bool) {
	if MarketFor(ticker) == MarketUS && (date == "2026-11-27" || date == "2026-12-24" || date == "2027-11-26") {
		return 13 * 60, true
	}
	return 0, false
}

// KnownClosures lists the published closures falling inside a window, so a
// projection can show its working rather than assert a date.
func (c ResearchCalendar) KnownClosures(ticker, from, through string) []string {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	seen := map[string]bool{}
	for _, set := range []map[string]bool{bundledCalendars[MarketFor(ticker)].closures, c.Closures[ticker], c.Closures["*"]} {
		for d := range set {
			if d >= from && d <= through {
				seen[d] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
