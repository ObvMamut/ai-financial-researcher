package marketdata

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// Schedules 13D and 13G are the disclosures a holder crossing 5% of a class must
// file. They are the largest positions this pipeline can see anybody take, and
// the two are not the same statement:
//
//   - **13D** is the activist filing. It is required when the holder has any
//     purpose other than passive investment — a board seat, a strategic review,
//     a sale of the company — and it must be filed within five business days of
//     crossing the threshold. On a 5–20 day horizon a fresh 13D is the single
//     strongest catalyst in this whole evidence set.
//   - **13G** is its passive counterpart, filed by index funds and advisers who
//     hold 5% because the index does. Vanguard filing a 13G/A on Apple says
//     nothing about Apple. Reading those as whale activity would put a signal on
//     every mega-cap in the shortlist at once.
//
// The plan for this leg assumed EDGAR full-text search was required because these
// are filed under the holder's CIK. They are also indexed under the *subject*
// issuer's CIK, which the pipeline already fetches for Form 4 — so this reads the
// same submissions document the other two legs do, and needs no new service.
//
// Only the structured form names are read. SEC's December 2024 amendments made
// XML filing mandatory for these schedules; the legacy "SC 13D"/"SC 13G" names
// are HTML documents whose numbers live in a rendered table, and every filing
// inside this leg's lookback is structured. Scraping the old shape to reach
// filings older than the window would add a parser that could only ever return
// stale information.
const (
	// stakeLookbackDays is how long a new 5% holder stays a live fact. A 13D is
	// a catalyst for as long as the campaign it announces runs, which outlasts
	// any single swing trade, but the *filing* is news for weeks rather than
	// months.
	stakeLookbackDays = 90
	// stakeMaxDocs caps documents per issuer. Large caps collect a dozen 13G/A
	// amendments each February, and reading six is enough to know that.
	stakeMaxDocs = 6
	// stakeMinPercent is the smallest holding worth naming. Below the 5%
	// threshold the schedule would not have been filed at all, so this only
	// screens out an amendment reporting an exit down to a token stake.
	stakeMinPercent = 5.0
)

// Structured form names, in the order they are searched.
var stakeForms = []string{"SCHEDULE 13D", "SCHEDULE 13D/A", "SCHEDULE 13G", "SCHEDULE 13G/A"}

// stakeFiling is one reported 5%+ position.
type stakeFiling struct {
	Holder string
	// Percent is the reported percentage of the class.
	Percent float64
	Shares  float64
	// PersonType is SEC's reporting-person code — IA (investment adviser), CO
	// (corporation), IN (individual), PN (partnership), HC (holding company)…
	PersonType string
	// Activist is true for a 13D of either kind. It is a statement of *intent*,
	// which is what separates the two schedules.
	Activist bool
	// Amendment is true for a "/A" filing: an update to a position already
	// disclosed, which may as easily be an exit as an increase.
	Amendment string
	EventDate time.Time
	Filed     time.Time
	Form      string
	URL       string
}

func (s stakeFiling) isAmendment() bool { return strings.HasSuffix(s.Form, "/A") }

// stakeDocument covers both schedules in one struct. They are separate schemas
// with separate element names for the same numbers — 13G puts the holder in
// `coverPageHeaderReportingPersonDetails/classPercent`, 13D in
// `reportingPersons/reportingPersonInfo/percentOfClass` — so both paths are
// declared and whichever the filing populates is the one that is read.
type stakeDocument struct {
	SubmissionType string `xml:"headerData>submissionType"`

	// 13G shape.
	GDetails struct {
		Name       string  `xml:"reportingPersonName"`
		Percent    float64 `xml:"classPercent"`
		Aggregate  float64 `xml:"reportingPersonBeneficiallyOwnedAggregateNumberOfShares"`
		PersonType string  `xml:"typeOfReportingPerson"`
	} `xml:"formData>coverPageHeaderReportingPersonDetails"`
	GEventDate string `xml:"formData>coverPageHeader>eventDateRequiresFilingThisStatement"`

	// 13D shape: one block per reporting person, and a filing routinely lists a
	// fund and the person behind it reporting the same shares.
	DPersons []struct {
		Name       string  `xml:"reportingPersonName"`
		Percent    float64 `xml:"percentOfClass"`
		Aggregate  float64 `xml:"aggregateAmountOwned"`
		PersonType string  `xml:"typeOfReportingPerson"`
	} `xml:"formData>reportingPersons>reportingPersonInfo"`
	DEventDate string `xml:"formData>coverPageHeader>dateOfEvent"`

	// The issuer's CUSIP, which both schedules carry on the cover page and
	// which is the only free identifier that joins a ticker to a 13F holdings
	// table. See issuerCUSIP in edgar13f.go.
	IssuerCusips []string `xml:"formData>coverPageHeader>issuerInfo>issuerCusips>issuerCusipNumber"`
	// The subject company, which is what makes a filing in this feed readable at
	// all. See aboutIssuer. The two schedules disagree on the capitalisation of
	// the CIK element — 13D writes `issuerCIK`, 13G writes `issuerCik` — and
	// encoding/xml matches element names case-sensitively, so both are declared.
	IssuerName     string `xml:"formData>coverPageHeader>issuerInfo>issuerName"`
	IssuerCIKUpper string `xml:"formData>coverPageHeader>issuerInfo>issuerCIK"`
	IssuerCIKMixed string `xml:"formData>coverPageHeader>issuerInfo>issuerCik"`
}

