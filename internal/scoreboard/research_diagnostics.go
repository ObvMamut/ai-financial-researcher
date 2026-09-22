package scoreboard

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

type ResearchUsage struct {
	IncompleteAttempts int `json:"attempts_with_incomplete_usage"`
	Attempts           int `json:"attempts"`
	CompleteAttempts   int `json:"complete_attempts"`
	PromptTokens       int `json:"reported_prompt_tokens"`
	CompletionTokens   int `json:"reported_completion_tokens"`
	TotalTokens        int `json:"reported_or_derived_total_tokens"`
}

type ResearchRunDiagnostics struct {
	SourceRegions map[string]map[string]int `json:"source_reasons_by_region,omitempty"`
	// StageProgress separates completed research contracts from independent reviews.
	// The older completed_research field retains its reviewed-workflow definition.
	StageProgress       *model.ResearchSummary   `json:"stage_progress,omitempty"`
	SourceDiagnostics   []model.SourceDiagnostic `json:"source_diagnostics,omitempty"`
	SourceReasons       map[string]int           `json:"source_reasons,omitempty"`
	LogicalCalls        int                      `json:"logical_calls"`
	AttemptedCompanies  int                      `json:"attempted_companies"`
	DeferredCompanies   int                      `json:"deferred_companies"`
	CompletedResearch   int                      `json:"completed_research"`
	FailedResearch      int                      `json:"failed_research"`
	TruncatedCalls      int                      `json:"truncated_calls"`
	TruncatedAttempts   int                      `json:"truncated_attempts"`
	InferredTruncations int                      `json:"historically_inferred_truncated_calls"`
	CapacityFailures    int                      `json:"capacity_failures"`
	RecoveryAttempts    int                      `json:"recovery_attempts"`
	// DispatchedAttempts sums DomainStatus.Attempts (subprocess attempts Go
	// actually made) across every logical call. A zero-attempt call (input
	// capacity rejected the prompt before a subprocess ever ran) is a logical
	// call that never dispatched, so this is always <= LogicalCalls and the gap
	// between them is exactly the zero-attempt failures below.
	DispatchedAttempts int `json:"dispatched_attempts"`
	// CompactionCalls and SchemaRepairs split DomainStatus.Recovery's two
	// values into separate counts. RecoveryAttempts already merges them (any
	// recovery with attempts>0); this is the September 15 audit's own split,
	// since the plan's later tasks change compaction behavior specifically and
	// must not be graded against schema-repair calls they never touch.
	CompactionCalls int `json:"compaction_calls"`
	SchemaRepairs   int `json:"schema_repairs"`
	// InputCapacityFailures counts calls that never dispatched a subprocess
	// because the assembled prompt itself exceeded the input byte budget
	// (DomainStatus.FailureKind == "input_capacity" with zero attempts). These
	// are distinct from a response that dispatched and then overflowed on the
	// way back (CapacityFailures / response_capacity): nothing here can be
	// recovered by compacting a reply, because no reply was ever requested.
	InputCapacityFailures     int                       `json:"input_capacity_failures"`
	ResearchOutcomesAvailable bool                      `json:"research_outcomes_available"`
	Requests                  int                       `json:"request_results"`
	RequestRepeats            int                       `json:"request_repeats"`
	RequestOutcomes           map[string]int            `json:"request_outcomes"`
	StageUsage                map[string]*ResearchUsage `json:"stage_usage"`
	PrimaryChiefFailed        bool                      `json:"primary_chief_failed"`
	FallbackSucceeded         bool                      `json:"fallback_succeeded"`

	Run                string                                   `json:"run"`
	ResearchMode       string                                   `json:"research_mode"`
	Cohort             string                                   `json:"cohort"`
	GeneratedAt        string                                   `json:"generated_at"`
	Outcome            string                                   `json:"outcome"`
	Result             string                                   `json:"result"`
	MetadataAvailable  bool                                     `json:"metadata_available"`
	Empty              bool                                     `json:"empty"`
	DurationMS         int64                                    `json:"duration_ms"`
	FailedCalls        int                                      `json:"failed_calls"`
	InvalidPayloads    int                                      `json:"invalid_payloads"`
	RepairedPayloads   int                                      `json:"repaired_payloads"`
	DataErrors         int                                      `json:"data_errors"`
	DistinctDataErrors int                                      `json:"distinct_data_errors"`
	Usage              ResearchUsage                            `json:"usage"`
	Coverage           map[string][]marketdata.ResearchCoverage `json:"coverage"`
	ArtifactIssues     []string                                 `json:"artifact_issues,omitempty"`
	MeasurementIssues  []string                                 `json:"measurement_issues,omitempty"`
	Arms               map[string]ControlArm                    `json:"arms"`
	Execution          []ResearchExecution                      `json:"execution_scenario,omitempty"`

	// CompletedReviews counts researched candidates whose independent challenge
	// actually rendered a verdict (supported, revise or reject) rather than
	// "unavailable" or "not_run". It is the numerator CompletedResearch already
	// requires as one of several conditions; recorded on its own because a run
	// can complete research contracts without ever reaching challenge (a
	// capacity failure downstream of a usable dossier), and the September 15
	// audit needed that fact isolated rather than folded into one pass/fail bit.
	CompletedReviews int `json:"completed_reviews"`
}

