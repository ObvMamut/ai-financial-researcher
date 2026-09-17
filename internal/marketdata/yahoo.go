package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// YahooClient fetches daily OHLCV history from Yahoo Finance's keyless chart
// endpoint. This is market data, not model access — the no-API-key rule in
// CLAUDE.md constrains model providers only.
//
// Operational notes: the endpoint 403s Go's default User-Agent, so a browser
// UA is mandatory; index symbols like ^GSPC must be path-escaped; holiday rows
// come back as nulls and are skipped; adjusted closes are preferred and OHLC
// is rescaled onto them so gap/range volatility stays consistent.
type YahooClient struct {
	client  *http.Client
	baseURL string // overridable in tests
	limiter *Limiter
	cache   *Cache
	// ttl is how long a cached series may be served before a refetch. The cache
	// was scoped to the UTC calendar day and nothing finer, so an entry written
	// at 04:00 answered every read until midnight; a Saturday run priced 8 of 12
	// names off Thursday closes.
	ttl time.Duration
}

// DefaultPriceTTL is how long a cached daily series is served before refetching.
// Short enough that a run started after the close sees the close.
const DefaultPriceTTL = 4 * time.Hour

func NewYahooClient(cache *Cache) *YahooClient {
	base := "https://query1.finance.yahoo.com"
	rerouted := false
	// CFR_YAHOO_BASE reroutes chart requests (tests, proxies/mirrors).
	if v := os.Getenv("CFR_YAHOO_BASE"); v != "" {
		base, rerouted = v, true
	}
	// ~4 req/s sustained, burst 5. The universe-wide pre-screen makes this the
	// pacing constraint of a cold run (~280 symbols ≈ 70s), which is the price
	// of not hammering a keyless public endpoint.
	//
	// A rerouted base is by definition not Yahoo — it is a local fixture server
	// or a mirror the operator chose — so there is nothing to be polite to and
	// the throttle only makes the hermetic tests take a minute per run.
	limiter := NewLimiter(2000, 240, 5)
	if rerouted {
		limiter = NewLimiter(math.MaxInt32, 1e6, 1e6)
	}
	return &YahooClient{
		client:  &http.Client{Timeout: 20 * time.Second},
		baseURL: base,
		limiter: limiter,
		cache:   cache,
		ttl:     DefaultPriceTTL,
	}
}

// SetPriceTTL overrides how long a cached series is served (config price_ttl).
func (y *YahooClient) SetPriceTTL(d time.Duration) {
	if d > 0 {
		y.ttl = d
	}
}

const yahooRange = "2y" // 12-1 momentum needs 252+21 bars; 2y covers it with slack

