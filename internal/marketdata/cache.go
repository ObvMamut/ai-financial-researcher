package marketdata

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Cache struct {
	baseDir string
}

func NewCache(baseDir string) *Cache {
	return &Cache{baseDir: baseDir}
}

// key derives the on-disk filename for a cache entry. source identifies the
// endpoint the data came from (e.g. a Yahoo base URL) and is part of the hash so
// an entry written by a client pointed at one server is invisible to a client
// pointed at another — otherwise a fixture/mirror run (CFR_YAHOO_BASE) would
// poison real runs for the rest of the UTC day.
func (c *Cache) key(source, provider, fn, ticker string) string {
	date := time.Now().Format("2006-01-02")
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s|%s|%s|%s|%s", source, provider, fn, ticker, date)))
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
