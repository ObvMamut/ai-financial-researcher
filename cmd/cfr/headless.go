package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mamut/claude-financial-researcher/internal/config"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/orchestrator"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

// runHeadless implements `cfr run`: drive the full pipeline without the TUI.
// Progress goes to stderr; the final IdeasResult (or human summary) goes to
// stdout. Exit codes: 0 complete, 1 error, 2 bad usage, 3 degraded.
func runHeadless(settings *config.Settings, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	mode := fs.String("mode", "independent", "run mode: independent | single")
	ticker := fs.String("ticker", "", "ticker for single-stock mode (implies --mode single)")
	indices := fs.String("indices", "", "comma-separated index keys to screen (e.g. sp500,eu50); empty = all")
	researchMode := fs.String("research-mode", settings.ResearchMode, "research mode: legacy or thesis")
	chiefEngine := fs.String("chief-engine", settings.ChiefEngine, "chief analyst engine: claude or api")
	asJSON := fs.Bool("json", false, "print the final IdeasResult as JSON on stdout")
	quiet := fs.Bool("quiet", false, "suppress progress output on stderr")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	req := model.RunRequest{Mode: model.Mode(*mode)}
	if *ticker != "" {
		req.Mode = model.ModeSingle
		req.Ticker = strings.ToUpper(strings.TrimSpace(*ticker))
	}
	switch req.Mode {
	case model.ModeIndependent, model.ModeSingle:
	default:
		fmt.Fprintf(os.Stderr, "invalid --mode %q (want independent or single)\n", *mode)
		return 2
	}
	if req.Mode == model.ModeSingle && req.Ticker == "" {
		fmt.Fprintln(os.Stderr, "single mode requires --ticker")
		return 2
	}
	if *indices != "" {
		for _, k := range strings.Split(*indices, ",") {
			if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
				req.Indices = append(req.Indices, k)
			}
		}
	}

	if _, err := os.Stat(settings.AgentsDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: agents directory %q not found — run from repo root\n", settings.AgentsDir)
		return 1
	}

	if *researchMode != "legacy" && *researchMode != "thesis" {
		fmt.Fprintln(os.Stderr, "research-mode must be legacy or thesis")
		return 2
	}
	settingsCopy := *settings
	settingsCopy.ResearchMode = *researchMode
	// Flags win over env/file per the fixed precedence. Re-validate here (not
	// just the flag's own enum) so a --chief-engine=api override without a
	// configured [chief_api] fails now — before any pre-screening or
	// frozen-corpus collection — rather than surfacing later as an
	// unconfigured-engine error mid-run.
	settingsCopy.ChiefEngine = *chiefEngine
	if err := settingsCopy.ValidateChiefEngine(); err != nil {
		// ValidateChiefEngine's errors name fields, never values (pinned by
		// internal/config/redaction_test.go), so this redaction is currently
		// a no-op — kept for symmetry with research_pair.go's identical call
		// and as a standing guard if that ever stops being true.
		fmt.Fprintf(os.Stderr, "error: %s\n", redact.String(err.Error()))
		return 2
	}
	settings = &settingsCopy
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logf := func(format string, a ...any) {
		if !*quiet {
			fmt.Fprintf(os.Stderr, format+"\n", a...)
		}
	}

	var (
		ideas *model.IdeasResult
		meta  *model.RunMeta
		fatal string
	)
	for ev := range orchestrator.Run(ctx, orchestratorConfig(settings, req)) {
		switch ev.Type {
		case orchestrator.EventLog:
			logf("%s", ev.Message)
		case orchestrator.EventStatus:
			logf("[%s] %s", ev.Status, ev.Agent)
		case orchestrator.EventError:
			fatal = ev.Message
		case orchestrator.EventComplete:
			ideas = ev.Ideas
			meta = ev.Meta
			logf("run artifacts: %s", ev.Message)
		}
	}

	if fatal != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", fatal)
		return 1
	}
	if ideas == nil {
		fmt.Fprintln(os.Stderr, "error: run produced no result")
		return 1
	}

	if *asJSON {
		if err := writeIdeasJSON(os.Stdout, ideas); err != nil {
			fmt.Fprintf(os.Stderr, "error: encode result: %v\n", err)
			return 1
		}
	} else {
		printIdeasText(ideas, meta)
	}

	if meta != nil && meta.Outcome != "complete" {
		return 3
	}
	return 0
}

