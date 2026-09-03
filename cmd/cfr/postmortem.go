package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mamut/claude-financial-researcher/internal/config"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
)

// runPostMortem implements `cfr postmortem`: the counted half of the
// self-analysis loop, printed on demand.
//
// It runs no model. The lessons a model drew are stored by a run and shown here
// when there are any; what this command computes fresh is the attribution — the
// per-cell record the lessons had to be checked against. Reading them side by
// side is the point: a claim about the pipeline should be checkable against the
// arithmetic that produced it, in one place.
//
// Exit codes: 0 ok, 1 error, 2 bad usage.
func runPostMortem(settings *config.Settings, args []string) int {
	fs := flag.NewFlagSet("postmortem", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the attribution and stored lessons as JSON on stdout")
	fillWindow := fs.Int("fill-window", settings.FillWindowDays, "sessions a limit entry stays live before the idea counts as unfilled")
	minN := fs.Int("min-n", scoreboard.MinCellN, "smallest cell to show; below it a bucket cannot support a claim")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	yc := marketdata.NewPrices(settings.Providers.AlpacaKeyID, settings.Providers.AlpacaSecret, marketdata.NewCache(settings.DataDir))
	sum, err := scoreboard.Replay(ctx, settings.RunsDir, yc, *fillWindow)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	attr := scoreboard.Attribute(sum)
	stored := scoreboard.LoadPostMortem(settings.DataDir)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(struct {
			Attribution *scoreboard.Attribution `json:"attribution"`
			PostMortem  *scoreboard.PostMortem  `json:"post_mortem,omitempty"`
		}{attr, stored}); err != nil {
			fmt.Fprintf(os.Stderr, "error: encode post-mortem: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Print(formatPostMortem(attr, stored, *minN))
	return 0
}

func formatPostMortem(a *scoreboard.Attribution, pm *scoreboard.PostMortem, minN int) string {
	var sb strings.Builder
	sb.WriteString("Post-mortem\n===========\n\n")
	if a == nil || a.NClosed == 0 {
		sb.WriteString("No closed trades yet. Every idea is still running, never filled its limit, or predates the levels the replay needs.\n")
		return sb.String()
	}

	fmt.Fprintf(&sb, "%d closed trade(s).\n\n", a.NClosed)
	lines := a.Lines(minN)
	if len(lines) == 0 {
		fmt.Fprintf(&sb, "No cell has the %d closed trades a claim needs yet.\n\n", minN)
	}
	for _, l := range lines {
		sb.WriteString("  " + l + "\n")
	}

	if pm == nil || len(pm.Lessons) == 0 {
		fmt.Fprintf(&sb, "\nNo lessons stored. A run draws them once %d ideas have closed; "+
			"`cfr run` writes them to .data/%s.\n", scoreboard.MinClosedForPostMortem, scoreboard.PostMortemFile)
		return sb.String()
	}

	fmt.Fprintf(&sb, "\nLessons (drawn %s, from %d closed ideas)\n", pm.ComputedAt, pm.NClosed)
	for _, l := range pm.Lessons {
		fmt.Fprintf(&sb, "  • %s (n=%d)\n      %s\n", l.Cell, l.N, strings.TrimSpace(l.Finding))
		if l.Action != "" {
			fmt.Fprintf(&sb, "      → %s\n", strings.TrimSpace(l.Action))
		}
	}
	// The deletions are shown, not hidden: a retrospective is the easiest output
	// in this system to invent, and what was thrown away says as much about the
	// report as what was kept.
	if len(pm.Rejected) > 0 {
		fmt.Fprintf(&sb, "\n  %d lesson(s) deleted before you saw them:\n", len(pm.Rejected))
		for _, r := range pm.Rejected {
			fmt.Fprintf(&sb, "    - %s\n", r)
		}
	}
	if len(pm.WeightSuggestions) > 0 {
		sb.WriteString("\n  Advisory weight suggestions (never applied automatically — edit [weights] yourself):\n")
		for _, w := range pm.WeightSuggestions {
			fmt.Fprintf(&sb, "    - %s %s: %s\n", w.Domain, w.Direction, w.Reason)
		}
	}
	return sb.String()
}