type ResearchExecution struct {
	Ticker           string   `json:"ticker"`
	Outcome          Outcome  `json:"outcome"`
	Conditional      bool     `json:"conditional"`
	RoundTripCostBPS float64  `json:"assumed_round_trip_cost_bps"`
	FillWindowDays   int      `json:"fill_window_days"`
	GrossReturn      *float64 `json:"gross_return_pct,omitempty"`
	NetReturn        *float64 `json:"scenario_net_return_pct,omitempty"`
}

func researchDiagnostics(r store.RunSummary, ideas *model.IdeasResult, resultErr error, meta *model.RunMeta, metaErr error) ResearchRunDiagnostics {
	d := ResearchRunDiagnostics{Run: r.Name, GeneratedAt: r.GeneratedAt, Outcome: r.Outcome, Result: "ok", Arms: map[string]ControlArm{}, Coverage: map[string][]marketdata.ResearchCoverage{}, StageUsage: map[string]*ResearchUsage{}, RequestOutcomes: map[string]int{}}
	if resultErr != nil || ideas == nil || ideas.Ideas == nil {
		d.Result = "invalid"
		if os.IsNotExist(resultErr) {
			d.Result = "missing"
		}
		d.ArtifactIssues = append(d.ArtifactIssues, "ideas.json: "+d.Result)
	} else {
		d.Empty = len(ideas.Ideas) == 0
		d.GeneratedAt = ideas.GeneratedAt
	}
	if ideas != nil {
		d.ResearchMode = ideas.ResearchMode
	}
	if d.ResearchMode == "" && meta != nil {
		d.ResearchMode = meta.ResearchMode
	}
	if d.ResearchMode == "" {
		d.ResearchMode = "legacy"
	}
	if metaErr == nil && meta != nil {
		d.MetadataAvailable = true
		d.DurationMS = meta.Duration
		d.Outcome = meta.Outcome
		d.DataErrors = len(meta.DataErrors)
		d.SourceDiagnostics = append([]model.SourceDiagnostic(nil), meta.SourceDiagnostics...)
		d.SourceReasons = map[string]int{}
		d.SourceRegions = map[string]map[string]int{}
		seenDiagnostics := map[string]bool{}
		for _, item := range meta.SourceDiagnostics {
			if !seenDiagnostics[item.ID] {
				d.SourceReasons[item.Reason]++
				region := item.Region
				if region == "" {
					region = "global"
				}
				if d.SourceRegions[region] == nil {
					d.SourceRegions[region] = map[string]int{}
				}
				d.SourceRegions[region][item.Reason]++
				seenDiagnostics[item.ID] = true
			}
		}
		distinct := map[string]bool{}
		for _, e := range meta.DataErrors {
			distinct[e] = true
		}
		d.DistinctDataErrors = len(distinct)
		d.LogicalCalls = len(meta.Domains)
		d.ResearchOutcomesAvailable = meta.ResearchOutcomes != nil
		var decisions []model.SelectionDecision
		if ideas != nil {
			decisions = ideas.Decisions
		}
		d.StageProgress = model.SummarizeResearch(meta.ResearchOutcomes, decisions)
		for _, o := range meta.ResearchOutcomes {
			if o.Transport == model.OutcomeNotRun {
				d.DeferredCompanies++
				continue
			}
			d.AttemptedCompanies++
			reviewed := o.Review != "" && o.Review != model.ReviewUnavailable && o.Review != model.OutcomeNotRun
			if o.Contract != model.OutcomeFailed && o.Evidence != model.EvidenceNone && o.Transport == model.OutcomeOK && (o.Parsing == model.OutcomeOK || o.Parsing == model.OutcomeRepaired) && reviewed {
				d.CompletedResearch++
			} else {
				d.FailedResearch++
			}
			if reviewed {
				d.CompletedReviews++
			}
		}
		for _, call := range meta.Domains {
			stage := researchDiagnosticStage(call.Domain)
			if d.StageUsage[stage] == nil {
				d.StageUsage[stage] = &ResearchUsage{}
			}
			d.StageUsage[stage].add(call)
			d.DispatchedAttempts += call.Attempts
			switch call.Recovery {
			case "compaction":
				d.CompactionCalls++
			case "schema_repair":
				d.SchemaRepairs++
			}
			if call.Attempts == 0 && call.FailureKind == "input_capacity" {
				d.InputCapacityFailures++
			}
			truncated := call.FailureKind == "output_limit"
			n := 0
			for _, u := range call.Usage {
				if u.FinishReason == "length" {
					n++
				}
			}
			if !truncated && call.FailureKind == "" && strings.Contains(call.Err, "response truncated at") {
				truncated = true
				d.InferredTruncations++
				n = call.Attempts
			}
			if truncated {
				d.TruncatedCalls++
				d.TruncatedAttempts += n
			}
			if call.FailureKind == "input_capacity" || call.FailureKind == "response_capacity" {
				d.CapacityFailures++
			}
			if call.Domain == "chief-analyst" && (call.Status == model.StatusFailed || call.Payload == "invalid") {
				d.PrimaryChiefFailed = true
			}
			if call.Domain == "chief-analyst-fallback" && call.Status == model.StatusDone && call.Payload != "invalid" {
				d.FallbackSucceeded = true
			}

			if call.Status == model.StatusFailed {
				d.FailedCalls++
			}
			if call.Payload == "invalid" {
				d.InvalidPayloads++
			}
			if call.Payload == model.OutcomeRepaired {
				d.RepairedPayloads++
			}
			if call.Recovery != "" && call.Attempts > 0 {
				d.RecoveryAttempts++
			}
			d.Usage.add(call)
		}
	} else {
		d.ArtifactIssues = append(d.ArtifactIssues, "metadata.json unavailable")
	}
	for _, stage := range []string{"discovery", "research"} {
		name := stage + "-coverage.json"
		b, err := os.ReadFile(filepath.Join(r.Dir, "data", name))
		var coverage []marketdata.ResearchCoverage
		if err != nil {
			d.ArtifactIssues = append(d.ArtifactIssues, name+" unavailable")
			continue
		}
		if json.Unmarshal(b, &coverage) != nil || coverage == nil {
			d.ArtifactIssues = append(d.ArtifactIssues, name+" invalid")
			continue
		}
		d.Coverage[stage] = coverage
	}
	files, err := filepath.Glob(filepath.Join(r.Dir, "data", "research-*.json"))
	if err != nil {
		d.ArtifactIssues = append(d.ArtifactIssues, "research request artifacts: "+err.Error())
	}
	for _, path := range files {
		if strings.HasSuffix(path, "research-coverage.json") {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			d.ArtifactIssues = append(d.ArtifactIssues, filepath.Base(path)+": unreadable")
			continue
		}
		var record struct {
			Candidate model.Candidate        `json:"candidate"`
			Results   []model.ResearchResult `json:"results"`
		}
		if err = json.Unmarshal(b, &record); err != nil || record.Candidate.Ticker == "" {
			continue
		}
		for _, r := range record.Results {
			d.Requests++
			d.RequestRepeats += r.Repeats
			d.RequestOutcomes[r.Outcome]++
		}
	}
	return d
}

