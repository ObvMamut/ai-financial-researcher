package marketdata

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
	"time"
)

// notice144 builds a Form 144 XML document from the parts a test cares about,
// in the element names the live filings actually use.
func notice144(owner, relationship, units, value, saleDate, nature, planDate string) []byte {
	plan := ""
	if planDate != "" {
		plan = "<planAdoptionDates><planAdoptionDate>" + planDate + "</planAdoptionDate></planAdoptionDates>"
	}
	return []byte(`<edgarSubmission xmlns="http://www.sec.gov/edgar/ownership">
  <formData>
    <issuerInfo>
      <issuerName>TEST CORP</issuerName>
      <nameOfPersonForWhoseAccountTheSecuritiesAreToBeSold>` + owner + `</nameOfPersonForWhoseAccountTheSecuritiesAreToBeSold>
      <relationshipsToIssuer><relationshipToIssuer>` + relationship + `</relationshipToIssuer></relationshipsToIssuer>
    </issuerInfo>
    <securitiesInformation>
      <securitiesClassTitle>Common</securitiesClassTitle>
      <noOfUnitsSold>` + units + `</noOfUnitsSold>
      <aggregateMarketValue>` + value + `</aggregateMarketValue>
      <approxSaleDate>` + saleDate + `</approxSaleDate>
    </securitiesInformation>
    <securitiesToBeSold>
      <natureOfAcquisitionTransaction>` + nature + `</natureOfAcquisitionTransaction>
    </securitiesToBeSold>
    <noticeSignature><noticeDate>` + saleDate + `</noticeDate>` + plan + `</noticeSignature>
  </formData>
</edgarSubmission>`)
}

func parse144(t *testing.T, raw []byte) form144Notice {
	t.Helper()
	var doc form144Document
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	n, ok := doc.notice("https://example.com/144", time.Now())
	if !ok {
		t.Fatal("document produced no notice")
	}
	return n
}

func TestForm144ParsesALiveFiling(t *testing.T) {
	// The element names in the SEC's own documentation and the ones in the
	// filings disagree — it is `noOfUnitsSold`, not `unitsToBeSold` — so the
	// parser is pinned to a filing rather than to a schema. This is Apple's
	// accession 0001950047-26-007959, fetched verbatim.
	raw, err := os.ReadFile("testdata/form144_aapl.xml")
	if err != nil {
		t.Fatal(err)
	}
	n := parse144(t, raw)

	if n.Owner != "JENNIFER NEWSTEAD" {
		t.Errorf("owner = %q", n.Owner)
	}
	if n.Relationship != "Officer" {
		t.Errorf("relationship = %q", n.Relationship)
	}
	if n.Shares != 8632 {
		t.Errorf("shares = %v, want 8632", n.Shares)
	}
	if n.ValueUSD != 2660900.32 {
		t.Errorf("value = %v, want 2660900.32", n.ValueUSD)
	}
	if got := n.SaleDate.Format("2006-01-02"); got != "2026-08-11" {
		t.Errorf("sale date = %s, want 2026-08-11 — the form writes MM/DD/YYYY", got)
	}
	if got := n.PlanAdopted.Format("2006-01-02"); got != "2026-05-05" {
		t.Errorf("plan adoption = %s, want 2026-05-05", got)
	}
	if !n.scheduled() {
		t.Error("RSUs sold under a plan adopted three months earlier did not read as scheduled")
	}
}

func TestForm144SchedulingIsReadFromEitherMark(t *testing.T) {
	cases := []struct {
		name, nature, plan string
		want               bool
	}{
		{"plan alone", "Open Market Purchase", "01/02/2026", true},
		{"vesting alone", "Restricted Stock Units", "", true},
		{"option exercise", "Stock Option Exercise", "", true},
		{"neither", "Open Market Purchase", "", false},
		{"gift, long held", "Gift", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := parse144(t, notice144("A SELLER", "Officer", "1000", "1000000", "08/01/2026", c.nature, c.plan))
			if got := n.scheduled(); got != c.want {
				t.Errorf("scheduled = %v, want %v (nature %q, plan %q)", got, c.want, c.nature, c.plan)
			}
		})
	}
}

func TestPlannedSalesAbstainOnTheCompensationCalendar(t *testing.T) {
	// The whole point of the leg. Four officers selling vesting stock under
	// plans adopted in advance is the resting state of a large-cap issuer, and
	// scoring it bearish is the levy this classifier exists to avoid.
	var notices []form144Notice
	for _, owner := range []string{"A", "B", "C", "D"} {
		notices = append(notices, parse144(t, notice144(owner, "Officer", "10000", "9000000", "08/01/2026", "Restricted Stock Units", "05/01/2026")))
	}
	got := classifyPlannedSales(notices)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "compensation calendar") {
		t.Errorf("the reason does not say why it abstained: %s", got.Reason)
	}
}

