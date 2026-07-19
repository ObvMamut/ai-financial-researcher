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

// runScoreboard implements `cfr scoreboard`: score past runs' ideas against
// current prices. Exit codes: 0 ok, 1 error, 2 bad usage.
func runScoreboard(settings *config.Settings, args []string) int {
	fs := flag.NewFlagSet("scoreboard", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the scoreboard as JSON on stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	yc := marketdata.NewYahooClient(marketdata.NewCache(settings.DataDir))
	sum, err := scoreboard.Build(ctx, settings.RunsDir, yc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
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