// issuerCIK is the subject company's CIK as the cover page states it, zero-padded
// to the ten digits EDGAR's submissions paths use.
func (d *stakeDocument) issuerCIK() string {
	for _, v := range []string{d.IssuerCIKUpper, d.IssuerCIKMixed} {
		if v = strings.TrimSpace(v); v != "" {
			return padCIK(v)
		}
	}
	return ""
}

// aboutIssuer reports whether this schedule is filed *against* the named issuer,
// rather than being one the issuer filed as a holder of somebody else.
//
// EDGAR's submissions document lists every filing a CIK is associated with, in
// either role, and recentFilingsAny filters on form name and date alone. So a
// company that itself holds 5% of something appears in its own feed as a
// Schedule 13G — and this leg read it as a stake in the company. On 2026-09-03
// that produced "SCHEDULE 13G/A filed by Banco Bilbao Vizcaya Argentaria at
// 4.7%" on BBVA, "by QUALCOMM Incorporated at 0.2%" on QCOM, and — the expensive
// one — "SCHEDULE 13D filed by Stellantis N.V. at 9.5%" on STLAM.MI, which
// carried the run's only bullish activist verdict on prose about a holder with a
// purpose beyond passive investment. The two sub-5% percentages give the shape
// away: a schedule is not required below the threshold that triggers it.
//
// Two independent tests, either of which is conclusive:
//
//   - the cover page names a subject company, and it is not this issuer;
//   - the reporting person is the issuer itself.
//
// A filing that states neither is kept. This leg is the only source of a 13D,
// and dropping real ones to be safe would cost more than it saves.
func (d *stakeDocument) aboutIssuer(cik, issuerName, holder string) (bool, string) {
	// The cover page states the subject company's own CIK, which is the same
	// identifier the submissions document was fetched under. Where it is present
	// nothing else is needed.
	if subject := d.issuerCIK(); subject != "" && cik != "" {
		if subject == padCIK(cik) {
			return true, ""
		}
		name := strings.TrimSpace(d.IssuerName)
		if name == "" {
			name = "CIK " + subject
		}
		return false, fmt.Sprintf("its cover page names %s as the subject company", name)
	}
	if issuerName == "" {
		return true, ""
	}
	if subject := strings.TrimSpace(d.IssuerName); subject != "" && !sameEntityName(subject, issuerName) {
		return false, fmt.Sprintf("its cover page names %s as the subject company", subject)
	}
	if sameEntityName(holder, issuerName) {
		return false, "the issuer is the reporting person, so it is a stake this company holds in somebody else"
	}
	return true, ""
}

// entityStopWords are the corporate-form and jurisdiction fragments that differ
// between how EDGAR names a registrant ("QUALCOMM INC/DE") and how a cover page
// names it ("QUALCOMM Incorporated") for the same company.
var entityStopWords = map[string]bool{
	"inc": true, "incorporated": true, "corp": true, "corporation": true,
	"co": true, "company": true, "companies": true, "cos": true,
	"plc": true, "ag": true, "nv": true, "sa": true, "spa": true, "se": true,
	"ltd": true, "limited": true, "llc": true, "lp": true, "lllp": true,
	"holding": true, "holdings": true, "group": true, "the": true,
	"n": true, "v": true, "s": true, "a": true,
}

// entityTokens reduces a company name to the words that identify it.
func entityTokens(name string) []string {
	if i := strings.Index(name, "/"); i >= 0 {
		name = name[:i] // EDGAR appends a state of incorporation: "QUALCOMM INC/DE".
	}
	var out []string
	for _, f := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if !entityStopWords[f] {
			out = append(out, f)
		}
	}
	return out
}

