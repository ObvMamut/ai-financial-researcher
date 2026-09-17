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
