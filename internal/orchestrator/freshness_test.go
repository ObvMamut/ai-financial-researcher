package orchestrator

import (
	"testing"
	"time"
)

// A Saturday run priced 8 of 12 names off Thursday closes: the US names' cache
// entries predated Friday's bar while FCX and the Asian names had it. Nothing
// compared the two, so precise entries shipped off a session-old close.
func TestStaleTickersAgainstThePack(t *testing.T) {
	asOf := map[string]string{
		"NVDA": "2026-08-27", // Thursday
		"MU":   "2026-08-27",
		"FCX":  "2026-08-28", // Friday — the newest bar anyone has
		"TSM":  "2026-08-28",
	}
	got := staleTickers(asOf, "2026-08-28")
	if want := []string{"MU", "NVDA"}; !equalStrings(got, want) {
		t.Errorf("staleTickers = %v, want %v", got, want)
	}

	// Everyone on the same bar: nothing is stale.
	if got := staleTickers(map[string]string{"A": "2026-08-28", "B": "2026-08-28"}, "2026-08-28"); len(got) != 0 {
		t.Errorf("staleTickers = %v on a uniform pack, want none", got)
	}
	// A missing date cannot be judged and must not be reported as stale.
	if got := staleTickers(map[string]string{"A": "", "B": "2026-08-28"}, "2026-08-28"); len(got) != 0 {
		t.Errorf("staleTickers = %v, want none — an unknown date is not evidence of staleness", got)
	}
}

// The pack itself can be stale as a whole — every name a session behind — which
// no cross-ticker comparison can see. The last completed trading day is the
// reference.
func TestLastTradingDayBefore(t *testing.T) {
	cases := []struct {
		now  string // a wall-clock instant in UTC
		want string
	}{
		// Saturday and Sunday both look back to Friday.
		{"2026-08-29T12:00:00Z", "2026-08-28"},
		{"2026-08-30T12:00:00Z", "2026-08-28"},
		// Monday before the US close still expects Friday's bar.
		{"2026-08-31T12:00:00Z", "2026-08-28"},
		// Monday well after the close expects Monday's.
		{"2026-08-31T23:00:00Z", "2026-08-31"},
		// Mid-week after the close.
		{"2026-09-02T23:00:00Z", "2026-09-02"},
		// Mid-week before it: the previous weekday.
		{"2026-09-02T09:00:00Z", "2026-09-01"},
	}
	for _, c := range cases {
		now, err := time.Parse(time.RFC3339, c.now)
		if err != nil {
			t.Fatal(err)
		}
		if got := lastTradingDay(now); got != c.want {
			t.Errorf("lastTradingDay(%s) = %s, want %s", c.now, got, c.want)
		}
	}
}
