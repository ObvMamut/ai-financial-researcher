package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// thesisFixture wires investigate() against a local OpenAI-compatible endpoint
// and the same fixture provider the rest of the thesis tests use, so a test can
// script one response per role and read back what the pipeline made of it.
func thesisFixture(t *testing.T, reply func(prompt string, call int) string) (*thesisRunner, *quant.Pack, func()) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		calls++
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{
			map[string]any{"message": map[string]string{"content": currentFixtureReply(reply(req.Messages[0].Content, calls), req.Messages[0].Content)}},
		}})
	}))
	cfg := Config{ResearchMode: "thesis", CheapEngine: model.CLIApi, AgentsDir: "../../agents",
		API:     model.APIConfig{BaseURL: srv.URL, Model: "fixture", APIKey: "fixture"},
		DataDir: t.TempDir(), RunsDir: t.TempDir()}
	cfg.applyDefaults()
	reg, err := agents.Load(cfg.AgentsDir)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.New(cfg.RunsDir)
	if err != nil {
		t.Fatal(err)
	}
	pool := newPool(1, nil, nil, cfg.API, model.CLIApi, 0)
	ctx, cancel := context.WithCancel(context.Background())
	pool.start(ctx)
	p := quant.NewPack()
	p.ByTicker["AAA"] = quant.Metrics{LastClose: 100, SigmaDaily: .02}
	runner := &thesisRunner{cfg: cfg, ch: make(chan Event, 200), run: run, reg: reg, pool: pool,
		cheap: model.CLIApi, svc: marketdata.NewService(nil, researchFixtureProvider{}), sources: map[string][]string{}}
	return runner, p, func() { cancel(); pool.stop(); srv.Close() }
}

func fixtureEvidenceID(t *testing.T) string {
	t.Helper()
	pack := marketdata.NewDataPack("news")
	pack.ByTicker["AAA"], _ = (researchFixtureProvider{}).Fetch(context.Background(), "news", "AAA")
	return marketdata.EvidenceFromPack(pack, "AAA", time.Now())[0].ID
}

func fenced(v any) string { return "```json\n" + jsonText(v) + "\n```" }

// A payload whose arrays arrived as strings — the shape half the 2026-09-07
// shortlist failed on — buys exactly one repair call, and the repaired dossier
// is recorded as repaired rather than presented as clean research.
func TestMalformedPayloadBuysOneBoundedRepairAndIsRecordedAsRepaired(t *testing.T) {
	id := fixtureEvidenceID(t)
	claim := model.ResearchClaim{ID: "c1", Kind: "inference", Text: "Delivery update may reprice expectations", EvidenceIDs: []string{id}, Passages: []model.ClaimPassage{{EvidenceID: id, Quote: "Issuer raised guidance and expects its delivery update in two weeks.", IssuerRole: "issuer giving guidance"}}}
	repairs := 0
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		switch {
		case strings.Contains(prompt, "formatting correction only"):
			repairs++
			if strings.Contains(prompt, "# Independent thesis challenge") {
				return fenced(model.ThesisChallenge{Ticker: "AAA", Verdict: "supported", Reason: "Release supports the timing", Claims: []model.ResearchClaim{claim}, MaterialIssues: []string{}})
			}
			d := supportedResearch().Dossier
			d.Claims = []model.ResearchClaim{claim}
			return fenced(d)
		case strings.Contains(prompt, "# Independent thesis challenge"):
			// material_issues as a string: the wrong-type failure verbatim.
			return "```json\n{\"ticker\":\"AAA\",\"verdict\":\"supported\",\"reason\":\"ok\",\"claims\":[],\"material_issues\":\"none\"}\n```"
		default:
			// unresolved as an object rather than an array of strings.
			return "```json\n{\"ticker\":\"AAA\",\"status\":\"supported\",\"unresolved\":{\"note\":\"none\"}}\n```"
		}
	})
	defer done()

	got := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA", Name: "Company"}, nil, pack, nil)
	if repairs != 2 {
		t.Fatalf("repair calls = %d, want one per malformed payload", repairs)
	}
	if got.Outcome.Parsing != model.OutcomeRepaired {
		t.Errorf("parsing outcome = %q, want the repair recorded", got.Outcome.Parsing)
	}
	if got.Outcome.Transport != model.OutcomeOK {
		t.Errorf("a decodable-after-repair payload is not a transport failure: %q", got.Outcome.Transport)
	}
	repaired := 0
	for _, r := range got.Reports {
		if r.Payload == model.OutcomeRepaired {
			repaired++
		}
	}
	if repaired == 0 {
		t.Error("no call was marked repaired; metadata still cannot tell a response from usable research")
	}
}

