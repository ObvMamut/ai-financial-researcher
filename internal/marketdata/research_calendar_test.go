package marketdata

import (
	"testing"
	"time"
)

func TestPublishedCalendarCoverageAndDateCounting(t *testing.T) {
	c := ResearchCalendar{}
	for _, tc := range []struct {
		ticker, anchor, want string
		estimated            bool
	}{
		{"AAA", "2026-09-04", "2026-09-08", false},
		{"AAA", "2027-12-30", "2027-12-31", false},
		{"AAA", "2027-12-31", "2028-01-03", true},
		{"9988.HK", "2026-09-07", "2026-09-08", true},
	} {
		anchor, _ := time.Parse("2006-01-02", tc.anchor)
		got, estimated := c.SessionDate(tc.ticker, anchor, 1)
		if got != tc.want || estimated != tc.estimated {
			t.Errorf("%+v: got %s estimated=%v", tc, got, estimated)
		}
	}
	from := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	if n := c.SessionsBetween("9988.HK", from, to); n != 13 {
		t.Fatalf("Alibaba date: %d sessions", n)
	}
	if n := c.SessionsBetween("9988.HK", to, from); n != -13 {
		t.Fatalf("reverse dates: %d", n)
	}
	for _, ticker := range []string{"", "^N225", "EURUSD=X"} {
		if MarketFor(ticker) != MarketUnknown {
			t.Errorf("assumed US exchange for %q", ticker)
		}
	}
}
