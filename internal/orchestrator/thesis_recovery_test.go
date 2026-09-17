package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestSeptember13DossiersReachResearchContract(t *testing.T) {
	files, err := filepath.Glob("testdata/sep13-dossiers/*.md")
	if err != nil {
		t.Fatal(err)
	}
	tested, compacted := 0, 0
	for _, file := range files {
		if filepath.Base(file) == "README.md" {
			continue
		}
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var original model.CandidateDossier
			if err := decodeResearch(string(raw), &original); err != nil {
				t.Fatal(err)
			}
			calls := 0
			runner, _, done := thesisFixture(t, func(prompt string, _ int) string {
				calls++
				if strings.Contains(prompt, "Compact this complete dossier") {
					var value map[string]any
					if err := decodeResearch(string(raw), &value); err != nil {
						t.Fatal(err)
					}
					for _, field := range dossierNarrativeFields {
						value[field] = "Concise synthetic narrative retaining uncertainty."
					}
					return fenced(value)
				}
				return string(raw)
			})
			defer done()
			var out model.CandidateDossier
			reports, transport, parsing, err := researchCall(context.Background(), runner, "thesis-researcher", "captured", "Captured response regression", &out, dossierSchema)
			if err != nil || transport != model.OutcomeOK || parsing != model.OutcomeOK {
				t.Fatalf("dossier discarded: transport=%s parsing=%s err=%v", transport, parsing, err)
			}
			if !reflect.DeepEqual(original.Claims, out.Claims) || !reflect.DeepEqual(original.Requests, out.Requests) || original.Status != out.Status || out.PreferredDirection != "NONE" {
				t.Fatal("recovery changed findings or skipped research requests")
			}
			if original.Ticker == "SAN.PA" {
				compacted++
				if calls != 2 || len(reports) != 2 || reports[1].Contract != "compacted" || len(reports[1].OriginalNarratives) != 12 {
					t.Fatalf("missing bounded compaction provenance: %+v", reports)
				}
			} else if calls != 1 || len(reports[0].WritingDiagnostics) == 0 {
				t.Fatal("writing targets triggered a repair or were not measured")
			}
			tested++
		})
	}
	if tested != 12 || compacted != 1 {
		t.Fatalf("tested=%d compacted=%d", tested, compacted)
	}
}

func TestWritingTargetsDoNotEraseCompleteDossiers(t *testing.T) {
	for _, length := range []int{400, 401} {
		d := supportedResearch().Dossier
		d.LongCase = strings.Repeat("界", length)
		d.Claims[0].Passages[0].Quote = strings.Repeat("é", 301)
		if problems := dossierSchema(&d); len(problems) != 0 {
			t.Fatal(problems)
		}
		if len(dossierWritingDiagnostics(&d)) == 0 {
			t.Fatal("missing length diagnostics")
		}
	}
}

func TestCompactionProtectsAllNonNarrativeFields(t *testing.T) {
	d := supportedResearch().Dossier
	d.EntryConditions = []string{"Confirm executable quote"}
	d.Monitoring = []string{"Monitor future deliveries"}
	d.Unresolved = []string{"Missing filing"}
	d.Events = []model.ResearchEvent{{Kind: "earnings", OccurredAt: "2026-09-01T12:00:00Z", EvidenceID: "source", Passage: "Issuer date"}}
	var original map[string]any
	if err := json.Unmarshal([]byte(jsonText(d)), &original); err != nil {
		t.Fatal(err)
	}
	original["future_extension"] = map[string]any{"uncertainty": "must survive"}
	before := fenced(original)
	for _, field := range []string{"claims", "unresolved", "entry_conditions", "monitoring", "events", "status", "future_extension", "expectations_claim_ids"} {
		t.Run(field, func(t *testing.T) {
			var value map[string]any
			if err := decodeResearch(before, &value); err != nil {
				t.Fatal(err)
			}
			value[field] = "changed"
			if compactionPreservesEvidence(before, fenced(value)) == nil {
				t.Fatal("protected field changed")
			}
		})
	}
	original["long_case"] = "Shortened narrative; uncertainty remains."
	if err := compactionPreservesEvidence(before, fenced(original)); err != nil {
		t.Fatal(err)
	}
	original["counterargument"] = ""
	if compactionPreservesEvidence(before, fenced(original)) == nil {
		t.Fatal("erased counterargument")
	}
}