// The repair is bounded at one. A model that cannot produce the schema costs one
// extra call, and its candidate ends as a research failure — never as a company
// the pipeline examined and rejected.
func TestUnrepairablePayloadIsAResearchFailureNotARejection(t *testing.T) {
	calls := 0
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		calls++
		return "```json\n{\"ticker\":\"AAA\",\"status\":\"maybe\",\"unresolved\":\"none\"}\n```"
	})
	defer done()

	got := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA", Name: "Company"}, nil, pack, nil)
	if calls != 2 {
		t.Fatalf("calls = %d, want the first attempt plus exactly one repair", calls)
	}
	if got.Outcome.Parsing != model.OutcomeFailed {
		t.Errorf("parsing outcome = %q, want failed", got.Outcome.Parsing)
	}
	if got.Dossier.Status == "rejected" {
		t.Error("an unreadable payload was recorded as a rejection of the company")
	}
	if !got.researchFailed() {
		t.Error("the run cannot tell that this candidate's research never completed")
	}

	res := &model.IdeasResult{Ideas: []model.TradeIdea{}, Decisions: []model.SelectionDecision{
		{Ticker: "AAA", Status: "watchlist", Reason: "Research did not produce a usable dossier; holding for a rerun."},
	}}
	cfg := Config{Mode: model.ModeIndependent}
	cfg.applyDefaults()
	finalizeThesis(res, []thesisResearch{got}, nil, verified{Quant: pack, Thesis: true}, cfg, time.Now())
	if len(res.Decisions) != 1 {
		t.Fatalf("decisions = %+v", res.Decisions)
	}
	d := res.Decisions[0]
	if d.Status != "watchlist" {
		t.Errorf("status = %q; a failed research call must not become a rejection", d.Status)
	}
	if d.Blocked != model.BlockedResearchFailure {
		t.Errorf("blocked = %q, want %q", d.Blocked, model.BlockedResearchFailure)
	}
	if !strings.Contains(d.Reason, "holding for a rerun") {
		t.Errorf("the selector's own reason was overwritten: %q", d.Reason)
	}
}

// TestZeroAttemptCapacityRefusalDoesNotClaimTransportOrParsingFailure pins
// Task 14's defect: an input-capacity refusal recorded Transport and Parsing
// as "failed" even though neither the model call nor any decoding ever
// happened — preparePrompt refused the assembled prompt on its own
// len(prompt) > InputBytes check before any subprocess ran or HTTP request
// was sent. Confirmed against real evidence: runs/2026-09-15T17-00-30/
// metadata.json records exactly this shape for SNOW, OKTA and ORCL (attempts
// 0, failure_kind "input_capacity", yet transport/parsing both "failed").
func TestZeroAttemptCapacityRefusalDoesNotClaimTransportOrParsingFailure(t *testing.T) {
	calls := 0
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		calls++
		d := supportedResearch().Dossier
		return fenced(d)
	})
	defer done()
	// A budget the persona wrapper alone already exceeds (agents/thesis-
	// researcher.md is ~10KB on its own): nothing to trim, every mandatory
	// section implicated, and the very first round call is refused before
	// any request reaches the fixture server.
	runner.cfg.Research.Budgets.Researcher = model.RoleBudget{InputBytes: 4096, ResponseBytes: 1024}

	got := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA", Name: "Company"}, nil, pack, nil)

	if calls != 0 {
		t.Fatalf("calls = %d, want 0 — an oversized prompt must never be dispatched", calls)
	}
	if got.Outcome.Transport != model.OutcomeNotAttempted {
		t.Errorf("Transport = %q, want %q — no call was ever dispatched", got.Outcome.Transport, model.OutcomeNotAttempted)
	}
	if got.Outcome.Parsing != model.OutcomeNotAttempted {
		t.Errorf("Parsing = %q, want %q — no payload was ever received to parse", got.Outcome.Parsing, model.OutcomeNotAttempted)
	}
	if len(got.Reports) == 0 {
		t.Fatal("no domain report recorded for the refused call")
	}
	r := got.Reports[0]
	if r.Attempts != 0 {
		t.Errorf("Attempts = %d, want 0", r.Attempts)
	}
	if r.FailureKind != "input_capacity" {
		t.Errorf("FailureKind = %q, want input_capacity", r.FailureKind)
	}
	if len(r.Omitted) == 0 {
		t.Error("Omitted is empty; an infeasible prompt must name which mandatory requirements exceeded capacity")
	}
	for _, want := range []string{"identity", "evidence"} {
		found := false
		for _, o := range r.Omitted {
			if o == want {
				found = true
			}
		}
		if !found {
			t.Errorf("Omitted = %v, want it to include mandatory section %q", r.Omitted, want)
		}
	}
}