func researchDiagnosticStage(name string) string {
	switch {
	case strings.Contains(name, "fallback"):
		return "chief_fallback"
	case strings.HasPrefix(name, "chief-"):
		return "chief"
	case name == "macro":
		return "macro"
	case strings.HasPrefix(name, "event-discovery") || strings.Contains(name, "triage"):
		return "discovery"
	case strings.Contains(name, "plan-review"):
		return "plan_review"
	case strings.Contains(name, "challenge"):
		return "challenge"
	default:
		return "research"
	}
}

func (u *ResearchUsage) add(d model.DomainStatus) {
	defer func() { u.IncompleteAttempts = max(0, u.Attempts-u.CompleteAttempts) }()
	attempts := max(d.Attempts, len(d.Usage))
	if attempts == 0 && (d.Status == model.StatusDone || d.Tokens > 0) {
		attempts = 1
	}
	u.Attempts += attempts
	if len(d.Usage) == 0 {
		u.CompletionTokens += max(0, d.Tokens)
		return
	}
	for _, a := range d.Usage {
		prompt, completion := a.PromptTokens != nil && *a.PromptTokens >= 0, a.CompletionTokens != nil && *a.CompletionTokens >= 0
		if prompt {
			u.PromptTokens += *a.PromptTokens
		}
		if completion {
			u.CompletionTokens += *a.CompletionTokens
		}
		if prompt && completion {
			total := *a.PromptTokens + *a.CompletionTokens
			if !a.Incomplete && (a.TotalTokens == nil || *a.TotalTokens == total) {
				u.CompleteAttempts++
			}
			u.TotalTokens += total
		} else if a.TotalTokens != nil && *a.TotalTokens >= 0 {
			u.TotalTokens += *a.TotalTokens
		}
	}
}

