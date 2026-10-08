package marketdata

import (
	"context"
	"testing"
)

func TestTickerRootStripsOnlyAKnownForeignSuffix(t *testing.T) {
	cases := map[string]string{
		"SAP.DE":  "SAP",
		"BMW.DE":  "BMW",
		"ASML.AS": "ASML",
		"AAPL":    "AAPL",
		// A US share class dot is not a foreign exchange suffix and stays put.
		"BRK.B": "BRK.B",
	}
	for ticker, want := range cases {
		if got := tickerRoot(ticker); got != want {
			t.Errorf("tickerRoot(%q) = %q, want %q", ticker, got, want)
		}
	}
}

func TestNormalizeCompanyNameStripsCorporateSuffixes(t *testing.T) {
	cases := []struct {
		name, wantNormalized, wantAlias string
	}{
		{"SAP SE", "SAP", ""},
		{"ASML Holding N.V.", "ASML", ""},
		{"Apple Inc.", "Apple", ""},
		{"ON Semiconductor Corporation", "ON Semiconductor", ""},
		{"Alphabet Inc. Class A", "Alphabet", ""},
		{"Berkshire Hathaway Inc. Class B", "Berkshire Hathaway", ""},
		// A name with nothing to strip is returned unchanged.
		{"Netflix", "Netflix", ""},

		// Fix-round regressions: a trailing "&" or dangling "and" left after
		// the suffix loop stripped "Co."/"Inc."/"Company" made these
		// unmatchable against any real headline (see mentionsText's word
		// boundary — a needle ending in "&" or "and" can never close on a
		// word boundary against a space).
		{"Merck & Co. Inc.", "Merck", ""},
		{"JPMorgan Chase & Co.", "JPMorgan Chase", ""},
		{"Eli Lilly and Company", "Eli Lilly", ""},
		{"Deere & Company", "Deere", ""},
		{"Wells Fargo & Company", "Wells Fargo", ""},
		{"Henkel AG & Co. KGaA", "Henkel", ""}, // also exercises the new KGaA suffix
		// "&"/"and" in the *middle* of a real name is not a suffix remnant
		// and must survive.
		{"Procter & Gamble Co.", "Procter & Gamble", ""},
		{"Air Products and Chemicals Inc.", "Air Products and Chemicals", ""},
		{"Nippon Telegraph and Telephone", "Nippon Telegraph and Telephone", ""},
		{"S&P Global Inc.", "S&P Global", ""}, // "&" with no surrounding spaces is one token, untouched

		// A leading "The" is part of the legal name, never of how a headline
		// refers to the company.
		{"The Trade Desk Inc.", "Trade Desk", ""},
		{"The Home Depot Inc.", "Home Depot", ""},
		{"The Coca-Cola Company", "Coca-Cola", ""},
		{"The Walt Disney Company", "Walt Disney", ""},
		{"The Boeing Company", "Boeing", ""},
		{"The Goldman Sachs Group Inc.", "Goldman Sachs", ""},

		// The new SpA suffix (Italian eu50 names).
		{"Intesa Sanpaolo SpA", "Intesa Sanpaolo", ""},
		{"Eni SpA", "Eni", ""},

		// A parenthetical alias is removed from the needle and returned
		// separately, rather than left inline where a headline never repeats
		// it verbatim.
		{"Industria de Diseño Textil SA (Inditex)", "Industria de Diseño Textil", "Inditex"},
		{"Fast Retailing Co. Ltd. (Uniqlo)", "Fast Retailing", "Uniqlo"},
		{"Hon Hai Precision Industry (Foxconn)", "Hon Hai Precision Industry", "Foxconn"},
	}
	for _, c := range cases {
		gotNormalized, gotAlias := normalizeCompanyName(c.name)
		if gotNormalized != c.wantNormalized || gotAlias != c.wantAlias {
			t.Errorf("normalizeCompanyName(%q) = (%q, %q), want (%q, %q)",
				c.name, gotNormalized, gotAlias, c.wantNormalized, c.wantAlias)
		}
	}
}