// TestResearchFailureNoteNamesACapacityRefusalDistinctlyFromAModelCallFailure
// keeps the human-facing decision reason (SelectionDecision.Reason/
// ReviewReason, rendered by both cmd/cfr/headless.go and internal/tui) as
// accurate as the machine-readable fields: a company blocked by a capacity
// refusal must not read as "a model call failed", which claims a dispatch
// that never happened.
func TestResearchFailureNoteNamesACapacityRefusalDistinctlyFromAModelCallFailure(t *testing.T) {
	r := thesisResearch{Outcome: model.ResearchOutcome{Transport: model.OutcomeNotAttempted, Parsing: model.OutcomeNotAttempted, Review: model.ReviewUnavailable}}
	note := researchFailureNote(r)
	if strings.Contains(note, "a model call failed") {
		t.Errorf("note = %q; a capacity refusal must not be described as a model call failing", note)
	}
	if !strings.Contains(note, "input capacity") {
		t.Errorf("note = %q, want it to name the capacity refusal", note)
	}
}

// TestOneFailureIsCountedOnceNotAsBothCompanyNoteAndDomainError pins Task
// 14's aggregation fix: a call-outcome failure (round, revision or
// challenge) used to be recorded twice in a run's DataErrors — once as the
// raw, unattributed error text carried in a company's own Errors list (its
// "company note"), and again, differently worded, as the ticker-tagged
// "domain error" every failed/invalid DomainStatus contributes. Confirmed
// against real evidence: runs/2026-09-15T17-00-30/metadata.json's
// data_errors carries both
// "input capacity exceeded for thesis-researcher: 103077 > 98304 bytes;
// required context cannot be omitted" (unattributed) and
// "research-4f4b5441-revision: input capacity exceeded for
// thesis-researcher: 103077 > 98304 bytes; required context cannot be
// omitted" (OKTA's revision, tagged) for the identical OKTA revision refusal.
func TestOneFailureIsCountedOnceNotAsBothCompanyNoteAndDomainError(t *testing.T) {
	const raw = "input capacity exceeded for thesis-researcher: 103077 > 98304 bytes; required context cannot be omitted"
	r := thesisResearch{
		Candidate: model.Candidate{Ticker: "OKTA"},
		// A document-fetch failure has no DomainStatus counterpart and must
		// still reach the run-level aggregate.
		Errors: []string{raw, "document https://issuer.example/x: fetch timed out"},
		Reports: []model.DomainStatus{
			{Domain: "research-4f4b5441-revision", Status: model.StatusFailed, FailureKind: "input_capacity", Attempts: 0, Payload: "invalid", Err: raw},
		},
	}

	got := researchOnlyErrors(r)

	count := 0
	for _, e := range got {
		if e == raw {
			count++
		}
	}
	if count != 0 {
		t.Fatalf("the call-outcome failure survived as an orphaned company error: %v (it must be left for the domain-tagged pass alone to add)", got)
	}
	found := false
	for _, e := range got {
		if strings.Contains(e, "fetch timed out") {
			found = true
		}
	}
	if !found {
		t.Errorf("an unrelated, uncovered error was dropped: %v", got)
	}
	// The detail stays reachable in the artifact either way.
	if r.Reports[0].Err != raw {
		t.Fatal("detailed evidence missing from the domain report")
	}
}

