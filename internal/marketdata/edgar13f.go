package marketdata

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Form 13F is the quarterly holdings report every institutional manager over
// $100M must file. It is the widest view of professional positioning that
// exists for free — and the weakest evidence in this file, for one structural
// reason: it is published 45 days after the quarter it describes, so by the time
// anyone reads it the manager has had six weeks to change their mind.
//
// That makes it unusable as a thesis and useful as a qualifier, which is how it
// is wired: every fact carries the quarter-end and a days-stale count, the leg
// abstains outright once the snapshot is older than two quarters of trading, and
// what it reads is not the *holding* but the **change** — a manager opening or
// closing a position is a decision with a date on it, where a position merely
// being held is a fact about last quarter.
//
// Two design choices follow from what this data can and cannot do:
//
//   - **A curated filer list, not a reverse index.** Building "who owns TICKER"
//     from all ~8,000 13F filers means downloading every one of them; the
//     interesting answer is anyway not "an index fund owns it" but "a manager
//     who concentrates owns it". Twenty-three names, each verified to be filing
//     currently, cover that at 1/300th of the cost.
//   - **CUSIP, not issuer name.** 13F info tables identify holdings by CUSIP and
//     an abbreviated issuer name — Ally Financial appears as "ALLY FINL INC" —
//     so name matching would silently miss and silently mismatch. The CUSIP is
//     recovered from the issuer's own structured 13D/G filings, which carry it
//     and which this provider already reads for the activist leg. On a
//     15-large-cap probe that resolved 14; the fifteenth abstains, which is the
//     correct outcome for a name whose identity could not be established.
const (
	// thirteenFStaleDays is how old a quarter-end may be before the snapshot
	// describes a book nobody holds any more. A 13F for Q2 is filed in
	// mid-August; by late November it is describing two quarters ago.
	thirteenFStaleDays = 150
	// thirteenFMoveFraction is the share change that makes a quarter's activity
	// a decision rather than a rebalance.
	thirteenFMoveFraction = 0.50
	// thirteenFMinValueUSD screens out a token move. A manager opening a $4M
	// line in a mega-cap, or closing one, has not expressed a view on it. It is
	// applied to whichever side of the change actually had size: an exit is
	// judged on what was exited, not on the zero left behind.
	thirteenFMinValueUSD = 25_000_000
	// thirteenFQuarters is how many quarters are read per filer: this one and
	// the last, because the change between them is the only part that carries a
	// date.
	thirteenFQuarters = 2
	// thirteenFMaxTable bounds one information table. Citadel's and Renaissance's
	// run to thousands of rows.
	thirteenFMaxTable = 64 << 20
)

// thirteenFFilers is the tracked list: managers who concentrate, take positions
// they intend to be noticed, or both. Every CIK below was verified against
// data.sec.gov to be filing 13F-HR currently — a filer that has gone quiet costs
// three requests a quarter and contributes nothing, so stale ones are not
// carried.
var thirteenFFilers = []struct{ CIK, Name string }{
	{"0001067983", "Berkshire Hathaway"},
	{"0002026053", "Pershing Square"},
	{"0001791786", "Elliott Investment Management"},
	{"0001040273", "Third Point"},
	{"0001167483", "Tiger Global Management"},
	{"0001517137", "Starboard Value"},
	{"0001603466", "Point72 Asset Management"},
	{"0001423053", "Citadel Advisors"},
	{"0001061165", "Lone Pine Capital"},
	{"0001135730", "Coatue Management"},
	{"0001350694", "Bridgewater Associates"},
	{"0001656456", "Appaloosa"},
	{"0001112520", "Akre Capital Management"},
	{"0001138995", "Glenview Capital Management"},
	{"0001582090", "Sachem Head Capital Management"},
	{"0001037389", "Renaissance Technologies"},
	{"0001998597", "JANA Partners Management"},
	{"0001061768", "Baupost Group"},
	{"0001418814", "ValueAct Holdings"},
	{"0001029160", "Soros Fund Management"},
	{"0001103804", "Viking Global Investors"},
	{"0000934639", "Maverick Capital"},
	{"0001027796", "Pzena Investment Management"},
}

