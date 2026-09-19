package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
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
// of `runes` characters each, and no case narrative, so a test of the
// document allocation measures only the document allocation.
func boardCompany(ticker string, sources, runes int) thesisResearch {
	anchor := time.Date(2026, 9, 15, 17, 0, 30, 0, time.UTC)
	filler := "Reported results were described without resolving the question at issue. "
	r := thesisResearch{
		Candidate: model.Candidate{Ticker: ticker, Name: ticker, Sector: "Health Care"},
		Dossier:   model.CandidateDossier{Ticker: ticker, Status: "supported", PreferredDirection: "BUY", EvidenceQuality: "mixed"},
		Challenge: model.ThesisChallenge{ContractVersion: 2, Ticker: ticker, Verdict: "supported"},
		Outcome: model.ResearchOutcome{Ticker: ticker, Transport: model.OutcomeOK, Parsing: model.OutcomeOK,
			Evidence: model.EvidenceDocuments, Review: "supported"},
		Temporal: researchTimeFacts{AsOf: anchor.Format(time.RFC3339)},
	}
	for i := 0; i < sources; i++ {
		id := fmt.Sprintf("%s-source-%03d", ticker, i)
		quote := fmt.Sprintf("%s source %03d states: ", ticker, i) + strings.Repeat("g", runes)
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
	// A budget that leaves far less optional context than the board wants:
	// the first three companies alone would absorb all of it.
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
	// head its full need and the tail whatever survived.
	total := 0
	for _, s := range alloc.Companies {
		total += s.ContextNeed
	}
	head := shareFor(alloc, "C00")
	for _, s := range []boardShare{tailShare, head} {
		want := alloc.OptionalPool * s.ContextNeed / total
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
