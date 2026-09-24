package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// gateDossier is a two-claim contract-v2 dossier: c1 is core (it carries the
// expectations and priced-in reasoning), c2 is a side observation.
func gateDossier() model.CandidateDossier {
	d := supportedResearch().Dossier
	d.ContractVersion = 2
	c2 := d.Claims[0]
	c2.ID, c2.Text = "c2", "Delivery timing is one of several demand signals"
	d.Claims = append(d.Claims, c2)
	d.ExpectationsClaimIDs, d.PricedInClaimIDs = []string{"c1"}, []string{"c1"}
	return d
}

// gateReview supports c1 outright and leaves c2 unresolved, with the given
// verdict and issues, bound to d's current hash, consistency problems
// appended exactly as the pipeline appends them.
func gateReview(d model.CandidateDossier, verdict string, issues ...model.MaterialIssue) model.ThesisChallenge {
	c := model.ThesisChallenge{ContractVersion: 2, Ticker: "AAA", DossierHash: dossierHash(d), Verdict: verdict, Reason: "review",
		ClaimReviews: []model.ClaimReview{
			{ClaimID: "c1", Assessment: "supported", Attribution: "confirmed", Reason: "issuer release quoted"},
			{ClaimID: "c2", Assessment: "unresolved", Attribution: "unresolved", Reason: "no second source for timing"},
		},
		MaterialIssues: issues}
	for _, p := range validateReviewConsistency(d, c) {
		c.MaterialIssues = appendIssue(c.MaterialIssues, p)
	}
	return c
}

// The objections 65 of 67 final September 23 challenges carried can never be
// answered from free data. Categorised as disclosed risks, with the core claim
// supported and nothing disputed, they no longer keep a thesis off supported —
// even under a `revise` verdict — and they travel as risks instead.
func TestDisclosedRisksWithSupportedCoreClaimsPassTheGate(t *testing.T) {
	d := gateDossier()
	for _, verdict := range []string{"supported", "revise"} {
		c := gateReview(d, verdict,
			issue(model.IssuePricedInUnprovable, "no consensus data shows what the guidance raise priced in"),
			issue(model.IssueFuturePrices, "no post-update prices exist yet"))
		if blockers := reviewBlockers(c); len(blockers) != 0 {
			t.Fatalf("%s: disclosed risks blocked support: %v", verdict, blockers)
		}
		risks := disclosedRisks(d, c, nil)
		for _, want := range []string{"priced_in_unprovable: no consensus data", "future_prices: no post-update prices", "claim c2 unresolved"} {
			found := false
			for _, r := range risks {
				if strings.HasPrefix(r, want) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: risk %q not disclosed in %v", verdict, want, risks)
			}
		}
	}
}

func TestBlockingIssuesAndCoreGapsStillBlock(t *testing.T) {
	d := gateDossier()
	cases := map[string]func(*model.ThesisChallenge){
		"blocking category": func(c *model.ThesisChallenge) {
			c.MaterialIssues = append(c.MaterialIssues, issue(model.IssueAttribution, "the quote describes a supplier, not the issuer"))
		},
		"core claim unresolved": func(c *model.ThesisChallenge) {
			c.ClaimReviews[0].Assessment = "unresolved"
		},
		"core attribution unresolved": func(c *model.ThesisChallenge) {
			c.ClaimReviews[0].Attribution = "unresolved"
		},
		"non-core claim disputed": func(c *model.ThesisChallenge) {
			c.ClaimReviews[1].Assessment = "disputed"
		},
		"pending request": func(c *model.ThesisChallenge) {
			c.Requests = []model.ResearchRequest{{Kind: "filings", Question: "latest 10-Q"}}
		},
		"reject": func(c *model.ThesisChallenge) { c.Verdict = "reject" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := gateReview(d, "supported")
			mutate(&c)
			c.MaterialIssues = append(c.MaterialIssues, validateReviewConsistency(d, c)...)
			if len(reviewBlockers(c)) == 0 {
				t.Fatal("blocking review passed the gate")
			}
		})
	}
	// A dossier that marks its own core claims is judged on those: c2 marked
	// core turns its unresolved review into a blocker.
	marked := gateDossier()
	marked.Claims[1].Core = true
	if len(reviewBlockers(gateReview(marked, "supported"))) == 0 {
		t.Fatal("a marked core claim left unresolved passed the gate")
	}
}