// thirteenFPosition is one tracked manager's holding in one issuer, across the
// two quarters that were read.
type thirteenFPosition struct {
	Filer string `json:"filer"`
	// Shares and ValueUSD are the most recent quarter's. PriorShares is the
	// quarter before, and -1 distinguishes "held nothing" from "not read".
	Shares      float64 `json:"shares"`
	ValueUSD    float64 `json:"value_usd"`
	PriorShares float64 `json:"prior_shares"`
	// PriorValueUSD is the quarter before's dollar value. A closed position is
	// worth zero now by definition, so reporting the current value of an exit
	// says "closed a $0 position" — a sentence that tells the reader nothing
	// about how big the thing that was closed was.
	PriorValueUSD float64 `json:"prior_value_usd"`
	Quarter       string  `json:"quarter"`
}

// sizeUSD is the value that describes what the manager did: what they hold now,
// or for an exit, what they held before it.
func (p thirteenFPosition) sizeUSD() float64 {
	if p.ValueUSD > 0 {
		return p.ValueUSD
	}
	return p.PriorValueUSD
}

// change classifies the quarter-over-quarter move.
func (p thirteenFPosition) change() string {
	switch {
	case p.PriorShares < 0:
		return "held"
	case p.PriorShares == 0 && p.Shares > 0:
		return "opened"
	case p.Shares == 0 && p.PriorShares > 0:
		return "closed"
	case p.PriorShares > 0 && p.Shares >= p.PriorShares*(1+thirteenFMoveFraction):
		return "added to"
	case p.PriorShares > 0 && p.Shares <= p.PriorShares*(1-thirteenFMoveFraction):
		return "cut"
	default:
		return "held"
	}
}

// thirteenFIndex is the whole tracked universe reduced to a CUSIP lookup.
type thirteenFIndex struct {
	ByCUSIP map[string][]thirteenFPosition `json:"by_cusip"`
	// Quarter is the newest period-of-report seen across the filers, which is
	// what the staleness is measured from.
	Quarter string `json:"quarter"`
	Filers  int    `json:"filers"`
	BuiltAt string `json:"built_at"`
}

func (x *thirteenFIndex) quarterEnd() (time.Time, bool) {
	t, err := time.Parse("2006-01-02", x.Quarter)
	return t, err == nil
}

func (x *thirteenFIndex) staleDays() int {
	t, ok := x.quarterEnd()
	if !ok {
		return 1 << 30
	}
	return int(time.Since(t).Hours() / 24)
}

// informationTable is the 13F holdings document.
type informationTable struct {
	Rows []struct {
		Issuer string  `xml:"nameOfIssuer"`
		CUSIP  string  `xml:"cusip"`
		Value  float64 `xml:"value"`
		Shares struct {
			Amount float64 `xml:"sshPrnamt"`
			Type   string  `xml:"sshPrnamtType"`
		} `xml:"shrsOrPrnAmt"`
	} `xml:"infoTable"`
}

// coverPage is the 13F cover, read only for the period it reports.
type coverPage struct {
	PeriodOfReport string `xml:"formData>coverPage>reportCalendarOrQuarter"`
}

// thirteenFHoldings is one filer's quarter, keyed by CUSIP. A manager files the
// same CUSIP several times when different sub-advisers hold it, so the rows are
// summed rather than replaced — Berkshire's Ally position spans three lines.
func (t *informationTable) byCUSIP() map[string]struct{ Shares, Value float64 } {
	out := map[string]struct{ Shares, Value float64 }{}
	for _, r := range t.Rows {
		cusip := normalizeCUSIP(r.CUSIP)
		if cusip == "" {
			continue
		}
		// Only share positions. A principal amount is a bond, and adding it to a
		// share count produces a number that means nothing.
		if s := strings.ToUpper(strings.TrimSpace(r.Shares.Type)); s != "" && s != "SH" {
			continue
		}
		cur := out[cusip]
		cur.Shares += r.Shares.Amount
		// The value column is reported in whole dollars on modern filings and in
		// thousands on older ones; every filing this reads is post-2023, so it is
		// dollars.
		cur.Value += r.Value
		out[cusip] = cur
	}
	return out
}

func normalizeCUSIP(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) < 8 || len(s) > 9 {
		return ""
	}
	return s
}

