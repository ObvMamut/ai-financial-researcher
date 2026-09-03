package marketdata

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// freshIndex is an index whose quarter is recent enough to be readable, so the
// staleness gate is not what a test is accidentally measuring.
func freshIndex(positions map[string][]thirteenFPosition) *thirteenFIndex {
	return &thirteenFIndex{
		ByCUSIP: positions,
		Quarter: time.Now().UTC().AddDate(0, 0, -60).Format("2006-01-02"),
		Filers:  len(thirteenFFilers),
	}
}

func TestInformationTableSumsRepeatedCUSIPs(t *testing.T) {
	// A manager files the same CUSIP once per sub-adviser; Berkshire's Ally
	// position spans three rows. Taking the last row instead of the sum would
	// under-report the position by whatever the other rows held.
	raw := []byte(`<informationTable>
  <infoTable><nameOfIssuer>ALLY FINL INC</nameOfIssuer><cusip>02005N100</cusip>
    <value>577211815</value><shrsOrPrnAmt><sshPrnamt>12561737</sshPrnamt><sshPrnamtType>SH</sshPrnamtType></shrsOrPrnAmt></infoTable>
  <infoTable><nameOfIssuer>ALLY FINL INC</nameOfIssuer><cusip>02005N100</cusip>
    <value>128838056</value><shrsOrPrnAmt><sshPrnamt>2803875</sshPrnamt><sshPrnamtType>SH</sshPrnamtType></shrsOrPrnAmt></infoTable>
  <infoTable><nameOfIssuer>SOME BOND</nameOfIssuer><cusip>11111X100</cusip>
    <value>900000000</value><shrsOrPrnAmt><sshPrnamt>900000</sshPrnamt><sshPrnamtType>PRN</sshPrnamtType></shrsOrPrnAmt></infoTable>
</informationTable>`)
	var it informationTable
	if err := xml.Unmarshal(raw, &it); err != nil {
		t.Fatal(err)
	}
	got := it.byCUSIP()
	ally := got["02005N100"]
	if ally.Shares != 12561737+2803875 {
		t.Errorf("shares = %v, want the sum of both rows", ally.Shares)
	}
	if ally.Value != 577211815+128838056 {
		t.Errorf("value = %v, want the sum of both rows", ally.Value)
	}
	// A principal amount is a bond. Adding it to a share count produces a
	// number that means nothing, so the row is dropped.
	if _, ok := got["11111X100"]; ok {
		t.Error("a PRN row was counted as a share position")
	}
}

func TestQuarterEndParsesTheCoverPageShape(t *testing.T) {
	// The 13F cover writes MM-DD-YYYY, which matches neither of the other two
	// EDGAR date shapes.
	for _, s := range []string{"06-30-2026", "06/30/2026", "2026-06-30"} {
		got, ok := parseQuarterEnd(s)
		if !ok || got.Format("2006-01-02") != "2026-06-30" {
			t.Errorf("parseQuarterEnd(%q) = %v, %v", s, got, ok)
		}
	}
	if _, ok := parseQuarterEnd("Q2 2026"); ok {
		t.Error("a shape nothing writes parsed anyway")
	}
}