func summarizeResearchArm(a ControlArm) ControlArm {
	entries, n := Dedupe(a.Entries, DefaultDedupeWindowDays)
	a.Duplicates = n
	a.Entries = entries
	a.Pending = 0
	acc := &horizonAcc{}
	for _, e := range entries {
		if e.CallDone {
			acc.add(e.CallPnLPct, e.CallExcessPct)
		} else {
			a.Pending++
		}
	}
	a.Record = acc.record()
	a.Overlapping = overlappingResearchCalls(entries)
	return a
}

func overlappingResearchCalls(entries []Entry) int {
	ordered := append([]Entry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].GeneratedAt < ordered[j].GeneratedAt })
	ends := map[string]string{}
	overlap := 0
	for _, e := range ordered {
		key := strings.ToUpper(e.Ticker) // Opposite directions still share the same underlying exposure.
		start := dateOf(e.GeneratedAt)
		if ends[key] >= start && start != "" {
			overlap++
		}
		end := e.CallEndDate
		if end == "" {
			if at, err := time.Parse(time.RFC3339, e.GeneratedAt); err == nil {
				end, _ = (marketdata.ResearchCalendar{}).SessionDate(e.Ticker, at, e.TimeframeDays)
			}
		}
		if end > ends[key] {
			ends[key] = end
		}
	}
	return overlap
}

// Research horizons are calendar sessions, not positions in a possibly gapped
// provider response. Only completed sessions can be admitted to the experiment.
func measureResearchCall(ctx context.Context, cache *seriesCache, r store.RunSummary, generatedAt string, c call, horizon int, asOf time.Time) (Entry, callState) {
	e := Entry{RunName: r.Name, GeneratedAt: generatedAt, Ticker: c.ticker, Index: c.index, Direction: string(c.direction), Confidence: c.confidence, PriceAtGen: c.anchor, TimeframeDays: horizon}
	fail := func(reason string) (Entry, callState) { e.Err = reason; return e, callUnmeasurable }
	generated, err := time.Parse(time.RFC3339, generatedAt)
	if err != nil || horizon <= 0 || (c.direction != model.DirectionBuy && c.direction != model.DirectionSell) {
		return fail("invalid generation timestamp, direction or horizon")
	}
	if generated.After(asOf) {
		return fail("run was generated after evaluation time")
	}
	dates, estimated := (marketdata.ResearchCalendar{}).Sessions(c.ticker, generated, horizon)
	if estimated {
		return fail("verified exchange calendar unavailable for the evaluation window")
	}
	e.CallEndDate = dates[len(dates)-1]
	if !researchSessionCompleted(c.ticker, e.CallEndDate, asOf) {
		return e, callPending
	}
	s, err := cache.get(ctx, c.ticker, r.Dir)
	if err != nil || s == nil || len(s.Bars) == 0 {
		return fail("matured horizon has no price history")
	}
	if c.anchor <= 0 {
		// Only use closes available when the call was made. A current intraday
		// generation must not acquire today's eventual closing price as anchor.
		c.anchor = closeOnOrBefore(researchCompletedSeries(s, c.ticker, generated), generated.UTC().Format("2006-01-02"))
		e.PriceAtGen = c.anchor
	}
	if c.anchor <= 0 || math.IsNaN(c.anchor) || math.IsInf(c.anchor, 0) {
		return fail("no valid generation anchor price")
	}
	var end quant.Bar
	for _, date := range dates {
		i := sort.Search(len(s.Bars), func(i int) bool { return s.Bars[i].Date >= date })
		if i >= len(s.Bars) || s.Bars[i].Date != date || s.Bars[i].Close <= 0 || math.IsNaN(s.Bars[i].Close) || math.IsInf(s.Bars[i].Close, 0) {
			return fail("matured horizon is missing a valid session bar: " + date)
		}
		end = s.Bars[i]
	}
	e.CallDone = true
	e.CallPnLPct = pnl(c.direction, c.anchor, end.Close)
	return e, callScored
}

