package marketdata

import (
	_ "embed"
	"encoding/csv"
	"strings"
)

//go:embed data/share_siblings.csv
var shareSiblingsRaw string

// shareSiblings maps a listing to the other share classes or lines of the same
// company that a feed may tag instead (VOW3.DE's stories are tagged VOW.DE).
// One-directional, and read only by isSubjectRelevant's tag check: a sibling
// tag still needs the story to be about the company. Only pairs a probe showed
// losing tagged stories are listed (2026-10-07; HEN3.DE and BMW.DE were probed
// and need none).
var shareSiblings = loadShareSiblings()

func loadShareSiblings() map[string][]string {
	out := map[string][]string{}
	records, err := csv.NewReader(strings.NewReader(shareSiblingsRaw)).ReadAll()
	if err != nil {
		return out
	}
	for i, rec := range records {
		if i == 0 || len(rec) < 2 {
			continue
		}
		t, s := strings.ToUpper(strings.TrimSpace(rec[0])), strings.ToUpper(strings.TrimSpace(rec[1]))
		if t != "" && s != "" && t != s {
			out[t] = append(out[t], s)
		}
	}
	return out
}

// Siblings returns a listing's share-class siblings, or nil.
func Siblings(ticker string) []string {
	return shareSiblings[strings.ToUpper(strings.TrimSpace(ticker))]
}
