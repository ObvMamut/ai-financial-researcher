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
		// A directory of personas naturally acquires a README. Loading it as a
		// role named "README" would put it in the persona-hash set and make two
		// otherwise identical persona directories look like different ones.
		if strings.EqualFold(e.Name(), "README.md") {
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
	PostMortemBlock      string              // chief-analyst: lessons drawn from that record, when there are enough closed trades
	AttributionBlock     string              // post-mortem: the computed per-cell record
	ClosedTradesBlock    string              // post-mortem: one line per closed trade, with the reasoning behind it
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
		sb.WriteString(shortlistSection(p.Shortlist, false))
		if p.Mode == model.ModeSingle {
			sb.WriteString(fmt.Sprintf("- **Ticker:** %s\n", p.Ticker))
			sb.WriteString("- **topN:** 1\n")
		} else {
			sb.WriteString("- **topN:** 5\n")
		}

		// Inject structured weights. A zero is rendered with the reason attached:
		// "Macro: 0%" on its own reads as a domain that failed rather than as one
		// that deliberately does not vote, and the difference decides whether the
		// Chief treats its report as evidence or as backdrop.
		sb.WriteString("\n### Authoritative Scoring Weights\n\n")
		weight := func(label string, w float64) {
			if w <= 0 {
				sb.WriteString(fmt.Sprintf("- **%s:** 0%% — reported but not scored; read it as context, never as a reason to move a number\n", label))
				return
			}
			sb.WriteString(fmt.Sprintf("- **%s:** %.0f%%\n", label, w*100))
		}
		weight("Fundamentals", p.Weights.Fundamentals)
		weight("Quant", p.Weights.Quant)
		weight("News", p.Weights.News)
		weight("Macro", p.Weights.Macro)
		weight("Sentiment", p.Weights.Sentiment)

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
		// And the reading of that record follows the record itself, so the Chief
		// sees the numbers before the interpretation of them.
		if p.PostMortemBlock != "" {
			sb.WriteString("\n")
			sb.WriteString(p.PostMortemBlock)
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

	case "post-mortem":
		// No shortlist, no prices, no reports. This role is looking backwards at
		// the pipeline's own closed trades and must not be handed anything about
		// today — being unable to see the current book is what keeps it from
		// writing a lesson that is really a view.
		if p.AttributionBlock != "" {
			sb.WriteString("\n")
			sb.WriteString(p.AttributionBlock)
		}
		if p.ClosedTradesBlock != "" {
			sb.WriteString("\n")
			sb.WriteString(p.ClosedTradesBlock)
		}

	default: // specialists: news, fundamentals, quant, sentiment, macro
		sb.WriteString(shortlistSection(p.Shortlist, blindToDirection(p.Role)))
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

// blindToDirection reports whether a role must be handed the shortlist without
// the direction the scout nominated in.
//
// The domains it covers are the ones whose evidence is *derived from the same
// prices the nomination is*. Stage 0.5 computes a composite from trailing
// returns and its sign is the direction; the scout nominates in that direction
// and states the composite's own figures as its reason; the shortlist line then
// carried both into every specialist prompt. A quant analyst reading the same
// daily bars, and a regime analyst asked to judge that market, were being handed
// the answer before they looked.
//
// What that cost is measurable in the stored runs. Since the pre-screen was
// introduced the quant domain has agreed with the nominated direction on 11 or
// 12 of 12 names in every single run, and macro on all 12 in each of the last
// three, with the two domains' signed scores correlating 0.85 to 0.99. Before
// the pre-screen existed quant agreed 3/3, 1/2, 5/9 and 0/5 — genuinely noisy,
// which is what an independent domain looks like. Together they are 45% of the
// domain weight, and it is the 45% that covers every name, so a base score was
// substantially the composite restated three times and the `agree` column the
// Chief reads as unanimity was measuring an echo.
//
// News, fundamentals and sentiment keep the scout's line. Their evidence —
// headlines, filings, positioning — is not derivable from the price series, so
// a stated thesis is something they can genuinely confirm or contradict, and
// they do: fundamentals dissents on 40% of names and sentiment on 29%.
//
// Blinding removes the anchoring, not the shared input: quant still reads bars
// the composite was computed from. If its agreement stays at 12/12 after this,
// the redundancy is the input rather than the prompt, and the answer is to let
// the composite into the base score as its own named term instead of laundering
// it through a domain.
func blindToDirection(role string) bool {
	switch role {
	case "quant", "macro":
		return true
	}
	return false
}

// shortlistSection renders the shortlist as its own block, one line per name.
//
// It replaces a bare comma-separated ticker list. That list told a specialist
// nothing about *why* a name was on it, so every domain re-derived the company
// from scratch — and the scout's actual thesis, the only reason the name
// survived screening, was thrown away between Stage 1 and Stage 2. Carrying it
// through means a specialist can confirm or contradict a stated thesis, and the
// Chief can see which nominations its domains agreed with.
//
// blind drops the direction and the reason for the roles that must not be told
// them; see blindToDirection.
func shortlistSection(cs []model.Candidate, blind bool) string {
	if len(cs) == 0 {
		return "- **Shortlist:** (empty)\n"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n### Shortlist (%d names)\n\n", len(cs)))
	if blind {
		sb.WriteString("Each line is the name, its sector and source index, and the setup archetype the pre-screen classified it as. The archetype is computed from price data and is verified.\n\n")
		sb.WriteString("**No direction is given for these names, and that is deliberate.** They were nominated in a direction, by a screen built from the same price history your own evidence comes from. Telling you which way would not be information; it would be your own input handed back to you, and a score that agreed with it would be counted as independent confirmation when it is nothing of the kind. Read each name on its evidence and say which way *that* points, including where it points the other way from whatever put the name here.\n\n")
	} else {
		sb.WriteString("Each line is the name, its sector and source index, the setup archetype the pre-screen classified it as, and the direction and reason a scout nominated it for. The archetype is computed from price data and is verified; the scout's reason is a hypothesis to test, not a verified fact.\n\n")
	}
	sb.WriteString(shortlistBlock(cs, blind))
	sb.WriteString("\n")
	return sb.String()
}

// shortlistBlock is the bare list of shortlist lines.
func shortlistBlock(cs []model.Candidate, blind bool) string {
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
		// The pre-screen's setup archetype. It travels with the name because it
		// is what the chasing adjustment and the entry band both key off: a
		// pullback is defined by not being extended, and charging it for
		// extension double-counts the thing that made it a candidate.
		//
		// It survives blinding because it is a shape, not a side: "pullback"
		// describes a trend resting against itself and is carried by names in
		// both directions, so it says what kind of setup to look at without
		// saying which way to look.
		if c.Setup != "" {
			meta = append(meta, "setup: "+c.Setup)
		}
		if len(meta) > 0 {
			sb.WriteString(" (" + strings.Join(meta, ", ") + ")")
		}
		if c.Bias != "" && !blind {
			sb.WriteString(" — scout: " + string(c.Bias))
			if c.Reason != "" {
				sb.WriteString(fmt.Sprintf(", %q", c.Reason))
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}
