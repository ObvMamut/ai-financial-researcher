package marketdata

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// ErrUnavailable is returned when a provider cannot satisfy a request
// (e.g. rate limit, network error, or missing ticker).
var ErrUnavailable = errors.New("market data unavailable from this provider")

// ErrNotApplicable means the provider structurally does not cover this request
// — a macro-only source asked for a ticker, or an SEC-only source asked about a
// foreign listing. It is not a failure and BuildPack does not record it, so a
// run's error list stays a list of things that actually went wrong. It wraps
// ErrUnavailable so existing errors.Is checks keep working.
var ErrNotApplicable = fmt.Errorf("%w: not covered by this provider", ErrUnavailable)

// Fact is a single piece of verified information.
//
// URL is the canonical link to the primary document a fact came from (a news
// article, a filing). It is what makes a citation checkable: agents running on a
// search-less engine may cite only URLs that appear in the pack, and the
// orchestrator enforces that against the pack's URL set.
type Fact struct {
	Summary string    `json:"summary,omitempty"`
	Content string    `json:"content,omitempty"`
	Label   string    `json:"label"`
	Value   string    `json:"value"`
	AsOf    time.Time `json:"as_of"`
	Source  string    `json:"source"`
	URL     string    `json:"url,omitempty"`
}

// TickerData is the set of facts collected for one ticker in one domain.
//
// Warnings records data the provider deliberately withheld, and why — a figure
// too stale to sit beside the others, a field it could not reconcile. BuildPack
// carries them into the pack's error list so they reach metadata.json instead of
// disappearing. A withheld fact is not a fetch failure, but the reader still has
// to know the provider had something and chose not to print it.
type TickerData struct {
	Ticker      string                   `json:"ticker"`
	Facts       []Fact                   `json:"facts"`
	Warnings    []string                 `json:"warnings,omitempty"`
	Diagnostics []model.SourceDiagnostic `json:"diagnostics,omitempty"`
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
