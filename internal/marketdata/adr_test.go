package marketdata

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestUSLineResolvesKnownADRs(t *testing.T) {
	cases := map[string]string{
		"2330.TW": "TSM",
		"9988.HK": "BABA",
		"RACE.MI": "RACE",
		"7203.T":  "TM",
		"asml.as": "ASML", // lookup is case-insensitive
	}
	for local, want := range cases {
		got, ok := USLine(local)
		if !ok {
			t.Errorf("USLine(%s): no mapping", local)
			continue
		}
		if got != want {
			t.Errorf("USLine(%s) = %s, want %s", local, got, want)
		}
	}
}

func TestUSLineLeavesUnmappedNamesAlone(t *testing.T) {
	// Samsung and SK Hynix have no US listing at all; inventing one would
	// attach another company's news to them.
	for _, local := range []string{"005930.KS", "000660.KS", "2317.TW", "PTT.BK"} {
		if line, ok := USLine(local); ok {
			t.Errorf("USLine(%s) = %s, want no mapping — it has no major-exchange US line", local, line)
		}
	}
	// A US ticker is already its own line; the map is for foreign symbols.
	if _, ok := USLine("AAPL"); ok {
		t.Error("USLine(AAPL): a US listing needs no ADR mapping")
	}
}

// Every mapped symbol must be a real universe constituent, or the map is
// carrying entries that can never fire and were never audited.
func TestADRMapMatchesTheUniverse(t *testing.T) {
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
	for local := range adrMap {
		if !known[local] {
			t.Errorf("ADR map has %s, which is not in any universe file", local)
		}
	}
	// Every mapped US line must itself be a plain US symbol, or the mapping
	// hands the provider a name it will reject for the same reason.
	for local, line := range adrMap {
		if !IsUSListing(line) {
			t.Errorf("%s maps to %s, which is not a US listing", local, line)
		}
	}
}

