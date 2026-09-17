package scoreboard

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// ResearchComparison separates engine/schema/persona vintages and always reports
// both horizons. This is observational comparison, not a paired experiment:
// historical runs that did not snapshot sources cannot be reconstructed.
type ResearchComparison struct {
	AsOf    string                   `json:"as_of"`
	Cohorts []ResearchCohort         `json:"cohorts"`
	Runs    []ResearchRunDiagnostics `json:"runs"`
	Pairs   []ResearchPairAudit      `json:"pairs,omitempty"`
	Notes   string                   `json:"notes"`
}
type ResearchCohort struct {
	Key              string                `json:"key"`
	Runs             int                   `json:"runs"`
	FailedResults    int                   `json:"failed_results"`
	DegradedRuns     int                   `json:"degraded_runs"`
	EmptyRuns        int                   `json:"empty_runs"`
	ConditionalPlans int                   `json:"conditional_plans"`
	DurationMS       int64                 `json:"total_duration_ms"`
	CompletionTokens int                   `json:"completion_tokens"`
	Usage            ResearchUsage         `json:"usage"`
	Arms             map[string]ControlArm `json:"arms"`
}

type ResearchComparisonOptions struct {
	AsOf             time.Time
	PairManifest     string
	RoundTripCostBPS *float64
	FillWindowDays   int
}

func CompareResearch(ctx context.Context, dir string, prices marketdata.PriceSource) (*ResearchComparison, error) {
	return CompareResearchWithOptions(ctx, dir, prices, ResearchComparisonOptions{})
}

