package marketdata

import (
	_ "embed"
	"encoding/csv"
	"strings"
)

//go:embed data/adr_map.csv
var adrMapRaw string

// adrMap maps a foreign primary listing to the US symbol that trades the same
// company, so a US-only provider can still reach it.
//
// Half of every all-indices shortlist is non-US by construction, and AlphaVantage
// rejects a dotted foreign symbol outright ("Invalid ticker format: 000660.KS").
// That made news, sentiment and fundamentals structurally blind to six of twelve
// names — but four of those six trade a liquid US line, and TSM's news is TSMC's
// news.
//
// Only major-exchange (NYSE/NASDAQ) lines are listed. The OTC pink-sheet ADRs
// that cover most of the rest — ENLAY, TCEHY, BASFY, SFTBY and so on — are
// deliberately excluded: their news coverage is thin and intermittent, and a
// mapping that returns two stale headlines is worse than an honest gap, because
// the pipeline would then count the name as grounded.
//
// Names with no US line at all (Samsung, SK Hynix, TCS, most of the .BK and .SI
// rows) stay uncovered. That is a real limit of free US-only sources, and the
// run reports it as such rather than inventing a proxy.
var adrMap = loadADRMap()

// adrNote maps a foreign listing to a human-readable description of its US line,
// which the pack prints beside the facts so an agent knows which listing it is
// reading about.
var adrNote = map[string]string{}

func loadADRMap() map[string]string {
	out := map[string]string{}
	records, err := csv.NewReader(strings.NewReader(adrMapRaw)).ReadAll()
	if err != nil {
		return out
	}
	for i, rec := range records {
		if i == 0 || len(rec) < 2 { // header
			continue
		}
		local := strings.ToUpper(strings.TrimSpace(rec[0]))
		line := strings.ToUpper(strings.TrimSpace(rec[1]))
		if local == "" || line == "" {
			continue
		}
		out[local] = line
		if len(rec) >= 3 {
			adrNote[local] = strings.TrimSpace(rec[2])
		}
	}
	return out
}

// USLine returns the US symbol that trades the same company as a foreign
// listing. It reports false for a US listing (which needs no mapping) and for a
// foreign listing with no major-exchange US line.
func USLine(ticker string) (string, bool) {
	if IsUSListing(ticker) {
		return "", false
	}
	line, ok := adrMap[strings.ToUpper(strings.TrimSpace(ticker))]
	return line, ok
}

// USLineNote describes the US line a foreign listing resolves to, for printing
// beside facts fetched under that symbol.
func USLineNote(ticker string) string {
	return adrNote[strings.ToUpper(strings.TrimSpace(ticker))]
}

// Reachable reports whether this pipeline's US-only per-ticker providers can
// fetch anything for a ticker at all — directly, or through its US line.
// Coverage bookkeeping uses it so a name the fetcher can now reach stops being
// reported as structurally quant-only.
func Reachable(ticker string) bool {
	if IsUSListing(ticker) {
		return true
	}
	_, ok := USLine(ticker)
	return ok
}

// providerSymbol is the symbol to actually request for a ticker: itself for a US
// listing, its US line for a mapped foreign one.
func providerSymbol(ticker string) (string, bool) {
	if IsUSListing(ticker) {
		return ticker, true
	}
	return USLine(ticker)
}

// IsForeignSuffix reports whether a bare exchange suffix ("TW", "DE") belongs to
// a market this pipeline's US-only providers do not cover. It is the single
// source of truth for the question: the universe package's cross-listing dedupe
// asks it too, so a symbol the providers reject is always a symbol the dedupe
// recognises as foreign.
func IsForeignSuffix(suffix string) bool {
	return foreignExchanges[strings.ToUpper(strings.TrimSpace(suffix))]
}
