package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// chiefAgreementFixture builds the one fixture run
// TestJSONTextAndTUIAgreeOnChiefAndResearchCounts uses to check that the text
// renderer (printIdeasText, below), `cfr run --json`'s stdout encoding
// (writeIdeasJSON, headless.go) and the TUI results view (internal/tui/
// results_test.go's own copy of this test) all report the same facts about
// which Chief engine answered and how research went. The two test files
// build this exact fixture independently (cmd/cfr is package main and cannot
// be imported by internal/tui, nor vice versa) rather than sharing a helper
// across that boundary — keep them in sync if you change one.
//
// ideas.ChiefEngine/ChiefAccepted mirror meta.ChiefEngine/ChiefAccepted
// exactly as orchestrator.go and thesis.go set them in production (both call
// sites write the ideas-result copy from the same variables that build the
// RunMeta one) — this fixture is not inventing independent facts, it is
// pinning that the two copies must agree.
func chiefAgreementFixture() (*model.RunMeta, *model.IdeasResult) {
	meta := &model.RunMeta{
		ChiefEngine:    "claude",
		ChiefModel:     "opus",
		ChiefAttempted: "claude,api",
		ChiefAccepted:  "api",
		Domains: []model.DomainStatus{
			{Domain: "chief-analyst", Status: model.StatusFailed, Err: "boom"},
			{Domain: "chief-analyst-fallback", Status: model.StatusDone},
		},
		ResearchOutcomes: []model.ResearchOutcome{
			{Transport: model.OutcomeOK, Parsing: model.OutcomeOK, Review: "supported"},
			{Transport: model.OutcomeOK, Parsing: model.OutcomeFailed, Review: model.ReviewUnavailable},
		},
	}
	ideas := &model.IdeasResult{
		ResearchMode:  "thesis",
		Mode:          "independent",
		GeneratedAt:   "2026-09-17T00:00:00Z",
		Ideas:         []model.TradeIdea{},
		Decisions:     []model.SelectionDecision{{Ticker: "BBB", Status: "watchlist", Reason: "wait for confirmation"}},
		ChiefEngine:   meta.ChiefEngine,
		ChiefAccepted: meta.ChiefAccepted,
	}
	return meta, ideas
}

// TestJSONTextAndTUIAgreeOnChiefAndResearchCounts asserts three surfaces
// report the same facts from one fixture run: `cfr run --json`'s actual
// stdout wire format (via writeIdeasJSON — the same function runHeadless
// calls, not a hand-rolled json.Marshal), headless's text renderer, and the
// persisted metadata.json/ideas.json artifacts (simulated by a real
// marshal/unmarshal round trip). The facts checked: the primary engine (and
// what was attempted/accepted), recovered failures, and the research/decision
// counts.
//
// R12 fixed a defect in the original criterion: `cfr run --json` encodes only
// *model.IdeasResult on stdout, which carried no engine field at all, making
// "JSON, text and TUI agree on primary engine" unsatisfiable by construction.
// The fix was IdeasResult.ChiefEngine/ChiefAccepted (types.go) — a compact,
// Go-computed subset of RunMeta's full provenance quartet, following the
// precedent ResearchSummary already set on the same struct — not a weaker
// test.
func TestJSONTextAndTUIAgreeOnChiefAndResearchCounts(t *testing.T) {
	meta, ideas := chiefAgreementFixture()

	// The persisted-artifact view: metadata.json/ideas.json, simulated by a
	// real round trip so a typo in a json struct tag would be caught here.
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrippedMeta model.RunMeta
	if err := json.Unmarshal(metaJSON, &roundTrippedMeta); err != nil {
		t.Fatal(err)
	}
	ideasJSON, err := json.Marshal(ideas)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrippedIdeas model.IdeasResult
	if err := json.Unmarshal(ideasJSON, &roundTrippedIdeas); err != nil {
		t.Fatal(err)
	}

	jsonLine := model.ChiefProvenanceLine(&roundTrippedMeta)
	if jsonLine == "" {
		t.Fatal("fixture produced an empty Chief provenance line")
	}
	jsonSummary := model.SummarizeResearch(roundTrippedMeta.ResearchOutcomes, roundTrippedIdeas.Decisions)
	if jsonSummary == nil {
		t.Fatal("fixture produced no research summary")
	}
	jsonIssues := model.ResearchRunIssues(&roundTrippedMeta)
	if len(jsonIssues) == 0 {
		t.Fatal("fixture produced no recovered-failure issues")
	}

	// The `cfr run --json` STDOUT view: the actual encoder runHeadless calls,
	// not a bare json.Marshal, so this is the wire format an operator piping
	// `cfr run --json` actually receives.
	var stdout bytes.Buffer
	if err := writeIdeasJSON(&stdout, ideas); err != nil {
		t.Fatal(err)
	}
	var stdoutIdeas model.IdeasResult
	if err := json.Unmarshal(stdout.Bytes(), &stdoutIdeas); err != nil {
		t.Fatal(err)
	}
	if stdoutIdeas.ChiefEngine == "" {
		t.Fatalf("cfr run --json stdout carries no chief_engine at all:\n%s", stdout.String())
	}
	if stdoutIdeas.ChiefEngine != roundTrippedMeta.ChiefEngine {
		t.Errorf("stdout ChiefEngine = %q, want %q (metadata.json's configured primary)", stdoutIdeas.ChiefEngine, roundTrippedMeta.ChiefEngine)
	}
	if stdoutIdeas.ChiefAccepted != roundTrippedMeta.ChiefAccepted {
		t.Errorf("stdout ChiefAccepted = %q, want %q (metadata.json's accepted engine)", stdoutIdeas.ChiefAccepted, roundTrippedMeta.ChiefAccepted)
	}

	// The text view: capture printIdeasText's stdout.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	printIdeasText(ideas, meta)
	w.Close()
	os.Stdout = saved
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)

	if !strings.Contains(text, jsonLine) {
		t.Errorf("text renderer missing Chief provenance line %q:\n%s", jsonLine, text)
	}
	// The text line must agree with what --json's stdout actually carries,
	// not just with the in-memory meta the test happened to build.
	if !strings.Contains(text, stdoutIdeas.ChiefEngine) {
		t.Errorf("text renderer's engine does not match stdout ChiefEngine %q:\n%s", stdoutIdeas.ChiefEngine, text)
	}
	if !strings.Contains(text, jsonSummary.String()) {
		t.Errorf("text renderer missing research summary %q:\n%s", jsonSummary.String(), text)
	}
	for _, issue := range jsonIssues {
		if !strings.Contains(text, issue) {
			t.Errorf("text renderer missing recovered-failure issue %q:\n%s", issue, text)
		}
	}
	for _, d := range roundTrippedIdeas.Decisions {
		if !strings.Contains(text, d.Ticker) || !strings.Contains(text, d.Reason) {
			t.Errorf("text renderer missing decision %+v:\n%s", d, text)
		}
	}
}
