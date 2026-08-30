package marketdata

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Form 4 is the SEC's statement of changes in beneficial ownership: an insider
// has four business days to report a trade in their own company's stock. It is
// free, keyless, dated, and it is the one positioning signal in this pipeline
// that is a record of what someone *did* rather than of what a model recalls.
//
// The sentiment domain previously ran on the same AlphaVantage NEWS_SENTIMENT
// call as the news domain, so two of five "independent" domains were reading one
// source and agreeing with each other by construction.
const (
	// form4LookbackDays bounds how far back a filing still says something about
	// current positioning. A quarter-old sale is history, not a signal.
	form4LookbackDays = 45
	// form4MaxDocs caps the filings actually downloaded per ticker. A busy
	// issuer files dozens in a window and SEC asks for 10 requests/second at
	// most; five recent filings carry the shape of the activity.
	form4MaxDocs = 5
)

// form4Transaction is one open-market line from a Form 4.
type form4Transaction struct {
	Owner  string
	Title  string
	Date   time.Time
	Shares float64
	Price  float64
	Buy    bool // acquired (code P) rather than disposed (code S)
	URL    string
}

func (t form4Transaction) value() float64 { return t.Shares * t.Price }

// submissionsResp is the slice of SEC's submissions JSON we need: parallel
// arrays, one index per filing.
type submissionsResp struct {
	Filings struct {
		Recent struct {
			AccessionNumber []string `json:"accessionNumber"`
			FilingDate      []string `json:"filingDate"`
			Form            []string `json:"form"`
			PrimaryDocument []string `json:"primaryDocument"`
		} `json:"recent"`
	} `json:"filings"`
}

// ownershipDocument is the Form 4 XML. Only the non-derivative table is read:
// derivative rows are options grants and conversions, which say more about a
// compensation schedule than about a view on the price.
type ownershipDocument struct {
	PeriodOfReport string `xml:"periodOfReport"`
	ReportingOwner []struct {
		ID struct {
			Name string `xml:"rptOwnerName"`
		} `xml:"reportingOwnerId"`
		Relationship struct {
			IsDirector   string `xml:"isDirector"`
			IsOfficer    string `xml:"isOfficer"`
			IsTenPercent string `xml:"isTenPercentOwner"`
			OfficerTitle string `xml:"officerTitle"`
		} `xml:"reportingOwnerRelationship"`
	} `xml:"reportingOwner"`
	NonDerivative []struct {
		TransactionDate struct {
			Value string `xml:"value"`
		} `xml:"transactionDate"`
		Coding struct {
			Code string `xml:"transactionCode"`
		} `xml:"transactionCoding"`
		Amounts struct {
			Shares struct {
				Value string `xml:"value"`
			} `xml:"transactionShares"`
			Price struct {
				Value string `xml:"value"`
			} `xml:"transactionPricePerShare"`
			AcquiredDisposed struct {
				Value string `xml:"value"`
			} `xml:"transactionAcquiredDisposedCode"`
		} `xml:"transactionAmounts"`
	} `xml:"nonDerivativeTable>nonDerivativeTransaction"`
}

