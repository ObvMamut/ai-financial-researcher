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
	_, profile, err := runner.preparePrompt("thesis-chief", "chief-fixture", jsonText(chiefContext([]thesisResearch{out})), 8192)
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
	_, err := runner.call(context.Background(), "thesis-researcher", "oversize", strings.Repeat("x", 100<<10), model.CLIApi)
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
	view := chiefContext(research)
	if len(view) != 12 || view[10].Dossier != nil || view[11].Dossier != nil {
		t.Fatal("failed or deferred company lost/treated as actionable")
	}
	_, p, err := runner.preparePrompt("thesis-chief", "twelve", jsonText(view), 8192)
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
	if _, _, err := runner.preparePrompt("thesis-challenger", "omitted", jsonText(selected), 8192); err == nil {
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
	r, err := runner.call(ctx, "thesis-researcher", "cancelled", "data", model.CLIApi)
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
