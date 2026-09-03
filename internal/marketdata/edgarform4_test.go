package marketdata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// form4XML is the ownership document SEC serves for one filing. The mix is
// deliberate: an open-market sale, an open-market purchase, and two lines that
// are compensation mechanics rather than a view on the price.
func form4XML(owner, title, date string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<ownershipDocument>
  <periodOfReport>%s</periodOfReport>
  <issuer><issuerTradingSymbol>NVDA</issuerTradingSymbol></issuer>
  <reportingOwner>
    <reportingOwnerId><rptOwnerName>%s</rptOwnerName></reportingOwnerId>
    <reportingOwnerRelationship><isOfficer>1</isOfficer><officerTitle>%s</officerTitle></reportingOwnerRelationship>
  </reportingOwner>
  <nonDerivativeTable>
    <nonDerivativeTransaction>
      <transactionDate><value>%s</value></transactionDate>
      <transactionCoding><transactionCode>S</transactionCode></transactionCoding>
      <transactionAmounts>
        <transactionShares><value>120000</value></transactionShares>
        <transactionPricePerShare><value>178.40</value></transactionPricePerShare>
        <transactionAcquiredDisposedCode><value>D</value></transactionAcquiredDisposedCode>
      </transactionAmounts>
    </nonDerivativeTransaction>
    <nonDerivativeTransaction>
      <transactionDate><value>%s</value></transactionDate>
      <transactionCoding><transactionCode>P</transactionCode></transactionCoding>
      <transactionAmounts>
        <transactionShares><value>1000</value></transactionShares>
        <transactionPricePerShare><value>175.00</value></transactionPricePerShare>
        <transactionAcquiredDisposedCode><value>A</value></transactionAcquiredDisposedCode>
      </transactionAmounts>
    </nonDerivativeTransaction>
    <nonDerivativeTransaction>
      <transactionDate><value>%s</value></transactionDate>
      <transactionCoding><transactionCode>A</transactionCode></transactionCoding>
      <transactionAmounts>
        <transactionShares><value>500000</value></transactionShares>
        <transactionPricePerShare><value>0</value></transactionPricePerShare>
        <transactionAcquiredDisposedCode><value>A</value></transactionAcquiredDisposedCode>
      </transactionAmounts>
    </nonDerivativeTransaction>
    <nonDerivativeTransaction>
      <transactionDate><value>%s</value></transactionDate>
      <transactionCoding><transactionCode>F</transactionCode></transactionCoding>
      <transactionAmounts>
        <transactionShares><value>40000</value></transactionShares>
        <transactionPricePerShare><value>178.40</value></transactionPricePerShare>
        <transactionAcquiredDisposedCode><value>D</value></transactionAcquiredDisposedCode>
      </transactionAmounts>
    </nonDerivativeTransaction>
  </nonDerivativeTable>
</ownershipDocument>`, date, owner, title, date, date, date, date)
}

// serveSEC answers the submissions index and the Form 4 archive paths.
func serveSEC(t *testing.T, forms []string, dates []string) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/submissions/"):
			var forms4, filed, acc, doc []string
			for i := range forms {
				forms4 = append(forms4, `"`+forms[i]+`"`)
				filed = append(filed, `"`+dates[i]+`"`)
				acc = append(acc, fmt.Sprintf(`"0001045810-26-%06d"`, i))
				doc = append(doc, fmt.Sprintf(`"xslF345X05/wk-form4_%d.xml"`, i))
			}
			fmt.Fprintf(w, `{"cik":"1045810","filings":{"recent":{
				"accessionNumber":[%s],"filingDate":[%s],"form":[%s],"primaryDocument":[%s]}}}`,
				strings.Join(acc, ","), strings.Join(filed, ","),
				strings.Join(forms4, ","), strings.Join(doc, ","))
		case strings.Contains(r.URL.Path, "/Archives/"):
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, form4XML("HUANG JEN HSUN", "President and CEO", dates[0]))
		case strings.HasPrefix(r.URL.Path, "/files/"):
			fmt.Fprint(w, `{"0":{"cik_str":1045810,"ticker":"NVDA","title":"NVIDIA"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func TestForm4CountsOnlyOpenMarketTrades(t *testing.T) {
	// A grant and a tax withholding are on a schedule set months earlier.
	// Counting them as insider conviction is the standard way to misread this
	// data — the 500k-share award would swamp every real trade in the file.
	recent := time.Now().AddDate(0, 0, -5).Format("2006-01-02")
	srv, _ := serveSEC(t, []string{"4"}, []string{recent})
	t.Setenv("CFR_SEC_BASE", srv.URL)

	p := NewEdgarProvider("test@example.com", nil)
	td, err := p.Fetch(context.Background(), "sentiment", "NVDA")
	if err != nil {
		t.Fatalf("Fetch sentiment: %v", err)
	}

	joined := fmt.Sprintf("%+v", td.Facts)
	if !strings.Contains(joined, "1 open-market buy") || !strings.Contains(joined, "1 sale") {
		t.Errorf("open-market counts wrong:\n%s", joined)
	}
	// 120,000 × 178.40 = $21.4M sold against 1,000 × 175 = $175k bought.
	if !strings.Contains(joined, "21.41M") || !strings.Contains(joined, "175k") {
		t.Errorf("dollar totals wrong:\n%s", joined)
	}
	if !strings.Contains(joined, "non-open-market") {
		t.Errorf("excluded compensation lines must be disclosed, not silently dropped:\n%s", joined)
	}
	if !strings.Contains(joined, "HUANG JEN HSUN (President and CEO)") {
		t.Errorf("the largest trade should name who made it:\n%s", joined)
	}
	// Every fact must be checkable against a real document.
	for _, f := range td.Facts {
		if f.URL == "" {
			t.Errorf("fact %q carries no primary-source URL", f.Label)
		}
	}
}

func TestForm4IgnoresFilingsOutsideTheWindow(t *testing.T) {
	old := time.Now().AddDate(0, 0, -120).Format("2006-01-02")
	srv, _ := serveSEC(t, []string{"4", "10-Q"}, []string{old, old})
	t.Setenv("CFR_SEC_BASE", srv.URL)

	td, err := NewEdgarProvider("test@example.com", nil).
		Fetch(context.Background(), "sentiment", "NVDA")
	if err != nil {
		t.Fatalf("Fetch sentiment: %v", err)
	}
	joined := fmt.Sprintf("%+v", td.Facts)
	if !strings.Contains(joined, "no Form 4 filings") {
		t.Errorf("a quarter-old filing is history, not positioning:\n%s", joined)
	}
}

func TestForm4FetchesTheXMLNotTheRenderedHTML(t *testing.T) {
	// SEC lists the rendered document under an "xslF345X05/" prefix. Fetching
	// that returns HTML, which parses as an empty ownership document — the
	// filing is there and reads as no activity at all.
	recent := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	srv, paths := serveSEC(t, []string{"4"}, []string{recent})
	t.Setenv("CFR_SEC_BASE", srv.URL)

	if _, err := NewEdgarProvider("test@example.com", nil).
		Fetch(context.Background(), "sentiment", "NVDA"); err != nil {
		t.Fatalf("Fetch sentiment: %v", err)
	}
	for _, path := range *paths {
		if strings.Contains(path, "xslF345X05") {
			t.Errorf("fetched the rendered HTML instead of the XML: %s", path)
		}
	}
}

// A foreign private issuer is exempt from Section 16: it files 20-F, not Form 4.
// "No Form 4 filings in the last 45 days" is then a statement about US filing
// law rendered as a statement about insider behaviour — and the computed leg
// reads it as "no signal", which is reassurance this source never offered.
// ASML.AS reached the 2026-09-01 sentiment pack with exactly that.
//
// The exemption covers Form 4 and nothing else, so the whale legs still run: a
// 13D can be filed against any Section 12 class and an ADR sits in tracked
// managers' 13F tables like anything else. What the issuer must not get is an
// insider verdict.
func TestForm4IsInapplicableToAForeignPrivateIssuer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/submissions/"):
			// ASML files with the SEC — it just never files a Form 4.
			fmt.Fprint(w, `{"cik":"937966","filings":{"recent":{
				"accessionNumber":["0000937966-26-000001"],"filingDate":["2026-08-30"],
				"form":["20-F"],"primaryDocument":["asml-20f.htm"]}}}`)
		case strings.HasPrefix(r.URL.Path, "/files/"):
			fmt.Fprint(w, `{"0":{"cik_str":937966,"ticker":"ASML","title":"ASML Holding NV"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_SEC_BASE", srv.URL)

	td, err := NewEdgarProvider("test@example.com", nil).
		Fetch(context.Background(), "sentiment", "ASML.AS")
	if err != nil {
		t.Fatalf("the whale legs are not Section 16 evidence and should still run: %v", err)
	}
	for _, f := range td.Facts {
		if strings.Contains(f.Value, "no Form 4 filings") {
			t.Errorf("the false reassurance survived: %q", f.Value)
		}
		if f.Label == InsiderSignalLabel || f.Label == InsiderActivityLabel {
			t.Errorf("an exempt issuer got an insider leg: %q = %q", f.Label, f.Value)
		}
	}
	// The exemption is reported as what it is: a fact about US filing law, kept
	// out of the facts the agent scores from.
	if len(td.Warnings) == 0 || !strings.Contains(td.Warnings[0], "foreign private issuer") {
		t.Errorf("the exemption was not recorded: %v", td.Warnings)
	}
	// And every leg that did run abstained, so the name is an abstention rather
	// than a coverage gap.
	if HasPositioningSignal(td) {
		t.Error("a name with nothing but empty filings claimed a directional verdict")
	}
	if !hasLabel(td, PlannedSalesSignalLabel) || !hasLabel(td, ActivistStakeSignalLabel) {
		t.Errorf("the whale legs did not run for an exempt issuer: %+v", td.Facts)
	}
}

func hasLabel(td TickerData, label string) bool {
	for _, f := range td.Facts {
		if f.Label == label {
			return true
		}
	}
	return false
}

func TestForm4SkipsNamesWithNoSECFiler(t *testing.T) {
	srv, _ := serveSEC(t, []string{"4"}, []string{time.Now().Format("2006-01-02")})
	t.Setenv("CFR_SEC_BASE", srv.URL)

	_, err := NewEdgarProvider("test@example.com", nil).
		Fetch(context.Background(), "sentiment", "MC.PA")
	if err == nil {
		t.Fatal("a French listing has no Form 4; want an error")
	}
	if !strings.Contains(err.Error(), "not covered by this provider") {
		t.Errorf("a structurally uncoverable name is not a failure, got %v", err)
	}
}

// The same exemption, reached the other way. ASML is in nq100 as the bare US
// symbol and in eu50 as ASML.AS, so one company enters the pipeline under two
// spellings — and the guard above only recognised the dotted one. An nq100 run
// therefore handed ASML the "no Form 4 filings in the last 45 days" line and the
// no-signal verdict derived from it, which is the precise false reassurance the
// dotted spelling is protected from.
//
// It is the resolved symbol the provider queries, so it is the resolved symbol
// the exemption has to be decided on.
func TestForm4ExemptionFollowsTheIssuerNotTheSpelling(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/submissions/"):
			fmt.Fprint(w, `{"cik":"937966","filings":{"recent":{
				"accessionNumber":["0000937966-26-000001"],"filingDate":["2026-08-30"],
				"form":["20-F"],"primaryDocument":["asml-20f.htm"]}}}`)
		case strings.HasPrefix(r.URL.Path, "/files/"):
			fmt.Fprint(w, `{"0":{"cik_str":937966,"ticker":"ASML","title":"ASML Holding NV"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_SEC_BASE", srv.URL)

	td, _ := NewEdgarProvider("test@example.com", nil).
		Fetch(context.Background(), "sentiment", "ASML")
	for _, f := range td.Facts {
		if strings.Contains(f.Value, "no Form 4 filings") {
			t.Errorf("ASML reached under its US symbol kept the false reassurance: %q", f.Value)
		}
		if f.Label == InsiderSignalLabel || f.Label == InsiderActivityLabel {
			t.Errorf("an exempt issuer got an insider leg: %q = %q", f.Label, f.Value)
		}
	}
	if len(td.Warnings) == 0 || !strings.Contains(td.Warnings[0], "foreign private issuer") {
		t.Errorf("the exemption was not recorded: %v", td.Warnings)
	}
	// A US domestic issuer must be unaffected — it really does file Form 4s, so
	// "none in 45 days" is a genuine observation about its insiders.
	if isForeignPrivateIssuer("AAPL") {
		t.Error("the widened test swept up a domestic issuer")
	}
}
