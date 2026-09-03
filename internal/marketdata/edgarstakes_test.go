package marketdata

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
	"time"
)

func parseStake(t *testing.T, path, form string, filed time.Time) stakeFiling {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc stakeDocument
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	s, ok := doc.stake(form, "https://example.com/13", filed)
	if !ok {
		t.Fatalf("%s produced no stake record", path)
	}
	return s
}

func TestStakeParsesALive13D(t *testing.T) {
	// The two schedules are separate XML schemas with different element names
	// for the same numbers, so both are pinned to a real filing. This is
	// accession 0001213900-26-078511, fetched verbatim.
	s := parseStake(t, "testdata/sched13d_sample.xml", "SCHEDULE 13D",
		time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC))

	if s.Holder != "Swift Prime Limited" {
		t.Errorf("holder = %q — a 13D lists co-filers on one stake and the largest should lead", s.Holder)
	}
	if s.Percent != 60.6 {
		t.Errorf("percent = %v, want 60.6", s.Percent)
	}
	if s.Shares != 5_000_000 {
		t.Errorf("shares = %v, want 5,000,000", s.Shares)
	}
	if !s.Activist {
		t.Error("a 13D did not read as activist")
	}
	if s.isAmendment() {
		t.Error("a fresh 13D read as an amendment")
	}
	if got := s.EventDate.Format("2006-01-02"); got != "2026-07-14" {
		t.Errorf("event date = %s, want 2026-07-14 — the form writes MM/DD/YYYY", got)
	}
}

func TestStakeParsesALive13G(t *testing.T) {
	// Apple, accession 0002100119-26-000139. The 13G schema puts the holder
	// somewhere else entirely, which is the whole reason both paths exist.
	s := parseStake(t, "testdata/sched13g_sample.xml", "SCHEDULE 13G",
		time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC))

	if s.Holder != "Vanguard Capital Management" {
		t.Errorf("holder = %q", s.Holder)
	}
	if s.Percent != 7.48 {
		t.Errorf("percent = %v, want 7.48", s.Percent)
	}
	if s.PersonType != "IA" {
		t.Errorf("person type = %q, want IA", s.PersonType)
	}
	if s.Activist {
		t.Error("a 13G read as activist — that is the distinction the leg is built on")
	}
	if got := s.EventDate.Format("2006-01-02"); got != "2026-03-31" {
		t.Errorf("event date = %s, want 2026-03-31", got)
	}
}

func TestActivistStakesReadANew13DAsBullish(t *testing.T) {
	stakes := []stakeFiling{{
		Holder: "ELLIOTT ASSOCIATES", Percent: 6.2, Shares: 1.2e7, PersonType: "PN",
		Activist: true, Form: "SCHEDULE 13D",
		Filed: time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC),
	}}
	got := classifyActivistStakes(stakes)
	if got.Bias != InsiderBullish {
		t.Fatalf("bias = %s, want bullish: %s", got.Bias, got.Reason)
	}
	for _, want := range []string{"ELLIOTT ASSOCIATES", "6.2%", "2026-08-20", "a partnership"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("reason is missing %q: %s", want, got.Reason)
		}
	}
}

func TestActivistStakesAbstainOnPassive13Gs(t *testing.T) {
	// The failure this leg is written to avoid: Vanguard and BlackRock hold 5%
	// of every mega-cap, so counting a 13G as whale activity would put the same
	// signal on every large name in the shortlist at once.
	stakes := []stakeFiling{
		{Holder: "Vanguard Capital Management", Percent: 7.5, Form: "SCHEDULE 13G", Filed: time.Now()},
		{Holder: "BlackRock Inc.", Percent: 6.4, Form: "SCHEDULE 13G", Filed: time.Now()},
	}
	got := classifyActivistStakes(stakes)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none on passive filings: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "index owning the index") {
		t.Errorf("the reason does not say why a 13G is not a signal: %s", got.Reason)
	}
	if !strings.Contains(got.Reason, "Vanguard Capital Management") {
		t.Errorf("the reason does not name the holders it dismissed: %s", got.Reason)
	}
}

func TestActivistStakesAbstainOnAnAmendment(t *testing.T) {
	// A 13D/A is as often a wind-down as an increase, and nothing in the filing
	// itself says which without diffing against the prior one.
	stakes := []stakeFiling{
		{Holder: "AN ACTIVIST", Percent: 5.1, Activist: true, Form: "SCHEDULE 13D/A", Filed: time.Now()},
	}
	got := classifyActivistStakes(stakes)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none on an amendment: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "as often an exit as an increase") {
		t.Errorf("reason = %s", got.Reason)
	}
}

func TestActivistStakesAbstainOnAnEmptyWindow(t *testing.T) {
	got := classifyActivistStakes(nil)
	if got.Bias != InsiderNone {
		t.Fatalf("bias = %s, want none", got.Bias)
	}
	if !strings.Contains(got.Reason, "no 5%+ ownership schedule") {
		t.Errorf("reason = %s", got.Reason)
	}
}

func TestANew13DOutranksTheNoiseAroundIt(t *testing.T) {
	// A real filing arriving in a month full of February 13G/A amendments must
	// still be found.
	stakes := []stakeFiling{
		{Holder: "Vanguard", Percent: 8.0, Form: "SCHEDULE 13G/A", Filed: time.Now()},
		{Holder: "BlackRock", Percent: 7.0, Form: "SCHEDULE 13G/A", Filed: time.Now()},
		{Holder: "STARBOARD VALUE", Percent: 5.4, Activist: true, Form: "SCHEDULE 13D",
			Filed: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)},
	}
	got := classifyActivistStakes(stakes)
	if got.Bias != InsiderBullish {
		t.Fatalf("bias = %s, want bullish: %s", got.Bias, got.Reason)
	}
	if !strings.Contains(got.Reason, "STARBOARD VALUE") {
		t.Errorf("the 13D was lost among the amendments: %s", got.Reason)
	}
}

