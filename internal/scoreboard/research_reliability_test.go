package scoreboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

func TestReliabilityDiagnosticsReproduceSeptember10Counts(t *testing.T) {
	// Sanitized operational fixture: no provider prose, credentials or company data.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/research-sep10.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Meta    model.RunMeta          `json:"meta"`
		Results []model.ResearchResult `json:"results"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	meta := &fixture.Meta
	results := fixture.Results
	b, err := json.Marshal(struct {
		Candidate model.Candidate        `json:"candidate"`
		Results   []model.ResearchResult `json:"results"`
	}{model.Candidate{Ticker: "FIXTURE"}, results})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "data", "research-46495854555245.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	d := researchDiagnostics(store.RunSummary{Dir: dir}, &model.IdeasResult{Ideas: []model.TradeIdea{}}, nil, meta, nil)
	if d.LogicalCalls != 44 || d.Usage.Attempts != 65 || d.AttemptedCompanies != 10 || d.DeferredCompanies != 2 || d.CompletedResearch != 1 || d.FailedResearch != 9 || d.TruncatedCalls != 10 || d.TruncatedAttempts != 20 || d.InferredTruncations != 10 || d.Requests != 69 || !d.PrimaryChiefFailed || !d.FallbackSucceeded {
		t.Fatalf("operational counts wrong: %+v", d)
	}
	if d.Usage.IncompleteAttempts == 0 || d.Usage.PromptTokens != 1748299 || d.Usage.CompletionTokens != 438995 {
		t.Fatalf("historical usage not reproduced: %+v", d.Usage)
	}
	for i := range meta.Domains {
		if meta.Domains[i].Domain == "chief-analyst-fallback" {
			meta.Domains[i].Payload = "invalid"
		}
	}
	d = researchDiagnostics(store.RunSummary{Dir: dir}, &model.IdeasResult{Ideas: []model.TradeIdea{}}, nil, meta, nil)
	if d.FallbackSucceeded {
		t.Fatal("malformed fallback labeled successful")
	}
}

// loadResearchDiagnosticsFixture reproduces the same setup
// TestReliabilityDiagnosticsReproduceSeptember10Counts uses: unmarshal a
// sanitized {meta, results} fixture, drop the results into a fake
// research-<hex>.json artifact so the request-outcome glob has something to
// read, and run it through the real researchDiagnostics. Fixtures loaded this
// way never carry prompts, configuration, credentials or source text — see
// each fixture's own "provenance" field for what was kept and why.
func loadResearchDiagnosticsFixture(t *testing.T, path string) ResearchRunDiagnostics {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Meta    model.RunMeta          `json:"meta"`
		Results []model.ResearchResult `json:"results"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(struct {
		Candidate model.Candidate        `json:"candidate"`
		Results   []model.ResearchResult `json:"results"`
	}{model.Candidate{Ticker: "FIXTURE"}, fixture.Results})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "data", "research-46495854555245.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	return researchDiagnostics(store.RunSummary{Dir: dir}, &model.IdeasResult{Ideas: []model.TradeIdea{}}, nil, &fixture.Meta, nil)
}

// September 15's thesis run failed 9 of 12 companies to byte budgets before
// the plan's capacity fixes landed. This freezes that failure as a fixture so
// later tasks can be shown to change something real, rather than being graded
// against a synthetic case invented after the fix.
func TestSeptember15BaselineReproducesTheFailureCounts(t *testing.T) {
	d := loadResearchDiagnosticsFixture(t, "testdata/research-sep15.json")
	for _, c := range []struct {
		name string
		got  int
		want int
	}{
		{"logical calls", d.LogicalCalls, 59},
		{"dispatched attempts", d.DispatchedAttempts, 56},
		{"compaction calls", d.CompactionCalls, 13},
		{"schema repairs", d.SchemaRepairs, 2},
		{"zero-attempt input failures", d.InputCapacityFailures, 3},
		// "Failed company workflows" and "usable dossiers" are exactly
		// FailedResearch/CompletedResearch, already on ResearchRunDiagnostics:
		// 6 research_outcomes.contract=="failed" plus the 3 input-capacity
		// companies reconcile to the same 9, and 12-9=3 usable dossiers.
		{"failed company workflows", d.FailedResearch, 9},
		{"usable dossiers", d.CompletedResearch, 3},
		{"completed reviews", d.CompletedReviews, 3},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
