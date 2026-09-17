package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/config"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/redact"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
)

type pairOutcomes struct {
	SnapshotSHA256 string                   `json:"generation_snapshot_sha256"`
	FetchedAt      time.Time                `json:"fetched_at"`
	Prices         map[string]*quant.Series `json:"prices"`
	Errors         map[string]string        `json:"errors"`
}

func evaluateResearchPair(ctx context.Context, settings *config.Settings, dir string, refresh bool) error {
	var prices marketdata.PriceSource
	if refresh {
		prices = marketdata.NewPrices(settings.Providers.AlpacaKeyID, settings.Providers.AlpacaSecret, marketdata.NewCache(filepath.Join(dir, "outcome-cache")))
	}
	report, path, err := evaluatePair(ctx, dir, prices)
	if err != nil {
		return err
	}
	fmt.Print(report.FormatText())
	fmt.Printf("Evaluation saved: %s\n", path)
	return nil
}

// Outcome prices are separate artifacts. Refreshing never changes either arm's
// evidence, generation prices, metadata, or calibration.
func evaluatePair(ctx context.Context, dir string, refresh marketdata.PriceSource) (*scoreboard.ResearchComparison, string, error) {
	var reg pairRegistration
	var state pairState
	if err := readPairJSON(filepath.Join(dir, "registration.json"), &reg); err != nil {
		return nil, "", err
	}
	if err := readPairJSON(filepath.Join(dir, "state.json"), &state); err != nil {
		return nil, "", err
	}
	if reg.Version != 1 || state.AsOf.IsZero() || state.SnapshotSHA256 == "" {
		return nil, "", fmt.Errorf("experiment has no sealed corpus")
	}
	if err := verifyPairInputs(dir, reg.Files); err != nil {
		return nil, "", err
	}
	var frozen marketdata.ResearchSnapshot
	for _, arm := range []string{"legacy", "thesis"} {
		path := filepath.Join(dir, "runs", arm, "data/evaluation-snapshot.json")
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, "", err
		}
		if pairHash(b) != state.SnapshotSHA256 {
			return nil, "", fmt.Errorf("%s generation corpus changed", arm)
		}
		if arm == "legacy" {
			if err := readPairJSON(path, &frozen); err != nil {
				return nil, "", err
			}
		}
	}
	var outcome pairOutcomes
	var outcomeFile string
	if refresh != nil {
		outcome = pairOutcomes{SnapshotSHA256: state.SnapshotSHA256, Prices: map[string]*quant.Series{}, Errors: map[string]string{}}
		symbols := map[string]bool{}
		for symbol := range frozen.Prices {
			symbols[symbol] = true
		}
		for symbol := range frozen.PriceErrors {
			symbols[symbol] = true
		}
		tickers, benchmarks, _, err := pairUniverse(reg.Request)
		if err != nil {
			return nil, "", err
		}
		for _, symbol := range append(tickers, benchmarks...) {
			symbols[symbol] = true
		}
		var ordered []string
		for symbol := range symbols {
			ordered = append(ordered, symbol)
		}
		sort.Strings(ordered)
		for _, symbol := range ordered {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
			acquiredAt := time.Now().UTC()
			series, err := refresh.HistoryFresh(ctx, symbol)
			if err != nil {
				outcome.Errors[symbol] = redact.String(err.Error())
				continue
			}
			if series == nil || len(series.Bars) == 0 {
				outcome.Errors[symbol] = "no outcome prices returned"
				continue
			}
			outcome.Prices[symbol] = marketdata.CompletedDailySeries(series, symbol, acquiredAt)
		}
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		outcome.FetchedAt = time.Now().UTC()
		if err := os.MkdirAll(filepath.Join(dir, "outcomes"), 0700); err != nil {
			return nil, "", err
		}
		outcomeFile = filepath.Join(dir, "outcomes", pairArtifactName(outcome.FetchedAt))
		if err := writePairJSON(outcomeFile, outcome, true); err != nil {
			return nil, "", err
		}
	} else {
		files, err := filepath.Glob(filepath.Join(dir, "outcomes", "*.json"))
		if err != nil {
			return nil, "", err
		}
		if len(files) > 0 {
			sort.Strings(files)
			outcomeFile = files[len(files)-1]
			if err := readPairJSON(outcomeFile, &outcome); err != nil {
				return nil, "", err
			}
		}
	}
	evaluationTime := time.Now().UTC()
	var source marketdata.PriceSource = &frozen
	if outcomeFile != "" {
		if outcome.SnapshotSHA256 != state.SnapshotSHA256 || outcome.FetchedAt.Before(state.AsOf) || outcome.FetchedAt.After(time.Now()) {
			return nil, "", fmt.Errorf("outcome prices do not match this experiment's timeline and corpus")
		}
		evaluationTime = outcome.FetchedAt
		source = &marketdata.ResearchSnapshot{Prices: outcome.Prices, PriceErrors: outcome.Errors}
	}
	var registeredSettings config.Settings
	if err := json.Unmarshal(reg.Settings, &registeredSettings); err != nil {
		return nil, "", err
	}
	report, err := scoreboard.CompareResearchWithOptions(ctx, filepath.Join(dir, "runs"), source, scoreboard.ResearchComparisonOptions{PairManifest: filepath.Join(dir, "pairs.json"), AsOf: evaluationTime, FillWindowDays: registeredSettings.FillWindowDays, RoundTripCostBPS: reg.RoundTripCostBPS})
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Join(dir, "evaluations"), 0700); err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, "evaluations", pairArtifactName(time.Now().UTC()))
	artifact := struct {
		SnapshotSHA256   string                         `json:"generation_snapshot_sha256"`
		OutcomeFile      string                         `json:"outcome_file,omitempty"`
		OutcomeFetchedAt time.Time                      `json:"outcome_fetched_at,omitzero"`
		Comparison       *scoreboard.ResearchComparison `json:"comparison"`
	}{state.SnapshotSHA256, outcomeFile, outcome.FetchedAt, report}
	if err := writePairJSON(path, artifact, true); err != nil {
		return nil, "", err
	}
	return report, path, nil
}

func pairArtifactName(at time.Time) string {
	return at.UTC().Format("20060102T150405.000000000Z") + ".json"
}
