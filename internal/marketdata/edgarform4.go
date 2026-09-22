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
	// form4MaxDocs caps the filings actually downloaded per ticker.
	//
	// It was 5, which is not enough to see the thing this source is for. The
	// classifier's bullish tests are about *breadth* — how many distinct
	// insiders bought — and a busy issuer files five Form 4s in a fortnight
	// without a single one of them being a purchase. Truncating at five reads
	// the top of the pile and calls the rest of the window quiet. Twelve is
	// bounded by what SEC asks for (10 requests/second, no daily cap) rather
	// than by caution: a full shortlist at twelve filings each is a couple of
	// seconds of the run.
	form4MaxDocs = 12

	// Form 144 is the notice an insider files *before* selling restricted or
	// control stock — the intent, where Form 4 is the record. On a 5–20 day
	// horizon that ordering matters: a 144 filed today describes selling that
	// has not happened yet.
	//
	// What it mostly describes, though, is a schedule. Almost every large-cap
	// 144 covers RSUs vesting into a 10b5-1 plan adopted months earlier, and
	// reading those as a bearish view is the identical mistake the Form 4
	// classifier exists to avoid — with the same consequence, a standing levy on
	// every long. Both discriminators are in the filing itself: whether a plan
	// adoption date is present, and how the stock was acquired.
	form144LookbackDays = 45
	form144MaxDocs      = 8
	// form144DiscretionaryUSD is the size at which one *unscheduled* planned
	// sale — no 10b5-1 plan behind it, not vesting stock being flipped — is a
	// decision worth reading.
	form144DiscretionaryUSD = 5_000_000
	// form144DiscretionarySellers is the breadth alternative: several insiders
	// filing discretionary notices in the same window is the group deciding.
	form144DiscretionarySellers = 2
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
	// SharesAfter is the reporting owner's remaining direct holding. It is what
	// separates an officer trimming 2% of their position from one exiting it, and
	// dollar totals cannot: these are $1B-a-day names where even a $5.76M sale is
	// half a percent of one session's volume.
	SharesAfter float64
}

func (t form4Transaction) value() float64 { return t.Shares * t.Price }

// insiderRole is which kind of insider filed. It is derived from the reported
// title rather than stored separately, because that is the only place the
// relationship reaches these structs.
type insiderRole string

const (
	roleOfficer    insiderRole = "officer"
	roleDirector   insiderRole = "director"
	roleTenPercent insiderRole = "10% owner"
	roleUnknown    insiderRole = "insider"
)

// role reads the relationship back out of the reported title. owner() writes the
// literal strings "director" and "10% owner" for those two relationships and the
// person's own officer title otherwise, so anything else is an officer.
func (t form4Transaction) role() insiderRole {
	switch title := strings.ToLower(strings.TrimSpace(t.Title)); {
	case title == "":
		return roleUnknown
	case title == string(roleDirector):
		return roleDirector
	case title == string(roleTenPercent):
		return roleTenPercent
	default:
		return roleOfficer
	}
}

// fractionOfHolding is how much of the owner's position this trade moved, or 0
// when the filing did not report a post-transaction balance.
func (t form4Transaction) fractionOfHolding() float64 {
	before := t.SharesAfter + t.Shares // a sale: what they held going in
	if t.Buy || before <= 0 {
		return 0
	}
	return t.Shares / before
}

