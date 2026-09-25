package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// maxLimiterWait bounds a single Wait call. The per-minute bucket should admit a
// request within one refill interval; anything longer means the limiter is
// misconfigured, and a run must degrade rather than hang.
const maxLimiterWait = 2 * time.Minute

// Limiter manages a provider's per-minute burst and its daily budget.
//
// The two limits are different in kind. The per-minute bucket is *transient*:
// waiting a moment turns a would-be failure into a slightly slower fetch, so
// Wait blocks on it. The daily budget is *genuine exhaustion*: no amount of
// waiting inside a run will refill it, so both Allow and Wait refuse
// immediately. When statePath is set the daily count is persisted, so the budget
// survives process restarts instead of resetting to zero on every `go run`.
type Limiter struct {
	mu         sync.Mutex
	lastReset  time.Time
	dailyCount int
	dailyLimit int
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
	statePath  string // "" = in-memory only

	// reserved holds back this many of today's daily slots for WaitReserved.
	// Every other limiter leaves this at zero, so it changes nothing for them.
	reserved int
}

// NewLimiter builds an in-memory limiter for one provider tier. burst is how
// many requests may fire back-to-back before the refill rate binds; when
// non-positive it defaults to one minute's allowance (rpm), the unit provider
// tiers are quoted in.
func NewLimiter(dailyLimit int, rpm, burst float64) *Limiter {
	if burst <= 0 {
		burst = rpm
	}
	if burst < 1 {
		burst = 1
	}
	now := time.Now()
	return &Limiter{
		dailyLimit: dailyLimit,
		maxTokens:  burst,
		tokens:     burst,
		refillRate: rpm / 60.0,
		lastRefill: now,
		lastReset:  now,
	}
}

// NewPersistentLimiter is NewLimiter with the daily count stored under dir, keyed
// by name. A provider's daily quota is a property of the API key, not of one
// process, so an in-memory count silently over-spends it across runs.
func NewPersistentLimiter(dailyLimit int, rpm, burst float64, dir, name string) *Limiter {
	l := NewLimiter(dailyLimit, rpm, burst)
	if dir != "" && name != "" {
		l.statePath = filepath.Join(dir, "limiter-"+name+".json")
		l.load()
	}
	return l
}

// limiterState is the on-disk form of the daily budget.
type limiterState struct {
	DailyCount int       `json:"daily_count"`
	LastReset  time.Time `json:"last_reset"`
}

// load restores the persisted daily count. A missing or corrupt file is not an
// error: the limiter simply starts the day fresh.
func (l *Limiter) load() {
	data, err := os.ReadFile(l.statePath)
	if err != nil {
		return
	}
	var st limiterState
	if json.Unmarshal(data, &st) != nil {
		return
	}
	l.dailyCount = st.DailyCount
	l.lastReset = st.LastReset
}

// save persists the daily count. Callers hold l.mu.
func (l *Limiter) save() {
	if l.statePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.statePath), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(limiterState{DailyCount: l.dailyCount, LastReset: l.lastReset})
	if err != nil {
		return
	}
	_ = os.WriteFile(l.statePath, data, 0o644)
}

// Reserve holds back n of today's daily slots for WaitReserved: an ordinary
// Wait/Allow caller stops being admitted once dailyLimit-n requests are
// spent, leaving those n slots for a caller that cannot afford to lose the
// race for the day's last one. Zero (the default) reserves nothing.
func (l *Limiter) Reserve(n int) {
	l.mu.Lock()
	l.reserved = n
	l.mu.Unlock()
}

// ExhaustForDay remembers a provider-confirmed daily quota refusal across runs.
// Local counts alone cannot account for calls made by another application.
func (l *Limiter) ExhaustForDay() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.resetIfNewDayLocked(time.Now())
	l.dailyCount = l.dailyLimit
	l.save()
}

// sameUTCDay reports whether two instants fall on the same UTC calendar day.
// Provider quotas reset on the provider's own clock; UTC is the deterministic
// choice, and being an hours off only costs a little unused budget.
func sameUTCDay(a, b time.Time) bool {
	au, bu := a.UTC(), b.UTC()
	return au.Year() == bu.Year() && au.YearDay() == bu.YearDay()
}

