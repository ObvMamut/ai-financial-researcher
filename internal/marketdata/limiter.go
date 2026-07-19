package marketdata

import (
	"sync"
	"time"
)

// Limiter manages rate limits and daily budgets.
type Limiter struct {
	mu           sync.Mutex
	lastReset    time.Time
	dailyCount   int
	dailyLimit   int
	tokens       float64
	maxTokens    float64
	refillRate   float64 // tokens per second
	lastRefill   time.Time
}

func NewLimiter(dailyLimit int, rpm float64) *Limiter {
	return &Limiter{
		dailyLimit: dailyLimit,
		maxTokens:  5, // typical for free tiers
		tokens:     5,
		refillRate: rpm / 60.0,
		lastRefill: time.Now(),
		lastReset:  time.Now(),
	}
}

func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	
	// Reset daily count if it's a new day
	if now.YearDay() != l.lastReset.YearDay() || now.Year() != l.lastReset.Year() {
		l.dailyCount = 0
		l.lastReset = now
	}

	if l.dailyCount >= l.dailyLimit {
		return false
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
		return true
	}

	return false
}