// sameEntityName is a deliberately strict match: equal identifying tokens, or one
// name a prefix of the other with something substantial in common. A loose match
// here silently deletes a real activist filing, which is the one fact in this
// domain worth having.
func sameEntityName(a, b string) bool {
	ta, tb := entityTokens(a), entityTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	if len(tb) < len(ta) {
		ta, tb = tb, ta
	}
	for i := range ta {
		if ta[i] != tb[i] {
			return false
		}
	}
	if len(ta) == len(tb) {
		return true // the same name under two corporate suffixes
	}
	// One name is a strict prefix of the other, which is only an identity when
	// the shorter half is distinctive on its own. "QUALCOMM" is; "State" — the
	// head of both State Street and State Auto Financial — is not.
	return len(ta) > 1 || len(ta[0]) >= 6
}

// stake reduces a parsed document to one record. Where a 13D lists several
// reporting persons for the same position, the largest is taken: they are
// co-filers on one stake, not several.
func (d *stakeDocument) stake(form, url string, filed time.Time) (stakeFiling, bool) {
	s := stakeFiling{Form: form, URL: url, Filed: filed, Activist: strings.Contains(form, "13D")}

	if len(d.DPersons) > 0 {
		best := d.DPersons[0]
		for _, p := range d.DPersons[1:] {
			if p.Percent > best.Percent {
				best = p
			}
		}
		s.Holder, s.Percent, s.Shares, s.PersonType = best.Name, best.Percent, best.Aggregate, best.PersonType
	} else {
		g := d.GDetails
		s.Holder, s.Percent, s.Shares, s.PersonType = g.Name, g.Percent, g.Aggregate, g.PersonType
	}
	s.Holder = strings.TrimSpace(s.Holder)
	if s.Holder == "" || s.Percent <= 0 {
		return stakeFiling{}, false
	}
	for _, raw := range []string{d.DEventDate, d.GEventDate} {
		if t, ok := parseEDGARDate(raw); ok {
			s.EventDate = t
			break
		}
	}
	return s, true
}

func (p *edgarProvider) fetchStakesDetailed(ctx context.Context, cik, ticker string) ([]stakeFiling, []string, []model.SourceDiagnostic) {
	var diagnostics []model.SourceDiagnostic
	record := func(reason, disposition, message string) string {
		diagnostics = append(diagnostics, sourceDiagnostic(p.Name(), ticker, "ownership", reason, disposition, message))
		return message
	}
	filings, err := p.recentFilingsAny(ctx, cik, stakeForms, stakeLookbackDays, stakeMaxDocs)
	if err != nil {
		return nil, []string{record("fetch_failed", "failed", fmt.Sprintf("13D/G index: %v", err))}, diagnostics
	}
	issuer := p.issuerName(ctx, cik)
	var out []stakeFiling
	var warnings []string
	for _, f := range filings {
		doc, err := p.fetchStakeDoc(ctx, cik, f.accession, f.document)
		if err != nil {
			warnings = append(warnings, record("fetch_failed", "failed", fmt.Sprintf("%s %s: %v", f.form, f.accession, err)))
			continue
		}
		s, ok := doc.stake(f.form, f.url, f.filed)
		if !ok {
			continue
		}
		if about, why := doc.aboutIssuer(cik, issuer, s.Holder); !about {
			warnings = append(warnings, record("filtered_unrelated", "withheld", fmt.Sprintf(
				"%s filed %s is not a stake in this issuer: %s — dropped",
				f.form, f.filed.Format("2006-01-02"), why)))
			continue
		}
		if s.Percent < stakeMinPercent {
			// A "5%+ ownership schedule" reporting 0.2% is either a wind-down
			// amendment or, far more often, a filing that is not about this
			// issuer at all and slipped both tests above.
			warnings = append(warnings, record("below_threshold", "withheld", fmt.Sprintf(
				"%s filed %s by %s reports %.1f%%, below the %.0f%% a schedule is filed at — dropped",
				f.form, f.filed.Format("2006-01-02"), s.Holder, s.Percent, stakeMinPercent)))
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Filed.After(out[j].Filed) })
	return out, warnings, diagnostics
}

// issuerName is the registrant name EDGAR files this CIK under, used to tell a
// schedule filed against the issuer from one the issuer filed itself. An index
// that will not load is not an error here: aboutIssuer keeps everything when it
// has no name to compare against, which is the behaviour this leg had before.
func (p *edgarProvider) issuerName(ctx context.Context, cik string) string {
	sub, err := p.submissionIndex(ctx, cik)
	if err != nil || sub == nil {
		return ""
	}
	return strings.TrimSpace(sub.Name)
}

