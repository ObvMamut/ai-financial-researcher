package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

type thesisResearch struct {
	Eligibility string                   `json:"eligibility,omitempty"`
	Temporal    researchTimeFacts        `json:"temporal_facts"`
	NextEvent   string                   `json:"next_event,omitempty"`
	Candidate   model.Candidate          `json:"candidate"`
	Documents   []model.EvidenceDocument `json:"documents"`
	Dossier     model.CandidateDossier   `json:"dossier"`
	Challenge   model.ThesisChallenge    `json:"challenge"`
	Reports     []model.DomainStatus     `json:"reports"`
	// Outcome separates transport, parsing, evidence and review. A candidate
	// whose research never produced a readable dossier is not a candidate the
	// pipeline examined and turned down, and the run has to be able to say so.
	Outcome model.ResearchOutcome `json:"outcome"`
	// Results answers every research request the models made, so an operation
	// that cannot be fulfilled is stated once instead of retried each round.
	Results []model.ResearchResult `json:"results,omitempty"`
	Errors  []string               `json:"errors,omitempty"`
}
type thesisRunner struct {
	fx       *marketdata.FXRates
	cfg      Config
	ch       chan<- Event
	run      *store.Run
	reg      *agents.Registry
	pool     *pool
	cheap    model.CLI
	svc      *marketdata.Service
	sources  map[string][]string
	calendar marketdata.ResearchCalendar
}

func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }
func decodeResearch[T any](s string, v *T) error {
	raw, ok := extractLastJSON(s)
	if !ok {
		return fmt.Errorf("missing fenced research JSON")
	}
	var next T
	if err := json.Unmarshal([]byte(raw), &next); err != nil {
		return err
	}
	*v = next
	return nil
}

func parseThesisIdeas(s string) (*model.IdeasResult, error) {
	var result model.IdeasResult
	if err := decodeResearch(s, &result); err != nil {
		return nil, err
	}
	if result.Ideas == nil || result.Decisions == nil {
		return nil, fmt.Errorf("thesis response requires explicit ideas and decisions arrays")
	}
	return &result, nil
}

