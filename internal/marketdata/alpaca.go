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

const (
	// alpacaLookbackDays matches yahooRange ("2y"): 12-1 momentum needs 252+21
	// bars and this covers it with slack. It is part of the cache key, so
	// changing it invalidates the stored series rather than serving a short one.
	alpacaLookbackDays = 730
	alpacaCacheFn      = "bars1D730d"

	// alpacaMaxSymbols is how many symbols go in one request. Alpaca documents
	// no cap for bars, but a chunk is also the blast radius of one failure: a
	// refused chunk costs 100 names, not the whole universe.
	alpacaMaxSymbols = 100

	// alpacaBarLimit is the per-response bar cap (Alpaca's maximum). Two years
	// of daily bars is ~504 per name, so a page carries roughly 19 names and a
	// page boundary lands *inside* a symbol — see fetchBars.
	alpacaBarLimit = 10000

	// alpacaSIPDelay is how far back `end` must sit for the free tier to serve
	// the SIP feed. Alpaca's rule is 15 minutes; the extra minute absorbs clock
	// skew between this machine and theirs.
	alpacaSIPDelay = 16 * time.Minute
)

// AlpacaPrices fetches daily OHLCV history from Alpaca's market-data API.
//
// It exists because Yahoo's chart endpoint began answering HTTP 429 to this
// host on every request, which stopped the pre-screen pricing a single name and
// so stopped the pipeline outright. Alpaca is US-only, so it covers 155 of the
// 272 universe constituents and Yahoo remains the source for every foreign
// listing, for index benchmarks and for FX — see RoutedPrices.
//
// The other half of the reason is shape: Yahoo is one request per ticker, and a
// universe-wide pre-screen therefore fires ~272 of them per run. Alpaca serves
// many symbols per request, so the US half becomes ~9 round trips, which is also
// the most plausible way back under Yahoo's limit for the names that have no
// alternative.
//
// This is market data, not model access — the no-API-key rule in CLAUDE.md
// constrains model providers only.
type AlpacaPrices struct {
	client  *http.Client
	keyID   string
	secret  string
	baseURL string
	limiter *Limiter
	cache   *Cache
	ttl     time.Duration
}

func NewAlpacaPrices(keyID, secret string, cache *Cache) *AlpacaPrices {
	base := "https://data.alpaca.markets"
	rerouted := false
	// CFR_ALPACA_BASE reroutes requests (tests, proxies), matching
	// CFR_YAHOO_BASE and CFR_AV_BASE.
	if v := os.Getenv("CFR_ALPACA_BASE"); v != "" {
		base, rerouted = v, true
	}
	// The free tier allows 200 requests a minute; 180 leaves headroom for the
	// news provider, which draws on the same account-wide budget. There is no
	// daily cap to model, unlike AlphaVantage.
	limiter := NewLimiter(math.MaxInt32, 180, 10)
	if rerouted {
		limiter = NewLimiter(math.MaxInt32, 1e6, 1e6)
	}
	return &AlpacaPrices{
		client:  &http.Client{Timeout: 30 * time.Second},
		keyID:   keyID,
		secret:  secret,
		baseURL: base,
		limiter: limiter,
		cache:   cache,
		ttl:     DefaultPriceTTL,
	}
}

// Available reports whether both halves of the credential are present. A key
// that is not configured is a choice, not a failure: RoutedPrices simply uses
// Yahoo for everything.
func (a *AlpacaPrices) Available() bool { return a.keyID != "" && a.secret != "" }

// SetPriceTTL overrides how long a cached series is served (config price_ttl).
func (a *AlpacaPrices) SetPriceTTL(d time.Duration) {
	if d > 0 {
		a.ttl = d
	}
}

// alpacaWireBar is one daily bar. Alpaca spells the fields with single letters.
type alpacaWireBar struct {
	Time   string  `json:"t"`
	Open   float64 `json:"o"`
	High   float64 `json:"h"`
	Low    float64 `json:"l"`
	Close  float64 `json:"c"`
	Volume float64 `json:"v"`
}

type alpacaBarsResp struct {
	Bars          map[string][]alpacaWireBar `json:"bars"`
	NextPageToken string                     `json:"next_page_token"`
}

// History returns up to two years of daily bars, served from cache while the
// entry is younger than the client's TTL.
func (a *AlpacaPrices) History(ctx context.Context, symbol string) (*quant.Series, error) {
	return a.history(ctx, symbol, a.ttl)
}

