package orchestrator

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestCapturedIssuerReleaseReachesResearchPrompt(t *testing.T) {
	b, err := os.ReadFile("../marketdata/testdata/regional-capture-2026-09-21.json")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Documents map[string][]model.EvidenceDocument `json:"documents"`
	}
	if err = json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	runner, _, done := thesisFixture(t, func(string, int) string { t.Fatal("no model dispatch"); return "" })
	defer done()
	for _, ticker := range []string{"ASML.AS", "NOKIA.HE", "2330.TW", "9988.HK"} {
		docs := c.Documents[ticker]
		visible := promptDocuments(docs, 1600)
		prompt, profile, err := runner.preparePrompt("thesis-researcher", "captured-"+ticker, []promptSection{{Name: "evidence", Mandatory: true, Body: jsonText(visible)}}, 8192)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, d := range visible {
			if marketdata.SubstantiveResearchDocument(d) && len(d.SelectedSpans) > 0 {
				found = true
				if !strings.Contains(prompt, d.ID) || !slices.Contains(profile.VisibleEvidence[ticker], d.ID) {
					t.Fatal("source passage missing from actual prompt profile")
				}
				for _, original := range docs {
					if original.ID == d.ID && !strings.Contains(original.Text, d.Text) && !d.OmittedText {
						t.Fatal("source text changed without omission marker")
					}
				}
			}
		}
		if !found {
			t.Fatalf("%s: navigation consumed the entire research evidence budget", ticker)
		}
	}
}