func researchSessionCompleted(ticker, date string, asOf time.Time) bool {
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return false
	}
	// Use the provider publication cutoff, which conservatively includes the
	// closing auction and daily-bar settlement. It also handles DST safely.
	return !asOf.Before(day.Add(time.Duration(marketdata.MarketCloseUTC(ticker)) * time.Hour))
}

func researchCompletedSeries(s *quant.Series, ticker string, asOf time.Time) *quant.Series {
	if s == nil {
		return nil
	}
	out := *s
	out.Bars = nil
	for _, b := range s.Bars {
		if researchSessionCompleted(ticker, b.Date, asOf) {
			out.Bars = append(out.Bars, b)
		}
	}
	return &out
}

// Use the stock's actual anchor date (including weekends and market holidays),
// then require exact benchmark dates. Advancing the benchmark to Monday while
// pricing the stock from Friday mixes two different return windows.
func researchBenchmarkReturn(ctx context.Context, cache *seriesCache, dir, ticker string, bench *quant.Series, generated, end string, price float64) (float64, bool) {
	stock, err := cache.get(ctx, ticker, dir)
	if err != nil || stock == nil {
		return 0, false
	}
	if at, err := time.Parse(time.RFC3339, generated); err == nil {
		stock = researchCompletedSeries(stock, ticker, at)
		generated = at.UTC().Format("2006-01-02")
	} else if _, err := time.Parse("2006-01-02", generated); err != nil {
		return 0, false
	}
	i := sort.Search(len(stock.Bars), func(i int) bool { return stock.Bars[i].Date > generated })
	if i == 0 {
		return 0, false
	}
	anchor := stock.Bars[i-1].Date
	var pack quant.Pack
	if have, err := store.ReadQuantPack(dir, &pack); err != nil {
		return 0, false
	} else if have {
		if m, ok := pack.ByTicker[ticker]; ok && m.AsOf != "" {
			if m.AsOf > generated {
				return 0, false
			}
			anchor = m.AsOf
		}
	}
	j := sort.Search(len(stock.Bars), func(j int) bool { return stock.Bars[j].Date >= anchor })
	if j >= len(stock.Bars) || stock.Bars[j].Date != anchor || math.Abs(stock.Bars[j].Close-price) > math.Max(.011, math.Abs(price)*1e-6) {
		return 0, false
	}
	at := func(date string) (float64, bool) {
		j := sort.Search(len(bench.Bars), func(j int) bool { return bench.Bars[j].Date >= date })
		if j >= len(bench.Bars) || bench.Bars[j].Date != date || bench.Bars[j].Close <= 0 || math.IsNaN(bench.Bars[j].Close) || math.IsInf(bench.Bars[j].Close, 0) {
			return 0, false
		}
		return bench.Bars[j].Close, true
	}
	a, ok := at(anchor)
	b, ok2 := at(end)
	if !ok || !ok2 {
		return 0, false
	}
	return b/a - 1, true
}

func researchExecution(ctx context.Context, cache *seriesCache, r store.RunSummary, ideas *model.IdeasResult, cost float64, fillWindow int) []ResearchExecution {
	return researchExecutionAt(ctx, cache, r, ideas, cost, fillWindow, time.Now())
}

func researchExecutionAt(ctx context.Context, cache *seriesCache, r store.RunSummary, ideas *model.IdeasResult, cost float64, fillWindow int, asOf time.Time) []ResearchExecution {
	var out []ResearchExecution
	for _, idea := range ideas.Ideas {
		if ctx.Err() != nil {
			break
		}
		// Do not mutate the shared cache: ordinary replay and directional scoring
		// have independent contracts. Prevent saved-price fallback from restoring
		// future bars when the filtered series is empty or unavailable.
		completed := &seriesCache{bySymbol: map[string]*quant.Series{}, byRun: map[string]*quant.Series{}}
		for _, symbol := range []string{idea.Ticker, benchmarkFor(idea.Index, idea.Ticker)} {
			s, _ := cache.get(ctx, symbol, r.Dir)
			filtered := researchCompletedSeries(s, symbol, asOf)
			key := strings.ToUpper(symbol)
			completed.bySymbol[key] = filtered
			completed.byRun[key+"\x00"+r.Dir] = filtered
		}
		e := replayIdea(ctx, r, ideas.GeneratedAt, idea, completed, fillWindow)
		row := ResearchExecution{Ticker: idea.Ticker, Outcome: e.Outcome, Conditional: idea.Status == "conditional", RoundTripCostBPS: cost, FillWindowDays: fillWindow}
		if e.Outcome.closed() {
			gross := e.PnLPct
			net := round2(gross - cost/100)
			row.GrossReturn = &gross
			row.NetReturn = &net
		}
		out = append(out, row)
	}
	return out
}