// HistoryFresh refetches unconditionally, for Stage 1.5's stale-name retry.
func (a *AlpacaPrices) HistoryFresh(ctx context.Context, symbol string) (*quant.Series, error) {
	return a.history(ctx, symbol, 0)
}

func (a *AlpacaPrices) history(ctx context.Context, symbol string, ttl time.Duration) (*quant.Series, error) {
	var cached quant.Series
	if a.cache != nil && ttl > 0 {
		if found, _ := a.cache.GetTTL(a.baseURL, "alpaca", alpacaCacheFn, symbol, ttl, &cached); found && len(cached.Bars) > 0 {
			return &cached, nil
		}
	}
	out, err := a.fetchBars(ctx, []string{symbol})
	if err != nil {
		return nil, err
	}
	s, ok := out[symbol]
	if !ok || len(s.Bars) == 0 {
		// Not an empty series: the caller cannot tell one of those from a name
		// that genuinely has no history, and quant.Compute on zero bars is a
		// row of zeros that reads like a real measurement.
		return nil, fmt.Errorf("alpaca %s: no bars returned", symbol)
	}
	a.store(symbol, s)
	return s, nil
}

func (a *AlpacaPrices) LastClose(ctx context.Context, symbol string) (float64, string, error) {
	s, err := a.History(ctx, symbol)
	if err != nil {
		return 0, "", err
	}
	return s.LastClose(), s.AsOf(), nil
}

// Prefetch fetches many symbols in as few requests as the bar limit allows and
// writes each series into the shared cache, so a following per-ticker loop —
// runPrescreen's, unchanged — is served entirely from disk.
//
// Best-effort by contract: a failed chunk costs that chunk, and every symbol in
// it is still individually fetchable afterwards, so the count returned is how
// many series were actually cached rather than an error.
func (a *AlpacaPrices) Prefetch(ctx context.Context, symbols []string) int {
	cached := 0
	for start := 0; start < len(symbols); start += alpacaMaxSymbols {
		end := start + alpacaMaxSymbols
		if end > len(symbols) {
			end = len(symbols)
		}
		out, err := a.fetchBars(ctx, symbols[start:end])
		if err != nil {
			continue
		}
		for sym, s := range out {
			if len(s.Bars) == 0 {
				continue
			}
			a.store(sym, s)
			cached++
		}
	}
	return cached
}

func (a *AlpacaPrices) store(symbol string, s *quant.Series) {
	if a.cache != nil {
		_ = a.cache.SetTTL(a.baseURL, "alpaca", alpacaCacheFn, symbol, s)
	}
}

// fetchBars requests one chunk of symbols and follows next_page_token to the
// end.
//
// Paging is not optional here. `limit` counts bars rather than symbols and the
// response is ordered by symbol then timestamp, so a single name's history
// routinely straddles a page boundary. Stopping at the first page returns a
// truncated series, and a truncated series is a *wrong* momentum number rather
// than an obviously missing one — so bars are accumulated across every page and
// only turned into a Series once the token is empty.
func (a *AlpacaPrices) fetchBars(ctx context.Context, symbols []string) (map[string]*quant.Series, error) {
	acc := map[string][]alpacaWireBar{}
	token := ""
	for {
		parsed, err := a.getPage(ctx, symbols, token)
		if err != nil {
			return nil, err
		}
		for sym, bars := range parsed.Bars {
			acc[sym] = append(acc[sym], bars...)
		}
		if parsed.NextPageToken == "" {
			break
		}
		token = parsed.NextPageToken
	}

	out := make(map[string]*quant.Series, len(acc))
	for sym, bars := range acc {
		s := &quant.Series{Symbol: sym}
		for _, b := range bars {
			// A partial or holiday row is not a bar; the same guard yahoo.go
			// applies to its JSON nulls.
			if len(b.Time) < 10 || b.Close <= 0 || b.Open <= 0 || b.High <= 0 || b.Low <= 0 {
				continue
			}
			s.Bars = append(s.Bars, quant.Bar{
				Date:   b.Time[:10],
				Open:   b.Open,
				High:   b.High,
				Low:    b.Low,
				Close:  b.Close,
				Volume: b.Volume,
			})
		}
		s.Sort()
		out[sym] = s
	}
	return out, nil
}

