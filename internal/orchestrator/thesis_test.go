package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

func supportedResearch() thesisResearch {
	doc := model.EvidenceDocument{ID: "ev-doc", Ticker: "AAA", URL: "https://issuer.example/release", Kind: "document", Text: strings.Repeat("Issuer raised guidance and expects its delivery update in two weeks. ", 5)}
	claim := model.ResearchClaim{ID: "c1", Kind: "inference", Text: "Deliveries may resolve changed expectations", EvidenceIDs: []string{doc.ID}, Passages: []model.ClaimPassage{{EvidenceID: doc.ID, Quote: "Issuer raised guidance and expects its delivery update in two weeks.", IssuerRole: "issuer giving guidance"}}}
	return thesisResearch{Candidate: model.Candidate{Ticker: "AAA", Name: "Company", Sector: "Health Care"}, Documents: []model.EvidenceDocument{doc}, Dossier: model.CandidateDossier{Ticker: "AAA", Status: "supported", LongCase: "Deliveries may lift expectations", ShortCase: "Delivery delays could lower expectations", NoTradeCase: "Stand aside if update timing is unresolved", PreferredDirection: "BUY", Hypothesis: "Guidance revision", Changed: "raised shipments", Expectations: "previous guidance", Underappreciated: "delivery timing", Mechanism: "shipment update in two weeks", PricedIn: "partial rise", Counterargument: "shipment delay", Invalidation: "delay announced", CatalystWindow: "next two weeks", EvidenceQuality: "mixed", Claims: []model.ResearchClaim{claim}}, Challenge: model.ThesisChallenge{Ticker: "AAA", Verdict: "supported", Reason: "source supports the mechanism", Claims: []model.ResearchClaim{claim}}}
}
func TestThesisRejectsHeadlineOnlyAndInventedEvidence(t *testing.T) {
	for _, kind := range []string{"headline", "invented"} {
		r := supportedResearch()
		if kind == "headline" {
			r.Documents[0].Kind = "headline"
		} else {
			r.Dossier.Claims[0].EvidenceIDs = []string{"ev-invented"}
		}
		validateDossier(&r)
		if r.Dossier.Status == "supported" {
			t.Fatalf("accepted %s", kind)
		}
	}
	r := supportedResearch()
	validateDossier(&r)
	if r.Dossier.Status != "supported" {
		t.Fatalf("rejected source dossier: %+v", r.Dossier)
	}
}
func TestDriftFullRetracementUsesCumulativeReturnNotDurationScaledZ(t *testing.T) {
	// Jump 100->110 over the event window, return to exactly 100 ten days
	// later. Dividing the later move by sqrt(10) used to hide the full reversal.
	s := &quant.Series{Symbol: "AAA"}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		price := 100.0
		if i == 5 {
			price = 105
		}
		if i >= 6 && i < 16 {
			price = 110
		}
		s.Bars = append(s.Bars, quant.Bar{Date: start.AddDate(0, 0, i).Format("2006-01-02"), Close: price})
	}
	d, ok := computeDrift(s, nil, start.AddDate(0, 0, 5), .02)
	if !ok || !d.retraced() || d.Score() != 0 {
		t.Fatalf("full reversal remained live: %+v", d)
	}
	s.Bars[19].Close = 106
	d, _ = computeDrift(s, nil, start.AddDate(0, 0, 5), .02)
	if d.retraced() {
		t.Fatal("partial reversal called complete")
	}
}
func TestThesisRiskDoesNotForceLegacyGeometry(t *testing.T) {
	cfg := riskDefaults(model.RiskConfig{})
	idea := model.TradeIdea{Ticker: "AAA", Direction: model.DirectionBuy, Entry: 100, Stop: 99, Target: 101, TimeframeDays: 15}
	p := quant.NewPack()
	p.ByTicker["AAA"] = quant.Metrics{LastClose: 100, SigmaDaily: .02, AvgDollarVol20USD: 1e9, FXToUSD: 1, Currency: "USD"}
	fs := gateIdea(&idea, verified{Quant: p, Thesis: true}, cfg)
	for _, f := range fs {
		if strings.Contains(f.Message, "floor") && !f.Observational {
			t.Fatalf("legacy floor retained: %s", f.Message)
		}
	}
	idea.Target = 200
	fs = gateIdea(&idea, verified{Quant: p, Thesis: true}, cfg)
	found := false
	for _, f := range fs {
		if f.Hard && strings.Contains(f.Message, "ceiling") {
			found = true
		}
	}
	if !found {
		t.Fatal("lost maximum target risk constraint")
	}
}