// fetchInsiderActivity summarises recent Form 4 open-market trades for a ticker.
func (p *edgarProvider) fetchInsiderActivity(ctx context.Context, ticker string) (TickerData, error) {
	symbol, ok := providerSymbol(ticker)
	if !ok {
		return TickerData{}, fmt.Errorf("%w: %s has no SEC filer", ErrNotApplicable, ticker)
	}
	p.ensureCIKMap(ctx)
	cik, ok := p.lookupCIK(symbol)
	if !ok {
		return TickerData{}, fmt.Errorf("%w: no CIK for %s", ErrUnavailable, symbol)
	}

	filings, err := p.recentForm4s(ctx, cik)
	if err != nil {
		return TickerData{}, err
	}
	td := TickerData{Ticker: ticker}
	browse := fmt.Sprintf("%s/cgi-bin/browse-edgar?action=getcompany&CIK=%s&type=4&dateb=&owner=include&count=40",
		p.tickersBase, cik)
	if len(filings) == 0 {
		td.Facts = append(td.Facts, Fact{
			Label:  "Insider activity (SEC Form 4)",
			Value:  fmt.Sprintf("no Form 4 filings in the last %d days", form4LookbackDays),
			AsOf:   time.Now(),
			Source: "SEC EDGAR",
			URL:    browse,
		})
		return td, nil
	}

	var txns []form4Transaction
	var other int
	for _, f := range filings {
		doc, err := p.fetchForm4(ctx, cik, f.accession, f.document)
		if err != nil {
			td.Warnings = append(td.Warnings, fmt.Sprintf("Form 4 %s: %v", f.accession, err))
			continue
		}
		open, skipped := doc.openMarket(f.url)
		txns = append(txns, open...)
		other += skipped
	}
	if len(txns) == 0 && other == 0 && len(td.Warnings) > 0 {
		return td, fmt.Errorf("%w: no readable Form 4 for %s", ErrUnavailable, symbol)
	}

	var buys, sells int
	var buyUSD, sellUSD float64
	for _, t := range txns {
		if t.Buy {
			buys++
			buyUSD += t.value()
		} else {
			sells++
			sellUSD += t.value()
		}
	}

	summary := fmt.Sprintf("%d open-market %s ($%s) vs %d %s ($%s) across %d filing(s) in %d days",
		buys, plural(buys, "buy", "buys"), usd(buyUSD),
		sells, plural(sells, "sale", "sales"), usd(sellUSD),
		len(filings), form4LookbackDays)
	if other > 0 {
		// Grants, option exercises and tax withholding are compensation
		// mechanics on a schedule set months earlier. Counting them as insider
		// conviction is the standard way to read this data wrong.
		summary += fmt.Sprintf("; %d further non-open-market line(s) (grants/exercises/withholding) excluded", other)
	}
	td.Facts = append(td.Facts, Fact{
		Label:  "Insider activity (SEC Form 4)",
		Value:  summary,
		AsOf:   time.Now(),
		Source: "SEC EDGAR",
		URL:    browse,
	})

	// The largest few trades by dollar value, which is where the signal is: one
	// $20M sale by a CEO is not three $50k purchases by directors.
	sort.SliceStable(txns, func(i, j int) bool { return txns[i].value() > txns[j].value() })
	for i, t := range txns {
		if i == 3 {
			break
		}
		verb := "sold"
		if t.Buy {
			verb = "bought"
		}
		who := t.Owner
		if t.Title != "" {
			who += " (" + t.Title + ")"
		}
		td.Facts = append(td.Facts, Fact{
			Label: "Insider transaction",
			Value: fmt.Sprintf("%s %s %s sh @ %.2f = $%s", who, verb,
				humanShares(t.Shares), t.Price, usd(t.value())),
			AsOf:   t.Date,
			Source: "SEC EDGAR",
			URL:    t.URL,
		})
	}
	return td, nil
}

type form4Filing struct {
	accession string
	document  string
	url       string
	filed     time.Time
}

func (p *edgarProvider) recentForm4s(ctx context.Context, cik string) ([]form4Filing, error) {
	u := fmt.Sprintf("%s/submissions/CIK%s.json", p.factsBase, cik)
	var sub submissionsResp
	if err := p.getJSON(ctx, u, &sub); err != nil {
		return nil, err
	}
	r := sub.Filings.Recent
	cutoff := time.Now().UTC().AddDate(0, 0, -form4LookbackDays)

	var out []form4Filing
	for i := range r.Form {
		if strings.TrimSpace(r.Form[i]) != "4" {
			continue
		}
		if i >= len(r.FilingDate) || i >= len(r.AccessionNumber) || i >= len(r.PrimaryDocument) {
			continue
		}
		filed, err := time.Parse("2006-01-02", strings.TrimSpace(r.FilingDate[i]))
		if err != nil || filed.Before(cutoff) {
			continue
		}
		acc := strings.TrimSpace(r.AccessionNumber[i])
		doc := strings.TrimSpace(r.PrimaryDocument[i])
		out = append(out, form4Filing{
			accession: acc,
			document:  doc,
			url:       p.form4URL(cik, acc, doc),
			filed:     filed,
		})
		if len(out) == form4MaxDocs {
			break
		}
	}
	return out, nil
}

