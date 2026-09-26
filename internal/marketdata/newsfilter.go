package marketdata

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The news domain carries 0.25 of the score and is served by two providers with
// the same failure mode: a feed can return plenty of items and lose every one
// of them to filtering, producing output byte-identical to a name with no news.
// The filters and the drop accounting live here so both providers report that
// the same way and neither can drift into silence on its own.
const (
	// newsMaxAge bounds how far back a headline may be and still describe the
	// flow this system trades. A two-month-old story is background.
	newsMaxAge = 21 * 24 * time.Hour
	// feedHeadlines is how many reach the prompt from a headline-only feed.
	// AlphaVantage keeps its own, lower count (newsHeadlines, alphavantage.go):
	// its items carry a per-article sentiment score, so each fact is longer and
	// six of them cost about what eight bare headlines do.
	feedHeadlines = 8
)

// newsArticle is one surviving item, in the form both feeds reduce to.
type newsArticle struct {
	Title     string
	Publisher string
	Link      string
	Published time.Time
	Related   bool
}

// companyNameKey is the context key WithCompanyNames stores its lookup
// function under.
type companyNameKey struct{}

// WithCompanyNames attaches a ticker → company name lookup to ctx, so a news
// provider's Fetch can test whether an item's headline or summary names the
// company, not just whether its symbol sits somewhere in a tag list.
//
// The pipeline already carries this name — the universe CSV's own column —
// but marketdata cannot import the universe package to read it directly:
// universe imports marketdata for IsForeignSuffix, and closing the loop back
// the other way would be a cycle. The orchestrator wires the lookup here
// instead, into the same ctx BuildPack already threads down to every
// provider's Fetch, once, where it loads the universe.
//
// An unwrapped ctx — every test in this package, and any collection path that
// runs before the orchestrator does this wiring (research_capture.go's
// frozen-snapshot capture, used by `research-pair`) — degrades to no
// company-name match. That is the same behavior every run had before this
// rule existed; root and ADR-symbol matching are unaffected either way.
func WithCompanyNames(ctx context.Context, lookup func(ticker string) string) context.Context {
	return context.WithValue(ctx, companyNameKey{}, lookup)
}

func companyNameFor(ctx context.Context, ticker string) string {
	lookup, _ := ctx.Value(companyNameKey{}).(func(string) string)
	if lookup == nil {
		return ""
	}
	return lookup(ticker)
}

// tickerRoot strips a known foreign exchange suffix, leaving a US ticker's own
// dot (a share class, "BRK.B") untouched: "SAP.DE" becomes "SAP", "BRK.B"
// stays "BRK.B". It mirrors internal/universe's own splitSuffix, which this
// package cannot call directly for the same import-direction reason
// WithCompanyNames exists.
func tickerRoot(ticker string) string {
	if i := strings.LastIndex(ticker, "."); i > 0 && IsForeignSuffix(ticker[i+1:]) {
		return ticker[:i]
	}
	return ticker
}

// companySuffixes are corporate-form words a headline drops the instant it
// names a company in prose — "SAP SE" is "SAP" to every reporter who writes
// about it. Matched with punctuation stripped, so "N.V." and "NV" are the
// same check.
var companySuffixes = map[string]bool{
	"HOLDINGS": true, "HOLDING": true, "GROUP": true, "INCORPORATED": true,
	"CORPORATION": true, "COMPANY": true, "LIMITED": true, "NV": true,
	"SA": true, "AS": true, "PLC": true, "ASA": true, "CORP": true,
	"INC": true, "LTD": true, "CO": true, "AG": true, "SE": true,
}

// normalizeCompanyName strips trailing corporate-form tokens, repeatedly —
// "ASML Holding N.V." carries two — so the name matches the way a headline
// actually spells it. It is a documented approximation, not a legal-name
// parser: a suffix not on the list is left in place, which only costs a text
// match, never adds a false one.
func normalizeCompanyName(name string) string {
	name = strings.TrimSpace(name)
	// A share-class suffix ("Class A", "Class B") sits after the legal suffix
	// in the universe CSV's own name column — "Alphabet Inc. Class A",
	// "Berkshire Hathaway Inc. Class B" — and would otherwise block the loop
	// below from ever reaching "Inc.": a headline never repeats the share
	// class, so dropping it first is what lets the rest of the name match at
	// all.
	if fields := strings.Fields(name); len(fields) >= 3 && strings.EqualFold(fields[len(fields)-2], "Class") {
		name = strings.Join(fields[:len(fields)-2], " ")
	}
	for {
		fields := strings.Fields(strings.TrimRight(name, "."))
		if len(fields) < 2 {
			break
		}
		last := strings.ToUpper(strings.ReplaceAll(fields[len(fields)-1], ".", ""))
		if !companySuffixes[last] {
			break
		}
		name = strings.Join(fields[:len(fields)-1], " ")
	}
	return strings.TrimSpace(name)
}

