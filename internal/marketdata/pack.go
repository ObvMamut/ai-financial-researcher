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

// USLineFactLabel marks facts fetched under a foreign listing's US line, so a
// reader knows which listing the coverage below describes.
const USLineFactLabel = "US line"

// HasDomainEvidence reports whether a ticker's facts include evidence that
// belongs to this domain, rather than only the context every domain is handed.
//
// The news domain draws on two AlphaVantage calls that fail independently: the
// bulk earnings calendar, which is one request for the whole run, and the
// per-ticker headline feed. Coverage flipped true on any fact at all, so when
// the free key's 25-request daily budget ran out mid-run the calendar fact
// alone still marked the name covered. On 2026-09-01 2330.TW was recorded as
// grounded for news carrying exactly one fact — a date — with every headline
// lost to rate limiting, and it therefore never appeared in coverageGaps. The
// starvation was invisible to the run's own verdict, which is the one place it
// had to be visible.
//
// A date is a fact about the calendar, not a read on the flow. It still reaches
// the prompt and still gates the trade; it just cannot claim the domain was
// covered on its own.
func HasDomainEvidence(domain string, td TickerData) bool {
	for _, f := range td.Facts {
		if domain == "news" && (f.Label == EarningsFactLabel || f.Label == USLineFactLabel) {
			continue
		}
		return true
	}
	return false
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
			f.Label, f.Value, f.AsOf.UTC().Format("2006-01-02"), f.Source, urlSuffix(f.URL)))
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
				f.Label, f.Value, f.AsOf.UTC().Format("2006-01-02"), f.Source, urlSuffix(f.URL)))
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
				f.Label, f.Value, f.AsOf.UTC().Format("2006-01-02"), f.Source, urlSuffix(f.URL)))
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

// Every "as of" above renders in UTC, because that is the zone the risk gate
// registers the same fact under (collectVerifiedDates does `t.UTC().Format`).
// Rendering machine-local instead meant that on a machine at +02:00 — which this
// one is — any fact collected between 22:00 and 24:00 UTC was *shown* to the
// model as one date and *verified* as the day before, so quoting it truthfully
// scored as an invention: 10 confidence points and the run's single corrective
// re-prompt. The date shown and the date registered have to be the same date.

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

			var data TickerData
			found := false
			if s.cache != nil {
				found, _ = s.cache.Get(prov.Source(), prov.Name(), domain, t, &data)
			}

			if !found {
				var err error
				data, err = prov.Fetch(ctx, domain, t)
				if err != nil && !errors.Is(err, ErrNotApplicable) {
					pack.Errors = append(pack.Errors, fmt.Sprintf("%s %s/%s: %v", prov.Name(), domain, t, err))
				}
				if err == nil && len(data.Facts) > 0 && s.cache != nil {
					s.cache.Set(prov.Source(), prov.Name(), domain, t, data)
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
				// News is the one domain where two providers carry the *same*
				// evidence rather than different evidence: the same wire story
				// reaches both feeds for a US name. Printed twice it reads as
				// two outlets corroborating each other, so an exact repeat of a
				// headline already merged is dropped. See newsHeadlineKey.
				seenHeadlines := map[string]bool{}
				for _, f := range merged.Facts {
					if k := newsHeadlineKey(f); k != "" {
						seenHeadlines[k] = true
					}
				}
				for _, f := range data.Facts {
					if k := newsHeadlineKey(f); k != "" {
						if seenHeadlines[k] {
							continue
						}
						seenHeadlines[k] = true
					}
					merged.Facts = append(merged.Facts, f)
				}
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

	// The combined positioning verdict is formed last, in its own pass over the
	// whole pack. The insider and options legs come from different providers, so
	// it needs both merged — and the options leg is judged against the run's own
	// cross-section of put/call ratios, so it needs every chain fetched. Doing it
	// inside the per-ticker loop meant the first name was classified against a
	// cross-section of one.
	peerMedian := PutCallMedian(pack.ByTicker)
	for t := range pack.ByTicker {
		td := pack.ByTicker[t]
		addPositioningSignal(&td, peerMedian)
		pack.ByTicker[t] = td
	}

	flagSharedHeadlineSets(pack)

	return pack
}

// flagSharedHeadlineSets records the one relevance failure a single ticker
// cannot see: two different companies handed exactly the same headlines.
//
// Per-ticker, a set of stories with a plausible date and a real publisher is
// indistinguishable from coverage. Across a run it is not — Kakao and a
// Singapore bank were served the same eight oil and photonics stories on
// 2026-09-03, which is a search endpoint answering an unresolvable query from a
// default set. The zero-tagged rule in headlineFacts catches most of these; this
// catches the ones a feed does tag, and it costs one pass over facts already in
// memory.
func flagSharedHeadlineSets(p *DataPack) {
	if p.Domain != "news" || len(p.ByTicker) < 2 {
		return
	}
	byKey := map[string][]string{}
	for t, td := range p.ByTicker {
		if k := headlineSetKey(td.Facts); k != "" {
			byKey[k] = append(byKey[k], t)
		}
	}
	for _, tickers := range byKey {
		if len(tickers) < 2 {
			continue
		}
		sort.Strings(tickers)
		p.Errors = append(p.Errors, fmt.Sprintf(
			"news: %s were served an identical set of headlines — different companies are not in the news for the same stories word for word, so this is one feed answering every one of them from the same fallback",
			strings.Join(tickers, ", ")))
	}
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
