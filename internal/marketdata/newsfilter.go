package marketdata

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
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

// WithCompanyNames attaches a ticker → names lookup to ctx, so a news
// provider's Fetch can test whether an item's headline or summary names the
// company, not just whether its symbol sits somewhere in a tag list. The
// lookup returns every name worth trying: the universe CSV's own column, plus
// any curated aliases for that ticker (internal/universe/data/aliases.csv) —
// "Google" for GOOGL, "TSMC" for 2330.TW, "Toyota" for 7203.T — for the
// brand/common names a legal name's own suffix-stripped form still can't
// reach.
//
// The pipeline already carries this data — the universe CSV's own name column
// and its aliases file — but marketdata cannot import the universe package to
// read it directly: universe imports marketdata for IsForeignSuffix, and
// closing the loop back the other way would be a cycle. The orchestrator
// wires the combined lookup here instead, into the same ctx BuildPack already
// threads down to every provider's Fetch, once, where it loads the universe.
//
// An unwrapped ctx — every test in this package, and any collection path that
// runs before the orchestrator does this wiring (research_capture.go's
// frozen-snapshot capture, used by `research-pair`) — degrades to no
// company-name match. That is the same behavior every run had before this
// rule existed; root and ADR-symbol matching are unaffected either way.
func WithCompanyNames(ctx context.Context, lookup func(ticker string) []string) context.Context {
	return context.WithValue(ctx, companyNameKey{}, lookup)
}

func companyNamesFor(ctx context.Context, ticker string) []string {
	lookup, _ := ctx.Value(companyNameKey{}).(func(string) []string)
	if lookup == nil {
		return nil
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
// same check. SpA (Italian) and KGaA (German) were missing until the eu50
// review found four SpA names (Intesa Sanpaolo, UniCredit, Eni, Enel) and one
// KGaA (Henkel AG & Co. KGaA) normalizing with the suffix still attached.
var companySuffixes = map[string]bool{
	"HOLDINGS": true, "HOLDING": true, "GROUP": true, "INCORPORATED": true,
	"CORPORATION": true, "COMPANY": true, "LIMITED": true, "NV": true,
	"SA": true, "AS": true, "PLC": true, "ASA": true, "CORP": true,
	"INC": true, "LTD": true, "CO": true, "AG": true, "SE": true,
	"SPA": true, "KGAA": true,
}

// normalizeCompanyName strips trailing corporate-form tokens, a leading
// "The", and a parenthetical alias, so the name matches the way a headline
// actually spells it. It is a documented approximation, not a legal-name
// parser: a suffix not on the list is left in place, which only costs a text
// match, never adds a false one.
//
// It returns the cleaned name and, separately, an alias pulled out of a
// parenthetical in the raw name — "Industria de Diseño Textil SA (Inditex)",
// "Fast Retailing Co. Ltd. (Uniqlo)", "Hon Hai Precision Industry (Foxconn)".
// A literal "(Inditex)" never appears in a headline, so leaving it in the
// needle made these three unmatchable; removing it and reusing its contents
// as a second needle is what actually grounds them.
func normalizeCompanyName(name string) (normalized string, alias string) {
	name = strings.TrimSpace(name)
	if i := strings.IndexByte(name, '('); i >= 0 {
		if j := strings.IndexByte(name[i:], ')'); j > 0 {
			alias = strings.TrimSpace(name[i+1 : i+j])
			name = strings.TrimSpace(name[:i] + name[i+j+1:])
		}
	}
	// A leading "The" is part of the legal name, not of how a headline refers
	// to the company: "The Trade Desk Inc." is "Trade Desk" the moment a
	// reporter writes about it, the same way "The Home Depot", "The
	// Coca-Cola Company", "The Walt Disney Company", "The Goldman Sachs
	// Group" and "The Boeing Company" all lose it. Left in, the needle
	// "The Trade Desk" never matches a headline that starts "Trade Desk
	// Plans...".
	if fields := strings.Fields(name); len(fields) > 1 && strings.EqualFold(fields[0], "The") {
		name = strings.Join(fields[1:], " ")
	}
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
		// "&" or "and" only ever survives to the end of the name as the
		// remnant of a corporate suffix this loop just stripped — "Merck &
		// Co. Inc." -> "Merck &", "Eli Lilly and Company" -> "Eli Lilly and",
		// "JPMorgan Chase & Co." -> "JPMorgan Chase &", "Wells Fargo &
		// Company" / "Deere & Company" the same way. A real name with "and"
		// in the *middle* ("Air Products and Chemicals", "Nippon Telegraph
		// and Telephone") never has it as the last token, so this never
		// touches those.
		if last == "&" || last == "AND" {
			name = strings.Join(fields[:len(fields)-1], " ")
			continue
		}
		if !companySuffixes[last] {
			break
		}
		name = strings.Join(fields[:len(fields)-1], " ")
	}
	return strings.TrimSpace(name), alias
}

// mentionsText reports whether needle appears in haystack as a whole word or
// phrase: the characters immediately before and after it, if any, are not
// letters or digits — so "SAP" matches "$SAP soared" and "SAP SE reported"
// but not "SAPient" or "ASAP". exact requires the spelling given, letter for
// letter (mentionsSymbol); otherwise only the needle's first letter must match
// as given and the rest may be in any case (mentionsCompanyName).
func mentionsText(haystack, needle string, exact bool) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" || haystack == "" {
		return false
	}
	pattern := regexp.QuoteMeta(needle)
	if !exact {
		_, size := utf8.DecodeRuneInString(needle)
		if rest := needle[size:]; rest != "" {
			pattern = regexp.QuoteMeta(needle[:size]) + "(?i:" + regexp.QuoteMeta(rest) + ")"
		}
	}
	re, err := regexp.Compile(`\b` + pattern + `\b`)
	if err != nil {
		return false
	}
	return re.MatchString(haystack)
}

