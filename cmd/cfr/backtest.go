package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/backtest"
	"github.com/mamut/claude-financial-researcher/internal/config"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// runBacktest implements `cfr backtest`: the point-in-time weekly replay of the
// pre-screen over every universe constituent (docs/workflow/backtest.md).
//
// It calls no model. Prices come from the keyless Yahoo chart endpoint through
// the shared data cache, cache-first, so a re-run inside --cache-age makes no
// request at all. No credential is read or needed.
//
// Exit codes: 0 ok, 1 error, 2 bad usage.
func runBacktest(settings *config.Settings, args []string) int {
	fs := flag.NewFlagSet("backtest", flag.ContinueOnError)
	years := fs.Int("years", 4, "years of weekly rebalances to replay (the first year of history is warm-up on top)")
	indices := fs.String("indices", "", "comma-separated index keys (default: all four)")
	cacheAge := fs.Duration("cache-age", 7*24*time.Hour, "serve a cached long price series younger than this without a request")
	asJSON := fs.Bool("json", false, "print the report as JSON on stdout instead of the text summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *years <= 0 {
		fmt.Fprintln(os.Stderr, "error: --years must be positive")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	uni, err := universe.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	var idx []string
	for _, s := range strings.Split(*indices, ",") {
		if s = strings.TrimSpace(s); s != "" {
			idx = append(idx, s)
		}
	}

	now := time.Now()
	res, err := backtest.Run(ctx, marketdata.NewYahooClient(marketdata.NewCache(settings.DataDir)), uni, backtest.Config{
		Years: *years, Indices: idx, CacheMaxAge: *cacheAge, Now: now,
		Log: func(s string) { fmt.Fprintln(os.Stderr, s) },
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	path, err := res.Save(filepath.Join(settings.DataDir, "backtest"), now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: save report: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "report written to %s\n", path)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fmt.Fprintf(os.Stderr, "error: encode report: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Print(res.Text())
	return 0
}
