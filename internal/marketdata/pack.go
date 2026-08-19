package marketdata

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// DataPack is a collection of verified facts for a set of tickers.
type DataPack struct {
	Domain     string
	ByTicker   map[string]TickerData
	MacroFacts []Fact
	Coverage   map[string]bool // ticker -> true if grounded
	Sources    map[string]bool
	Errors     []string // provider failures encountered while building the pack
}

func NewDataPack(domain string) *DataPack {
	return &DataPack{
		Domain:   domain,
		ByTicker: make(map[string]TickerData),
		Coverage: make(map[string]bool),
		Sources:  make(map[string]bool),
	}
}

func (p *DataPack) Markdown() string {
	if len(p.ByTicker) == 0 && len(p.MacroFacts) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("### Verified Market Data\n\n")

	if len(p.MacroFacts) > 0 {
		sb.WriteString("#### Macro Indicators\n")
		for _, f := range p.MacroFacts {
			sb.WriteString(fmt.Sprintf("- **%s**: %s (as of %s, source: %s)\n",
				f.Label, f.Value, f.AsOf.Format("2006-01-02"), f.Source))
		}
		sb.WriteString("\n")
	}

	tickers := make([]string, 0, len(p.ByTicker))
	for t := range p.ByTicker {
		tickers = append(tickers, t)
	}
	sort.Strings(tickers)

	for _, t := range tickers {
		data := p.ByTicker[t]
		if len(data.Facts) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("#### %s\n", t))
		for _, f := range data.Facts {
			sb.WriteString(fmt.Sprintf("- **%s**: %s (as of %s, source: %s)\n",
				f.Label, f.Value, f.AsOf.Format("2006-01-02"), f.Source))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// Service orchestrates multiple data providers with caching.
type Service struct {
	providers []Provider
	cache     *Cache
}

func NewService(cache *Cache, providers ...Provider) *Service {
	return &Service{
		providers: providers,
		cache:     cache,
	}
}

func (s *Service) BuildPack(ctx context.Context, domain string, tickers []string) *DataPack {
	pack := NewDataPack(domain)

	// Collect Macro data if applicable
	for _, prov := range s.providers {
		if !prov.Available() {
			continue
		}

		var macro []Fact
		found := false
		if s.cache != nil {
			found, _ = s.cache.Get(prov.Source(), prov.Name(), "MacroFetch", "GLOBAL", &macro)
		}

		if !found {
			var err error
			macro, err = prov.MacroFetch(ctx)
			if err != nil {
				pack.Errors = append(pack.Errors, fmt.Sprintf("%s macro: %v", prov.Name(), err))
			}
			if err == nil && len(macro) > 0 && s.cache != nil {
				s.cache.Set(prov.Source(), prov.Name(), "MacroFetch", "GLOBAL", macro)
			}
		}

		if len(macro) > 0 {
			pack.MacroFacts = append(pack.MacroFacts, macro...)
			for _, f := range macro {
				pack.Sources[f.Source] = true
			}
		}
	}

	// Collect Ticker data
	for _, t := range tickers {
		t = strings.ToUpper(t)
		for _, prov := range s.providers {
			if !prov.Available() {
				continue
			}

			// Check if provider supports this domain
			supported := false
			for _, d := range prov.Domains() {
				if d == domain {
					supported = true
					break
				}
			}
			if !supported {
				continue
			}

			var data TickerData
			found := false
			if s.cache != nil {
				found, _ = s.cache.Get(prov.Source(), prov.Name(), domain, t, &data)
			}

			if !found {
				var err error
				data, err = prov.Fetch(ctx, domain, t)
				if err != nil {
					pack.Errors = append(pack.Errors, fmt.Sprintf("%s %s/%s: %v", prov.Name(), domain, t, err))
				}
				if err == nil && len(data.Facts) > 0 && s.cache != nil {
					s.cache.Set(prov.Source(), prov.Name(), domain, t, data)
				}
			}

			if len(data.Facts) > 0 {
				pack.ByTicker[t] = data
				pack.Coverage[t] = true
				for _, f := range data.Facts {
					pack.Sources[f.Source] = true
				}
				break // Found data for this ticker/domain, stop looking
			}
		}
	}

	return pack
}
