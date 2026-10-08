package marketdata

import (
	"bufio"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestSiblingsListsTheEvidenceBackedPairsOnly(t *testing.T) {
	cases := map[string][]string{
		"VOW3.DE": {"VOW.DE"},
		"3988.HK": {"601988.SS"},
		"2318.HK": {"601318.SS", "82318.HK"},
		"GOOGL":   {"GOOG"},
		"googl ":  {"GOOG"}, // the lookup normalises like USLine does
	}
	for ticker, want := range cases {
		if got := Siblings(ticker); !reflect.DeepEqual(got, want) {
			t.Errorf("Siblings(%q) = %q, want %q", ticker, got, want)
		}
	}
	// One-directional: only the listed listing gains siblings. HEN3.DE and
	// BMW.DE were probed and need none (R6).
	for _, ticker := range []string{"VOW.DE", "GOOG", "601318.SS", "HEN3.DE", "BMW.DE", "AAPL"} {
		if got := Siblings(ticker); got != nil {
			t.Errorf("Siblings(%q) = %q, want nil", ticker, got)
		}
	}
}

// Every listing in the sibling table is a universe constituent, and no row maps
// a listing to itself or repeats.
func TestShareSiblingsMatchTheUniverse(t *testing.T) {
	known := map[string]bool{}
	for _, path := range []string{"sp500", "nq100", "eu50", "asia100"} {
		f, err := os.Open("../universe/data/" + path + ".csv")
		if err != nil {
			t.Fatalf("open universe CSV: %v", err)
		}
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			known[strings.ToUpper(strings.SplitN(line, ",", 2)[0])] = true
		}
		f.Close()
	}
	if len(shareSiblings) == 0 {
		t.Fatal("the sibling table loaded empty")
	}
	for ticker, sibs := range shareSiblings {
		if !known[ticker] {
			t.Errorf("sibling table has %s, which is not in any universe file", ticker)
		}
		seen := map[string]bool{}
		for _, s := range sibs {
			if s == ticker {
				t.Errorf("%s lists itself as a sibling", ticker)
			}
			if seen[s] {
				t.Errorf("%s lists sibling %s twice", ticker, s)
			}
			seen[s] = true
		}
	}
}
