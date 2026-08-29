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

// Five of the seven personas instruct the agent to search the web and tag
// sources. On the HTTP engine there is no search tool, and the models complied
// by inventing sources — so the prompt has to correct the persona explicitly.
func TestAssemblePromptDeclaresCapabilities(t *testing.T) {
	reg, err := Load("../../agents")
	if err != nil {
		t.Fatal(err)
	}

	base := PromptParams{
		Role:      "news",
		Mode:      model.ModeIndependent,
		RunTS:     time.Now(),
		Shortlist: []model.Candidate{{Ticker: "AAPL", Name: "Apple Inc."}},
	}

	withSearch, err := reg.AssemblePrompt(func() PromptParams {
		p := base
		p.Caps = Capabilities{WebSearch: true}
		return p
	}())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withSearch, "Web search: AVAILABLE") {
		t.Error("a search-capable engine should be told to use search")
	}

	without, err := reg.AssemblePrompt(base) // zero value: no search
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(without, "Web search: NOT AVAILABLE") {
		t.Error("a search-less engine must be told so")
	}
	if !strings.Contains(without, "MUST NOT emit") {
		t.Error("a search-less engine must be forbidden from emitting [source:] tags")
	}
	if !strings.Contains(without, "`missing` array") {
		t.Error("a search-less engine must be told where to report gaps")
	}
	// The capability block has to outrank the persona text it contradicts.
	if !strings.Contains(without, "overrides the persona above") {
		t.Error("the capability block must declare itself authoritative")
	}
	if strings.Index(without, "Engine capabilities") > strings.Index(without, "## Task context") {
		t.Error("capabilities should be declared before the task context")
	}
}

// The Chief Analyst synthesises what the specialists wrote; it is not a
// research role and gets no capability block.
func TestChiefAnalystHasNoCapabilityBlock(t *testing.T) {
	reg, err := Load("../../agents")
	if err != nil {
		t.Fatal(err)
	}
	p, err := reg.AssemblePrompt(PromptParams{
		Role:      "chief-analyst",
		Mode:      model.ModeIndependent,
		RunTS:     time.Now(),
		Shortlist: []model.Candidate{{Ticker: "AAPL", Name: "Apple Inc."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p, "Engine capabilities") {
		t.Error("the chief analyst is a synthesis role and needs no capability block")
	}
}
