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
	cases := map[string]string{
		"SAP SE":                          "SAP",
		"ASML Holding N.V.":               "ASML",
		"Apple Inc.":                      "Apple",
		"ON Semiconductor Corporation":    "ON Semiconductor",
		"Alphabet Inc. Class A":           "Alphabet",
		"Berkshire Hathaway Inc. Class B": "Berkshire Hathaway",
		// A name with nothing to strip is returned unchanged.
		"Netflix": "Netflix",
	}
	for name, want := range cases {
		if got := normalizeCompanyName(name); got != want {
			t.Errorf("normalizeCompanyName(%q) = %q, want %q", name, got, want)
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
	ctx := WithCompanyNames(context.Background(), func(ticker string) string {
		if ticker == "GOOGL" {
			return "Alphabet Inc. Class A"
		}
		return ""
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