// mentionsSymbol reports whether text names this ticker root or ADR symbol as
// its subject, spelled exactly as the ticker is. Reporters write a ticker in
// capitals — "ServiceNow (NOW)", "NASDAQ:COST", a "$LOW" cashtag — and a large
// part of the universe is spelled like an ordinary word: NOW, COST, LOW, NET,
// TEAM, SNOW, CAT, DIS, META, ON, A, T. Matched case-insensitively, a market
// wrap tagging NOW, COST and LOW said "Stocks now higher", "input cost for
// retailers" and "record low volatility" and counted as coverage of all three
// (final branch review, 2026-09-25) — the F3 defect this filter exists to
// close. mentionsText's word boundary treats a leading "$" as a boundary for
// free, so a cashtag needs no separate check.
func mentionsSymbol(text, symbol string) bool {
	if symbol == "" {
		return false
	}
	return mentionsText(text, symbol, true)
}

// mentionsCompanyName reports whether text names the company: its normalized
// legal name, or the parenthetical alias its raw name carries (see
// normalizeCompanyName), each as the proper noun a headline writes it as.
//
// A name matches in any case except its first letter, which must be spelled as
// given: "Trade desk" and "SAMSUNG" still name the company, "meta-analysis"
// and "an apple a day" do not name Meta or Apple. The capital is what makes a
// word a name, and several names and aliases here are ordinary words
// (aliases.csv's "Meta"; "Apple", "Target", "Visa" in the universe itself).
// The rule cannot tell a sentence-initial common word from the name —
// "Meta-analysis finds..." still reads as Meta — which is a residual limit
// named in docs/research/2026-09-25-news-relevance.md. A name of two letters
// or fewer must match exactly, like a ticker: none in the universe today, but
// the rule should not depend on that.
func mentionsCompanyName(text, name string) bool {
	normalized, alias := normalizeCompanyName(name)
	if mentionsName(text, normalized) {
		return true
	}
	return alias != "" && mentionsName(text, alias)
}

