package marketdata

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DataPack is a collection of verified facts for a set of tickers.
type DataPack struct {
	Domain     string
	ByTicker   map[string]TickerData
	MacroFacts []Fact
	Coverage   map[string]bool // ticker -> true if grounded; false entries are requested-but-missing
	Sources    map[string]bool
	// Citable is the set of domains this pack legitimately vouches for: the
	// publisher of every headline it carries and the endpoint of every provider
	// that contributed. On a search-less engine it is the *only* set an agent may
	// cite from, and the orchestrator enforces that against the written report.
	Citable map[string]bool
	Errors  []string // provider failures encountered while building the pack

	// EventDates maps a ticker to its next verified scheduled binary event
	// (currently earnings). It is derived from the same fact the prompt renders,
	// so the date the model reads and the date the validator gates on cannot
	// disagree. Absent means no verified date — never "no event".
	EventDates map[string]time.Time `json:"event_dates,omitempty"`
}

func NewDataPack(domain string) *DataPack {
	return &DataPack{
		Domain:     domain,
		ByTicker:   make(map[string]TickerData),
		Coverage:   make(map[string]bool),
		Sources:    make(map[string]bool),
		Citable:    make(map[string]bool),
		EventDates: make(map[string]time.Time),
	}
}

// addCitable records the host of a URL (or a bare domain) as citable. Values
// that are not domains — provider labels like "FRED" — are ignored.
func (p *DataPack) addCitable(raw string) {
	if d := NormalizeDomain(raw); d != "" {
		p.Citable[d] = true
	}
}

// NormalizeDomain reduces a URL or bare host to a comparable domain: lowercase,
// no scheme, no path, no "www." prefix. It returns "" for anything that is not
// domain-shaped, so provider labels ("SEC EDGAR") never widen the citable set.
// Exported because citation checking compares an agent's [source:] tags against
// the pack's Citable set and must normalise both sides identically.
func NormalizeDomain(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.TrimPrefix(s, "www.")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "@"); i >= 0 { // strip userinfo
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 { // strip port
		s = s[:i]
	}
	if s == "" || strings.ContainsAny(s, " \t") || !strings.Contains(s, ".") {
		return ""
	}
	return s
}