// submissionsResp is the slice of SEC's submissions JSON we need: parallel
// arrays, one index per filing.
type submissionsResp struct {
	// Name is the registrant EDGAR files this CIK under. The 13D/G leg compares
	// it with a schedule's reporting person to tell a stake in this issuer from
	// one this issuer holds in somebody else; see aboutIssuer.
	Name    string `json:"name"`
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
		PostTransaction struct {
			SharesOwnedFollowing struct {
				Value string `xml:"value"`
			} `xml:"sharesOwnedFollowingTransaction"`
		} `xml:"postTransactionAmounts"`
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

	filings, err := p.recentFilings(ctx, cik, "4", form4LookbackDays, form4MaxDocs)
	if err != nil {
		return TickerData{}, err
	}
	td := TickerData{Ticker: ticker}
	browse := fmt.Sprintf("%s/cgi-bin/browse-edgar?action=getcompany&CIK=%s&type=4&dateb=&owner=include&count=40",
		p.tickersBase, cik)
	// Form 144 rides the same issuer CIK and the same submissions index. It is
	// read *before* the Form 4 verdict rather than appended after it, because the
	// two forms describe the same sales from opposite ends and the notices are
	// what say whether those sales were scheduled — see scheduledContext.
	plannedURL := fmt.Sprintf("%s/cgi-bin/browse-edgar?action=getcompany&CIK=%s&type=144&dateb=&owner=include&count=40",
		p.tickersBase, cik)
	notices, noticeWarnings := p.fetchPlannedSales(ctx, cik)
	sched := scheduledSplit(notices)
	if len(filings) == 0 {
		// A foreign private issuer is exempt from Section 16: it files 20-F, not
		// Form 4. "No Form 4 filings in the last 45 days" is then a statement
		// about US filing law dressed up as a statement about insider behaviour —
		// and classifyInsiderActivity(nil) reads it as "no signal", which is
		// reassurance this source never offered. ASML.AS got exactly that.
		//
		// Only the *empty* case is converted: an issuer that does file Form 4s
		// has real activity to report whatever its suffix says.
		//
		// The test is the *resolved* symbol, not the requested one. ASML sits in
		// nq100 as the bare US line and in eu50 as ASML.AS, so one issuer enters
		// under two spellings; testing the request exempted the dotted one and
		// handed the other the no-signal verdict this whole branch exists to
		// prevent. The FPI registry that separates them is the ADR map's own
		// us_line column — see isForeignPrivateIssuer.
		if isForeignPrivateIssuer(symbol) {
			// Section 16 does not apply, so there is no insider leg to write —
			// "no Form 4 filings in 45 days" would be a statement about US
			// filing law dressed up as a statement about this issuer's
			// insiders, and classifyInsiderActivity(nil) reads it as "no
			// signal", which is reassurance this source never offered.
			//
			// The other three legs are not exempt. A 13D can be filed against
			// any Section 12 class, an ADR sits in tracked managers' 13F tables
			// like anything else, and a Form 144 covers control securities
			// whoever the issuer is. Returning ErrNotApplicable here — which is
			// what this did — threw those away too, so a foreign name could
			// never have positioning evidence of any kind. It now abstains where
			// the legs are quiet, which is a recorded abstention rather than a
			// fetch that failed.
			td.Warnings = append(td.Warnings, fmt.Sprintf(
				"%s is a foreign private issuer: exempt from Section 16, so it files 20-F rather than Form 4 and there is no insider leg for it",
				ticker))
			td.Diagnostics = append(td.Diagnostics, sourceDiagnostic(p.Name(), ticker, "insider", "not_applicable", "expected", td.Warnings[len(td.Warnings)-1]))
			p.addPlannedSales(&td, plannedURL, notices, noticeWarnings)
			p.addActivistStakes(ctx, &td, cik)
			p.addInstitutionalHoldings(ctx, &td, cik)
			if len(td.Facts) == 0 {
				return TickerData{Ticker: ticker, Diagnostics: td.Diagnostics}, fmt.Errorf(
					"%w: %s is a foreign private issuer with no 13D/G, Form 144 or tracked 13F position — nothing in SEC's filings reaches it",
					ErrNotApplicable, ticker)
			}
			return td, nil
		}
		td.Facts = append(td.Facts, Fact{
			Label:  InsiderActivityLabel,
			Value:  fmt.Sprintf("no Form 4 filings in the last %d days", form4LookbackDays),
			AsOf:   time.Now(),
			Source: "SEC EDGAR",
			URL:    browse,
		})
		td.Facts = append(td.Facts, signalFact(InsiderSignalLabel,
			classifyInsiderActivity(nil), "computed", browse))
		p.addPlannedSales(&td, plannedURL, notices, noticeWarnings)
		p.addActivistStakes(ctx, &td, cik)
		p.addInstitutionalHoldings(ctx, &td, cik)
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
	// The verdict on that activity, computed rather than left to the agent's
	// reading of it. See insidersignal.go for why routine selling is not a signal.
	td.Facts = append(td.Facts, signalFact(InsiderSignalLabel,
		classifyInsiderActivityWith(txns, sched), "computed", browse))

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
		td.Facts = append(td.Facts, Fact{
			Label: "Insider transaction",
			Value: fmt.Sprintf("%s %s %s sh @ %.2f = $%s", who(t), verb,
				humanShares(t.Shares), t.Price, usd(t.value())),
			AsOf:   t.Date,
			Source: "SEC EDGAR",
			URL:    t.URL,
		})
	}
	p.addPlannedSales(&td, plannedURL, notices, noticeWarnings)
	p.addActivistStakes(ctx, &td, cik)
	p.addInstitutionalHoldings(ctx, &td, cik)
	return td, nil
}

// addInstitutionalHoldings appends the 13F leg. Unlike the other two it needs an
// identifier the ticker does not carry — the CUSIP — so it abstains silently
// where that cannot be established rather than writing a fact it cannot back.
func (p *edgarProvider) addInstitutionalHoldings(ctx context.Context, td *TickerData, cik string) {
	idx := p.thirteenF(ctx)
	if idx == nil {
		return
	}
	cusip, ok := p.issuerCUSIP(ctx, cik)
	if !ok {
		return
	}
	positions := idx.ByCUSIP[cusip]
	url := fmt.Sprintf("%s/cgi-bin/browse-edgar?action=getcompany&CIK=%s&type=13F&dateb=&owner=include&count=40",
		p.tickersBase, cik)
	td.Facts = append(td.Facts,
		Fact{
			Label:  InstitutionalLabel,
			Value:  thirteenFSummary(positions, idx),
			AsOf:   time.Now(),
			Source: "SEC EDGAR",
			URL:    url,
		},
		signalFact(InstitutionalSignalLabel, classifyInstitutionalHoldings(positions, idx), "computed", url),
	)
}

