package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"time"
)

// yahooBrowserUA is required on every Yahoo endpoint: the default Go user agent
// is answered with a 403.
const yahooBrowserUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"

// optionsExpiries is how many expiries the open-interest ratio is taken over.
// The front two cover roughly the 1–4 week window this system trades; going
// further out picks up hedging and covered-call programmes that say nothing
// about positioning over the next fortnight.
const optionsExpiries = 2

// yahooOptionsProvider reads the keyless option chain for positioning evidence:
// the put/call open-interest ratio, and the at-the-money implied volatility to
// set against the realized volatility the quant stage already computed.
//
// This exists because the sentiment domain had no data source of its own — it
// was reading AlphaVantage's news sentiment, the same call the news domain made,
// so two of five nominally independent domains agreed with each other by
// construction. Open interest is a record of positions actually held.
//
// The endpoint is crumb-gated, so every request goes through yahooAuth. It was
// described here as *intermittently* gated, with a 401 treated as an expected
// degradation to insider filings alone — but the gate is unconditional now, so
// the degraded path was the only path and the provider had stopped contributing
// anything at all. See yahoocrumb.go.
type yahooOptionsProvider struct {
	auth    *yahooAuth
	baseURL string
	limiter *Limiter
}

func NewYahooOptionsProvider() Provider {
	base := "https://query1.finance.yahoo.com"
	rerouted := false
	if v := os.Getenv("CFR_YAHOO_BASE"); v != "" {
		base, rerouted = v, true
	}
	limiter := NewLimiter(2000, 240, 5)
	if rerouted {
		limiter = NewLimiter(math.MaxInt32, 1e6, 1e6)
	}
	return &yahooOptionsProvider{
		auth:    newYahooAuth(base, rerouted),
		baseURL: base,
		limiter: limiter,
	}
}

func (p *yahooOptionsProvider) Name() string      { return "Yahoo Options" }
func (p *yahooOptionsProvider) Source() string    { return p.baseURL }
func (p *yahooOptionsProvider) Domains() []string { return []string{"sentiment"} }
func (p *yahooOptionsProvider) Available() bool   { return true }
func (p *yahooOptionsProvider) MacroFetch(ctx context.Context) ([]Fact, error) {
	return nil, ErrNotApplicable
}

type yahooOptionsResp struct {
	OptionChain struct {
		Result []struct {
			UnderlyingSymbol string  `json:"underlyingSymbol"`
			ExpirationDates  []int64 `json:"expirationDates"`
			Quote            struct {
				RegularMarketPrice float64 `json:"regularMarketPrice"`
			} `json:"quote"`
			Options []struct {
				ExpirationDate int64            `json:"expirationDate"`
				Calls          []yahooOptionLeg `json:"calls"`
				Puts           []yahooOptionLeg `json:"puts"`
			} `json:"options"`
		} `json:"result"`
		Error *struct {
			Description string `json:"description"`
		} `json:"error"`
	} `json:"optionChain"`
}

type yahooOptionLeg struct {
	Strike            float64 `json:"strike"`
	OpenInterest      float64 `json:"openInterest"`
	ImpliedVolatility float64 `json:"impliedVolatility"`
}

