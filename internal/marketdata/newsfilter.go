package marketdata

import (
	"fmt"
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

// headlineFacts renders the articles the prompt will see, newest first, or
// nothing plus a warning when not one of them is about this company.
//
// Whether the feed tagged an item with this ticker is the only relevance signal
// either source offers, and a mix of tagged and untagged items is printed rather
// than filtered: an untagged item about the sector is real context, as long as an
// agent can tell it from coverage of the company itself.
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
