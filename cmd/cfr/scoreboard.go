package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
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
	fillWindow := fs.Int("fill-window", settings.FillWindowDays, "sessions a limit entry stays live before the idea counts as unfilled")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	yc := marketdata.NewYahooClient(marketdata.NewCache(settings.DataDir))
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
	fmt.Print(sum.FormatText())
	return 0
}
