package marketdata

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// stubProvider serves a fixed set of tickers for one domain and reports a
// caller-chosen error for everything else.
type stubProvider struct {
	name    string
	source  string
	domains []string
	data    map[string]TickerData
	macro   []Fact
	failErr error
}

func (s *stubProvider) Name() string      { return s.name }
func (s *stubProvider) Source() string    { return s.source }
func (s *stubProvider) Domains() []string { return s.domains }
func (s *stubProvider) Available() bool   { return true }

func (s *stubProvider) Fetch(ctx context.Context, domain, ticker string) (TickerData, error) {
	if td, ok := s.data[strings.ToUpper(ticker)]; ok {
		return td, nil
	}
	if s.failErr != nil {
		return TickerData{}, s.failErr
	}
	return TickerData{}, ErrUnavailable
}

func (s *stubProvider) MacroFetch(ctx context.Context) ([]Fact, error) {
	if len(s.macro) == 0 {
		return nil, ErrNotApplicable
	}
	return s.macro, nil
}

func newsProvider() *stubProvider {
	return &stubProvider{
		name:    "Stub",
		source:  "https://stub.example.com",
		domains: []string{"news"},
		data: map[string]TickerData{
			"AAPL": {Ticker: "AAPL", Facts: []Fact{{
				Label:  "Headline 1",
				Value:  "Apple ships something — Reuters",
				AsOf:   time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC),
				Source: "reuters.com",
				URL:    "https://reuters.com/markets/apple-story",
			}}},
		},
	}
}

// A pack that fetched nothing for a ticker used to look identical to one that
// was never asked about it. That silence is what the specialists filled in.
func TestBuildPackMarksRequestedButMissing(t *testing.T) {
	svc := NewService(nil, newsProvider())
	pack := svc.BuildPack(context.Background(), "news", []string{"AAPL", "MSFT", "nvda"})

	if !pack.Coverage["AAPL"] {
		t.Error("AAPL had data and should be covered")
	}
	for _, want := range []string{"MSFT", "NVDA"} {
		covered, present := pack.Coverage[want]
		if !present {
			t.Errorf("%s was requested but is absent from Coverage entirely", want)
		}
		if covered {
			t.Errorf("%s has no data and must not be marked covered", want)
		}
	}

	ungrounded := pack.Ungrounded()
	if len(ungrounded) != 2 || ungrounded[0] != "MSFT" || ungrounded[1] != "NVDA" {
		t.Errorf("Ungrounded() = %v, want [MSFT NVDA]", ungrounded)
	}
}

func TestMarkdownStatesTheGaps(t *testing.T) {
	svc := NewService(nil, newsProvider())
	pack := svc.BuildPack(context.Background(), "news", []string{"AAPL", "MSFT", "NVDA"})

	md := pack.Markdown()
	if !strings.Contains(md, "#### No verified data for") {
		t.Fatalf("markdown lacks the ungrounded block:\n%s", md)
	}
	if !strings.Contains(md, "MSFT, NVDA") {
		t.Errorf("ungrounded block should name MSFT and NVDA:\n%s", md)
	}
	if !strings.Contains(md, "`missing` array") {
		t.Errorf("ungrounded block should tell the agent what to do with the gap:\n%s", md)
	}
	// The link is what makes the fact citable by an agent that cannot browse.
	if !strings.Contains(md, "https://reuters.com/markets/apple-story") {
		t.Errorf("fact lines should carry their source URL:\n%s", md)
	}
}

// A pack that fetched nothing at all must still render the gap, or the prompt
// says nothing about the shortlist it was handed.
func TestMarkdownWithNoDataStillReportsTheShortlist(t *testing.T) {
	svc := NewService(nil, &stubProvider{
		name: "Empty", source: "https://empty.example.com", domains: []string{"news"},
	})
	pack := svc.BuildPack(context.Background(), "news", []string{"AAPL"})

	md := pack.Markdown()
	if !strings.Contains(md, "No verified data for") || !strings.Contains(md, "AAPL") {
		t.Errorf("an empty pack must still name the tickers it covers nothing for:\n%s", md)
	}
}

// Citable is the allow-list a search-less agent may quote from. It must hold
// publisher domains and provider endpoints, and nothing else.
func TestPackCitableHoldsOnlyRealDomains(t *testing.T) {
	prov := newsProvider()
	prov.data["AAPL"].Facts[0].Source = "SEC EDGAR" // a label, not a domain
	svc := NewService(nil, prov)
	pack := svc.BuildPack(context.Background(), "news", []string{"AAPL"})

	if !pack.Citable["reuters.com"] {
		t.Error("the publisher of a headline we carry should be citable")
	}
	if !pack.Citable["stub.example.com"] {
		t.Error("the provider endpoint should be citable")
	}
	if pack.Citable["SEC EDGAR"] || pack.Citable["sec edgar"] {
		t.Error("a provider label is not a domain and must not widen the citable set")
	}
}

// ErrNotApplicable means "this source does not cover that", which is not a
// failure. Recording it filled the run's error list with one entry per foreign
// ticker and per macro-less provider, burying the failures that mattered.
func TestBuildPackIgnoresNotApplicable(t *testing.T) {
	svc := NewService(nil, &stubProvider{
		name: "Partial", source: "https://partial.example.com",
		domains: []string{"news"}, failErr: ErrNotApplicable,
	})
	pack := svc.BuildPack(context.Background(), "news", []string{"SAP.DE"})
	if len(pack.Errors) != 0 {
		t.Errorf("a not-applicable ticker is not an error: %v", pack.Errors)
	}

	svc = NewService(nil, &stubProvider{
		name: "Broken", source: "https://broken.example.com",
		domains: []string{"news"}, failErr: errors.New("connection refused"),
	})
	pack = svc.BuildPack(context.Background(), "news", []string{"AAPL"})
	if len(pack.Errors) != 1 {
		t.Errorf("a real failure must still be recorded, got %v", pack.Errors)
	}
}