type yahooChartResp struct {
	Chart struct {
		Result []struct {
			Timestamp  []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Open   []float64 `json:"open"`
					High   []float64 `json:"high"`
					Low    []float64 `json:"low"`
					Close  []float64 `json:"close"`
					Volume []float64 `json:"volume"`
				} `json:"quote"`
				Adjclose []struct {
					Adjclose []float64 `json:"adjclose"`
				} `json:"adjclose"`
			} `json:"indicators"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

// History returns up to 2 years of daily bars for symbol, served from cache
// while the entry is younger than the client's TTL.
func (y *YahooClient) History(ctx context.Context, symbol string) (*quant.Series, error) {
	return y.history(ctx, symbol, y.ttl)
}

// HistoryFresh refetches symbol unconditionally, bypassing the cache. Stage 1.5
// uses it for the one retry on a name whose last bar trails the rest of the
// shortlist, which is how a genuinely stale cache entry gets corrected rather
// than merely flagged.
func (y *YahooClient) HistoryFresh(ctx context.Context, symbol string) (*quant.Series, error) {
	return y.history(ctx, symbol, 0)
}

func (y *YahooClient) history(ctx context.Context, symbol string, ttl time.Duration) (*quant.Series, error) {
	var cached quant.Series
	if y.cache != nil && ttl > 0 {
		if found, _ := y.cache.GetTTL(y.baseURL, "yahoo", "chart"+yahooRange, symbol, ttl, &cached); found && len(cached.Bars) > 0 {
			return &cached, nil
		}
	}

	if err := y.wait(ctx); err != nil {
		return nil, err
	}

	u := fmt.Sprintf("%s/v8/finance/chart/%s?range=%s&interval=1d&includeAdjustedClose=true",
		y.baseURL, url.PathEscape(yahooSymbol(symbol)), yahooRange)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")

	resp, err := y.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yahoo %s: %w", symbol, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("yahoo %s: HTTP %d: %s", symbol, resp.StatusCode, string(body))
	}

	var parsed yahooChartResp
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("yahoo %s: decode: %w", symbol, err)
	}
	if parsed.Chart.Error != nil {
		return nil, fmt.Errorf("yahoo %s: %s: %s", symbol, parsed.Chart.Error.Code, parsed.Chart.Error.Description)
	}
	if len(parsed.Chart.Result) == 0 || len(parsed.Chart.Result[0].Indicators.Quote) == 0 {
		return nil, fmt.Errorf("yahoo %s: empty chart result", symbol)
	}

	res := parsed.Chart.Result[0]
	q := res.Indicators.Quote[0]
	var adj []float64
	if len(res.Indicators.Adjclose) > 0 {
		adj = res.Indicators.Adjclose[0].Adjclose
	}

	series := &quant.Series{Symbol: symbol}
	for i, ts := range res.Timestamp {
		if i >= len(q.Close) {
			break
		}
		o, h, l, c := at(q.Open, i), at(q.High, i), at(q.Low, i), at(q.Close, i)
		vol := at(q.Volume, i)
		if c <= 0 || o <= 0 || h <= 0 || l <= 0 {
			continue // holiday/partial rows arrive as JSON nulls → zeros
		}
		// Rescale OHLC onto the adjusted close so splits/dividends don't show
		// up as fake gaps in the volatility estimators. Volume is rescaled by
		// the same factor (inversely) so Close×Volume still reconstructs the
		// real dollars traded that day — otherwise an adjusted price times an
		// unadjusted share count corrupts AvgDollarVol20 across a split.
		if a := at(adj, i); a > 0 {
			f := a / c
			o, h, l, c = o*f, h*f, l*f, a
			vol /= f
		}
		series.Bars = append(series.Bars, quant.Bar{
			Date:   time.Unix(ts, 0).UTC().Format("2006-01-02"),
			Open:   o,
			High:   h,
			Low:    l,
			Close:  c,
			Volume: vol,
		})
	}
	if len(series.Bars) == 0 {
		return nil, fmt.Errorf("yahoo %s: no usable bars", symbol)
	}
	series.Sort()

	// A response that trails what we already hold is a regression upstream, not
	// news, and must not be written over the better series.
	//
	// On 2026-09-05 the cache held BAYN.DE through Friday 2026-09-04, fetched
	// that evening. Saturday morning's pre-screen asked Yahoo for all 47 eu50
	// constituents and every one came back ending 2026-09-03; the cache was
	// rewritten with the shorter series, the forced refetch a minute later got
	// 2026-09-03 again, and the run then priced STLAM.MI off a superseded close.
	// The risk gate caught it and asked the Chief to re-price against the newest
	// bar in the quant block — which no longer existed — so the only available
	// answer was to delete the idea. The run shipped two instead of three.
	//
	// Keeping the newer series wholesale rather than unioning the bars is
	// deliberate: closes here are rescaled onto the adjusted close, so a split
	// between two fetches puts the two series on different adjustment bases and
	// splicing them would manufacture a gap. The cache entry keeps its original
	// timestamp when we decline to write, so the next call re-runs this
	// comparison and Yahoo wins the moment it catches up.
	if y.cache != nil {
		if kept, ok := y.keepNewerCached(symbol, series); ok {
			return kept, nil
		}
		_ = y.cache.SetTTL(y.baseURL, "yahoo", "chart"+yahooRange, symbol, series)
	}
	return series, nil
}

// priceCacheLookbackDays is how far back keepNewerCached looks for a better
// series. Cache keys carry the UTC calendar date, so Friday's entry and
// Saturday's are different files and today's key alone cannot see the one that
// matters: the 2026-09-05 regression was Saturday's fetch against Friday
// evening's cache. Four days spans a weekend plus a public holiday, which is the
// longest a market can be shut with the previous session still the live one.
const priceCacheLookbackDays = 4

// keepNewerCached reports the best series already cached in the last few days,
// when it ends on a later session than the one just fetched. It applies on both
// paths on purpose — a forced refetch is exactly when a regressing upstream does
// the most damage, because the caller asked for it precisely because the bar
// looked old.
//
// Every day in the window is read and the newest series wins, rather than
// stopping at the most recently written entry: within one run the first fetch
// may already have written today's regressed series, and comparing against that
// would compare the bad data with itself.
func (y *YahooClient) keepNewerCached(symbol string, fetched *quant.Series) (*quant.Series, bool) {
	var best *quant.Series
	for d := 0; d <= priceCacheLookbackDays; d++ { // 0 is today
		var cached quant.Series
		if _, ok := y.cache.PeekTTL(d, y.baseURL, "yahoo", "chart"+yahooRange, symbol, &cached); !ok {
			continue
		}
		if len(cached.Bars) == 0 {
			continue
		}
		if best == nil || cached.AsOf() > best.AsOf() {
			c := cached
			best = &c
		}
	}
	if best == nil || best.AsOf() <= fetched.AsOf() {
		return nil, false
	}
	return best, true
}

// LastClose returns the most recent daily close (cached like History).
func (y *YahooClient) LastClose(ctx context.Context, symbol string) (float64, string, error) {
	s, err := y.History(ctx, symbol)
	if err != nil {
		return 0, "", err
	}
	return s.LastClose(), s.AsOf(), nil
}

// Prefetch satisfies PriceSource. Yahoo's chart endpoint is one symbol per
// request with no batch form, so there is nothing to warm ahead of the
// per-ticker loop and this is deliberately a no-op rather than a hidden N-call
// fan-out that would look cheap at the call site.
func (y *YahooClient) Prefetch(context.Context, []string) int { return 0 }

// wait blocks until the rate limiter admits one request or ctx expires.
func (y *YahooClient) wait(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)
	for !y.limiter.Allow() {
		if time.Now().After(deadline) {
			return fmt.Errorf("yahoo: rate limiter saturated")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

// yahooSymbol spells a ticker the way the chart endpoint wants it. Yahoo writes
// US class shares with a hyphen — BRK.B is BRK-B — and 404s the dotted form,
// which is what every other source and the universe CSVs use. A dot followed by
// a known exchange suffix is a foreign listing and is left alone.
//
// One symbol in today's universe needs this (Berkshire), and every run silently
// lost it until the universe-wide pre-screen made the 404 visible.
func yahooSymbol(symbol string) string {
	i := strings.LastIndex(symbol, ".")
	if i <= 0 || IsForeignSuffix(symbol[i+1:]) {
		return symbol
	}
	return symbol[:i] + "-" + symbol[i+1:]
}

func at(xs []float64, i int) float64 {
	if i < 0 || i >= len(xs) {
		return 0
	}
	return xs[i]
}