// addActivistStakes appends the 13D/G leg, on the same best-effort terms as the
// Form 144 one.
func (p *edgarProvider) addActivistStakes(ctx context.Context, td *TickerData, cik string) {
	stakes, warnings, diagnostics := p.fetchStakesDetailed(ctx, cik, td.Ticker)
	td.Diagnostics = append(td.Diagnostics, diagnostics...)
	td.Warnings = append(td.Warnings, warnings...)
	if len(warnings) > 0 && len(stakes) == 0 {
		return
	}
	url := fmt.Sprintf("%s/cgi-bin/browse-edgar?action=getcompany&CIK=%s&type=SC+13&dateb=&owner=include&count=40",
		p.tickersBase, cik)
	td.Facts = append(td.Facts,
		Fact{
			Label:  ActivistStakeLabel,
			Value:  stakesSummary(stakes),
			AsOf:   time.Now(),
			Source: "SEC EDGAR",
			URL:    url,
		},
		signalFact(ActivistStakeSignalLabel, classifyActivistStakes(stakes), "computed", url),
	)
}

// addPlannedSales appends the Form 144 leg. It never fails the fetch: the Form 4
// facts above it are the older and better-established evidence, and losing them
// because an issuer's 144 index was slow would be a bad trade.
func (p *edgarProvider) addPlannedSales(td *TickerData, url string, notices []form144Notice, warnings []string) {
	td.Warnings = append(td.Warnings, warnings...)
	if len(warnings) > 0 && len(notices) == 0 {
		// The index itself did not answer. Saying "no planned sales" here would
		// be reporting a fetch failure as a fact about the issuer.
		return
	}
	td.Facts = append(td.Facts,
		Fact{
			Label:  PlannedSalesLabel,
			Value:  plannedSalesSummary(notices),
			AsOf:   time.Now(),
			Source: "SEC EDGAR",
			URL:    url,
		},
		signalFact(PlannedSalesSignalLabel, classifyPlannedSales(notices), "computed", url),
	)
}

type form4Filing struct {
	accession string
	document  string
	url       string
	form      string
	filed     time.Time
}

// recentFilings lists one issuer's filings of a given form inside a lookback
// window, newest first, capped at maxDocs.
//
// Form 4, Form 144 and the 13D/G stake schedules all live in the same
// submissions index under the same issuer CIK and differ only in the form
// string, so they share this. The lookback is a parameter rather than a constant
// because they do not mean the same thing: Form 4 reports a trade that happened,
// Form 144 announces one that has not, and a 13D is a position that stays a
// catalyst for weeks.
//
// Only the `recent` block is read, which SEC caps at the last 1,000 filings.
// Anything older is paged into separate files — and irrelevant here, because no
// lookback this system uses reaches past 1,000 filings of an issuer's history.
// A window that ever did would silently see a truncated list, so the caller's
// lookback is the guard: keep it inside a quarter.
func (p *edgarProvider) recentFilings(ctx context.Context, cik, form string, lookbackDays, maxDocs int) ([]form4Filing, error) {
	return p.recentFilingsAny(ctx, cik, []string{form}, lookbackDays, maxDocs)
}

// recentFilingsAny is the same over several form names at once, which is what
// the 13D/G leg needs: four form strings against one index document.
func (p *edgarProvider) recentFilingsAny(ctx context.Context, cik string, forms []string, lookbackDays, maxDocs int) ([]form4Filing, error) {
	sub, err := p.submissionIndex(ctx, cik)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, f := range forms {
		want[f] = true
	}
	r := sub.Filings.Recent
	cutoff := time.Now().UTC().AddDate(0, 0, -lookbackDays)

	var out []form4Filing
	for i := range r.Form {
		form := strings.TrimSpace(r.Form[i])
		if !want[form] {
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
			form:      form,
			filed:     filed,
		})
		if len(out) == maxDocs {
			break
		}
	}
	return out, nil
}

// submissionIndex fetches an issuer's filing index once per run.
func (p *edgarProvider) submissionIndex(ctx context.Context, cik string) (*submissionsResp, error) {
	p.mu.Lock()
	cached, ok := p.submissions[cik]
	p.mu.Unlock()
	if ok {
		return cached, nil
	}
	u := fmt.Sprintf("%s/submissions/CIK%s.json", p.factsBase, cik)
	var sub submissionsResp
	if err := p.getJSON(ctx, u, &sub); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.submissions[cik] = &sub
	p.mu.Unlock()
	return &sub, nil
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
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}
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
			Owner:       owner,
			Title:       title,
			Date:        date,
			Shares:      shares,
			Price:       price,
			Buy:         strings.EqualFold(strings.TrimSpace(t.Amounts.AcquiredDisposed.Value), "A"),
			URL:         url,
			SharesAfter: parseFloatOrZero(t.PostTransaction.SharesOwnedFollowing.Value),
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
	if err := p.limiter.Wait(ctx); err != nil {
		return err
	}
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