// TestUsableDossierFollowedByUnavailableReviewStaysVisibleAsResearchCompleted
// pins Task 14's stage-progress fix. Before it, a challenge call's own
// transport/parsing outcome was folded into the same Outcome.Transport/
// .Parsing fields that describe the dossier itself, so a challenge that could
// not be attempted (capacity-refused, same as any other transport problem)
// overwrote a genuinely completed dossier's "ok" with "failed" — resetting
// completed research to an apparent zero. ORCL's real September 15 record
// shows exactly this: its revision had already succeeded (contract
// "compacted") when challenge-final was a zero-attempt capacity refusal, yet
// the outcome read transport/parsing "failed" for the whole company.
//
// The fix: Outcome.Transport/.Parsing describe only the dossier-producing
// calls (research rounds and revision); a challenge's own outcome is fully
// carried by Outcome.Review, independently. The company must still fail
// overall (research_failed() and the run's Failed count), and still stay
// blocked for selection — only the completed dossier's own record must not
// be corrupted by a review that could not be attempted afterward.
func TestUsableDossierFollowedByUnavailableReviewStaysVisibleAsResearchCompleted(t *testing.T) {
	id := fixtureEvidenceID(t)
	claim := model.ResearchClaim{ID: "c1", Kind: "inference", Text: "Delivery update may reprice expectations", EvidenceIDs: []string{id}, Passages: []model.ClaimPassage{{EvidenceID: id, Quote: "Issuer raised guidance and expects its delivery update in two weeks.", IssuerRole: "issuer giving guidance"}}}
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		// Only the research round ever reaches the model: the challenger's
		// budget below refuses its call before any request is dispatched.
		d := supportedResearch().Dossier
		d.Claims = []model.ResearchClaim{claim}
		return fenced(d)
	})
	defer done()
	// The researcher keeps its default (generous) budget — the round must
	// genuinely complete — while the challenger's is too small for even its
	// own persona text, so the independent challenge is refused outright.
	runner.cfg.Research.Budgets.Challenger = model.RoleBudget{InputBytes: 4096, ResponseBytes: 1024}

	got := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA", Name: "Company"}, nil, pack, nil)

	if got.Outcome.Transport != model.OutcomeOK {
		t.Fatalf("Transport = %q, want ok — the research round genuinely completed", got.Outcome.Transport)
	}
	if got.Outcome.Parsing != model.OutcomeOK {
		t.Fatalf("Parsing = %q, want ok", got.Outcome.Parsing)
	}
	if got.Outcome.Review != model.ReviewUnavailable {
		t.Fatalf("Review = %q, want unavailable — the challenge was capacity-refused", got.Outcome.Review)
	}
	if got.Dossier.Status != "watchlist" {
		t.Errorf("Dossier.Status = %q, want watchlist — still blocked for selection", got.Dossier.Status)
	}
	if !got.researchFailed() {
		t.Error("researchFailed() must stay true: the review never completed, so this candidate is still not a plan")
	}

	summary := model.SummarizeResearch([]model.ResearchOutcome{got.Outcome}, nil)
	if summary.Researched != 1 {
		t.Errorf("Researched = %d, want 1 — a completed dossier must stay visible even though its review could not be attempted", summary.Researched)
	}
	if summary.Failed != 1 {
		t.Errorf("Failed = %d, want 1 — the company still failed overall (no review completed)", summary.Failed)
	}
}

// BSX: Claude read the research failure correctly and said watchlist; Go
// overwrote it with "rejected" and swapped in the challenger's words. Both
// halves are preserved now — the decision's reason stays the selector's, and
// the review's wording lives in its own field.
func TestSelectorReasoningSurvivesTheIndependentReview(t *testing.T) {
	r := supportedResearch()
	r.Outcome = model.ResearchOutcome{Ticker: "AAA", Transport: model.OutcomeOK, Parsing: model.OutcomeOK, Evidence: model.EvidenceDocuments, Review: "reject"}
	r.Dossier.Status = "rejected"
	r.Challenge = model.ThesisChallenge{Ticker: "AAA", Verdict: "reject", Reason: "September 24 falls outside the holding window"}

	res := &model.IdeasResult{Ideas: []model.TradeIdea{}, Decisions: []model.SelectionDecision{
		{Ticker: "AAA", Status: "rejected", Reason: "September 24 is 13 weekdays out and inside the window, but the mechanism is already priced."},
	}}
	cfg := Config{Mode: model.ModeIndependent}
	cfg.applyDefaults()
	finalizeThesis(res, []thesisResearch{r}, nil, verified{Quant: quant.NewPack(), Thesis: true}, cfg, time.Now())
	d := res.Decisions[0]
	if !strings.Contains(d.Reason, "already priced") {
		t.Errorf("the selector's reason was replaced by the challenger's: %q", d.Reason)
	}
	if !strings.Contains(d.ReviewReason, "outside the holding window") {
		t.Errorf("the review's own wording was lost: %q", d.ReviewReason)
	}
	if d.Blocked != model.BlockedReviewReject {
		t.Errorf("blocked = %q, want a substantive review rejection", d.Blocked)
	}
}