// writeIdeasJSON is the exact encoding `cfr run --json` writes to stdout —
// factored out so tests can exercise the real wire format (including
// IdeasResult's compact Chief provenance, ChiefEngine/ChiefAccepted) rather
// than reimplementing the encode call.
func writeIdeasJSON(w io.Writer, ideas *model.IdeasResult) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(ideas)
}

// printIdeasText renders the final ideas as a human-readable summary.
func printIdeasText(res *model.IdeasResult, meta *model.RunMeta) {
	fmt.Printf("%d idea(s) — mode %s, generated %s\n", len(res.Ideas), res.Mode, res.GeneratedAt)
	if line := model.SourceDiagnosticsLine(meta); line != "" {
		fmt.Println(line)
	}
	if line := model.ChiefProvenanceLine(meta); line != "" {
		fmt.Println(line)
	}
	if res.ResearchMode == "thesis" {
		summary := res.ResearchSummary
		if summary == nil && meta != nil {
			summary = model.SummarizeResearch(meta.ResearchOutcomes, res.Decisions)
		}
		if summary != nil {
			fmt.Println(summary.String())
		}
		for _, issue := range model.ResearchRunIssues(meta) {
			fmt.Println(issue)
		}
	}
	if meta != nil && meta.Outcome != "" && meta.Outcome != "complete" {
		fmt.Printf("outcome: %s\n", meta.Outcome)
	}
	fmt.Println()
	for _, idea := range res.Ideas {
		if idea.Thesis != nil {
			fmt.Printf("%d. %s %s (%s) — %s · evidence %s\n", idea.Rank, idea.Direction, idea.Ticker, idea.Name, idea.Status, idea.Thesis.EvidenceQuality)
			fmt.Printf("   Why now: %s\n   Invalidation: %s\n   Entry expires %s; exit by %s\n", idea.Thesis.WhyNow, idea.Thesis.Invalidation, idea.Thesis.EntryExpiresOn, idea.Thesis.ExpiresOn)
			for _, note := range idea.Thesis.Monitoring {
				fmt.Printf("   Monitor: %s\n", note)
			}
			for _, p := range idea.Thesis.Prerequisites {
				fmt.Printf("   Before entry: %s\n", p)
			}
		} else {
			fmt.Printf("%d. %s %s (%s) — confidence %d\n", idea.Rank, idea.Direction, idea.Ticker, idea.Name, idea.Confidence)
		}
		if idea.Entry > 0 {
			fmt.Printf("   entry %.2f · stop %.2f · target %.2f · RR %.2f · %dd\n",
				idea.Entry, idea.Stop, idea.Target, idea.RiskReward, idea.TimeframeDays)
		}
		fmt.Printf("   %s\n", idea.Why)
		if idea.PositionNote != "" {
			fmt.Printf("   note: %s\n", idea.PositionNote)
		}
	}
	if res.ResearchMode == "thesis" {
		for _, d := range res.Decisions {
			if d.Status == "watchlist" || d.Status == "rejected" {
				label, reason := d.Status, d.Reason
				if d.Blocked == model.BlockedResearchFailure {
					label = "research failed"
					if d.ReviewReason != "" {
						reason = d.ReviewReason
					}
				}
				fmt.Printf("%s · %s: %s\n", d.Ticker, label, reason)
			}
		}
	}
	if res.Notes != "" {
		fmt.Printf("\nnotes: %s\n", res.Notes)
	}
	if meta != nil && len(meta.Warnings) > 0 {
		fmt.Printf("\nwarnings:\n")
		for _, w := range meta.Warnings {
			fmt.Printf("  - %s\n", w)
		}
	}
}
