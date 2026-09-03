package marketdata

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Form 144 is the notice of *proposed* sale of restricted or control securities.
// An insider files it before selling, which makes it the only forward-looking
// item in this pipeline's positioning evidence — Form 4 says what was done, a
// 144 says what is about to be.
//
// It is also, overwhelmingly, a schedule. The Apple 144s from 2026 are RSUs
// vesting into a 10b5-1 plan adopted three months earlier; so are most large-cap
// 144s. Reading those as a bearish view would put the same standing levy on
// every long that the Form 4 classifier was rewritten to remove, and for the
// same reason: it would be scoring the compensation calendar.
//
// The two discriminators are in the filing. `planAdoptionDate` is present when
// the sale is executing a plan set before the seller could have known anything
// current, and `natureOfAcquisitionTransaction` says whether the stock being
// sold is vesting equity passing through or a position being reduced. A notice
// with neither mark is a discretionary decision to sell, and that is the only
// kind this leg reads a direction from.

// form144Notice is one proposed sale.
type form144Notice struct {
	Owner        string
	Relationship string
	Shares       float64
	ValueUSD     float64
	SaleDate     time.Time
	// PlanAdopted is the 10b5-1 plan date behind the sale, zero when the notice
	// names none.
	PlanAdopted time.Time
	// Acquisition is how the stock being sold was obtained ("Restricted Stock
	// Units", "Option Exercise", "Open Market Purchase", …).
	Acquisition string
	URL         string
	Filed       time.Time
}

// scheduled reports whether this notice describes the compensation calendar
// rather than a decision. Either mark is enough: a plan adopted in advance
// removes the seller's discretion over the timing, and vesting equity being sold
// as it lands is a payroll mechanic.
func (n form144Notice) scheduled() bool {
	if !n.PlanAdopted.IsZero() {
		return true
	}
	a := strings.ToLower(n.Acquisition)
	for _, mark := range []string{"restricted stock", "rsu", "option exercise", "stock option", "vest", "grant", "award", "espp", "employee stock purchase"} {
		if strings.Contains(a, mark) {
			return true
		}
	}
	return false
}

// form144Document is the slice of the Form 144 XML this reads. The element names
// are taken from a live filing (Apple, accession 0001950047-26-007959), not from
// the schema documentation, because the two disagree on several of them —
// `noOfUnitsSold`, not `unitsToBeSold`.
type form144Document struct {
	IssuerInfo struct {
		IssuerName    string `xml:"issuerName"`
		PersonAccount string `xml:"nameOfPersonForWhoseAccountTheSecuritiesAreToBeSold"`
		Relationships []struct {
			Relationship string `xml:"relationshipToIssuer"`
		} `xml:"relationshipsToIssuer"`
	} `xml:"formData>issuerInfo"`
	Securities []struct {
		ClassTitle    string `xml:"securitiesClassTitle"`
		UnitsSold     string `xml:"noOfUnitsSold"`
		MarketValue   string `xml:"aggregateMarketValue"`
		ApproxSale    string `xml:"approxSaleDate"`
		ExchangeName  string `xml:"securitiesExchangeName"`
		UnitsOutstand string `xml:"noOfUnitsOutstanding"`
	} `xml:"formData>securitiesInformation"`
	ToBeSold []struct {
		AcquiredDate string `xml:"acquiredDate"`
		Nature       string `xml:"natureOfAcquisitionTransaction"`
		Amount       string `xml:"amountOfSecuritiesAcquired"`
	} `xml:"formData>securitiesToBeSold"`
	Signature struct {
		NoticeDate   string   `xml:"noticeDate"`
		PlanAdoption []string `xml:"planAdoptionDates>planAdoptionDate"`
	} `xml:"formData>noticeSignature"`
}

// notice reduces a parsed document to one record. A filing may list several
// securities blocks; they are one proposed sale by one person, so the shares and
// the value add up and the earliest sale date leads.
func (d *form144Document) notice(url string, filed time.Time) (form144Notice, bool) {
	n := form144Notice{
		Owner: strings.TrimSpace(d.IssuerInfo.PersonAccount),
		URL:   url,
		Filed: filed,
	}
	if n.Owner == "" {
		n.Owner = "an insider"
	}
	var rels []string
	for _, r := range d.IssuerInfo.Relationships {
		if v := strings.TrimSpace(r.Relationship); v != "" {
			rels = append(rels, v)
		}
	}
	n.Relationship = strings.Join(rels, "/")

	for _, s := range d.Securities {
		n.Shares += parseFloatOrZero(s.UnitsSold)
		n.ValueUSD += parseFloatOrZero(s.MarketValue)
		if t, ok := parseEDGARDate(s.ApproxSale); ok && (n.SaleDate.IsZero() || t.Before(n.SaleDate)) {
			n.SaleDate = t
		}
	}
	if n.Shares <= 0 && n.ValueUSD <= 0 {
		return form144Notice{}, false
	}
	for _, t := range d.ToBeSold {
		if v := strings.TrimSpace(t.Nature); v != "" {
			n.Acquisition = v
			break
		}
	}
	for _, p := range d.Signature.PlanAdoption {
		if t, ok := parseEDGARDate(p); ok {
			n.PlanAdopted = t
			break
		}
	}
	return n, true
}