// mentionsText reports whether needle appears in haystack as a whole word or
// phrase: the characters immediately before and after it, if any, are not
// letters or digits — so "SAP" matches "$SAP soared" and "SAP SE reported"
// but not "SAPient" or "ASAP". caseSensitive additionally requires the exact
// spelling given; see mentionsSymbol for why a short symbol needs that.
func mentionsText(haystack, needle string, caseSensitive bool) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" || haystack == "" {
		return false
	}
	pattern := `\b` + regexp.QuoteMeta(needle) + `\b`
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(haystack)
}

// mentionsSymbol reports whether text names this ticker root or ADR symbol as
// its subject. A root of two letters or fewer ("ON", "A", "T") collides with
// ordinary words and mid-sentence abbreviations — "shares moved on Tuesday" —
// so it only counts spelled in the same case a ticker actually is: "ON
// Semiconductor" or "$ON", never "on". mentionsText's word boundary treats a
// leading "$" as a boundary for free, so a cashtag needs no separate check.
func mentionsSymbol(text, symbol string) bool {
	if symbol == "" {
		return false
	}
	return mentionsText(text, symbol, len(symbol) <= 2)
}

// mentionsCompanyName reports whether text names the company, once its legal
// suffix is normalized away.
func mentionsCompanyName(text, name string) bool {
	n := normalizeCompanyName(name)
	if n == "" {
		return false
	}
	return mentionsText(text, n, false)
}

// relatesTo reports whether a feed's tag list names this ticker at all. Both
// Alpaca (queried with symbols=<ticker>) and Yahoo (searched by symbol) filter
// or rank by it before this point, so a negative here is the rarer case — the
// point of isSubjectRelevant is that a positive here is not sufficient on its
// own.
func relatesTo(related []string, ticker string) bool {
	want := strings.ToUpper(strings.TrimSpace(ticker))
	if want == "" {
		return false
	}
	for _, r := range related {
		if strings.EqualFold(strings.TrimSpace(r), want) {
			return true
		}
	}
	return false
}

// isSubjectRelevant decides Related for one item: is the company its subject,
// or only a symbol somewhere in a tag list a market wrap or a peer's premarket
// note also carries?
//
// F3 (2026-09-24): Alpaca marked SAP.DE's news covered on four items — three
// premarket notes about AMD, NVIDIA and Micron that each name SAP once, deep
// in the body, as a software name an AI pullback could rotate into, and a
// European-market-close wrap that tags a dozen companies in one summary
// table. Alpaca's per-symbol query means all four genuinely carry "SAP" in
// their tag list, so relatesTo alone called it coverage; the news specialist
// scored SAP.DE on that, and the resulting non-empty domain_scores entry is
// what carried it past the evidence floor (checkPriceOnlyEvidence,
// riskgate.go), which only checks whether a domain scored the name at all,
// not what it read.
//
// An item counts as coverage only if the company is its subject: the
// headline or summary names the ticker root, the ADR symbol or the company
// name, or the tag list itself is short enough (≤3 symbols) that being
// tagged at all is informative rather than incidental. Membership in the tag
// list is still required first — dropping it would let a short tag list on a
// completely different company's story (the Namibia/oil fallback
// TestYahooNewsRefusesAFeedThatTagsNothingToTheTicker guards against) count
// for whichever ticker happened to be fetched at the time.
func isSubjectRelevant(ctx context.Context, symbols []string, headline, summary, ticker string) bool {
	root := tickerRoot(ticker)
	adr, _ := USLine(ticker)
	if !relatesTo(symbols, ticker) && !relatesTo(symbols, root) && !relatesTo(symbols, adr) {
		return false
	}
	text := headline + " " + summary
	if mentionsSymbol(text, root) {
		return true
	}
	if adr != "" && mentionsSymbol(text, adr) {
		return true
	}
	if mentionsCompanyName(text, companyNameFor(ctx, ticker)) {
		return true
	}
	return len(symbols) <= 3
}