func TestSchemaChecksNameEveryProblemAtOnce(t *testing.T) {
	d := &model.CandidateDossier{
		Ticker:          "AAA",
		Status:          "maybe",
		EvidenceQuality: "great",
		Claims:          []model.ResearchClaim{{ID: "", Kind: "guess"}},
		// A bad request must not cost the dossier: the retrieval protocol
		// answers it, and the substance of the research survives.
		Requests: []model.ResearchRequest{{Kind: "web_search", Question: "q"}},
	}
	problems := strings.Join(dossierSchema(d), " | ")
	for _, want := range []string{"status", "evidence_quality", `no "id"`, "observation"} {
		if !strings.Contains(problems, want) {
			t.Errorf("schema check missed %q; got %s", want, problems)
		}
	}
	if strings.Contains(problems, "web_search") {
		t.Errorf("a malformed request invalidated the whole dossier: %s", problems)
	}
	c := &model.ThesisChallenge{Verdict: "maybe"}
	problems = strings.Join(challengeSchema(c), " | ")
	if !strings.Contains(problems, "ticker") || !strings.Contains(problems, "verdict") {
		t.Errorf("challenge schema check missed a required field: %s", problems)
	}
	if len(challengeSchema(&model.ThesisChallenge{Ticker: "AAA", Verdict: "supported", Reason: "Source supports the mechanism"})) != 0 {
		t.Error("an omitted optional array was treated as a schema error")
	}
}

// Every request is answered, and the answer says what the model can do about it.
// Twenty of the 2026-09-07 run's requests named operations that do not exist and
// nine searched for text that was not in the document; none of those answers
// reached the model, so it asked again.
func TestEveryResearchRequestIsAnsweredAndRepeatsAreRefused(t *testing.T) {
	round := 0
	var seenAnswers string
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		if strings.Contains(prompt, "# Independent thesis challenge") {
			return fenced(model.ThesisChallenge{Ticker: "AAA", Verdict: "reject", Reason: "no", MaterialIssues: []string{}})
		}
		round++
		d := model.CandidateDossier{Ticker: "AAA", Status: "watchlist", EvidenceQuality: "mixed", Hypothesis: "h", Unresolved: []string{}}
		switch round {
		case 1:
			d.Requests = []model.ResearchRequest{
				{Kind: "web_search", Question: "search the web"},
				{Question: "no kind at all"},
				{Kind: "news", Question: "fetch newer news"},
			}
		case 2:
			// Answers are checked after the second request batch.
			// Ask for exactly the same things again.
			d.Requests = []model.ResearchRequest{
				{Kind: "web_search", Question: "search the web"},
				{Kind: "passage", EvidenceID: "ev-does-not-exist", Query: "guidance", Question: "the guidance line"},
				{Kind: "document", URL: "https://not-in-evidence.example/x", Question: "read this"},
			}
		case 3:
			seenAnswers = prompt
		}
		return fenced(d)
	})
	defer done()

	got := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA", Name: "Company"}, nil, pack, nil)

	byOutcome := map[string]int{}
	for _, r := range got.Results {
		byOutcome[r.Outcome]++
		if r.Outcome == "" {
			t.Errorf("request %+v went unanswered", r.Request)
		}
		if r.Detail == "" {
			t.Errorf("request %+v was answered with no reason", r.Request)
		}
	}
	if byOutcome[model.RequestUnsupported] < 2 {
		t.Errorf("an unknown kind and an omitted kind must both be answered unsupported: %v", byOutcome)
	}
	if byOutcome[model.RequestAlreadyDone] < 1 {
		t.Errorf("the repeated requests must be answered as already attempted: %v", byOutcome)
	}
	if byOutcome[model.RequestUnavailable] < 1 {
		t.Errorf("a passage search that found nothing must be answered unavailable: %v", byOutcome)
	}
	if !strings.Contains(seenAnswers, "Answers to your research requests") {
		t.Fatal("the answers never reached the next round's prompt")
	}
	// Task 13's request-ledger compaction (compactResults, thesis_compaction.go)
	// reduces the PROMPT's view of an unsupported/unavailable/already-attempted
	// answer to its request's identity plus the outcome word alone — the full
	// explanatory sentence was worth saying once, in the round it happened, and
	// worth nothing repeated into every later round unchanged. The durable
	// ledger (got.Results, asserted above) still carries every Detail sentence
	// in full; only the compacted prompt view changes.
	for _, want := range []string{
		`"kind":"web_search"`,
		`"kind":"news"`,
		`"kind":"document","url":"https://not-in-evidence.example/x"`,
		`"outcome":"unsupported"`,
		`"outcome":"already attempted"`,
	} {
		if !strings.Contains(seenAnswers, want) {
			t.Errorf("the model was not told %q", want)
		}
	}
	for _, gone := range []string{"No such operation: web_search", "performs no additional retrieval", "not in your evidence."} {
		if strings.Contains(seenAnswers, gone) {
			t.Errorf("prompt still carries the full explanatory sentence %q; a repeated answer should compact to identity + outcome", gone)
		}
	}
}

