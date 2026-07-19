package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

// buildDegradedIdeas produces a mechanical fallback ranking when the Chief
// Analyst is unavailable (subprocess failed or emitted unparseable JSON).
// It parses each specialist report's structured JSON tail and computes, per
// shortlisted ticker, a weighted directional score renormalized over the
// domains that actually reported. Confidence is capped at 55 — this path has
// no cross-domain reasoning, so it must never look as confident as a real
// synthesis.
func buildDegradedIdeas(cfg Config, reports []agents.ReportContext, shortlist []model.Candidate) *model.IdeasResult {
	weights := map[string]float64{
		"news":         cfg.Weights.News,
		"fundamentals": cfg.Weights.Fundamentals,
		"quant":        cfg.Weights.Quant,
		"technicals":   cfg.Weights.Quant, // legacy domain name, same weight slot
		"sentiment":    cfg.Weights.Sentiment,
		"macro":        cfg.Weights.Macro,
	}

	type accum struct {
		weighted float64 // Σ weight × biasSign × strength/10
		total    float64 // Σ weight over domains that scored this ticker
		domains  int
	}
	byTicker := make(map[string]*accum)
	candidate := make(map[string]model.Candidate, len(shortlist))
	for _, c := range shortlist {
		candidate[strings.ToUpper(c.Ticker)] = c
	}

	parsed := 0
	for _, rep := range reports {
		raw, ok := extractLastJSON(rep.Content)
		if !ok {
			continue
		}
		var sr model.SpecialistResult
		if err := json.Unmarshal([]byte(raw), &sr); err != nil {
			continue
		}
		domain := strings.ToLower(strings.TrimSpace(sr.Domain))
		if domain == "" {
			domain = strings.ToLower(rep.Domain)
		}
		w, ok := weights[domain]
		if !ok || w <= 0 {
			continue
		}
		parsed++
		for _, s := range sr.Scores {
			t := strings.ToUpper(strings.TrimSpace(s.Ticker))
			if _, inShortlist := candidate[t]; !inShortlist {
				continue // defensive: ignore tickers the model invented
			}
			sign := 0.0
			switch s.Bias {
			case model.BiasBullish:
				sign = 1
			case model.BiasBearish:
				sign = -1
			}
			strength := s.Strength
			if strength < 0 {
				strength = 0
			} else if strength > 10 {
				strength = 10
			}
			a := byTicker[t]
			if a == nil {
				a = &accum{}
				byTicker[t] = a
			}
			a.weighted += w * sign * float64(strength) / 10
			a.total += w
			a.domains++
		}
	}

	result := &model.IdeasResult{
		Mode:        string(cfg.Mode),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if parsed == 0 || len(byTicker) == 0 {
		result.Notes = "Degraded: synthesis unavailable and no parseable specialist scores were found. See specialist reports for detail."
		return result
	}

	type scored struct {
		ticker     string
		direction  model.Direction
		confidence int
	}
	var ranked []scored
	for t, a := range byTicker {
		if a.total == 0 {
			continue
		}
		score := a.weighted / a.total // renormalize over present domains; ∈ [-1, 1]
		if score == 0 {
			continue
		}
		dir := model.DirectionBuy
		if score < 0 {
			dir = model.DirectionSell
			score = -score
		}
		conf := int(score*100 + 0.5)
		if conf > 55 {
			conf = 55
		}
		ranked = append(ranked, scored{ticker: t, direction: dir, confidence: conf})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].confidence != ranked[j].confidence {
			return ranked[i].confidence > ranked[j].confidence
		}
		return ranked[i].ticker < ranked[j].ticker
	})

	topN := 5
	if cfg.Mode == model.ModeSingle {
		topN = 1
	}
	if len(ranked) > topN {
		ranked = ranked[:topN]
	}

	for i, s := range ranked {
		c := candidate[s.ticker]
		result.Ideas = append(result.Ideas, model.TradeIdea{
			Rank:       i + 1,
			Ticker:     c.Ticker,
			Name:       c.Name,
			Index:      c.Index,
			Direction:  s.direction,
			Confidence: s.confidence,
			Why: fmt.Sprintf("Degraded mechanical score from %d specialist domain(s); no chief-analyst synthesis.",
				byTicker[s.ticker].domains),
		})
	}
	result.Notes = fmt.Sprintf(
		"Degraded: chief-analyst synthesis unavailable. Ideas are a mechanical weighted average of %d specialist score block(s); confidence capped at 55.",
		parsed)
	return result
}
