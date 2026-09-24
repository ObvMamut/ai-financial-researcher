package orchestrator

import (
	"fmt"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

// buildDegradedIdeas produces a mechanical fallback ranking when the Chief
// Analyst is unavailable (subprocess failed or emitted unparseable JSON).
//
// It is the base scores and nothing else, capped at 55: this path has no
// cross-domain reasoning, so it must never look as confident as a real
// synthesis. The weighting arithmetic itself lives in basescore.go — it used to
// be duplicated here, which meant the fallback ranking and the numbers the Chief
// was shown could drift apart while both looked authoritative.
func buildDegradedIdeas(cfg Config, reports []agents.ReportContext, shortlist []model.Candidate, stoodDown map[string]map[string]bool) *model.IdeasResult {
	candidate := make(map[string]model.Candidate, len(shortlist))
	for _, c := range shortlist {
		candidate[strings.ToUpper(c.Ticker)] = c
	}
	bases := computeBaseScores(cfg.Weights, reports, shortlist, stoodDown)

	result := &model.IdeasResult{
		Mode:        string(cfg.Mode),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}

	scored := 0
	for _, b := range bases {
		if len(b.Domains) > 0 {
			scored++
		}
	}
	if scored == 0 {
		result.Notes = "Degraded: synthesis unavailable and no parseable specialist scores were found. See specialist reports for detail."
		return result
	}

	topN := 5
	if cfg.Mode == model.ModeSingle {
		topN = 1
	}

	for _, b := range bases {
		if len(result.Ideas) == topN {
			break
		}
		// A name with no direction has either no coverage or exactly cancelling
		// domains; either way there is no trade to state.
		if b.Direction == "" {
			continue
		}
		conf := b.Confidence
		if conf > degradedConfCap {
			conf = degradedConfCap
		}
		c := candidate[b.Ticker]
		result.Ideas = append(result.Ideas, model.TradeIdea{
			Rank:      len(result.Ideas) + 1,
			Ticker:    c.Ticker,
			Name:      c.Name,
			Index:     c.Index,
			Direction: b.Direction,
			// No levels, so the entry policy is the whole trade: under the
			// default the scoreboard replays it at the next open with a time
			// exit, which is the measured best geometry anyway.
			EntryType:      riskDefaults(cfg.Risk).EntryType,
			Confidence:     conf,
			BaseConfidence: b.Confidence,
			DomainScores:   b.Domains,
			Why: fmt.Sprintf("Degraded mechanical score from %d specialist domain(s); no chief-analyst synthesis.",
				len(b.Domains)),
		})
	}

	result.Notes = fmt.Sprintf(
		"Degraded: chief-analyst synthesis unavailable. Ideas are the computed weighted domain score for %d name(s); confidence capped at %d.",
		scored, degradedConfCap)
	return result
}
