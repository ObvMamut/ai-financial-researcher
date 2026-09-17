package marketdata

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// ResearchSnapshot is a closed evidence corpus collected before either arm of
// an experiment starts. Missing entries are unavailable; there is no live
// fallback. Callers receive copies so one arm cannot mutate the other's input.
type ResearchSnapshot struct {
	Version          int                                 `json:"version"`
	StartedAt        time.Time                           `json:"collection_started_at"`
	AsOf             time.Time                           `json:"as_of"`
	Tickers          []string                            `json:"tickers"`
	Packs            map[string]*DataPack                `json:"packs"`
	Discovery        *DataPack                           `json:"discovery"`
	Prices           map[string]*quant.Series            `json:"prices"`
	PriceErrors      map[string]string                   `json:"price_errors"`
	ReportDateValues map[string]time.Time                `json:"report_dates"`
	Documents        map[string][]model.EvidenceDocument `json:"documents"`
	FilingsByTicker  map[string][]model.EvidenceDocument `json:"filings"`
	FilingErrors     map[string]string                   `json:"filing_errors"`
}

func NewFrozenService(s *ResearchSnapshot) *Service { return &Service{frozen: s} }

func cloneEvidence(d model.EvidenceDocument) model.EvidenceDocument {
	d.Links = slices.Clone(d.Links)
	d.SelectedSpans = slices.Clone(d.SelectedSpans)
	d.OmittedClaimIDs = slices.Clone(d.OmittedClaimIDs)
	return d
}

func (s *ResearchSnapshot) BuildPack(ctx context.Context, domain string, tickers []string) *DataPack {
	source := s.Packs[domain]
	if domain == "quant" {
		// Quant is computed from frozen prices, with the same macro backdrop
		// the live service supplies. There is no per-company quant provider.
		source = s.Packs["macro"]
	}
	return frozenPack(ctx, source, domain, tickers)
}

func (s *ResearchSnapshot) DiscoveryPack(ctx context.Context, tickers []string) *DataPack {
	return frozenPack(ctx, s.Discovery, "news", tickers)
}

func frozenPack(ctx context.Context, source *DataPack, domain string, tickers []string) *DataPack {
	p := NewDataPack(domain)
	if ctx.Err() != nil {
		p.Errors = append(p.Errors, ctx.Err().Error())
		return p
	}
	if source != nil {
		p.MacroFacts = slices.Clone(source.MacroFacts)
		maps.Copy(p.Sources, source.Sources)
		maps.Copy(p.Citable, source.Citable)
		p.Errors = append(p.Errors, source.Errors...)
	}
	for _, name := range tickers {
		t := strings.ToUpper(name)
		p.Coverage[t] = false
		if source == nil {
			p.Errors = append(p.Errors, t+": domain absent from frozen corpus")
			continue
		}
		if domain == "quant" || IsRegimeDomain(domain) {
			continue
		}
		if td, ok := source.ByTicker[t]; ok {
			td.Facts = slices.Clone(td.Facts)
			td.Warnings = slices.Clone(td.Warnings)
			p.ByTicker[t] = td
			p.Coverage[t] = source.Coverage[t]
		} else if covered, requested := source.Coverage[t]; !requested || covered {
			p.Errors = append(p.Errors, t+": facts unavailable in frozen corpus")
		}
		if at, ok := source.EventDates[t]; ok {
			p.EventDates[t] = at
		}
	}
	return p
}

