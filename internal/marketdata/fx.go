package marketdata

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// FXRates converts a listing's own currency into USD, so that one liquidity
// floor and one account can be stated once and mean the same thing on every
// exchange.
//
// It exists because nothing in the pipeline was doing this. `AvgDollarVol20` is
// Σ close·volume in whatever the listing trades in, and every consumer read it
// as dollars: the pre-screen's $20M floor, the risk gate's liquidity check, the
// scout table's `ADV$M` column. On 2026-09-01 that floor excluded exactly one
// name out of 274 while asia100's median read $10,015M — SK Hynix's ₩6.76tn
// turnover printing as "$6,760,821M". The Chief Analyst spotted it unaided and
// wrote it into its own notes.
//
// Rates come from Yahoo's keyless chart endpoint, the same source and client as
// every price series, so this inherits the rate limiter, the cache and the TTL.
// One extra request per distinct currency per run.
type FXRates struct {
	yc *YahooClient

	mu    sync.Mutex
	rates map[string]float64 // ISO code → USD per unit
	errs  map[string]error   // codes we already failed on, so we ask once
}

func NewFXRates(yc *YahooClient) *FXRates {
	return &FXRates{yc: yc, rates: map[string]float64{"USD": 1}, errs: map[string]error{}}
}

// ToUSD returns the multiplier that converts an amount quoted in ticker's own
// currency into USD, along with the currency code.
//
// A US listing is (1, "USD", nil) without touching the network. A rate that
// cannot be fetched returns an error and a multiplier of 0: callers must treat
// that as "unknown", never as "free", because silently defaulting to 1 is the
// bug this type exists to fix.
func (f *FXRates) ToUSD(ctx context.Context, ticker string) (float64, string, error) {
	if f == nil {
		return 1, "USD", nil
	}
	ex := exchangeFor(ticker)
	if ex.currency == "USD" {
		return 1, "USD", nil
	}

	f.mu.Lock()
	if r, ok := f.rates[ex.currency]; ok {
		f.mu.Unlock()
		return f.scale(ex, r), ex.currency, nil
	}
	if err, ok := f.errs[ex.currency]; ok {
		f.mu.Unlock()
		return 0, ex.currency, err
	}
	f.mu.Unlock()

	// Yahoo spells a cross as <BASE><QUOTE>=X, and its close is USD per unit of
	// base — exactly the multiplier wanted.
	rate, err := f.fetch(ctx, ex.currency)

	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		f.errs[ex.currency] = err
		return 0, ex.currency, err
	}
	f.rates[ex.currency] = rate
	return f.scale(ex, rate), ex.currency, nil
}

func (f *FXRates) fetch(ctx context.Context, code string) (float64, error) {
	pair := code + "USD=X"
	s, err := f.yc.History(ctx, pair)
	if err != nil {
		return 0, fmt.Errorf("%w: FX %s: %v", ErrUnavailable, pair, err)
	}
	rate := s.LastClose()
	if rate <= 0 {
		return 0, fmt.Errorf("%w: FX %s: no usable rate", ErrUnavailable, pair)
	}
	return rate, nil
}

// scale applies the minor-unit correction. London quotes in pence, so a GBP rate
// has to be divided by 100 — the classic way to be wrong about a UK listing by
// two orders of magnitude in the direction that makes it look liquid.
func (f *FXRates) scale(ex exchange, rate float64) float64 {
	if ex.pence {
		return rate / 100
	}
	return rate
}

// Convert turns an amount quoted in ticker's currency into USD. It reports false
// when the rate is unknown, so a caller can decline to compare rather than
// compare wrongly.
func (f *FXRates) Convert(ctx context.Context, ticker string, amount float64) (float64, bool) {
	rate, _, err := f.ToUSD(ctx, ticker)
	if err != nil || rate <= 0 {
		return 0, false
	}
	return amount * rate, true
}

// Failures lists the currencies whose rates could not be fetched, with the
// reason, for the run's data_errors. Sorted for a stable artifact.
func (f *FXRates) Failures() []string {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for code, err := range f.errs {
		out = append(out, fmt.Sprintf("FX %s: %v", code, err))
	}
	sort.Strings(out)
	return out
}
