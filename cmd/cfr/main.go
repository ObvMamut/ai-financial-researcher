// Command cfr is the entry point for Claude Financial Researcher.
//
//	cfr                  launch the TUI
//	cfr run [flags]      headless run (JSON/exit-code friendly)
//	cfr scoreboard       performance of past ideas (path replay; --legacy for the old math)
package main

import (
	"context"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mamut/claude-financial-researcher/internal/config"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/orchestrator"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
	"github.com/mamut/claude-financial-researcher/internal/tui"
)

func main() {
	settings, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "run":
			os.Exit(runHeadless(settings, os.Args[2:]))
		case "scoreboard":
			os.Exit(runScoreboard(settings, os.Args[2:]))
		case "postmortem":
			os.Exit(runPostMortem(settings, os.Args[2:]))
		case "-h", "--help", "help":
			fmt.Println("usage: cfr [run|scoreboard|postmortem] [flags]\n\n  cfr             launch the TUI\n  cfr run         headless research run (see cfr run -h)\n  cfr scoreboard  replay past ideas through their price history (see cfr scoreboard -h)\n  cfr postmortem  what the closed trades show, by cell (see cfr postmortem -h)")
			os.Exit(0)
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q (try: cfr, cfr run, cfr scoreboard, cfr postmortem)\n", os.Args[1])
			os.Exit(2)
		}
	}

	if _, err := os.Stat(settings.AgentsDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: agents directory %q not found — run from repo root\n", settings.AgentsDir)
		os.Exit(1)
	}

	runFn := func(ctx context.Context, req model.RunRequest) <-chan orchestrator.Event {
		return orchestrator.Run(ctx, orchestratorConfig(settings, req))
	}
	sbFn := func(ctx context.Context) (*scoreboard.Summary, error) {
		yc := marketdata.NewPrices(settings.Providers.AlpacaKeyID, settings.Providers.AlpacaSecret, marketdata.NewCache(settings.DataDir))
		sum, err := scoreboard.Replay(ctx, settings.RunsDir, yc, settings.FillWindowDays)
		if err == nil {
			// Opening the scoreboard is also how the track record gets refreshed
			// for the next run's Chief Analyst.
			_ = scoreboard.Calibrate(sum).Save(settings.DataDir)
		}
		return sum, err
	}

	app := tui.New(runFn, settings.RunsDir, sbFn)
	p := tea.NewProgram(app, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// orchestratorConfig maps resolved settings + a run request onto the
// orchestrator's Config.
func orchestratorConfig(s *config.Settings, req model.RunRequest) orchestrator.Config {
	indices := req.Indices
	if len(indices) == 0 {
		indices = s.Indices // config-file default selection (may still be empty = all)
	}
	return orchestrator.Config{
		Mode:          req.Mode,
		Ticker:        req.Ticker,
		Indices:       indices,
		AgentsDir:     s.AgentsDir,
		RunsDir:       s.RunsDir,
		DataDir:       s.DataDir,
		Workers:       s.Workers,
		KeepRuns:      s.KeepRuns,
		PriceTTL:      s.PriceTTL,
		DataCacheDays: s.DataCacheDays,

		PrescreenTopPerIndex:      s.PrescreenTopPerIndex,
		PrescreenPullbackPerIndex: s.PrescreenPullbackPerIndex,
		PrescreenBasePerIndex:     s.PrescreenBasePerIndex,
		MaxShortlist:              s.MaxShortlist,
		MaxPerIndex:               s.MaxPerIndex,
		MaxThinlyCovered:          s.MaxThinlyCovered,
		ShortlistReserve:          s.ShortlistReserve,
		ShortlistReserveMinMerit:  s.ShortlistReserveMinMerit,
		ChiefAdjustBand:           s.ChiefAdjustBand,
		FillWindowDays:            s.FillWindowDays,
		Risk:                      s.Risk,

		Timeouts:          s.Timeouts,
		Retry:             s.Retry,
		Weights:           s.Weights,
		Providers:         s.Providers,
		Models:            s.Models,
		Binaries:          s.Binaries,
		CheapEngine:       s.CheapEngine,
		API:               s.API,
		Local:             s.Local,
		LocalConcurrency:  s.LocalConcurrency,
		GeminiConcurrency: s.GeminiConcurrency,

		SynthesisMaxAttempts: s.SynthesisMaxAttempts,
		ChiefFallback:        s.ChiefFallback,
	}
}