// headlineFacts renders the articles the prompt will see, newest first, or
// nothing plus a warning when not one of them is about this company.
//
// Related now asks whether the company is the item's subject — see
// isSubjectRelevant — not merely whether the feed's tag list carries its
// symbol somewhere. A mix of subject and non-subject items is still printed
// rather than filtered: an item about the sector, or one that only mentions
// this name in passing, is real context, as long as an agent can tell it from
// coverage of the company itself.
//
// *None* of them tagged is a different thing. yahoonews.go's own comment
// promised a relevance filter that was never written, and on 2026-09-03 the
// search endpoint answered five foreign listings with a canned set — the same
// eight oil, Namibia and photonics stories for a Korean chat app, a Taiwanese
// chip designer and a Singapore bank, byte-identical across all five. The news
// domain carries 0.25 of the score. Spending eight of its slots on a fallback
// payload is worse than reporting the name as uncovered, which is what it is.
func headlineFacts(arts []newsArticle, note string) ([]Fact, string) {
	tagged := 0
	for _, a := range arts {
		if a.Related {
			tagged++
		}
	}
	if len(arts) > 0 && tagged == 0 {
		return nil, fmt.Sprintf(
			"news feed returned %d %s and tagged none of them to this ticker%s — a feed that knows the symbol tags at least one story to it, so this is a search fallback answering a query it could not resolve, not coverage of this company",
			len(arts), plural(len(arts), "item", "items"), note)
	}

	var out []Fact
	for i, a := range arts {
		if i >= feedHeadlines {
			break
		}
		rel := "tagged to this ticker"
		if !a.Related {
			rel = "surfaced by search, not tagged to this ticker"
		}
		publisher := a.Publisher
		if publisher == "" {
			publisher = "unattributed"
		}
		out = append(out, Fact{
			Label:  fmt.Sprintf("Headline %d (%s)", i+1, rel),
			Value:  fmt.Sprintf("%s — %s%s", a.Title, publisher, note),
			AsOf:   a.Published,
			Source: publisher,
			URL:    a.Link,
		})
	}
	return out, ""
}

// headlineSetKey identifies the exact set of headlines a ticker was served, for
// the cross-ticker check in BuildPack. Two different companies handed the same
// set in one run were not both in the news; one feed answered both with the same
// fallback.
func headlineSetKey(facts []Fact) string {
	var keys []string
	for _, f := range facts {
		if k := newsHeadlineKey(f); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x00")
}

// newsDrops counts why each returned item was discarded. The breakdown is the
// diagnosis: "20 items, 20 with no usable timestamp" is a renamed field and
// wants a code change; "20 items, 20 older than 21 days" is a genuinely stale
// name and wants nothing.
type newsDrops struct {
	noTitle     int
	noTimestamp int
	stale       int
	duplicate   int
}

// warning renders the breakdown for a feed that returned items and kept none.
func (d newsDrops) warning(returned int, timestampField string) string {
	var parts []string
	if d.noTimestamp > 0 {
		parts = append(parts, fmt.Sprintf("%d with no usable %q", d.noTimestamp, timestampField))
	}
	if d.stale > 0 {
		parts = append(parts, fmt.Sprintf("%d older than the %d-day cutoff", d.stale, int(newsMaxAge.Hours()/24)))
	}
	if d.noTitle > 0 {
		parts = append(parts, fmt.Sprintf("%d with an empty title", d.noTitle))
	}
	if d.duplicate > 0 {
		parts = append(parts, fmt.Sprintf("%d duplicate titles", d.duplicate))
	}
	if len(parts) == 0 {
		// Unreachable through the loop above, which accounts for every item it
		// discards; kept so a future filter that forgets to count still says
		// something truthful rather than an empty parenthesis.
		parts = append(parts, "no reason recorded")
	}

	msg := fmt.Sprintf("news feed returned %d %s and not one survived filtering: %s",
		returned, plural(returned, "item", "items"), strings.Join(parts, ", "))
	// A single reason accounting for the whole feed is the readable case, and
	// it is the one worth interpreting out loud at 3am.
	switch {
	case d.noTimestamp == returned:
		msg += " — a whole feed with no timestamp is that field being renamed, retyped or moved, not a quiet name"
	case d.stale == returned:
		msg += " — the feed is answering, the name is just stale; nothing to fix here"
	case d.noTitle == returned:
		msg += " — a whole feed with no title is that field being renamed, not a quiet name"
	}
	return msg
}

// newsHeadlineKey identifies a headline for cross-provider dedupe, or "" for a
// fact that is not a headline.
//
// A US name is served by both news providers, and BuildPack merges every
// provider that answers rather than stopping at the first — deliberately, since
// two sources on one domain are usually two different pieces of evidence. News
// is the exception: the same wire story reaches both feeds, and printed twice
// it reads to a specialist as two outlets corroborating each other.
//
// The key is the headline text ahead of the " — publisher" suffix
// headlineFacts appends. Two feeds that word a story differently will not match,
// which is the right failure: only an identical headline is provably the same item.
func newsHeadlineKey(f Fact) string {
	if !strings.HasPrefix(f.Label, "Headline ") {
		return ""
	}
	title := f.Value
	if i := strings.Index(title, " — "); i > 0 {
		title = title[:i]
	}
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}
