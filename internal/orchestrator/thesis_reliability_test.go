package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

func TestCompactReviewRequiresCompleteAttributionAndCurrentHash(t *testing.T) {
	d := supportedResearch().Dossier
	c := model.ThesisChallenge{ContractVersion: 2, Ticker: d.Ticker, DossierHash: dossierHash(d), Verdict: "supported", ClaimReviews: []model.ClaimReview{{ClaimID: "c1", Assessment: "supported", Attribution: "confirmed", Reason: "Source establishes issuer and update"}}}
	if p := validateReviewConsistency(d, c); len(p) != 0 {
		t.Fatal(p)
	}
	for _, variant := range []string{"hash", "missing", "duplicate", "unknown", "dispute", "attribution"} {
		t.Run(variant, func(t *testing.T) {
			v := c
			v.ClaimReviews = append([]model.ClaimReview(nil), c.ClaimReviews...)
			switch variant {
			case "hash":
				v.DossierHash = "old"
			case "missing":
				v.ClaimReviews = nil
			case "duplicate":
				v.ClaimReviews = append(v.ClaimReviews, v.ClaimReviews[0])
			case "unknown":
				v.ClaimReviews[0].ClaimID = "invented"
			case "dispute":
				v.ClaimReviews[0].Assessment = "disputed"
			case "attribution":
				v.ClaimReviews[0].Attribution = "disputed"
			}
			if len(validateReviewConsistency(d, v)) == 0 {
				t.Fatal("accepted invalid referenced review")
			}
		})
	}
	d.Mechanism = "a different mechanism"
	if len(validateReviewConsistency(d, c)) == 0 {
		t.Fatal("review survived revision")
	}
}

func TestInitialAndReviewPassagesReachPersistedInputs(t *testing.T) {
	quote := "TSMC raised its quarterly revenue guidance; delivery demand increased while overseas costs remain uncertain."
	source := model.EvidenceDocument{ID: "tsmc-source", Ticker: "AAA", Title: "TSMC quarterly revenue guidance", Kind: "document", URL: "https://issuer.example/tsmc", ReportingPeriod: "2026 Q3", Text: strings.Repeat("navigation menu cookie privacy. ", 200) + quote + strings.Repeat(" tail text", 200)}
	if strings.Index(source.Text, quote) <= 5339 {
		t.Fatal("fixture must exercise deep passage")
	}
	d := supportedResearch().Dossier
	d.ContractVersion = 2
	d.ExpectationsClaimIDs = []string{"c1"}
	d.PricedInClaimIDs = []string{"c1"}
	d.Claims = []model.ResearchClaim{{ID: "c1", Kind: "inference", Text: "Guidance changes expectations with cost uncertainty", EvidenceIDs: []string{source.ID}, Passages: []model.ClaimPassage{{EvidenceID: source.ID, Quote: quote, IssuerRole: "reporting issuer"}}}}
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		if !strings.Contains(prompt, quote) {
			t.Error("material source absent from engine input")
		}
		if strings.Contains(prompt, "# Independent thesis challenge") {
			return fenced(model.ThesisChallenge{ContractVersion: 2, Ticker: "AAA", DossierHash: dossierHash(d), Verdict: "supported", Reason: "Evidence supports conditional mechanism", ClaimReviews: []model.ClaimReview{{ClaimID: "c1", Assessment: "supported", Attribution: "confirmed", Reason: "Issuer update and uncertainty both retained"}}})
		}
		return fenced(d)
	})
	defer done()
	out := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA"}, []model.EvidenceDocument{source}, pack, nil)
	if out.Dossier.Status != "supported" {
		t.Fatalf("failed source research: %+v", out)
	}
	view, _, berr := chiefBoard([]thesisResearch{out}, 180978)
	if berr != nil {
		t.Fatal(berr)
	}
	_, profile, err := runner.preparePrompt("thesis-chief", "chief-fixture", singleSection(jsonText(view)), 8192)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Bytes > profile.InputLimit {
		t.Fatal("unbounded chief")
	}
	for _, name := range []string{"input-research-414141-round-1.json", "input-research-414141-challenge.json", "input-chief-fixture.json"} {
		b, err := os.ReadFile(filepath.Join(runner.run.Dir, "data", name))
		if err != nil {
			t.Fatal(err)
		}
		var input struct {
			Prompt string
			Data   string
		}
		if err = json.Unmarshal(b, &input); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(input.Prompt, quote) || !strings.Contains(input.Data, "selected_spans") {
			t.Fatalf("persisted input lacks source passage: %s", name)
		}
	}
}

func TestRoleBudgetsRejectBeforeDispatchAndKeepTwelveOutcomes(t *testing.T) {
	calls := 0
	runner, _, done := thesisFixture(t, func(string, int) string { calls++; return "unused" })
	defer done()
	_, err := runner.call(context.Background(), "thesis-researcher", "oversize", strings.Repeat("x", 100<<10), runner.cheapTarget())
	if err == nil || calls != 0 {
		t.Fatal("oversized input reached engine")
	}
	var research []thesisResearch
	for i := 0; i < 12; i++ {
		r := supportedResearch()
		r.Candidate.Ticker = fmt.Sprintf("C%d", i)
		r.Outcome = model.ResearchOutcome{Ticker: r.Candidate.Ticker, Transport: model.OutcomeOK, Parsing: model.OutcomeOK, Evidence: model.EvidenceDocuments, Review: "supported"}
		if i == 10 {
			r.Outcome.Transport = model.OutcomeNotRun
			r.Eligibility = model.BlockedEvent
		}
		if i == 11 {
			r.Outcome.Transport = model.OutcomeFailed
		}
		research = append(research, r)
	}
	view, _, berr := chiefBoard(research, 180978)
	if berr != nil {
		t.Fatal(berr)
	}
	if len(view) != 12 || view[10].Dossier != nil || view[11].Dossier != nil {
		t.Fatal("failed or deferred company lost/treated as actionable")
	}
	_, p, err := runner.preparePrompt("thesis-chief", "twelve", singleSection(jsonText(view)), 8192)
	if err != nil || p.Bytes > 192<<10 {
		t.Fatalf("twelve-company budget: %v %+v", err, p)
	}
	r := model.Report{Status: model.StatusDone, Stdout: strings.Repeat("界", 10000)}
	responseCapacity(&r, &model.PromptProfile{ResponseLimit: 20 << 10})
	if r.Status != model.StatusFailed || r.FailureKind != "response_capacity" {
		t.Fatal("UTF-8 bytes not bounded")
	}
}

func TestNumericalComparisonNormalizesAndRejectsInvalidBasis(t *testing.T) {
	anchor := time.Date(2026, 9, 10, 23, 0, 0, 0, time.UTC)
	prices := map[string]*quant.Series{"9988.HK": {Symbol: "9988.HK", Bars: []quant.Bar{{Date: "2026-09-10", Close: 100}}}, "AAA": {Symbol: "AAA", Bars: []quant.Bar{{Date: "2026-09-10", Close: 100}}}, "SHEL.L": {Symbol: "SHEL.L", Bars: []quant.Bar{{Date: "2026-09-10", Close: 2000}}}}
	snapshot := &marketdata.ResearchSnapshot{Prices: map[string]*quant.Series{"HKDUSD=X": {Symbol: "HKDUSD=X", Bars: []quant.Bar{{Date: "2026-09-10", Close: .125}}}}}
	fx := marketdata.NewFXRates(snapshot)
	quote := "Each ADS represents eight ordinary shares in the reporting issuer. Effective 2025-01-01."
	docs := []model.EvidenceDocument{{ID: "ratio", Kind: "document", Authority: "issuer", Ticker: "9988.HK", Text: quote}}
	cross := model.NumericalComparison{SourceTicker: "BABA", TargetTicker: "9988.HK", SourceCurrency: "USD", TargetCurrency: "HKD", SourceUnit: "major", TargetUnit: "major", SourceBasis: "ADS", TargetBasis: "share", Target: 120, TargetPublishedOn: "2026-09-09", PriceDate: "2026-09-10", TargetHorizon: "12 months", OrdinarySharesPerADS: 8, RatioEffectiveOn: "2025-01-01", RatioEvidenceID: "ratio", RatioPassage: quote}
	if err := normalizeComparison(context.Background(), &cross, "9988.HK", docs, prices, fx, anchor); err != nil {
		t.Fatal(err)
	}
	if *cross.NormalizedTarget != 120 || *cross.UpsidePct < 19.99 || len(cross.FX) != 2 {
		t.Fatalf("wrong normalized result: %+v", cross)
	}
	for _, variant := range []string{"currency", "ratio", "reverse", "missing_fx", "stale_fx", "future", "later_target", "unknown_listing", "future_ratio", "unrelated_ratio", "third_party_ratio"} {
		t.Run(variant, func(t *testing.T) {
			c := cross
			docCopy := append([]model.EvidenceDocument(nil), docs...)
			f := fx
			switch variant {
			case "future_ratio":
				docCopy[0].PublishedAt = anchor.Add(time.Hour)
			case "unrelated_ratio":
				docCopy[0].Ticker = "TSM"
			case "third_party_ratio":
				docCopy[0].Authority = ""
			case "currency":
				c.TargetCurrency = "USD"
			case "ratio":
				c.RatioEvidenceID = "missing"
			case "reverse":
				c.OrdinarySharesPerADS = .125
			case "missing_fx":
				f = nil
			case "stale_fx":
				f = marketdata.NewFXRates(&marketdata.ResearchSnapshot{Prices: map[string]*quant.Series{"HKDUSD=X": {Bars: []quant.Bar{{Date: "2026-09-01", Close: .125}}}}})
			case "future":
				c.TargetPublishedOn = "2026-09-11"
			case "later_target":
				c.PriceDate = "2026-09-08"
			case "unknown_listing":
				c.SourceTicker = "X.UNKNOWN"
			}
			if normalizeComparison(context.Background(), &c, "9988.HK", docCopy, prices, f, anchor) == nil || c.NormalizedTarget != nil {
				t.Fatal("unresolved comparison gained support")
			}
		})
	}
	for _, ticker := range []string{"AAA", "SHEL.L"} {
		currency, unit, _ := marketdata.ResearchCurrency(ticker)
		c := model.NumericalComparison{SourceTicker: ticker, TargetTicker: ticker, SourceCurrency: currency, TargetCurrency: currency, SourceUnit: unit, TargetUnit: unit, SourceBasis: "share", TargetBasis: "share", Target: 110, PriceDate: "2026-09-10", TargetPublishedOn: "2026-09-09", TargetHorizon: "15 sessions"}
		if ticker == "SHEL.L" {
			c.Target = 2200
		}
		if err := normalizeComparison(context.Background(), &c, ticker, nil, prices, nil, anchor); err != nil || *c.UpsidePct < 9.99 || *c.UpsidePct > 10.01 {
			t.Fatalf("same-listing calculation: %+v %v", c, err)
		}
	}
}

func TestFutureEvidenceAndTimestampPrecision(t *testing.T) {
	anchor := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	r := supportedResearch()
	r.Documents[0].PublishedAt = anchor.Add(time.Hour)
	validateEvidenceTime(&r, anchor)
	if r.Dossier.Status == "supported" {
		t.Fatal("future publication supported thesis")
	}
	r.Dossier.Events = []model.ResearchEvent{{OccurredAt: "2026-09-10T16:30:00-04:00"}}
	f := temporalFacts(r, anchor, marketdata.ResearchCalendar{}, nil)
	if f.Events[0].Precision != "timestamp" || f.Events[0].Ordering != "future" {
		t.Fatal(f.Events)
	}
	r.Dossier.ContractVersion = 2
	r.Dossier.ExpectationsClaimIDs = []string{"invented"}
	if len(reasoningReferences(r.Dossier)) == 0 {
		t.Fatal("unsupported priced-in reference passed")
	}
}