type researchFixtureProvider struct{}

func (researchFixtureProvider) Name() string      { return "fixture" }
func (researchFixtureProvider) Source() string    { return "https://issuer.example" }
func (researchFixtureProvider) Domains() []string { return []string{"news"} }
func (researchFixtureProvider) Available() bool   { return true }
func (researchFixtureProvider) MacroFetch(context.Context) ([]marketdata.Fact, error) {
	return nil, nil
}
func (researchFixtureProvider) Fetch(_ context.Context, _ string, ticker string) (marketdata.TickerData, error) {
	return marketdata.TickerData{Ticker: ticker, Facts: []marketdata.Fact{{Label: "Headline 1", Value: "Guidance raised", Content: strings.Repeat("Issuer raised guidance and expects its delivery update in two weeks. ", 5), URL: "https://issuer.example/release", Source: "issuer"}}}, nil
}
func TestCompanyResearchLoopAndIndependentChallenge(t *testing.T) {
	// No CLI accounts or external URLs: evidence comes from a fixture provider,
	// and a local model endpoint exercises the actual pool and prompt assembly.
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		prompt := req.Messages[0].Content
		calls++
		docs := marketdata.EvidenceFromPack(func() *marketdata.DataPack {
			p := marketdata.NewDataPack("news")
			td, _ := (researchFixtureProvider{}).Fetch(context.Background(), "news", "AAA")
			p.ByTicker["AAA"] = td
			return p
		}(), "AAA", time.Now())
		claim := model.ResearchClaim{ID: "c1", Kind: "inference", Text: "Delivery update may reprice expectations", EvidenceIDs: []string{docs[0].ID}, Passages: []model.ClaimPassage{{EvidenceID: docs[0].ID, Quote: "Issuer raised guidance and expects its delivery update in two weeks.", IssuerRole: "issuer giving guidance"}}}
		var response any
		if strings.Contains(prompt, "# Independent thesis challenge") {
			response = model.ThesisChallenge{Ticker: "AAA", Verdict: "supported", Reason: "The release supports the timing", Claims: []model.ResearchClaim{claim}}
		} else {
			d := supportedResearch().Dossier
			d.Claims = []model.ResearchClaim{claim}
			response = d
		}
		content := "```json\n" + jsonText(response) + "\n```"
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": currentFixtureReply(content, prompt)}}}})
	}))
	defer srv.Close()
	cfg := Config{ResearchMode: "thesis", CheapEngine: model.CLIApi, API: model.APIConfig{BaseURL: srv.URL, Model: "fixture", APIKey: "fixture"}, AgentsDir: "../../agents", DataDir: t.TempDir(), RunsDir: t.TempDir()}
	cfg.applyDefaults()
	reg, e := agents.Load(cfg.AgentsDir)
	if e != nil {
		t.Fatal(e)
	}
	run, e := store.New(cfg.RunsDir)
	if e != nil {
		t.Fatal(e)
	}
	pool := newPool(1, nil, nil, cfg.API, model.CLIApi, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.start(ctx)
	defer pool.stop()
	runner := thesisRunner{cfg: cfg, ch: make(chan Event, 100), run: run, reg: reg, pool: pool, cheap: model.CLIApi, svc: marketdata.NewService(nil, researchFixtureProvider{}), sources: map[string][]string{}}
	p := quant.NewPack()
	p.ByTicker["AAA"] = quant.Metrics{LastClose: 100, SigmaDaily: .02}
	got := runner.investigate(ctx, model.Candidate{Ticker: "AAA", Name: "Company"}, nil, p, nil)
	if got.Dossier.Status != "supported" || got.Challenge.Verdict != "supported" {
		t.Fatalf("research failed: %+v", got)
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want research + independent challenge", calls)
	}
	if len(got.Reports) != 2 {
		t.Fatal("missing call accounting")
	}
	matches, _ := filepath.Glob(filepath.Join(run.Dir, "data", "research-*.json"))
	if len(matches) != 1 {
		t.Fatal("missing evidence artifact")
	}
}
func TestThesisUnknownEvidenceCannotSurviveFinalSelection(t *testing.T) {
	r := supportedResearch()
	p := quant.NewPack()
	p.ByTicker["AAA"] = quant.Metrics{LastClose: 100, SigmaDaily: .02, AvgDollarVol20USD: 1e9, FXToUSD: 1, Currency: "USD"}
	cfg := Config{Mode: model.ModeIndependent}
	cfg.applyDefaults()
	idea := model.TradeIdea{Ticker: "AAA", Direction: model.DirectionBuy, Entry: 100, Stop: 97, Target: 105, Why: "Delivery update", TimeframeDays: 20, Thesis: &model.ThesisPlan{WhyNow: "update", Invalidation: "delay", CatalystWindow: "two weeks", EntryReason: "last close", StopReason: "risk", TargetReason: "delivery update", OutcomeLow: 95, OutcomeHigh: 110, EvidenceIDs: []string{"invented"}}}
	res := &model.IdeasResult{Ideas: []model.TradeIdea{idea}}
	v := verified{Quant: p, Thesis: true}
	fs := validateThesisResult(res, []thesisResearch{r}, v, cfg, time.Now(), marketdata.ResearchCalendar{})
	finalizeThesis(res, []thesisResearch{r}, fs, v, cfg, time.Now())
	if len(res.Ideas) != 0 || len(res.Decisions) != 1 || res.Decisions[0].Status != "rejected" {
		t.Fatalf("invalid idea survived: %s", fmt.Sprint(res))
	}
}