// thirteenF returns the tracked-manager index, building it at most once per
// process and keeping it on disk for a quarter.
func (p *edgarProvider) thirteenF(ctx context.Context) *thirteenFIndex {
	p.mu.Lock()
	if p.thirteenFIdx != nil || p.thirteenFTried {
		idx := p.thirteenFIdx
		p.mu.Unlock()
		return idx
	}
	p.thirteenFTried = true
	p.mu.Unlock()

	var idx thirteenFIndex
	if p.cache != nil {
		// One quarter: the filings do not change inside one, and rebuilding a
		// 23-manager index on every run would spend a hundred requests to
		// reproduce the same map.
		if ok, err := p.cache.GetTTL("sec", "edgar", "13f", "tracked", 90*24*time.Hour, &idx); ok && err == nil {
			p.mu.Lock()
			p.thirteenFIdx = &idx
			p.mu.Unlock()
			return &idx
		}
	}

	built, complete := p.buildThirteenF(ctx)
	if built == nil {
		return nil
	}
	// Only a build that walked every tracked manager is written to disk. A run
	// cut short — a cancelled context, a stage timeout — still uses what it
	// managed to gather, but persisting that under a 90-day TTL would let one
	// interrupted run hand every run for the next quarter a two-manager index
	// presenting itself as the tracked set. The truncation leaves no trace in
	// the shape: an index of 2 filers and one of 23 look identical.
	if p.cache != nil && complete {
		_ = p.cache.SetTTL("sec", "edgar", "13f", "tracked", built)
	}
	p.mu.Lock()
	p.thirteenFIdx = built
	p.mu.Unlock()
	return built
}

// buildThirteenF walks the tracked managers. complete reports whether it got
// through all of them; a caller must not persist an incomplete index.
func (p *edgarProvider) buildThirteenF(ctx context.Context) (index *thirteenFIndex, complete bool) {
	idx := &thirteenFIndex{
		ByCUSIP: map[string][]thirteenFPosition{},
		BuiltAt: time.Now().UTC().Format(time.RFC3339),
	}
	var newest time.Time

	complete = true
	for _, f := range thirteenFFilers {
		if ctx.Err() != nil {
			complete = false
			break
		}
		filings, err := p.recentFilingsAny(ctx, f.CIK, []string{"13F-HR"}, 400, thirteenFQuarters)
		if err != nil || len(filings) == 0 {
			continue
		}
		current, quarter, err := p.thirteenFQuarter(ctx, f.CIK, filings[0])
		if err != nil {
			continue
		}
		prior := map[string]struct{ Shares, Value float64 }{}
		havePrior := false
		if len(filings) > 1 {
			if pq, _, err := p.thirteenFQuarter(ctx, f.CIK, filings[1]); err == nil {
				prior, havePrior = pq, true
			}
		}
		if quarter.After(newest) {
			newest = quarter
		}
		idx.Filers++

		// Every CUSIP either manager-quarter touched, so a closed position is
		// visible rather than simply absent.
		seen := map[string]bool{}
		for cusip := range current {
			seen[cusip] = true
		}
		for cusip := range prior {
			seen[cusip] = true
		}
		for cusip := range seen {
			cur := current[cusip]
			pos := thirteenFPosition{
				Filer:       f.Name,
				Shares:      cur.Shares,
				ValueUSD:    cur.Value,
				PriorShares: -1,
				Quarter:     quarter.Format("2006-01-02"),
			}
			if havePrior {
				pos.PriorShares = prior[cusip].Shares
				pos.PriorValueUSD = prior[cusip].Value
			}
			idx.ByCUSIP[cusip] = append(idx.ByCUSIP[cusip], pos)
		}
	}
	if idx.Filers == 0 {
		return nil, complete
	}
	idx.Quarter = newest.Format("2006-01-02")
	return idx, complete
}