// parseEDGARDate reads the two date shapes these filings use: Form 144 writes
// MM/DD/YYYY, the ownership forms write YYYY-MM-DD.
func parseEDGARDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"01/02/2006", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// fetchPlannedSales lists the Form 144 notices an issuer's insiders filed in the
// window. It is best-effort: a ticker with no 144s is the common case and is not
// an error.
func (p *edgarProvider) fetchPlannedSales(ctx context.Context, cik string) ([]form144Notice, []string) {
	filings, err := p.recentFilings(ctx, cik, "144", form144LookbackDays, form144MaxDocs)
	if err != nil {
		return nil, []string{fmt.Sprintf("Form 144 index: %v", err)}
	}
	var out []form144Notice
	var warnings []string
	for _, f := range filings {
		doc, err := p.fetchForm144(ctx, cik, f.accession, f.document)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Form 144 %s: %v", f.accession, err))
			continue
		}
		if n, ok := doc.notice(f.url, f.filed); ok {
			out = append(out, n)
		}
	}
	return out, warnings
}

func (p *edgarProvider) fetchForm144(ctx context.Context, cik, accession, document string) (*form144Document, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	// The archive path and the rendered-document prefix are identical to Form
	// 4's ("xsl144X01/" rather than "xslF345X05/"), and form4URL strips any
	// "xsl" prefix, so it builds this one too.
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
	var doc form144Document
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return &doc, nil
}

// classifyPlannedSales reduces a window of Form 144 notices to one verdict.
//
// It can only ever be bearish or silent, and that is not an oversight: a notice
// of intent to sell has no bullish counterpart, because there is no form an
// insider files to announce a purchase. The asymmetry is in the instrument, so
// the leg is written to abstain readily rather than to balance itself — the
// insider and flow legs beside it are the ones that can vote up.
func classifyPlannedSales(notices []form144Notice) insiderSignal {
	if len(notices) == 0 {
		return insiderSignal{InsiderNone, insiderNoSignal + fmt.Sprintf(": no Form 144 notices in the last %d days", form144LookbackDays)}
	}

	var discretionary []form144Notice
	var schedUSD, discUSD float64
	sellers := map[string]bool{}
	for _, n := range notices {
		if n.scheduled() {
			schedUSD += n.ValueUSD
			continue
		}
		discretionary = append(discretionary, n)
		discUSD += n.ValueUSD
		sellers[n.Owner] = true
	}

	if len(discretionary) == 0 {
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: %d Form 144 notice(s) worth $%s, every one of them executing a 10b5-1 plan or selling vesting equity — the compensation calendar, not a view",
			insiderNoSignal, len(notices), usd(schedUSD))}
	}

	switch {
	case discUSD >= form144DiscretionaryUSD:
		n := largestNotice(discretionary)
		who := n.Owner
		if n.Relationship != "" {
			who += " (" + n.Relationship + ")"
		}
		return insiderSignal{InsiderBearish, fmt.Sprintf(
			"bearish: $%s of proposed sales filed under no 10b5-1 plan and not against vesting equity — largest is %s at $%s, %s — above the $%s bar at which an unscheduled sale is a decision rather than a schedule",
			usd(discUSD), who, usd(n.ValueUSD), saleTiming(n), usd(form144DiscretionaryUSD))}
	case len(sellers) >= form144DiscretionarySellers:
		return insiderSignal{InsiderBearish, fmt.Sprintf(
			"bearish: %d insiders filed unscheduled Form 144 notices ($%s total) in the same %d-day window — breadth, not one person's liquidity",
			len(sellers), usd(discUSD), form144LookbackDays)}
	}
	return insiderSignal{InsiderNone, fmt.Sprintf(
		"%s: %d unscheduled Form 144 notice(s) worth $%s from %d insider(s), neither large (under $%s) nor broad (under %d filers)%s",
		insiderNoSignal, len(discretionary), usd(discUSD), len(sellers),
		usd(form144DiscretionaryUSD), form144DiscretionarySellers, scheduledNote(len(notices)-len(discretionary), schedUSD))}
}

// scheduledSplit totals a window's notices by whether the seller chose the
// timing. It is the input the Form 4 breadth test needs.
func scheduledSplit(notices []form144Notice) scheduledContext {
	var c scheduledContext
	for _, n := range notices {
		if n.scheduled() {
			c.ScheduledUSD += n.ValueUSD
			continue
		}
		c.UnscheduledUSD += n.ValueUSD
	}
	return c
}

func scheduledNote(n int, usdTotal float64) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("; %d further scheduled notice(s) worth $%s excluded", n, usd(usdTotal))
}

func saleTiming(n form144Notice) string {
	if n.SaleDate.IsZero() {
		return "no sale date given"
	}
	return "to be sold from " + n.SaleDate.Format("2006-01-02")
}

func largestNotice(ns []form144Notice) form144Notice {
	best := ns[0]
	for _, n := range ns[1:] {
		if n.ValueUSD > best.ValueUSD {
			best = n
		}
	}
	return best
}

// plannedSalesSummary is the human-readable sentence stored beside the verdict.
func plannedSalesSummary(notices []form144Notice) string {
	var schedUSD, discUSD float64
	sched, disc := 0, 0
	for _, n := range notices {
		if n.scheduled() {
			sched++
			schedUSD += n.ValueUSD
			continue
		}
		disc++
		discUSD += n.ValueUSD
	}
	return fmt.Sprintf("%d proposed insider %s in %d days: %d scheduled ($%s, 10b5-1 or vesting equity) and %d unscheduled ($%s)",
		len(notices), plural(len(notices), "sale", "sales"), form144LookbackDays,
		sched, usd(schedUSD), disc, usd(discUSD))
}