func CompareResearchWithOptions(ctx context.Context, dir string, prices marketdata.PriceSource, options ResearchComparisonOptions) (*ResearchComparison, error) {
	if options.AsOf.IsZero() {
		options.AsOf = time.Now()
	}
	if options.FillWindowDays <= 0 {
		options.FillWindowDays = DefaultFillWindowDays
	}
	if options.RoundTripCostBPS != nil && (*options.RoundTripCostBPS < 0 || math.IsNaN(*options.RoundTripCostBPS) || math.IsInf(*options.RoundTripCostBPS, 0)) {
		return nil, fmt.Errorf("research cost must be finite and nonnegative")
	}
	var manifest []ResearchPairSpec
	if options.PairManifest != "" {
		var err error
		manifest, err = loadResearchPairs(options.PairManifest)
		if err != nil {
			return nil, err
		}
	}
	result := &ResearchComparison{AsOf: options.AsOf.UTC().Format(time.RFC3339), Runs: []ResearchRunDiagnostics{}, Cohorts: []ResearchCohort{}}
	runs, e := store.ListRuns(dir)
	if e != nil {
		return nil, e
	}
	cache := &seriesCache{yc: prices, bySymbol: map[string]*quant.Series{}, byRun: map[string]*quant.Series{}}
	cohorts := map[string]*ResearchCohort{}
	for _, r := range runs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ideas, resultErr := store.LoadIdeas(r.Dir)
		meta, metaErr := store.LoadMeta(r.Dir)
		diag := researchDiagnostics(r, ideas, resultErr, meta, metaErr)
		if ideas == nil || resultErr != nil {
			ideas = &model.IdeasResult{ResearchMode: "unknown"}
			if meta != nil {
				ideas.ResearchMode = meta.ResearchMode
				ideas.SchemaVersion = meta.SchemaVersion
			}
		}
		validResult := diag.Result == "ok"
		mode := ideas.ResearchMode
		if mode == "" {
			mode = "legacy"
		}
		key := fmt.Sprintf("%s/schema-%d", mode, ideas.SchemaVersion)
		if meta != nil {
			// chiefEngineLabel is folded in separately from SynthesisModel,
			// which already usually does this job for free once Task 6 fixes
			// its value: a claude-Chief run and an api-Chief run normally
			// carry different model names and so land in different cohorts
			// without any extra key material. The case that string equality
			// alone cannot resolve is a historical run recorded before
			// ChiefEngine existed: it can carry the exact same SynthesisModel
			// value ("opus") as a new, explicitly-confirmed claude run, but
			// that historical value is not trustworthy provenance — see
			// testdata/research-sep15.json, which recorded "opus" while the
			// DeepSeek fallback actually produced the accepted output. Pooling
			// that kind of run with a confidently-labeled new "claude" cohort
			// would silently attribute possibly-fallback-answered results to
			// Claude, so an absent ChiefEngine gets its own label rather than
			// merging into either.
			chiefEngineLabel := meta.ChiefEngine
			if chiefEngineLabel == "" {
				chiefEngineLabel = "claude (unrecorded)"
			}
			key += "/" + meta.Engine + ":" + meta.EngineModel + "/" + meta.SynthesisModel + "/chief:" + chiefEngineLabel + "/" + personaKey(meta)
			if meta.SynthesisFallbackEngine != "" {
				key += "/fallback:" + meta.SynthesisFallbackEngine
			}
		}
		c := cohorts[key]
		if c == nil {
			c = &ResearchCohort{Key: key, Arms: map[string]ControlArm{}}
			cohorts[key] = c
		}
		diag.Cohort = key
		c.Runs++
		if !validResult {
			c.FailedResults++
		}
		if meta != nil && meta.Outcome == "degraded" {
			c.DegradedRuns++
		}
		if validResult && len(ideas.Ideas) == 0 {
			c.EmptyRuns++
		}
		if meta != nil {
			c.DurationMS += meta.Duration
		}
		c.Usage.Attempts += diag.Usage.Attempts
		c.Usage.CompleteAttempts += diag.Usage.CompleteAttempts
		c.Usage.PromptTokens += diag.Usage.PromptTokens
		c.Usage.CompletionTokens += diag.Usage.CompletionTokens
		c.Usage.TotalTokens += diag.Usage.TotalTokens
		c.CompletionTokens = c.Usage.CompletionTokens
		for _, i := range ideas.Ideas {
			if i.Status == "conditional" {
				c.ConditionalPlans++
			}
		}
		if !validResult {
			result.Runs = append(result.Runs, diag)
			continue
		}
		var ps prescreenFile
		have, _ := store.ReadPrescreen(r.Dir, &ps)
		for _, h := range []int{10, 15} {
			for _, arm := range []string{"shipped", "composite", "shortlist"} {
				calls := shippedCalls(ideas.Ideas)
				if arm == "composite" {
					if !have {
						continue
					}
					calls = compositeCalls(ps.Rows, 5)
				}
				if arm == "shortlist" {
					if meta == nil {
						continue
					}
					calls = shortlistCalls(meta.Shortlist, ps.Rows)
				}
				name := fmt.Sprintf("%s/%d", arm, h)
				a := ControlArm{Name: name, Label: arm}
				for _, call := range calls {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					entry, state := measureResearchCall(ctx, cache, r, ideas.GeneratedAt, call, h, options.AsOf)
					if state == callScored {
						// A missing benchmark is not a zero-return benchmark. Require
						// aligned data before admitting a call to the comparison.
						b, err := cache.get(ctx, benchmarkFor(call.index, call.ticker), r.Dir)
						if err != nil || b == nil {
							state = callUnmeasurable
							entry.Err = "benchmark history unavailable"
						} else if br, ok := researchBenchmarkReturn(ctx, cache, r.Dir, call.ticker, b, ideas.GeneratedAt, entry.CallEndDate, entry.PriceAtGen); !ok {
							state = callUnmeasurable
							entry.Err = "benchmark or generation anchor is missing an aligned valid boundary"
						} else {
							entry.CallBenchPct = round2(br * 100)
							entry.CallExcessPct = round2(entry.CallPnLPct - entry.CallBenchPct)
							if string(call.direction) == "SELL" {
								entry.CallExcessPct = round2(entry.CallPnLPct + entry.CallBenchPct)
							}
						}
					}
					if state == callUnmeasurable {
						a.Unmeasurable++
						if entry.Err != "" {
							diag.MeasurementIssues = append(diag.MeasurementIssues, name+" "+call.ticker+": "+entry.Err)
						}
						continue
					}
					a.Entries = append(a.Entries, entry)
				}
				diag.Arms[name] = summarizeResearchArm(a)
				combined := c.Arms[name]
				combined.Name, combined.Label = name, arm
				combined.Entries = append(combined.Entries, a.Entries...)
				combined.Unmeasurable += a.Unmeasurable
				c.Arms[name] = combined
			}
		}
		if options.RoundTripCostBPS != nil {
			diag.Execution = researchExecutionAt(ctx, cache, r, ideas, *options.RoundTripCostBPS, options.FillWindowDays, options.AsOf)
		}
		result.Runs = append(result.Runs, diag)
	}
	result.Notes = "Cohorts use different dates unless runs were deliberately paired. Returns are directional calls, not executed profits; conditional plans assume prerequisites can be met. Completed calls without benchmark history are unavailable. Small samples and overlapping market exposures do not establish an edge. No historical source reconstruction or automatic tuning. Token totals are reported usage lower bounds when attempts have missing counts; CLI usage without telemetry is unavailable; incomplete telemetry remains a lower bound. Cost scenarios are assumptions, not observed execution costs."
	keys := []string{}
	for k := range cohorts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c := cohorts[k]
		for name, a := range c.Arms {
			c.Arms[name] = summarizeResearchArm(a)
		}
		result.Cohorts = append(result.Cohorts, *c)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result.Pairs = auditResearchPairs(ctx, dir, manifest, result.Runs)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
