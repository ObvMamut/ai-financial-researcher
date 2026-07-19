package agents

import (
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestLoadAndAssemble(t *testing.T) {
	reg, err := Load("../../agents")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	shortlist := []model.Candidate{
		{Ticker: "AAPL", Name: "Apple Inc.", Bias: model.BiasBullish},
		{Ticker: "NVDA", Name: "NVIDIA", Bias: model.BiasBullish},
	}

	t.Run("scout", func(t *testing.T) {
		p, err := reg.AssemblePrompt(PromptParams{
			Role:                 "scout",
			Mode:                 model.ModeIndependent,
			RunTS:                time.Now(),
			IndexKey:             "sp500",
			IndexConstituentList: "AAPL | Apple Inc. | Info Tech | NASDAQ\n",
		})
		if err != nil {
			t.Fatalf("AssemblePrompt scout: %v", err)
		}
		if !strings.Contains(p, "Scout") {
			t.Error("scout prompt missing persona text")
		}
		if !strings.Contains(p, "sp500") {
			t.Error("scout prompt missing index key")
		}
	})

	t.Run("specialist", func(t *testing.T) {
		p, err := reg.AssemblePrompt(PromptParams{
			Role:      "quant",
			Mode:      model.ModeIndependent,
			RunTS:     time.Now(),
			Shortlist: shortlist,
		})
		if err != nil {
			t.Fatalf("AssemblePrompt quant: %v", err)
		}
		if !strings.Contains(p, "AAPL") {
			t.Error("specialist prompt missing shortlist ticker")
		}
	})

	t.Run("chief-analyst", func(t *testing.T) {
		p, err := reg.AssemblePrompt(PromptParams{
			Role:      "chief-analyst",
			Mode:      model.ModeIndependent,
			RunTS:     time.Now(),
			Shortlist: shortlist,
			Reports: []ReportContext{
				{Domain: "quant", Content: "Quant report content here."},
			},
			Missing: []string{"sentiment"},
		})
		if err != nil {
			t.Fatalf("AssemblePrompt chief-analyst: %v", err)
		}
		if !strings.Contains(p, "Missing") {
			t.Error("chief-analyst prompt missing missing-domain annotation")
		}
		if !strings.Contains(p, "Quant specialist report") {
			t.Error("chief-analyst prompt missing inlined report")
		}
	})

	t.Run("unknown role", func(t *testing.T) {
		_, err := reg.AssemblePrompt(PromptParams{Role: "nonexistent"})
		if err == nil {
			t.Error("expected error for unknown role")
		}
	})
}