func TestFreshSchemasCannotUseHistoricalCompatibility(t *testing.T) {
	d := supportedResearch().Dossier
	if p := currentResearchSchema(dossierSchema)(&d); len(p) == 0 {
		t.Fatal("fresh output bypassed contract version")
	}
	if p := dossierSchema(&d); len(p) != 0 {
		t.Fatal("historical dossier no longer readable", p)
	}
	c := supportedResearch().Challenge
	if p := currentResearchSchema(challengeSchema)(&c); len(p) == 0 {
		t.Fatal("fresh challenge bypassed hash contract")
	}
}

func TestMaterialPassageOmissionFailsExplicitlyAndFutureTextIsHidden(t *testing.T) {
	runner, _, done := thesisFixture(t, func(string, int) string { return "unused" })
	defer done()
	source := model.EvidenceDocument{ID: "source", Ticker: "AAA", Kind: "document", Text: strings.Repeat("a", 300) + " material quotation that cannot fit a very small context"}
	c := model.ResearchClaim{ID: "c1", EvidenceIDs: []string{source.ID}, Passages: []model.ClaimPassage{{EvidenceID: source.ID, Quote: "material quotation that cannot fit a very small context"}}}
	selected := promptDocuments([]model.EvidenceDocument{source}, 10, c)
	if len(selected[0].OmittedClaimIDs) != 1 {
		t.Fatal("material omission not declared")
	}
	if _, _, err := runner.preparePrompt("thesis-challenger", "omitted", singleSection(jsonText(selected)), 8192); err == nil {
		t.Fatal("missing material passage reached reviewer")
	}
	source.PublishedAt = runner.run.TS.Add(time.Hour)
	selected = promptDocumentsAt([]model.EvidenceDocument{source}, 1000, runner.run.TS)
	if selected[0].Text != "" || selected[0].Error == "" {
		t.Fatal("future observation exposed to model")
	}
}

func TestFixedRegionalPanelMakesDeepPrimaryPassagesVisible(t *testing.T) {
	// Synthetic, sanitized fixed-panel replay. Live endpoint acceptance is separate.
	panel := []string{"ASML.AS", "STLAM.MI", "NOKIA.HE", "2330.TW", "9988.HK", "BHP.AX"}
	gains := map[string]int{}
	for _, ticker := range panel {
		quote := "Quarterly revenue increased while guidance retains material uncertainty about future costs and demand."
		d := model.EvidenceDocument{ID: "primary-" + ticker, Ticker: ticker, Title: "Quarterly results and guidance", Authority: "issuer", Kind: "document", Text: strings.Repeat("cookie menu privacy navigation ", 220) + quote}
		if strings.Contains(string([]rune(d.Text)[:600]), quote) {
			t.Fatal("baseline prefix unexpectedly contains passage")
		}
		selected := promptDocuments([]model.EvidenceDocument{d}, 1600)
		ids := visibleEvidence(jsonText(selected))
		if !strings.Contains(selected[0].Text, quote) || len(ids[ticker]) != 1 {
			t.Fatalf("panel evidence not delivered: %s; selected=%q visible=%v", ticker, selected[0].Text, ids)
		}
		gains[marketdata.ResearchRegion(ticker)]++
	}
	if gains["Europe"] != 3 || gains["Asia-Pacific"] != 3 {
		t.Fatal(gains)
	}
}

func TestFailedRevisionSkipsFinalChallenge(t *testing.T) {
	calls := 0
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		calls++
		if strings.Contains(prompt, "Final revision:") {
			return strings.Repeat("x", 21<<10)
		}
		if strings.Contains(prompt, "# Independent thesis challenge") {
			return fenced(model.ThesisChallenge{Ticker: "AAA", Verdict: "revise", Reason: "Resolve material uncertainty", MaterialIssues: []string{"Check costs"}})
		}
		return fenced(supportedResearch().Dossier)
	})
	defer done()
	out := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA"}, nil, pack, nil)
	if calls != 3 || out.Challenge.Verdict != model.ReviewUnavailable || out.Outcome.Transport != model.OutcomeOK || out.Outcome.Contract != model.OutcomeFailed {
		t.Fatalf("failed revision led to dependent work: calls=%d %+v", calls, out.Outcome)
	}
	if out.Reports[2].FailureKind != "response_capacity" || out.Reports[2].Attempts != 1 {
		t.Fatal(out.Reports)
	}
}

func TestCancelledThesisCallDoesNotDispatch(t *testing.T) {
	calls := 0
	runner, _, done := thesisFixture(t, func(string, int) string { calls++; return "unused" })
	defer done()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := runner.call(ctx, "thesis-researcher", "cancelled", "data", runner.cheapTarget())
	if err == nil || calls != 0 || r.Attempts != 0 || r.Status != model.StatusFailed {
		t.Fatal("cancelled request dispatched")
	}
}

func TestRequiredPassagesUseGlobalBudgetBeforeOptionalContext(t *testing.T) {
	doc := model.EvidenceDocument{ID: "long-report", Ticker: "AAA", Kind: "document", Title: "Issuer report"}
	var claims []model.ResearchClaim
	for i := 0; i < 12; i++ {
		c := model.ResearchClaim{ID: fmt.Sprintf("c%d", i), EvidenceIDs: []string{doc.ID}}
		for j := 0; j < 2; j++ {
			quote := fmt.Sprintf("Quote %02d-%d ", i, j) + strings.Repeat("x", 289)
			if len(quote) != 300 {
				t.Fatal(len(quote))
			}
			doc.Text += strings.Repeat("filler ", 100) + quote
			c.Passages = append(c.Passages, model.ClaimPassage{EvidenceID: doc.ID, Quote: quote, IssuerRole: "reporting issuer"})
		}
		claims = append(claims, c)
	}
	other := model.EvidenceDocument{ID: "z-other", Ticker: "AAA", Kind: "document", Text: "A material conflicting observation from another issuer release changes the interpretation of quarterly revenue."}
	claims = append(claims, model.ResearchClaim{ID: "conflict", EvidenceIDs: []string{other.ID}, Passages: []model.ClaimPassage{{EvidenceID: other.ID, Quote: other.Text, IssuerRole: "reporting issuer"}}})
	minimum := 7200 + 23*5 + len([]rune(other.Text))
	for _, budget := range []int{minimum, 24000} {
		docs := promptDocuments([]model.EvidenceDocument{doc, other}, budget, claims...)
		count := 0
		for _, d := range docs {
			count += len([]rune(d.Text))
			if len(d.OmittedClaimIDs) != 0 {
				t.Fatalf("globally feasible quotes omitted at %d: %v", budget, d.OmittedClaimIDs)
			}
		}
		if count > budget {
			t.Fatal("global budget exceeded")
		}
		for _, c := range claims {
			for _, p := range c.Passages {
				found := false
				for _, d := range docs {
					if d.ID == p.EvidenceID && strings.Contains(d.Text, p.Quote) {
						found = true
					}
				}
				if !found {
					t.Fatal("material quotation lost", c.ID)
				}
			}
		}
	}
}

func TestCurrentTargetProvenanceCannotBeOmitted(t *testing.T) {
	d := supportedResearch().Dossier
	d.ContractVersion = 2
	target := 120.0
	d.Claims[0].Comparison = &model.NumericalComparison{NormalizedTarget: &target}
	for _, plan := range []model.ThesisPlan{{}, {TargetMethod: "external_comparison"}, {TargetMethod: "external_comparison", TargetClaimIDs: []string{"missing"}}, {TargetMethod: "thesis_scenario", TargetClaimIDs: []string{"c1"}}} {
		if len(targetProvenanceProblems(d, plan)) == 0 {
			t.Fatal("invalid target provenance accepted", plan)
		}
	}
	for _, plan := range []model.ThesisPlan{{TargetMethod: "thesis_scenario"}, {TargetMethod: "external_comparison", TargetClaimIDs: []string{"c1"}}} {
		if p := targetProvenanceProblems(d, plan); len(p) != 0 {
			t.Fatal(p)
		}
	}
}

func TestFutureChallengerAndPlanEvidenceCannotSupport(t *testing.T) {
	r := supportedResearch()
	anchor := time.Now()
	r.Documents[0].PublishedAt = anchor.Add(time.Hour)
	if p := validateClaimsAt(r.Challenge.Claims, r.Documents, "AAA", anchor); len(p) == 0 {
		t.Fatal("future challenger observation accepted")
	}
	r.Documents[0].PublishedAt = anchor.Add(-time.Hour)
	ratio := model.EvidenceDocument{ID: "future-ratio", Ticker: "AAA", PublishedAt: anchor.Add(time.Hour)}
	r.Documents = append(r.Documents, ratio)
	r.Challenge.Claims[0].Comparison = &model.NumericalComparison{RatioEvidenceID: ratio.ID}
	if p := validateClaimsAt(r.Challenge.Claims, r.Documents, "AAA", anchor); len(p) == 0 {
		t.Fatal("future comparison provenance accepted")
	}
}

// --- Chief board budget -------------------------------------------------
//
// The September 15 Chief input (runs/2026-09-15T17-00-30) assembled 158,528
// of its 196,608 bytes with **three** usable dossiers and nine outcome
// records, because nine of twelve companies' research had failed. The board
// itself was 142,756 bytes for those three; twelve of the same shape project
// to 521,568 against a ~181,000-byte board budget. These tests are built from
// the twelve anonymised round-one dossiers in testdata/sep13-dossiers
// (13,233-22,881 bytes compact, 12-13 claims, 14-21 quoted passages each),
// not from twelve tiny fixtures, which would fit whatever the code did.
//
// None of these tests drives a payload through thesisFixture's reply path, so
// currentFixtureReply never rewrites anything they measure; the fixture
// dossiers are decoded straight off disk and already carry contract_version 2.

