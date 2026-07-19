package orchestrator

import (
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
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
