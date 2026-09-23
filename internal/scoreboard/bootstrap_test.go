package scoreboard

import (
	"fmt"
	"testing"
)

// closedCall is an entry whose window has completed with the given excess.
func closedCall(date string, excess float64) Entry {
	return Entry{GeneratedAt: date + "T08:00:00Z", CallDone: true, CallExcessPct: excess}
}

func TestExcessCIBracketsTheMeanAndIsDeterministic(t *testing.T) {
	var es []Entry
	for w := 0; w < 12; w++ {
		date := fmt.Sprintf("2026-%02d-%02d", 3+w/4, 2+7*(w%4))
		es = append(es, closedCall(date, float64(w%5)-1), closedCall(date, float64(w%3)))
	}
	ci, ok := excessCI(es)
	if !ok {
		t.Fatal("twelve weeks of closed calls must produce an interval")
	}
	mean := 0.0
	for _, e := range es {
		mean += e.CallExcessPct
	}
	mean /= float64(len(es))
	if !(ci.Low < mean && mean < ci.High) {
		t.Fatalf("interval [%v, %v] does not bracket the mean %v", ci.Low, ci.High, mean)
	}
	again, _ := excessCI(es)
	if again != ci {
		t.Fatalf("the bootstrap must be seeded: %v then %v", ci, again)
	}
}

// Calls made in one week share one market move. However many there are, they
// are one observation of that week, and an interval built from them would be a
// statement about nothing.
func TestExcessCIRefusesASingleWeek(t *testing.T) {
	es := []Entry{
		closedCall("2026-09-07", 3), closedCall("2026-09-08", -1),
		closedCall("2026-09-09", 2), closedCall("2026-09-10", 5),
	}
	if _, ok := excessCI(es); ok {
		t.Fatal("calls from a single week must not produce an interval")
	}
}

func TestExcessCIIgnoresPendingCalls(t *testing.T) {
	es := []Entry{closedCall("2026-09-01", 1), closedCall("2026-09-15", 1)}
	pending := closedCall("2026-09-22", 90)
	pending.CallDone = false
	ci, ok := excessCI(append(es, pending))
	if !ok || ci.High > 1.0001 {
		t.Fatalf("a pending call leaked into the interval: %+v ok=%v", ci, ok)
	}
}

// Two arms drawn in the same weeks see the same markets. Resampling the weeks
// together removes that shared move from the difference, so an arm that beats
// the other by exactly one point every week has a difference interval at one
// point, however violently the weeks themselves swing.
func TestExcessDiffCIIsPairedByWeek(t *testing.T) {
	var over, under []Entry
	for w := 0; w < 10; w++ {
		date := fmt.Sprintf("2026-%02d-%02d", 3+w/4, 2+7*(w%4))
		swing := float64((w*37)%17) - 8
		over = append(over, closedCall(date, swing+1))
		under = append(under, closedCall(date, swing))
	}
	ci, ok := excessDiffCI(over, under)
	if !ok {
		t.Fatal("ten shared weeks must produce a difference interval")
	}
	if ci.Low < 0.99 || ci.High > 1.01 {
		t.Fatalf("paired difference should sit at 1, got [%v, %v]", ci.Low, ci.High)
	}
}