func (p *yahooOptionsProvider) Fetch(ctx context.Context, domain string, ticker string) (TickerData, error) {
	if domain != "sentiment" {
		return TickerData{}, ErrNotApplicable
	}
	// Listed options are a US-market instrument here; a foreign primary listing
	// has no chain on this endpoint, and its US line's chain is the honest
	// substitute where one exists.
	symbol, ok := providerSymbol(ticker)
	if !ok {
		return TickerData{}, fmt.Errorf("%w: %s has no US options listing", ErrNotApplicable, ticker)
	}

	first, err := p.fetchChain(ctx, symbol, 0)
	if err != nil {
		return TickerData{}, err
	}
	if len(first.OptionChain.Result) == 0 || len(first.OptionChain.Result[0].Options) == 0 {
		return TickerData{Ticker: ticker}, nil
	}
	res := first.OptionChain.Result[0]

	var callOI, putOI float64
	expiries := 0
	accumulate := func(r yahooOptionsResp) {
		if len(r.OptionChain.Result) == 0 {
			return
		}
		for _, o := range r.OptionChain.Result[0].Options {
			for _, c := range o.Calls {
				callOI += c.OpenInterest
			}
			for _, put := range o.Puts {
				putOI += put.OpenInterest
			}
			expiries++
		}
	}
	accumulate(first)

	// The bare call returns only the front chain; the next expiry needs its own
	// request keyed by epoch.
	if len(res.ExpirationDates) > 1 && optionsExpiries > 1 {
		if second, err := p.fetchChain(ctx, symbol, res.ExpirationDates[1]); err == nil {
			accumulate(second)
		}
	}

	spot := res.Quote.RegularMarketPrice
	td := TickerData{Ticker: ticker}
	link := fmt.Sprintf("https://finance.yahoo.com/quote/%s/options", url.PathEscape(symbol))
	note := ""
	if symbol != ticker {
		note = fmt.Sprintf(" (US line: %s)", symbol)
	}

	if callOI > 0 {
		ratio := putOI / callOI
		td.Facts = append(td.Facts, Fact{
			Label: OptionsPositioningLabel,
			Value: fmt.Sprintf("put/call open interest %.2f (%.0f puts vs %.0f calls) over the front %d %s%s",
				ratio, putOI, callOI, expiries, plural(expiries, "expiry", "expiries"), note),
			AsOf:   time.Now(),
			Source: "Yahoo Finance options",
			URL:    link,
		})
		// Which side the ratio actually favours, decided here. The agent read a
		// crowded put side as bearish and a crowded call side as "squeeze risk",
		// also bearish — so the same metric voted down whichever way it pointed.
		td.Facts = append(td.Facts, signalFact(OptionsSignalLabel,
			classifyOptionsPositioning(ratio), "computed", link))
	}
	if iv, strike, ok := atmIV(res.Options[0].Calls, res.Options[0].Puts, spot); ok {
		td.Facts = append(td.Facts, Fact{
			Label: "Implied volatility (ATM, front expiry)",
			Value: fmt.Sprintf("%.1f%% annualized at the %.2f strike (spot %.2f)%s",
				iv*100, strike, spot, note),
			AsOf:   time.Now(),
			Source: "Yahoo Finance options",
			URL:    link,
		})
	}
	return td, nil
}

func (p *yahooOptionsProvider) fetchChain(ctx context.Context, symbol string, expiry int64) (yahooOptionsResp, error) {
	var out yahooOptionsResp
	if err := p.limiter.Wait(ctx); err != nil {
		return out, fmt.Errorf("%w: Yahoo options: %v", ErrUnavailable, err)
	}
	u := p.baseURL + "/v7/finance/options/" + url.PathEscape(yahooSymbol(symbol))
	if expiry > 0 {
		u += fmt.Sprintf("?date=%d", expiry)
	}
	// Do carries the cookie+crumb and retries once on a 401, which is what a
	// stale crumb looks like.
	resp, err := p.auth.Do(ctx, u)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// A 401 that survives the retry means the handshake itself is failing;
		// say so, because "HTTP 401" alone read as a transient gate for weeks.
		if resp.StatusCode == http.StatusUnauthorized {
			return out, fmt.Errorf("%w: Yahoo options HTTP 401 for %s after the crumb handshake — the handshake is not working, sentiment is running without option data",
				ErrUnavailable, symbol)
		}
		return out, fmt.Errorf("%w: Yahoo options HTTP %d for %s", ErrUnavailable, resp.StatusCode, symbol)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("%w: Yahoo options: %v", ErrUnavailable, err)
	}
	if out.OptionChain.Error != nil {
		return out, fmt.Errorf("%w: Yahoo options: %s", ErrUnavailable, out.OptionChain.Error.Description)
	}
	return out, nil
}

// atmIV averages the call and put implied volatilities at the strike closest to
// spot. Averaging the two sides is what makes the number comparable to the
// realized volatility computed from closes: a single side carries the skew.
func atmIV(calls, puts []yahooOptionLeg, spot float64) (iv, strike float64, ok bool) {
	if spot <= 0 {
		return 0, 0, false
	}
	nearest := func(legs []yahooOptionLeg) (yahooOptionLeg, bool) {
		best, found := yahooOptionLeg{}, false
		for _, l := range legs {
			if l.ImpliedVolatility <= 0 || l.Strike <= 0 {
				continue
			}
			if !found || math.Abs(l.Strike-spot) < math.Abs(best.Strike-spot) {
				best, found = l, true
			}
		}
		return best, found
	}
	c, haveCall := nearest(calls)
	p, havePut := nearest(puts)
	switch {
	case haveCall && havePut:
		return (c.ImpliedVolatility + p.ImpliedVolatility) / 2, (c.Strike + p.Strike) / 2, true
	case haveCall:
		return c.ImpliedVolatility, c.Strike, true
	case havePut:
		return p.ImpliedVolatility, p.Strike, true
	}
	return 0, 0, false
}