// TestIsSubjectRelevantRequiresExactCaseForAShortRoot guards ON Semiconductor
// (ticker "ON", nq100.csv) against the false positive the controller flagged:
// a plain case-insensitive word match on a one- or two-letter root would tag
// "chipmakers rally on strong demand" to every trade with a short ticker, so a
// short root only counts spelled the way a ticker actually is.
//
// Every case below uses a symbols list of four tickers so the ≤3-symbols
// shortcut in isSubjectRelevant never fires — the point here is exercising
// mentionsSymbol's case rule specifically, not the list-length rule.
func TestIsSubjectRelevantRequiresExactCaseForAShortRoot(t *testing.T) {
	symbols := []string{"ON", "AMD", "NVDA", "INTC"}
	ctx := context.Background()

	if isSubjectRelevant(ctx, symbols, "Chipmakers rally on strong demand outlook", "", "ON") {
		t.Error("a lowercase word match on a two-letter root counted as a mention of ON Semiconductor")
	}
	if !isSubjectRelevant(ctx, symbols, "ON Semiconductor beats on revenue", "", "ON") {
		t.Error("an exact-case mention of a short root did not count as coverage")
	}
	if !isSubjectRelevant(ctx, symbols, "$ON rallies after guidance raise", "", "ON") {
		t.Error("a cashtag mention of a short root did not count as coverage")
	}
}

// TestIsSubjectRelevantMatchesCompanyNameWhenTheRootNeverAppears covers the
// third route into coverage: a headline that never spells out the ticker but
// does name the company. Alphabet/GOOGL is a real universe row whose ticker
// and name share no text, which isolates this path from a root match.
func TestIsSubjectRelevantMatchesCompanyNameWhenTheRootNeverAppears(t *testing.T) {
	symbols := []string{"GOOGL", "MSFT", "AMZN", "META"}
	ctx := WithCompanyNames(context.Background(), func(ticker string) []string {
		if ticker == "GOOGL" {
			return []string{"Alphabet Inc. Class A"}
		}
		return nil
	})
	if !isSubjectRelevant(ctx, symbols, "Alphabet unveils new data-center chip", "", "GOOGL") {
		t.Error("a headline naming the company, with the root itself absent, did not count as coverage")
	}
	// Without the lookup wired (every pre-existing caller, and every
	// collection path that predates this feature), the same headline must not
	// silently start matching on the ticker text alone.
	if isSubjectRelevant(context.Background(), symbols, "Alphabet unveils new data-center chip", "", "GOOGL") {
		t.Error("company-name matching fired without a lookup in ctx")
	}
}

// TestIsSubjectRelevantRequiresTagMembershipFirst is the regression the
// existing TestYahooNewsRefusesAFeedThatTagsNothingToTheTicker fixture
// documents at the provider level: a short tag list is only informative once
// the ticker is actually one of the tags. Without that gate, an oil story
// tagged to a handful of unrelated symbols would count as coverage for
// whichever ticker happened to be fetched, since a short list alone would
// satisfy the ≤3 rule.
func TestIsSubjectRelevantRequiresTagMembershipFirst(t *testing.T) {
	if isSubjectRelevant(context.Background(), []string{"XOM", "CVX"}, "Oil edges down as investors weigh uncertainty", "", "O39.SI") {
		t.Error("an unrelated short tag list counted as coverage for a ticker it never named")
	}
}

