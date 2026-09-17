package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/mamut/claude-financial-researcher/internal/config"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
)

// runScoreboard implements `cfr scoreboard`: replay past runs' ideas through
// their own daily bars. Exit codes: 0 ok, 1 error, 2 bad usage.
func runScoreboard(settings *config.Settings, args []string) int {
	fs := flag.NewFlagSet("scoreboard", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the scoreboard as JSON on stdout")
	legacy := fs.Bool("legacy", false, "use the old mark-to-current-price math instead of the path replay")
	researchCompare := fs.Bool("research-compare", false, "compare research engine vintages and controls over 10 and 15 sessions")
	researchPairs := fs.String("research-pairs", "", "audit explicitly declared run pairs from a JSON manifest (requires --research-compare)")
	researchCost := fs.String("research-cost-bps", "", "assumed round-trip execution cost in basis points (requires --research-compare)")
	offline := fs.Bool("offline", false, "use saved price snapshots only (requires --research-compare)")
	control := fs.Bool("control", false, "compare what shipped against the pre-screen composite alone and against the funnel's own shortlist")
	horizon := fs.Int("horizon", scoreboard.DefaultControlHorizon, "sessions each control arm is scored over")
	fillWindow := fs.Int("fill-window", settings.FillWindowDays, "sessions a limit entry stays live before the idea counts as unfilled")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if (*control && *legacy) || (*researchCompare && (*control || *legacy)) {
		fmt.Fprintln(os.Stderr, "error: choose one of --research-compare, --control or --legacy")
		return 2
	}
	if fs.NArg() > 0 || (!*researchCompare && (*researchPairs != "" || *researchCost != "" || *offline)) {
		fmt.Fprintln(os.Stderr, "error: research comparison options require --research-compare; positional arguments are not accepted")
		return 2
	}
	options := scoreboard.ResearchComparisonOptions{PairManifest: *researchPairs, FillWindowDays: *fillWindow}
	if *researchCost != "" {
		cost, err := strconv.ParseFloat(*researchCost, 64)
		if err != nil || cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
			fmt.Fprintln(os.Stderr, "error: research-cost-bps must be finite and nonnegative")
			return 2
		}
		options.RoundTripCostBPS = &cost
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var yc marketdata.PriceSource
	if !*offline {
		yc = marketdata.NewPrices(settings.Providers.AlpacaKeyID, settings.Providers.AlpacaSecret, marketdata.NewCache(settings.DataDir))
	}

	if *researchCompare {
		rep, err := scoreboard.CompareResearchWithOptions(ctx, settings.RunsDir, yc, options)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(rep); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		} else {
			fmt.Print(rep.FormatText())
		}
		return 0
	}
	// The control arms answer a different question from the scoreboard proper —
	// whether the model stages beat the arithmetic they sit on — so they get
	// their own output and are never folded into the stored track record.
	if *control {
		rep, err := scoreboard.Control(ctx, settings.RunsDir, yc, *horizon)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(rep); err != nil {
				fmt.Fprintf(os.Stderr, "error: encode control report: %v\n", err)
				return 1
			}
			return 0
		}
		fmt.Print(rep.FormatText())
		return 0
	}

	sum, err := scoreboard.Replay(ctx, settings.RunsDir, yc, *fillWindow)
	if *legacy {
		sum, err = scoreboard.Build(ctx, settings.RunsDir, yc)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Storing what was just measured is what lets the next run's Chief Analyst
	// and risk gate see it. The legacy math is not a track record — it counts
	// unfilled and still-open ideas as flat trades — so it is never stored.
	if !*legacy {
		if err := scoreboard.Calibrate(sum).Save(settings.DataDir); err != nil {
			fmt.Fprintf(os.Stderr, "warn: store the track record: %v\n", err)
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(sum); err != nil {
			fmt.Fprintf(os.Stderr, "error: encode scoreboard: %v\n", err)
			return 1
		}
		return 0
	}
	// FormatText already carries the attribution cells for a replay summary; the
	// legacy math has no notion of a trade closing and so produces none.
	fmt.Print(sum.FormatText())
	return 0
}