func validateResearchBudgets(c model.ResearchConfig) error {
	if c.Rounds < 1 || c.Rounds > 6 || c.Documents < 1 || c.Documents > 32 || c.Candidates < 1 || c.Candidates > 48 || c.Shortlist < 1 || c.Shortlist > c.Candidates {
		return fmt.Errorf("invalid research budgets")
	}
	return c.Budgets.Defaults().Validate()
}
func (t *thesisRunner) call(ctx context.Context, role, name, data string, target callTarget) (model.Report, error) {
	if err := ctx.Err(); err != nil {
		return model.Report{Agent: name, CLI: target.CLI, Status: model.StatusFailed, FailureKind: "cancelled", Err: err.Error()}, err
	}
	// Only a CLIApi call actually has a max_tokens parameter; a CLI subprocess
	// (gemini/claude) has no such cap, and the profile must not claim one for
	// a call that has none — an absent/zero OutputTokenLimit means
	// "unrecorded," never a measured value.
	outputTokens := 0
	if target.CLI == model.CLIApi {
		outputTokens = target.API.MaxTokens
		if outputTokens <= 0 {
			outputTokens = defaultMaxTokens
		}
	}
	prompt, profile, e := t.preparePrompt(role, name, data, outputTokens)
	if e != nil {
		r := model.Report{Agent: name, CLI: target.CLI, Status: model.StatusFailed, Err: e.Error(), FailureKind: promptFailureKind(e), Prompt: profile}
		agentStatus(t.ch, name, r.Status, &r)
		return r, e
	}
	agentStatus(t.ch, name, model.StatusRunning, nil)
	var r model.Report
	if target.throttled {
		// Cheap calls still go through the shared pool, so its cheap-engine
		// throttle (agy keyring contention / a single local GPU) keeps
		// applying exactly as before.
		r = <-t.pool.submit(target.CLI, name, string(target.Stage), prompt, target.Timeout, target.Retry)
	} else {
		// The Chief never goes through the pool: its Model/Binary/API are its
		// own, resolved by resolveChiefEngine, and must never be re-resolved
		// through the pool's per-CLI maps (which belong to the cheap engine
		// and can share model.CLIApi's value with a Chief routed to "api").
		r = runAgent(ctx, target.CLI, name, string(target.Stage), prompt, target.Timeout, target.Retry, target.Model, target.Binary, target.API)
	}
	responseCapacity(&r, profile)
	if e = t.run.WriteReport(name, r.Stdout); e != nil {
		return r, e
	}
	agentStatus(t.ch, name, r.Status, &r)
	if r.Status != model.StatusDone {
		return r, fmt.Errorf("%s: %s", name, r.Err)
	}
	return r, nil
}
func reportStatus(r model.Report) model.DomainStatus {
	return model.DomainStatus{FailureKind: r.FailureKind, Prompt: r.Prompt, Domain: r.Agent, Status: r.Status, Err: r.Err, Attempts: r.Attempts, Duration: r.Duration, Tokens: r.Tokens, Usage: r.Usage}
}
func runThesis(ctx context.Context, cfg Config, ch chan<- Event, run *store.Run, reg *agents.Registry, uni *universe.Universe, p *pool, cheap model.CLI, chiefE chiefEngine, svc *marketdata.Service, prices marketdata.PriceSource, fx *marketdata.FXRates, ps *Prescreen, indices []string, start time.Time, stages map[string]int64) error {
	if err := validateResearchBudgets(cfg.Research); err != nil {
		return err
	}
	sources, e := marketdata.LoadResearchSources(cfg.Research.SourcesFile)
	if e != nil {
		return e
	}
	calendar, e := marketdata.LoadResearchCalendar(cfg.Research.HolidaysFile)
	if e != nil {
		return e
	}
	t := &thesisRunner{cfg: cfg, ch: ch, run: run, reg: reg, pool: p, cheap: cheap, svc: svc, sources: sources, calendar: calendar, fx: fx}
	var statuses []model.DomainStatus
	var errs []string
	phase := time.Now()
	candidates, news, ds, discoveryErrors := t.discover(ctx, uni, ps, indices)
	statuses = append(statuses, ds...)
	errs = append(errs, discoveryErrors...)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if e = run.WriteDataPack("discovery", news); e != nil {
		return e
	}
	if e = run.WriteDataPack("discovery-coverage", marketdata.MeasureResearchCoverage(news)); e != nil {
		return e
	}
	if e = run.WriteDataPack("candidates", candidates); e != nil {
		return e
	}
	shortlist := candidates
	if len(candidates) > 0 && cfg.Mode != model.ModeSingle {
		candidateNews := make(map[string][]model.EvidenceDocument, len(candidates))
		for _, c := range candidates {
			candidateNews[c.Ticker] = news[c.Ticker]
		}
		data := "Select at most " + fmt.Sprint(cfg.Research.Shortlist) + " research candidates.\n" + jsonText(candidates) + "\nEvidence:\n" + jsonText(compactEvidence(candidateNews, 4))
		var result model.ScoutResult
		reports, _, _, err := researchCall(ctx, t, "thesis-triage", "thesis-triage", data, &result, scoutSchema)
		statuses = append(statuses, reports...)
		if err != nil {
			errs = append(errs, "triage: "+err.Error())
			shortlist = nil
		} else {
			shortlist = validatedCandidates(result.Candidates, candidates, cfg.Research.Shortlist)
		}
	}
	if e = run.WriteShortlist(shortlist); e != nil {
		return e
	}
	stages["screening"] = time.Since(phase).Milliseconds()
	phase = time.Now()
	qp, series := buildQuantPack(ctx, ch, run, prices, fx, shortlist)
	stages["quant"] = time.Since(phase).Milliseconds()
	phase = time.Now()
	macro := svc.BuildPack(ctx, "macro", nil)
	if e = run.WriteDataPack("macro", macro); e != nil {
		return e
	}
	macroR, macroErr := t.call(ctx, "macro", "macro", macro.Markdown()+qp.RegimeBlock(), t.cheapTarget())
	statuses = append(statuses, reportStatus(macroR))
	if macroErr != nil {
		errs = append(errs, macroErr.Error())
	}
	research := make([]thesisResearch, len(shortlist))
	// Bound both retrieval and model work. The existing pool independently honors
	// CLI/local throttles, so research concurrency cannot bypass account limits.
	sem := make(chan struct{}, cfg.Workers)
	var wg sync.WaitGroup
	for i, c := range shortlist {
		wg.Add(1)
		go func(i int, c model.Candidate) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			research[i] = t.investigate(ctx, c, news[c.Ticker], qp, series)
		}(i, c)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, r := range research {
		statuses = append(statuses, r.Reports...)
		errs = append(errs, r.Errors...)
	}
	if e = run.WriteDataPack("research", research); e != nil {
		return e
	}
	researchedEvidence := map[string][]model.EvidenceDocument{}
	for _, r := range research {
		researchedEvidence[r.Candidate.Ticker] = r.Documents
	}
	if e = run.WriteDataPack("research-coverage", marketdata.MeasureResearchCoverage(researchedEvidence)); e != nil {
		return e
	}
	stages["analysis"] = time.Since(phase).Milliseconds()
	phase = time.Now()
	result := &model.IdeasResult{Ideas: []model.TradeIdea{}}
	chiefData := "Maximum ideas: 5 (single mode: 1). Research:\n" + jsonText(chiefContext(research)) + "\nVerified quant:\n" + qp.CompactBlock() + "\nMacro:\n" + macroR.Stdout + "\nMaximum risk policy (minimum stop/RR and expectancy floors do not apply):\n" + jsonText(cfg.Risk)
	usableDossiers := 0
	for _, r := range research {
		if !r.researchFailed() && r.Eligibility == "" && r.Dossier.Ticker != "" {
			usableDossiers++
		}
	}
	// chiefAttemptedEngines/chiefAcceptedEngine/chiefModel are Task 6's
	// provenance trail — see the equivalent tracking in orchestrator.go's
	// legacy dispatch for the full rationale. Left empty/unset when
	// usableDossiers == 0: the Chief is never dispatched on an empty research
	// board (see the all-failed skip below), so nothing was attempted and
	// nothing was accepted, even though the primary engine is still the one
	// configured.
	var chiefAttemptedEngines []string
	var chiefAcceptedEngine string
	chiefModel := chiefE.Model
	if usableDossiers > 0 {
		chiefAttemptedEngines = append(chiefAttemptedEngines, string(chiefE.CLI))
		r, err := t.call(ctx, "thesis-chief", "chief-analyst", chiefData, chiefTarget(chiefE, cfg, chiefInitial))
		statuses = append(statuses, reportStatus(r))
		if err == nil {
			result, err = parseThesisIdeas(r.Stdout)
			if err != nil {
				statuses[len(statuses)-1].Payload = "invalid"
			} else {
				statuses[len(statuses)-1].Payload = model.OutcomeOK
				chiefAcceptedEngine = string(chiefE.CLI)
			}
		}
		if err != nil {
			errs = append(errs, "chief: "+err.Error())
			result = &model.IdeasResult{Ideas: []model.TradeIdea{}}
			if api, ok, _ := chiefFallbackAllowed(cfg, chiefE); ok {
				chiefAttemptedEngines = append(chiefAttemptedEngines, string(model.CLIApi))
				prompt, profile, pe := t.preparePrompt("thesis-chief", "chief-analyst-fallback", chiefData, api.MaxTokens)
				var rr model.Report
				if pe != nil {
					rr = model.Report{Agent: "chief-analyst-fallback", CLI: model.CLIApi, Status: model.StatusFailed, FailureKind: promptFailureKind(pe), Err: pe.Error(), Prompt: profile}
					errs = append(errs, "chief fallback: "+pe.Error())
				} else {
					rr = runAgent(ctx, model.CLIApi, "chief-analyst-fallback", string(model.StageSynthesis), prompt, cfg.Timeouts.SynthesisFallback, cfg.Retry, "", "", api)
				}
				responseCapacity(&rr, profile)
				statuses = append(statuses, reportStatus(rr))
				if e = run.WriteReport("chief-analyst-fallback", rr.Stdout); e != nil {
					return e
				}
				if rr.Status == model.StatusDone {
					if parsed, pe := parseThesisIdeas(rr.Stdout); pe == nil {
						result = parsed
						statuses[len(statuses)-1].Payload = model.OutcomeOK
						chiefAcceptedEngine = string(model.CLIApi)
						chiefModel = api.Model
					} else {
						statuses[len(statuses)-1].Payload = "invalid"
						errs = append(errs, "chief fallback: "+pe.Error())
					}
				}
			}
		}
	}
	v := verified{Universe: uni, Quant: qp, Shortlist: shortlist, Series: series, Thesis: true, Dates: map[string]bool{}, Events: map[string]time.Time{}}
	findings := validateThesisResult(result, research, v, cfg, run.TS, calendar)
	planFindings, planReports := t.reviewPlans(ctx, result, research, "initial")
	findings = append(findings, planFindings...)
	statuses = append(statuses, planReports...)
	if len(findings) > 0 && len(result.Ideas) > 0 {
		r, err := t.call(ctx, "thesis-chief", "chief-analyst-corrective", chiefData+"\nRevise or reject these unsupported constructions. Do not stretch targets.\n"+jsonText(findings)+"\nPrevious response:\n"+jsonText(result), chiefTarget(chiefE, cfg, chiefCorrective))
		statuses = append(statuses, reportStatus(r))
		if err == nil {
			if corrected, pe := parseThesisIdeas(r.Stdout); pe == nil {
				result = corrected
				findings = validateThesisResult(result, research, v, cfg, run.TS, calendar)
				pf, pr := t.reviewPlans(ctx, result, research, "corrected")
				findings = append(findings, pf...)
				statuses = append(statuses, pr...)
			} else {
				errs = append(errs, "chief corrective: "+pe.Error())
			}
		} else {
			errs = append(errs, err.Error())
		}
	}
	finalizeThesis(result, research, findings, v, cfg, run.TS)
	stages["synthesis"] = time.Since(phase).Milliseconds()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	outcome := "complete"
	for _, s := range statuses {
		// A call that returned and a call whose answer could be read are two
		// different successes. Both have to fail the run, or a shortlist that
		// lost half its dossiers to unreadable JSON reports as complete.
		if s.Status != model.StatusDone || s.Payload == "invalid" {
			errs = appendUnique(errs, s.Domain+": "+s.Err)
		}
	}
	if len(errs) > 0 {
		outcome = "degraded"
	}
	warns := []string{}
	for _, f := range findings {
		warns = append(warns, f.Message)
	}
	outcomes := make([]model.ResearchOutcome, 0, len(research))
	for i := range research {
		research[i].Outcome.Decision = decisionFor(result, research[i].Candidate.Ticker, research[i].Outcome.Decision)
		outcomes = append(outcomes, research[i].Outcome)
	}
	result.ResearchSummary = model.SummarizeResearch(outcomes, result.Decisions)
	meta := model.RunMeta{ResearchOutcomes: outcomes, SchemaVersion: 2, ResearchMode: "thesis", Research: cfg.Research, Mode: string(cfg.Mode), Ticker: cfg.Ticker, Indices: indices, GeneratedAt: result.GeneratedAt, Shortlist: shortlist, Domains: statuses, Outcome: outcome, Warnings: warns, DataErrors: errs, Duration: time.Since(start).Milliseconds(), Stages: stages, Engine: string(cfg.CheapEngine), EngineModel: cheapModelName(cfg), SynthesisModel: chiefModel, ChiefEngine: string(chiefE.CLI), ChiefModel: chiefModel, ChiefAttempted: strings.Join(chiefAttemptedEngines, ","), ChiefAccepted: chiefAcceptedEngine, PersonaSHA: reg.PersonaSHA(), PersonaSet: filepath.Base(cfg.AgentsDir)}
	for _, s := range statuses {
		if s.Domain == "chief-analyst-fallback" {
			meta.SynthesisFallbackEngine = cfg.ChiefFallback.Model
		}
	}
	coverage := marketdata.MeasureResearchCoverage(researchedEvidence)
	primaryIDs := map[string]bool{}
	for ticker, docs := range researchedEvidence {
		for _, doc := range docs {
			if marketdata.SubstantiveResearchDocument(doc) && (doc.Authority == "issuer" || doc.Authority == "depositary") {
				primaryIDs[ticker+"\x00"+doc.ID] = true
			}
		}
	}
	visible := map[string]map[string]bool{}
	for _, status := range statuses {
		if status.Attempts == 0 || status.Prompt == nil {
			continue
		}
		for ticker, ids := range status.Prompt.VisibleEvidence {
			region := marketdata.ResearchRegion(ticker)
			if visible[region] == nil {
				visible[region] = map[string]bool{}
			}
			for _, id := range ids {
				visible[region][ticker+"\x00"+id] = true
			}
		}
	}
	for i := range coverage {
		coverage[i].VisibleDocuments = len(visible[coverage[i].Region])
		coverage[i].VisiblePrimaryDocuments = 0
		for key := range visible[coverage[i].Region] {
			if primaryIDs[key] {
				coverage[i].VisiblePrimaryDocuments++
			}
		}
	}
	if e = run.WriteDataPack("research-coverage", coverage); e != nil {
		return e
	}
	if e = run.WriteIdeas(result); e != nil {
		return e
	}
	if e = run.WriteMeta(meta); e != nil {
		return e
	}
	log(ch, fmt.Sprintf("Thesis research complete: %d plans; %d decisions. %s", len(result.Ideas), len(result.Decisions), outcome))
	ch <- Event{Type: EventComplete, Ideas: result, Meta: &meta, Message: run.Dir}
	return nil
}