// resetIfNewDayLocked rolls the daily count over when the calendar has. Callers
// hold l.mu. It is shared with DailyBudget rather than inlined in tryAllow
// because a reader that skips it reports yesterday's spend on the first call of
// a new day — announcing an exhausted key at the top of a run whose whole budget
// is in fact available.
func (l *Limiter) resetIfNewDayLocked(now time.Time) {
	if !sameUTCDay(now, l.lastReset) {
		l.dailyCount = 0
		l.lastReset = now
		l.save()
	}
}

// DailyBudget reports how much of today's allowance this key has spent, and the
// allowance itself; remaining is limit-used. It is the seam the orchestrator
// reads to log a nearly-spent free tier at run start and to report exhaustion
// once, instead of as one identical data_error per ticker.
//
// It rolls the day over if the calendar has, but never consumes a request:
// reading the budget is not spending it.
func (l *Limiter) DailyBudget() (used, limit int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.resetIfNewDayLocked(time.Now())
	return l.dailyCount, l.dailyLimit
}

// tryAllow attempts one admission. exhausted reports that the caller's own
// share of the *daily* budget is spent — the caller must not retry, unlike a
// merely empty token bucket. A privileged caller (allowReserved) may also
// spend the slots a prior Reserve call held back; an ordinary caller may not,
// so it never gets the chance to spend the last one out from under a
// privileged caller that has not had its turn yet.
func (l *Limiter) tryAllow(allowReserved bool) (ok bool, exhausted bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.resetIfNewDayLocked(now)

	limit := l.dailyLimit
	if !allowReserved {
		limit -= l.reserved
	}
	if l.dailyCount >= limit {
		return false, true
	}

	// Token bucket refill
	elapsed := now.Sub(l.lastRefill).Seconds()
	l.tokens += elapsed * l.refillRate
	if l.tokens > l.maxTokens {
		l.tokens = l.maxTokens
	}
	l.lastRefill = now

	if l.tokens >= 1.0 {
		l.tokens -= 1.0
		l.dailyCount++
		l.save()
		return true, false
	}

	return false, false
}

// Allow admits one request without waiting. Prefer Wait for anything whose data
// is worth a short delay; Allow discards it instead.
func (l *Limiter) Allow() bool {
	ok, _ := l.tryAllow(false)
	return ok
}

// Wait blocks until the per-minute bucket admits one request. It returns an
// error when ctx expires, when the daily budget is exhausted, or when the wait
// exceeds maxLimiterWait — never blocking indefinitely.
func (l *Limiter) Wait(ctx context.Context) error {
	return l.waitFor(ctx, false)
}

// WaitReserved is Wait, but may also spend the slots a prior Reserve call held
// back. It exists for a single caller: the AlphaVantage earnings calendar
// (avcalendar.go), whose one bulk request must not lose the daily-budget race
// to a burst of per-ticker news calls scheduled in the same run. On
// 2026-09-24 every one of 8 runs spent the AlphaVantage key's full 25-request
// budget on per-ticker news before the calendar's turn ever came, so all 8
// shipped with no verified earnings calendar at all.
func (l *Limiter) WaitReserved(ctx context.Context) error {
	return l.waitFor(ctx, true)
}

func (l *Limiter) waitFor(ctx context.Context, allowReserved bool) error {
	deadline := time.Now().Add(maxLimiterWait)
	for {
		ok, exhausted := l.tryAllow(allowReserved)
		if ok {
			return nil
		}
		if exhausted {
			return fmt.Errorf("daily budget of %d requests is exhausted", l.dailyLimit)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("rate limiter saturated after %s", maxLimiterWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(l.pollInterval()):
		}
	}
}

// pollInterval is how long to sleep before re-checking the bucket: a fraction of
// one token's refill time, floored so a fast tier does not spin.
func (l *Limiter) pollInterval() time.Duration {
	l.mu.Lock()
	rate := l.refillRate
	l.mu.Unlock()
	if rate <= 0 {
		return 100 * time.Millisecond
	}
	d := time.Duration(float64(time.Second) / rate / 4)
	if d < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	if d > 5*time.Second {
		return 5 * time.Second
	}
	return d
}