// form4URL builds the archive path for a filing's primary document. SEC lists
// the *rendered* document (an "xslF345X05/" prefix), which is HTML; stripping
// the prefix yields the machine-readable XML at the same location.
func (p *edgarProvider) form4URL(cik, accession, document string) string {
	if i := strings.LastIndex(document, "/"); i >= 0 && strings.HasPrefix(document, "xsl") {
		document = document[i+1:]
	}
	return fmt.Sprintf("%s/Archives/edgar/data/%s/%s/%s",
		p.tickersBase, strings.TrimLeft(cik, "0"), strings.ReplaceAll(accession, "-", ""), document)
}

func (p *edgarProvider) fetchForm4(ctx context.Context, cik, accession, document string) (*ownershipDocument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.form4URL(cik, accession, document), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", p.userAgent())
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var doc ownershipDocument
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return &doc, nil
}

// openMarket extracts the open-market purchases and sales, returning them plus
// the count of lines deliberately excluded.
func (d *ownershipDocument) openMarket(url string) ([]form4Transaction, int) {
	owner, title := d.owner()
	var out []form4Transaction
	skipped := 0
	for _, t := range d.NonDerivative {
		code := strings.ToUpper(strings.TrimSpace(t.Coding.Code))
		if code != "P" && code != "S" {
			skipped++
			continue
		}
		shares := parseFloatOrZero(t.Amounts.Shares.Value)
		price := parseFloatOrZero(t.Amounts.Price.Value)
		if shares <= 0 || price <= 0 {
			skipped++
			continue
		}
		date, _ := time.Parse("2006-01-02", strings.TrimSpace(t.TransactionDate.Value))
		if date.IsZero() {
			date, _ = time.Parse("2006-01-02", strings.TrimSpace(d.PeriodOfReport))
		}
		out = append(out, form4Transaction{
			Owner:  owner,
			Title:  title,
			Date:   date,
			Shares: shares,
			Price:  price,
			Buy:    strings.EqualFold(strings.TrimSpace(t.Amounts.AcquiredDisposed.Value), "A"),
			URL:    url,
		})
	}
	return out, skipped
}

func (d *ownershipDocument) owner() (name, title string) {
	if len(d.ReportingOwner) == 0 {
		return "an insider", ""
	}
	o := d.ReportingOwner[0]
	name = strings.TrimSpace(o.ID.Name)
	if name == "" {
		name = "an insider"
	}
	rel := o.Relationship
	switch {
	case strings.TrimSpace(rel.OfficerTitle) != "":
		title = strings.TrimSpace(rel.OfficerTitle)
	case rel.IsDirector == "1" || strings.EqualFold(rel.IsDirector, "true"):
		title = "director"
	case rel.IsTenPercent == "1" || strings.EqualFold(rel.IsTenPercent, "true"):
		title = "10% owner"
	}
	return name, title
}

// getJSON is the SEC-identified GET used by both the fundamentals and the
// insider paths.
func (p *edgarProvider) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", p.userAgent())
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d from %s", ErrUnavailable, resp.StatusCode, url)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func parseFloatOrZero(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// usd renders a dollar amount at a readable magnitude — an insider trade spans
// four orders of magnitude and "$21400000" is not a number anyone reads.
func usd(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.2fB", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.2fM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.0fk", v/1e3)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

func humanShares(s float64) string {
	if s >= 1e6 {
		return fmt.Sprintf("%.2fM", s/1e6)
	}
	return strconv.FormatFloat(s, 'f', -1, 64)
}