func TestPositionChangeReadsTheQuarterOverQuarterMove(t *testing.T) {
	cases := []struct {
		name          string
		shares, prior float64
		want          string
	}{
		{"new position", 1000, 0, "opened"},
		{"exit", 0, 1000, "closed"},
		{"doubled", 2000, 1000, "added to"},
		{"halved", 400, 1000, "cut"},
		{"trimmed 10%", 900, 1000, "held"},
		{"unknown prior", 1000, -1, "held"},
	}
	for _, c := range cases {
		p := thirteenFPosition{Shares: c.shares, PriorShares: c.prior}
		if got := p.change(); got != c.want {
			t.Errorf("%s: change = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestInstitutionalAbstainsOnAStaleQuarter(t *testing.T) {
	// The gate that keeps this leg honest: past two quarters of trading the
	// snapshot describes a book nobody still holds.
	idx := &thirteenFIndex{
		ByCUSIP: map[string][]thirteenFPosition{},
		Quarter: time.Now().UTC().AddDate(0, 0, -200).Format("2006-01-02"),
		Filers:  20,
	}
	got := classifyInstitutionalHoldings([]thirteenFPosition{
		{Filer: "Elliott", Shares: 1e6, PriorShares: 0, ValueUSD: 4e8},
	}, idx)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none on a stale quarter: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "nobody still holds") {
		t.Errorf("the reason does not name the staleness: %s", got.Reason)
	}
}

func TestInstitutionalAbstainsOnAMereHolding(t *testing.T) {
	// "Berkshire owns Apple" is true every quarter and carries no date. Only a
	// change votes.
	got := classifyInstitutionalHoldings([]thirteenFPosition{
		{Filer: "Berkshire Hathaway", Shares: 3e8, PriorShares: 3.05e8, ValueUSD: 6e10},
	}, freshIndex(nil))
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "a holding is not a decision") {
		t.Errorf("reason = %s", got.Reason)
	}
}

func TestInstitutionalReadsANewPositionAsBullish(t *testing.T) {
	got := classifyInstitutionalHoldings([]thirteenFPosition{
		{Filer: "Elliott Investment Management", Shares: 5e6, PriorShares: 0, ValueUSD: 4e8},
		{Filer: "Berkshire Hathaway", Shares: 1e6, PriorShares: 1e6, ValueUSD: 9e7},
	}, freshIndex(nil))
	if got.Bias != InsiderBullish {
		t.Fatalf("bias = %s, want bullish: %s", got.Bias, got.Reason)
	}
	for _, want := range []string{"Elliott Investment Management opened", "days stale", "held without a material change"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("reason is missing %q: %s", want, got.Reason)
		}
	}
}

func TestInstitutionalIgnoresATokenNewPosition(t *testing.T) {
	// A manager opening a $4M line in a mega-cap has not expressed a view.
	got := classifyInstitutionalHoldings([]thirteenFPosition{
		{Filer: "Citadel Advisors", Shares: 20000, PriorShares: 0, ValueUSD: 4e6},
	}, freshIndex(nil))
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none below the size floor: %s", got.Bias, got.Reason)
	}
}

func TestInstitutionalReadsAnExitAsBearish(t *testing.T) {
	// An exit is worth zero now by definition, so it has to be sized by what was
	// exited. Reading the current value would say "closed a $0 position", which
	// tells a reader nothing and falls under the size floor besides.
	got := classifyInstitutionalHoldings([]thirteenFPosition{
		{Filer: "Tiger Global Management", Shares: 0, PriorShares: 4e6, ValueUSD: 0, PriorValueUSD: 3.2e8},
	}, freshIndex(nil))
	if got.Bias != InsiderBearish {
		t.Fatalf("bias = %s, want bearish: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "Tiger Global Management closed a $320.00M position") {
		t.Errorf("the exit is not sized by what was exited: %s", got.Reason)
	}
}

func TestInstitutionalIgnoresATokenExit(t *testing.T) {
	got := classifyInstitutionalHoldings([]thirteenFPosition{
		{Filer: "Citadel Advisors", Shares: 0, PriorShares: 20000, ValueUSD: 0, PriorValueUSD: 4e6},
	}, freshIndex(nil))
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none below the size floor: %s", got.Bias, got.Reason)
	}
}

func TestInstitutionalAbstainsWhenManagersDisagree(t *testing.T) {
	got := classifyInstitutionalHoldings([]thirteenFPosition{
		{Filer: "Elliott Investment Management", Shares: 5e6, PriorShares: 0, ValueUSD: 4e8},
		{Filer: "Tiger Global Management", Shares: 0, PriorShares: 4e6, ValueUSD: 0, PriorValueUSD: 3.2e8},
	}, freshIndex(nil))
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none when the tape moves both ways: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "moved both ways") {
		t.Errorf("reason = %s", got.Reason)
	}
}

func TestInstitutionalAbstainsWithNoIndex(t *testing.T) {
	got := classifyInstitutionalHoldings(nil, nil)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none", got.Bias)
	}
	if !strings.Contains(got.Reason, "index is unavailable") {
		t.Errorf("reason = %s", got.Reason)
	}
}