func TestThesisIndependentPipelineAllowsAResearchedNoTrade(t *testing.T) {
	testThesisIndependentNoTrade(t, false)
}

func TestThesisChiefFailureUsesConfiguredDeepSeekFallback(t *testing.T) {
	testThesisIndependentNoTrade(t, true)
}

func testThesisIndependentNoTrade(t *testing.T, chiefFails bool, capacityFailure ...bool) {
	t.Helper()
	capacity := len(capacityFailure) > 0 && capacityFailure[0]
	testThesisResultFixture(t, chiefFails, capacity, "no_trade")
}

func TestThesisConditionalIdeaCompletesFullPipeline(t *testing.T) {
	testThesisResultFixture(t, false, false, "conditional")
}

func TestThesisAllFailedSkipsChiefAndPersistsDegradedResults(t *testing.T) {
	testThesisResultFixture(t, true, false, "all_failed")
}

func testThesisResultFixture(t *testing.T, chiefFails, capacity bool, scenario string) {
	t.Helper()
	// Every network request is served locally, including discovery, prices and
	// model calls. The Chief deliberately chooses no trade after the dossier
	// passes; that must remain a complete run, never a mechanical fallback.
	data := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "search") {
			w.Write([]byte(`{"news":[],"quotes":[]}`))
			return
		}
		w.Write(syntheticChart(path.Base(r.URL.Path)))
	}))
	defer data.Close()
	t.Setenv("CFR_YAHOO_BASE", data.URL)
	pack := marketdata.NewDataPack("news")
	pack.ByTicker["AAA"], _ = (researchFixtureProvider{}).Fetch(context.Background(), "news", "AAA")
	doc := marketdata.EvidenceFromPack(pack, "AAA", time.Now())[0]
	claim := model.ResearchClaim{ID: "c1", Kind: "inference", Text: "Delivery timing may resolve expectations", EvidenceIDs: []string{doc.ID}, Passages: []model.ClaimPassage{{EvidenceID: doc.ID, Quote: "Issuer raised guidance and expects its delivery update in two weeks.", IssuerRole: "issuer giving guidance"}}}
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		prompt := request.Messages[0].Content
		var value any
		switch {
		case strings.Contains(prompt, "# Research candidate triage"):
			value = model.ScoutResult{Candidates: []model.Candidate{{Ticker: "AAA", Bias: model.BiasBullish, Reason: "Investigate the delivery update"}}}
		case strings.Contains(prompt, "# Independent thesis challenge"):
			review := model.ThesisChallenge{Ticker: "AAA", Verdict: "supported", Reason: "Source supports the mechanism", Claims: []model.ResearchClaim{claim}, ConditionsReviewed: true}
			if at := strings.Index(prompt, "Plan hash: "); at >= 0 {
				review.PlanHash = strings.Fields(prompt[at+len("Plan hash: "):])[0]
				review.TargetAssessment = "supported"
			}
			value = review
		case strings.Contains(prompt, "# Company researcher"):
			d := supportedResearch().Dossier
			d.Claims = []model.ResearchClaim{claim}
			if scenario == "conditional" {
				d.EntryConditions = []string{"Confirm an executable quote before entry"}
				d.Monitoring = []string{"Monitor the upcoming delivery announcement"}
			}
			if scenario == "all_failed" {
				d.Status = "invalid_status"
			}
			value = d
		default:
			value = model.SpecialistResult{Domain: "macro"}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": currentFixtureReply("```json\n"+jsonText(value)+"\n```", prompt)}}}})
	}))
	defer modelServer.Close()
	chiefResult := model.IdeasResult{Ideas: []model.TradeIdea{}, Decisions: []model.SelectionDecision{{Ticker: "AAA", Status: "rejected", Reason: "The potential move is already priced in"}}}
	if scenario == "conditional" {
		var chart struct {
			Chart struct {
				Result []struct {
					Indicators struct{ Quote []struct{ Close []float64 } }
				}
			}
		}
		if err := json.Unmarshal(syntheticChart("AAA"), &chart); err != nil {
			t.Fatal(err)
		}
		closes := chart.Chart.Result[0].Indicators.Quote[0].Close
		price := closes[len(closes)-1]
		chiefResult.Ideas = []model.TradeIdea{{Ticker: "AAA", Direction: model.DirectionBuy, Entry: price, Stop: price * .98, Target: price * 1.03, Why: "Delivery update changes expectations", TimeframeDays: 15, Thesis: &model.ThesisPlan{TargetMethod: "thesis_scenario", WhyNow: "delivery window", Invalidation: "delivery delay", CatalystWindow: "next two weeks", EntryReason: "verified last close", StopReason: "maximum tolerable scenario loss", TargetReason: "delivery conversion scenario", OutcomeLow: price * .97, OutcomeHigh: price * 1.05, EvidenceIDs: []string{doc.ID}}}}
		chiefResult.Decisions = []model.SelectionDecision{{Ticker: "AAA", Status: "conditional", Reason: "Supported thesis pending execution check"}}
	}
	chief := filepath.Join(t.TempDir(), "chief")
	if err := os.WriteFile(chief, []byte("#!/bin/sh\ncat <<'RESULT'\n"+fenced(chiefResult)+"\nRESULT\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Mode: model.ModeIndependent, ResearchMode: "thesis", AgentsDir: "../../agents", RunsDir: t.TempDir(), DataDir: t.TempDir(), CheapEngine: model.CLIApi,
		API: model.APIConfig{BaseURL: modelServer.URL, Model: "fixture", APIKey: "fixture"}, Binaries: map[model.CLI]string{model.CLIClaude: chief}}
	if chiefFails {
		if err := os.WriteFile(chief, []byte("#!/bin/sh\necho 'Your organization has disabled Claude subscription access for Claude Code' >&2\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
		srv := fakeDeepSeekServer(t, "```json\n"+jsonText(model.IdeasResult{Ideas: []model.TradeIdea{}, Decisions: []model.SelectionDecision{{Ticker: "AAA", Status: "rejected", Reason: "The potential move is already priced in"}}})+"\n```")
		cfg.ChiefFallback = model.APIConfig{BaseURL: srv.URL, Model: "deepseek-reasoner", APIKey: "fixture-fallback"}
	}
	cfg.applyDefaults()
	if capacity {
		cfg.Research.Budgets.Chief = model.RoleBudget{InputBytes: 4096, ResponseBytes: 1024}
	}
	reg, err := agents.Load(cfg.AgentsDir)
	if err != nil {
		t.Fatal(err)
	}
	uni, err := universe.Load()
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.New(cfg.RunsDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := newPool(2, cfg.Models, cfg.Binaries, cfg.API, model.CLIApi, 0)
	p.start(ctx)
	defer p.stop()
	cache := marketdata.NewCache(cfg.DataDir)
	prices := marketdata.NewPrices("", "", cache)
	fx := marketdata.NewFXRates(marketdata.NewYahooClient(cache))
	ps := &Prescreen{Rows: []PrescreenRow{{Ticker: "AAA", Name: "Company", Index: "sp500", Score: 1, Close: 100, Setup: SetupContinuation}}}
	ch := make(chan Event, 100)
	chiefE, err := resolveChiefEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	err = runThesis(ctx, cfg, ch, run, reg, uni, p, model.CLIApi, chiefE, marketdata.NewService(nil, researchFixtureProvider{}), prices, fx, ps, []string{"sp500"}, time.Now(), map[string]int64{})
	if err != nil {
		t.Fatal(err)
	}
	ideas, err := store.LoadIdeas(run.Dir)
	if err != nil {
		t.Fatal(err)
	}
	expectedIdeas := 0
	if scenario == "conditional" {
		expectedIdeas = 1
	}
	if ideas.SchemaVersion != 2 || ideas.ResearchMode != "thesis" || len(ideas.Ideas) != expectedIdeas || len(ideas.Decisions) != 1 || scenario == "no_trade" && !capacity && ideas.Decisions[0].Status != "rejected" {
		t.Fatalf("invalid no-trade result: %+v", ideas)
	}
	meta, err := store.LoadMeta(run.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if scenario == "conditional" {
		idea := ideas.Ideas[0]
		if idea.Status != "conditional" || idea.Shares <= 0 || !slices.Contains(idea.Thesis.Prerequisites, "Confirm an executable quote before entry") || len(idea.Thesis.Monitoring) != 1 {
			t.Fatalf("conditional plan incomplete: %+v", idea)
		}
		reviewed := false
		for _, call := range meta.Domains {
			if strings.HasPrefix(call.Domain, "plan-review-") && call.Payload == model.OutcomeOK {
				reviewed = true
			}
		}
		if !reviewed || ideas.ResearchSummary == nil || ideas.ResearchSummary.Conditional != 1 {
			t.Fatal("execution review or research summary missing")
		}
	}
	if scenario == "all_failed" {
		if meta.Outcome != "degraded" || ideas.Decisions[0].Blocked != model.BlockedResearchFailure || ideas.ResearchSummary == nil || ideas.ResearchSummary.Failed != 1 || ideas.ResearchSummary.Reviewed != 0 {
			t.Fatalf("failed research hidden: %+v", ideas)
		}
		for _, call := range meta.Domains {
			if strings.HasPrefix(call.Domain, "chief-analyst") {
				t.Fatal("empty research board reached Chief/fallback")
			}
		}
		// The Chief was never dispatched (usableDossiers == 0): nothing was
		// attempted and nothing was accepted, even though the primary engine
		// is still the one configured.
		if meta.ChiefEngine != "claude" {
			t.Errorf("ChiefEngine = %q, want claude (the configured primary, whether or not it was ever called)", meta.ChiefEngine)
		}
		if meta.ChiefAttempted != "" {
			t.Errorf("ChiefAttempted = %q, want empty — the Chief was skipped entirely", meta.ChiefAttempted)
		}
		if meta.ChiefAccepted != "" {
			t.Errorf("ChiefAccepted = %q, want empty — no engine's output shipped", meta.ChiefAccepted)
		}
		return
	}
	if capacity {
		if meta.Outcome != "degraded" {
			t.Fatal("capacity failure did not finalize degraded")
		}
		count := 0
		for _, domain := range meta.Domains {
			if domain.FailureKind == "input_capacity" {
				count++
				if domain.Attempts != 0 {
					t.Fatal("oversized input dispatched")
				}
			}
		}
		if count != 2 {
			t.Fatalf("primary and fallback capacity failures not retained: %d", count)
		}
		// Both the primary and the fallback were dispatched and both failed:
		// attempted names both engines, accepted names neither.
		if meta.ChiefAttempted != "claude,api" {
			t.Errorf("ChiefAttempted = %q, want claude,api", meta.ChiefAttempted)
		}
		if meta.ChiefAccepted != "" {
			t.Errorf("ChiefAccepted = %q, want empty — both primary and fallback failed on capacity", meta.ChiefAccepted)
		}
	} else if chiefFails {
		if meta.Outcome != "degraded" || meta.SynthesisFallbackEngine != "deepseek-reasoner" {
			t.Fatalf("fallback provenance missing: %+v", meta)
		}
		found := false
		for _, domain := range meta.Domains {
			if domain.Domain == "chief-analyst-fallback" && domain.Status == model.StatusDone {
				found = true
			}
		}
		if !found {
			t.Fatal("configured fallback did not complete")
		}
		// The primary (claude) call failed and the DeepSeek fallback (api)
		// rescued the run: configured stays claude, attempted lists both in
		// order, accepted and the model actually addressed are the fallback's.
		if meta.ChiefEngine != "claude" {
			t.Errorf("ChiefEngine = %q, want claude", meta.ChiefEngine)
		}
		if meta.ChiefAttempted != "claude,api" {
			t.Errorf("ChiefAttempted = %q, want claude,api", meta.ChiefAttempted)
		}
		if meta.ChiefAccepted != "api" {
			t.Errorf("ChiefAccepted = %q, want api", meta.ChiefAccepted)
		}
		if meta.ChiefModel != "deepseek-reasoner" || meta.SynthesisModel != "deepseek-reasoner" {
			t.Errorf("ChiefModel/SynthesisModel = %q/%q, want deepseek-reasoner/deepseek-reasoner", meta.ChiefModel, meta.SynthesisModel)
		}
	} else if meta.Outcome != "complete" {
		t.Fatalf("no trade degraded: %+v", meta)
	} else {
		// The primary claude call answered directly: configured, attempted
		// and accepted all agree, and the model is the resolved primary's
		// (cfg.Models[CLIClaude], defaulted to "opus"), never left unset.
		if meta.ChiefEngine != "claude" || meta.ChiefAttempted != "claude" || meta.ChiefAccepted != "claude" {
			t.Errorf("provenance mismatch on a clean primary success: engine=%q attempted=%q accepted=%q", meta.ChiefEngine, meta.ChiefAttempted, meta.ChiefAccepted)
		}
		if meta.ChiefModel != "opus" || meta.SynthesisModel != "opus" {
			t.Errorf("ChiefModel/SynthesisModel = %q/%q, want opus/opus", meta.ChiefModel, meta.SynthesisModel)
		}
	}
	if _, err := os.Stat(filepath.Join(run.Dir, "data", "research.json")); err != nil {
		t.Fatal(err)
	}
}

func TestThesisDecodeDistinguishesMalformedFromNoTrade(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"ideas":null,"decisions":[]}`, `{"ideas":[],"decisions":null}`} {
		if _, err := parseThesisIdeas("```json\n" + raw + "\n```"); err == nil {
			t.Errorf("accepted malformed no-trade response %s", raw)
		}
	}
	if _, err := parseThesisIdeas("```json\n{\"ideas\":[],\"decisions\":[]}\n```"); err != nil {
		t.Fatal(err)
	}
	d := model.CandidateDossier{Requests: []model.ResearchRequest{{Kind: "filings"}}, Unresolved: []string{"old gap"}}
	if err := decodeResearch("```json\n{\"ticker\":\"AAA\",\"status\":\"supported\"}\n```", &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Requests) != 0 || len(d.Unresolved) != 0 {
		t.Fatal("previous round's state leaked into replacement dossier")
	}
}

func TestThesisSupportedPlanAndEarningsCutoff(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for _, event := range []string{"", "2026-09-10"} {
		r := supportedResearch()
		r.NextEvent = event
		p := quant.NewPack()
		p.ByTicker["AAA"] = quant.Metrics{LastClose: 100, SigmaDaily: .02, AvgDollarVol20USD: 1e9, FXToUSD: 1, Currency: "USD"}
		cfg := Config{Mode: model.ModeIndependent}
		cfg.applyDefaults()
		idea := model.TradeIdea{Ticker: "AAA", Direction: model.DirectionBuy, Entry: 100, Stop: 97, Target: 105, Why: "Delivery update", TimeframeDays: 15, Thesis: &model.ThesisPlan{WhyNow: "update", Invalidation: "delay", CatalystWindow: "two weeks", EntryReason: "last close", StopReason: "risk", TargetReason: "delivery update", OutcomeLow: 95, OutcomeHigh: 110, EvidenceIDs: []string{"ev-doc"}}}
		res := &model.IdeasResult{Ideas: []model.TradeIdea{idea}}
		v := verified{Quant: p, Thesis: true}
		fs := validateThesisResult(res, []thesisResearch{r}, v, cfg, now, marketdata.ResearchCalendar{})
		finalizeThesis(res, []thesisResearch{r}, fs, v, cfg, now)
		if event == "" {
			if len(res.Ideas) != 1 || res.Ideas[0].Status != "conditional" || res.Ideas[0].Shares <= 0 || res.Ideas[0].Confidence != 0 {
				t.Fatalf("supported plan failed: %+v, findings=%+v", res, fs)
			}
		} else if len(res.Ideas) != 0 {
			t.Fatal("near earnings silently shortened the thesis below ten sessions")
		}
	}
}

func TestThesisLargeChiefPromptUsesStdin(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "chief")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n[ \"$1\" = '-p' ] || exit 2\n[ \"$2\" = '--model' ] || exit 2\n[ \"$3\" = 'fixture' ] || exit 2\ncat\n"), 0700); err != nil {
		t.Fatal(err)
	}
	prompt := strings.Repeat("evidence ", 20000)
	r := runAgent(context.Background(), model.CLIClaude, "chief-analyst", "synthesis", prompt, 5*time.Second, model.RetryPolicy{MaxAttempts: 1}, "fixture", bin, model.APIConfig{})
	if r.Status != model.StatusDone || r.Stdout != prompt {
		t.Fatalf("large prompt lost: status=%s err=%s bytes=%d", r.Status, r.Err, len(r.Stdout))
	}
}

func TestThesisChiefCapacityFailurePersistsDegradedArtifacts(t *testing.T) {
	testThesisIndependentNoTrade(t, true, true)
}
