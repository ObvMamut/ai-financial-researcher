package marketdata

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"unicode"
)

// TestNormalizedNamesAreWellFormed is the table test the fix-round review
// asked for "over the real universe (universe.Load or equivalent)".
// marketdata cannot import internal/universe — universe already imports
// marketdata for IsForeignSuffix, and closing the loop the other way would
// cycle — so this reads the same four CSV files directly by path instead,
// which is the "or equivalent" the ask allowed for.
//
// It exists because normalizeCompanyName had already shipped needles no
// headline could ever match: a trailing "&" ("Merck & Co. Inc." -> "Merck
// &"), a dangling "and" ("Eli Lilly and Company" -> "Eli Lilly and"), a
// leading "The" a headline drops ("The Trade Desk Inc." -> "The Trade
// Desk"), and a parenthetical alias left inside the needle ("... (Inditex)").
// Every one of those breaks the identical way: the normalized string no
// longer starts or ends on a real word, so mentionsText's \b can never bound
// it against ordinary prose. This is the invariant, checked over every name
// (and every alias) this pipeline actually carries, not just the handful of
// cases a reviewer happened to notice.
func TestNormalizedNamesAreWellFormed(t *testing.T) {
	for _, path := range []string{
		"../universe/data/sp500.csv",
		"../universe/data/nq100.csv",
		"../universe/data/eu50.csv",
		"../universe/data/asia100.csv",
	} {
		for _, name := range readUniverseColumnForTest(t, path, 1) {
			normalized, alias := normalizeCompanyName(name)
			assertWellFormedNeedle(t, path, name, "normalized name", normalized)
			if alias != "" {
				assertWellFormedNeedle(t, path, name, "parenthetical alias", alias)
			}
		}
	}
	for _, alias := range readUniverseColumnForTest(t, "../universe/data/aliases.csv", 1) {
		assertWellFormedNeedle(t, "../universe/data/aliases.csv", alias, "aliases.csv row", alias)
	}
}

// assertWellFormedNeedle checks the property mentionsText's word-boundary
// regex actually depends on: a needle that doesn't begin and end on a letter
// or digit can never be bounded against real prose (a trailing "&" is
// followed by a space in any real headline, which is a non-word/non-word
// transition — not a boundary — so the whole match fails silently).
func assertWellFormedNeedle(t *testing.T, path, rawName, kind, needle string) {
	t.Helper()
	runes := []rune(needle)
	if len(runes) == 0 {
		t.Errorf("%s: %q produced an empty %s", path, rawName, kind)
		return
	}
	isWordRune := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	if !isWordRune(runes[0]) || !isWordRune(runes[len(runes)-1]) {
		t.Errorf("%s: %q's %s %q does not begin and end on a word character — it can never be word-bounded against a headline",
			path, rawName, kind, needle)
	}
}

// readUniverseColumnForTest mirrors internal/universe's own parseCSV closely
// enough for this test's purpose: skip blank/comment lines and the header,
// split on comma, return one column.
func readUniverseColumnForTest(t *testing.T, path string, column int) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var values []string
	scanner := bufio.NewScanner(f)
	first := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if first {
			first = false
			if strings.HasPrefix(strings.ToLower(line), "ticker") {
				continue
			}
		}
		parts := strings.SplitN(line, ",", 6)
		if len(parts) <= column {
			continue
		}
		values = append(values, strings.TrimSpace(parts[column]))
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	if len(values) == 0 {
		t.Fatalf("%s: no rows read — the relative-path assumption (test CWD = package dir) may be wrong", path)
	}
	return values
}