// Historical artifacts stored material issues as bare strings and had no
// categories. They must still load, and every such issue must still block, as
// it did when it was written.
func TestLegacyUncategorisedIssuesStillBlock(t *testing.T) {
	raw := `{"ticker":"AAA","verdict":"supported","reason":"ok","claims":[],"material_issues":["Check costs"]}`
	var c model.ThesisChallenge
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("historical challenge no longer loads: %v", err)
	}
	if len(c.MaterialIssues) != 1 || c.MaterialIssues[0].Issue != "Check costs" || c.MaterialIssues[0].Category != "" {
		t.Fatalf("decoded %+v", c.MaterialIssues)
	}
	if !c.MaterialIssues[0].Blocking() || len(reviewBlockers(c)) == 0 {
		t.Fatal("an uncategorised historical issue stopped blocking")
	}
	// A historical review has no contract version: only `supported` passes it.
	legacy := model.ThesisChallenge{Ticker: "AAA", Verdict: "revise", Claims: supportedResearch().Dossier.Claims}
	if len(reviewBlockers(legacy)) == 0 {
		t.Fatal("a historical revise verdict passed the gate")
	}
	// An unknown category blocks rather than slipping through as a risk.
	if !(model.MaterialIssue{Category: "vibes", Issue: "x"}).Blocking() {
		t.Fatal("unknown category treated as a disclosed risk")
	}
	// A historical dossier without a lean still decodes.
	var d model.CandidateDossier
	if err := json.Unmarshal([]byte(`{"ticker":"AAA","status":"watchlist","preferred_direction":"NONE","claims":[],"unresolved":["gap"]}`), &d); err != nil || d.Lean != "" {
		t.Fatalf("historical dossier: %+v %v", d, err)
	}
}

func TestNewChallengeIssueCategoriesAreAClosedEnum(t *testing.T) {
	check := currentResearchSchema(challengeSchema)
	c := model.ThesisChallenge{ContractVersion: 2, Ticker: "AAA", Verdict: "revise", Reason: "r",
		MaterialIssues: []model.MaterialIssue{{Category: "priced_in", Issue: "typo category"}}}
	if problems := check(&c); len(problems) == 0 || !strings.Contains(strings.Join(problems, ";"), "priced_in") {
		t.Fatalf("unknown category accepted: %v", problems)
	}
	c.MaterialIssues[0].Category = model.IssuePricedInUnprovable
	if problems := check(&c); len(problems) != 0 {
		t.Fatal(problems)
	}
}