// decisionFor reads back the status a candidate actually ended with, so the
// per-candidate outcome record and the published board cannot disagree.
func decisionFor(res *model.IdeasResult, ticker, fallback string) string {
	for _, d := range res.Decisions {
		if d.Ticker == ticker {
			return d.Status
		}
	}
	return fallback
}

func compactEvidence(all map[string][]model.EvidenceDocument, n int) map[string][]model.EvidenceDocument {
	out := map[string][]model.EvidenceDocument{}
	for t, docs := range all {
		for i, d := range docs {
			if i >= n {
				break
			}
			original := d.Text
			d.Text = relevantPassages(d, nil, 400)
			d.SelectedSpans = selectedSpans(original, d.Text)
			d.OmittedText = d.Text != original
			d.Truncated = d.Truncated || d.OmittedText
			d.Links = nil
			out[t] = append(out[t], d)
		}
	}
	return out
}
func validatedCandidates(in, allowed []model.Candidate, max int) []model.Candidate {
	by := map[string]model.Candidate{}
	for _, c := range allowed {
		by[c.Ticker] = c
	}
	seen := map[string]bool{}
	out := []model.Candidate{}
	for _, c := range in {
		t := strings.ToUpper(strings.TrimSpace(c.Ticker))
		a, ok := by[t]
		if !ok || seen[t] || len(out) >= max {
			continue
		}
		seen[t] = true
		a.Bias = normalizeBias(c.Bias)
		a.Reason = c.Reason
		out = append(out, a)
	}
	return out
}
func (t *thesisRunner) discover(ctx context.Context, uni *universe.Universe, ps *Prescreen, indices []string) ([]model.Candidate, map[string][]model.EvidenceDocument, []model.DomainStatus, []string) {
	all := []model.Candidate{}
	rows := map[string]PrescreenRow{}
	if t.cfg.Mode == model.ModeSingle {
		ticker := strings.ToUpper(strings.TrimSpace(t.cfg.Ticker))
		c := model.Candidate{Ticker: ticker, Name: ticker}
		if u, ok := uni.Lookup(ticker); ok {
			c.Name = u.Name
			c.Index = u.Index
			c.Sector = u.Sector
		}
		all = append(all, c)
	} else if ps != nil {
		for _, r := range ps.Rows {
			if r.Excluded != "" {
				continue
			}
			if _, ok := rows[r.Ticker]; ok {
				continue
			}
			rows[r.Ticker] = r
			all = append(all, model.Candidate{Ticker: r.Ticker, Name: r.Name, Index: r.Index, Sector: r.Sector, Setup: r.Setup})
		}
	}
	news := map[string][]model.EvidenceDocument{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, t.cfg.Workers)
	// Discovery does not spend AlphaVantage's small daily budget; reserve that
	// provider for the triaged shortlist and its verified calendar.
	discovery := marketdata.NewService(marketdata.NewCache(t.cfg.DataDir), marketdata.NewAlpacaNewsProvider(t.cfg.Providers.AlpacaKeyID, t.cfg.Providers.AlpacaSecret), marketdata.NewYahooNewsProvider())
	var errs []string
	log(t.ch, fmt.Sprintf("Discovering dated news across %d eligible global names…", len(all)))
	for _, c := range all {
		wg.Add(1)
		go func(c model.Candidate) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			var pack *marketdata.DataPack
			if t.cfg.Frozen != nil {
				pack = t.cfg.Frozen.DiscoveryPack(ctx, []string{c.Ticker})
			} else {
				pack = discovery.BuildPack(ctx, "news", []string{c.Ticker})
			}
			docs := marketdata.EvidenceFromPack(pack, c.Ticker, t.run.TS)
			mu.Lock()
			news[c.Ticker] = docs
			errs = append(errs, pack.Errors...)
			mu.Unlock()
		}(c)
	}
	wg.Wait()
	if t.cfg.Mode == model.ModeSingle {
		return all, news, nil, errs
	}
	// Price lane: round-robin indices prevents one index monopolising discovery.
	sort.SliceStable(all, func(i, j int) bool { return math.Abs(rows[all[i].Ticker].Score) > math.Abs(rows[all[j].Ticker].Score) })
	priceLane := []model.Candidate{}
	queues := map[string][]model.Candidate{}
	for _, c := range all {
		r := rows[c.Ticker]
		c.Bias = model.BiasBullish
		if r.Score < 0 {
			c.Bias = model.BiasBearish
		}
		c.Reason = fmt.Sprintf("Price setup %s; composite %+.2f. Filing reactions are proxies, not verified earnings events.", r.Setup, r.Score)
		queues[c.Index] = append(queues[c.Index], c)
	}
	for len(priceLane) < t.cfg.Research.Candidates {
		added := false
		for _, idx := range indices {
			if len(queues[idx]) > 0 {
				priceLane = append(priceLane, queues[idx][0])
				queues[idx] = queues[idx][1:]
				added = true
			}
		}
		if !added {
			break
		}
	}
	var statuses []model.DomainStatus
	eventLane := []model.Candidate{}
	eventQueues := map[string][]model.Candidate{}
	for _, idx := range indices {
		eligible := []model.Candidate{}
		local := map[string][]model.EvidenceDocument{}
		for _, c := range all {
			if c.Index == idx && len(news[c.Ticker]) > 0 {
				eligible = append(eligible, c)
				local[c.Ticker] = news[c.Ticker]
			}
		}
		if len(eligible) == 0 {
			continue
		}
		var sr model.ScoutResult
		reports, _, _, e := researchCall(ctx, t, "thesis-triage", "event-discovery-"+idx, "Event discovery: select at most 12 companies with a material dated change, regardless of price ranking.\n"+jsonText(eligible)+"\n"+jsonText(compactEvidence(local, 1)), &sr, scoutSchema)
		statuses = append(statuses, reports...)
		if e != nil {
			errs = append(errs, "event discovery "+idx+": "+e.Error())
			continue
		}
		eventQueues[idx] = validatedCandidates(sr.Candidates, eligible, 12)
	}
	for i := 0; i < 12; i++ {
		for _, idx := range indices {
			if i < len(eventQueues[idx]) {
				eventLane = append(eventLane, eventQueues[idx][i])
			}
		}
	}
	// Alternate lanes; overlap frees capacity for the other lane, with no merit
	// sort after model triage. Stable round-robin ordering is persisted.
	merged := []model.Candidate{}
	seen := map[string]bool{}
	for i := 0; len(merged) < t.cfg.Research.Candidates && (i < len(priceLane) || i < len(eventLane)); i++ {
		for _, lane := range [][]model.Candidate{eventLane, priceLane} {
			if i < len(lane) && !seen[lane[i].Ticker] && len(merged) < t.cfg.Research.Candidates {
				seen[lane[i].Ticker] = true
				merged = append(merged, lane[i])
			}
		}
	}
	return merged, news, statuses, errs
}