func TestCompactionFailureDoesNotBuyAnotherRepair(t *testing.T) {
	for _, failure := range []string{"oversize", "malformed", "changed_status"} {
		t.Run(failure, func(t *testing.T) {
			d := supportedResearch().Dossier
			d.ContractVersion = 2
			d.LongCase = strings.Repeat("long narrative ", 2000)
			calls := 0
			runner, _, done := thesisFixture(t, func(prompt string, _ int) string {
				calls++
				if !strings.Contains(prompt, "Compact this complete dossier") || failure == "oversize" {
					return fenced(d)
				}
				if failure == "malformed" {
					return "incomplete response"
				}
				next := d
				next.LongCase = "short"
				next.Status = "rejected"
				return fenced(next)
			})
			defer done()
			var out model.CandidateDossier
			reports, transport, parsing, err := researchCall(context.Background(), runner, "thesis-researcher", "failed", "data", &out, dossierSchema)
			if err == nil || calls != 2 || transport != model.OutcomeOK || parsing != model.OutcomeOK || out.Ticker != "" {
				t.Fatalf("failure hidden or retried: calls=%d transport=%s parsing=%s err=%v", calls, transport, parsing, err)
			}
			r := thesisResearch{}
			r.addResearchReports(reports)
			if r.Outcome.Contract != model.OutcomeFailed || r.Outcome.CompactionAttempts != 1 || !r.researchFailed() {
				t.Fatalf("failure provenance lost: %+v", r.Outcome)
			}
		})
	}
}

func TestCompactionCancellationAndInputBudgetPreventDispatch(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		calls := 0
		runner, _, done := thesisFixture(t, func(string, int) string { calls++; return "unexpected" })
		ctx, cancel := context.WithCancel(context.Background())
		if cancelled {
			cancel()
		}
		var d model.CandidateDossier
		r, err := compactDossier(ctx, runner, "thesis-researcher", "bounded", strings.Repeat("界", 100000), &d)
		cancel()
		done()
		if err == nil || calls != 0 || r.Attempts != 0 {
			t.Fatal("cancelled/over-budget compaction dispatched")
		}
	}
}

func TestConditionsRequireReviewAndDoNotHideCoreEvidenceGaps(t *testing.T) {
	r := supportedResearch()
	r.Dossier.ContractVersion = 2
	r.Dossier.ExpectationsClaimIDs, r.Dossier.PricedInClaimIDs = []string{"c1"}, []string{"c1"}
	r.Dossier.EntryConditions = []string{"Confirm the executable quote before entry"}
	r.Dossier.Monitoring = []string{"Monitor next month's delivery announcement"}
	validateDossier(&r)
	if r.Dossier.Status != "supported" {
		t.Fatalf("future monitoring became missing evidence: %+v", r.Dossier)
	}
	c := model.ThesisChallenge{ContractVersion: 2, DossierHash: dossierHash(r.Dossier), Verdict: "supported", ClaimReviews: []model.ClaimReview{{ClaimID: "c1", Assessment: "supported", Attribution: "confirmed", Reason: "issuer source"}}}
	if len(compactReviewProblems(r.Dossier, c)) == 0 {
		t.Fatal("unreviewed conditions accepted")
	}
	c.ConditionsReviewed = true
	if problems := compactReviewProblems(r.Dossier, c); len(problems) != 0 {
		t.Fatal(problems)
	}
	r.Dossier.Unresolved = []string{"Missing issuer attribution"}
	validateDossier(&r)
	if r.Dossier.Status == "supported" {
		t.Fatal("entry conditions bypassed missing core evidence")
	}
}

