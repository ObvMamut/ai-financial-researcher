package orchestrator

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

type warning struct {
	Ticker  string
	Message string
}

// validateIdeas normalises and checks the Chief Analyst's ideas against the
// shortlist the run actually analysed and the verified price data it computed.
//
// shortlist is the authority on a name's index and full name. Enrichment used
// to come from the universe files and only ever filled an *empty* field, so an
// idea the Chief labelled nq100 for a name the run screened out of sp500 kept
// the wrong label all the way into the scoreboard's per-index attribution.
//
// bases carries the deterministic weighted domain scores. Confidence is an
// adjustment to those, not a free assertion, so anything outside the configured
// band is clamped here and reported.
func validateIdeas(res *model.IdeasResult, cfg Config, uni *universe.Universe, qp *quant.Pack, shortlist []model.Candidate, bases []BaseScore) []warning {
	var warnings []warning

	byTicker := make(map[string]model.Candidate, len(shortlist))
	for _, c := range shortlist {
		byTicker[strings.ToUpper(strings.TrimSpace(c.Ticker))] = c
	}
	baseByTicker := make(map[string]BaseScore, len(bases))
	for _, b := range bases {
		baseByTicker[strings.ToUpper(strings.TrimSpace(b.Ticker))] = b
	}

	// Fix GeneratedAt if missing
	if res.GeneratedAt == "" {
		res.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if res.Mode == "" {
		res.Mode = string(cfg.Mode)
	}

	validIdeas := make([]model.TradeIdea, 0, len(res.Ideas))

	sectors := make(map[string]int)

	for i := range res.Ideas {
		idea := &res.Ideas[i]

		// 1. Clamp confidence
		if idea.Confidence > 100 {
			warnings = append(warnings, warning{Ticker: idea.Ticker, Message: "confidence clamped from >100 to 100"})
			idea.Confidence = 100
		}
		if idea.Confidence < 0 {
			warnings = append(warnings, warning{Ticker: idea.Ticker, Message: "confidence clamped from <0 to 0"})
			idea.Confidence = 0
		}

		// 2. Normalize direction
		dir := strings.ToUpper(strings.TrimSpace(string(idea.Direction)))
		if dir == "BUY" || dir == "LONG" {
			idea.Direction = model.DirectionBuy
		} else if dir == "SELL" || dir == "SHORT" {
			idea.Direction = model.DirectionSell
		} else {
			warnings = append(warnings, warning{Ticker: idea.Ticker, Message: "dropped due to invalid direction: " + string(idea.Direction)})
			continue
		}

		// 3. Schema defaults / enrichment. The shortlist wins over the universe
		// files: it records which index this run screened the name out of, and
		// a cross-listed name sits in more than one.
		if c, ok := byTicker[strings.ToUpper(strings.TrimSpace(idea.Ticker))]; ok {
			if idea.Name == "" {
				idea.Name = c.Name
			}
			if c.Index != "" && idea.Index != c.Index {
				if idea.Index != "" {
					warnings = append(warnings, warning{Ticker: idea.Ticker,
						Message: fmt.Sprintf("index %q corrected to %q (the index this run screened it from)", idea.Index, c.Index)})
				}
				idea.Index = c.Index
			}
		}
		if c, ok := uni.Lookup(idea.Ticker); ok {
			if idea.Name == "" {
				idea.Name = c.Name
			}
			if idea.Index == "" {
				idea.Index = c.Index
			}
			sectors[c.Sector]++
		}

		// 4. Confidence is anchored to the computed base score.
		warnings = append(warnings, anchorConfidence(idea, baseByTicker, cfg.ChiefAdjustBand)...)

		// 5. Trade mechanics: ordering, plausibility vs verified data, RR.
		warnings = append(warnings, validateLevels(idea, qp)...)

		validIdeas = append(validIdeas, *idea)
	}

	res.Ideas = validIdeas

	// 6. Diversification check (simplistic: > 3 ideas in same sector)
	if cfg.Mode == model.ModeIndependent && len(res.Ideas) >= 4 {
		for sector, count := range sectors {
			if count > 3 {
				warnings = append(warnings, warning{Message: "diversification fail: too many ideas in " + sector})
				// We don't drop here, just flag it for the caller to decide if re-prompt is needed
			}
		}
	}

	return warnings
}

// validateLevels checks an idea's entry/stop/target mechanics. Warn, never
// drop: the human sees the flags and the re-prompt handles hard ordering
// violations. Plausibility checks only fire when verified quant data exists
// for the ticker.
func validateLevels(idea *model.TradeIdea, qp *quant.Pack) []warning {
	var ws []warning
	warn := func(format string, args ...any) {
		ws = append(ws, warning{Ticker: idea.Ticker, Message: fmt.Sprintf(format, args...)})
	}

	if idea.Entry == 0 && idea.Stop == 0 && idea.Target == 0 {
		warn("no trade levels provided (entry/stop/target)")
		return ws
	}
	if idea.Entry <= 0 || idea.Stop <= 0 || idea.Target <= 0 {
		warn("level ordering: incomplete levels (entry %.2f stop %.2f target %.2f)", idea.Entry, idea.Stop, idea.Target)
		return ws
	}

	// Ordering per direction.
	switch idea.Direction {
	case model.DirectionBuy:
		if !(idea.Stop < idea.Entry && idea.Entry < idea.Target) {
			warn("level ordering: BUY requires stop < entry < target (got %.2f / %.2f / %.2f)", idea.Stop, idea.Entry, idea.Target)
		}
	case model.DirectionSell:
		if !(idea.Target < idea.Entry && idea.Entry < idea.Stop) {
			warn("level ordering: SELL requires target < entry < stop (got %.2f / %.2f / %.2f)", idea.Target, idea.Entry, idea.Stop)
		}
	}

	// Recompute risk/reward from the levels; fix silently-wrong claims.
	risk := math.Abs(idea.Entry - idea.Stop)
	reward := math.Abs(idea.Target - idea.Entry)
	if risk > 0 {
		rr := reward / risk
		if idea.RiskReward == 0 {
			idea.RiskReward = math.Round(rr*100) / 100
		} else if math.Abs(idea.RiskReward-rr)/rr > 0.20 {
			warn("risk_reward %.2f off by >20%% from levels (computed %.2f) — corrected", idea.RiskReward, rr)
			idea.RiskReward = math.Round(rr*100) / 100
		}
	}

	// Plausibility against verified market data.
	if qp == nil {
		return ws
	}
	m, ok := qp.ByTicker[strings.ToUpper(idea.Ticker)]
	if !ok || m.LastClose <= 0 {
		return ws
	}
	// chief-analyst.md permits a limit entry within ~5% of the last close, so
	// anything beyond that is the model exceeding its own brief. The threshold
	// used to be 10%, leaving the whole permitted band unpoliced.
	if dev := math.Abs(idea.Entry/m.LastClose - 1); dev > 0.05 {
		warn("entry %.2f is %.0f%% away from verified last close %.2f (%s)", idea.Entry, dev*100, m.LastClose, m.AsOf)
	}
	if m.SigmaDaily > 0 {
		h := float64(idea.TimeframeDays)
		if h <= 0 {
			h = 10
		}
		unit := m.SigmaDaily * math.Sqrt(h) * m.LastClose // 1σ move over the timeframe, in price
		if unit > 0 {
			stopDist := math.Abs(idea.Entry - idea.Stop)
			if stopDist < 0.5*unit {
				warn("stop distance %.2f is under 0.5× the expected %.0fd move (1σ ≈ %.2f) — likely noise-stopped", stopDist, h, unit)
			} else if stopDist > 5*unit {
				warn("stop distance %.2f exceeds 5× the expected %.0fd move (1σ ≈ %.2f) — oversized risk", stopDist, h, unit)
			}
		}
	}
	return ws
}

// anchorConfidence ties an idea's confidence to the deterministic base score
// for the direction it proposes, and records the base and its per-domain
// strengths on the idea.
//
// Confidence used to be whatever the Chief asserted, checked only against 0–100.
// Two runs on the same reports could disagree by thirty points, and nothing in
// the artifacts said which weighting produced either. The band is the size of
// the judgment the model is being asked for: the arithmetic sets the level, the
// model may move it by ±band for a reason it names, and the coverage cap binds
// over both.
func anchorConfidence(idea *model.TradeIdea, bases map[string]BaseScore, band int) []warning {
	b, ok := bases[strings.ToUpper(strings.TrimSpace(idea.Ticker))]
	if !ok {
		return []warning{{Ticker: idea.Ticker,
			Message: "no computed base score for this ticker — confidence is unanchored"}}
	}
	if band <= 0 {
		band = 10
	}
	idea.BaseConfidence = b.For(idea.Direction)
	idea.DomainScores = b.Domains

	lo, hi := idea.BaseConfidence-band, idea.BaseConfidence+band
	if b.Cap > 0 && hi > b.Cap {
		hi = b.Cap // the band may not be used to climb over a coverage cap
	}
	if lo < 0 {
		lo = 0
	}
	if hi > 100 {
		hi = 100
	}
	if idea.Confidence >= lo && idea.Confidence <= hi {
		return nil
	}
	was := idea.Confidence
	if idea.Confidence > hi {
		idea.Confidence = hi
	} else {
		idea.Confidence = lo
	}
	msg := fmt.Sprintf("confidence %d is outside the computed base %d ±%d — clamped to %d",
		was, idea.BaseConfidence, band, idea.Confidence)
	// A whole second band beyond the band is not a judgment the arithmetic
	// missed; it means the model scored a different thesis than its own domains
	// reported, and that is worth one more synthesis call to correct.
	if abs(float64(was-idea.BaseConfidence)) > float64(2*band) {
		msg = "far " + msg
	}
	if b.Cap > 0 {
		msg += fmt.Sprintf(" (coverage cap %d, %.0f%% of domain weight)", b.Cap, b.CoveredWeight*100)
	}
	if b.Direction != "" && b.Direction != idea.Direction {
		msg += fmt.Sprintf(" — the domains read %s, so a %s starts from zero", b.Direction, idea.Direction)
	}
	return []warning{{Ticker: idea.Ticker, Message: msg}}
}