func (t *thesisRunner) investigate(ctx context.Context, c model.Candidate, initial []model.EvidenceDocument, qp *quant.Pack, series map[string]*quant.Series) thesisResearch {
	out := thesisResearch{Candidate: c, Documents: initial}
	out.Outcome = model.ResearchOutcome{Ticker: c.Ticker, Transport: model.OutcomeOK, Parsing: model.OutcomeOK, Review: model.ReviewUnavailable}
	ticker := c.Ticker
	for _, domain := range []string{"news", "fundamentals", "sentiment"} {
		pack := t.svc.BuildPack(ctx, domain, []string{ticker})
		if domain == "fundamentals" {
			enrichFundamentals(pack, qp)
		}
		if d, ok := pack.EventDates[ticker]; ok {
			out.NextEvent = d.Format("2006-01-02")
		}
		out.Documents = append(out.Documents, marketdata.EvidenceFromPack(pack, ticker, researchTime(ctx).UTC())...)
		out.Errors = append(out.Errors, pack.Errors...)
		if domain == "news" && earningsBlocked(ticker, out.NextEvent, t.run.TS, t.calendar) {
			t.deferForEarnings(&out, qp)
			return out
		}
	}
	if t.cfg.Frozen != nil {
		for _, d := range t.cfg.Frozen.SourceEvidence(ctx, ticker) {
			out.Documents = append(out.Documents, d)
			if d.Error != "" {
				out.Errors = append(out.Errors, d.Error)
			}
		}
	}
	q := qp.CompactLine(ticker)
	out.Documents = append(out.Documents, model.EvidenceDocument{ID: marketdata.EvidenceID(ticker, "quant", q), Ticker: ticker, Kind: "computed", Title: "Verified price statistics", Text: q, RetrievedAt: t.run.TS})
	out.Documents = marketdata.DeduplicateEvidence(out.Documents)
	safeName := fmt.Sprintf("research-%x", []byte(ticker))
	read := marketdata.DocumentReader{Contact: t.cfg.Providers.ContactEmail}.Read
	filings := func(ctx context.Context, ticker string) ([]model.EvidenceDocument, error) {
		return marketdata.ResearchFilings(ctx, t.cfg.Providers.ContactEmail, marketdata.NewCache(t.cfg.DataDir), ticker)
	}
	if t.cfg.Frozen != nil {
		read = t.cfg.Frozen.ReadDocument
		filings = t.cfg.Frozen.Filings
	}
	used := 0
	passages := 0
	// Every request gets an answer, and every answer goes back into the prompt.
	// Previously an unfulfillable request produced a line in an error list the
	// model never read, so it asked again: the 2026-09-07 run spent 20 requests
	// on operations that do not exist and nine on passage searches for text that
	// was not in the document, none of which was ever reported back.
	answered := map[string]int{} // request key -> index into out.Results
	requestKey := func(r model.ResearchRequest) string {
		switch r.Kind {
		case "document":
			return r.Kind + "\x00" + marketdata.CanonicalResearchURL(r.URL) + "\x00" + r.ObservationDate
		case "filings", "news":
			return r.Kind + "\x00" + r.ObservationDate
		case "passage":
			return r.Kind + "\x00" + strings.TrimSpace(r.EvidenceID) + "\x00" + strings.TrimSpace(r.Query)
		default:
			return r.Kind + "\x00" + r.Question
		}
	}

	record := func(req model.ResearchRequest, outcome, detail string, ids ...string) {
		res := model.ResearchResult{Request: req, Outcome: outcome, Detail: detail, EvidenceIDs: ids}
		if i, seen := answered[requestKey(req)]; seen {
			out.Results[i] = res
			return
		}
		answered[requestKey(req)] = len(out.Results)
		out.Results = append(out.Results, res)
	}
	fetch := func(requests []model.ResearchRequest) {
		for _, req := range requests {
			if ctx.Err() != nil {
				return
			}
			if i, seen := answered[requestKey(req)]; seen {
				// Asking again cannot add evidence, so it costs neither budget
				// nor a provider call. Saying so is what stops the loop.
				out.Results[i].Repeats++
				continue
			}
			if req.ObservationDate != "" {
				date, err := time.Parse("2006-01-02", req.ObservationDate)
				if err != nil {
					record(req, model.RequestUnsupported, "observation_date must be YYYY-MM-DD")
					continue
				}
				if date.Add(time.Duration(marketdata.MarketCloseUTC(ticker)) * time.Hour).After(t.run.TS) {
					record(req, model.RequestUnavailable, "Requested observation is future or its session has not completed at the run anchor; use a conditional scenario or awaiting-prices watchlist.")
					continue
				}
			}
			if !model.ValidRequestKind(req.Kind) {
				kind := req.Kind
				if kind == "" {
					kind = "(omitted)"
				}
				record(req, model.RequestUnsupported, "No such operation: "+kind+". The only operations are "+strings.Join(model.ResearchRequestKinds, ", ")+".")
				continue
			}
			// A provider's full article is already source text. Reuse it before
			// charging the retrieval budget or fetching the same URL again.
			if req.Kind == "document" {
				var existing string
				var previousFailure string
				for _, d := range out.Documents {
					if req.URL != "" && marketdata.CanonicalResearchURL(d.URL) == marketdata.CanonicalResearchURL(req.URL) && d.Kind == "document" && d.Error != "" {
						previousFailure = d.Error
					}
					if req.URL != "" && marketdata.CanonicalResearchURL(d.URL) == marketdata.CanonicalResearchURL(req.URL) && d.Kind == "document" && d.ParentID == "" && d.Error == "" && len(d.Text) >= 200 {
						existing = d.ID
						break
					}
				}
				if previousFailure != "" && existing == "" {
					record(req, model.RequestAlreadyDone, "Source already failed this run: "+previousFailure)
					continue
				}
				if existing != "" {
					record(req, model.RequestAlreadyDone, "Full source text is already supplied; no additional retrieval or budget charge.", existing)
					continue
				}
			}
			// The document budget covers the operations that actually retrieve.
			// A budget-exhausted request still gets an answer rather than
			// stopping the batch: the model needs to know which of its questions
			// went unanswered, not just that some did.
			if (req.Kind == "document" || req.Kind == "filings") && used >= t.cfg.Research.Documents {
				record(req, model.RequestExhausted, fmt.Sprintf("The document budget for this company (%d attempts) is spent; no further retrieval is possible this run.", t.cfg.Research.Documents))
				out.Dossier.Unresolved = appendUnique(out.Dossier.Unresolved, "Document budget exhausted: "+req.Question)
				continue
			}
			switch req.Kind {
			case "news":
				// Explicitly not a retrieval operation. The company's news was
				// fetched before research began and is already in the evidence;
				// asking for it again returns the same facts and, in particular,
				// no newer date.
				var ids []string
				for _, d := range out.Documents {
					if d.Error == "" && (d.Kind == "headline" || d.Kind == "summary") {
						ids = append(ids, d.ID)
					}
				}
				record(req, model.RequestAlreadyDone, fmt.Sprintf("News is fetched once before research begins and performs no additional retrieval; the %d news items already in your evidence are all there are. Use document requests on their URLs to read further.", len(ids)), ids...)
			case "filings":
				used++
				docs, e := filings(ctx, ticker)
				if e != nil {
					out.Errors = append(out.Errors, e.Error())
					record(req, model.RequestUnavailable, "SEC filing index unavailable: "+e.Error())
					break
				}
				if len(docs) == 0 {
					record(req, model.RequestUnavailable, "No recent SEC filings are indexed for this issuer; a non-US issuer may have none.")
					break
				}
				var ids []string
				for _, d := range docs {
					ids = append(ids, d.ID)
				}
				out.Documents = append(out.Documents, docs...)
				record(req, model.RequestFulfilled, fmt.Sprintf("%d recent filing links added. A filing date is not an announcement time; read the release exhibit.", len(docs)), ids...)
			case "passage":
				if passages >= 2*t.cfg.Research.Documents {
					record(req, model.RequestExhausted, "The passage-extraction budget for this company is spent.")
					break
				}
				var source *model.EvidenceDocument
				for i := range out.Documents {
					if out.Documents[i].ID == req.EvidenceID && out.Documents[i].Error == "" {
						source = &out.Documents[i]
						break
					}
				}
				if source == nil {
					record(req, model.RequestUnavailable, "No readable saved document has evidence id "+req.EvidenceID+". Passage requests can only inspect a document already in your evidence.")
					break
				}
				query := req.Query
				if query == "" {
					query = req.Question
				}
				at := strings.Index(strings.ToLower(source.Text), strings.ToLower(query))
				if at < 0 {
					record(req, model.RequestUnavailable, "The literal text "+strconv.Quote(query)+" does not occur in "+source.ID+". A passage query is an exact substring search over the stored text, not a description of what to look for.")
					break
				}
				start := at - 200
				if start < 0 {
					start = 0
				}
				end := start + 5000
				if end > len(source.Text) {
					end = len(source.Text)
				}
				snippet := *source
				snippet.ParentID = source.ID
				snippet.Text = source.Text[start:end]
				snippet.Title = "Passage from " + source.ID
				snippet.ID = marketdata.EvidenceID(ticker, source.URL, snippet.Text)
				snippet.Links = nil
				passages++
				out.Documents = append(out.Documents, snippet)
				record(req, model.RequestFulfilled, "Passage extracted from "+source.ID+".", snippet.ID)
			case "document":
				allowed := false
				for _, u := range t.sources[ticker] {
					if marketdata.CanonicalResearchURL(u) == marketdata.CanonicalResearchURL(req.URL) {
						allowed = true
					}
				}
				for _, d := range out.Documents {
					if marketdata.CanonicalResearchURL(d.URL) == marketdata.CanonicalResearchURL(req.URL) {
						allowed = true
					}
					for _, u := range d.Links {
						if marketdata.CanonicalResearchURL(u) == marketdata.CanonicalResearchURL(req.URL) {
							allowed = true
						}
					}
				}
				if !allowed {
					record(req, model.RequestUnsupported, "That URL is not in your evidence. Only a URL carried by a supplied document, discovered as a link inside one, or listed under the issuer source URLs can be read.")
					break
				}
				used++
				doc := read(ctx, ticker, req.URL)
				if t.cfg.Frozen == nil {
					marketdata.MarkResearchAuthority(&doc, t.sources)
				}
				out.Documents = append(out.Documents, doc)
				if doc.Error != "" {
					out.Errors = appendUnique(out.Errors, "document "+req.URL+": "+doc.Error)
					record(req, model.RequestUnavailable, "The source did not yield readable text: "+doc.Error)
					break
				}
				record(req, model.RequestFulfilled, fmt.Sprintf("%d characters of source text added.", len(doc.Text)), doc.ID)
			}
		}
		out.Documents = marketdata.DeduplicateEvidence(out.Documents)
	}
	// Give the researcher actual source text before its first judgment whenever
	// the feed supplied a readable URL; issuer pages supply discoverable links.
	if urls := t.sources[ticker]; len(urls) > 0 {
		fetch([]model.ResearchRequest{{Kind: "document", URL: urls[0], Question: "issuer announcements"}})
	}
	initialURLs := []string{}
	for _, d := range out.Documents {
		for _, link := range d.Links {
			initialURLs = appendUnique(initialURLs, link)
		}
		if d.URL != "" && d.Kind != "document" {
			initialURLs = appendUnique(initialURLs, d.URL)
		}
	}
	issuer := ""
	if urls := t.sources[ticker]; len(urls) > 0 {
		issuer = urls[0]
	}
	for _, u := range marketdata.RankResearchLinks(initialURLs, issuer) {
		if marketdata.CanonicalResearchURL(u) == marketdata.CanonicalResearchURL(issuer) {
			continue
		}
		if used >= min(2, t.cfg.Research.Documents) {
			break
		}
		fetch([]model.ResearchRequest{{Kind: "document", URL: u, Question: "headline context"}})
	}
	base := func() string {
		out.Temporal = temporalFacts(out, t.run.TS, t.calendar, qp)
		// Answers to previous requests come before the diagnostics, because they
		// are the half the model is expected to act on: an operation reported
		// unsupported or a passage reported absent should not be asked for again.
		return "Company identity: " + jsonText(model.Candidate{Ticker: ticker, Name: c.Name, Index: c.Index, Sector: c.Sector}) +
			"\nComputed temporal facts (use these dates and units rather than counting weekdays):\n" + jsonText(out.Temporal) +
			"\nEvidence (reporting periods must survive synthesis):\n" + jsonText(promptDocumentsAt(out.Documents, 24000, t.run.TS, evidenceClaims(out.Dossier, out.Challenge.Claims...)...)) +
			"\nIssuer source URLs:\n" + jsonText(t.sources[ticker]) +
			"\nAnswers to your research requests (every request is answered; do not repeat one answered unsupported, unavailable or already attempted):\n" + jsonText(out.Results) +
			"\nDossier hash: " + dossierHash(out.Dossier) + "\nPrevious dossier:\n" + jsonText(out.Dossier) +
			"\nOriginal narratives from bounded compaction (check that no qualifications or counterarguments were lost):\n" + jsonText(compactionOriginals(out.Reports)) +
			"\nRetrieval diagnostics:\n" + jsonText(out.Errors)
	}
	for round := 0; round < t.cfg.Research.Rounds; round++ {
		reports, transport, parsing, e := researchCall(ctx, t, "thesis-researcher", fmt.Sprintf("%s-round-%d", safeName, round+1), base()+fmt.Sprintf("\nRound %d/%d; document attempts %d/%d.", round+1, t.cfg.Research.Rounds, used, t.cfg.Research.Documents), &out.Dossier, dossierSchema)
		out.addResearchReports(reports)
		out.Outcome.Parsing = worsePayload(out.Outcome.Parsing, parsing)
		if transport == model.OutcomeFailed {
			out.Outcome.Transport = model.OutcomeFailed
		}
		if e != nil {
			// A call that did not return, or a payload that would not decode
			// after its one repair attempt, is a research failure. It stops the
			// company from being tradable; it is not a finding about the
			// company, and nothing downstream may read it as one.
			out.Errors = append(out.Errors, e.Error())
			out.Outcome.Notes = appendUnique(out.Outcome.Notes, "research round "+fmt.Sprint(round+1)+" produced no usable dossier: "+e.Error())
			out.Dossier.Status = "watchlist"
			break
		}
		if len(out.Dossier.Requests) == 0 {
			break
		}
		if round+1 < t.cfg.Research.Rounds {
			fetch(out.Dossier.Requests)
		}
	}
	t.validateComparisons(ctx, &out, series)
	validateEvidenceTime(&out, t.run.TS)
	validateDossier(&out)
	addReleaseReactions(&out, qp, series, t.run.TS, t.calendar)
	challenge := func(suffix string) {
		reports, transport, parsing, e := researchCall(ctx, t, "thesis-challenger", safeName+"-challenge"+suffix, base()+"\nPrevious challenge:\n"+jsonText(out.Challenge), &out.Challenge, challengeSchema)
		out.addResearchReports(reports)
		out.Outcome.Parsing = worsePayload(out.Outcome.Parsing, parsing)
		if transport == model.OutcomeFailed {
			out.Outcome.Transport = model.OutcomeFailed
		}
		if e != nil {
			// A review that never happened is not a rejection. Recording it as
			// one is how BSX — whose research call failed outright — reached the
			// board as a company the pipeline had considered and turned down.
			out.Errors = append(out.Errors, e.Error())
			out.Outcome.Notes = appendUnique(out.Outcome.Notes, "independent challenge unavailable: "+e.Error())
			out.Challenge = model.ThesisChallenge{Ticker: ticker, Verdict: model.ReviewUnavailable, Reason: "The independent challenge did not complete; this is a research failure, not a finding about the company."}
		}
		if out.Challenge.Verdict == "supported" && len(compactionOriginals(out.Reports)) > 0 && out.Challenge.CompactionAssessment != "preserved" {
			out.Challenge.MaterialIssues = appendUnique(out.Challenge.MaterialIssues, "compacted narratives require explicit review of preserved qualifications and counterarguments")
		}
		for _, problem := range append(validateReviewConsistency(out.Dossier, out.Challenge), validateClaimsAt(out.Challenge.Claims, out.Documents, ticker, t.run.TS)...) {
			out.Challenge.MaterialIssues = appendUnique(out.Challenge.MaterialIssues, problem)
		}
	}
	// A challenge needs a dossier to challenge. When the research loop produced
	// none — the call never returned, or its payload would not decode even after
	// the repair attempt — reviewing it spends two more calls to rediscover that
	// there is nothing there, and risks recording the emptiness as an adverse
	// verdict. Say plainly that the review did not happen.
	if out.Outcome.Transport == model.OutcomeFailed || out.Outcome.Parsing == model.OutcomeFailed || out.Outcome.Contract == model.OutcomeFailed {
		out.Challenge = model.ThesisChallenge{Ticker: ticker, Verdict: model.ReviewUnavailable, Reason: "No independent challenge was run: the research loop produced no readable dossier to review."}
		out.Outcome.Notes = appendUnique(out.Outcome.Notes, "independent challenge skipped: no dossier to review")
	} else {
		challenge("")
	}
	if out.Challenge.Verdict == "revise" || len(out.Challenge.MaterialIssues) > 0 || len(out.Challenge.Requests) > 0 {
		fetch(out.Challenge.Requests)
		reports, transport, parsing, e := researchCall(ctx, t, "thesis-researcher", safeName+"-revision", base()+"\nFinal revision: resolve these issues or stand down. No further retrieval rounds.\n"+jsonText(out.Challenge), &out.Dossier, dossierSchema)
		out.addResearchReports(reports)
		out.Outcome.Parsing = worsePayload(out.Outcome.Parsing, parsing)
		if transport == model.OutcomeFailed {
			out.Outcome.Transport = model.OutcomeFailed
		}
		if e != nil {
			out.Errors = append(out.Errors, e.Error())
			out.Outcome.Notes = appendUnique(out.Outcome.Notes, "revision produced no usable dossier: "+e.Error())
			out.Dossier.Status = "watchlist"
		}
		t.validateComparisons(ctx, &out, series)
		validateEvidenceTime(&out, t.run.TS)
		validateDossier(&out)
		addReleaseReactions(&out, qp, series, t.run.TS, t.calendar)
		if e == nil {
			challenge("-final")
		} else {
			out.Challenge = model.ThesisChallenge{Ticker: ticker, Verdict: model.ReviewUnavailable, Reason: "Final challenge skipped: revision failed"}
		}
	}
	validateDossier(&out)
	out.Outcome.Evidence = classifyEvidence(out.Documents)
	for _, problem := range validateReviewConsistency(out.Dossier, out.Challenge) {
		out.Challenge.MaterialIssues = appendUnique(out.Challenge.MaterialIssues, problem)
	}
	out.Outcome.Review = out.Challenge.Verdict
	if out.Challenge.Ticker != ticker || out.Challenge.Verdict != "supported" || len(out.Challenge.MaterialIssues) > 0 || len(out.Challenge.Requests) > 0 || !reviewHasClaims(out.Challenge) || len(validateClaimsAt(out.Challenge.Claims, out.Documents, ticker, t.run.TS)) > 0 || len(validateClaimPassages(out.Challenge.Claims, out.Documents)) > 0 {
		out.Dossier.Status = "watchlist"
	}
	// Only a review that actually happened and actually said "reject" is a
	// rejection. An unavailable challenge leaves the company on the watchlist
	// with the reason recorded as a research failure.
	if out.Challenge.Verdict == "reject" {
		out.Dossier.Status = "rejected"
	}
	if out.Outcome.Transport == model.OutcomeFailed || out.Outcome.Parsing == model.OutcomeFailed || out.Outcome.Contract == model.OutcomeFailed || out.Outcome.Review == model.ReviewUnavailable {
		out.Dossier.Status = "watchlist"
	}
	out.Outcome.Decision = out.Dossier.Status
	out.Temporal = temporalFacts(out, t.run.TS, t.calendar, qp)
	if e := t.run.WriteDataPack(safeName, out); e != nil {
		out.Errors = append(out.Errors, e.Error())
		out.Outcome.Notes = appendUnique(out.Outcome.Notes, "evidence artifact not persisted: "+e.Error())
		out.Dossier.Status = "watchlist"
	}
	out.Outcome.Decision = out.Dossier.Status
	return out
}