// sep13ResearchBoard rebuilds a full twelve-company board at the size the
// September 15 run carried. The fixtures preserve each dossier's field shapes
// and Unicode lengths but collapsed every source ID to one placeholder, so
// this re-diversifies them — keeping each ID's original byte length, so the
// calibration survives — and synthesises the three parts a dossier fixture
// does not carry, at the per-part sizes that run measured: ~294 bytes of
// document scaffolding per source before projection against the run's 382-422,
// one claim_review per claim, fifteen session dates and one evidence age per
// cited source.
//
// Every dossier is made `supported`/`BUY`. The captured round produced twelve
// `watchlist`/`NONE` dossiers, and a board of companies the Chief cannot
// select is the easy case: validateThesisResult hard-blocks any idea whose
// dossier is not supported, so their evidence is dead weight a projection
// could legitimately thin. Twelve *selectable* companies is the case that has
// to fit.
func sep13ResearchBoard(t *testing.T) []thesisResearch {
	t.Helper()
	files, err := filepath.Glob("testdata/sep13-dossiers/*.md")
	if err != nil {
		t.Fatal(err)
	}
	anchor := time.Date(2026, 9, 15, 17, 0, 30, 0, time.UTC)
	filler := "Regulatory filings and market commentary continue to describe the reporting period in general terms without resolving the specific question at issue. "
	var board []thesisResearch
	for _, file := range files {
		if filepath.Base(file) == "README.md" {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var d model.CandidateDossier
		if err := decodeResearch(string(raw), &d); err != nil {
			t.Fatal(err)
		}
		d.Status, d.PreferredDirection = "supported", "BUY"
		sources, ages := 0, []evidenceAge{}
		var docs []model.EvidenceDocument
		claims := append([]model.ResearchClaim(nil), d.Claims...)
		for i := range claims {
			c := &claims[i]
			c.Passages = append([]model.ClaimPassage(nil), c.Passages...)
			var ids []string
			for j := range c.Passages {
				old := c.Passages[j].EvidenceID
				id := old[:len(old)-4] + fmt.Sprintf("%04d", sources)
				sources++
				c.Passages[j].EvidenceID = id
				ids = append(ids, id)
				doc := model.EvidenceDocument{ID: id, Ticker: d.Ticker, Kind: "document",
					URL:         "https://src.example.test/" + id,
					Title:       "Q2 2026 results statement " + id[:4],
					Source:      "issuer press release",
					PublishedAt: anchor.AddDate(0, 0, -3-sources), RetrievedAt: anchor,
					Text: strings.Repeat(filler, 14) + c.Passages[j].Quote + strings.Repeat(filler, 14)}
				if sources%2 == 0 {
					doc.Authority, doc.ReportingPeriod = "issuer", "2026 Q2"
				}
				docs = append(docs, doc)
				hours := float64(72 + 24*sources)
				ages = append(ages, evidenceAge{EvidenceID: id, Hours: &hours})
			}
			c.EvidenceIDs = ids
		}
		d.Claims = claims
		reviews := make([]model.ClaimReview, 0, len(claims))
		for _, c := range claims {
			reviews = append(reviews, model.ClaimReview{ClaimID: c.ID, Assessment: "supported",
				Attribution: "confirmed", Reason: strings.Repeat("x", 450)})
		}
		sessions := make([]string, 0, 15)
		for i := 1; i <= 15; i++ {
			sessions = append(sessions, anchor.AddDate(0, 0, i).Format("2006-01-02"))
		}
		board = append(board, thesisResearch{
			Candidate: model.Candidate{Ticker: d.Ticker, Name: "Company " + d.Ticker,
				Sector: "Health Care", Index: "sp500", Setup: "drift", Reason: strings.Repeat("x", 120), Bias: "BUY"},
			Documents: docs, Dossier: d,
			Challenge: model.ThesisChallenge{ContractVersion: 2, Ticker: d.Ticker, DossierHash: dossierHash(d),
				Verdict: "supported", Reason: strings.Repeat("x", 220), ClaimReviews: reviews,
				MaterialIssues: []string{}, TargetAssessment: "supported",
				CompactionAssessment: "preserved", ConditionsReviewed: true},
			Outcome: model.ResearchOutcome{Ticker: d.Ticker, Transport: model.OutcomeOK,
				Parsing: model.OutcomeOK, Evidence: model.EvidenceDocuments, Review: "supported", Decision: "actionable"},
			Temporal: researchTimeFacts{AsOf: anchor.Format(time.RFC3339), Sessions: sessions,
				Closures: []string{"2026-09-21"},
				Events: []researchEventFact{{Precision: "date_only", Date: "2026-09-29", Ordering: "future",
					Availability: "future outcome unavailable; use conditional scenarios", SessionsAway: 9, InsideWindow: true}},
				EvidenceAges: ages, Volatility: "sigma_daily from 2y daily closes"},
		})
	}
	if len(board) != 12 {
		t.Fatalf("built %d companies, want 12", len(board))
	}
	return board
}

// chiefBoardSections reproduces the Chief's non-board sections at the exact
// sizes the September 15 run measured (quant 3,685, macro 3,944, risk_policy
// 349), so the budget under test is that run's budget and not a convenient
// one.
func chiefBoardSections() ([]promptSection, string) {
	return []promptSection{
		{Name: "quant", Mandatory: true, Body: strings.Repeat("q", 3685)},
		{Name: "macro", Mandatory: false, Body: strings.Repeat("m", 3944)},
		{Name: "risk_policy", Mandatory: true, Body: strings.Repeat("r", 349)},
	}, "Maximum ideas: 5 (single mode: 1). Research:\n"
}

func TestTwelveRealisticDossiersFitTheChiefInputBudget(t *testing.T) {
	runner, _, done := thesisFixture(t, func(string, int) string { return "" })
	defer done()
	research := sep13ResearchBoard(t)
	other, prefix := chiefBoardSections()
	budget, err := runner.chiefBoardBudget(other, prefix)
	if err != nil {
		t.Fatal(err)
	}
	board, alloc, err := chiefBoard(research, budget)
	if err != nil {
		t.Fatalf("twelve realistic dossiers do not fit: %v", err)
	}
	if len(board) != 12 {
		t.Fatalf("board carries %d companies, want 12", len(board))
	}
	for i, c := range board {
		if c.Outcome.Ticker != research[i].Candidate.Ticker || c.Outcome.Transport == "" {
			t.Fatalf("%s lost its outcome record: %+v", research[i].Candidate.Ticker, c.Outcome)
		}
		if c.Dossier == nil || len(c.Dossier.Claims) == 0 || len(c.Documents) == 0 {
			t.Fatalf("%s lost its research", c.Candidate.Ticker)
		}
		for _, d := range c.Documents {
			if len(d.OmittedClaimIDs) > 0 {
				t.Fatalf("%s dropped a required quotation for %v", c.Candidate.Ticker, d.OmittedClaimIDs)
			}
		}
	}
	sections := append([]promptSection{{Name: "company_board", Mandatory: true, Body: prefix + jsonText(board)}}, other...)
	_, profile, err := runner.preparePrompt("thesis-chief", "twelve-realistic", sections, 24<<10)
	if err != nil {
		t.Fatalf("assembled Chief prompt rejected: %v", err)
	}
	if profile.Bytes > 192<<10 {
		t.Fatalf("assembled Chief prompt is %d bytes, over the %d limit", profile.Bytes, 192<<10)
	}
	if profile.Components["company_board"] > budget {
		t.Fatalf("board section %d exceeds its own budget %d", profile.Components["company_board"], budget)
	}
	t.Logf("board %d of %d budget; prompt %d of %d; required floor %d; optional pool %d",
		alloc.BoardBytes, budget, profile.Bytes, profile.InputLimit, alloc.RequiredBytes, alloc.OptionalPool)
}

// boardCompany builds one synthetic company carrying `sources` quoted sources
// of `runes` characters each.
//
// It carries a case narrative. An earlier version deliberately left the
// narrative fields empty "so a test of the document allocation measures only
// the document allocation" — which deleted from the fixture the one tier that
// in production consumed the entire optional pool, and so proved the document
// allocation's fairness on a board where nothing competed with it. The
// narrative is sized off the company's own evidence, so a company that is
// heavy in sources is heavy in both tiers and order-independence is tested
// against an uneven board in both.
func boardCompany(ticker string, sources, runes int) thesisResearch {
	return boardCompanyIn(ticker, sources, runes,
		"Reported results were described without resolving the question at issue. ", "g")
}

// boardCompanyIn spells out the source language. `filler` is the prose around
// each quotation and `pad` the repeated body inside it — one ASCII byte per
// character for a Latin board, three for a CJK one, which is the case the
// board's fit loop exists for: the optional pool is counted in bytes and spent
// in characters.
func boardCompanyIn(ticker string, sources, runes int, filler, pad string) thesisResearch {
	anchor := time.Date(2026, 9, 15, 17, 0, 30, 0, time.UTC)
	r := thesisResearch{
		Candidate: model.Candidate{Ticker: ticker, Name: ticker, Sector: "Health Care"},
		Dossier: model.CandidateDossier{Ticker: ticker, Status: "supported", PreferredDirection: "BUY", EvidenceQuality: "mixed",
			LongCase:       "long case: " + strings.Repeat("n", 400+sources*4),
			ShortCase:      "short case: " + strings.Repeat("n", 360+sources*4),
			NoTradeCase:    "no-trade case: " + strings.Repeat("n", 380+sources*3),
			CatalystWindow: "catalyst window: " + strings.Repeat("n", 200+sources*2),
			Invalidation:   "invalidation: " + strings.Repeat("n", 300+sources*2)},
		Challenge: model.ThesisChallenge{ContractVersion: 2, Ticker: ticker, Verdict: "supported"},
		Outcome: model.ResearchOutcome{Ticker: ticker, Transport: model.OutcomeOK, Parsing: model.OutcomeOK,
			Evidence: model.EvidenceDocuments, Review: "supported"},
		Temporal: researchTimeFacts{AsOf: anchor.Format(time.RFC3339)},
	}
	for i := 0; i < sources; i++ {
		id := fmt.Sprintf("%s-source-%03d", ticker, i)
		quote := fmt.Sprintf("%s source %03d states: ", ticker, i) + strings.Repeat(pad, runes)
		r.Documents = append(r.Documents, model.EvidenceDocument{ID: id, Ticker: ticker, Kind: "document",
			Source: "issuer press release", PublishedAt: anchor.AddDate(0, 0, -3-i), RetrievedAt: anchor,
			Text: strings.Repeat(filler, 30) + quote + strings.Repeat(filler, 30)})
		claim := model.ResearchClaim{ID: fmt.Sprintf("c%d", i), Kind: "observation",
			Text: "The source reports a change in the reporting period", EvidenceIDs: []string{id},
			Passages: []model.ClaimPassage{{EvidenceID: id, Quote: quote, IssuerRole: "reporting issuer"}}}
		r.Dossier.Claims = append(r.Dossier.Claims, claim)
		r.Challenge.ClaimReviews = append(r.Challenge.ClaimReviews, model.ClaimReview{ClaimID: claim.ID,
			Assessment: "supported", Attribution: "confirmed", Reason: "The quoted passage establishes the change"})
	}
	return r
}

func shareFor(alloc boardAllocation, ticker string) boardShare {
	for _, s := range alloc.Companies {
		if s.Ticker == ticker {
			return s
		}
	}
	return boardShare{}
}

func companyFor(board []chiefCompany, ticker string) chiefCompany {
	for _, c := range board {
		if c.Candidate.Ticker == ticker {
			return c
		}
	}
	return chiefCompany{}
}

// A loop that spends the budget company by company runs out before it reaches
// the end of the slice, so the last company's allocation is a function of who
// was in front of it. A global budget reserves every company's required
// quotations first and then splits what is left in proportion to each one's
// unmet need, so the twelfth company gets its passages and its share whatever
// order the slice is in.
func TestBoardAllocationIsNotFirstComeFirstServed(t *testing.T) {
	var research []thesisResearch
	for i := 0; i < 12; i++ {
		sources, runes := 4, 120
		if i < 3 {
			sources, runes = 40, 400
		}
		research = append(research, boardCompany(fmt.Sprintf("C%02d", i), sources, runes))
	}
	last := "C11"
	_, floor, err := chiefBoard(research, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	// A budget that leaves far less optional context than the board wants —
	// the first three companies alone would absorb all of it — and less than
	// the case narrative alone wants, so BOTH tiers are allocated at their
	// margin and a walk down the slice would visibly run out in each.
	budget := floor.RequiredBytes + 12000
	board, alloc, err := chiefBoard(research, budget)
	if err != nil {
		t.Fatal(err)
	}
	if len(jsonText(board)) > budget {
		t.Fatalf("board %d over budget %d", len(jsonText(board)), budget)
	}
	tail := companyFor(board, last)
	if len(tail.Documents) != 4 {
		t.Fatalf("%s lost sources: %d", last, len(tail.Documents))
	}
	for i, d := range tail.Documents {
		if d.Text == "" || len(d.OmittedClaimIDs) > 0 {
			t.Fatalf("%s source %d lost its required quotation: %q %v", last, i, d.Text, d.OmittedClaimIDs)
		}
	}
	tailShare := shareFor(alloc, last)
	if tailShare.ContextGrant <= 0 {
		t.Fatalf("%s received no document allocation while its claims still wanted context: %+v", last, tailShare)
	}
	// Proportional, not greedy: the exact integer share the whole need vector
	// and the final pool imply. A loop that spent as it walked would give the
	// head its full need and the tail whatever survived. The pool the document
	// tier divides is what the case-narrative tier left, which is why the
	// board records both.
	total := 0
	for _, s := range alloc.Companies {
		total += s.ContextNeed
	}
	if alloc.NarrativeBytes <= 0 || alloc.NarrativeBytes >= alloc.OptionalPool {
		t.Fatalf("the two tiers are not competing: narrative %d of a %d pool", alloc.NarrativeBytes, alloc.OptionalPool)
	}
	contextPool := alloc.OptionalPool - alloc.NarrativeBytes
	head := shareFor(alloc, "C00")
	for _, s := range []boardShare{tailShare, head} {
		want := contextPool * s.ContextNeed / total
		if s.ContextGrant < want || s.ContextGrant >= s.ContextNeed {
			t.Fatalf("%s got %d of the %d-character pool against a proportional %d and a need of %d",
				s.Ticker, s.ContextGrant, alloc.OptionalPool, want, s.ContextNeed)
		}
	}
	if head.ContextNeed <= 4*tailShare.ContextNeed {
		t.Fatalf("the fixture no longer makes the head far heavier than the tail: %d vs %d", head.ContextNeed, tailShare.ContextNeed)
	}
	// Order independence: reversing the slice must change nothing about any
	// company's record or its share.
	reversed := make([]thesisResearch, 0, len(research))
	for i := len(research) - 1; i >= 0; i-- {
		reversed = append(reversed, research[i])
	}
	other, ralloc, err := chiefBoard(reversed, budget)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range board {
		ticker := c.Candidate.Ticker
		if a, b := jsonText(c), jsonText(companyFor(other, ticker)); a != b {
			t.Fatalf("%s projected differently after reordering (%d vs %d bytes)", ticker, len(a), len(b))
		}
		if a, b := shareFor(alloc, ticker), shareFor(ralloc, ticker); a != b {
			t.Fatalf("%s allocated differently after reordering: %+v vs %+v", ticker, a, b)
		}
	}
}

// Trimming evidence is an operation on evidence. It must never be an
// operation on the candidate list: a failed, deferred or ineligible company
// is a fact about this run that the Chief has to write a decision for, and
// the Chief cannot decide about a company it cannot see.
func TestEveryCandidateKeepsItsOutcomeEvenWhenEvidenceIsTrimmed(t *testing.T) {
	var research []thesisResearch
	for i := 0; i < 12; i++ {
		r := boardCompany(fmt.Sprintf("C%02d", i), 12, 300)
		switch i {
		case 9:
			r.Outcome.Transport = model.OutcomeFailed
		case 10:
			r.Outcome.Transport, r.Eligibility = model.OutcomeNotRun, model.BlockedEvent
		case 11:
			r.Outcome.Review, r.Eligibility = model.ReviewUnavailable, model.BlockedPrices
		}
		research = append(research, r)
	}
	_, floor, err := chiefBoard(research, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the required floor: nothing optional survives anywhere on the
	// board, which is the tightest budget chiefBoard will accept at all.
	board, alloc, err := chiefBoard(research, floor.RequiredBytes)
	if err != nil {
		t.Fatal(err)
	}
	if alloc.OptionalPool != 0 {
		t.Fatalf("expected an exhausted optional pool, got %d", alloc.OptionalPool)
	}
	if len(board) != 12 {
		t.Fatalf("board carries %d companies, want 12", len(board))
	}
	for i, c := range board {
		if c.Candidate.Ticker != research[i].Candidate.Ticker {
			t.Fatalf("candidate %d is %q, want %q", i, c.Candidate.Ticker, research[i].Candidate.Ticker)
		}
		if c.Outcome.Ticker != c.Candidate.Ticker || c.Outcome.Transport == "" {
			t.Fatalf("%s lost its outcome record: %+v", c.Candidate.Ticker, c.Outcome)
		}
		if c.Eligibility != research[i].Eligibility {
			t.Fatalf("%s lost its eligibility %q", c.Candidate.Ticker, research[i].Eligibility)
		}
	}
	for _, ticker := range []string{"C09", "C10", "C11"} {
		c := companyFor(board, ticker)
		if c.Dossier != nil || c.Challenge != nil || len(c.Documents) > 0 {
			t.Fatalf("%s was presented as actionable research", ticker)
		}
	}
	for _, ticker := range []string{"C00", "C08"} {
		c := companyFor(board, ticker)
		if len(c.Documents) != 12 {
			t.Fatalf("%s lost sources under the tightest budget: %d", ticker, len(c.Documents))
		}
		for _, d := range c.Documents {
			if d.Text == "" || len(d.OmittedClaimIDs) > 0 {
				t.Fatalf("%s lost a required quotation under trimming", ticker)
			}
		}
	}
}

// chiefCorrectivePayload rebuilds the two mandatory sections the corrective
// round appends, at the size a real thesis round carries them. The previous
// response is five ideas each with a full ThesisPlan — the 2026-09-15 thesis
// run's own ideas.json is 10,975 bytes and the largest on disk is 16,823 —
// and the findings are what the risk gate writes for a re-prompt.
func chiefCorrectivePayload(tickers []string) []promptSection {
	result := &model.IdeasResult{}
	for i, ticker := range tickers {
		if i == 5 {
			break
		}
		result.Ideas = append(result.Ideas, model.TradeIdea{Rank: i + 1, Ticker: ticker, Name: "Company " + ticker,
			Index: "sp500", Direction: "BUY", Confidence: 55, Setup: "drift", Why: strings.Repeat("w", 310),
			Entry: 100, Stop: 94.5, Target: 112.25, RiskReward: 2.2, TimeframeDays: 15, PositionNote: strings.Repeat("p", 90),
			Thesis: &model.ThesisPlan{WhyNow: strings.Repeat("y", 170), Invalidation: strings.Repeat("i", 140),
				CatalystWindow: strings.Repeat("c", 90), EvidenceQuality: "mixed", EntryReason: strings.Repeat("e", 110),
				StopReason: strings.Repeat("s", 110), TargetReason: strings.Repeat("t", 120), TargetMethod: "thesis_scenario",
				OutcomeLow: 96, OutcomeHigh: 118, ExpiresOn: "2026-10-06", EntryExpiresOn: "2026-09-22",
				Monitoring:     []string{strings.Repeat("m", 60), strings.Repeat("m", 60)},
				EvidenceIDs:    []string{ticker + "-src-0001", ticker + "-src-0002", ticker + "-src-0003"},
				TargetClaimIDs: []string{"c1", "c2"}}})
	}
	var findings []riskFinding
	for i := 0; i < 3 && i < len(tickers); i++ {
		findings = append(findings, riskFinding{Ticker: tickers[i], Hard: true, Message: strings.Repeat("f", 130)})
	}
	return []promptSection{
		{Name: "corrective_findings", Mandatory: true, Body: "\nRevise or reject these unsupported constructions. Do not stretch targets.\n" + jsonText(findings)},
		{Name: "previous_chief_response", Mandatory: true, Body: "\nPrevious response:\n" + jsonText(result)},
	}
}

// The corrective round is the same Chief prompt plus two more MANDATORY
// sections. A board sized only for the initial call spends the whole budget,
// so those two sections are appended past the input limit and the one
// corrective call the pipeline gets can never be assembled — measured on this
// very board, the initial prompt lands 1,015 bytes under the limit against a
// corrective payload of thousands. finalizeThesis then deletes every idea
// carrying a non-observational finding instead of letting the Chief revise it,
// and reports a component-less input_capacity error one call downstream.
func TestCorrectiveChiefPromptFitsOnATwelveDossierBoard(t *testing.T) {
	runner, _, done := thesisFixture(t, func(string, int) string { return "" })
	defer done()
	research := sep13ResearchBoard(t)
	other, prefix := chiefBoardSections()
	cp, err := runner.chiefPrompt(research, other, prefix)
	if err != nil {
		t.Fatal(err)
	}
	initial, _, err := cp.sections()
	if err != nil {
		t.Fatalf("initial board does not fit: %v", err)
	}
	if _, _, err = runner.preparePrompt("thesis-chief", "corrective-initial", initial, 24<<10); err != nil {
		t.Fatalf("initial Chief prompt rejected: %v", err)
	}
	tickers := make([]string, 0, len(research))
	for _, r := range research {
		tickers = append(tickers, r.Candidate.Ticker)
	}
	extra := chiefCorrectivePayload(tickers)
	payload := 0
	for _, sec := range extra {
		payload += len(sec.Body)
	}
	// A real round, not a token one: the largest five-idea `ideas` array on
	// disk under runs/ is 6,482 bytes without a ThesisPlan, and every idea
	// here carries one.
	if payload < 8000 {
		t.Fatalf("the corrective payload is a toy: %d bytes, want a realistic round of 8000+", payload)
	}
	corrective, alloc, err := cp.sections(extra...)
	if err != nil {
		t.Fatalf("corrective board does not fit: %v", err)
	}
	_, profile, err := runner.preparePrompt("thesis-chief", "corrective-round", corrective, 24<<10)
	if err != nil {
		t.Fatalf("corrective Chief prompt rejected with a %d-byte payload: %v", payload, err)
	}
	// Every company still reaches the corrective call: the room is bought out
	// of optional context, never out of the candidate list.
	if got := len(alloc.Companies); got != 12 {
		t.Fatalf("corrective board carries %d companies, want 12", got)
	}
	for _, name := range []string{"company_board", "quant", "risk_policy", "corrective_findings", "previous_chief_response"} {
		if profile.Components[name] == 0 {
			t.Fatalf("corrective prompt lost its %s section: %v", name, profile.Components)
		}
	}
	t.Logf("corrective payload %d; board %d; prompt %d of %d", payload, alloc.BoardBytes, profile.Bytes, profile.InputLimit)

	// Twelve dossiers this size leave the corrective round about 11 KB, since
	// the board's required floor alone is 169,696 of its 180,944 bytes. That
	// is a limit of the research, not of the reservation — and past it the
	// board says so by name instead of letting preparePrompt report a
	// component-less input_capacity error one call downstream.
	_, _, err = cp.sections(promptSection{Name: "corrective_findings", Mandatory: true, Body: strings.Repeat("f", 24<<10)})
	if err == nil {
		t.Fatal("a corrective payload larger than the whole optional pool was accepted")
	}
	if !strings.Contains(err.Error(), research[0].Candidate.Ticker) {
		t.Fatalf("the corrective overflow names no company: %v", err)
	}
}

// The optional pool has two tiers and both must be able to reach it at the
// budget production actually runs on. Handing the whole pool to case
// narrative made the global document allocation — the thing this board exists
// to build — inert: measured on this board, source context received exactly
// zero bytes at every budget below the required floor plus 28,447, which is
// roughly four times the real pool.
func TestBothOptionalTiersAreFundedAtTheProductionBudget(t *testing.T) {
	runner, _, done := thesisFixture(t, func(string, int) string { return "" })
	defer done()
	research := sep13ResearchBoard(t)
	other, prefix := chiefBoardSections()
	budget, err := runner.chiefBoardBudget(other, prefix)
	if err != nil {
		t.Fatal(err)
	}
	board, alloc, err := chiefBoard(research, budget)
	if err != nil {
		t.Fatal(err)
	}
	context := 0
	for _, s := range alloc.Companies {
		context += s.ContextGrant
	}
	if alloc.NarrativeBytes <= 0 {
		t.Fatalf("case narrative was funded nothing at the production budget: %+v", alloc)
	}
	if context <= 0 {
		t.Fatalf("source context was funded nothing at the production budget: narrative %d of a %d pool",
			alloc.NarrativeBytes, alloc.OptionalPool)
	}
	if alloc.NarrativeBytes > alloc.OptionalPool {
		t.Fatalf("case narrative spent %d of a %d pool", alloc.NarrativeBytes, alloc.OptionalPool)
	}
	// Funded by rank across the whole board: for each narrative field, every
	// usable company has it or none does. A board where some companies are
	// argued for and others are not invites the Chief's ranking to be decided
	// by which argument was funded.
	names := []string{"long_case", "short_case", "no_trade_case", "catalyst_window", "invalidation"}
	funded := make([]bool, len(names))
	for rank, name := range names {
		with, without := 0, 0
		for _, c := range board {
			if c.Dossier == nil {
				continue
			}
			d := *c.Dossier
			if *caseNarrative(&d)[rank] != "" {
				with++
			} else {
				without++
			}
		}
		if with > 0 && without > 0 {
			t.Fatalf("%s is funded for %d companies and withheld from %d: the board is not argued to one depth", name, with, without)
		}
		funded[rank] = with > 0
	}
	// And funded as a PREFIX of caseNarrative's order — the three cases, then
	// the catalyst window, then the invalidation — because that is the order
	// the Chief persona names them in. Skipping an unaffordable rank to buy a
	// cheaper later one would convert more bytes and argue the wrong thing.
	for rank := 1; rank < len(funded); rank++ {
		if funded[rank] && !funded[rank-1] {
			t.Fatalf("%s was funded over %s: the narrative order is not a priority order", names[rank], names[rank-1])
		}
	}
	// The board is built to SPEND its budget. Leaving more than a quarter of
	// the optional pool unconverted means the allocation is not pricing what
	// it buys — either it grants what cannot be spent, or it overdraws and
	// lets the fit loop claw the whole pool back.
	if unused := budget - alloc.BoardBytes; unused > alloc.OptionalPool/4 {
		t.Fatalf("%d bytes of a %d-byte budget went unconverted, against an optional pool of %d",
			unused, budget, alloc.OptionalPool)
	}
	t.Logf("pool %d = narrative %d + context %d chars; board %d of %d",
		alloc.OptionalPool, alloc.NarrativeBytes, context, alloc.BoardBytes, budget)
}

// A grant an indivisible tier cannot spend must come back. Funding case
// narrative whole-field-or-nothing out of a per-company share of a small pool
// bought nothing at all and returned nothing: at a 1,000-byte pool the board
// came out at EXACTLY the required floor with 100% of the pool burned.
func TestAnUnspendableOptionalGrantIsNotBurned(t *testing.T) {
	research := sep13ResearchBoard(t)
	_, floor, err := chiefBoard(research, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	const slack = 1000
	_, alloc, err := chiefBoard(research, floor.RequiredBytes+slack)
	if err != nil {
		t.Fatal(err)
	}
	if alloc.BoardBytes <= alloc.RequiredBytes {
		t.Fatalf("a %d-byte pool bought nothing: board %d, required floor %d",
			slack, alloc.BoardBytes, alloc.RequiredBytes)
	}
	t.Logf("slack %d converted %d bytes of board", slack, alloc.BoardBytes-alloc.RequiredBytes)
}

// Ruling R4 permits dropping "claim text already carried by its cited
// passage" — where it actually is. A document published after the run anchor
// has its text blanked by visibleAt, and one that failed retrieval has none
// to begin with, so their quotations reach no prompt at all. Dropping the
// claim's own words for a quotation that is nowhere ships the Chief a claim
// with no content of any kind, and is a regression against the projection
// this board replaced, which never blanked claim text.
func TestClaimKeepsItsTextWhenItsQuotationReachesNoDocument(t *testing.T) {
	anchor := time.Date(2026, 9, 15, 17, 0, 30, 0, time.UTC)
	quote := "The issuer stated that shipments rose by a fifth over the reporting period."
	company := func(mutate func(*model.EvidenceDocument)) thesisResearch {
		doc := model.EvidenceDocument{ID: "doc-1", Ticker: "AAA", Kind: "document", Source: "issuer press release",
			PublishedAt: anchor.AddDate(0, 0, -3), RetrievedAt: anchor,
			Text: "Preamble to the release. " + quote + " Closing remarks of the release."}
		mutate(&doc)
		claim := model.ResearchClaim{ID: "c1", Kind: "observation", Text: "Shipments rose by a fifth in the period",
			EvidenceIDs: []string{doc.ID},
			Passages:    []model.ClaimPassage{{EvidenceID: doc.ID, Quote: quote, IssuerRole: "reporting issuer"}}}
		return thesisResearch{
			Candidate: model.Candidate{Ticker: "AAA", Name: "AAA"},
			Documents: []model.EvidenceDocument{doc},
			Dossier: model.CandidateDossier{Ticker: "AAA", Status: "supported", PreferredDirection: "BUY",
				Claims: []model.ResearchClaim{claim},
				Events: []model.ResearchEvent{{Kind: "guidance", OccurredAt: "2026-09-29T12:00:00Z", EvidenceID: doc.ID, Passage: quote}}},
			Challenge: model.ThesisChallenge{ContractVersion: 2, Ticker: "AAA", Verdict: "supported"},
			Outcome: model.ResearchOutcome{Ticker: "AAA", Transport: model.OutcomeOK, Parsing: model.OutcomeOK,
				Evidence: model.EvidenceDocuments, Review: "supported"},
			Temporal: researchTimeFacts{AsOf: anchor.Format(time.RFC3339)},
		}
	}
	unreachable := map[string]func(*model.EvidenceDocument){
		"published after the run anchor": func(d *model.EvidenceDocument) { d.PublishedAt = anchor.AddDate(0, 0, 3) },
		"retrieval failed":               func(d *model.EvidenceDocument) { d.Text, d.Error = "", "retrieval failed: 503 from the issuer host" },
	}
	for name, mutate := range unreachable {
		// A budget far above anything the board could spend, so capacity is
		// provably not the reason anything is missing.
		board, _, err := chiefBoard([]thesisResearch{company(mutate)}, 1<<30)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		c := board[0]
		if c.Documents[0].Text != "" {
			t.Fatalf("%s: the fixture no longer withholds the source text: %q", name, c.Documents[0].Text)
		}
		if c.Dossier.Claims[0].Text == "" {
			t.Fatalf("%s: the claim reached the Chief with no text and no quotation: %s", name, jsonText(c.Dossier.Claims))
		}
		if c.Dossier.Events[0].Passage == "" {
			t.Fatalf("%s: the event reached the Chief with no passage and no source text: %s", name, jsonText(c.Dossier.Events))
		}
	}
	// The control: when the quotation IS in the prompt, the claim's own words
	// are still dropped, because the passage carries them.
	board, _, err := chiefBoard([]thesisResearch{company(func(*model.EvidenceDocument) {})}, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(board[0].Documents[0].Text, quote) {
		t.Fatalf("the control did not place its quotation: %q", board[0].Documents[0].Text)
	}
	if board[0].Dossier.Claims[0].Text != "" || board[0].Dossier.Events[0].Passage != "" {
		t.Fatalf("a placed quotation no longer carries its claim: %s", jsonText(board[0].Dossier))
	}
}

// A board whose required records and quotations alone exceed the budget is a
// capacity failure that must name the companies. A generic input_capacity
// error attributable to no component is the September 15 diagnostic failure
// this board exists to remove, so the naming is the point of the refusal and
// not decoration on it.
func TestRequiredFloorOverflowNamesItsCompaniesAndKeepsEveryCandidate(t *testing.T) {
	runner, _, done := thesisFixture(t, func(string, int) string { return "" })
	defer done()
	research := append(sep13ResearchBoard(t), sep13ResearchBoard(t)...)
	other, prefix := chiefBoardSections()
	budget, err := runner.chiefBoardBudget(other, prefix)
	if err != nil {
		t.Fatal(err)
	}
	board, alloc, err := chiefBoard(research, budget)
	if err == nil {
		t.Fatalf("24 realistic dossiers were accepted against a %d-byte budget at %d bytes", budget, alloc.BoardBytes)
	}
	var capacity promptCapacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("refusal is not a capacity error (%T): %v", err, err)
	}
	if promptFailureKind(err) != "input_capacity" {
		t.Fatalf("refusal classifies as %q", promptFailureKind(err))
	}
	named := 0
	for _, r := range research {
		if strings.Contains(err.Error(), r.Candidate.Ticker+" ") {
			named++
		}
	}
	if named < 12 {
		t.Fatalf("the refusal names %d of the board's companies: %v", named, err)
	}
	if !regexp.MustCompile(`[A-Z0-9.]+ \d{4,}`).MatchString(err.Error()) {
		t.Fatalf("the refusal names no company with a byte count: %v", err)
	}
	// The board comes back WITH the error, so preparePrompt still refuses the
	// assembled prompt for the primary and the fallback alike and each keeps
	// its zero-attempt provenance. Returning nothing would skip the Chief and
	// erase that.
	if len(board) != len(research) {
		t.Fatalf("the refusal dropped candidates: %d of %d", len(board), len(research))
	}
	for i, c := range board {
		if c.Outcome.Ticker != research[i].Candidate.Ticker || c.Outcome.Transport == "" {
			t.Fatalf("%s lost its outcome record in the refusal", research[i].Candidate.Ticker)
		}
	}
	sections := append([]promptSection{{Name: "company_board", Mandatory: true, Body: prefix + jsonText(board)}}, other...)
	if _, _, perr := runner.preparePrompt("thesis-chief", "required-floor-overflow", sections, 24<<10); perr == nil {
		t.Fatal("preparePrompt accepted the over-budget board")
	} else if promptFailureKind(perr) != "input_capacity" {
		t.Fatalf("preparePrompt classified the overflow as %q: %v", promptFailureKind(perr), perr)
	}
}

// The optional pool is counted in bytes and spent in characters, and one
// character of a Hong Kong or Taiwan source is three bytes, so the first
// split overshoots on a multi-byte board. The fit loop rescales by the
// overshoot it measured and renders again; without that it would have nothing
// between "fits" and "no optional context at all", and every such board would
// ship at its required floor.
func TestMultiByteBoardRescalesItsPoolInsteadOfCollapsingIt(t *testing.T) {
	var research []thesisResearch
	for i := 0; i < 12; i++ {
		research = append(research, boardCompanyIn(fmt.Sprintf("C%02d", i), 12, 300,
			"本期業績報告並未解決有關問題所涉及的具體事項。", "字"))
	}
	_, floor, err := chiefBoard(research, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	_, alloc, err := chiefBoard(research, floor.RequiredBytes+60000)
	if err != nil {
		t.Fatal(err)
	}
	if alloc.Attempts < 2 {
		t.Fatalf("the multi-byte board fit on attempt %d: the rescale never ran", alloc.Attempts)
	}
	if alloc.BoardBytes > floor.RequiredBytes+60000 {
		t.Fatalf("the rescaled board is over budget: %d of %d", alloc.BoardBytes, floor.RequiredBytes+60000)
	}
	// The rescale has to land on a pool that still buys something. Collapsing
	// straight to zero also "fits" — it is the required floor, which was
	// checked before the loop began — and would be indistinguishable from
	// convergence without this.
	if alloc.OptionalPool <= 0 || alloc.BoardBytes <= floor.RequiredBytes {
		t.Fatalf("the rescale collapsed the pool instead of converging: pool %d, board %d, floor %d",
			alloc.OptionalPool, alloc.BoardBytes, floor.RequiredBytes)
	}
	t.Logf("converged in %d attempts: pool %d, board %d of %d", alloc.Attempts, alloc.OptionalPool,
		alloc.BoardBytes, floor.RequiredBytes+60000)
}

// The board is measured on the same side of redaction as its budget.
// chiefBoardBudget subtracts every other section's REDACTED length, because
// that is what assembleSections receives; redact.String substitutes a 21-byte
// placeholder, so a registered credential of 8 to 20 characters echoed back
// into evidence GROWS the board after it was checked to fit.
func TestBoardIsMeasuredAfterRedaction(t *testing.T) {
	const secret = "k7Qv2Xb9La3Md"
	redact.Register(secret)
	if len(secret) >= len(redact.Placeholder) {
		t.Fatalf("the fixture credential does not grow under redaction: %d vs %d", len(secret), len(redact.Placeholder))
	}
	anchor := time.Date(2026, 9, 15, 17, 0, 30, 0, time.UTC)
	r := boardCompany("AAA", 3, 120)
	r.Documents = append(r.Documents, model.EvidenceDocument{ID: "AAA-rejected", Ticker: "AAA", Kind: "document",
		Source: "alphavantage", PublishedAt: anchor.AddDate(0, 0, -2), RetrievedAt: anchor,
		Error: "provider rejected https://example.test/query?function=OVERVIEW&apikey=" + secret})
	r.Dossier.Claims = append(r.Dossier.Claims, model.ResearchClaim{ID: "c-rejected", Kind: "observation",
		Text: "The filing was not retrievable", EvidenceIDs: []string{"AAA-rejected"}})
	_, floor, err := chiefBoard([]thesisResearch{r}, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	board, _, err := chiefBoard([]thesisResearch{r}, floor.RequiredBytes)
	if err != nil {
		t.Fatal(err)
	}
	raw, redacted := len(jsonText(board)), len(redact.String(jsonText(board)))
	if strings.Contains(jsonText(board), secret) == false {
		t.Fatal("the fixture no longer carries the credential into the board")
	}
	if redacted <= raw {
		t.Fatalf("the fixture credential does not grow the board: raw %d, redacted %d", raw, redacted)
	}
	if floor.RequiredBytes != redacted {
		t.Fatalf("the board was measured at %d, its raw size, against a budget built from redacted bodies (%d)",
			floor.RequiredBytes, redacted)
	}
	// The consequence, end to end: a board measured raw is accepted at a
	// budget it does not fit once preparePrompt redacts it.
	if _, _, err = chiefBoard([]thesisResearch{r}, redacted-1); err == nil {
		t.Fatalf("a %d-byte board was accepted against a %d-byte budget", redacted, redacted-1)
	}
}

// ---------------------------------------------------------------------------
// Task 13: revision and challenge prompt fitting (SNOW/OKTA/ORCL, 2026-09-15)
// ---------------------------------------------------------------------------

// readInputPack reads back one preparePrompt call's persisted artifact —
// the same file TestInitialAndReviewPassagesReachPersistedInputs and
// TestFullArticleReuseAndSavedReviewEvidence read — for the exact prompt
// text and its measured profile, rather than trusting a test's own
// reimplementation of what preparePrompt would have measured.
func readInputPack(t *testing.T, dir, name string) (prompt string, profile model.PromptProfile) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "data", "input-"+name+".json"))
	if err != nil {
		t.Fatalf("reading input pack %q: %v", name, err)
	}
	var v struct {
		Prompt  string              `json:"prompt"`
		Profile model.PromptProfile `json:"profile"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decoding input pack %q: %v", name, err)
	}
	return v.Prompt, v.Profile
}

// captured13Corpus builds a realistic-scale evidence/dossier corpus at the
// shape task-13-context.md's controller measurement records for the
// September 15 captures: dozens of evidence documents (most of them
// scaffolding — id, kind, dates, selected_spans — rather than text, the way
// 4 of SNOW's captured 51 documents carried no text at all), and a dossier
// whose claims and narratives run to the writing target's own ceiling. Only
// the first numClaims documents are cited; the rest are uncited context,
// costing scaffolding without a required quotation.
func captured13Corpus(ticker string, numDocs, numClaims int) ([]model.EvidenceDocument, model.CandidateDossier) {
	anchor := time.Now().UTC()
	docs := make([]model.EvidenceDocument, numDocs)
	var claims []model.ResearchClaim
	var claimIDs []string
	for i := 0; i < numDocs; i++ {
		id := fmt.Sprintf("%s-doc-%03d", ticker, i)
		filler := fmt.Sprintf("Filing excerpt %03d covers demand trends, consumption patterns and forward guidance for the reporting segment in additional operational detail. ", i)
		docs[i] = model.EvidenceDocument{ID: id, Ticker: ticker, Kind: "document", Source: "issuer", Authority: "issuer",
			PublishedAt: anchor.AddDate(0, 0, -5-(i%20)), RetrievedAt: anchor, Text: strings.Repeat(filler, 20)}
	}
	for i := 0; i < numClaims; i++ {
		quote := strings.TrimSpace(fmt.Sprintf("Metric %03d: consumption growth moderated while guidance held for the quarter.", i))
		docs[i].Text = strings.Repeat("Navigation and legal boilerplate repeated across the release. ", 15) + quote + strings.Repeat(" Trailing commentary continues after the disclosed figure for additional context.", 15)
		id := fmt.Sprintf("c%d", i)
		claimIDs = append(claimIDs, id)
		claims = append(claims, model.ResearchClaim{ID: id, Kind: "observation",
			Text:        fmt.Sprintf("Guidance commentary %d changes the near-term setup for the segment", i),
			EvidenceIDs: []string{docs[i].ID},
			Passages:    []model.ClaimPassage{{EvidenceID: docs[i].ID, Quote: quote, IssuerRole: "reporting issuer"}}})
	}
	pad := func(letter string) string { return strings.Repeat(letter, 380) }
	d := model.CandidateDossier{ContractVersion: 2, Ticker: ticker, Status: "supported", PreferredDirection: "BUY", EvidenceQuality: "mixed",
		LongCase: "Long case: " + pad("l"), ShortCase: "Short case: " + pad("s"), NoTradeCase: "No-trade case: " + pad("n"),
		Hypothesis: "Hypothesis: " + pad("h"), Changed: "Changed: " + pad("c"), Expectations: "Expectations: " + pad("e"),
		Underappreciated: "Underappreciated: " + pad("u"), Mechanism: "Mechanism: " + pad("m"), PricedIn: "Priced in: " + pad("p"),
		Counterargument: "Counterargument: " + pad("g"), Invalidation: "Invalidation: " + pad("i"), CatalystWindow: "Catalyst window: " + pad("w"),
		Claims: claims, ExpectationsClaimIDs: claimIDs, PricedInClaimIDs: claimIDs,
		Unresolved:      []string{"whether channel checks corroborate the disclosed metric", "how currency effects net against reported growth"},
		EntryConditions: []string{"confirm the metric holds for two more sessions", "no adverse guidance revision before entry"},
		Monitoring:      []string{"track the next scheduled disclosure date", "watch peer commentary for corroboration"}}
	return docs, d
}

func captured13Reviews(claimIDs []string, reason string) []model.ClaimReview {
	out := make([]model.ClaimReview, 0, len(claimIDs))
	for _, id := range claimIDs {
		out = append(out, model.ClaimReview{ClaimID: id, Assessment: "supported", Attribution: "confirmed", Reason: reason})
	}
	return out
}

// captured13Hash extracts the dossier hash a real model would echo back,
// reading it from the SAME prompt text it was given — exactly the technique
// currentFixtureReply already uses for older, contract-version-less
// fixtures (thesis_schema_test.go). A test fixture must never hardcode a
// hash computed independently of what the prompt actually says, because
// out.Dossier's exact serialization is not this test's to predict.
func captured13Hash(prompt string) string {
	m := regexp.MustCompile(`Dossier hash: ([a-f0-9]{64})`).FindStringSubmatch(prompt)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// TestCapturedRevisionAndChallengePromptsFit rebuilds the SNOW/OKTA/ORCL
// failure shape at realistic scale (task-13-context.md's controller
// measurement: ~51 evidence documents, ~47% of the evidence section's own
// bytes pure scaffinolding, a previous_dossier and previous_challenge/
// revision_issues carrying real bulk) and drives it through the actual
// production call sites — base(), challengeSections, the revision and
// challenge-final builders in thesis.go — rather than a hand-rolled
// reimplementation of what they do.
//
// A "revise" initial challenge forces exactly the sequence the captures
// exercised: round-1 dossier, initial challenge, revision (SNOW/OKTA's
// overflow site), final challenge (ORCL's overflow site).
func TestCapturedRevisionAndChallengePromptsFit(t *testing.T) {
	const ticker = "SNOW"
	docs, dossier := captured13Corpus(ticker, 140, 35)
	if raw := len(jsonText(dossier)); raw > 20480 {
		t.Fatalf("fixture dossier is %d raw bytes, over the researcher's own 20,480-byte response limit — it would take an unintended compaction detour instead of exercising the input-side fix this test targets", raw)
	}
	var claimIDs []string
	for _, c := range dossier.Claims {
		claimIDs = append(claimIDs, c.ID)
	}

	initialIssues := []string{
		"unresolved: whether the disclosed consumption metric nets out currency effects",
		"unresolved: guidance timing versus the described catalyst window",
		"unresolved: whether peer commentary corroborates the reported trend",
	}
	reviewReason := "The quoted passage attributes the disclosed metric to the reporting issuer and supports the claim as stated."

	calls := 0
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		calls++
		switch {
		case strings.Contains(prompt, "Final revision:"):
			return fenced(dossier)
		case strings.Contains(prompt, "# Independent thesis challenge") && calls > 2:
			return fenced(model.ThesisChallenge{ContractVersion: 2, Ticker: ticker, DossierHash: captured13Hash(prompt),
				Verdict: "supported", ConditionsReviewed: true,
				Reason:       "The revision resolved the disclosed metric's currency treatment and corroborated timing.",
				ClaimReviews: captured13Reviews(claimIDs, reviewReason)})
		case strings.Contains(prompt, "# Independent thesis challenge"):
			return fenced(model.ThesisChallenge{ContractVersion: 2, Ticker: ticker, DossierHash: captured13Hash(prompt),
				Verdict:        "revise",
				Reason:         "The currency treatment of the disclosed metric and its corroborating timing are unresolved.",
				MaterialIssues: initialIssues,
				ClaimReviews:   captured13Reviews(claimIDs, reviewReason)})
		default:
			return fenced(dossier)
		}
	})
	defer done()

	out := runner.investigate(context.Background(), model.Candidate{Ticker: ticker, Name: ticker}, docs, pack, nil)

	if out.Outcome.Transport != model.OutcomeOK {
		t.Fatalf("transport failed: %+v errors=%v", out.Outcome, out.Errors)
	}
	for _, e := range out.Errors {
		if strings.Contains(e, "input capacity exceeded") {
			t.Fatalf("a prompt was refused for capacity instead of fitting: %s", e)
		}
	}

	safeName := fmt.Sprintf("research-%x", []byte(ticker))
	revPrompt, revProfile := readInputPack(t, runner.run.Dir, safeName+"-revision")
	chalPrompt, chalProfile := readInputPack(t, runner.run.Dir, safeName+"-challenge-final")

	if revProfile.Bytes > revProfile.InputLimit {
		t.Errorf("revision prompt = %d bytes, over its own %d-byte limit", revProfile.Bytes, revProfile.InputLimit)
	}
	if chalProfile.Bytes > chalProfile.InputLimit {
		t.Errorf("challenge-final prompt = %d bytes, over its own %d-byte limit", chalProfile.Bytes, chalProfile.InputLimit)
	}
	if revProfile.Bytes != len(revPrompt) || chalProfile.Bytes != len(chalPrompt) {
		t.Fatalf("profile.Bytes disagrees with the persisted prompt length: revision %d/%d, challenge-final %d/%d",
			revProfile.Bytes, len(revPrompt), chalProfile.Bytes, len(chalPrompt))
	}

	// This input is a genuine repro, not one that happens to fit either way:
	// substituting the OLD fixed-24000-character evidence cap for THIS
	// company's evidence would have overflowed both budgets, matching the
	// captured SNOW/OKTA (+92 to +4,773 over, at the revision site) and ORCL
	// (+4,109 over, at the challenge-final site) failures.
	oldEvidence := evidenceLabel + jsonText(promptDocumentsAt(docs, 24000, runner.run.TS, evidenceClaims(dossier)...))
	if projected := revProfile.Bytes - revProfile.Components["evidence"] + len(oldEvidence); projected <= revProfile.InputLimit {
		t.Fatalf("fixture too small to reproduce the failure: the old fixed evidence cap would still have fit (%d <= %d bytes) on the revision prompt", projected, revProfile.InputLimit)
	}
	if projected := chalProfile.Bytes - chalProfile.Components["evidence"] + len(oldEvidence); projected <= chalProfile.InputLimit {
		t.Fatalf("fixture too small to reproduce the failure: the old fixed evidence cap would still have fit (%d <= %d bytes) on the challenge-final prompt", projected, chalProfile.InputLimit)
	}

	// Every unresolved review issue from the INITIAL challenge, and every
	// required quotation, must still be traceable in both later prompts.
	for _, want := range initialIssues {
		if !strings.Contains(revPrompt, want) {
			t.Errorf("revision prompt lost unresolved review issue %q", want)
		}
		if !strings.Contains(chalPrompt, want) {
			t.Errorf("challenge-final prompt lost unresolved review issue %q", want)
		}
	}
	// Quotation dedup, wired end to end: each claim's accepted quotation is
	// carried by the quotations section and by evidence's own required-span
	// rendering — never a THIRD time inline on the claim itself inside
	// previous_dossier/previous_challenge/revision_issues, the duplication
	// dossierReference/challengeReference exist to remove.
	for _, c := range dossier.Claims {
		q := c.Passages[0].Quote
		if !strings.Contains(revPrompt, q) {
			t.Errorf("revision prompt lost the required quotation for %s", c.ID)
		}
		if !strings.Contains(chalPrompt, q) {
			t.Errorf("challenge-final prompt lost the required quotation for %s", c.ID)
		}
		if n := strings.Count(revPrompt, q); n > 2 {
			t.Errorf("revision prompt still carries %d copies of %s's quotation, want at most 2 (evidence + quotations)", n, c.ID)
		}
		if n := strings.Count(chalPrompt, q); n > 2 {
			t.Errorf("challenge-final prompt still carries %d copies of %s's quotation, want at most 2 (evidence + quotations)", n, c.ID)
		}
	}
	t.Logf("revision: %d/%d bytes (evidence %d; old-style evidence %d would project to %d, %d over); "+
		"challenge-final: %d/%d bytes (evidence %d; old-style would project to %d, %d over)",
		revProfile.Bytes, revProfile.InputLimit, revProfile.Components["evidence"], len(oldEvidence),
		revProfile.Bytes-revProfile.Components["evidence"]+len(oldEvidence), revProfile.Bytes-revProfile.Components["evidence"]+len(oldEvidence)-revProfile.InputLimit,
		chalProfile.Bytes, chalProfile.InputLimit, chalProfile.Components["evidence"],
		chalProfile.Bytes-chalProfile.Components["evidence"]+len(oldEvidence), chalProfile.Bytes-chalProfile.Components["evidence"]+len(oldEvidence)-chalProfile.InputLimit)
}

// TestEachRequiredQuotationIsStoredOnceAndStillResolvable pins Task 13's
// quotation dedup: a quotation cited by three claims used to be serialized
// once per citing claim, in every prompt that echoed the dossier back as
// context (previous_dossier, previous_challenge, revision_issues) — on top
// of the SAME text already carried, verbatim, by the evidence section's own
// required-span rendering.
func TestEachRequiredQuotationIsStoredOnceAndStillResolvable(t *testing.T) {
	quote := "Issuer raised guidance and expects its delivery update in two weeks and reiterated the full-year outlook."
	doc := model.EvidenceDocument{ID: "shared-doc", Ticker: "AAA", Kind: "document",
		Text: strings.Repeat("Boilerplate legal text repeated across the page. ", 20) + quote + strings.Repeat(" Trailing commentary follows.", 20)}
	claims := []model.ResearchClaim{
		{ID: "c1", Kind: "observation", Text: "Guidance raised", EvidenceIDs: []string{doc.ID}, Passages: []model.ClaimPassage{{EvidenceID: doc.ID, Quote: quote, IssuerRole: "reporting issuer"}}},
		{ID: "c2", Kind: "inference", Text: "Outlook reiterated", EvidenceIDs: []string{doc.ID}, Passages: []model.ClaimPassage{{EvidenceID: doc.ID, Quote: quote, IssuerRole: "reporting issuer"}}},
		{ID: "c3", Kind: "inference", Text: "Delivery timing confirmed", EvidenceIDs: []string{doc.ID}, Passages: []model.ClaimPassage{{EvidenceID: doc.ID, Quote: quote, IssuerRole: "reporting issuer"}}},
	}
	d := model.CandidateDossier{ContractVersion: 2, Ticker: "AAA", Status: "supported", PreferredDirection: "BUY", EvidenceQuality: "mixed", Claims: claims}

	qs := requiredQuotations(claims)
	if len(qs) != 1 {
		t.Fatalf("requiredQuotations = %d entries, want exactly 1 for one quotation cited by three claims: %+v", len(qs), qs)
	}
	if qs[0].EvidenceID != doc.ID || qs[0].Quote != quote {
		t.Fatalf("wrong quotation stored: %+v", qs[0])
	}
	if !strings.Contains(doc.Text, qs[0].Quote) {
		t.Fatal("stored quotation does not occur verbatim in its cited document — not resolvable against the same input")
	}

	ref := dossierReference(d)
	body := jsonText(ref)
	if strings.Count(body, quote) != 0 {
		t.Fatalf("dossierReference still carries %d inline copies of the quotation", strings.Count(body, quote))
	}
	for i, c := range ref.Claims {
		if c.ID != claims[i].ID || len(c.Passages) != 1 || c.Passages[0].EvidenceID != doc.ID {
			t.Fatalf("claim %d lost its identity or evidence reference: %+v", i, c)
		}
		if c.Passages[0].Quote != "" {
			t.Fatalf("claim %d still carries its own inline copy of the quotation", i)
		}
	}

	ch := model.ThesisChallenge{ContractVersion: 2, Ticker: "AAA", Verdict: "supported", Claims: claims}
	chRef := challengeReference(ch)
	if strings.Count(jsonText(chRef), quote) != 0 {
		t.Fatal("challengeReference still carries an inline copy of the quotation")
	}
	if len(chRef.Claims) != 3 || chRef.Claims[0].Passages[0].EvidenceID != doc.ID {
		t.Fatalf("challengeReference lost claim identity or evidence reference: %+v", chRef.Claims)
	}
}

// TestSuccessfulCompactionDoesNotOverflowTheRevisionPrompt covers what Task
// 11's fix round already built (compaction_originals, thesis.go's base()):
// it is a NAMED, independently measured section — unlike the pre-Task-11
// marker scan, which had no entry for it at all and silently folded its
// bytes into previous_dossier, its immediate predecessor in the old
// concatenation — and, being Mandatory: false, it is the section a real
// limit sheds first rather than let a tight budget overflow silently.
func TestSuccessfulCompactionDoesNotOverflowTheRevisionPrompt(t *testing.T) {
	originals := map[string]map[string]string{"AAA-round-1": {"long_case": "Original uncompacted long case text describing the setup in full before it was shortened for budget."}}
	body := "\nOriginal narratives from bounded compaction (check that no qualifications or counterarguments were lost):\n" + jsonText(originals)
	section := promptSection{Name: "compaction_originals", Mandatory: false, Body: body}

	text, sizes, omitted, err := assembleSections([]promptSection{{Name: "identity", Mandatory: true, Body: "id"}, section}, noSectionLimit)
	if err != nil {
		t.Fatal(err)
	}
	if sizes["compaction_originals"] != len(body) {
		t.Fatalf("compaction_originals measured at %d, want its own exact %d bytes", sizes["compaction_originals"], len(body))
	}
	if !strings.Contains(text, "Original uncompacted long case text") {
		t.Fatal("compaction_originals missing from the assembled prompt at an unbounded limit")
	}
	if len(omitted) != 0 {
		t.Fatalf("omitted = %v, want none at an unbounded limit", omitted)
	}

	// Dropped and named, never silently overflowing, once the budget really
	// is tight.
	tight := len("id") + 1
	text, sizes, omitted, err = assembleSections([]promptSection{{Name: "identity", Mandatory: true, Body: "id"}, section}, tight)
	if err != nil {
		t.Fatalf("the mandatory total alone (%d bytes) must fit a %d-byte limit: %v", len("id"), tight, err)
	}
	if len(omitted) != 1 || omitted[0] != "compaction_originals" {
		t.Fatalf("omitted = %v, want exactly [compaction_originals]", omitted)
	}
	if _, ok := sizes["compaction_originals"]; ok {
		t.Fatal("a dropped section must not still be counted in sizes")
	}
	if strings.Contains(text, "Original uncompacted") {
		t.Fatal("a dropped section's content still reached the assembled text")
	}

	// End to end: a real compaction's originals reach the challenge prompt
	// as this SAME named, measured section — not folded into previous_dossier.
	id := fixtureEvidenceID(t)
	d := supportedResearch().Dossier
	d.LongCase = "Material qualification: delivery timing remains uncertain. " + strings.Repeat("Repeated explanation. ", 1400)
	d.Claims[0].EvidenceIDs = []string{id}
	d.Claims[0].Passages[0].EvidenceID = id
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		if strings.Contains(prompt, "# Independent thesis challenge") {
			return fenced(model.ThesisChallenge{Ticker: "AAA", Verdict: "supported", Reason: "Material qualifications retained", CompactionAssessment: "preserved", Claims: d.Claims})
		}
		if strings.Contains(prompt, "Compact this complete dossier") {
			next := d
			next.LongCase = "Delivery opportunity depends on uncertain timing."
			return fenced(next)
		}
		return fenced(d)
	})
	defer done()
	out := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA"}, nil, pack, nil)
	if out.Outcome.Contract != "compacted" {
		t.Fatalf("fixture did not actually take a compaction: %+v", out.Outcome)
	}
	_, profile := readInputPack(t, runner.run.Dir, "research-414141-challenge")
	if profile.Components["compaction_originals"] == 0 {
		t.Fatalf("compaction_originals is not a measured, named component of the challenge prompt: %+v", profile.Components)
	}
}

// TestRepeatedRequestResultsCompactToIDsWithoutForgettingOutcomes pins
// compactResults (thesis_compaction.go): a request answered unsupported,
// unavailable or already attempted is worth explaining in full once, in the
// round it happened — carrying the same sentence into every later round
// unchanged is exactly what grew request_results from 1,725 to 5,114 bytes
// across the three captured overflows with none of it new information.
func TestRepeatedRequestResultsCompactToIDsWithoutForgettingOutcomes(t *testing.T) {
	full := []model.ResearchResult{
		{Request: model.ResearchRequest{Kind: "web_search", Question: "search the web"}, Outcome: model.RequestUnsupported, Detail: "No such operation: web_search. The only operations are document, news, filings, passage."},
		{Request: model.ResearchRequest{Kind: "document", URL: "https://not-in-evidence.example/x", Question: "read this"}, Outcome: model.RequestUnsupported, Detail: "That URL is not in your evidence.", Repeats: 2},
		{Request: model.ResearchRequest{Kind: "passage", EvidenceID: "ev-does-not-exist", Query: "guidance"}, Outcome: model.RequestUnavailable, Detail: "No readable saved document has evidence id ev-does-not-exist."},
		{Request: model.ResearchRequest{Kind: "news"}, Outcome: model.RequestAlreadyDone, Detail: "News is fetched once before research begins."},
		{Request: model.ResearchRequest{Kind: "document", URL: "https://issuer.example/release"}, Outcome: model.RequestFulfilled, Detail: "1000 characters of source text added.", EvidenceIDs: []string{"doc-1"}},
	}
	compacted := compactResults(full)
	if len(compacted) != len(full) {
		t.Fatalf("compactResults changed the entry count: %d -> %d", len(full), len(compacted))
	}
	for i, r := range compacted {
		if full[i].Outcome == model.RequestFulfilled {
			if r.Detail == "" || len(r.EvidenceIDs) == 0 {
				t.Errorf("a fulfilled request lost the record the model has to act on: %+v", r)
			}
			continue
		}
		if r.Detail != "" {
			t.Errorf("entry %d (%s) still carries its full Detail sentence in the compacted view: %+v", i, full[i].Outcome, r)
		}
		if r.Outcome != full[i].Outcome {
			t.Errorf("entry %d outcome changed: %q -> %q — the model must still be told not to re-issue it", i, full[i].Outcome, r.Outcome)
		}
		if r.Request.Kind != full[i].Request.Kind {
			t.Errorf("entry %d lost its identifying kind", i)
		}
	}
	// The compacted view is a smaller PROMPT projection; it never touches the
	// full ledger it was built from.
	if full[1].Detail == "" || full[1].Repeats != 2 {
		t.Fatal("compactResults mutated its input instead of returning a new slice")
	}
	// The model is never invited to re-issue a compacted request: identity
	// (kind + whichever of url/evidence_id/observation_date it carried) is
	// preserved so a later round's own identical request is recognizable.
	if compacted[1].Request.URL != "https://not-in-evidence.example/x" {
		t.Fatal("compacted document request lost its identifying URL")
	}
	if compacted[2].Request.EvidenceID != "ev-does-not-exist" {
		t.Fatal("compacted passage request lost its identifying evidence_id")
	}
}

// TestShorteningAProjectionCannotAuthorizeADifferentDossier is the one way
// prompt shortening (Task 12's global board, Task 13's per-call evidence
// resizing) could silently change what a review authorized: dossierHash
// (thesis_budget.go) hashes the full CandidateDossier struct — never
// anything derived from a prompt's projection of it — so it cannot move
// merely because two prompts showed different amounts of supporting
// evidence text for the SAME dossier.
func TestShorteningAProjectionCannotAuthorizeADifferentDossier(t *testing.T) {
	d := supportedResearch().Dossier
	doc := model.EvidenceDocument{ID: "big-doc", Ticker: "AAA", Kind: "document",
		Text: strings.Repeat("filler content padding out the source document. ", 500) + "Issuer raised guidance and expects its delivery update in two weeks."}
	claims := []model.ResearchClaim{{ID: "c1", EvidenceIDs: []string{doc.ID}, Passages: []model.ClaimPassage{{EvidenceID: doc.ID, Quote: "Issuer raised guidance and expects its delivery update in two weeks.", IssuerRole: "issuer"}}}}

	small := jsonText(promptDocuments([]model.EvidenceDocument{doc}, 100, claims...))
	large := jsonText(promptDocuments([]model.EvidenceDocument{doc}, 5000, claims...))
	if small == large {
		t.Fatal("fixture does not actually exercise two different projections of the same evidence")
	}

	hash := dossierHash(d)
	if dossierHash(d) != hash {
		t.Fatal("dossierHash is not even deterministic against itself")
	}
	projectionHash := fmt.Sprintf("%x", sha256.Sum256([]byte(large)))
	if projectionHash == hash {
		t.Fatal("fixture collision: the projection hash coincides with the dossier hash")
	}

	good := model.ThesisChallenge{ContractVersion: 2, Ticker: d.Ticker, DossierHash: hash, Verdict: "supported",
		ClaimReviews: []model.ClaimReview{{ClaimID: "c1", Assessment: "supported", Attribution: "confirmed", Reason: "ok"}}}
	if p := compactReviewProblems(d, good); len(p) != 0 {
		t.Fatalf("the correct dossier hash was rejected: %v", p)
	}
	bad := good
	bad.DossierHash = projectionHash
	if p := compactReviewProblems(d, bad); len(p) == 0 {
		t.Fatal("a review echoing the projection hash instead of the full dossier hash was accepted")
	}
}

// TestCapacityUsesBytesWhileSpanOffsetsStayUnicodeCharacters pins two
// distinct facts at once: EvidenceSpan offsets index RUNES into the stored
// text (model.EvidenceSpan's own contract), while the capacity DECISION —
// whether a prompt fits its role's byte budget — must gate on UTF-8 BYTES,
// never runes. A foreign-language filing (阿里巴巴集团, and the wider set of
// non-ASCII sources sizeEvidence's convergence loop exists for) can run
// several bytes per rune, so a prompt whose RUNE count looks comfortably
// under a limit can still overflow it in bytes.
func TestCapacityUsesBytesWhileSpanOffsetsStayUnicodeCharacters(t *testing.T) {
	mixed := "ASCII prefix text precedes the quotation. 阿里巴巴集团 raised guidance — 🎯 delivery window follows and more filler text keeps this away from either edge of the document for the merge logic to exercise correctly."
	quote := "阿里巴巴集团 raised guidance — 🎯 delivery window follows"
	if !strings.Contains(mixed, quote) {
		t.Fatal("fixture setup: quote is not a substring of the document")
	}
	doc := model.EvidenceDocument{ID: "cjk-doc", Ticker: "9988.HK", Kind: "document", Text: mixed}
	claim := model.ResearchClaim{ID: "c1", EvidenceIDs: []string{doc.ID}, Passages: []model.ClaimPassage{{EvidenceID: doc.ID, Quote: quote, IssuerRole: "issuer"}}}

	spans := requiredSpans(doc, []model.ResearchClaim{claim})
	if len(spans) != 1 {
		t.Fatalf("expected exactly one required span, got %+v", spans)
	}
	wantRunes := len([]rune(quote))
	if spans[0].End-spans[0].Start != wantRunes {
		t.Fatalf("span width = %d, want %d RUNES — offsets must index runes, not bytes", spans[0].End-spans[0].Start, wantRunes)
	}
	if rendered := renderEvidenceSpans(doc.Text, spans); rendered != quote {
		t.Fatalf("rendered span = %q, want the exact quote %q — rune offsets must still locate the exact bytes after assembly", rendered, quote)
	}

	// The capacity decision itself must gate on bytes: build a prompt whose
	// CJK-heavy section is well under a plausible RUNE cap (a naive
	// utf8.RuneCountInString check would pass it) but whose BYTE length
	// exceeds a small role input budget. The budget is derived from the
	// persona wrapper's own measured cost, not a guessed constant: the
	// researcher persona alone already costs several KB, so a hardcoded
	// small limit would fail for that reason alone and prove nothing about
	// the CJK section specifically.
	runner, _, done := thesisFixture(t, func(string, int) string { return "unused" })
	defer done()
	wrapper, werr := runner.promptWrapperBytes("thesis-researcher")
	if werr != nil {
		t.Fatal(werr)
	}
	const identityBody = "id"
	limit := wrapper + len(identityBody) + 512
	runner.cfg.Research.Budgets.Researcher = model.RoleBudget{InputBytes: limit, ResponseBytes: 1024}
	cjk := strings.Repeat("阿里巴巴集团营收增长强劲", 200) // 12 runes/rep * 3 bytes/rune = 36 bytes/rep
	runeCount := utf8.RuneCountInString(cjk)
	if runeCount >= limit {
		t.Fatalf("fixture invalid: %d runes must be well under the %d-byte limit to prove a BYTE check, not a rune check, is what refuses this", runeCount, limit)
	}
	sections := []promptSection{
		{Name: "identity", Mandatory: true, Body: identityBody},
		{Name: "evidence", Mandatory: true, Body: cjk},
	}
	_, _, err := runner.preparePrompt("thesis-researcher", "cjk-capacity", sections, 0)
	if err == nil {
		t.Fatal("a prompt over its byte limit but comfortably under a rune-counted limit was accepted")
	}
	if promptFailureKind(err) != "input_capacity" {
		t.Fatalf("failure kind = %q, want input_capacity", promptFailureKind(err))
	}

	// sizeEvidence's own convergence must actually SHRINK a CJK-heavy corpus
	// to fit a small byte budget, not merely happen to fit one that was
	// already small enough on its own: pad the corpus with plenty of
	// uncited CJK context so an implementation that ignored the available
	// byte budget (e.g. rendering at a large fixed rune cap regardless of
	// what was passed in) would clearly overflow the available room. The
	// budget again has to clear the wrapper's own real cost, or the "room
	// for evidence" this is meant to measure is negative before evidence
	// even starts.
	available := 4096
	runner.cfg.Research.Budgets.Researcher = model.RoleBudget{InputBytes: wrapper + available, ResponseBytes: 1024}
	big := model.EvidenceDocument{ID: "cjk-required", Ticker: "9988.HK", Kind: "document", Text: strings.Repeat("填充文字用于撑大文档内容长度。", 5) + quote}
	bigClaim := model.ResearchClaim{ID: "c2", EvidenceIDs: []string{big.ID}, Passages: []model.ClaimPassage{{EvidenceID: big.ID, Quote: quote, IssuerRole: "issuer"}}}
	padding := make([]model.EvidenceDocument, 2)
	for i := range padding {
		padding[i] = model.EvidenceDocument{ID: fmt.Sprintf("cjk-padding-%d", i), Ticker: "9988.HK", Kind: "document",
			Text: strings.Repeat("大量未引用的中文上下文用于撑满可选预算而不是必须引用的内容。", 40)}
	}
	corpus := append([]model.EvidenceDocument{big}, padding...)
	rendered := runner.sizeEvidence("thesis-researcher", corpus, time.Time{}, []model.ResearchClaim{bigClaim}, nil, nil)
	if got := len(redact.String(jsonText(rendered))) + len(evidenceLabel); got > available {
		t.Fatalf("sizeEvidence produced %d bytes (+label) against a %d-byte allowance for evidence — it did not actually shrink for the byte budget it was given", got, available)
	}
	if !strings.Contains(jsonText(rendered), quote) {
		t.Fatal("sizeEvidence dropped the required quotation while still overflowing on padding")
	}
}
