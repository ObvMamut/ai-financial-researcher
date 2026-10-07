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

// The plausibility band an at-the-money implied volatility has to sit inside to
// be written as a fact.
//
// Yahoo publishes a placeholder rather than an absence on a contract it has no
// quote for, and atmIV used to accept anything above zero. On 2026-09-03 that
// put "IV at 0.2% annualized" in front of the sentiment agent for six of seven
// names, and the agent — correctly, given what it was told — read a 0.2% implied
// against a 59% realized as options being given away and scored the run's
// strongest sentiment verdict on it. No listed equity trades at 2% annualized
// vol and none trades at 400%; a number outside that band is a broken quote, and
// the honest output is no fact plus an error saying what arrived.
const (
	minPlausibleIV = 0.02
	maxPlausibleIV = 4.0
)

// oiMinContracts is the total two-expiry open interest below which the put/call
// ratio says nothing. It mirrors flowMinContracts on the volume leg, which has
// always had one: BBVA's ADR line produced "put/call open interest 8.14" out of
// 676 puts against 83 calls — 759 contracts, where a single order moves the
// ratio by more than the signal it is supposed to carry.
const oiMinContracts = 2_000

// preOpenStates are the session labels under which a chain's open interest and
// implied volatility have not yet been republished for the day.
//
// Every weekday run started before the US open since the IV check existed
// (2026-09-04 06:11Z, 09-10 06:49Z, eight runs on 09-24 before 13:30Z, 10-07
// 05:13Z) withheld placeholder IVs, and 10-07 also lost open interest on six
// names, which the warnings blamed on a renamed field. In-session and weekend
// runs did not. A pre-open gap is expected and recorded as one diagnostic; the
// warnings stay for every other state, including a missing label, because
// 2026-09-04 lost open interest inside the session too.
var preOpenStates = map[string]bool{"PREPRE": true, "PRE": true}

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
				// MarketState is Yahoo's session label (PREPRE, PRE, REGULAR,
				// POST, POSTPOST, CLOSED). See preOpenStates.
				MarketState string `json:"marketState"`
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
	// Volume is the session's traded contracts at this strike. Open interest is
	// the position that already exists; volume is the position being taken
	// today, and the two answer different questions — the ratio between them is
	// the whole point of the flow leg.
	Volume float64 `json:"volume"`
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
	flow := optionFlow{Spot: res.Quote.RegularMarketPrice}
	expiries := 0
	// legs and volumeLegs exist only to tell a quiet chain from a chain whose
	// volume field stopped arriving. Both readings end with the flow leg
	// emitting nothing, and only one of them is correct. See the warning below.
	legs, volumeLegs := 0, 0
	accumulate := func(r yahooOptionsResp) {
		if len(r.OptionChain.Result) == 0 {
			return
		}
		for _, o := range r.OptionChain.Result[0].Options {
			for _, c := range o.Calls {
				callOI += c.OpenInterest
				legs++
				if c.Volume > 0 {
					volumeLegs++
				}
				flow.addCall(c)
			}
			for _, put := range o.Puts {
				putOI += put.OpenInterest
				legs++
				if put.Volume > 0 {
					volumeLegs++
				}
				flow.addPut(put)
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

	// Open interest and traded volume arrive in the same JSON object, so a
	// chain that parsed strikes and open interest but carried volume on not one
	// of them cannot be a quiet name — it is the volume field itself failing to
	// arrive. That failure is otherwise perfectly silent: every leg is dropped
	// by addCall/addPut, summary() reports not ok, and neither flow fact is
	// written, on every ticker at once, with no error anywhere. This is not an
	// error return — the open-interest positioning leg below is unaffected and
	// worth keeping — it is a warning on the run's own data_errors channel.
	var chainWarnings []string
	if legs > 0 && volumeLegs == 0 && callOI+putOI > 0 {
		chainWarnings = append(chainWarnings, fmt.Sprintf(
			"option flow leg is silent: the chain parsed %d strikes carrying %s contracts of open interest across the front %d %s%s, but not one of them reported a positive \"volume\" — open interest and volume arrive in the same object, so this is the volume field being renamed or dropped, not a quiet chain",
			legs, contracts(callOI+putOI), expiries, plural(expiries, "expiry", "expiries"), note))
	}
	// And the mirror image, which is the one that actually happened. The warning
	// above could never fire on it: it is guarded on open interest being present.
	//
	// A chain that reported real volume on some strike and zero open interest on
	// every single one is not a chain of freshly listed contracts — INTC traded
	// 90,677 calls against no standing call position at all on 2026-09-03. It is
	// the openInterest field failing to arrive, and it takes both open-interest
	// legs down at once: the put/call ratio below is skipped for want of a
	// denominator, and volOI() reads every strike as position-building.
	if legs > 0 && volumeLegs > 0 && callOI+putOI == 0 {
		chainWarnings = append(chainWarnings, fmt.Sprintf(
			"option open-interest legs are blind: the chain parsed %d strikes across the front %d %s%s and %d of them reported traded volume, but not one reported any open interest — volume and open interest arrive in the same object, so this is the \"openInterest\" field being renamed or dropped, not a chain of new listings",
			legs, expiries, plural(expiries, "expiry", "expiries"), note, volumeLegs))
	}

	if callOI > 0 && callOI+putOI >= oiMinContracts {
		ratio := putOI / callOI
		td.Facts = append(td.Facts, Fact{
			Label: OptionsPositioningLabel,
			Value: fmt.Sprintf("put/call open interest %.2f (%.0f puts vs %.0f calls) over the front %d %s%s",
				ratio, putOI, callOI, expiries, plural(expiries, "expiry", "expiries"), note),
			AsOf:   time.Now(),
			Source: "Yahoo Finance options",
			URL:    link,
		})
		// The verdict is *not* decided here. Which side a ratio favours depends on
		// where the rest of the run's names sit — an extreme every name shares is
		// the tape's level, not this one's positioning — and this provider sees
		// one ticker at a time. addPositioningSignal computes it at pack level,
		// from the raw sentence above, once every chain has been fetched.
	}
	// The flow leg: what traded today, not what is already held. Both facts are
	// written together — the sentence a reader gets and the verdict the agent is
	// bound by — so the pair never reaches addPositioningSignal half-formed.
	if summary, ok := flow.summary(expiries); ok {
		td.Facts = append(td.Facts,
			Fact{
				Label:  UnusualOptionsLabel,
				Value:  summary + note,
				AsOf:   time.Now(),
				Source: "Yahoo Finance options",
				URL:    link,
			},
			signalFact(OptionsFlowSignalLabel, classifyUnusualOptions(flow),
				"Yahoo Finance options", link),
		)
	}
	if iv, strike, ok := atmIV(res.Options[0].Calls, res.Options[0].Puts, spot); ok {
		switch {
		case iv < minPlausibleIV || iv > maxPlausibleIV:
			// No fact. An implied volatility is only useful set against the
			// realized volatility the quant stage computed, and a broken quote
			// makes that comparison say the opposite of the truth. Say what
			// arrived, so this reads as a data failure rather than a quiet name.
			chainWarnings = append(chainWarnings, fmt.Sprintf(
				"implied volatility withheld: the %.2f strike quoted %.2f%% annualized against a %.0f%%–%.0f%% plausibility band%s — that is a placeholder or a broken quote, not a volatility, and set against realized vol it would read as an option given away",
				strike, iv*100, minPlausibleIV*100, maxPlausibleIV*100, note))
		default:
			td.Facts = append(td.Facts, Fact{
				Label: "Implied volatility (ATM, front expiry)",
				Value: fmt.Sprintf("%.1f%% annualized at the %.2f strike (spot %.2f)%s",
					iv*100, strike, spot, note),
				AsOf:   time.Now(),
				Source: "Yahoo Finance options",
				URL:    link,
			})
		}
	}
	if len(chainWarnings) > 0 && preOpenStates[res.Quote.MarketState] {
		td.Diagnostics = append(td.Diagnostics, sourceDiagnostic(p.Name(), ticker, "sentiment", "off_session", "expected",
			fmt.Sprintf("chain fetched before the US session (%s)%s: open interest and implied volatility are not yet republished for the day, so %d option %s withheld rather than read",
				res.Quote.MarketState, note, len(chainWarnings), plural(len(chainWarnings), "leg was", "legs were"))))
	} else {
		td.Warnings = append(td.Warnings, chainWarnings...)
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
