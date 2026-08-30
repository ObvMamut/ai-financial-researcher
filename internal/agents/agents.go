// Package agents loads agent personas and assembles the prompts sent to each CLI.
package agents

import (
	"crypto/sha256"
	"encoding/hex"
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
	shas     map[string]string // role → sha256 of the persona file
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
		shas:     make(map[string]string),
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
		// A persona is runtime data that can change between runs without any
		// code change, so a run's outcome is only attributable if the exact
		// prompt text it used is identifiable. The hash goes into metadata.json.
		sum := sha256.Sum256(data)
		r.shas[role] = hex.EncodeToString(sum[:])[:12]
	}
	return r, nil
}

// PersonaSHA maps each loaded role to a short hash of its persona file, for
// recording in run metadata. Two runs with the same hashes used the same
// prompts; two with different hashes are not comparable on outcome alone.
func (r *Registry) PersonaSHA() map[string]string {
	out := make(map[string]string, len(r.shas))
	for k, v := range r.shas {
		out[k] = v
	}
	return out
}

// Capabilities declares what the engine running this agent can actually do.
// The personas were written for a search-capable CLI; the OpenAI-compatible HTTP
// engine sends only model+messages and has no tools at all. Stating the truth in
// the prompt is what stops an agent from "complying" with an impossible
// instruction by inventing sources.
type Capabilities struct {
	// WebSearch reports whether the agent can search the web during this call.
	// The zero value is false: an engine whose capabilities were never declared
	// is treated as search-less, which is the safe direction — it suppresses
	// citations rather than inviting them.
	WebSearch bool
}

