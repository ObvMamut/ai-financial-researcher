package marketdata

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

type SnapshotCaptureConfig struct {
	Tickers, Benchmarks   []string
	Providers             model.ProviderConfig
	CacheDir, SourcesFile string
	ProviderStateDir      string
	Documents             int
	Log                   func(string)
}

// CaptureResearchSnapshot finishes collection before the paired models start.
// Acquisition is sequential and cancellable, with existing provider and document
// bounds. Provider failures are retained; cancellation never seals a partial corpus.
func CaptureResearchSnapshot(ctx context.Context, cfg SnapshotCaptureConfig) (*ResearchSnapshot, error) {
	if len(cfg.Tickers) == 0 || cfg.Documents < 1 || cfg.Documents > 32 {
		return nil, fmt.Errorf("snapshot requires tickers and a document budget of 1–32")
	}
	seeds, err := LoadResearchSources(cfg.SourcesFile)
	if err != nil {
		return nil, err
	}
	s := &ResearchSnapshot{Version: 1, StartedAt: time.Now().UTC(), Packs: map[string]*DataPack{}, Prices: map[string]*quant.Series{}, PriceErrors: map[string]string{}, Documents: map[string][]model.EvidenceDocument{}, FilingsByTicker: map[string][]model.EvidenceDocument{}, FilingErrors: map[string]string{}}
	tickers := map[string]bool{}
	for _, t := range cfg.Tickers {
		tickers[strings.ToUpper(t)] = true
	}
	for t := range tickers {
		s.Tickers = append(s.Tickers, t)
	}
	sort.Strings(s.Tickers)
	log := func(message string) {
		if cfg.Log != nil {
			cfg.Log(message)
		}
	}
	cache := NewCache(cfg.CacheDir)
	providerStateDir := cfg.ProviderStateDir
	if providerStateDir == "" {
		providerStateDir = cfg.CacheDir
	}
	prices := NewPrices(cfg.Providers.AlpacaKeyID, cfg.Providers.AlpacaSecret, cache)
	symbols := map[string]bool{}
	for _, t := range append(append([]string(nil), s.Tickers...), cfg.Benchmarks...) {
		symbols[strings.ToUpper(t)] = true
	}
	for _, t := range s.Tickers {
		if c := exchangeFor(t).currency; c != "USD" {
			symbols[c+"USD=X"] = true
		}
	}
	var ordered []string
	for t := range symbols {
		ordered = append(ordered, t)
	}
	sort.Strings(ordered)
	prices.Prefetch(ctx, s.Tickers)
	for _, t := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log("Collecting prices: " + t)
		series, e := prices.History(ctx, t)
		if e != nil {
			s.PriceErrors[t] = redact.String(e.Error())
			continue
		}
		if series == nil || len(series.Bars) == 0 {
			s.PriceErrors[t] = "no daily bars returned"
			continue
		}
		copy := CompletedDailySeries(series, t, s.StartedAt)
		if len(copy.Bars) == 0 {
			s.PriceErrors[t] = "no completed daily bars"
			continue
		}
		s.Prices[t] = copy
	}
	svc := NewService(cache, NewEdgarProvider(cfg.Providers.ContactEmail, cache), NewAlpacaNewsProvider(cfg.Providers.AlpacaKeyID, cfg.Providers.AlpacaSecret), NewYahooNewsProvider(), NewAlphaVantageProvider(cfg.Providers.AlphaVantageKey, providerStateDir), NewYahooOptionsProvider(), NewFredProvider(cfg.Providers.FredKey))
	for _, domain := range []string{"news", "fundamentals", "sentiment", "macro"} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log("Collecting domain: " + domain)
		s.Packs[domain] = svc.BuildPack(ctx, domain, s.Tickers)
	}
	// Discovery shares the collected news. It cannot see later headlines.
	s.Discovery = frozenPack(ctx, s.Packs["news"], "news", s.Tickers)
	s.ReportDateValues = NewEdgarReportDates(cfg.Providers.ContactEmail, cache).ReportDates(ctx, s.Tickers, s.StartedAt.AddDate(0, 0, -50))
	reader := DocumentReader{Contact: cfg.Providers.ContactEmail}
	for _, ticker := range s.Tickers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		log("Collecting source corpus: " + ticker)
		docs := []model.EvidenceDocument{}
		var urls []string
		for _, domain := range []string{"news", "fundamentals", "sentiment"} {
			for _, d := range EvidenceFromPack(s.Packs[domain], ticker, s.StartedAt) {
				if d.Kind == "document" {
					docs = append(docs, d)
				}
				if d.URL != "" {
					urls = append(urls, d.URL)
				}
			}
		}
		filings, e := ResearchFilings(ctx, cfg.Providers.ContactEmail, cache, ticker)
		if e != nil {
			s.FilingErrors[ticker] = redact.String(e.Error())
		} else {
			s.FilingsByTicker[ticker] = filings
		}
		var primary []string
		primary = append(primary, seeds[ticker]...)
		for _, filing := range filings {
			if filing.URL != "" {
				primary = append(primary, filing.URL)
			}
		}
		urls = append(primary, urls...)
		seen := map[string]bool{}
		for _, d := range docs {
			seen[d.URL] = true
		}
		attempts := 0
		for len(urls) > 0 && attempts < cfg.Documents {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			u := urls[0]
			urls = urls[1:]
			if seen[u] {
				continue
			}
			seen[u] = true
			attempts++
			d := reader.Read(ctx, ticker, u)
			MarkResearchAuthority(&d, seeds)
			docs = append(docs, d)
			if d.Error == "" {
				urls = append(RankResearchLinks(d.Links, u), urls...)
			}
		}
		s.Documents[ticker] = DeduplicateEvidence(docs)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.AsOf = time.Now().UTC()
	return s, nil
}