// thirteenFQuarter reads one 13F filing: its period, and its holdings by CUSIP.
func (p *edgarProvider) thirteenFQuarter(ctx context.Context, cik string, f form4Filing) (map[string]struct{ Shares, Value float64 }, time.Time, error) {
	// The submissions index points at the *cover page*; the holdings live in a
	// separate, numbered document in the same folder, so the folder listing is
	// the only way to find it.
	table, err := p.thirteenFTableURL(ctx, cik, f.accession)
	if err != nil {
		return nil, time.Time{}, err
	}
	var period time.Time
	if raw, err := p.getRaw(ctx, p.form4URL(cik, f.accession, "primary_doc.xml"), 1<<20); err == nil {
		var cp coverPage
		if xml.Unmarshal(raw, &cp) == nil {
			period, _ = parseQuarterEnd(cp.PeriodOfReport)
		}
	}
	if period.IsZero() {
		// Fall back to the filing date less the 45-day reporting lag, which is
		// approximately the quarter it covers. Better than discarding the filing.
		period = f.filed.AddDate(0, 0, -45)
	}

	raw, err := p.getRaw(ctx, table, thirteenFMaxTable)
	if err != nil {
		return nil, time.Time{}, err
	}
	var t informationTable
	if err := xml.Unmarshal(raw, &t); err != nil {
		return nil, time.Time{}, fmt.Errorf("%w: 13F table: %v", ErrUnavailable, err)
	}
	return t.byCUSIP(), period, nil
}

// thirteenFTableURL finds the information table inside a filing's folder: the
// one XML document that is not the cover page.
func (p *edgarProvider) thirteenFTableURL(ctx context.Context, cik, accession string) (string, error) {
	base := fmt.Sprintf("%s/Archives/edgar/data/%s/%s",
		p.tickersBase, strings.TrimLeft(cik, "0"), strings.ReplaceAll(accession, "-", ""))
	var listing struct {
		Directory struct {
			Item []struct {
				Name string `json:"name"`
			} `json:"item"`
		} `json:"directory"`
	}
	if err := p.getJSON(ctx, base+"/index.json", &listing); err != nil {
		return "", err
	}
	for _, it := range listing.Directory.Item {
		name := strings.TrimSpace(it.Name)
		if strings.HasSuffix(name, ".xml") && name != "primary_doc.xml" {
			return base + "/" + name, nil
		}
	}
	return "", fmt.Errorf("%w: no information table in %s", ErrUnavailable, accession)
}

// getRaw is the SEC-identified GET for documents rather than JSON.
func (p *edgarProvider) getRaw(ctx context.Context, url string, limit int64) ([]byte, error) {
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
		return nil, fmt.Errorf("%w: HTTP %d from %s", ErrUnavailable, resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// parseQuarterEnd reads the period a 13F covers. The cover page writes
// MM-DD-YYYY, which neither of the other EDGAR date shapes matches.
func parseQuarterEnd(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"01-02-2006", "01/02/2006", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// issuerCUSIP recovers a ticker's CUSIP from the issuer's own structured 13D/G
// filings, which carry it on the cover page. Cached forever once found: a CUSIP
// does not change.
//
// The scan is deliberately not bounded by stakeLookbackDays. The activist leg
// wants recent filings because it is reading news; this wants any filing ever,
// because it is reading an identifier.
func (p *edgarProvider) issuerCUSIP(ctx context.Context, cik string) (string, bool) {
	var cached string
	if p.cache != nil {
		if ok, err := p.cache.Get("sec", "edgar", "cusip", cik, &cached); ok && err == nil && cached != "" {
			return cached, true
		}
	}
	filings, err := p.recentFilingsAny(ctx, cik, stakeForms, 3650, 4)
	if err != nil {
		return "", false
	}
	issuer := p.issuerName(ctx, cik)
	for _, f := range filings {
		doc, err := p.fetchStakeDoc(ctx, cik, f.accession, f.document)
		if err != nil {
			continue
		}
		// The same feed carries schedules this issuer filed as a holder of
		// somebody else, and their cover pages carry somebody else's CUSIP.
		// Taking one of those here would bind this ticker to another company's
		// identifier permanently — this result is cached forever, and it is the
		// key the tracked-manager 13F leg joins on.
		holder := ""
		if s, ok := doc.stake(f.form, f.url, f.filed); ok {
			holder = s.Holder
		}
		if about, _ := doc.aboutIssuer(cik, issuer, holder); !about {
			continue
		}
		for _, c := range doc.IssuerCusips {
			if v := normalizeCUSIP(c); v != "" {
				if p.cache != nil {
					_ = p.cache.Set("sec", "edgar", "cusip", cik, v)
				}
				return v, true
			}
		}
	}
	return "", false
}

// classifyInstitutionalHoldings reduces the tracked managers' positions in one
// issuer to a verdict.
//
// It reads changes, never holdings. "Berkshire owns Apple" is true every quarter
// and says nothing about the next fortnight; "Elliott opened a position last
// quarter" has a date on it. And even a change only votes when it is large
// enough in dollars to be a decision and recent enough to still describe a book
// somebody holds.
func classifyInstitutionalHoldings(positions []thirteenFPosition, idx *thirteenFIndex) insiderSignal {
	if idx == nil {
		return insiderSignal{InsiderNone, insiderNoSignal + ": the tracked-manager 13F index is unavailable this run"}
	}
	stale := idx.staleDays()
	if stale > thirteenFStaleDays {
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: the newest 13F quarter on file ended %s, %d days ago — past the %d-day point at which a quarterly snapshot describes a book nobody still holds",
			insiderNoSignal, idx.Quarter, stale, thirteenFStaleDays)}
	}
	if len(positions) == 0 {
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: none of the %d tracked managers reported a position as of %s",
			insiderNoSignal, idx.Filers, idx.Quarter)}
	}

	var opened, closed []thirteenFPosition
	held := 0
	for _, p := range positions {
		switch p.change() {
		case "opened", "added to":
			if p.ValueUSD >= thirteenFMinValueUSD {
				opened = append(opened, p)
			}
		case "closed", "cut":
			if p.sizeUSD() >= thirteenFMinValueUSD {
				closed = append(closed, p)
			}
		default:
			held++
		}
	}
	sortByValue(opened)
	sortByValue(closed)

	switch {
	case len(opened) > 0 && len(closed) == 0:
		return insiderSignal{InsiderBullish, fmt.Sprintf(
			"bullish: %s as of the quarter ended %s (%d days stale)%s",
			moveList(opened), idx.Quarter, stale, heldNote(held))}
	case len(closed) > 0 && len(opened) == 0:
		return insiderSignal{InsiderBearish, fmt.Sprintf(
			"bearish: %s as of the quarter ended %s (%d days stale)%s",
			moveList(closed), idx.Quarter, stale, heldNote(held))}
	case len(opened) > 0 && len(closed) > 0:
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: tracked managers moved both ways last quarter — %s, while %s",
			insiderNoSignal, moveList(opened), moveList(closed))}
	default:
		return insiderSignal{InsiderNone, fmt.Sprintf(
			"%s: %d tracked manager(s) held a position as of %s with no material change in the quarter — a holding is not a decision",
			insiderNoSignal, held, idx.Quarter)}
	}
}

