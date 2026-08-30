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