// AlphaVantage rejects a dotted foreign symbol outright ("Invalid ticker
// format: 000660.KS"), so a shortlist with six non-US names got zero news for
// half of itself. Four of those six trade a liquid US line.
func TestAlphaVantageFollowsTheUSLine(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sym := r.URL.Query().Get("tickers")
		if r.URL.Query().Get("function") == "EARNINGS_CALENDAR" {
			w.Write([]byte("symbol,name,reportDate,fiscalDateEnding,estimate,currency\n"))
			return
		}
		asked = append(asked, sym)
		json.NewEncoder(w).Encode(map[string]any{
			"feed": []map[string]any{{
				"title": "TSMC lifts capex", "url": "https://example.com/a",
				"time_published": "20260828T143000", "source": "Example",
				"source_domain": "example.com",
				"ticker_sentiment": []map[string]string{
					{"ticker": sym, "relevance_score": "0.9", "ticker_sentiment_score": "0.4", "ticker_sentiment_label": "Bullish"},
				},
			}},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_AV_BASE", srv.URL)

	// AV no longer gets isSubjectRelevant's "≤3 symbols" free pass (fix round
	// 1), so the fixture's "TSMC" headline needs the curated-alias lookup a
	// live run wires from universe.AliasesFor (2330.TW's own alias is
	// "TSMC" — its legal name is never spelled out in a headline).
	ctx := WithCompanyNames(context.Background(), func(string) []string { return []string{"TSMC"} })
	p := NewAlphaVantageProvider("key", "")
	td, err := p.Fetch(ctx, "news", "2330.TW")
	if err != nil {
		t.Fatalf("Fetch 2330.TW: %v", err)
	}
	if len(asked) != 1 || asked[0] != "TSM" {
		t.Fatalf("asked AlphaVantage for %v, want [TSM]", asked)
	}
	// Facts stay keyed to the ticker the run asked about.
	if td.Ticker != "2330.TW" {
		t.Errorf("TickerData.Ticker = %s, want the requested symbol", td.Ticker)
	}
	// …and say plainly which listing they describe, so the agent does not read
	// them as the local line's own coverage.
	joined := fmt.Sprintf("%+v", td.Facts)
	if !strings.Contains(joined, "TSM") {
		t.Errorf("facts do not name the US line they came from: %s", joined)
	}
}

// A foreign name with no US line must still be skipped without spending a
// request from the 25/day budget.
func TestAlphaVantageStillSkipsUnmappedForeignNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("provider issued a request for an unmapped foreign symbol: %s", r.URL)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_AV_BASE", srv.URL)

	_, err := NewAlphaVantageProvider("key", "").Fetch(context.Background(), "news", "000660.KS")
	if err == nil {
		t.Fatal("expected a skip for a name with no US line")
	}
	if !isNotApplicable(err) {
		t.Errorf("err = %v, want a structural skip", err)
	}
}

// Coverage bookkeeping has to agree with the fetcher: a name the provider can
// now reach is no longer "quant-only".
func TestUSLineWidensReachableCoverage(t *testing.T) {
	if !Reachable("2330.TW") {
		t.Error("2330.TW is reachable through TSM")
	}
	if Reachable("000660.KS") {
		t.Error("000660.KS has no US line and is not reachable")
	}
	if !Reachable("AAPL") {
		t.Error("a US listing is always reachable")
	}
}

// Chunghwa Telecom trades a live NYSE ADR (168 sessions in 2026, last 2026-09-02,
// 17 Benzinga headlines in the trailing year) and the map did not carry it. It
// was found by matching every uncovered universe name against Alpaca's active
// US-equity registry rather than by recalling ADR symbols, which is the only way
// this stays honest.
func TestUSLineCoversChunghwaTelecom(t *testing.T) {
	got, ok := USLine("2412.TW")
	if !ok {
		t.Fatal("2412.TW has a live NYSE line (CHT) and must be mapped")
	}
	if got != "CHT" {
		t.Errorf("USLine(2412.TW) = %s, want CHT", got)
	}
}

// Telefonica delisted its NYSE ADR — last bar 2026-01-16 — and filed to suspend
// its SEC reporting obligations on 2026-01-20. The symbol is gone from Alpaca's
// active asset registry.
//
// A stale row is worse than no row. Reachable() would keep reporting TEF.MC as
// covered, so it would never be counted as structurally quant-only, while EDGAR
// served whatever pre-deregistration filings remain as though they were current.
// That is the failure the map's own doc comment warns about — a mapping that
// makes the pipeline count a name as grounded when it is not.
func TestDelistedUSLinesAreNotMapped(t *testing.T) {
	if line, ok := USLine("TEF.MC"); ok {
		t.Errorf("USLine(TEF.MC) = %s, but that ADR delisted on 2026-01-16 and the issuer deregistered", line)
	}
	if Reachable("TEF.MC") {
		t.Error("TEF.MC must read as unreachable, not as covered by a dead symbol")
	}
}

// The venue column is the audit record: every row was checked against Alpaca's
// active US-equity asset registry, which is what caught TEF. A row whose venue
// is not a major exchange is documentation, not a mapping — the loader must not
// resolve it.
func TestADRMapVenuesAreAudited(t *testing.T) {
	f, err := os.Open("data/adr_map.csv")
	if err != nil {
		t.Fatalf("open ADR map: %v", err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("parse ADR map: %v", err)
	}
	if len(rows) == 0 || len(rows[0]) < 4 || rows[0][2] != "venue" {
		t.Fatalf("ADR map header = %v, want a third column named venue", rows[0])
	}
	major := map[string]bool{"NYSE": true, "NASDAQ": true}
	mappedVenues := 0
	for _, rec := range rows[1:] {
		local, line, venue := rec[0], rec[1], rec[2]
		_, resolved := USLine(local)
		if major[venue] {
			mappedVenues++
			if !resolved {
				t.Errorf("%s is venued %s but does not resolve", local, venue)
			}
			continue
		}
		if resolved {
			t.Errorf("%s (venue %s) resolves to %s; only a major-exchange line may", local, venue, line)
		}
	}
	if mappedVenues == 0 {
		t.Error("no row carries a major-exchange venue")
	}
}

// Section 16 exempts foreign private issuers, so "no Form 4 filings in 45 days"
// is a statement about US filing law, not about insider behaviour. The old test
// was the *requested* listing, so it fired for ASML.AS but not for ASML — the
// same issuer, reached through the symbol the provider actually queries.
//
// The map's us_line column is exactly a list of audited foreign issuers trading
// US lines, so it serves as the FPI registry the old comment said did not exist.
func TestForeignPrivateIssuerCoversResolvedUSLines(t *testing.T) {
	for _, sym := range []string{"ASML.AS", "ASML", "TSM", "BABA", "SONY", "005930.KS"} {
		if !isForeignPrivateIssuer(sym) {
			t.Errorf("isForeignPrivateIssuer(%s) = false; it is a foreign issuer exempt from Section 16", sym)
		}
	}
	for _, sym := range []string{"AAPL", "MSFT", "BRK.B", "JPM"} {
		if isForeignPrivateIssuer(sym) {
			t.Errorf("isForeignPrivateIssuer(%s) = true; a US domestic issuer does file Form 4s", sym)
		}
	}
}
