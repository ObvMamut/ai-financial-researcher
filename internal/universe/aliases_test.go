package universe

import "testing"

// TestAliasTickersExistInTheUniverse catches the same class of slip
// TestConstituentFieldsAreInTheRightColumns guards for the index files
// themselves: an aliases.csv row for a ticker that was renamed, delisted, or
// simply mistyped would silently feed a needle nobody's news ever gets
// checked against — isSubjectRelevant (internal/marketdata/newsfilter.go)
// only calls AliasesFor for a ticker the universe already produced.
func TestAliasTickersExistInTheUniverse(t *testing.T) {
	u, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for ticker, names := range aliases {
		if _, found := u.Lookup(ticker); !found {
			t.Errorf("aliases.csv: %s (alias %v) is not in any universe file", ticker, names)
		}
	}
}
