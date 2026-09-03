package marketdata

import (
	"context"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// PriceSource is the daily-bar seam.
type PriceSource interface {
	History(ctx context.Context, symbol string) (*quant.Series, error)
	HistoryFresh(ctx context.Context, symbol string) (*quant.Series, error)
	LastClose(ctx context.Context, symbol string) (float64, string, error)
	Prefetch(ctx context.Context, symbols []string) int
}

// alpacaEligible reports whether Alpaca can serve this symbol at all.
//
// IsUSListing on its own is not enough, and both ways it is wrong are silent.
// isForeignListing decides by looking for a known exchange suffix after a dot,
// so "^GSPC" and "EURUSD=X" contain no dot and come back as US listings — but
// Alpaca has no index data and no FX. Routing them there empties the
// pre-screen's benchmark column and macro's regime block, which is what macro
// coverage is now grounded on, and the fallback to Yahoo only hides it behind
// a doubled request.
//
//   - "^..." is an index (^GSPC, ^NDX, ^STOXX50E, ^N225, ^HSI, ^STI, ...) —
//     see universe.benchmarkSymbols.
//   - "...=X" is one of fx.go's synthetic Yahoo currency pairs.
func alpacaEligible(symbol string) bool {
	if strings.HasPrefix(symbol, "^") || strings.Contains(symbol, "=") {
		return false
	}
	return IsUSListing(symbol)
}

// NewPrices is the one place a price source is built. Callers pass the Alpaca
// credentials they hold and get back a source that is correct whether or not
// those are set: with no key it is Yahoo alone, exactly as before Alpaca
// existed.
//
// It takes the two key strings rather than a model.ProviderConfig on purpose —
// marketdata does not import model, and one provider's credentials are not
// worth inverting that.
func NewPrices(alpacaKeyID, alpacaSecret string, cache *Cache) *RoutedPrices {
	return NewRoutedPrices(NewAlpacaPrices(alpacaKeyID, alpacaSecret, cache), NewYahooClient(cache))
}

// RoutedPrices sends each symbol to the source that can actually answer for it:
// Alpaca for US equities, Yahoo for foreign listings, index benchmarks and FX.
//
// Yahoo stays the fallback rather than being replaced, because Alpaca reaches
// 155 of the universe's 272 constituents and nothing else reaches the other
// 117. An Alpaca error therefore degrades to Yahoo rather than losing the name.
//
// FX deliberately does not come through here at all: FXRates holds the concrete
// *YahooClient (fx.go), so a synthetic pair can never reach a source that has
// no idea what it is.
type RoutedPrices struct {
	alpaca *AlpacaPrices
	yahoo  *YahooClient
}

func NewRoutedPrices(alpaca *AlpacaPrices, yahoo *YahooClient) *RoutedPrices {
	return &RoutedPrices{alpaca: alpaca, yahoo: yahoo}
}

func (r *RoutedPrices) useAlpaca(symbol string) bool {
	return r.alpaca != nil && r.alpaca.Available() && alpacaEligible(symbol)
}

func (r *RoutedPrices) History(ctx context.Context, symbol string) (*quant.Series, error) {
	if r.useAlpaca(symbol) {
		if s, err := r.alpaca.History(ctx, symbol); err == nil {
			return s, nil
		}
	}
	return r.yahoo.History(ctx, symbol)
}

func (r *RoutedPrices) HistoryFresh(ctx context.Context, symbol string) (*quant.Series, error) {
	if r.useAlpaca(symbol) {
		if s, err := r.alpaca.HistoryFresh(ctx, symbol); err == nil {
			return s, nil
		}
	}
	return r.yahoo.HistoryFresh(ctx, symbol)
}

func (r *RoutedPrices) LastClose(ctx context.Context, symbol string) (float64, string, error) {
	s, err := r.History(ctx, symbol)
	if err != nil {
		return 0, "", err
	}
	return s.LastClose(), s.AsOf(), nil
}

// Prefetch batches the eligible symbols into Alpaca's multi-symbol endpoint and
// warms the shared cache. Ineligible symbols are skipped silently — they are
// not failures, they are simply Yahoo's, and Yahoo has no batch form to warm.
func (r *RoutedPrices) Prefetch(ctx context.Context, symbols []string) int {
	if r.alpaca == nil || !r.alpaca.Available() {
		return 0
	}
	var eligible []string
	for _, s := range symbols {
		if alpacaEligible(s) {
			eligible = append(eligible, s)
		}
	}
	return r.alpaca.Prefetch(ctx, eligible)
}

// SetPriceTTL forwards the configured TTL to both back ends.
func (r *RoutedPrices) SetPriceTTL(d time.Duration) {
	if r.alpaca != nil {
		r.alpaca.SetPriceTTL(d)
	}
	if r.yahoo != nil {
		r.yahoo.SetPriceTTL(d)
	}
}