// researchFailed reports whether this candidate never produced usable research,
// as opposed to producing research that was examined and found wanting. The
// distinction decides whether a decision may say "rejected".
func (r thesisResearch) researchFailed() bool {
	return r.Outcome.Transport == model.OutcomeFailed ||
		r.Outcome.Parsing == model.OutcomeFailed ||
		r.Outcome.Contract == model.OutcomeFailed ||
		r.Outcome.Review == model.ReviewUnavailable ||
		r.Outcome.Evidence == model.EvidenceNone
}

// Keep imports and artifacts deliberately local; never save credentials or raw
// prompts containing engine settings. Only evidence, decisions and reports persist.

func (t *thesisRunner) reviewPlans(ctx context.Context, res *model.IdeasResult, research []thesisResearch, phase string) ([]riskFinding, []model.DomainStatus) {
	var findings []riskFinding
	var reports []model.DomainStatus
	by := map[string]thesisResearch{}
	for _, r := range research {
		by[r.Candidate.Ticker] = r
	}
	for _, idea := range res.Ideas {
		r, ok := by[idea.Ticker]
		if !ok {
			continue
		}
		data := "Plan hash: " + executionPlanHash(idea) + "\nReview this final execution plan, especially whether evidence supports its target and outcome range in 10–15 sessions. Reject rule-driven target stretching. You are reviewing feasibility, not forecasting probability.\n" + jsonText(idea) + "\nComputed temporal facts:\n" + jsonText(r.Temporal) + "\nDossier hash: " + dossierHash(r.Dossier) + "\nDossier:\n" + jsonText(r.Dossier) + "\nEvidence:\n" + jsonText(promptDocumentsAt(r.Documents, 24000, t.run.TS, evidenceClaims(r.Dossier, r.Challenge.Claims...)...))
		var challenge model.ThesisChallenge
		rs, _, _, e := researchCall(ctx, t, "thesis-challenger", fmt.Sprintf("plan-review-%x-%s", []byte(idea.Ticker), phase), data, &challenge, challengeSchema)
		reports = append(reports, rs...)
		challenge.MaterialIssues = append(challenge.MaterialIssues, validateReviewConsistency(r.Dossier, challenge)...)
		if r.Dossier.ContractVersion == 2 && (challenge.PlanHash != executionPlanHash(idea) || challenge.TargetAssessment != "supported") {
			challenge.MaterialIssues = append(challenge.MaterialIssues, "execution review must support target provenance and match the supplied plan hash")
		}
		if e != nil || challenge.Ticker != idea.Ticker || challenge.Verdict != "supported" || len(challenge.MaterialIssues) > 0 || len(challenge.Requests) > 0 || !reviewHasClaims(challenge) || len(validateClaimsAt(challenge.Claims, r.Documents, idea.Ticker, t.run.TS)) > 0 || len(validateClaimPassages(challenge.Claims, r.Documents)) > 0 {
			reason := challenge.Reason
			blocked := model.BlockedReviewReject
			if e != nil {
				// The review did not happen. The plan still cannot ship without
				// one, but the reason it cannot is a pipeline failure, and the
				// decision has to say that rather than imply a verdict.
				reason = "the independent plan review did not complete (" + e.Error() + ")"
				blocked = model.BlockedResearchFailure
			}
			if reason == "" {
				reason = "plan lacks a supported independent review"
			}
			findings = append(findings, riskFinding{Ticker: idea.Ticker, Hard: true, Blocked: blocked, Message: idea.Ticker + ": execution challenge: " + reason})
		}
	}
	return findings, reports
}