func TestLongNarrativeStillRetrievesAndReceivesIndependentReview(t *testing.T) {
	id := fixtureEvidenceID(t)
	d := supportedResearch().Dossier
	d.LongCase = strings.Repeat("Evidence and counterargument remain explicit. ", 12)
	d.Claims[0].EvidenceIDs = []string{id}
	d.Claims[0].Passages[0].EvidenceID = id
	calls := 0
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		calls++
		if strings.Contains(prompt, "# Independent thesis challenge") {
			return fenced(model.ThesisChallenge{Ticker: "AAA", Verdict: "supported", Reason: "Source supports the mechanism", Claims: d.Claims})
		}
		next := d
		if strings.Contains(prompt, "Round 1/3") {
			next.Requests = []model.ResearchRequest{{Kind: "passage", EvidenceID: id, Query: "Issuer raised guidance", Question: "Verify the delivery statement"}}
		} else if !strings.Contains(prompt, "Passage extracted from "+id) {
			t.Error("retrieval answer missing from next research round")
		}
		return fenced(next)
	})
	defer done()
	out := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA"}, nil, pack, nil)
	if calls != 3 || out.Dossier.Status != "supported" || out.Outcome.Review != "supported" || out.Outcome.RepairAttempts != 0 {
		t.Fatalf("research interrupted: calls=%d outcome=%+v", calls, out.Outcome)
	}
}

func TestCompactionOriginalsReachIndependentChallenge(t *testing.T) {
	id := fixtureEvidenceID(t)
	d := supportedResearch().Dossier
	d.LongCase = "Material qualification: delivery timing remains uncertain. " + strings.Repeat("Repeated explanation. ", 1400)
	d.Claims[0].EvidenceIDs = []string{id}
	d.Claims[0].Passages[0].EvidenceID = id
	reviewed := false
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		if strings.Contains(prompt, "# Independent thesis challenge") {
			reviewed = true
			if !strings.Contains(prompt, d.LongCase) {
				t.Error("original qualification omitted from independent review")
			}
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
	if !reviewed || out.Dossier.Status != "supported" || out.Outcome.Contract != "compacted" || out.Outcome.CompactionAttempts != 1 {
		t.Fatalf("compaction did not reach review: %+v", out.Outcome)
	}
}

func TestEquivalentDocumentRequestsReuseEvidenceButFutureDatesStayUnavailable(t *testing.T) {
	id := fixtureEvidenceID(t)
	d := supportedResearch().Dossier
	d.Claims[0].EvidenceIDs = []string{id}
	d.Claims[0].Passages[0].EvidenceID = id
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		if strings.Contains(prompt, "# Independent thesis challenge") {
			return fenced(model.ThesisChallenge{Ticker: "AAA", Verdict: "supported", Reason: "source supports thesis", Claims: d.Claims})
		}
		next := d
		if strings.Contains(prompt, "Round 1/3") {
			next.Requests = []model.ResearchRequest{
				{Kind: "document", URL: "https://ISSUER.example/release#main", Question: "Read existing release"},
				{Kind: "document", URL: "https://issuer.example/release", ObservationDate: "2099-01-01", Question: "Unavailable future release"},
			}
		}
		return fenced(next)
	})
	defer done()
	out := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA"}, nil, pack, nil)
	reused, future := false, false
	for _, r := range out.Results {
		if r.Outcome == model.RequestAlreadyDone && len(r.EvidenceIDs) == 1 && r.EvidenceIDs[0] == id {
			reused = true
		}
		if r.Request.ObservationDate == "2099-01-01" && r.Outcome == model.RequestUnavailable {
			future = true
		}
	}
	if !reused || !future || out.Dossier.Status != "supported" {
		t.Fatalf("request semantics lost: %+v", out.Results)
	}
}

func TestClaudeDisabledSubscriptionStopsWithoutUnchangedRetry(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'Your organization has disabled Claude subscription access for Claude Code' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := runAgent(context.Background(), model.CLIClaude, "chief-analyst", "synthesis", "fixture", time.Second, model.RetryPolicy{MaxAttempts: 3}, "", bin, model.APIConfig{})
	if r.Status != model.StatusFailed || r.FailureKind != "authentication" || r.Attempts != 1 || !strings.Contains(r.Err, "disabled Claude subscription access") {
		t.Fatalf("permanent access failure lost or retried: %+v", r)
	}
}