// TestIsSubjectRelevantMatchesRealHeadlinesNormalizeCompanyNameOnceMissed is a
// fix-round regression test for three genuine headlines from the SAP.DE run
// (runs/2026-09-24T12-58-48/data/news.json) that isSubjectRelevant wrongly
// rejected before this fix: normalizeCompanyName produced needles no headline
// could ever match — a trailing "&" left after stripping "Co. Inc." from
// "Merck & Co. Inc." (MRK), and a leading "The" left in "The Trade Desk Inc."
// (TTD). Both are fixed by normalizeCompanyName itself, so each fixture below
// deliberately carries more than three symbols to keep the ≤3-symbols
// shortcut from masking whether the text-matching fix actually did the work.
//
// REGN is different: "Regeneron Pharmaceuticals Inc." normalizes to
// "Regeneron Pharmaceuticals", a two-word phrase that never appears in this
// particular headline (it only says "Regeneron"). That is not a
// normalizeCompanyName bug — the controller ruling explicitly rejects
// matching on a bare leading word of a multi-word name, since it invites too
// many false positives ("Regeneron" is distinctive, but the same rule
// applied elsewhere would not be). REGN's headline is genuine coverage
// anyway, through the ≤3-symbols branch: a single-company analyst note like
// this one plausibly carries only its own ticker. news.json does not
// preserve the real symbols array, so this reconstruction is exactly that —
// plausible, not verified — which is why it stays a residual limitation in
// docs/research/2026-09-25-news-relevance.md rather than a closed case.
func TestIsSubjectRelevantMatchesRealHeadlinesNormalizeCompanyNameOnceMissed(t *testing.T) {
	mrkCtx := WithCompanyNames(context.Background(), func(ticker string) []string {
		if ticker == "MRK" {
			return []string{"Merck & Co. Inc."}
		}
		return nil
	})
	if !isSubjectRelevant(mrkCtx, []string{"MRK", "MRNA", "PFE", "NVS"},
		"Merck Advances European Regulatory Approval For Keytruda Combination In Bladder Cancer",
		"European committee backs Merck's Keytruda + Padcev combo for bladder cancer after Phase 3 trial slashes risk of recurrence by 47%.",
		"MRK") {
		t.Error("Merck & Co. Inc.'s trailing '&' after suffix-stripping made this genuine Merck headline unmatchable")
	}

	ttdCtx := WithCompanyNames(context.Background(), func(ticker string) []string {
		if ticker == "TTD" {
			return []string{"The Trade Desk Inc."}
		}
		return nil
	})
	if !isSubjectRelevant(ttdCtx, []string{"TTD", "APP", "LULU", "MGNI"},
		"Trade Desk Plans 15% Job Cut, Expects Up to $51 Million In Charges",
		"Trade Desk shares fall as the company plans to reduce its workforce by approximately 15% as part of an organizational restructuring.",
		"TTD") {
		t.Error("The Trade Desk Inc.'s leading 'The' made this genuine Trade Desk headline unmatchable")
	}

	regnCtx := WithCompanyNames(context.Background(), func(ticker string) []string {
		if ticker == "REGN" {
			return []string{"Regeneron Pharmaceuticals Inc."}
		}
		return nil
	})
	if !isSubjectRelevant(regnCtx, []string{"REGN"},
		"Regeneron Highlights EYLEA HD Momentum, Dupixent Growth and Pipeline Catalysts",
		"", "REGN") {
		t.Error("a single-company Regeneron analyst note (≤3 symbols) did not count as coverage")
	}
}

// TestIsSubjectRelevantMatchesACuratedAlias covers aliases.csv end to end:
// GOOGL's CSV name normalizes to "Alphabet", which a headline that only says
// "Google" cannot match — this is exactly why the alias exists.
// internal/universe wires it into the same WithCompanyNames lookup as the
// primary name (see orchestrator.go); this test wires it the same way by
// hand, without importing internal/universe, to keep this package's tests
// free of the import-cycle constraint isSubjectRelevant itself works around.
func TestIsSubjectRelevantMatchesACuratedAlias(t *testing.T) {
	symbols := []string{"GOOGL", "MSFT", "AMZN", "META"}
	ctx := WithCompanyNames(context.Background(), func(ticker string) []string {
		if ticker == "GOOGL" {
			return []string{"Alphabet Inc. Class A", "Google"}
		}
		return nil
	})
	if !isSubjectRelevant(ctx, symbols, "Google unveils new data-center chip", "", "GOOGL") {
		t.Error("the curated alias 'Google' did not count as coverage for GOOGL")
	}
}

