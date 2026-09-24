package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestClaimPassageSurvivesLongNavigationAndTextBudget(t *testing.T) {
	quote := "Sony filed a lawsuit against Anthropic, alleging unauthorized use of its recordings."
	doc := model.EvidenceDocument{ID: "sony", Ticker: "SONY", Kind: "document", Source: "fixture issuer", ReportingPeriod: "2026", Text: strings.Repeat("企業 navigation text. ", 1000) + quote + strings.Repeat(" tail", 1000)}
	claim := model.ResearchClaim{ID: "c1", Kind: "observation", Text: "Sony brought the claim", EvidenceIDs: []string{"sony"}, Passages: []model.ClaimPassage{{EvidenceID: "sony", Quote: quote, IssuerRole: "plaintiff"}}}
	docs := []model.EvidenceDocument{doc}
	for i := 0; i < 40; i++ {
		docs = append(docs, model.EvidenceDocument{ID: strings.Repeat("x", i+1), Text: strings.Repeat("unrelated headline ", 100), Kind: "headline"})
	}
	out := promptDocuments(docs, 900, claim)
	if !strings.Contains(out[0].Text, quote) || !out[0].Truncated || out[0].ReportingPeriod != "2026" {
		t.Fatalf("lost quoted context: %+v", out[0])
	}
	used := 0
	for _, d := range out {
		if !utf8.ValidString(d.Text) {
			t.Fatal("split UTF-8")
		}
		used += len([]rune(d.Text))
	}
	if used > 900 {
		t.Fatalf("budget exceeded: %d", used)
	}
}

func TestSonyAttributionRequiresQuotedEvidenceAndConsistentReview(t *testing.T) {
	// Synthetic, sanitized regression for the issuer-role failure in the plan.
	quote := "Sony filed a lawsuit against Anthropic, alleging unauthorized use of its recordings."
	docs := []model.EvidenceDocument{{ID: "sony", Ticker: "SONY", Text: quote, Kind: "document"}}
	c := model.ResearchClaim{ID: "c1", Kind: "observation", Text: "Sony is a plaintiff", EvidenceIDs: []string{"sony"}, Passages: []model.ClaimPassage{{EvidenceID: "sony", Quote: quote, IssuerRole: "plaintiff"}}}
	if errors := validateClaimPassages([]model.ResearchClaim{c}, docs); len(errors) != 0 {
		t.Fatal(errors)
	}
	dossier := model.CandidateDossier{Claims: []model.ResearchClaim{c}}
	review := model.ThesisChallenge{Verdict: "supported", Claims: []model.ResearchClaim{c}}
	review.Claims[0].Passages = append([]model.ClaimPassage(nil), c.Passages...)
	review.Claims[0].Passages[0].IssuerRole = "defendant"
	if errors := validateReviewConsistency(dossier, review); len(errors) == 0 {
		t.Fatal("inconsistent plaintiff/defendant attribution accepted")
	}
	c.Passages[0].Quote = "Anthropic filed a lawsuit against Sony for unauthorized use."
	if errors := validateClaimPassages([]model.ResearchClaim{c}, docs); len(errors) == 0 {
		t.Fatal("real ID with fabricated passage accepted")
	}
}

func TestBothDirectionsMustBeConsideredBeforeSupport(t *testing.T) {
	r := supportedResearch()
	r.Dossier.ShortCase = ""
	validateDossier(&r)
	if r.Dossier.Status == "supported" {
		t.Fatal("unexamined opposite case accepted")
	}
	r = supportedResearch()
	r.Dossier.PreferredDirection = "NONE"
	validateDossier(&r)
	if r.Dossier.Status == "supported" {
		t.Fatal("no-trade conclusion promoted to supported direction")
	}
}

func TestFullArticleReuseAndSavedReviewEvidence(t *testing.T) {
	id := fixtureEvidenceID(t)
	c := model.ResearchClaim{ID: "c1", Kind: "inference", Text: "Delivery update may change expectations", EvidenceIDs: []string{id}, Passages: []model.ClaimPassage{{EvidenceID: id, Quote: "Issuer raised guidance and expects its delivery update in two weeks.", IssuerRole: "issuer"}}}
	runner, qp, close := thesisFixture(t, func(prompt string, call int) string {
		if strings.Contains(prompt, "# Independent thesis challenge") {
			return fenced(model.ThesisChallenge{Ticker: "AAA", Verdict: "supported", Claims: []model.ResearchClaim{c}, Reason: "Source supports the mechanism"})
		}
		d := supportedResearch().Dossier
		d.Claims = []model.ResearchClaim{c}
		if call == 1 {
			d.Requests = []model.ResearchRequest{{Kind: "document", URL: "https://issuer.example/release", Question: "read full source"}}
		}
		return fenced(d)
	})
	defer close()
	runner.cfg.Research.Documents = 1
	out := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA"}, nil, qp, nil)
	if out.Dossier.Status != "supported" {
		t.Fatalf("reuse failed: %+v", out)
	}
	found := false
	for _, r := range out.Results {
		if r.Request.URL == "https://issuer.example/release" && r.Outcome == model.RequestAlreadyDone {
			found = true
		}
	}
	if !found {
		t.Fatal("full body was fetched again")
	}
	files, err := filepath.Glob(filepath.Join(runner.run.Dir, "data", "input-*-challenge.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("review evidence missing: %v %v", files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil || !strings.Contains(string(b), "delivery update") || !strings.Contains(string(b), "Computed temporal facts") {
		t.Fatalf("review input not preserved: %v", err)
	}
}

// A context grant smaller than one full ±180-character window must still buy
// context. The board allocator treats source context as divisible; a window
// that could only be taken whole made every sliver grant — ~300 characters
// per company on a twelve-name board — buy nothing, and left a third of the
// optional pool unconverted.
func TestPromptDocumentsNarrowsContextToASmallGrant(t *testing.T) {
	quote := "revenue rose 12 percent"
	text := strings.Repeat("a", 1000) + quote + strings.Repeat("b", 1000)
	doc := model.EvidenceDocument{ID: "ev-1", Kind: "document", Text: text}
	claim := model.ResearchClaim{ID: "c1", Kind: "observation", EvidenceIDs: []string{"ev-1"},
		Passages: []model.ClaimPassage{{EvidenceID: "ev-1", Quote: quote, IssuerRole: "issuer"}}}

	required := requiredChars([]model.EvidenceDocument{doc}, []model.ResearchClaim{claim})
	bare := promptDocuments([]model.EvidenceDocument{doc}, required, claim)[0].Text
	grant := 120 // well under the 360 characters a full window costs
	padded := promptDocuments([]model.EvidenceDocument{doc}, required+grant, claim)[0].Text

	if !strings.Contains(padded, quote) {
		t.Fatalf("the required quotation was lost: %q", padded)
	}
	gained := len([]rune(padded)) - len([]rune(bare))
	if gained <= 0 || gained > grant {
		t.Fatalf("a %d-character grant bought %d characters of context; want some, and no more than the grant", grant, gained)
	}
}
