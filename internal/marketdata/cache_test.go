package marketdata

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
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

// The key hashed source, provider, domain, ticker and the date — nothing about
// the *shape* of what was stored. So on 2026-09-01 an entry written earlier the
// same day by a binary that predated the computed positioning legs was served
// back without them, the sentiment guardrail failed open on the absent verdict,
// and the domain scored exactly the three tickers it exists to silence. A
// payload-shape version in the key makes that a miss instead of a silent
// downgrade.
func TestCacheScopedByFactSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	old := NewCache(dir)
	old.schema = factSchemaVersion - 1
	if err := old.Set("src", "prov", "sentiment", "ORCL", "pre-schema payload"); err != nil {
		t.Fatal(err)
	}

	var got string
	if found, _ := NewCache(dir).Get("src", "prov", "sentiment", "ORCL", &got); found {
		t.Errorf("an entry written under schema v%d was served to schema v%d as %q",
			old.schema, factSchemaVersion, got)
	}
	// The entry is still readable by a client on its own schema, so the version
	// scopes the cache rather than corrupting it.
	if found, _ := old.Get("src", "prov", "sentiment", "ORCL", &got); !found || got != "pre-schema payload" {
		t.Errorf("same-schema Get = %q/%v, want the stored value", got, found)
	}
}

// The cache had no TTL at all: an entry written at 04:00 served every read for
// the rest of the UTC day. On 2026-08-29 that shipped precise entry prices off
// Thursday closes in a Saturday run, for 8 of 12 names, with "re-price before
// entering" in a note as the only mitigation.
func TestCacheTTLExpiresStaleEntries(t *testing.T) {
	c := NewCache(t.TempDir())
	if err := c.SetTTL("src", "prov", "fn", "TEST", "payload"); err != nil {
		t.Fatalf("SetTTL: %v", err)
	}

	var got string
	if found, _ := c.GetTTL("src", "prov", "fn", "TEST", time.Hour, &got); !found {
		t.Fatal("a fresh entry must be a hit")
	}
	if got != "payload" {
		t.Errorf("payload = %q, want the stored value", got)
	}

	// A zero TTL means "never serve from cache" — the forced-refetch path.
	if found, _ := c.GetTTL("src", "prov", "fn", "TEST", 0, &got); found {
		t.Error("a zero TTL must bypass the cache entirely")
	}

	// Reach past the entry's age and it stops being a hit.
	if found, _ := c.GetTTL("src", "prov", "fn", "TEST", -time.Second, &got); found {
		t.Error("an entry older than the TTL must be a miss")
	}
}

// Entries written by the old, envelope-less Set must not be read back as
// infinitely fresh by GetTTL — they carry no timestamp to judge.
func TestCacheTTLIgnoresPreEnvelopeEntries(t *testing.T) {
	c := NewCache(t.TempDir())
	if err := c.Set("src", "prov", "fn", "TEST", "legacy"); err != nil {
		t.Fatal(err)
	}
	var got string
	if found, _ := c.GetTTL("src", "prov", "fn", "TEST", time.Hour, &got); found {
		t.Error("a legacy entry has no fetched_at and cannot be judged fresh")
	}
	// The legacy reader still finds it, so nothing already on disk breaks.
	if found, _ := c.Get("src", "prov", "fn", "TEST", &got); !found || got != "legacy" {
		t.Errorf("legacy Get = %q/%v, want the stored value", got, found)
	}
}

// Old cache files pile up one directory per day's worth of runs; nothing ever
// removed them.
func TestCachePruneRemovesOldFiles(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir)
	if err := c.SetTTL("src", "prov", "fn", "FRESH", "x"); err != nil {
		t.Fatal(err)
	}

	old := filepath.Join(dir, "stale.json")
	if err := os.WriteFile(old, []byte(`{"x":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	removed, err := c.Prune(7 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("pruned %d files, want 1", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the stale file should be gone")
	}
	var got string
	if found, _ := c.GetTTL("src", "prov", "fn", "FRESH", time.Hour, &got); !found {
		t.Error("Prune removed a fresh entry")
	}
}

// Cache.Get/Set hash today's date into the key, so an entry written today is
// already a miss tomorrow — the bug GetPermanent/SetPermanent exist to fix.
// Two Cache instances with different injected clocks stand in for two runs on
// two different days: a real day rollover produces exactly this, and nothing
// else distinguishes them.
func TestCacheGetPermanentIgnoresTheDate(t *testing.T) {
	dir := t.TempDir()
	day1 := NewCache(dir)
	day1.now = func() time.Time { return time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC) }
	if err := day1.SetPermanent("src", "prov", "reportdates", "20260901", []string{"AAA"}); err != nil {
		t.Fatalf("SetPermanent: %v", err)
	}

	day2 := NewCache(dir)
	day2.now = func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }
	var got []string
	found, err := day2.GetPermanent("src", "prov", "reportdates", "20260901", &got)
	if err != nil {
		t.Fatalf("GetPermanent: %v", err)
	}
	if !found {
		t.Fatal("an entry written on one day must still be a hit read from a later day")
	}
	if len(got) != 1 || got[0] != "AAA" {
		t.Errorf("got %v, want [AAA]", got)
	}
}

// Prune ages out date-scoped entries by file mtime; a permanent entry must
// never be touched by it, whatever its mtime, because it can never be
// legitimately stale.
func TestCachePruneExemptsPermanentEntries(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir)
	if err := c.SetPermanent("src", "prov", "reportdates", "20260901", []string{"AAA"}); err != nil {
		t.Fatalf("SetPermanent: %v", err)
	}

	entries, err := os.ReadDir(c.permanentDir())
	if err != nil || len(entries) != 1 {
		t.Fatalf("permanentDir listing = %v (err %v), want one file", entries, err)
	}
	old := filepath.Join(c.permanentDir(), entries[0].Name())
	past := time.Now().Add(-365 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Prune(7 * 24 * time.Hour); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	var got []string
	found, err := c.GetPermanent("src", "prov", "reportdates", "20260901", &got)
	if err != nil || !found {
		t.Errorf("Prune removed a permanent entry (found %v, err %v)", found, err)
	}
}
