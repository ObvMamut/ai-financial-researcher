package marketdata

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Wait must turn a momentarily empty bucket into a slightly slower fetch. With
// Allow, the burst-th request in a batch was simply dropped and the ticker went
// ungrounded for the rest of the run.
func TestLimiterWaitBlocksThenAdmits(t *testing.T) {
	// 600 rpm = 10 tokens/s, burst 1: the second request must wait ~100ms.
	l := NewLimiter(100, 600, 1)

	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	if l.Allow() {
		t.Fatal("burst of 1 should be spent after one admission")
	}

	start := time.Now()
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 50*time.Millisecond {
		t.Errorf("second Wait returned in %s — it did not actually wait for a refill", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("second Wait took %s — far longer than one refill interval", elapsed)
	}
}

// The daily budget is genuine exhaustion, not a transient queue: waiting inside
// a run can never refill it, so Wait must refuse immediately.
func TestLimiterWaitRefusesExhaustedDailyBudget(t *testing.T) {
	l := NewLimiter(1, 600, 5)
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("first Wait: %v", err)
	}

	start := time.Now()
	err := l.Wait(context.Background())
	if err == nil {
		t.Fatal("Wait should refuse once the daily budget is spent")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Wait blocked %s on an exhausted daily budget; it must fail fast", elapsed)
	}
}

func TestLimiterWaitHonoursContext(t *testing.T) {
	l := NewLimiter(100, 1, 1) // 1 rpm: the next token is a minute away
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("first Wait: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := l.Wait(ctx); err == nil {
		t.Fatal("Wait should return once ctx expires")
	}
}

// A provider's daily quota belongs to the API key, not to one process. An
// in-memory count let three `go run`s spend 75 of a 25-request tier and then
// blame AlphaVantage for the failures.
func TestLimiterDailyCountSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	first := NewPersistentLimiter(3, 600, 5, dir, "testprov")
	for i := 0; i < 3; i++ {
		if !first.Allow() {
			t.Fatalf("request %d should have been admitted", i+1)
		}
	}
	if first.Allow() {
		t.Fatal("fourth request should exceed the daily limit")
	}

	// Simulate a process restart: a brand new limiter over the same directory.
	second := NewPersistentLimiter(3, 600, 5, dir, "testprov")
	if second.Allow() {
		t.Error("restarted limiter re-granted a spent daily budget")
	}

	// A different provider's budget is its own.
	other := NewPersistentLimiter(3, 600, 5, dir, "otherprov")
	if !other.Allow() {
		t.Error("a different provider's budget should be untouched")
	}
}

func TestLimiterCorruptStateStartsFresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "limiter-testprov.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := NewPersistentLimiter(2, 600, 5, dir, "testprov")
	if !l.Allow() {
		t.Error("a corrupt state file should not block the day's budget")
	}
}

// A stale count from a previous day must not eat today's budget.
func TestLimiterResetsOnNewDay(t *testing.T) {
	dir := t.TempDir()
	spent := NewPersistentLimiter(2, 600, 5, dir, "testprov")
	if !spent.Allow() || !spent.Allow() || spent.Allow() {
		t.Fatal("setup: budget of 2 not spent as expected")
	}

	// Backdate the persisted reset marker by a day, as an overnight run would.
	stale := NewPersistentLimiter(2, 600, 5, dir, "testprov")
	stale.mu.Lock()
	stale.lastReset = time.Now().AddDate(0, 0, -1)
	stale.save()
	stale.mu.Unlock()

	fresh := NewPersistentLimiter(2, 600, 5, dir, "testprov")
	if !fresh.Allow() {
		t.Error("budget should have reset on a new UTC day")
	}
}

// Burst defaults to the tier's per-minute allowance, so a 5/min provider can
// fire its five without an artificial hardcoded ceiling.
func TestLimiterBurstDefaultsToRPM(t *testing.T) {
	l := NewLimiter(100, 5, 0)
	for i := 0; i < 5; i++ {
		if !l.Allow() {
			t.Fatalf("burst request %d rejected; burst should default to the 5/min tier", i+1)
		}
	}
	if l.Allow() {
		t.Error("a sixth immediate request should be held back")
	}
}

// A spent free tier is a property of the key that a run can read *before* it
// spends five minutes rediscovering it one ticker at a time. On 2026-09-01 the
// AlphaVantage counter stood at 24 of 25 and the run reported it as a scatter
// of unrelated per-ticker failures.
func TestLimiterDailyBudgetReportsTheRemainingCount(t *testing.T) {
	l := NewLimiter(3, 600, 5)

	used, limit := l.DailyBudget()
	if used != 0 || limit != 3 {
		t.Fatalf("DailyBudget() = (%d, %d), want (0, 3) on a fresh limiter", used, limit)
	}
	// Reading the budget must not spend it.
	if used, _ := l.DailyBudget(); used != 0 {
		t.Errorf("the accessor consumed a request: used = %d", used)
	}

	for i := 0; i < 3; i++ {
		if !l.Allow() {
			t.Fatalf("request %d should have been admitted", i+1)
		}
	}
	if used, limit := l.DailyBudget(); used != limit || limit-used != 0 {
		t.Errorf("DailyBudget() = (%d, %d) at the cap, want zero remaining", used, limit)
	}
}

// tryAllow resets the count on a new UTC day before admitting. An accessor that
// skips that check reports yesterday's spend on the first call of a new day —
// which would announce an exhausted key at the top of a run that has its whole
// budget available.
func TestLimiterDailyBudgetResetsOnANewDay(t *testing.T) {
	dir := t.TempDir()
	spent := NewPersistentLimiter(2, 600, 5, dir, "testprov")
	if !spent.Allow() || !spent.Allow() {
		t.Fatal("setup: budget of 2 not spent as expected")
	}
	spent.mu.Lock()
	spent.lastReset = time.Now().AddDate(0, 0, -1)
	spent.save()
	spent.mu.Unlock()

	fresh := NewPersistentLimiter(2, 600, 5, dir, "testprov")
	used, limit := fresh.DailyBudget()
	if used != 0 || limit != 2 {
		t.Errorf("DailyBudget() = (%d, %d) on a new UTC day, want a full budget of (0, 2)", used, limit)
	}
	// And the reset must have actually happened, not just been reported.
	if !fresh.Allow() {
		t.Error("the new day's budget was reported free but not granted")
	}
}
