package marketdata

import (
	"context"
	"sync/atomic"
	"testing"
)

// A cache entry written by a client pointed at one server (e.g. a test fixture
// via CFR_YAHOO_BASE) must be invisible to a client pointed at another server,
// or fixture data poisons real runs for the rest of the day.
func TestCacheScopedBySource(t *testing.T) {
	dir := t.TempDir()
	var hitsA, hitsB atomic.Int64
	srvA := newFixtureServer(t, &hitsA)
	defer srvA.Close()
	srvB := newFixtureServer(t, &hitsB)
	defer srvB.Close()

	a := NewYahooClient(NewCache(dir))
	a.baseURL = srvA.URL
	b := NewYahooClient(NewCache(dir))
	b.baseURL = srvB.URL

	if _, err := a.History(context.Background(), "TEST"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.History(context.Background(), "TEST"); err != nil {
		t.Fatal(err)
	}
	if got := hitsB.Load(); got != 1 {
		t.Errorf("client B server hits = %d, want 1 (B must not read A's cache entries)", got)
	}

	// Same-source caching still works: a second read via A stays served from cache.
	if _, err := a.History(context.Background(), "TEST"); err != nil {
		t.Fatal(err)
	}
	if got := hitsA.Load(); got != 1 {
		t.Errorf("client A server hits = %d, want 1 (second call must come from cache)", got)
	}
}
