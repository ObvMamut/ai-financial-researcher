package agents

import (
	"os"
	"path/filepath"
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

// The scout used to receive a bare ticker list and was asked to name the best
// setups in it — with no prices and no search, the only thing it could rank on
// was familiarity. The computed table is what it now screens from, so it has to
// actually reach the prompt, ahead of the constituent list.
func TestScoutPromptCarriesPrescreenTable(t *testing.T) {
	reg, err := Load("../../agents")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	table := "| rank | ticker | score | mom12-1 |\n|---:|---|---:|---:|\n| 1 | NVDA | +2.10 | +48.0% |\n"
	p, err := reg.AssemblePrompt(PromptParams{
		Role:                 "scout",
		Mode:                 model.ModeIndependent,
		RunTS:                time.Now(),
		IndexKey:             "sp500",
		IndexConstituentList: "AAPL | Apple Inc. | Info Tech | NASDAQ\n",
		PrescreenTable:       table,
	})
	if err != nil {
		t.Fatalf("AssemblePrompt scout: %v", err)
	}
	if !strings.Contains(p, table) {
		t.Fatalf("scout prompt does not carry the pre-screen table:\n%s", p)
	}
	if strings.Index(p, table) > strings.Index(p, "### Constituent list") {
		t.Error("the ranked table must precede the constituent list — it is what the scout screens from")
	}

	// With no table (a pre-screen that fetched nothing) the prompt must degrade
	// to the plain list rather than carry an empty, authoritative-looking heading.
	bare, err := reg.AssemblePrompt(PromptParams{
		Role: "scout", Mode: model.ModeIndependent, RunTS: time.Now(),
		IndexKey: "sp500", IndexConstituentList: "AAPL | Apple Inc. | Info Tech | NASDAQ\n",
	})
	if err != nil {
		t.Fatalf("AssemblePrompt scout: %v", err)
	}
	if strings.Contains(bare, "Computed pre-screen") {
		t.Error("empty pre-screen still rendered its heading")
	}
}

// A bare comma-separated ticker list told a specialist nothing about why a name
// was on the shortlist, so the scout's thesis — the only reason the name
// survived screening — was discarded between Stage 1 and Stage 2.
func TestShortlistBlockCarriesScoutContext(t *testing.T) {
	reg, err := Load("../../agents")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	shortlist := []model.Candidate{
		{Ticker: "NVDA", Name: "NVIDIA Corporation", Sector: "Technology", Index: "sp500",
			Bias: model.BiasBullish, Reason: "pullback to the prior breakout zone"},
		{Ticker: "NKE", Name: "Nike Inc.", Sector: "Consumer Discretionary", Index: "sp500",
			Bias: model.BiasBearish, Reason: "inventory overhang"},
	}
	for _, role := range []string{"news", "chief-analyst"} {
		p, err := reg.AssemblePrompt(PromptParams{
			Role: role, Mode: model.ModeIndependent, RunTS: time.Now(), Shortlist: shortlist,
		})
		if err != nil {
			t.Fatalf("AssemblePrompt %s: %v", role, err)
		}
		for _, want := range []string{
			"NVDA — NVIDIA Corporation (Technology, sp500) — scout: bullish",
			"pullback to the prior breakout zone",
			"NKE — Nike Inc. (Consumer Discretionary, sp500) — scout: bearish",
		} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt missing %q", role, want)
			}
		}
	}
}

func TestChiefPromptLeadsWithTheComputedBaseScores(t *testing.T) {
	// The Chief reads top-down. The arithmetic it is meant to start from has to
	// arrive before the prose it is meant to adjust with, or the prose anchors
	// it first and the base becomes a number to reconcile against afterwards.
	reg, err := Load("../../agents")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	block := "### Computed base scores (authoritative)\n\n| ticker | base |\n|---|---|\n| NVDA | 41 |\n"
	p, err := reg.AssemblePrompt(PromptParams{
		Role:           "chief-analyst",
		Mode:           model.ModeIndependent,
		RunTS:          time.Now(),
		Shortlist:      []model.Candidate{{Ticker: "NVDA", Name: "NVIDIA", Index: "sp500"}},
		BaseScoreBlock: block,
		QuantBlock:     "- NVDA: close 231.50\n  ↳ maxDD126 -18.0%\n",
		Reports:        []ReportContext{{Domain: "quant", Content: "quant prose"}},
	})
	if err != nil {
		t.Fatalf("AssemblePrompt chief-analyst: %v", err)
	}
	if !strings.Contains(p, block) {
		t.Fatalf("chief prompt does not carry the base scores:\n%s", p)
	}
	if strings.Index(p, block) > strings.Index(p, "### Specialist reports") {
		t.Error("the computed base scores must precede the specialist reports")
	}
	if strings.Index(p, "Authoritative Scoring Weights") > strings.Index(p, block) {
		t.Error("the weights that produced the base belong above it")
	}

	// A run whose specialists all failed has no base block; the prompt must not
	// carry an empty authoritative-looking heading.
	bare, err := reg.AssemblePrompt(PromptParams{
		Role: "chief-analyst", Mode: model.ModeIndependent, RunTS: time.Now(),
	})
	if err != nil {
		t.Fatalf("AssemblePrompt chief-analyst (bare): %v", err)
	}
	// The persona names the block in prose; what must be absent is the heading
	// that introduces one.
	if strings.Contains(bare, "### Computed base scores") {
		t.Error("no base scores should render no heading")
	}
}

func TestChiefPromptCarriesTheTrackRecord(t *testing.T) {
	reg, err := Load("../../agents")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p := PromptParams{
		Role:             "chief-analyst",
		Mode:             model.ModeIndependent,
		RunTS:            time.Now(),
		Shortlist:        []model.Candidate{{Ticker: "AAA", Index: "sp500"}},
		BaseScoreBlock:   "### Computed base scores\n\nAAA BUY 55\n",
		TrackRecordBlock: "### Track record (computed from 31 closed ideas)\n\n- Overall: 45%\n",
	}
	got, err := reg.AssemblePrompt(p)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if !strings.Contains(got, "### Track record (computed from 31 closed ideas)") {
		t.Errorf("chief prompt missing the track record:\n%s", got)
	}
	// The record qualifies the scores, so it has to come after them — and
	// before the prose the Chief reads to adjust them.
	base := strings.Index(got, "### Computed base scores")
	rec := strings.Index(got, "### Track record")
	reports := strings.Index(got, "### Specialist reports")
	if !(base < rec && rec < reports) {
		t.Errorf("ordering base=%d record=%d reports=%d, want base < record < reports", base, rec, reports)
	}

	// With no record, nothing is said about one. An empty header would invite
	// the Chief to reason about a track record it cannot see.
	p.TrackRecordBlock = ""
	got, err = reg.AssemblePrompt(p)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	// The persona itself explains what to do when a record is present, so look
	// for the injected block's own header rather than the words.
	if strings.Contains(got, "### Track record (") {
		t.Errorf("prompt carries a track-record block it does not have:\n%s", got)
	}
}

func TestLoadIgnoresAReadme(t *testing.T) {
	// A persona directory with documentation in it is still a persona
	// directory. Turning the README into an agent would also change the
	// persona-hash set, which is what run outcomes are attributed by.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scout.md"), []byte("# Agent: Scout\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("how to use these\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := reg.PersonaSHA()["README"]; ok {
		t.Errorf("README was loaded as a persona: %v", reg.PersonaSHA())
	}
	if len(reg.PersonaSHA()) != 1 {
		t.Errorf("persona set = %v, want just the scout", reg.PersonaSHA())
	}
}
