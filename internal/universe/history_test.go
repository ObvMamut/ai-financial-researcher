package universe

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func date(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestSP500HistoryMatchesKnownIndexEvents(t *testing.T) {
	h, err := LoadHistory("sp500")
	if err != nil {
		t.Fatal(err)
	}
	// Each case is a public index event, checked a week either side so the
	// exact effective day never matters.
	cases := []struct {
		ticker, in, out string
	}{
		{"TSLA", "2020-12-28", "2020-12-14"}, // added 2020-12-21
		{"UBER", "2023-12-26", "2023-12-11"}, // added 2023-12-18
		{"PLTR", "2024-09-30", "2024-09-16"}, // added 2024-09-23
		{"CRWD", "2024-07-01", "2024-06-17"}, // added 2024-06-24
		{"SIVB", "2023-03-01", "2023-03-22"}, // removed 2023-03-15
		{"FRC", "2023-04-03", "2023-05-15"},  // removed after JPMorgan's takeover
		{"TWTR", "2022-10-03", "2022-11-14"}, // removed after the Musk buyout
		{"ATVI", "2023-10-02", "2023-10-30"}, // removed after Microsoft's acquisition
		{"FB", "2021-06-01", "2022-07-01"},   // became META 2022-06-09
		{"META", "2022-07-01", "2021-06-01"},
		{"CHK", "2016-01-04", "2019-01-02"},  // a departed loser the hanshof file lacks
		{"YHOO", "2016-06-01", "2017-07-03"}, // removed after Verizon's acquisition
	}
	for _, c := range cases {
		if !slices.Contains(h.MembersOn(date(c.in)), c.ticker) {
			t.Errorf("%s not a member on %s", c.ticker, c.in)
		}
		if slices.Contains(h.MembersOn(date(c.out)), c.ticker) {
			t.Errorf("%s still a member on %s", c.ticker, c.out)
		}
	}
}

func TestSP500HistoryHandsATickerToANewCompanyAsTwoIntervals(t *testing.T) {
	// 21st Century Fox's FOX/FOXA left on 2019-03-19 and Fox Corp's took the
	// same tickers that day: two companies, so two intervals each.
	h, err := LoadHistory("sp500")
	if err != nil {
		t.Fatal(err)
	}
	var fox []Interval
	for _, iv := range h.Intervals {
		if iv.Ticker == "FOX" {
			fox = append(fox, iv)
		}
	}
	if len(fox) < 2 || !fox[0].To.Equal(date("2019-03-19")) || !fox[1].From.Equal(date("2019-03-19")) {
		t.Errorf("FOX intervals = %+v, want one ending and one starting on 2019-03-19", fox)
	}
}

func TestSP500HistorySizeIsAlwaysAnIndexOfAbout500(t *testing.T) {
	h, err := LoadHistory("sp500")
	if err != nil {
		t.Fatal(err)
	}
	for d := h.Baseline; d.Before(date("2026-10-03")); d = d.AddDate(0, 0, 7) {
		if n := len(h.MembersOn(d)); n < 498 || n > 508 {
			t.Fatalf("%d members on %s", n, d.Format("2006-01-02"))
		}
	}
}

func TestSP500HistoryContainsTodaysSample(t *testing.T) {
	h, err := LoadHistory("sp500")
	if err != nil {
		t.Fatal(err)
	}
	u, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	members := h.MembersOn(date("2026-10-02"))
	for _, c := range u.Constituents("sp500") {
		if !slices.Contains(members, strings.ToUpper(c.Ticker)) {
			t.Errorf("%s is in today's sample but not a member on 2026-10-02", c.Ticker)
		}
	}
}

func TestParseHistoryRejectsAnInconsistentFile(t *testing.T) {
	for name, body := range map[string]string{
		"removes a non-member": "date,added,removed,renamed\n2015-01-01,A B,,\n2016-01-04,,C,\n",
		"adds a member":        "date,added,removed,renamed\n2015-01-01,A B,,\n2016-01-04,A,,\n",
		"dates out of order":   "date,added,removed,renamed\n2015-01-01,A B,,\n2016-01-04,C,,\n2016-01-01,D,,\n",
		"renames a non-member": "date,added,removed,renamed\n2015-01-01,A B,,\n2016-01-04,,,C>D\n",
	} {
		if _, err := parseHistory("x", strings.NewReader(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNQ100HistoryMatchesKnownIndexEvents(t *testing.T) {
	h, err := LoadHistory("nq100")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ticker, in, out string
	}{
		{"XLNX", "2022-02-01", "2022-03-01"}, // replaced by AZN on 2022-02-22, after AMD's acquisition
		{"AZN", "2022-03-01", "2022-02-01"},
		{"CDW", "2023-12-26", "2023-12-11"}, // December 2023 reconstitution
		{"ZM", "2023-12-11", "2023-12-26"},
		{"FB", "2022-06-01", "2022-06-16"}, // became META 2022-06-09
		{"CELG", "2019-11-01", "2019-12-02"},
	}
	for _, c := range cases {
		if !slices.Contains(h.MembersOn(date(c.in)), c.ticker) {
			t.Errorf("%s not a member on %s", c.ticker, c.in)
		}
		if slices.Contains(h.MembersOn(date(c.out)), c.ticker) {
			t.Errorf("%s still a member on %s", c.ticker, c.out)
		}
	}
	for d := h.Baseline; d.Before(date("2026-10-03")); d = d.AddDate(0, 0, 7) {
		if n := len(h.MembersOn(d)); n < 99 || n > 108 {
			t.Fatalf("%d members on %s", n, d.Format("2006-01-02"))
		}
	}
}

func TestHistorySectorsCoverTodayAndRenamedTickers(t *testing.T) {
	sec, err := HistorySectors()
	if err != nil {
		t.Fatal(err)
	}
	for ticker, want := range map[string]string{"META": "Communication Services", "FB": "Communication Services", "BRK.B": "Financials"} {
		if sec[ticker] != want {
			t.Errorf("%s sector %q, want %q", ticker, sec[ticker], want)
		}
	}
	if _, ok := sec["CHK"]; ok {
		t.Error("a departed company has a sector; the source cannot place it")
	}
}

// Every row of the nq100 sample is a Nasdaq-100 member on the history's last
// date — the sample may be partial, never wrong.
func TestNQ100SampleHoldsOnlyCurrentMembers(t *testing.T) {
	h, err := LoadHistory("nq100")
	if err != nil {
		t.Fatal(err)
	}
	u, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	last := h.Last
	members := h.MembersOn(last)
	for _, c := range u.Constituents("nq100") {
		if !slices.Contains(members, strings.ToUpper(c.Ticker)) {
			t.Errorf("%s is in nq100.csv but not a Nasdaq-100 member on %s", c.Ticker, last.Format("2006-01-02"))
		}
	}
}
