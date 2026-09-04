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

// verified is everything the run computed or fetched that an idea has to be
// consistent with. It is a struct rather than a parameter list because each
// phase of this project has added another authority the Chief's output is
// checked against, and a seven-argument validator invites callers to pass them
// in the wrong order.
type verified struct {
	// Universe is the fallback authority on a name's identity.
	Universe *universe.Universe
	// Quant is the computed price/vol pack; nil when no prices were available.
	Quant *quant.Pack
	// Shortlist is the authority on a name's index and full name — it records
	// which index *this run* screened the name out of.
	Shortlist []model.Candidate
	// Bases are the deterministic weighted domain scores confidence is anchored
	// to.
	Bases []BaseScore
	// Events maps a ticker to its next verified scheduled binary event.
	Events map[string]time.Time
	// Dates is every date any verified fact in this run carried, formatted
	// YYYY-MM-DD. It is what a date in an idea's prose is checked against.
	Dates map[string]bool
	// Series holds the daily bars behind Quant, for the book-level correlation
	// check. Reading them back off disk would answer the same question slower.
	Series map[string]*quant.Series
	// RealizedR is the pipeline's measured average R per closed trade, present
	// only once the scoreboard has enough closed history to mean anything, and
	// RealizedN the number of closed trades behind it. When set, the expectancy
	// check blends the two — including when the record is negative, which is the
	// whole point of measuring.
	RealizedR *float64
	RealizedN int
}

// validateIdeas normalises and checks the Chief Analyst's ideas against
// everything the run itself established.
//
// Enrichment used to come from the universe files and only ever filled an
// *empty* field, so an idea the Chief labelled nq100 for a name the run screened
// out of sp500 kept the wrong label all the way into the scoreboard's per-index
// attribution. Confidence used to be unchecked entirely.
func validateIdeas(res *model.IdeasResult, cfg Config, v verified) []warning {
	var warnings []warning

	byTicker := make(map[string]model.Candidate, len(v.Shortlist))
	for _, c := range v.Shortlist {
		byTicker[strings.ToUpper(strings.TrimSpace(c.Ticker))] = c
	}
	baseByTicker := make(map[string]BaseScore, len(v.Bases))
	for _, b := range v.Bases {
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
			// The setup archetype is always the shortlist's, never the Chief's:
			// it is a classification of measured price data, and the only
			// reason it travels on the idea at all is so the run artifact
			// records which shape was traded.
			idea.Setup = c.Setup
			if c.Index != "" && idea.Index != c.Index {
				if idea.Index != "" {
					warnings = append(warnings, warning{Ticker: idea.Ticker,
						Message: fmt.Sprintf("index %q corrected to %q (the index this run screened it from)", idea.Index, c.Index)})
				}
				idea.Index = c.Index
			}
		}
		if c, ok := v.Universe.Lookup(idea.Ticker); ok {
			if idea.Name == "" {
				idea.Name = c.Name
			}
			if idea.Index == "" {
				idea.Index = c.Index
			}
		}

		// 4. Confidence is anchored to the computed base score.
		warnings = append(warnings, anchorConfidence(idea, baseByTicker, cfg.ChiefAdjustBand)...)

		// 5. Trade mechanics: ordering, plausibility vs verified data, RR.
		warnings = append(warnings, validateLevels(idea, v.Quant, cfg.Risk.EntryPatienceSigma, cfg.Risk.EntryChaseSigma)...)

		validIdeas = append(validIdeas, *idea)
	}

	res.Ideas = validIdeas

	// Sector concentration used to be checked here at "more than 3", and again
	// in the risk gate at "more than 2" — two rules for one question, disagreeing.
	// The gate's is the one that survives.

	return warnings
}

// validateLevels checks an idea's entry/stop/target mechanics. Warn, never
// drop: the human sees the flags and the re-prompt handles hard ordering
// violations. Plausibility checks only fire when verified quant data exists
// for the ticker.
func validateLevels(idea *model.TradeIdea, qp *quant.Pack, patient, chase float64) []warning {
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
	// chief-analyst.md permits a limit entry within a band of the last close — a
	// band in the name's own volatility, not a flat percentage. This check read a
	// flat 5% and claimed in its comment that 5% *was* the rule, so the two
	// disagreed in both directions: on TTD (σ_daily 7.9%) the persona allowed
	// 8.9% and the app warned at 5%, while on AMGN (σ_daily 1.8%) the persona
	// allowed 2.0% and the app waved 5% through.
	//
	// The band is asymmetric, because the two directions are not the same
	// trade. An entry placed *against* the position — a long bidding below the
	// close, a short offering above it — is patience: the worst case is that
	// the limit never trades, which the scoreboard already records as
	// `unfilled` rather than as a loss. An entry placed *with* the position is
	// chasing: paying up for a name that has already moved, where the worst
	// case is a filled position at the top of the move.
	//
	// One band for both had a real cost. It is why the 2026-09-04 run bid 442
	// for AMGN against a 444.12 close — half a percent below the market on a
	// name at 0.993 of its 52-week high — when what the reasoning actually
	// argued for was waiting. Under a symmetric 0.5σ√5 there was no wider bid
	// available to place.
	// Guard the widths rather than trust them. A zero band would warn on every
	// idea including a limit set exactly at the close, and validateIdeas is
	// reachable with a Config that never passed through riskDefaults.
	if patient <= 0 {
		patient = 1.5
	}
	if chase <= 0 {
		chase = 0.5
	}
	dev := idea.Entry/m.LastClose - 1
	band, side := chase, "chasing"
	if isPatientEntry(idea.Direction, dev) {
		band, side = patient, "patient"
	}
	limit := 0.05
	if m.SigmaDaily > 0 {
		limit = band * m.SigmaDaily * math.Sqrt(5)
	}
	if math.Abs(dev) > limit {
		warn("entry %.2f is %.1f%% away from verified last close %.2f (%s), beyond the %.1f%% %s limit-entry band (%.1fσ√5)",
			idea.Entry, math.Abs(dev)*100, m.LastClose, m.AsOf, limit*100, side, band)
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
	idea.Consensus = math.Round(b.Consensus*100) / 100

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

// isPatientEntry reports whether a limit sits on the waiting side of the last
// close for this direction. dev is the entry's signed deviation from that
// close. An entry exactly at the close is neither, and takes the tighter band,
// which changes nothing: zero is inside both.
func isPatientEntry(dir model.Direction, dev float64) bool {
	if dir == model.DirectionSell {
		return dev > 0
	}
	return dev < 0
}
