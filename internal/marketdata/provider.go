package marketdata

import (
	"context"
	"errors"
	"time"
)

// ErrUnavailable is returned when a provider cannot satisfy a request
// (e.g. rate limit, network error, or missing ticker).
var ErrUnavailable = errors.New("market data unavailable from this provider")

// Fact is a single piece of verified information.
type Fact struct {
	Label  string    `json:"label"`
	Value  string    `json:"value"`
	AsOf   time.Time `json:"as_of"`
	Source string    `json:"source"`
}

// TickerData is the set of facts collected for one ticker in one domain.
type TickerData struct {
	Ticker string `json:"ticker"`
	Facts  []Fact `json:"facts"`
}

// Provider defines the interface for data sources.
type Provider interface {
	Name() string
	// Source identifies the endpoint the provider fetches from. It scopes cache
	// entries so data from one endpoint is never served to a client pointed at
	// another (see Cache.key).
	Source() string
	Domains() []string
	Available() bool
	Fetch(ctx context.Context, domain string, ticker string) (TickerData, error)
	MacroFetch(ctx context.Context) ([]Fact, error)
}