func TestThirteenFIndexSurvivesTheDiskCache(t *testing.T) {
	// The index is kept for a quarter on disk, so the round trip is what the
	// whole build cost depends on being paid once.
	idx := freshIndex(map[string][]thirteenFPosition{
		"037833100": {{Filer: "Berkshire Hathaway", Shares: 3e8, ValueUSD: 6e10, PriorShares: 3e8, Quarter: "2026-06-30"}},
	})
	raw, err := idx.jsonCompact()
	if err != nil {
		t.Fatal(err)
	}
	var back thirteenFIndex
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got := back.ByCUSIP["037833100"]
	if len(got) != 1 || got[0].Filer != "Berkshire Hathaway" || got[0].Shares != 3e8 {
		t.Fatalf("round trip lost the position: %+v", got)
	}
	if back.Quarter != idx.Quarter {
		t.Errorf("round trip lost the quarter: %q", back.Quarter)
	}
}

func TestTrackedFilersAreDistinctAndPadded(t *testing.T) {
	// A duplicate CIK would double-count one manager's move, and an unpadded one
	// 404s against data.sec.gov's zero-padded submissions path.
	seen := map[string]string{}
	for _, f := range thirteenFFilers {
		if len(f.CIK) != 10 {
			t.Errorf("%s: CIK %q is not zero-padded to 10 digits", f.Name, f.CIK)
		}
		if prev, dup := seen[f.CIK]; dup {
			t.Errorf("CIK %s is listed twice: %s and %s", f.CIK, prev, f.Name)
		}
		seen[f.CIK] = f.Name
		if strings.TrimSpace(f.Name) == "" {
			t.Errorf("CIK %s has no display name — it would appear unattributed in a verdict", f.CIK)
		}
	}
}

func TestCUSIPNormalisationRejectsGarbage(t *testing.T) {
	if got := normalizeCUSIP(" 037833100 "); got != "037833100" {
		t.Errorf("normalizeCUSIP = %q", got)
	}
	for _, bad := range []string{"", "037", "037833100037833100"} {
		if got := normalizeCUSIP(bad); got != "" {
			t.Errorf("normalizeCUSIP(%q) = %q, want empty", bad, got)
		}
	}
}

func TestInstitutionalLegJoinsThePositioningVerdict(t *testing.T) {
	td := TickerData{Ticker: "TEST", Facts: []Fact{
		{Label: InstitutionalLabel, Value: thirteenFSummary(nil, freshIndex(nil))},
		signalFact(InstitutionalSignalLabel, classifyInstitutionalHoldings([]thirteenFPosition{
			{Filer: "Elliott Investment Management", Shares: 5e6, PriorShares: 0, ValueUSD: 4e8},
		}, freshIndex(nil)), "computed", ""),
	}}
	addPositioningSignal(&td, 0)

	var verdict string
	for _, f := range td.Facts {
		if f.Label == PositioningSignalLabel {
			verdict = f.Value
		}
	}
	if !strings.Contains(verdict, "institutional —") {
		t.Errorf("the 13F leg is not in the combined verdict: %s", verdict)
	}
	if !strings.Contains(verdict, "institutional bullish") {
		t.Errorf("the leg's direction did not carry: %s", verdict)
	}
}

// secStub serves the three documents one tracked filer's 13F build reads, and
// cancels the build's context once it has answered for the first filer.
type secStub struct {
	cancel   func()
	firstCIK string
	mu       sync.Mutex
	served   int
}

func (s *secStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/submissions/"):
		// Only the first tracked filer has anything to report. Once it is fully
		// in the index the run is cut short, which is what a stage timeout or a
		// ctrl-C does to the other 22. The cancel fires here rather than while
		// the first filer's last document is still on the wire, so that filer
		// lands complete and the truncation is unambiguous.
		if !strings.Contains(path, s.firstCIK) {
			s.mu.Lock()
			done := s.served > 0
			s.mu.Unlock()
			if done {
				s.cancel()
			}
			http.NotFound(w, r)
			return
		}
		filed := time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02")
		fmt.Fprintf(w, `{"filings":{"recent":{
			"accessionNumber":["0000000000-26-000001"],
			"filingDate":[%q],
			"form":["13F-HR"],
			"primaryDocument":["primary_doc.xml"]}}}`, filed)
	case strings.HasSuffix(path, "/index.json"):
		fmt.Fprint(w, `{"directory":{"item":[{"name":"primary_doc.xml"},{"name":"table.xml"}]}}`)
	case strings.HasSuffix(path, "/primary_doc.xml"):
		q := time.Now().UTC().AddDate(0, 0, -60).Format("01-02-2006")
		fmt.Fprintf(w, `<edgarSubmission><periodOfReport>%s</periodOfReport></edgarSubmission>`, q)
	case strings.HasSuffix(path, "/table.xml"):
		fmt.Fprint(w, `<informationTable><infoTable>
			<cusip>037833100</cusip>
			<shrsOrPrnAmt><sshPrnamt>1000000</sshPrnamt></shrsOrPrnAmt>
			<value>50000</value>
		</infoTable></informationTable>`)
		s.mu.Lock()
		s.served++
		s.mu.Unlock()
	default:
		http.NotFound(w, r)
	}
}

