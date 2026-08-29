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

// macroProvider serves regime facts and nothing per-ticker, which is exactly the
// shape BuildPack produces for the macro domain: Coverage seeded all-false and
// never written true.
func macroProvider(facts ...Fact) *stubProvider {
	return &stubProvider{
		name: "FRED", source: "https://fred.stlouisfed.org",
		domains: []string{"macro"}, macro: facts,
	}
}

// The macro pack's prompt carried four real FRED series *and* a block reading
// "No verified data for: 000660.KS, 2330.TW, …" for all twelve names. The
// specialist believed the block over the data and scored every name strength 1.
func TestMacroPackReportsNoPerTickerGaps(t *testing.T) {
	svc := NewService(nil, macroProvider(Fact{
		Label:  "10-Year Treasury Rate",
		Value:  "4.67",
		AsOf:   time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC),
		Source: "fred.stlouisfed.org",
	}))
	pack := svc.BuildPack(context.Background(), "macro", []string{"NVDA", "AIR.PA"})

	if got := pack.Ungrounded(); len(got) != 0 {
		t.Errorf("Ungrounded() = %v, want none: macro grounds the whole shortlist or none of it", got)
	}

	md := pack.Markdown()
	if strings.Contains(md, "No verified data for") {
		t.Errorf("macro must not render a per-ticker gap block:\n%s", md)
	}
	for _, tk := range []string{"NVDA", "AIR.PA"} {
		if strings.Contains(md, tk) {
			t.Errorf("macro markdown names ticker %s; its evidence is market-wide:\n%s", tk, md)
		}
	}
	if !strings.Contains(md, "4.67") {
		t.Errorf("macro markdown should carry its verified series:\n%s", md)
	}
}

// A macro run that fetched nothing still has a gap to report — just a
// domain-level one, not a ticker list.
func TestMacroPackWithNoFactsWarnsAtDomainLevel(t *testing.T) {
	svc := NewService(nil, macroProvider())
	pack := svc.BuildPack(context.Background(), "macro", []string{"NVDA", "AIR.PA"})

	md := pack.Markdown()
	if !strings.Contains(md, "No verified macro data") {
		t.Fatalf("an empty macro pack must warn at the domain level:\n%s", md)
	}
	for _, tk := range []string{"NVDA", "AIR.PA"} {
		if strings.Contains(md, tk) {
			t.Errorf("the domain-level warning must name no tickers, found %s:\n%s", tk, md)
		}
	}
}

// Regression guard for the Phase 4 behaviour this fix must not undo: a
// per-ticker domain with the same all-false Coverage still lists its gaps.
func TestPerTickerPackStillListsGaps(t *testing.T) {
	svc := NewService(nil, &stubProvider{
		name: "Empty", source: "https://empty.example.com", domains: []string{"news"},
	})
	pack := svc.BuildPack(context.Background(), "news", []string{"NVDA", "AIR.PA"})

	if got := pack.Ungrounded(); len(got) != 2 {
		t.Errorf("Ungrounded() = %v, want both names", got)
	}
	md := pack.Markdown()
	if !strings.Contains(md, "No verified data for") {
		t.Fatalf("a news pack must still render its per-ticker gap block:\n%s", md)
	}
	for _, tk := range []string{"NVDA", "AIR.PA"} {
		if !strings.Contains(md, tk) {
			t.Errorf("news gap block should name %s:\n%s", tk, md)
		}
	}
}

// SEC and AlphaVantage are US-only, so half a balanced shortlist can never be
// covered. Listed as a plain gap it read as a fetch failure; it is a known limit.
func TestGapBlockMarksNonUSListingsAsStructural(t *testing.T) {
	svc := NewService(nil, newsProvider())
	pack := svc.BuildPack(context.Background(), "news", []string{"AAPL", "GE", "AIR.PA", "2330.TW"})

	md := pack.Markdown()
	if !strings.Contains(md, "GE") {
		t.Errorf("a US name with no data is still a plain gap:\n%s", md)
	}
	if !strings.Contains(md, "2330.TW, AIR.PA (non-US listings — no US filings or news coverage)") {
		t.Errorf("non-US gaps should be annotated as structural:\n%s", md)
	}
	if !strings.Contains(md, "not a fetch failure") {
		t.Errorf("the annotation should say the gap is expected:\n%s", md)
	}
	// The annotation must not swallow the US name into the same clause.
	if strings.Contains(md, "GE, 2330.TW") {
		t.Errorf("US and non-US gaps must be listed separately:\n%s", md)
	}
}

func TestIsUSListing(t *testing.T) {
	for _, tk := range []string{"AAPL", "NVDA", "BRK.B"} {
		if !IsUSListing(tk) {
			t.Errorf("IsUSListing(%q) = false, want true", tk)
		}
	}
	for _, tk := range []string{"AIR.PA", "2330.TW", "000660.KS", "HDFCBANK.NS", "dte.de"} {
		if IsUSListing(tk) {
			t.Errorf("IsUSListing(%q) = true, want false", tk)
		}
	}
}
