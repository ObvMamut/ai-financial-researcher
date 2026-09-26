package universe

import (
	_ "embed"
	"encoding/csv"
	"strings"
)

//go:embed data/aliases.csv
var aliasesRaw string

// aliases maps a ticker to the extra brand/common names its headlines
// actually use, beyond the CSV name column marketdata.WithCompanyNames also
// carries. A legal name's own suffix-stripped form (normalizeCompanyName,
// internal/marketdata/newsfilter.go) still can't reach "Google" for
// GOOGL/GOOG ("Alphabet"), "TSMC" for 2330.TW ("Taiwan Semiconductor
// Manufacturing", a phrase headlines never spell out), "Toyota" for 7203.T
// ("Toyota Motor Corporation" normalizes to "Toyota Motor", not the bare
// brand name), "Samsung" for 005930.KS ("Samsung Electronics", same gap), or
// "Meta"/"Facebook" for META ("Meta Platforms").
//
// Deliberately short: an entry is added only where the legal name
// demonstrably fails to match how financial headlines refer to the company —
// not preemptively for every ticker. Several equally well-known cases
// (Tencent, Inditex, Uniqlo, Foxconn) are already reachable without an entry
// here — Tencent's "Holdings" suffix strips to the bare brand name on its
// own, and Inditex/Uniqlo/Foxconn are parenthesised inside their own CSV name
// ("Fast Retailing Co. Ltd. (Uniqlo)") and extracted by normalizeCompanyName
// itself. See docs/research/2026-09-25-news-relevance.md for the full
// reasoning and the residual cases (numeric-root foreign tickers whose
// common Western acronym isn't listed here, e.g. NTT for 9432.T, LVMH for
// MC.PA) left for a future, equally conservative addition.
var aliases = loadAliases()

func loadAliases() map[string][]string {
	out := map[string][]string{}
	records, err := csv.NewReader(strings.NewReader(aliasesRaw)).ReadAll()
	if err != nil {
		return out
	}
	for i, rec := range records {
		if i == 0 || len(rec) < 2 { // header
			continue
		}
		ticker := strings.ToUpper(strings.TrimSpace(rec[0]))
		alias := strings.TrimSpace(rec[1])
		if ticker == "" || alias == "" {
			continue
		}
		out[ticker] = append(out[ticker], alias)
	}
	return out
}

// AliasesFor returns the extra brand/common names configured for a ticker, or
// nil. Fed into marketdata.WithCompanyNames alongside the universe's own
// name, so news-relevance matching (isSubjectRelevant) tries every one of
// them the same way it tries the CSV name.
func AliasesFor(ticker string) []string {
	return aliases[strings.ToUpper(strings.TrimSpace(ticker))]
}