func TestPlannedSalesReadALargeUnscheduledNoticeAsBearish(t *testing.T) {
	notices := []form144Notice{
		parse144(t, notice144("BIG SELLER", "Officer", "200000", "12000000", "08/01/2026", "Open Market Purchase", "")),
		parse144(t, notice144("ROUTINE", "Officer", "1000", "500000", "08/01/2026", "Restricted Stock Units", "05/01/2026")),
	}
	got := classifyPlannedSales(notices)
	if got.Bias != InsiderBearish {
		t.Fatalf("bias = %s, want bearish: %s", got.Bias, got.Reason)
	}
	for _, want := range []string{"BIG SELLER (Officer)", "no 10b5-1 plan", "2026-08-01"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("reason is missing %q: %s", want, got.Reason)
		}
	}
	// And the scheduled notice did not inflate the number the verdict rests on.
	if strings.Contains(got.Reason, "12.50M") {
		t.Errorf("the scheduled notice was counted into the discretionary total: %s", got.Reason)
	}
}

func TestPlannedSalesReadBreadthAsBearish(t *testing.T) {
	// Two unscheduled sellers, neither large enough alone.
	notices := []form144Notice{
		parse144(t, notice144("FIRST", "Director", "5000", "900000", "08/01/2026", "Gift", "")),
		parse144(t, notice144("SECOND", "Officer", "5000", "900000", "08/02/2026", "Gift", "")),
	}
	got := classifyPlannedSales(notices)
	if got.Bias != InsiderBearish {
		t.Fatalf("bias = %s, want bearish on breadth: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "2 insiders") {
		t.Errorf("the reason does not name the breadth: %s", got.Reason)
	}
}

func TestPlannedSalesAbstainOnOneSmallUnscheduledNotice(t *testing.T) {
	notices := []form144Notice{
		parse144(t, notice144("LONE SELLER", "Director", "5000", "900000", "08/01/2026", "Gift", "")),
	}
	got := classifyPlannedSales(notices)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none — neither large nor broad: %s", got.Bias, got.Reason)
	}
}

func TestPlannedSalesAbstainOnAnEmptyWindow(t *testing.T) {
	got := classifyPlannedSales(nil)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none", got.Bias)
	}
	if !strings.Contains(got.Reason, "no Form 144 notices") {
		t.Errorf("reason = %s", got.Reason)
	}
}

func TestPlannedSalesLegJoinsThePositioningVerdict(t *testing.T) {
	td := TickerData{Ticker: "TEST", Facts: []Fact{
		{Label: PlannedSalesLabel, Value: plannedSalesSummary(nil)},
		signalFact(PlannedSalesSignalLabel, classifyPlannedSales([]form144Notice{
			{Owner: "BIG SELLER", Relationship: "Officer", ValueUSD: 12e6, Acquisition: "Gift"},
		}), "computed", ""),
	}}
	addPositioningSignal(&td, 0)

	var verdict string
	for _, f := range td.Facts {
		if f.Label == PositioningSignalLabel {
			verdict = f.Value
		}
	}
	if !strings.Contains(verdict, "planned sales —") {
		t.Errorf("the Form 144 leg is not in the combined verdict: %s", verdict)
	}
	if !strings.Contains(verdict, "planned sales bearish") {
		t.Errorf("the leg's direction did not carry: %s", verdict)
	}
}

func TestInsiderRolesAreReadFromTheReportedTitle(t *testing.T) {
	cases := map[string]insiderRole{
		"":                        roleUnknown,
		"director":                roleDirector,
		"10% owner":               roleTenPercent,
		"Chief Executive Officer": roleOfficer,
		"EVP, General Counsel":    roleOfficer,
	}
	for title, want := range cases {
		if got := (form4Transaction{Title: title}).role(); got != want {
			t.Errorf("role(%q) = %s, want %s", title, got, want)
		}
	}
}

func TestAnOfficerBuyClearsALowerBarThanADirectors(t *testing.T) {
	// $150k: over the officer bar, under the general one. The same purchase by a
	// director is not a signal, and that asymmetry is the point.
	officer := []form4Transaction{
		{Owner: "CEO", Title: "Chief Executive Officer", Shares: 1000, Price: 150, Buy: true},
	}
	if got := classifyInsiderActivity(officer); got.Bias != InsiderBullish {
		t.Errorf("an officer's $150k open-market buy is not bullish: %s", got.Reason)
	} else if !strings.Contains(got.Reason, "cannot diversify") {
		t.Errorf("the reason does not say why the officer bar is lower: %s", got.Reason)
	}

	director := []form4Transaction{
		{Owner: "A DIRECTOR", Title: "director", Shares: 1000, Price: 150, Buy: true},
	}
	if got := classifyInsiderActivity(director); got.Bias != InsiderNone {
		t.Errorf("a director's $150k buy should not clear the bar on its own: %s", got.Reason)
	}
}

