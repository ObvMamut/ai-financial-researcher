package marketdata

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// EarningsFactLabel is the label of the fact carrying a verified next-earnings
// date. BuildPack keys the typed DataPack.EventDates off it, so the number the
// model reads and the date the risk checks use are the same string.
const EarningsFactLabel = "Next earnings"

// earningsCalendarHorizon is what we ask AlphaVantage for. Three months is the
// widest the endpoint offers and costs exactly the same one request as the
// narrowest, so there is no reason to ask for less.
const earningsCalendarHorizon = "3month"

// earningsCalendar is the shared next-earnings lookup behind the AlphaVantage
// provider.
//
// EARNINGS_CALENDAR returns one CSV covering every symbol it knows, so the whole
// shortlist costs a single request out of the free tier's 25 per day — a
// per-ticker endpoint would have spent half the budget on dates alone. The
// result is memoized in-process and cached on disk under a date-scoped key, so a
// second run the same day pays nothing.
//
// An unresolved binary event inside the holding window is the single largest
// uncontrolled risk in a swing trade, and it was the one fact the pipeline never
// had: the news persona asked the model for earnings dates, and on a search-less
// engine the model supplied them from memory.
type earningsCalendar struct {
	client  *http.Client
	apiKey  string
	baseURL string
	limiter *Limiter
	cache   *Cache

	mu       sync.Mutex
	loaded   bool
	err      error
	bySymbol map[string][]time.Time // report dates, ascending
}

// next returns the soonest scheduled report date for ticker that is not in the
// past, reaching a foreign listing through its US line. The bool is false when
// the calendar simply does not cover the name — not every symbol reports inside
// the horizon, and that is not an error.
func (c *earningsCalendar) next(ctx context.Context, ticker string) (time.Time, bool, error) {
	symbol, ok := providerSymbol(ticker)
	if !ok {
		return time.Time{}, false, nil
	}
	if err := c.load(ctx); err != nil {
		return time.Time{}, false, err
	}

	c.mu.Lock()
	dates := c.bySymbol[strings.ToUpper(avSymbol(symbol))]
	c.mu.Unlock()

	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, d := range dates {
		if !d.Before(today) {
			return d, true, nil
		}
	}
	return time.Time{}, false, nil
}

// load fetches and parses the bulk CSV at most once per process, and at most
// once per UTC day across processes (the cache key is date-scoped). A failure is
// remembered too: retrying a rate-limited endpoint twelve times in one run is
// how the daily budget disappears.
func (c *earningsCalendar) load(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return c.err
	}
	c.loaded = true

	var cached map[string][]string
	if c.cache != nil {
		if found, _ := c.cache.Get(c.baseURL, "AlphaVantage", "earnings_calendar", "GLOBAL", &cached); found && len(cached) > 0 {
			c.bySymbol = parseCachedCalendar(cached)
			return nil
		}
	}

	body, err := c.fetch(ctx)
	if err != nil {
		c.err = err
		return err
	}
	bySymbol, err := parseEarningsCalendar(strings.NewReader(body))
	if err != nil {
		c.err = err
		return err
	}
	c.bySymbol = bySymbol

	if c.cache != nil && len(bySymbol) > 0 {
		flat := make(map[string][]string, len(bySymbol))
		for sym, ds := range bySymbol {
			for _, d := range ds {
				flat[sym] = append(flat[sym], d.Format("2006-01-02"))
			}
		}
		_ = c.cache.Set(c.baseURL, "AlphaVantage", "earnings_calendar", "GLOBAL", flat)
	}
	return nil
}

func (c *earningsCalendar) fetch(ctx context.Context) (string, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return "", fmt.Errorf("%w: AlphaVantage earnings calendar: %v", ErrUnavailable, err)
	}
	v := url.Values{}
	v.Set("function", "EARNINGS_CALENDAR")
	v.Set("horizon", earningsCalendarHorizon)
	v.Set("apikey", c.apiKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/query?"+v.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// The CSV is a few hundred KB; anything far beyond that is not a calendar.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	body := string(raw)
	// An exhausted quota or a bad key arrives as a 200 carrying JSON where the
	// CSV should be. Parsed as CSV it yields an empty calendar, which reads
	// exactly like "nobody reports in the next three months".
	if t := strings.TrimSpace(body); strings.HasPrefix(t, "{") {
		return "", fmt.Errorf("%w: AlphaVantage earnings calendar: %s", ErrUnavailable, firstLine(t))
	}
	return body, nil
}

// parseEarningsCalendar reads the bulk CSV into report dates per symbol,
// ascending. Rows with an unparseable date are skipped rather than failing the
// whole calendar: one malformed line should not cost every name its date.
func parseEarningsCalendar(r io.Reader) (map[string][]time.Time, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: AlphaVantage earnings calendar: %v", ErrUnavailable, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: AlphaVantage earnings calendar: empty response", ErrUnavailable)
	}

	// Locate the columns by header name; the endpoint has added fields before.
	symCol, dateCol := 0, 2
	head := rows[0]
	for i, h := range head {
		switch strings.TrimSpace(strings.ToLower(h)) {
		case "symbol":
			symCol = i
		case "reportdate":
			dateCol = i
		}
	}

	out := make(map[string][]time.Time)
	for _, row := range rows[1:] {
		if len(row) <= symCol || len(row) <= dateCol {
			continue
		}
		sym := strings.ToUpper(strings.TrimSpace(row[symCol]))
		d, err := time.Parse("2006-01-02", strings.TrimSpace(row[dateCol]))
		if sym == "" || err != nil {
			continue
		}
		out[sym] = append(out[sym], d)
	}
	for sym := range out {
		sortTimes(out[sym])
	}
	return out, nil
}

func parseCachedCalendar(flat map[string][]string) map[string][]time.Time {
	out := make(map[string][]time.Time, len(flat))
	for sym, ds := range flat {
		for _, s := range ds {
			if d, err := time.Parse("2006-01-02", s); err == nil {
				out[sym] = append(out[sym], d)
			}
		}
		sortTimes(out[sym])
	}
	return out
}

func sortTimes(ts []time.Time) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j].Before(ts[j-1]); j-- {
			ts[j], ts[j-1] = ts[j-1], ts[j]
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