// URLs returns every primary-source link the pack carries, sorted. These are the
// only links an agent on a search-less engine can honestly quote.
func (p *DataPack) URLs() []string {
	seen := make(map[string]bool)
	for _, td := range p.ByTicker {
		for _, f := range td.Facts {
			if f.URL != "" {
				seen[f.URL] = true
			}
		}
	}
	for _, f := range p.MacroFacts {
		if f.URL != "" {
			seen[f.URL] = true
		}
	}
	out := make([]string, 0, len(seen))
	for u := range seen {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// IsRegimeDomain reports whether a domain's evidence is market-wide rather than
// per-ticker. Macro is the only one: the 10-year yield describes every name on
// the shortlist or none of them, so it is never "missing for AAPL".
func IsRegimeDomain(domain string) bool { return domain == "macro" }

// IsUSListing reports whether a ticker can be reached by this pipeline's
// per-ticker providers at all. SEC EDGAR and AlphaVantage are both US-only, so a
// foreign-suffixed symbol is not a fetch failure — it is structurally
// uncoverable, and the shortlist carries ~6 such names on an all-indices run.
func IsUSListing(ticker string) bool { return !isForeignListing(ticker) }

// Ungrounded lists the requested tickers the pack found no data for, sorted.
// A regime domain has no per-ticker gap to report, so it returns nil: macro's
// Coverage map is seeded all-false and never written true, and reporting that
// as a gap told the macro specialist its four real FRED series covered nobody.
func (p *DataPack) Ungrounded() []string {
	if IsRegimeDomain(p.Domain) {
		return nil
	}
	var out []string
	for t, ok := range p.Coverage {
		if !ok {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// MacroMarkdown renders just the regime indicators. The quant role's data block
// is the computed metrics pack, which used to *replace* the provider pack's
// markdown wholesale — so quant was the only specialist that lost the macro
// backdrop every other role received.
func (p *DataPack) MacroMarkdown() string {
	if len(p.MacroFacts) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("#### Macro Indicators\n")
	for _, f := range p.MacroFacts {
		sb.WriteString(fmt.Sprintf("- **%s**: %s (as of %s, source: %s)%s\n",
			f.Label, f.Value, f.AsOf.Format("2006-01-02"), f.Source, urlSuffix(f.URL)))
	}
	return sb.String()
}

func (p *DataPack) Markdown() string {
	ungrounded := p.Ungrounded()
	// A regime pack always renders: with facts it states the regime, without
	// them it must say so at the domain level (see the regime branch below).
	if !IsRegimeDomain(p.Domain) && len(p.ByTicker) == 0 && len(p.MacroFacts) == 0 && len(ungrounded) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("### Verified Market Data\n\n")

	if len(p.MacroFacts) > 0 {
		sb.WriteString("#### Macro Indicators\n")
		for _, f := range p.MacroFacts {
			sb.WriteString(fmt.Sprintf("- **%s**: %s (as of %s, source: %s)%s\n",
				f.Label, f.Value, f.AsOf.Format("2006-01-02"), f.Source, urlSuffix(f.URL)))
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
			sb.WriteString(fmt.Sprintf("- **%s**: %s (as of %s, source: %s)%s\n",
				f.Label, f.Value, f.AsOf.Format("2006-01-02"), f.Source, urlSuffix(f.URL)))
		}
		sb.WriteString("\n")
	}

	// A regime domain's gap is domain-level, never per-ticker. Rendering it as a
	// ticker list is what made the macro specialist score all twelve names
	// strength 1 while its prompt carried four real FRED series.
	if IsRegimeDomain(p.Domain) {
		if len(p.MacroFacts) == 0 {
			sb.WriteString("#### No verified macro data\n\n")
			sb.WriteString("This run fetched **no** verified macro series at all — no rates, spreads, inflation or employment figures. Say so, keep your conviction low, and record the gap at the run level. Do not substitute recollection or inference for the missing figures.\n\n")
		}
		return sb.String()
	}

	// State the gaps explicitly. Without this the block renders a confident
	// "Verified Market Data" heading and says nothing about the tickers it holds
	// no data for — which is precisely the silence agents fill by inventing.
	if len(ungrounded) > 0 {
		// Separate the names no provider here can ever reach from the ones a
		// fetch genuinely failed for. Listing them together read as three
		// domains failing when half the shortlist was simply out of scope.
		var us, foreign []string
		for _, t := range ungrounded {
			if Reachable(t) {
				us = append(us, t)
			} else {
				foreign = append(foreign, t)
			}
		}
		sb.WriteString("#### No verified data for\n\n")
		if len(us) > 0 {
			sb.WriteString(strings.Join(us, ", "))
			sb.WriteString("\n\n")
		}
		if len(foreign) > 0 {
			sb.WriteString(strings.Join(foreign, ", "))
			sb.WriteString(" (non-US listings with no US line — no US filings or news coverage, and no ADR to read instead). This is a known limit of this run's data sources, not a fetch failure: they are graded on quant alone.\n\n")
		}
		sb.WriteString("These tickers have **no** verified ")
		sb.WriteString(p.Domain)
		sb.WriteString(" data in this run. Say so, score them low, and list every one of them in your `missing` array. Do not substitute recollection or inference for the missing figures.\n\n")
	}

	return sb.String()
}

// urlSuffix renders a fact's primary-source link, which is what makes the fact
// citable by an agent that cannot browse.
func urlSuffix(u string) string {
	if u == "" {
		return ""
	}
	return " — " + u
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
			if err != nil && !errors.Is(err, ErrNotApplicable) {
				pack.Errors = append(pack.Errors, fmt.Sprintf("%s macro: %v", prov.Name(), err))
			}
			if err == nil && len(macro) > 0 && s.cache != nil {
				s.cache.Set(prov.Source(), prov.Name(), "MacroFetch", "GLOBAL", macro)
			}
		}

		if len(macro) > 0 {
			pack.MacroFacts = append(pack.MacroFacts, macro...)
			pack.addCitable(prov.Source())
			for _, f := range macro {
				pack.Sources[f.Source] = true
				pack.addCitable(f.URL)
				pack.addCitable(f.Source)
			}
		}
	}

	// Seed every requested ticker as ungrounded. Only ever writing Coverage[t]
	// = true made gaps invisible: a pack that fetched nothing looked identical
	// to one that was never asked, and the prompt said nothing about the names
	// it had no data for.
	for _, t := range tickers {
		pack.Coverage[strings.ToUpper(t)] = false
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

			// Several request domains may resolve to one upstream call (see
			// CacheDomainer); cache under the shared key so we fetch once.
			cacheDomain := domain
			if cd, ok := prov.(CacheDomainer); ok {
				cacheDomain = cd.CacheDomain(domain)
			}

			var data TickerData
			found := false
			if s.cache != nil {
				found, _ = s.cache.Get(prov.Source(), prov.Name(), cacheDomain, t, &data)
			}

			if !found {
				var err error
				data, err = prov.Fetch(ctx, domain, t)
				if err != nil && !errors.Is(err, ErrNotApplicable) {
					pack.Errors = append(pack.Errors, fmt.Sprintf("%s %s/%s: %v", prov.Name(), domain, t, err))
				}
				if err == nil && len(data.Facts) > 0 && s.cache != nil {
					s.cache.Set(prov.Source(), prov.Name(), cacheDomain, t, data)
				}
			}

			// A provider that withheld a figure has to say so where the run's
			// reader will see it, whether or not it also returned usable facts.
			for _, w := range data.Warnings {
				pack.Errors = append(pack.Errors, fmt.Sprintf("%s %s/%s: %s", prov.Name(), domain, t, w))
			}

			// Merge rather than stop at the first provider that answers. The
			// old break meant a domain served by two sources only ever saw one
			// of them: insider filings and option positioning are different
			// evidence about the same question, and taking whichever replied
			// first would have made the sentiment domain a coin toss between
			// them.
			if len(data.Facts) > 0 {
				merged := pack.ByTicker[t]
				merged.Ticker = t
				merged.Facts = append(merged.Facts, data.Facts...)
				pack.ByTicker[t] = merged
				pack.Coverage[t] = true
				recordEventDate(pack, t, data.Facts)
				pack.addCitable(prov.Source())
				for _, f := range data.Facts {
					pack.Sources[f.Source] = true
					pack.addCitable(f.URL)
					pack.addCitable(f.Source)
				}
			}
		}
	}

	return pack
}

// recordEventDate lifts a verified earnings date out of the facts into the
// pack's typed map. It reads the same fact the prompt renders rather than a
// parallel channel, so the risk checks and the model are never looking at two
// different dates.
func recordEventDate(p *DataPack, ticker string, facts []Fact) {
	for _, f := range facts {
		if f.Label != EarningsFactLabel {
			continue
		}
		if d, err := time.Parse("2006-01-02", strings.TrimSpace(f.Value)); err == nil {
			p.EventDates[strings.ToUpper(ticker)] = d
		}
		return
	}
}
