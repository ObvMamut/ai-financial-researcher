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
				// Horizon-matched defaults (see scoring.md): quant leads because
				// it is the only fully-covered, computed, 5–20d-scoped domain.
				if c.Weights.Quant != 0.35 {
					t.Errorf("expected Quant weight 0.35, got %f", c.Weights.Quant)
				}
				if c.Weights.Fundamentals != 0.18 {
					t.Errorf("expected Fundamentals weight 0.18, got %f", c.Weights.Fundamentals)
				}
				// Macro runs and is read, but does not vote: a regime is one
				// fact shared by a whole market, and scoring it per name turned
				// it into twelve confirmations of the direction the pre-screen
				// had already chosen.
				if c.Weights.Macro != 0 {
					t.Errorf("expected Macro weight 0, got %f", c.Weights.Macro)
				}
				if c.ChiefAdjustBand != 10 {
					t.Errorf("expected ChiefAdjustBand 10, got %d", c.ChiefAdjustBand)
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

	ws := validateIdeas(res, Config{Mode: model.ModeIndependent}, verified{Universe: uni, Shortlist: shortlist})

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

// The persona permits a limit entry within a sigma band of the last close; the
// validator said nothing until 10%, so the whole permitted band was unpoliced.
// 107 against a 100 close is a *long paying up*, which takes the chase band.
func TestValidateLevelsFlagsEntryBeyondThePersonaBand(t *testing.T) {
	qp := &quant.Pack{ByTicker: map[string]quant.Metrics{
		"AAPL": {LastClose: 100, AsOf: "2026-08-28"},
	}}
	idea := &model.TradeIdea{Ticker: "AAPL", Direction: model.DirectionBuy, Entry: 107, Stop: 100, Target: 121}

	var got string
	for _, w := range validateLevels(idea, qp, 1.5, 0.5) {
		if strings.Contains(w.Message, "away from verified last close") {
			got = w.Message
		}
	}
	if got == "" {
		t.Errorf("a 7%% entry deviation must be flagged: %+v", validateLevels(idea, qp, 1.5, 0.5))
	}
}

// A long bidding below the close and a short offering above it are waiting for a
// better price; the worst case is that the limit never trades, which the
// scoreboard records as `unfilled` rather than as a loss. The same distance on
// the other side is paying up for a move that has already happened. One band for
// both is why the 2026-09-04 run bid 442 for AMGN against a 444.12 close — half
// a percent below the market on a name at 0.993 of its 52-week high.
func TestValidateLevelsEntryBandIsAsymmetric(t *testing.T) {
	// sigma_daily 2% -> 0.5 sigma root 5 = 2.24%, 1.5 sigma root 5 = 6.71%.
	qp := &quant.Pack{ByTicker: map[string]quant.Metrics{
		"AAPL": {LastClose: 100, AsOf: "2026-08-28", SigmaDaily: 0.02},
	}}
	flagged := func(idea *model.TradeIdea) bool {
		for _, w := range validateLevels(idea, qp, 1.5, 0.5) {
			if strings.Contains(w.Message, "away from verified last close") {
				return true
			}
		}
		return false
	}

	cases := []struct {
		name     string
		dir      model.Direction
		entry    float64
		wantFlag bool
	}{
		{"long bidding a real pullback is patient", model.DirectionBuy, 95, false},
		{"long paying up past the chase band is flagged", model.DirectionBuy, 104, true},
		{"long paying up inside the chase band is fine", model.DirectionBuy, 102, false},
		{"long bidding beyond even the patient band is flagged", model.DirectionBuy, 90, true},
		{"short offering into a bounce is patient", model.DirectionSell, 105, false},
		{"short selling down past the chase band is flagged", model.DirectionSell, 96, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idea := &model.TradeIdea{Ticker: "AAPL", Direction: tc.dir, Entry: tc.entry, Stop: 1, Target: 2}
			if got := flagged(idea); got != tc.wantFlag {
				t.Errorf("entry %.0f vs close 100: flagged = %v, want %v", tc.entry, got, tc.wantFlag)
			}
		})
	}
}