func TestATenPercentOwnerAddingReadsAsAccumulation(t *testing.T) {
	txns := []form4Transaction{
		{Owner: "A FUND", Title: "10% owner", Shares: 10000, Price: 40, Buy: true},
	}
	got := classifyInsiderActivity(txns)
	if got.Bias != InsiderBullish {
		t.Fatalf("bias = %s, want bullish: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "accumulation") {
		t.Errorf("the reason does not name what a 10%% owner buying is: %s", got.Reason)
	}
}

func TestScheduledNoticesSuppressTheBreadthRead(t *testing.T) {
	// The live NKE read that produced this rule: 5 distinct insiders sold $400k
	// across a 45-day window — enough sellers to trip the breadth test — while
	// the same window's Form 144s were 5 notices worth the same $400k, every one
	// of them a 10b5-1 plan or vesting equity. The wider Form 4 window did not
	// find more information, it found more of the payroll.
	txns := []form4Transaction{
		{Owner: "A", Title: "PRESIDENT", Shares: 4867, Price: 42.05, SharesAfter: 200000},
		{Owner: "B", Title: "EVP: CFO", Shares: 2463, Price: 41.60, SharesAfter: 150000},
		{Owner: "C", Title: "EVP: COO", Shares: 890, Price: 41.60, SharesAfter: 90000},
		{Owner: "D", Title: "VP", Shares: 700, Price: 41.60, SharesAfter: 80000},
		{Owner: "E", Title: "VP", Shares: 600, Price: 41.60, SharesAfter: 70000},
	}
	var sellUSD float64
	for _, t := range txns {
		sellUSD += t.value()
	}

	// Without the notices, breadth alone reads bearish — which is the behaviour
	// on any issuer whose 144 index could not be read, so it must still hold.
	if got := classifyInsiderActivity(txns); got.Bias != InsiderBearish {
		t.Errorf("breadth with no Form 144 context should still read bearish: %s", got.Reason)
	}

	sched := scheduledContext{ScheduledUSD: sellUSD}
	got := classifyInsiderActivityWith(txns, sched)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none when every notice was scheduled: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "compensation calendar arriving together") {
		t.Errorf("the reason does not say what suppressed it: %s", got.Reason)
	}
}

func TestOneUnscheduledNoticeRestoresTheBreadthRead(t *testing.T) {
	// The suppression is about the window being *entirely* pre-declared. A single
	// discretionary notice means somebody chose their timing, and the breadth is
	// evidence again.
	txns := []form4Transaction{
		{Owner: "A", Title: "PRESIDENT", Shares: 5000, Price: 40, SharesAfter: 200000},
		{Owner: "B", Title: "EVP: CFO", Shares: 5000, Price: 40, SharesAfter: 150000},
		{Owner: "C", Title: "EVP: COO", Shares: 5000, Price: 40, SharesAfter: 90000},
	}
	sched := scheduledContext{ScheduledUSD: 500_000, UnscheduledUSD: 100_000}
	if got := classifyInsiderActivityWith(txns, sched); got.Bias != InsiderBearish {
		t.Errorf("one unscheduled notice should restore the breadth read: %s", got.Reason)
	}
}

func TestScheduledNoticesDoNotSuppressDepth(t *testing.T) {
	// A plan that takes a third of somebody's holding is still a third of their
	// holding gone. Only breadth is the calendar's artefact.
	txns := []form4Transaction{
		{Owner: "EXITING OFFICER", Title: "CFO", Shares: 60000, Price: 40, SharesAfter: 40000},
		{Owner: "B", Title: "VP", Shares: 100, Price: 40, SharesAfter: 90000},
		{Owner: "C", Title: "VP", Shares: 100, Price: 40, SharesAfter: 90000},
	}
	sched := scheduledContext{ScheduledUSD: 10_000_000}
	got := classifyInsiderActivityWith(txns, sched)
	if got.Bias != InsiderBearish {
		t.Fatalf("bias = %s, want bearish — depth survives a plan: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "of their own holding") {
		t.Errorf("reason = %s", got.Reason)
	}
}

func TestScheduledSplitTotalsTheWindow(t *testing.T) {
	notices := []form144Notice{
		{ValueUSD: 1e6, Acquisition: "Restricted Stock Units"},
		{ValueUSD: 2e6, PlanAdopted: time.Now().AddDate(0, -3, 0)},
		{ValueUSD: 5e5, Acquisition: "Gift"},
	}
	got := scheduledSplit(notices)
	if got.ScheduledUSD != 3e6 {
		t.Errorf("scheduled = %v, want 3M", got.ScheduledUSD)
	}
	if got.UnscheduledUSD != 5e5 {
		t.Errorf("unscheduled = %v, want 500k", got.UnscheduledUSD)
	}
	// An unscheduled notice present means the window was not fully pre-declared,
	// whatever the totals.
	if got.coversSelling(3e6) {
		t.Error("a window with an unscheduled notice claimed to cover its selling")
	}
	if (scheduledContext{ScheduledUSD: 3e6}).coversSelling(10e6) {
		t.Error("scheduled notices covering under half the Form 4 selling claimed to cover it")
	}
	if !(scheduledContext{ScheduledUSD: 3e6}).coversSelling(4e6) {
		t.Error("scheduled notices covering most of the selling did not count")
	}
}
