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
// Only major-exchange (NYSE/NASDAQ) lines are mapped. Widening the map to the
// sponsored OTC ADRs that cover most of the rest — BAYRY, BMWYY, TOELY, SFTBY,
// TCEHY and so on — was specified, then probed against live Alpaca on 2026-09-03
// and abandoned on the evidence:
//
//   - 68 of 77 candidate OTC lines returned *zero* headlines over 21 days, and
//     the nine that returned any averaged three. A mapping is not free: Reachable
//     stops counting the name as structurally quant-only, so a row that yields no
//     news converts an honest coverage gap into a domain that scores a name on
//     nothing. That is the same silent grounding this file already warns about.
//   - OTC bars are not available to this account at all: feed=otc answers
//     HTTP 403, "subscription does not permit querying OTC data". So an OTC row
//     could not have been price-validated even where its news was adequate, and
//     an unvalidated row is what produced the TEF entry below.
//
// The venue column is the audit record for that probe, checked against Alpaca's
// active US-equity asset registry. It is what caught TEF: Telefonica's ADR
// stopped trading on 2026-01-16 and the issuer filed to suspend its SEC
// reporting obligations four days later, so the row had been quietly resolving
// to a dead symbol — EDGAR still serves its pre-deregistration filings, which is
// stale fundamentals wearing the face of current ones. A row venued anything but
// NYSE or NASDAQ stays in the file as documentation and is deliberately *not*
// loaded, so a later reader re-adding it from a stale reference trips the test
// rather than the pipeline.
//
// Names with no US line at all (Samsung, SK Hynix, TCS, most of the .BK and .SI
// rows) stay uncovered. That is a real limit of free US-only sources, and the
// run reports it as such rather than inventing a proxy.
var adrMap = loadADRMap()

// adrNote maps a foreign listing to a human-readable description of its US line,
// which the pack prints beside the facts so an agent knows which listing it is
// reading about.
var adrNote = map[string]string{}

// majorVenues are the exchanges whose listings this pipeline will resolve to.
// Everything else in the file is an audited negative result, not a mapping.
var majorVenues = map[string]bool{"NYSE": true, "NASDAQ": true}

// usLines is the set of US symbols that are ADRs of foreign issuers — the
// map's us_line column, inverted. It is the FPI registry that edgarform4.go
// used to say this pipeline did not have.
var usLines = map[string]bool{}

func loadADRMap() map[string]string {
	out := map[string]string{}
	records, err := csv.NewReader(strings.NewReader(adrMapRaw)).ReadAll()
	if err != nil {
		return out
	}
	for i, rec := range records {
		if i == 0 || len(rec) < 3 { // header
			continue
		}
		local := strings.ToUpper(strings.TrimSpace(rec[0]))
		line := strings.ToUpper(strings.TrimSpace(rec[1]))
		venue := strings.ToUpper(strings.TrimSpace(rec[2]))
		if local == "" || line == "" || !majorVenues[venue] {
			continue
		}
		out[local] = line
		usLines[line] = true
		note := venue
		if len(rec) >= 4 && strings.TrimSpace(rec[3]) != "" {
			note = venue + " — " + strings.TrimSpace(rec[3])
		}
		adrNote[local] = note
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

// isForeignPrivateIssuer reports whether a symbol belongs to an issuer exempt
// from Section 16, and so files no Form 4.
//
// A foreign suffix answers it for a local listing, but the symbol the providers
// actually query is the *resolved* US line — ASML, not ASML.AS — and that
// carries no suffix to read. ASML Holding NV is no less a foreign private issuer
// for being reached through its NASDAQ ADR; it files 20-F either way. Testing
// the requested ticker meant the exemption fired for ASML.AS and missed ASML.
//
// The registry is the ADR map's own us_line column, which is exactly a list of
// audited foreign issuers trading US lines. It covers what it covers — an FPI
// outside the universe is not in it — but the caller only consults this when a
// name returned *no* Form 4 filings at all, so a name missing from the registry
// degrades to the old behaviour rather than to a wrong one.
func isForeignPrivateIssuer(symbol string) bool {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	return !IsUSListing(symbol) || usLines[symbol]
}