// TestIsSubjectRelevantRequiresExactCaseForAWordLikeRoot is the final branch
// review's probe: a market wrap tagging seven symbols, three of them spelled
// like ordinary words. Matched case-insensitively, each wrap sentence counted
// as coverage of NOW, COST or LOW — the F3 defect again, through a word
// rather than a tag list. A ticker is only ever written in capitals, so it
// only counts spelled that way.
func TestIsSubjectRelevantRequiresExactCaseForAWordLikeRoot(t *testing.T) {
	symbols := []string{"NOW", "COST", "LOW", "AAPL", "MSFT", "NVDA", "AMZN"}
	ctx := context.Background()
	for _, c := range []struct{ ticker, headline string }{
		{"NOW", "Stocks now higher as Treasury yields ease"},
		{"COST", "Tariffs raise the input cost for retailers, economists say"},
		{"LOW", "Market wrap: record low volatility as the S&P 500 drifts higher"},
	} {
		if isSubjectRelevant(ctx, symbols, c.headline, "", c.ticker) {
			t.Errorf("%s: a lowercase word in a wrap counted as coverage: %q", c.ticker, c.headline)
		}
	}
	for _, headline := range []string{
		"ServiceNow (NOW) shares jump after subscription revenue beat",
		"$NOW rallies into the close",
	} {
		if !isSubjectRelevant(ctx, symbols, headline, "", "NOW") {
			t.Errorf("an exact-case ticker mention did not count as coverage: %q", headline)
		}
	}
}

// TestIsSubjectRelevantReadsANameAsAProperNoun covers the rule company names
// and aliases.csv rows match under: any case except the first letter. "Meta"
// is an alias and an ordinary prefix; the capital is what makes it the name.
func TestIsSubjectRelevantReadsANameAsAProperNoun(t *testing.T) {
	symbols := []string{"META", "GOOGL", "MSFT", "AMZN"}
	ctx := WithCompanyNames(context.Background(), func(ticker string) []string {
		if ticker == "META" {
			return []string{"Meta Platforms Inc.", "Meta", "Facebook"}
		}
		return nil
	})
	if isSubjectRelevant(ctx, symbols, "Tech stocks slip as a new meta-analysis questions AI productivity gains", "", "META") {
		t.Error("a lowercase 'meta-analysis' counted as coverage of Meta")
	}
	for _, headline := range []string{
		"Meta unveils a new open-weights model",
		"FACEBOOK PARENT SHARES CLIMB", // an all-caps headline still names it
	} {
		if !isSubjectRelevant(ctx, symbols, headline, "", "META") {
			t.Errorf("a capitalised name or alias did not count as coverage: %q", headline)
		}
	}
}

// R6 (2026-10-07 probe): Yahoo tagged 19 of 20 Volkswagen stories VOW.DE and
// none VOW3.DE, the sample's line. A sibling tag satisfies the tag check; the
// story must still name the company.
func TestIsSubjectRelevantAcceptsAShareClassSiblingTag(t *testing.T) {
	ctx := WithCompanyNames(context.Background(), func(ticker string) []string {
		if ticker == "VOW3.DE" || ticker == "VOW.DE" {
			return []string{"Volkswagen AG"}
		}
		return nil
	})
	tags := []string{"VOW.DE", "BMW.DE", "MBG.DE", "PAH3.DE"}
	if !isSubjectRelevant(ctx, tags, "Volkswagen cuts its outlook as China sales slide", "", "VOW3.DE") {
		t.Error("a Volkswagen story tagged to the ordinary line was not counted for the preference line")
	}
	// The sibling widens only the tag check: a four-tag story that never names
	// the company still does not count.
	if isSubjectRelevant(ctx, tags, "German carmakers slip on tariff fears", "", "VOW3.DE") {
		t.Error("a sector story was counted on a sibling tag alone")
	}
	// One-directional: the ordinary line gains nothing from a preference tag.
	if isSubjectRelevant(ctx, []string{"VOW3.DE", "BMW.DE", "MBG.DE", "PAH3.DE"}, "Volkswagen cuts its outlook", "", "VOW.DE") {
		t.Error("the sibling table was read in reverse")
	}
}
