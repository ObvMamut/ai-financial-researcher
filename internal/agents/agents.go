// Package agents loads agent personas and assembles the prompts sent to each CLI.
package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// Registry holds loaded persona text keyed by role name.
type Registry struct {
	personas map[string]string // role → markdown content
	dir      string
}

// Load reads all *.md files from agentsDir (e.g. "agents/") into the registry.
// It must be called before any prompt assembly.
func Load(agentsDir string) (*Registry, error) {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil, fmt.Errorf("agents: read dir %q: %w", agentsDir, err)
	}
	r := &Registry{
		personas: make(map[string]string),
		dir:      agentsDir,
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		role := strings.TrimSuffix(e.Name(), ".md")
		data, err := os.ReadFile(filepath.Join(agentsDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("agents: read %q: %w", e.Name(), err)
		}
		r.personas[role] = string(data)
	}
	return r, nil
}

// PromptParams holds all inputs needed to assemble a prompt for one agent run.
type PromptParams struct {
	Role       string           // e.g. "scout", "quant", "chief-analyst"
	Mode       model.Mode       // independent | single
	RunTS      time.Time        // run timestamp (for context)
	Shortlist  []model.Candidate // current shortlist (empty for scouts)
	IndexKey   string           // scout only: which index to screen
	Ticker     string           // single-stock mode: the ticker
	Reports    []ReportContext  // chief-analyst only: specialist reports to inline
	Missing    []string         // chief-analyst: domains that failed
	DataBlock  string           // verified market data block (full quant pack for the quant role)
	QuantBlock string           // chief-analyst: compact verified quant lines per ticker
	Weights    model.DomainWeights // structured weights
	IndexConstituentList string // scout only: formatted constituent list
}

// ReportContext wraps a specialist's saved markdown for inclusion in the chief-analyst prompt.
type ReportContext struct {
	Domain  string
	Content string
}

// AssemblePrompt builds the full prompt string for the given role.
// Returns an error if the persona is not registered.
func (r *Registry) AssemblePrompt(p PromptParams) (string, error) {
	persona, ok := r.personas[p.Role]
	if !ok {
		return "", fmt.Errorf("agents: unknown role %q", p.Role)
	}

	var sb strings.Builder

	// Persona block
	sb.WriteString(persona)
	sb.WriteString("\n\n")

	// Task context
	sb.WriteString("## Task context\n\n")
	sb.WriteString(fmt.Sprintf("- **Mode:** %s\n", p.Mode))
	sb.WriteString(fmt.Sprintf("- **Run timestamp:** %s\n", p.RunTS.UTC().Format(time.RFC3339)))

	switch p.Role {
	case "scout":
		sb.WriteString(fmt.Sprintf("- **Index:** %s\n", p.IndexKey))
		sb.WriteString("\n### Constituent list\n\n```\n")
		sb.WriteString(p.IndexConstituentList)
		sb.WriteString("```\n")

	case "chief-analyst":
		sb.WriteString(fmt.Sprintf("- **Shortlist:** %s\n", shortlistLine(p.Shortlist)))
		if p.Mode == model.ModeSingle {
			sb.WriteString(fmt.Sprintf("- **Ticker:** %s\n", p.Ticker))
			sb.WriteString("- **topN:** 1\n")
		} else {
			sb.WriteString("- **topN:** 5\n")
		}

		// Inject structured weights
		sb.WriteString("\n### Authoritative Scoring Weights\n\n")
		sb.WriteString(fmt.Sprintf("- **Fundamentals:** %.0f%%\n", p.Weights.Fundamentals*100))
		sb.WriteString(fmt.Sprintf("- **Quant:** %.0f%%\n", p.Weights.Quant*100))
		sb.WriteString(fmt.Sprintf("- **News:** %.0f%%\n", p.Weights.News*100))
		sb.WriteString(fmt.Sprintf("- **Macro:** %.0f%%\n", p.Weights.Macro*100))
		sb.WriteString(fmt.Sprintf("- **Sentiment:** %.0f%%\n", p.Weights.Sentiment*100))

		if p.QuantBlock != "" {
			sb.WriteString("\n### Quant reference (verified)\n\n")
			sb.WriteString("Computed in-process from daily OHLCV — ground truth for prices, vol, and the σ_daily used to place stops/targets:\n\n")
			sb.WriteString(p.QuantBlock)
		}

		if len(p.Missing) > 0 {
			sb.WriteString(fmt.Sprintf("\n- **Missing/failed specialist reports:** %s\n",
				strings.Join(p.Missing, ", ")))
			sb.WriteString("  (Apply confidence caps as per your instructions for missing data)\n")
		}
		sb.WriteString("\n### Specialist reports\n\n")
		for _, rc := range p.Reports {
			sb.WriteString(fmt.Sprintf("---\n#### %s specialist report\n\n", capitalize(rc.Domain)))
			sb.WriteString(rc.Content)
			sb.WriteString("\n\n")
		}

	default: // specialists: news, fundamentals, quant, sentiment, macro
		sb.WriteString(fmt.Sprintf("- **Shortlist:** %s\n", shortlistLine(p.Shortlist)))
		if p.Mode == model.ModeSingle {
			sb.WriteString(fmt.Sprintf("- **Ticker:** %s (single-stock mode — provide richer depth)\n", p.Ticker))
		}
		if p.DataBlock != "" {
			sb.WriteString("\n")
			sb.WriteString(p.DataBlock)
			sb.WriteString("\n")
		}
	}

	return sb.String(), nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// shortlistLine produces a compact comma-separated ticker list.
func shortlistLine(cs []model.Candidate) string {
	tickers := make([]string, len(cs))
	for i, c := range cs {
		tickers[i] = c.Ticker
	}
	return strings.Join(tickers, ", ")
}