func (p *edgarProvider) fetchStakeDoc(ctx context.Context, cik, accession, document string) (*stakeDocument, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	// Same archive layout and same rendered-document prefix convention as the
	// ownership forms ("xslSCHEDULE_13D_X02/"), which form4URL already strips.
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var doc stakeDocument
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return &doc, nil
}

// classifyActivistStakes reduces a window of 13D/G filings to one verdict.
//
// Only a *new* 13D votes. Everything else abstains, and each abstention has its
// own reason, because they are different situations a reader will want told
// apart:
//
//   - A 13G is passive by definition. It is the index owning the index.
//   - A 13D/A is an update to a campaign already disclosed, and this leg cannot
//     tell an activist adding from one selling out without diffing against the
//     prior filing — which would need a second fetch to answer a question the
//     amendment itself does not state.
//   - No filings at all is the normal case for almost every name.
func classifyActivistStakes(stakes []stakeFiling) insiderSignal {
	if len(stakes) == 0 {
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: no 5%%+ ownership schedule filed against this issuer in %d days",
			insiderNoSignal, stakeLookbackDays)}
	}

	var fresh13D []stakeFiling
	amendments, passive := 0, 0
	for _, s := range stakes {
		switch {
		case s.Activist && !s.isAmendment() && s.Percent >= stakeMinPercent:
			fresh13D = append(fresh13D, s)
		case s.isAmendment():
			amendments++
		default:
			passive++
		}
	}

	if len(fresh13D) > 0 {
		s := fresh13D[0]
		for _, c := range fresh13D[1:] {
			if c.Percent > s.Percent {
				s = c
			}
		}
		return insiderSignal{InsiderBullish, fmt.Sprintf(
			"bullish: %s filed a new Schedule 13D on %s disclosing %.1f%% of the class%s — a 13D is filed only when the holder has a purpose beyond passive investment, which makes it a catalyst rather than a holding",
			s.Holder, s.Filed.Format("2006-01-02"), s.Percent, personTypeNote(s.PersonType))}
	}

	switch {
	case amendments > 0 && passive > 0:
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: %d amendment(s) and %d passive 13G filing(s) in %d days, but no new 13D — an amendment does not say whether the holder added or sold",
			insiderNoSignal, amendments, passive, stakeLookbackDays)}
	case amendments > 0:
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: %d amendment(s) to schedules already on file and no new 13D — an amendment is as often an exit as an increase, and this leg does not diff against the prior filing",
			insiderNoSignal, amendments)}
	default:
		holders := passiveHolders(stakes)
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: %d passive 13G filing(s) (%s) and no 13D — a 13G is the index owning the index, not a view on the company",
			insiderNoSignal, passive, holders)}
	}
}

func personTypeNote(code string) string {
	name := map[string]string{
		"IA": "an investment adviser",
		"CO": "a corporation",
		"IN": "an individual",
		"PN": "a partnership",
		"HC": "a holding company",
		"FI": "a bank",
		"BD": "a broker-dealer",
		"IC": "an investment company",
		"EP": "a pension plan",
	}[strings.ToUpper(strings.TrimSpace(code))]
	if name == "" {
		return ""
	}
	return ", filed as " + name
}

func passiveHolders(stakes []stakeFiling) string {
	var names []string
	for i, s := range stakes {
		if i == 3 {
			names = append(names, "…")
			break
		}
		names = append(names, s.Holder)
	}
	return strings.Join(names, ", ")
}

// stakesSummary is the human-readable sentence stored beside the verdict.
func stakesSummary(stakes []stakeFiling) string {
	if len(stakes) == 0 {
		return fmt.Sprintf("no Schedule 13D or 13G filed against this issuer in the last %d days", stakeLookbackDays)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d 5%%+ ownership %s in %d days:", len(stakes),
		plural(len(stakes), "schedule", "schedules"), stakeLookbackDays)
	for i, s := range stakes {
		if i == stakeMaxDocs {
			break
		}
		fmt.Fprintf(&b, " %s filed %s by %s at %.1f%%", s.Form, s.Filed.Format("2006-01-02"), s.Holder, s.Percent)
		if s.Shares > 0 {
			fmt.Fprintf(&b, " (%s sh)", humanShares(s.Shares))
		}
		if i < len(stakes)-1 {
			b.WriteString(";")
		}
	}
	return b.String()
}