func mentionsName(text, name string) bool {
	if name == "" {
		return false
	}
	return mentionsText(text, name, utf8.RuneCountInString(name) <= 2)
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

// relatesToAny is relatesTo over several candidate tags.
func relatesToAny(related, tickers []string) bool {
	for _, t := range tickers {
		if relatesTo(related, t) {
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
// headline or summary names the ticker root, the ADR symbol, the company
// name or one of its aliases, or the tag list itself is short enough (≤3
// symbols) that being tagged at all is informative rather than incidental.
// Membership in the tag list is still required first — dropping it would let
// a short tag list on a completely different company's story (the
// Namibia/oil fallback TestYahooNewsRefusesAFeedThatTagsNothingToTheTicker
// guards against) count for whichever ticker happened to be fetched at the
// time. A share-class sibling (Siblings: VOW.DE for VOW3.DE, GOOG for GOOGL)
// also satisfies the tag check, since a feed may tag the company's other line
// instead; it widens nothing else, so the story must still be about the company.
func isSubjectRelevant(ctx context.Context, symbols []string, headline, summary, ticker string) bool {
	root := tickerRoot(ticker)
	adr, _ := USLine(ticker)
	if !relatesTo(symbols, ticker) && !relatesTo(symbols, root) && !relatesTo(symbols, adr) && !relatesToAny(symbols, Siblings(ticker)) {
		return false
	}
	if namesCompanyInText(ctx, headline, summary, ticker) {
		return true
	}
	return len(symbols) <= 3
}

// namesCompanyInText is isSubjectRelevant's text-matching core, factored out
// so a caller whose tag list can't be given the same "≤3 symbols" reading
// (see alphavantage.go's articlesFor) can use the real name/root/ADR match
// without also inheriting that fallback or the tag-membership gate. It reports
// whether headline or summary names the ticker root, the ADR symbol, or the
// company name or one of its aliases.
func namesCompanyInText(ctx context.Context, headline, summary, ticker string) bool {
	root := tickerRoot(ticker)
	adr, _ := USLine(ticker)
	text := headline + " " + summary
	if mentionsSymbol(text, root) {
		return true
	}
	if adr != "" && mentionsSymbol(text, adr) {
		return true
	}
	for _, name := range companyNamesFor(ctx, ticker) {
		if mentionsCompanyName(text, name) {
			return true
		}
	}
	return false
}

// IsSubjectRelevant exports isSubjectRelevant's rule for callers outside this
// package that need to re-derive coverage from facts already saved to disk,
// rather than from a live Fetch: internal/orchestrator's news-relevance
// acceptance audit (news_relevance_audit_test.go) re-tests a past run's saved
// headlines against the current rule, and it needs both this package's text
// matching and internal/universe's name/alias lookup in the same place —
// exactly the combination WithCompanyNames exists to avoid an import cycle
// over. A prior version of that audit kept a second, hand-copied implementation
// of this rule instead and warned it had to be "kept in sync"; exporting the
// real function removes that drift risk.
func IsSubjectRelevant(ctx context.Context, symbols []string, headline, summary, ticker string) bool {
	return isSubjectRelevant(ctx, symbols, headline, summary, ticker)
}

// NamesCompanyInText exports namesCompanyInText for the same reason
// IsSubjectRelevant is exported: the acceptance audit re-derives AlphaVantage
// coverage too, and AlphaVantage (articlesFor, alphavantage.go) uses this text
// match directly rather than the full isSubjectRelevant rule — it does not
// take the tag-membership gate or the "≤3 symbols" fallback, so the audit
// must not either when re-testing an AlphaVantage fact.
func NamesCompanyInText(ctx context.Context, headline, summary, ticker string) bool {
	return namesCompanyInText(ctx, headline, summary, ticker)
}

// headlineFacts renders the articles the prompt will see, newest first, or
// nothing plus a warning when not one of them is about this company.
//
// Related now asks whether the company is the item's subject — see
// isSubjectRelevant — not merely whether the feed's tag list carries its
// symbol somewhere. A mix of subject and non-subject items is still printed
// rather than filtered: an item about the sector, or one that only mentions
// this name in passing, is real context, as long as an agent can tell it from
// coverage of the company itself. The label says which is which, and says only
// what is true of both feeds: a non-subject item may be an untagged Yahoo search
// result or an Alpaca story that does carry this symbol in its tag list, so it
// is "not about this company", not "not tagged".
//
// *None* of them about the company is a different thing, and has two sources. yahoonews.go's own comment
// promised a relevance filter that was never written, and on 2026-09-03 the
// search endpoint answered five foreign listings with a canned set — the same
// eight oil, Namibia and photonics stories for a Korean chat app, a Taiwanese
// chip designer and a Singapore bank, byte-identical across all five. The news
// domain carries 0.25 of the score. Spending eight of its slots on a fallback
// payload is worse than reporting the name as uncovered, which is what it is.
// The second is F3's (isSubjectRelevant): Alpaca's per-symbol query returned
// SAP.DE four stories that all carried SAP in their tag list and were about
// AMD, NVIDIA, Micron and a European market close.
func headlineFacts(arts []newsArticle, note string) ([]Fact, string) {
	tagged := 0
	for _, a := range arts {
		if a.Related {
			tagged++
		}
	}
	if len(arts) > 0 && tagged == 0 {
		return nil, fmt.Sprintf(
			"news feed returned %d %s and not one of them is about this company%s — each was either untagged to this ticker or tagged on a story about other companies that never names it (a market wrap, a peer's note), so the feed has no coverage of this company",
			len(arts), plural(len(arts), "item", "items"), note)
	}

	var out []Fact
	for i, a := range arts {
		if i >= feedHeadlines {
			break
		}
		rel := "tagged to this ticker"
		if !a.Related {
			rel = "context, not about this company"
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