func (a *AlpacaPrices) getPage(ctx context.Context, symbols []string, token string) (alpacaBarsResp, error) {
	var out alpacaBarsResp
	if err := a.wait(ctx); err != nil {
		return out, err
	}

	now := time.Now().UTC()
	q := url.Values{}
	// Alpaca uses the dotted form for US class shares (BRK.B), where Yahoo
	// wants BRK-B and 404s the dot. So the symbol goes over the wire as the
	// universe files spell it — deliberately *not* through yahooSymbol.
	q.Set("symbols", strings.Join(symbols, ","))
	q.Set("timeframe", "1Day")
	q.Set("start", now.AddDate(0, 0, -alpacaLookbackDays).Format("2006-01-02"))
	// The end bound is what makes the SIP feed legal on the free tier, which
	// requires the window to close at least 15 minutes ago. Without it the
	// request is refused and — see below — refusal is the good outcome.
	q.Set("end", now.Add(-alpacaSIPDelay).Format(time.RFC3339))
	// Split- and dividend-adjusted, so returns are total returns and a split
	// does not read as a 50% gap to the volatility estimators. Note this is not
	// quite yahoo.go's convention: that one rescales volume by the same factor
	// so Close x Volume reconstructs the real dollars traded, whereas Alpaca
	// split-adjusts volume but does not dividend-adjust it. AvgDollarVol20USD
	// therefore runs low by roughly the trailing dividend yield (~0.5-2%/yr) on
	// an Alpaca-served name relative to a Yahoo-served one — immaterial against
	// a $25M-a-day floor, but it is a real difference in what the number means.
	q.Set("adjustment", "all")
	// Explicit, never defaulted. The free tier resolves an absent feed to "the
	// best available for your subscription", which is IEX.
	//
	// Both feeds were measured against this account on 2026-09-03, over the 119
	// US universe names, 20 sessions: IEX returned HTTP 200 and a full set of
	// bars whose closes match SIP to a few cents (AAPL 325.03 against 324.96)
	// and whose dollar volume is a median 24.3x too small, ranging 11.8x to
	// 49.1x. Thirteen of the 119 then fall under the $20M ADV floor and are
	// dropped from the pre-screen as illiquid, with no error anywhere in the
	// run — and these are mega-caps. The proportion only worsens further down
	// the cap scale, and the spread across that 11.8x-49.1x range means the
	// error is not even a uniform rescaling: it reorders names by liquidity.
	q.Set("feed", "sip")
	q.Set("limit", fmt.Sprint(alpacaBarLimit))
	q.Set("sort", "asc")
	if token != "" {
		q.Set("page_token", token)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/v2/stocks/bars?"+q.Encode(), nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("APCA-API-KEY-ID", a.keyID)
	req.Header.Set("APCA-API-SECRET-KEY", a.secret)
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return out, fmt.Errorf("alpaca bars: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body := strings.TrimSpace(string(mustReadLimited(resp.Body, 512)))
		if isAlpacaSIPRefusal(resp.StatusCode, body) {
			// Deliberately fatal for this fetch. The tempting recovery — retry
			// on feed=iex — is the one thing that must never happen: it would
			// return HTTP 200 and a complete set of plausible bars, and the run
			// would price every US name off IEX's slice of its real volume
			// without a single error to show for it.
			return out, fmt.Errorf("alpaca bars: the SIP feed was refused (HTTP %d: %s) — not retrying on feed=iex, which would succeed and understate every US name's average dollar volume by a measured median of 24x (11.8x-49.1x across the universe), dropping names under the liquidity floor as if they had quietly stopped trading",
				resp.StatusCode, body)
		}
		return out, fmt.Errorf("alpaca bars: HTTP %d: %s", resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("alpaca bars: decode: %w", err)
	}
	return out, nil
}

// isAlpacaSIPRefusal distinguishes "your plan may not read this feed" from any
// other rejection. Alpaca answers a subscription problem with 403; the body
// check keeps a future 422 from being read as an ordinary bad request.
func isAlpacaSIPRefusal(status int, body string) bool {
	if status == http.StatusForbidden {
		return true
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "subscription") || strings.Contains(lower, "sip data")
}

func mustReadLimited(r io.Reader, n int64) []byte {
	b, _ := io.ReadAll(io.LimitReader(r, n))
	return b
}

// wait blocks until the rate limiter admits one request or ctx expires.
func (a *AlpacaPrices) wait(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)
	for !a.limiter.Allow() {
		if time.Now().After(deadline) {
			return fmt.Errorf("alpaca: rate limiter saturated")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}