func TestStakesSummaryNamesEveryFiling(t *testing.T) {
	stakes := []stakeFiling{
		{Holder: "STARBOARD VALUE", Percent: 5.4, Shares: 3.2e6, Activist: true,
			Form: "SCHEDULE 13D", Filed: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)},
	}
	summary := stakesSummary(stakes)
	for _, want := range []string{"SCHEDULE 13D", "2026-08-25", "STARBOARD VALUE", "5.4%", "3.20M sh"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary is missing %q: %s", want, summary)
		}
	}
	if got := stakesSummary(nil); !strings.Contains(got, "no Schedule 13D or 13G") {
		t.Errorf("empty summary = %s", got)
	}
}

func TestActivistStakeLegJoinsThePositioningVerdict(t *testing.T) {
	td := TickerData{Ticker: "TEST", Facts: []Fact{
		{Label: ActivistStakeLabel, Value: stakesSummary(nil)},
		signalFact(ActivistStakeSignalLabel, classifyActivistStakes([]stakeFiling{{
			Holder: "ELLIOTT ASSOCIATES", Percent: 6.2, Activist: true,
			Form: "SCHEDULE 13D", Filed: time.Now(),
		}}), "computed", ""),
	}}
	addPositioningSignal(&td, 0)

	var verdict string
	for _, f := range td.Facts {
		if f.Label == PositioningSignalLabel {
			verdict = f.Value
		}
	}
	if !strings.Contains(verdict, "activist stakes —") {
		t.Errorf("the 13D/G leg is not in the combined verdict: %s", verdict)
	}
	if !strings.Contains(verdict, "activist stakes bullish") {
		t.Errorf("the leg's direction did not carry: %s", verdict)
	}
	if !HasPositioningSignal(td) {
		t.Error("a fresh 13D does not ground the sentiment domain")
	}
}

func TestStakeKeepsAScheduleFiledAgainstTheIssuer(t *testing.T) {
	// The 13G sample is Apple's: subject CIK 0000320193, filed by somebody else.
	// Fetched under Apple's own CIK it is exactly what this leg is for.
	raw, err := os.ReadFile("testdata/sched13g_sample.xml")
	if err != nil {
		t.Fatal(err)
	}
	var doc stakeDocument
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.issuerCIK(); got != "0000320193" {
		t.Fatalf("issuerCIK = %q, want Apple's 0000320193 — a 13G writes the element as `issuerCik`", got)
	}
	if about, why := doc.aboutIssuer("0000320193", "Apple Inc.", "Some Adviser LP"); !about {
		t.Errorf("a schedule filed against the issuer was dropped: %s", why)
	}
}

func TestStakeDropsAScheduleTheIssuerFiledOnSomebodyElse(t *testing.T) {
	// EDGAR's submissions document lists a CIK's filings in either role, and
	// recentFilingsAny filters on form name and date alone. On 2026-09-03 that
	// put "SCHEDULE 13D filed by Stellantis N.V. at 9.5%" on STLAM.MI and
	// carried the run's only bullish activist verdict.
	raw, err := os.ReadFile("testdata/sched13d_sample.xml")
	if err != nil {
		t.Fatal(err)
	}
	var doc stakeDocument
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// The 13D writes `issuerCIK`; the subject is AIOS Tech, CIK 0001603993.
	if got := doc.issuerCIK(); got != "0001603993" {
		t.Fatalf("issuerCIK = %q, want 0001603993", got)
	}
	about, why := doc.aboutIssuer("0000842180", "Banco Bilbao Vizcaya Argentaria, S.A.", "Swift Prime Limited")
	if about {
		t.Fatal("a schedule about a different subject company was kept")
	}
	if !strings.Contains(why, "AIOS Tech") {
		t.Errorf("the reason does not name the real subject: %s", why)
	}
}

func TestStakeDropsAScheduleWhoseReportingPersonIsTheIssuer(t *testing.T) {
	// The fallback for a cover page with no subject CIK. All three of the run's
	// bad rows named the issuer itself as the reporting person.
	for _, tc := range []struct{ issuer, holder string }{
		{"BANCO BILBAO VIZCAYA ARGENTARIA, S.A.", "Banco Bilbao Vizcaya Argentaria S.A."},
		{"QUALCOMM INC/DE", "QUALCOMM Incorporated"},
		{"Stellantis N.V.", "Stellantis N.V."},
	} {
		doc := &stakeDocument{}
		about, why := doc.aboutIssuer("", tc.issuer, tc.holder)
		if about {
			t.Errorf("%q filing on itself was kept as a stake in itself", tc.holder)
			continue
		}
		if !strings.Contains(why, "reporting person") {
			t.Errorf("%q: reason does not say why: %s", tc.holder, why)
		}
	}

	// And the match is strict enough not to delete a real filer.
	doc := &stakeDocument{}
	if about, why := doc.aboutIssuer("", "QUALCOMM INC/DE", "STATE STREET CORPORATION"); !about {
		t.Errorf("State Street's genuine 13G on Qualcomm was dropped: %s", why)
	}
	// A shared leading word is not an identity: "State Street" is not "State
	// Auto Financial", and one-token matches have to be distinctive.
	if sameEntityName("State Street Corporation", "State Auto Financial Corp") {
		t.Error("two unrelated companies matched on a shared leading word")
	}
	if sameEntityName("Apex Co", "Apex Holdings Group") != true {
		t.Error("the same company under two corporate suffixes did not match")
	}
}
