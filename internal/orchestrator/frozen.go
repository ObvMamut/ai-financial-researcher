package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

type researchTimeKey struct{}

func withResearchTime(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, researchTimeKey{}, at)
}

func researchTime(ctx context.Context) time.Time {
	if at, ok := ctx.Value(researchTimeKey{}).(time.Time); ok {
		return at
	}
	return time.Now()
}

func frozenAsOf(cfg Config) time.Time {
	if cfg.Frozen != nil {
		return cfg.Frozen.AsOf
	}
	return time.Time{}
}

// Validate before starting workers or creating providers: an evaluation must
// never quietly become an ordinary live run when a required input is absent.
func validateFrozenRun(cfg Config) error {
	if cfg.Frozen == nil && cfg.EvaluationRun == nil {
		return nil
	}
	if cfg.Frozen == nil || cfg.EvaluationRun == nil {
		return fmt.Errorf("frozen evaluation requires both a snapshot and a reserved run")
	}
	if cfg.CheapEngine != model.CLIApi && cfg.CheapEngine != model.CLILocal {
		return fmt.Errorf("frozen evaluation requires cheap_engine api or local; CLI research tools cannot be frozen")
	}
	if cfg.Frozen.AsOf.IsZero() || !cfg.EvaluationRun.TS.Equal(cfg.Frozen.AsOf) {
		return fmt.Errorf("frozen evaluation run timestamp must equal the nonzero snapshot cutoff")
	}
	if st, err := os.Stat(cfg.EvaluationRun.Dir); err != nil || !st.IsDir() {
		return fmt.Errorf("frozen evaluation requires an existing reserved run directory")
	}
	rel, err := filepath.Rel(cfg.EvaluationRun.Dir, cfg.DataDir)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return fmt.Errorf("frozen evaluation data directory must be isolated inside its reserved run")
	}
	available := map[string]bool{}
	for _, ticker := range cfg.Frozen.Tickers {
		available[strings.ToUpper(ticker)] = true
	}
	required := []string{strings.ToUpper(strings.TrimSpace(cfg.Ticker))}
	if cfg.Mode == model.ModeIndependent {
		u, err := universe.Load()
		if err != nil {
			return err
		}
		required = nil
		for _, index := range selectIndices(cfg.Indices) {
			for _, c := range u.Constituents(index) {
				required = append(required, c.Ticker)
			}
		}
	}
	for _, ticker := range required {
		if !available[ticker] {
			return fmt.Errorf("frozen snapshot does not declare selected ticker %s", ticker)
		}
	}
	return nil
}