func TestDossierLeanAndLabelValidation(t *testing.T) {
	valid := func() model.CandidateDossier {
		d := gateDossier()
		d.Lean, d.Conviction = "BUY", 3
		return d
	}
	if p := dossierLabelProblems(ptr(valid())); len(p) != 0 {
		t.Fatalf("valid dossier rejected: %v", p)
	}
	none := valid()
	none.PreferredDirection, none.Status, none.Lean, none.NoneReason = "NONE", "watchlist", "SELL", "event_inside_window"
	yes := true
	none.MoveDriver, none.PendingBinaryEvent, none.CorporateAction = "earnings", &model.BinaryEvent{Present: true, Date: "2026-10-02"}, &yes
	if p := dossierLabelProblems(&none); len(p) != 0 {
		t.Fatalf("a NONE dossier with a lean and a reason rejected: %v", p)
	}
	cases := map[string]func(*model.CandidateDossier){
		"missing lean":           func(d *model.CandidateDossier) { d.Lean = "" },
		"lean NONE":              func(d *model.CandidateDossier) { d.Lean = "NONE" },
		"conviction zero":        func(d *model.CandidateDossier) { d.Conviction = 0 },
		"conviction six":         func(d *model.CandidateDossier) { d.Conviction = 6 },
		"NONE without reason":    func(d *model.CandidateDossier) { d.PreferredDirection = "NONE" },
		"NONE with other reason": func(d *model.CandidateDossier) { d.PreferredDirection, d.NoneReason = "NONE", "uncertain" },
		"reason with BUY":        func(d *model.CandidateDossier) { d.NoneReason = "no_mechanism" },
		"lean contradicts":       func(d *model.CandidateDossier) { d.Lean = "SELL" },
		"move driver":            func(d *model.CandidateDossier) { d.MoveDriver = "momentum" },
		"event date": func(d *model.CandidateDossier) {
			d.PendingBinaryEvent = &model.BinaryEvent{Present: true, Date: "late October"}
		},
		"four core claims": func(d *model.CandidateDossier) {
			for len(d.Claims) < 4 {
				d.Claims = append(d.Claims, d.Claims[0])
			}
			for i := range d.Claims {
				d.Claims[i].Core = true
			}
		},
	}
	for name, mutate := range cases {
		d := valid()
		mutate(&d)
		if len(dossierLabelProblems(&d)) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// A dossier without a lean is a schema error, and like every schema error it
// buys exactly one repair call rather than a verdict on the company.
func TestMissingLeanBuysTheSingleRepair(t *testing.T) {
	d := gateDossier()
	d.Lean, d.Conviction = "BUY", 3
	calls, repairPrompt := 0, ""
	runner, _, done := thesisFixture(t, func(prompt string, _ int) string {
		calls++
		if strings.Contains(prompt, "formatting correction only") {
			repairPrompt = prompt
			return fenced(d)
		}
		var value map[string]any
		json.Unmarshal([]byte(jsonText(d)), &value)
		value["lean"], value["conviction"] = "", 0
		return fenced(value)
	})
	defer done()
	var out model.CandidateDossier
	_, _, parsing, err := researchCall(context.Background(), runner, "thesis-researcher", "lean-repair", "data", &out, dossierSchema)
	if err != nil || parsing != model.OutcomeRepaired || calls != 2 {
		t.Fatalf("err=%v parsing=%s calls=%d", err, parsing, calls)
	}
	if !strings.Contains(repairPrompt, `"lean"`) || out.Lean != "BUY" || out.Conviction != 3 {
		t.Fatalf("repair did not name or restore the lean: lean=%q prompt has lean=%t", out.Lean, strings.Contains(repairPrompt, `"lean"`))
	}
}

// End to end: a supported BUY dossier whose review supports its core claim and
// raises only a disclosed risk ships as supported without a revision call,
// with the risk carried, its evidence quality mixed, the plan inheriting the
// risks, and every label persisted in data/research-<hex>.json by JSON key.
func TestDisclosedRiskThesisShipsAndPersistsItsLabels(t *testing.T) {
	id := fixtureEvidenceID(t)
	d := gateDossier()
	for i := range d.Claims {
		d.Claims[i].EvidenceIDs = []string{id}
		d.Claims[i].Passages[0].EvidenceID = id
	}
	no := false
	d.Lean, d.Conviction, d.EvidenceQuality = "BUY", 4, "strong"
	d.MoveDriver, d.PendingBinaryEvent, d.CorporateAction = "news", &model.BinaryEvent{Present: true, Date: "2026-10-20"}, &no
	calls, revised := 0, false
	runner, pack, done := thesisFixture(t, func(prompt string, _ int) string {
		calls++
		if strings.Contains(prompt, "Final revision:") {
			revised = true
		}
		if strings.Contains(prompt, "# Independent thesis challenge") {
			return fenced(model.ThesisChallenge{ContractVersion: 2, Ticker: "AAA", DossierHash: captured13Hash(prompt), Verdict: "supported",
				Reason: "The core release supports the mechanism; timing corroboration cannot be had from free data.",
				ClaimReviews: []model.ClaimReview{
					{ClaimID: "c1", Assessment: "supported", Attribution: "confirmed", Reason: "issuer release quoted"},
					{ClaimID: "c2", Assessment: "unresolved", Attribution: "confirmed", Reason: "no second source for timing"},
				},
				MaterialIssues: []model.MaterialIssue{issue(model.IssueForecastMechanism, "the delivery update's size is a forecast")}})
		}
		return fenced(d)
	})
	defer done()
	out := runner.investigate(context.Background(), model.Candidate{Ticker: "AAA", Name: "Company", Sector: "Health Care"}, nil, pack, nil)
	if out.Dossier.Status != "supported" {
		t.Fatalf("disclosed-risk thesis did not ship: status=%s blocking=%v issues=%+v", out.Dossier.Status, out.Blocking, out.Challenge.MaterialIssues)
	}
	if revised || calls != 2 {
		t.Fatalf("a disclosed risk bought a revision: calls=%d revised=%t", calls, revised)
	}
	if out.Dossier.EvidenceQuality != "mixed" || len(out.Dossier.Risks) != 2 {
		t.Fatalf("risks not disclosed: quality=%s risks=%v", out.Dossier.EvidenceQuality, out.Dossier.Risks)
	}

	raw, err := os.ReadFile(filepath.Join(runner.run.Dir, "data", "research-414141.json"))
	if err != nil {
		t.Fatal(err)
	}
	var artifact struct {
		Dossier map[string]json.RawMessage `json:"dossier"`
	}
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"lean": `"BUY"`, "conviction": `4`, "move_driver": `"news"`,
		"pending_binary_event": `{"present":true,"date":"2026-10-20"}`, "corporate_action": `false`, "evidence_quality": `"mixed"`} {
		var got bytes.Buffer
		json.Compact(&got, artifact.Dossier[key])
		if got.String() != want {
			t.Errorf("artifact dossier.%s = %s, want %s", key, got.String(), want)
		}
	}
	if len(artifact.Dossier["risks"]) == 0 {
		t.Error("artifact does not persist the disclosed risks")
	}

	now := time.Now()
	p := quant.NewPack()
	p.ByTicker["AAA"] = quant.Metrics{LastClose: 100, SigmaDaily: .02, AvgDollarVol20USD: 1e9, FXToUSD: 1, Currency: "USD"}
	cfg := Config{Mode: model.ModeIndependent}
	cfg.applyDefaults()
	res := &model.IdeasResult{Ideas: []model.TradeIdea{{Ticker: "AAA", Direction: model.DirectionBuy, Entry: 100, Stop: 97, Target: 105, Why: "Delivery update", TimeframeDays: 15,
		Thesis: &model.ThesisPlan{WhyNow: "update", Invalidation: "delay", CatalystWindow: "two weeks", EntryReason: "last close", StopReason: "risk", TargetReason: "delivery update", TargetMethod: "thesis_scenario", OutcomeLow: 95, OutcomeHigh: 110, EvidenceIDs: []string{id}}}}}
	for _, f := range validateThesisResult(res, []thesisResearch{out}, verified{Quant: p, Thesis: true}, cfg, now, runner.calendar) {
		if strings.Contains(f.Message, "thesis or challenge unresolved") {
			t.Fatalf("the plan gate refused a supported disclosed-risk thesis: %s", f.Message)
		}
	}
	if th := res.Ideas[0].Thesis; len(th.Risks) != 2 || th.EvidenceQuality != "mixed" {
		t.Fatalf("plan did not inherit the disclosed risks: %+v", th)
	}
}

// An unreachable or stale source is a fact about retrieval, not about the
// thesis: whether any claim rests on it is decided claim by claim, and core
// claims must already be supported with confirmed attribution, their quotes
// verified in Go against stored text. On the 2026-09-24 run 2330.TW's BUY
// dossier had all nine claim reviews supported and was held off supported by
// this one category alone. It travels as a disclosed risk.
func TestStaleSourceIsADisclosedRiskWhenCoreClaimsHold(t *testing.T) {
	d := gateDossier()
	c := gateReview(d, "revise",
		issue(model.IssueStaleSource, "the investor-relations page returned 403"),
		issue(model.IssueAnnualTargetHorizon, "brokers' targets are twelve-month"))
	if blockers := reviewBlockers(c); len(blockers) != 0 {
		t.Fatalf("a stale source blocked a review whose core claims hold: %v", blockers)
	}
	risks := strings.Join(disclosedRisks(d, c, nil), "\n")
	if !strings.Contains(risks, "stale_or_inaccessible_source: the investor-relations page returned 403") {
		t.Fatalf("the stale source was not carried as a risk:\n%s", risks)
	}
}