func sortByValue(ps []thirteenFPosition) {
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].sizeUSD() > ps[j].sizeUSD() })
}

func moveList(ps []thirteenFPosition) string {
	var parts []string
	for i, p := range ps {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("and %d more", len(ps)-3))
			break
		}
		parts = append(parts, fmt.Sprintf("%s %s a $%s position", p.Filer, p.change(), usd(p.sizeUSD())))
	}
	return strings.Join(parts, ", ")
}

func heldNote(held int) string {
	if held == 0 {
		return ""
	}
	return fmt.Sprintf("; %d further tracked manager(s) held without a material change", held)
}

// thirteenFSummary is the human-readable sentence stored beside the verdict.
func thirteenFSummary(positions []thirteenFPosition, idx *thirteenFIndex) string {
	if idx == nil {
		return "tracked-manager 13F holdings unavailable this run"
	}
	if len(positions) == 0 {
		return fmt.Sprintf("none of %d tracked managers reported a position as of the quarter ended %s (%d days stale)",
			idx.Filers, idx.Quarter, idx.staleDays())
	}
	sortByValue(positions)
	var b strings.Builder
	fmt.Fprintf(&b, "%d of %d tracked managers held this name as of the quarter ended %s (%d days stale):",
		len(positions), idx.Filers, idx.Quarter, idx.staleDays())
	for i, p := range positions {
		if i == 5 {
			fmt.Fprintf(&b, " and %d more;", len(positions)-5)
			break
		}
		fmt.Fprintf(&b, " %s %s $%s (%s sh)", p.Filer, p.change(), usd(p.sizeUSD()), humanShares(p.Shares))
		if i < len(positions)-1 {
			b.WriteString(";")
		}
	}
	return b.String()
}

// jsonCompact is used by the cache round-trip test to assert the index survives
// serialisation, which is what the quarter-long disk cache depends on.
func (x *thirteenFIndex) jsonCompact() ([]byte, error) { return json.Marshal(x) }
