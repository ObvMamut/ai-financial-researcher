package marketdata

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// factSchemaVersion is the shape of the payloads this binary writes. It is part
// of every cache key, so changing the fields a provider returns makes yesterday's
// — or this morning's — entries unreadable instead of invisibly wrong.
//
// It exists because the key hashed source, provider, domain, ticker and the
// calendar date and nothing about the payload. On 2026-09-01 the computed
// positioning legs (insidersignal.go) were added mid-day; entries written earlier
// by the previous binary were served back without them, and the sentiment
// guardrail — which failed open on an absent verdict — scored exactly the three
// tickers it exists to silence and abstained on the two it should have scored.
// Bump this whenever a cached payload gains, loses or changes a field.
//
// 3: the options provider stopped writing its own computed verdict. That leg is
// judged against the run's own cross-section of put/call ratios now, so a stored
// verdict is an answer to a different question, and a cached entry carrying one
// would have had it rendered into the prompt beside the recomputed one.
//
// 4: the whale legs. A sentiment TickerData now carries traded option volume,
// Form 144 notices, 13D/G ownership schedules and tracked-manager 13F positions,
// each with its own computed verdict. A cached entry written before them has
// none, and addPositioningSignal has no reconstruction path for the new labels
// by design — the raw fact and its verdict are always written together by one
// provider, which is only true as long as this bump happens.
const factSchemaVersion = 4

type Cache struct {
	baseDir string
	// schema namespaces entries by payload shape; see factSchemaVersion.
	schema int
	// now supplies the calendar date entries are scoped to. Injectable so a test
	// can cross a day boundary, which is the case that matters: keys carry the
	// UTC date, so Friday's entry and Saturday's are different files.
	now func() time.Time
}

func NewCache(baseDir string) *Cache {
	return &Cache{baseDir: baseDir, schema: factSchemaVersion, now: time.Now}
}

func (c *Cache) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// key derives the on-disk filename for a cache entry. source identifies the
// endpoint the data came from (e.g. a Yahoo base URL) and is part of the hash so
// an entry written by a client pointed at one server is invisible to a client
// pointed at another — otherwise a fixture/mirror run (CFR_YAHOO_BASE) would
// poison real runs for the rest of the UTC day.
func (c *Cache) key(source, provider, fn, ticker string) string {
	return c.keyOn(0, source, provider, fn, ticker)
}

// keyOn is key for a day daysBack before today. The date is in the hash, so
// reading an entry written on an earlier day means asking for that day's key
// explicitly — there is no way to scan for it.
func (c *Cache) keyOn(daysBack int, source, provider, fn, ticker string) string {
	date := c.clock().AddDate(0, 0, -daysBack).Format("2006-01-02")
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("v%d|%s|%s|%s|%s|%s", c.schema, source, provider, fn, ticker, date)))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (c *Cache) Get(source, provider, fn, ticker string, out interface{}) (bool, error) {
	path := filepath.Join(c.baseDir, c.key(source, provider, fn, ticker)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	err = json.Unmarshal(data, out)
	return err == nil, err
}

func (c *Cache) Set(source, provider, fn, ticker string, val interface{}) error {
	if err := os.MkdirAll(c.baseDir, 0755); err != nil {
		return err
	}
	path := filepath.Join(c.baseDir, c.key(source, provider, fn, ticker)+".json")
	data, err := json.Marshal(val)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// envelope wraps a cached payload with the time it was fetched. Entries written
// by Set carry no envelope; GetTTL treats those as misses rather than guessing
// an age, so nothing already on disk is read back as infinitely fresh.
type envelope struct {
	FetchedAt time.Time       `json:"fetched_at"`
	Payload   json.RawMessage `json:"payload"`
}

// ttlKey namespaces TTL entries away from the date-scoped ones written by Set,
// so the two encodings never collide on one path.
func (c *Cache) ttlKey(source, provider, fn, ticker string) string {
	return c.ttlKeyOn(0, source, provider, fn, ticker)
}

func (c *Cache) ttlKeyOn(daysBack int, source, provider, fn, ticker string) string {
	return c.keyOn(daysBack, source, provider, "ttl:"+fn, ticker)
}

// GetTTL reads an entry only while it is younger than ttl.
//
// Cache.Get is scoped to the UTC calendar day and nothing more, so an entry
// written at 04:00 served every read until midnight. In a Saturday run that
// meant precise entry prices computed off Thursday closes for 8 of 12 names —
// the idea shipped with "re-price before entering" in a note as the only
// mitigation. A ttl of zero or less is always a miss, which is how a caller
// forces a refetch.
func (c *Cache) GetTTL(source, provider, fn, ticker string, ttl time.Duration, out interface{}) (bool, error) {
	if ttl <= 0 {
		return false, nil
	}
	path := filepath.Join(c.baseDir, c.ttlKey(source, provider, fn, ticker)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return false, err
	}
	if env.FetchedAt.IsZero() || time.Since(env.FetchedAt) > ttl {
		return false, nil
	}
	if err := json.Unmarshal(env.Payload, out); err != nil {
		return false, err
	}
	return true, nil
}

// PeekTTL reads a TTL entry whatever its age, reporting when it was written.
//
// It exists so a caller can compare what it just fetched against what it
// already had. GetTTL cannot answer that: expiry is the whole question it
// exists to decide, and a forced refetch passes ttl=0, which it treats as an
// unconditional miss. A stale entry is still evidence about the world, and
// discarding it before the comparison is how a good series gets overwritten by
// a worse one.
func (c *Cache) PeekTTL(daysBack int, source, provider, fn, ticker string, out interface{}) (time.Time, bool) {
	path := filepath.Join(c.baseDir, c.ttlKeyOn(daysBack, source, provider, fn, ticker)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return time.Time{}, false
	}
	if err := json.Unmarshal(env.Payload, out); err != nil {
		return time.Time{}, false
	}
	return env.FetchedAt, true
}

// SetTTL writes an entry stamped with the current time, for reading via GetTTL.
func (c *Cache) SetTTL(source, provider, fn, ticker string, val interface{}) error {
	if err := os.MkdirAll(c.baseDir, 0755); err != nil {
		return err
	}
	payload, err := json.Marshal(val)
	if err != nil {
		return err
	}
	data, err := json.Marshal(envelope{FetchedAt: time.Now(), Payload: payload})
	if err != nil {
		return err
	}
	path := filepath.Join(c.baseDir, c.ttlKey(source, provider, fn, ticker)+".json")
	return os.WriteFile(path, data, 0644)
}

// Prune deletes cache files not modified within maxAge and reports how many
// went. Keys are date-scoped, so yesterday's entries can never be read again —
// but nothing removed them, and a universe-wide pre-screen writes a few hundred
// files a day. Returns the count removed; a maxAge of zero or less prunes
// nothing.
func (c *Cache) Prune(maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	entries, err := os.ReadDir(c.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(c.baseDir, e.Name())) == nil {
			removed++
		}
	}
	return removed, nil
}