func TestAnInterruptedThirteenFBuildIsNotCachedForAQuarter(t *testing.T) {
	// The build walks 23 managers and breaks out the moment the context is
	// done, then returns whatever it had. Caching that put a *partial* index on
	// disk under the 90-day TTL, so a single cancelled or timed-out run left
	// every run for the next quarter reading a two-manager index that reports
	// itself as the tracked set. The truncation is invisible: an index with 2
	// filers and one with 23 have the same shape.
	stub := &secStub{firstCIK: strings.TrimSpace(thirteenFFilers[0].CIK)}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub.cancel = cancel

	dir := t.TempDir()
	cache := NewCache(dir)
	p := &edgarProvider{
		client:       srv.Client(),
		contactEmail: "test@example.com",
		cache:        cache,
		factsBase:    srv.URL,
		tickersBase:  srv.URL,
		limiter:      NewLimiter(1<<30, edgarRequestsPerSecond*60, edgarRequestsPerSecond),
		submissions:  map[string]*submissionsResp{},
	}

	idx := p.thirteenF(ctx)
	if idx == nil || idx.Filers == 0 {
		t.Fatalf("stub did not produce a partial index (filers=%v) — the test is not exercising the interrupted path", idx)
	}
	if idx.Filers >= len(thirteenFFilers) {
		t.Fatalf("build was not interrupted: %d filers of %d", idx.Filers, len(thirteenFFilers))
	}

	// A fresh provider on the same cache must not be handed the truncated index.
	var cached thirteenFIndex
	ok, err := cache.GetTTL("sec", "edgar", "13f", "tracked", 90*24*time.Hour, &cached)
	if err != nil {
		t.Fatalf("cache read: %v", err)
	}
	if ok {
		t.Errorf("a build interrupted after %d of %d filers was cached for 90 days", idx.Filers, len(thirteenFFilers))
	}
}

func TestACompleteThirteenFBuildIsStillCached(t *testing.T) {
	// The other half of the rule. Refusing to cache an interrupted build is only
	// correct if a finished one is still written: the ~115 requests the walk
	// costs are the whole reason for the 90-day TTL, and paying them on every
	// run would be a worse bug than the one being fixed.
	stub := &secStub{firstCIK: strings.TrimSpace(thirteenFFilers[0].CIK), cancel: func() {}}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	dir := t.TempDir()
	cache := NewCache(dir)
	p := &edgarProvider{
		client:       srv.Client(),
		contactEmail: "test@example.com",
		cache:        cache,
		factsBase:    srv.URL,
		tickersBase:  srv.URL,
		limiter:      NewLimiter(1<<30, edgarRequestsPerSecond*60, edgarRequestsPerSecond),
		submissions:  map[string]*submissionsResp{},
	}

	idx := p.thirteenF(context.Background())
	if idx == nil || idx.Filers != 1 {
		t.Fatalf("want the one filer the stub serves, got %+v", idx)
	}
	var cached thirteenFIndex
	ok, err := cache.GetTTL("sec", "edgar", "13f", "tracked", 90*24*time.Hour, &cached)
	if err != nil {
		t.Fatalf("cache read: %v", err)
	}
	if !ok {
		t.Fatal("a build that walked every tracked filer was not cached")
	}
	if len(cached.ByCUSIP["037833100"]) != 1 {
		t.Errorf("cached index lost the position: %+v", cached.ByCUSIP)
	}
}
