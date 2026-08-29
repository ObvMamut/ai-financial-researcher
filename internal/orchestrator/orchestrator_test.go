package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

func TestConfigApplyDefaults(t *testing.T) {
	tests := []struct {
		name     string
		input    Config
		validate func(*testing.T, Config)
	}{
		{
			name:  "empty config uses all defaults",
			input: Config{},
			validate: func(t *testing.T, c Config) {
				if c.AgentsDir != "agents" {
					t.Errorf("expected AgentsDir agents, got %q", c.AgentsDir)
				}
				if c.RunsDir != "runs" {
					t.Errorf("expected RunsDir runs, got %q", c.RunsDir)
				}
				if c.DataDir != ".data" {
					t.Errorf("expected DataDir .data, got %q", c.DataDir)
				}
				if c.Workers != 4 {
					t.Errorf("expected Workers 4, got %d", c.Workers)
				}
				if c.Timeouts.Screening != 5*time.Minute {
					t.Errorf("expected Screening timeout 5m, got %v", c.Timeouts.Screening)
				}
				if c.Retry.MaxAttempts != 2 {
					t.Errorf("expected MaxAttempts 2, got %d", c.Retry.MaxAttempts)
				}
				if c.Weights.Fundamentals != 0.30 {
					t.Errorf("expected Fundamentals weight 0.30, got %f", c.Weights.Fundamentals)
				}
			},
		},
		{
			name: "existing values are preserved",
			input: Config{
				AgentsDir: "custom_agents",
				Workers:   8,
				Timeouts: model.StageTimeouts{
					Screening: 1 * time.Minute,
				},
				Weights: model.DomainWeights{
					Quant: 1.0,
				},
			},
			validate: func(t *testing.T, c Config) {
				if c.AgentsDir != "custom_agents" {
					t.Errorf("expected AgentsDir custom_agents, got %q", c.AgentsDir)
				}
				if c.Workers != 8 {
					t.Errorf("expected Workers 8, got %d", c.Workers)
				}
				if c.Timeouts.Screening != 1*time.Minute {
					t.Errorf("expected Screening timeout 1m, got %v", c.Timeouts.Screening)
				}
				// Analysis timeout should still be defaulted
				if c.Timeouts.Analysis != 5*time.Minute {
					t.Errorf("expected Analysis timeout 5m, got %v", c.Timeouts.Analysis)
				}
				if c.Weights.Quant != 1.0 {
					t.Errorf("expected Quant weight 1.0, got %f", c.Weights.Quant)
				}
				// Since Quant is non-zero, the whole Weights struct is not defaulted
				if c.Weights.News != 0 {
					t.Errorf("expected News weight 0, got %f", c.Weights.News)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.applyDefaults()
			tt.validate(t, tt.input)
		})
	}
}

// ideas.json labelled NVDA and MU nq100 while shortlist.json had them as sp500:
// validateIdeas only ever *backfilled* an empty index, never corrected a wrong
// one, so per-index attribution in the scoreboard was quietly wrong.
func TestValidateIdeasCorrectsIndexAttribution(t *testing.T) {
	uni, err := universe.Load()
	if err != nil {
		t.Fatal(err)
	}
	shortlist := []model.Candidate{{Ticker: "NVDA", Name: "NVIDIA", Index: "sp500"}}
	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		{Ticker: "NVDA", Direction: "BUY", Index: "nq100"},
		{Ticker: "MU", Direction: "BUY"}, // not shortlisted: falls back to the universe
	}}

	ws := validateIdeas(res, Config{Mode: model.ModeIndependent}, uni, nil, shortlist)

	if got := res.Ideas[0].Index; got != "sp500" {
		t.Errorf("NVDA index = %q, want sp500 — the shortlist is authoritative", got)
	}
	if res.Ideas[0].Name != "NVIDIA" {
		t.Errorf("NVDA name = %q, want it backfilled from the shortlist", res.Ideas[0].Name)
	}
	found := false
	for _, w := range ws {
		if w.Ticker == "NVDA" && strings.Contains(w.Message, "index") {
			found = true
		}
	}
	if !found {
		t.Errorf("correcting an index silently hides a model error: %+v", ws)
	}
}

// The persona permits a limit entry within 5% of the last close; the validator
// said nothing until 10%, so the whole permitted band was unpoliced.
func TestValidateLevelsFlagsEntryBeyondThePersonaBand(t *testing.T) {
	qp := &quant.Pack{ByTicker: map[string]quant.Metrics{
		"AAPL": {LastClose: 100, AsOf: "2026-08-28"},
	}}
	idea := &model.TradeIdea{Ticker: "AAPL", Direction: model.DirectionBuy, Entry: 107, Stop: 100, Target: 121}

	var got string
	for _, w := range validateLevels(idea, qp) {
		if strings.Contains(w.Message, "away from verified last close") {
			got = w.Message
		}
	}
	if got == "" {
		t.Errorf("a 7%% entry deviation must be flagged: %+v", validateLevels(idea, qp))
	}
}