func (r *ResearchComparison) FormatText() string {
	var b strings.Builder
	for _, c := range r.Cohorts {
		fmt.Fprintf(&b, "%s\n  %d runs · %d empty · %d conditional plans\n", c.Key, c.Runs, c.EmptyRuns, c.ConditionalPlans)
		fmt.Fprintf(&b, "  %d unreadable/missing results · %d degraded · %.1fs · %d reported completion tokens\n", c.FailedResults, c.DegradedRuns, float64(c.DurationMS)/1000, c.CompletionTokens)
		names := []string{}
		for name := range c.Arms {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			a := c.Arms[name]
			fmt.Fprintf(&b, "  %-14s n=%d pending=%d unavailable=%d overlap=%d", name, a.Record.N, a.Pending, a.Unmeasurable, a.Overlapping)
			if a.Record.N == 0 {
				b.WriteString(" · return unavailable · excess unavailable\n")
			} else {
				fmt.Fprintf(&b, " · return %+.2f%% · excess %+.2f%%\n", a.Record.AvgPnL, a.Record.AvgExcess)
			}
		}
	}
	for _, run := range r.Runs {
		fmt.Fprintf(&b, "Run %s: result=%s outcome=%s; %d failed calls, %d invalid payloads; %d/%d attempts with complete usage\n", run.Run, run.Result, run.Outcome, run.FailedCalls, run.InvalidPayloads, run.Usage.CompleteAttempts, run.Usage.Attempts)
		fmt.Fprintf(&b, "  reported tokens: prompt=%d completion=%d total=%d; duration=%.1fs; data errors=%d (%d distinct)\n", run.Usage.PromptTokens, run.Usage.CompletionTokens, run.Usage.TotalTokens, float64(run.DurationMS)/1000, run.DataErrors, run.DistinctDataErrors)
		for _, issue := range run.MeasurementIssues {
			fmt.Fprintf(&b, "  unavailable: %s\n", issue)
		}
		for _, stage := range []string{"discovery", "research"} {
			for _, coverage := range run.Coverage[stage] {
				fmt.Fprintf(&b, "  %s %s: %d eligible · %d with news · %d with documents\n", stage, coverage.Region, coverage.Eligible, coverage.WithNews, coverage.WithDocuments)
			}
		}
		for _, e := range run.Execution {
			if e.NetReturn != nil {
				fmt.Fprintf(&b, "  %s replay %s: gross %+.2f%% · scenario net %+.2f%% at %.1f bps; conditional=%t\n", e.Ticker, e.Outcome, *e.GrossReturn, *e.NetReturn, e.RoundTripCostBPS, e.Conditional)
			} else {
				fmt.Fprintf(&b, "  %s replay %s: cost scenario pending/unavailable\n", e.Ticker, e.Outcome)
			}
		}
	}
	for _, pair := range r.Pairs {
		fmt.Fprintf(&b, "Pair %s: %s · %s\n", pair.ID, pair.Status, strings.Join(pair.Issues, "; "))
		for _, arm := range []string{"shipped", "composite", "shortlist"} {
			for _, h := range []int{10, 15} {
				name := fmt.Sprintf("%s/%d", arm, h)
				d, ok := pair.Deltas[name]
				if !ok {
					continue
				}
				fmt.Fprintf(&b, "  %s: %s · legacy n=%d thesis n=%d", name, d.State, d.LegacyN, d.ThesisN)
				if d.ExcessDifference != nil {
					fmt.Fprintf(&b, " · excess difference %+.2f%%", *d.ExcessDifference)
				}
				b.WriteByte('\n')
			}
		}
		if len(pair.OverlappingPairs) > 0 {
			fmt.Fprintf(&b, "  overlapping exposures with pairs: %s\n", strings.Join(pair.OverlappingPairs, ", "))
		}
	}
	b.WriteString(r.Notes + "\n")
	return b.String()
}
