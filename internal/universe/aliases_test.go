package universe

import "testing"

// TestAliasTickersExistInTheUniverse catches the same class of slip
// TestConstituentFieldsAreInTheRightColumns guards for the index files
// themselves: an aliases.csv row for a ticker that was renamed, delisted, or
// simply mistyped would silently feed a needle nobody's news ever gets
// checked against — isSubjectRelevant (internal/marketdata/newsfilter.go)
// only calls AliasesFor for a ticker the universe already produced.
// R6 (2026-10-07 probe): headlines say "Ping An" and "BMW". Only 1 of 20 BMW
// headlines spelled out "Bayerische Motoren Werke", and without "Ping An" the
// 2318.HK sibling tags (601318.SS, 82318.HK) never meet a headline that names it.
func TestAliasesCoverBMWAndPingAn(t *testing.T) {
	for ticker, want := range map[string]string{"BMW.DE": "BMW", "2318.HK": "Ping An"} {
		got := AliasesFor(ticker)
		if len(got) == 0 || got[0] != want {
			t.Errorf("AliasesFor(%s) = %q, want %q first: it becomes the Yahoo name query", ticker, got, want)
		}
	}
}

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
