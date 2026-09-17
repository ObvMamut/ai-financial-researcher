package marketdata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

func TestFrozenSnapshotCopiesEveryMutableEvidenceValue(t *testing.T) {
	p := NewDataPack("fundamentals")
	p.ByTicker["AAPL"] = TickerData{Facts: []Fact{{Content: "original"}}, Warnings: []string{"original"}}
	p.MacroFacts = []Fact{{Value: "original"}}
	p.Sources["original"] = true
	p.Citable["example.com"] = true
	d := model.EvidenceDocument{URL: "https://example.com", Text: "original", Links: []string{"original"}}
	s := &ResearchSnapshot{Packs: map[string]*DataPack{"fundamentals": p}, Prices: map[string]*quant.Series{"AAPL": {Bars: []quant.Bar{{Close: 100}}}}, Documents: map[string][]model.EvidenceDocument{"AAPL": {d}}, FilingsByTicker: map[string][]model.EvidenceDocument{"AAPL": {d}}}
	ctx := context.Background()
	copy := s.BuildPack(ctx, "fundamentals", []string{"AAPL"})
	copy.ByTicker["AAPL"].Facts[0].Content = "changed"
	copy.ByTicker["AAPL"].Warnings[0] = "changed"
	copy.MacroFacts[0].Value = "changed"
	delete(copy.Sources, "original")
	delete(copy.Citable, "example.com")
	price, err := s.History(ctx, "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	price.Bars[0].Close = 999
	doc := s.ReadDocument(ctx, "AAPL", d.URL)
	doc.Links[0] = "changed"
	filings, err := s.Filings(ctx, "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	filings[0].Links[0] = "changed"
	if p.ByTicker["AAPL"].Facts[0].Content != "original" || p.ByTicker["AAPL"].Warnings[0] != "original" || p.MacroFacts[0].Value != "original" || !p.Sources["original"] || !p.Citable["example.com"] || s.Prices["AAPL"].LastClose() != 100 || s.Documents["AAPL"][0].Links[0] != "original" || s.FilingsByTicker["AAPL"][0].Links[0] != "original" {
		t.Fatal("snapshot was mutated by a reader")
	}
}

func TestFrozenComputedAndRegimePacksPreserveTheirCoverageSemantics(t *testing.T) {
	macro := NewDataPack("macro")
	macro.MacroFacts = []Fact{{Label: "Policy rate", Value: "4%"}}
	macro.Coverage["AAPL"] = false
	news := NewDataPack("news")
	news.Coverage["AAPL"] = false // requested but no usable facts, not an absent snapshot
	s := &ResearchSnapshot{Packs: map[string]*DataPack{"macro": macro, "news": news}}
	for _, domain := range []string{"macro", "quant"} {
		p := s.BuildPack(context.Background(), domain, []string{"AAPL"})
		if len(p.Errors) != 0 || len(p.MacroFacts) != 1 || p.Coverage["AAPL"] {
			t.Fatalf("%s invented a per-company provider gap or lost macro evidence: %+v", domain, p)
		}
	}
	if p := s.BuildPack(context.Background(), "news", []string{"AAPL"}); len(p.Errors) != 0 || p.Coverage["AAPL"] {
		t.Fatalf("frozen unavailable coverage changed semantics: %+v", p)
	}
	if p := s.BuildPack(context.Background(), "news", []string{"MSFT"}); len(p.Errors) == 0 {
		t.Fatal("undeclared company evidence was silently accepted")
	}
}

func TestFrozenSnapshotMissingAndCanceledReadsAreExplicit(t *testing.T) {
	s := &ResearchSnapshot{AsOf: time.Now()}
	ctx := context.Background()
	if _, err := s.HistoryFresh(ctx, "AAPL"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("prices: %v", err)
	}
	if _, err := s.Filings(ctx, "AAPL"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("filings: %v", err)
	}
	if d := s.ReadDocument(ctx, "AAPL", "https://example.com"); d.Error == "" {
		t.Fatal("missing document presented as evidence")
	}
	p := s.BuildPack(ctx, "news", []string{"AAPL"})
	if p.Coverage["AAPL"] || len(p.Errors) == 0 {
		t.Fatal("missing facts presented as coverage")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.History(ctx, "AAPL"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prices: %v", err)
	}
	if _, err := s.Filings(ctx, "AAPL"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled filings: %v", err)
	}
	if _, err := CaptureResearchSnapshot(ctx, SnapshotCaptureConfig{Tickers: []string{"AAPL"}, Documents: 1, CacheDir: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled collection: %v", err)
	}
}

func TestFrozenDailyPricesCannotMatureAfterAcquisition(t *testing.T) {
	series := &quant.Series{Symbol: "AAPL", Bars: []quant.Bar{{Date: "2026-09-08", Close: 100}, {Date: "2026-09-09", Close: 101}}}
	intraday := time.Date(2026, 9, 9, 18, 0, 0, 0, time.UTC)
	saved := CompletedDailySeries(series, "AAPL", intraday)
	if len(saved.Bars) != 1 || saved.AsOf() != "2026-09-08" {
		t.Fatalf("stored forming bar: %+v", saved)
	}
	later := CompletedDailySeries(saved, "AAPL", intraday.Add(8*time.Hour))
	if len(later.Bars) != 1 {
		t.Fatal("elapsed time manufactured an acquired close")
	}
	if len(series.Bars) != 2 {
		t.Fatal("mutated provider series")
	}
}

func TestFrozenSourceKindsAndTruncationSurviveBothPresentations(t *testing.T) {
	url := "https://example.com/filing"
	pointer := model.EvidenceDocument{ID: "index", URL: url, Kind: "fact", Text: "Filing index notice", PublishedAt: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)}
	body := model.EvidenceDocument{ID: "body", URL: url, Kind: "document", Text: "Source body", Truncated: true, Links: []string{"exhibit"}}
	s := &ResearchSnapshot{Documents: map[string][]model.EvidenceDocument{"AAPL": {body}}, FilingsByTicker: map[string][]model.EvidenceDocument{"AAPL": {pointer}}}
	docs := s.SourceEvidence(context.Background(), "AAPL")
	if len(docs) != 2 || docs[0].Kind != "document" || !docs[0].Truncated || docs[1].Kind != "fact" || !docs[1].PublishedAt.Equal(pointer.PublishedAt) {
		t.Fatalf("altered source provenance: %+v", docs)
	}
	p := NewDataPack("fundamentals")
	s.AddLegacySources(context.Background(), p, []string{"AAPL"})
	if len(p.ByTicker["AAPL"].Facts) != 2 || p.ByTicker["AAPL"].Facts[0].Value != body.Text {
		t.Fatal("legacy lost source text")
	}
	if p.Coverage["AAPL"] {
		t.Fatal("source text invented fundamental metrics coverage")
	}
	for _, d := range EvidenceFromPack(p, "AAPL", time.Now()) {
		if d.Kind == "document" {
			t.Fatal("legacy presentation reclassified a filing notice")
		}
	}
}