// A budget-exhausted request is still answered. Stopping the batch left the
// model unable to tell which of its questions went unanswered.
func TestExhaustedDocumentBudgetStillAnswersEveryRequest(t *testing.T) {
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		if strings.Contains(prompt, "# Independent thesis challenge") {
			return fenced(model.ThesisChallenge{Ticker: "AAA", Verdict: "reject", Reason: "no", MaterialIssues: []string{}})
		}
		return fenced(model.CandidateDossier{Ticker: "AAA", Status: "watchlist", EvidenceQuality: "mixed", Hypothesis: "h", Unresolved: []string{},
			Requests: []model.ResearchRequest{
				{Kind: "filings", Question: "first"},
				{Kind: "filings", Question: "second"},
				{Kind: "news", Question: "third"},
			}})
	})
	runner.cfg.Research.Documents = 1
	defer done()

	got := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA", Name: "Company"}, nil, pack, nil)
	answered := 0
	for _, r := range got.Results {
		if r.Outcome != "" {
			answered++
		}
	}
	if answered < 2 {
		t.Fatalf("requests after the budget ran out went unanswered: %+v", got.Results)
	}
}

// Fixtures that predate referenced reviews are rendered into the current wire
// contract. Invalid types/enums remain invalid, so repair regressions still run.
func currentFixtureReply(raw, prompt string) string {
	var value map[string]any
	if decodeResearch(raw, &value) != nil {
		return raw
	}
	if _, ok := value["ticker"]; !ok {
		return raw
	}
	if _, ok := value["contract_version"]; ok {
		return raw
	}
	value["contract_version"] = 2
	if _, ok := value["status"]; ok {
		ids := []string{}
		if claims, ok := value["claims"].([]any); ok {
			for _, c := range claims {
				if claim, ok := c.(map[string]any); ok {
					if id, ok := claim["id"].(string); ok {
						ids = append(ids, id)
					}
				}
			}
		}
		value["expectations_claim_ids"] = ids
		value["priced_in_claim_ids"] = ids
	}
	if _, ok := value["verdict"]; ok {
		hash := ""
		for _, pattern := range []string{`Dossier hash: ([a-f0-9]{64})`, `"dossier_hash"\s*:\s*"([a-f0-9]{64})"`} {
			if m := regexp.MustCompile(pattern).FindStringSubmatch(prompt); len(m) > 1 {
				hash = m[1]
				break
			}
		}
		value["dossier_hash"] = hash
		reviews := []model.ClaimReview{}
		if claims, ok := value["claims"].([]any); ok {
			for _, c := range claims {
				if claim, ok := c.(map[string]any); ok {
					if id, ok := claim["id"].(string); ok {
						reviews = append(reviews, model.ClaimReview{ClaimID: id, Assessment: "supported", Attribution: "confirmed", Reason: "Fixture source supports attribution"})
					}
				}
			}
		}
		value["claim_reviews"] = reviews
		value["claims"] = []any{}
	}
	return fenced(value)
}