// PromptParams holds all inputs needed to assemble a prompt for one agent run.
type PromptParams struct {
	Role                 string              // e.g. "scout", "quant", "chief-analyst"
	Mode                 model.Mode          // independent | single
	RunTS                time.Time           // run timestamp (for context)
	Shortlist            []model.Candidate   // current shortlist (empty for scouts)
	IndexKey             string              // scout only: which index to screen
	Ticker               string              // single-stock mode: the ticker
	Reports              []ReportContext     // chief-analyst only: specialist reports to inline
	Missing              []string            // chief-analyst: domains that failed
	DataBlock            string              // verified market data block (full quant pack for the quant role)
	QuantBlock           string              // chief-analyst: compact verified quant lines per ticker
	BaseScoreBlock       string              // chief-analyst: computed weighted domain scores per ticker
	TrackRecordBlock     string              // chief-analyst: the pipeline's realized record, when there is enough of one
	Weights              model.DomainWeights // structured weights
	IndexConstituentList string              // scout only: formatted constituent list
	PrescreenTable       string              // scout only: Stage 0.5 ranked table for this index
	Caps                 Capabilities        // what the engine running this agent can do
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

	// Capability block. It follows the persona because it corrects it: the
	// personas assume a search-capable engine, and on the HTTP engine that
	// assumption is what produced fabricated citations.
	if p.Role != "chief-analyst" {
		sb.WriteString(capabilityBlock(p.Caps))
	}

	// Task context
	sb.WriteString("## Task context\n\n")
	sb.WriteString(fmt.Sprintf("- **Mode:** %s\n", p.Mode))
	sb.WriteString(fmt.Sprintf("- **Run timestamp:** %s\n", p.RunTS.UTC().Format(time.RFC3339)))

	switch p.Role {
	case "scout":
		sb.WriteString(fmt.Sprintf("- **Index:** %s\n", p.IndexKey))
		// The ranked table comes first because it is what the scout screens
		// from. The full constituent list follows as the set it may reach
		// outside the table, not as the thing to read top to bottom.
		if p.PrescreenTable != "" {
			sb.WriteString("\n### Computed pre-screen (verified, ground truth)\n\n")
			sb.WriteString("Ranked in-process from Yahoo Finance daily OHLCV before you were called. ")
			sb.WriteString("`score` is the composite these names are ordered by; every other column is a measured value. ")
			sb.WriteString("Do not re-derive or contradict these numbers, and do not supply figures for names that are not here.\n\n")
			sb.WriteString(p.PrescreenTable)
			sb.WriteString("\n")
		}
		sb.WriteString("\n### Constituent list\n\n```\n")
		sb.WriteString(p.IndexConstituentList)
		sb.WriteString("```\n")

	case "chief-analyst":
		sb.WriteString(shortlistSection(p.Shortlist))
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

		// The computed scores come before the reports they summarise: the Chief
		// starts from the arithmetic and reads the prose to adjust it, not the
		// other way round.
		if p.BaseScoreBlock != "" {
			sb.WriteString("\n")
			sb.WriteString(p.BaseScoreBlock)
		}

		// The record comes after the scores it qualifies: what this pipeline has
		// actually achieved is context for how hard to lean on today's evidence.
		if p.TrackRecordBlock != "" {
			sb.WriteString("\n")
			sb.WriteString(p.TrackRecordBlock)
		}

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
		sb.WriteString(shortlistSection(p.Shortlist))
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

// capabilityBlock states the engine's real capabilities and the citation rule
// that follows from them. On a search-less engine the rule is absolute: no
// `[source:]` tags at all, because there is no source the agent could have read.
func capabilityBlock(c Capabilities) string {
	if c.WebSearch {
		return `## Engine capabilities (authoritative — overrides the persona above)

- **Web search: AVAILABLE.** Use it for anything the verified-data block below
  does not cover.
- Tag every web-sourced claim ` + "`[source:domain.com YYYY-MM-DD]`" + `. No tag, no claim.
- Numbers in a "Verified Market Data" or "Verified price context" block are
  ground truth: cite them as ` + "`[verified]`" + ` and surface any conflict with what
  you find.

`
	}
	return `## Engine capabilities (authoritative — overrides the persona above)

- **Web search: NOT AVAILABLE.** This engine sends your prompt and nothing else.
  You have no search tool, no browser, and no way to fetch a URL. Anything not
  written in this prompt is unknown to you. Where the persona above says web
  search is your external capability, it is wrong for this run.
- **You MUST NOT emit ` + "`[source:domain.com YYYY-MM-DD]`" + ` tags.** There is no page you
  could have read, so any such tag would be fabricated. Fabricated citations are
  the single worst failure mode here — worse than saying nothing.
- Cite only what is in this prompt: quote figures from the verified-data block as
  ` + "`[verified]`" + `, and reference a headline by the exact URL given with it.
- Where the verified data does not cover a ticker, **say so plainly and list that
  ticker in the ` + "`missing`" + ` array**, with a low strength score. An acknowledged gap
  is a correct answer; a confident guess is not. Do not describe short interest,
  options skew, analyst counts, earnings dates, or any other figure that is not
  in this prompt.

`
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// shortlistSection renders the shortlist as its own block, one line per name.
//
// It replaces a bare comma-separated ticker list. That list told a specialist
// nothing about *why* a name was on it, so every domain re-derived the company
// from scratch — and the scout's actual thesis, the only reason the name
// survived screening, was thrown away between Stage 1 and Stage 2. Carrying it
// through means a specialist can confirm or contradict a stated thesis, and the
// Chief can see which nominations its domains agreed with.
func shortlistSection(cs []model.Candidate) string {
	if len(cs) == 0 {
		return "- **Shortlist:** (empty)\n"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n### Shortlist (%d names)\n\n", len(cs)))
	sb.WriteString("Each line is the name, its sector and source index, and the direction and reason a scout nominated it for. The scout's reason is a hypothesis to test, not a verified fact.\n\n")
	sb.WriteString(shortlistBlock(cs))
	sb.WriteString("\n")
	return sb.String()
}

// shortlistBlock is the bare list of shortlist lines.
func shortlistBlock(cs []model.Candidate) string {
	var sb strings.Builder
	for _, c := range cs {
		sb.WriteString("- ")
		sb.WriteString(strings.ToUpper(c.Ticker))
		if c.Name != "" {
			sb.WriteString(" — " + c.Name)
		}
		var meta []string
		if c.Sector != "" {
			meta = append(meta, c.Sector)
		}
		if c.Index != "" {
			meta = append(meta, c.Index)
		}
		if len(meta) > 0 {
			sb.WriteString(" (" + strings.Join(meta, ", ") + ")")
		}
		if c.Bias != "" {
			sb.WriteString(" — scout: " + string(c.Bias))
			if c.Reason != "" {
				sb.WriteString(fmt.Sprintf(", %q", c.Reason))
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}