func (s *ResearchSnapshot) History(ctx context.Context, symbol string) (*quant.Series, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	symbol = strings.ToUpper(symbol)
	if series := s.Prices[symbol]; series != nil && len(series.Bars) > 0 {
		copy := *series
		copy.Bars = slices.Clone(series.Bars)
		return &copy, nil
	}
	return nil, fmt.Errorf("%w: frozen prices %s: %s", ErrUnavailable, symbol, s.PriceErrors[symbol])
}
func (s *ResearchSnapshot) HistoryFresh(ctx context.Context, symbol string) (*quant.Series, error) {
	return s.History(ctx, symbol)
}
func (s *ResearchSnapshot) LastClose(ctx context.Context, symbol string) (float64, string, error) {
	p, err := s.History(ctx, symbol)
	if err != nil {
		return 0, "", err
	}
	return p.LastClose(), p.AsOf(), nil
}
func (s *ResearchSnapshot) Prefetch(ctx context.Context, symbols []string) int {
	n := 0
	for _, symbol := range symbols {
		if _, err := s.History(ctx, symbol); err == nil {
			n++
		}
	}
	return n
}
func (s *ResearchSnapshot) ReportDates(ctx context.Context, tickers []string, since time.Time) map[string]time.Time {
	out := map[string]time.Time{}
	if ctx.Err() != nil {
		return out
	}
	for _, ticker := range tickers {
		if d, ok := s.ReportDateValues[strings.ToUpper(ticker)]; ok && !d.Before(since) && !d.After(s.AsOf) {
			out[strings.ToUpper(ticker)] = d
		}
	}
	return out
}
func (s *ResearchSnapshot) ReadDocument(ctx context.Context, ticker, raw string) model.EvidenceDocument {
	if ctx.Err() == nil {
		for _, d := range s.Documents[strings.ToUpper(ticker)] {
			if d.URL == raw {
				return cloneEvidence(d)
			}
		}
	}
	message := "document unavailable in frozen corpus; live retrieval disabled"
	if ctx.Err() != nil {
		message = ctx.Err().Error()
	}
	return model.EvidenceDocument{ID: EvidenceID(ticker, raw, message), Ticker: ticker, URL: raw, Kind: "document", Error: message, RetrievedAt: s.AsOf}
}
func (s *ResearchSnapshot) Filings(ctx context.Context, ticker string) ([]model.EvidenceDocument, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ticker = strings.ToUpper(ticker)
	if reason := s.FilingErrors[ticker]; reason != "" {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, reason)
	}
	if docs, ok := s.FilingsByTicker[ticker]; ok {
		out := make([]model.EvidenceDocument, len(docs))
		for i, d := range docs {
			out[i] = cloneEvidence(d)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: filings absent from frozen corpus", ErrUnavailable)
}

// CompletedDailySeries copies only bars that were complete at acquisition.
// Persisting forming bars and later advancing an evaluation clock would turn an
// old intraday price into a false final close.
func CompletedDailySeries(series *quant.Series, symbol string, acquiredAt time.Time) *quant.Series {
	if series == nil {
		return nil
	}
	out := &quant.Series{Symbol: series.Symbol, Bars: []quant.Bar{}}
	for _, bar := range series.Bars {
		day, err := time.Parse("2006-01-02", bar.Date)
		if err == nil && !day.Add(time.Duration(MarketCloseUTC(symbol))*time.Hour).After(acquiredAt) {
			out.Bars = append(out.Bars, bar)
		}
	}
	return out
}

// SourceEvidence preserves original kinds, publication times and truncation
// flags. Filing-index notices must not be promoted into source documents.
func (s *ResearchSnapshot) SourceEvidence(ctx context.Context, ticker string) []model.EvidenceDocument {
	if ctx.Err() != nil {
		return nil
	}
	ticker = strings.ToUpper(ticker)
	var out []model.EvidenceDocument
	for _, docs := range [][]model.EvidenceDocument{s.Documents[ticker], s.FilingsByTicker[ticker]} {
		for _, d := range docs {
			out = append(out, cloneEvidence(d))
		}
	}
	if reason := s.FilingErrors[ticker]; reason != "" {
		out = append(out, model.EvidenceDocument{Ticker: ticker, Kind: "fact", Error: reason, RetrievedAt: s.AsOf})
	}
	return out
}

// AddLegacySources renders the same captured source corpus for the legacy
// fundamentals prompt. It does not alter the underlying provider pack or
// reclassify document retrieval as fundamental-metric coverage.
func (s *ResearchSnapshot) AddLegacySources(ctx context.Context, p *DataPack, tickers []string) {
	for _, ticker := range tickers {
		td := p.ByTicker[ticker]
		td.Ticker = ticker
		for _, d := range s.SourceEvidence(ctx, ticker) {
			if d.Error != "" {
				p.Errors = append(p.Errors, ticker+": "+d.Error)
				continue
			}
			if strings.TrimSpace(d.Text) == "" {
				continue
			}
			label := "Frozen " + d.Kind + ": " + d.Title
			if d.Truncated {
				label += " (truncated)"
			}
			td.Facts = append(td.Facts, Fact{Label: label, Value: d.Text, AsOf: d.PublishedAt, Source: d.Source, URL: d.URL})
			p.addCitable(d.URL)
		}
		p.ByTicker[ticker] = td
	}
}
