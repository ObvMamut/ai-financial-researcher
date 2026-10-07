package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/config"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
)

// runCanary implements `cfr canary`: one probe per external source, through
// the providers a run uses, with no model call (internal/marketdata/canary.go).
//
// Exit codes: 0 every probe passed or was skipped, 1 a probe failed, 2 bad usage.
func runCanary(settings *config.Settings, args []string) int {
	fs := flag.NewFlagSet("canary", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the results as JSON on stdout")
	timeout := fs.Duration("timeout", 2*time.Minute, "give up on the whole canary after this long")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	res := marketdata.RunCanary(ctx, marketdata.CanaryConfig{
		AlpacaKeyID:  settings.Providers.AlpacaKeyID,
		AlpacaSecret: settings.Providers.AlpacaSecret,
		ContactEmail: settings.Providers.ContactEmail,
		Cache:        marketdata.NewCache(settings.DataDir),
	})
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(res)
	} else {
		for _, r := range res {
			fmt.Printf("%-4s  %-28s %s\n", r.Status, r.Name, r.Detail)
		}
	}
	if !marketdata.CanaryHealthy(res) {
		return 1
	}
	return 0
}
