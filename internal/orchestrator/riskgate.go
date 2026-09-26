package orchestrator

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// The risk gate is deterministic and runs after synthesis. Every rule here used
// to be a sentence in a persona, expressed as a preference — and a model asked
// for "usually 1–2 σ" and "risk_reward ≥ 1.5 preferred" satisfies it at the
// cheapest edge of the band. The 2026-08 runs came back with a cluster of
// 1.0-σ stops and reward:risk ratios between 1.52 and 1.61, which is not a
// coincidence and not analysis. Numbers the app enforces cannot be gamed by
// wording.
const (
	// expectancyPaths is the Monte-Carlo sample size, counted in antithetic
	// *pairs* — each draw is used as both z and -z, so the run is 2x this many
	// paths and the sampling error in the drift cancels between the halves.
	//
	// Five thousand independent paths put the standard error on a hit
	// probability under one point, which was the number the comment here
	// justified. But the quantity actually compared to the floor is the mean
	// P&L, whose standard error at that sample was +/-6 bps — comparable to the
	// floor itself, so a near-floor verdict was decided by the hash of the
	// ticker string rather than by the geometry. Ten thousand antithetic pairs
	// put it under one basis point.
	expectancyPaths = 10000
	// defaultMaxPerSector is how many of the top ideas may share a sector
	// before the book is one bet in several tickets. Exposed as
	// risk.max_per_sector so the shortlist's own sector cap can be derived from
	// it rather than guessed at.
	defaultMaxPerSector = 2
	// DefaultMaxPerSector mirrors defaultMaxPerSector under an exported name so
	// other packages — the backtest lab's live-book replay (internal/backtest)
	// — can build a live-shaped sector cap without hard-copying the number.
	DefaultMaxPerSector = defaultMaxPerSector
	// defaultEdgeSigmaDaily is the daily expected return assumed in the
	// expectancy simulation, in units of σ_daily.
	//
	// It has to be small, and the reason is measurable. A driftless simulation
	// is vacuous — gambler's ruin puts every geometry at about −cost. But so is
	// an optimistic one in the other direction: measured across the geometries
	// the bands permit, an edge of 0.05σ/day puts every single one between +97
	// and +149 bps, so the check would never fire. At 0.02σ/day (≈0.03%/day for
	// a typical name, still a generous 7-8% a year) the same geometries spread
	// from +10 to +64 bps and the ordering is informative. Phase 5 replaces the
	// prior with this system's own realized hit rate.
	defaultEdgeSigmaDaily = 0.02
	// defaultMinExpectancyR is the floor a geometry's simulated expectancy has
	// to clear, as a multiple of the trade's own risk.
	//
	// A floor of 10 bps of *entry* was tried first and was not a test of the
	// construction at all. Expectancy in bps is proportional to the stop
	// distance, so the check collapsed to roughly 200*sigma_daily(%)*days - 33
	// and graded volatility: on 2026-09-01 five ideas with near-identical
	// normalised geometry (stop ~1.3 sigma, target ~2.6 sigma, R:R ~1.9,
	// breakeven ~34.5%) scored +28.7, +21.4, +8.2, +4.6 and -3.1 bps in exact
	// order of sigma_daily, and the three calmest were dropped — the calm trends
	// the pre-screen exists to find. In R, and with costs scaled to liquidity,
	// the same five run +0.032, +0.045, +0.039, +0.015 and +0.009 (see
	// TestTheBookTheExpectancyGateRefused): still ordered, because a fixed cost
	// really is a larger share of a tighter stop, but no longer a sign change.
	//
	// The floor is set where the number stops being distinguishable from zero,
	// and nowhere above it. The antithetic sample puts the standard error under
	// a basis point — about 0.001R — so 0.005R is several errors clear of zero
	// while still refusing a geometry that only breaks even.
	//
	// It is deliberately not a quality bar. The *level* of this distribution is
	// set by EdgeSigmaDaily, which is an assumed prior; a floor placed inside an
	// assumed distribution measures the assumption. Only the ordering is earned,
	// and only until the measured record replaces the level (blendRealized).
	// Setting the previous floor at 10 bps — inside the observed 3.0-24.1 spread
	// of one book — is exactly that mistake, and it cost the next run three of
	// its five ideas.
	defaultMinExpectancyR = 0.005
	// defaultCatastropheStopSigma is the nearest a market_on_open idea's stop
	// may sit, in units of σ_daily·√h. The 2026-09-23 backtest (4,080 trades,
	// next-open entry, 15-session hold, 30 bps costs) measured no stop at
	// +0.71% a trade, a 2σ√h stop at +0.61%, 1σ√h at +0.49% and the old
	// ~9%/15% stop/target at +0.30%: every nearer stop and every target cost
	// return. Two sigma is the widest stop that still bounds a gap-and-run.
	defaultCatastropheStopSigma = 2.0
	// catastropheStopCeiling bounds a market_on_open stop at this multiple of
	// the floor. Past it the "stop" no longer describes a loss anyone would
	// sit through, and the risk budget would size the position to almost
	// nothing.
	catastropheStopCeiling = 2.0
	// realizedPriorPairs is the weight, in closed trades, given to the simulated
	// expectancy once a measured record exists. At n_closed == this the record
	// and the simulation count equally; the record's share grows from there.
	realizedPriorPairs = 30
)

// costTiers scales the round-trip cost assumption by how liquid the name is.
//
// cfg.CostBps is the cost of trading a name at the liquidity floor — 30 bps of
// spread, commission and slippage on a $20M-a-day listing is realistic. Charging
// the same 30 bps to a mega-cap is not, and because the expectancy check is a
// net-of-cost number, the flat assumption taxed exactly the largest and most
// liquid names hardest relative to their (smaller) stop distances.
var costTiers = []struct {
	minADVUSD float64
	scale     float64
}{
	{1e9, 0.35},
	{2e8, 0.6},
	{5e7, 0.85},
}

// effectiveCostBps is the round-trip cost charged to one idea. With no ADV
// figure it is the configured cost unscaled: an unknown name is assumed to be
// the expensive kind.
func effectiveCostBps(cfg model.RiskConfig, m quant.Metrics) float64 {
	adv := m.AvgDollarVol20USD
	if adv <= 0 {
		return cfg.CostBps
	}
	for _, t := range costTiers {
		if adv >= t.minADVUSD {
			return cfg.CostBps * t.scale
		}
	}
	return cfg.CostBps
}

// riskFinding is one gate violation.
type riskFinding struct {
	Ticker string
	// Message is written to be usable verbatim as a re-prompt instruction.
	Message string
	// Hard marks a finding that costs the idea its place if a corrective
	// re-prompt does not fix it. Book-level findings are not hard: dropping a
	// sound idea because of its neighbour is not a risk control.
	Hard bool
	// Observational marks a finding the Chief Analyst cannot act on, so it is
	// logged and warned about but never spent on the single corrective call.
	//
	// Every finding used to be appended to the re-prompt under the heading
	// "CRITICAL: Your previous output failed validation" — including
	// noteDirectionBalance, whose own comment says it is deliberately log-only
	// because forcing balance manufactures a bad trade; "has no verified price
	// data, so its levels cannot be checked", which is a fact about the run's
	// data and not about the output; and sizeIdea's "raise account_equity or
	// risk_per_trade_pct", which is an instruction to the operator. Asking a
	// model to fix any of those spends the one re-prompt on nothing and invites
	// it to rewrite the ideas that were already sound.
	Observational bool
	// Blocked names what kind of failure this is, for the decision the finding
	// produces: a plan review whose call never returned is a research failure,
	// not a risk verdict, and recording it as one hides that the pipeline broke.
	// Empty means the ordinary risk/construction case.
	Blocked string
}

// actionable reports whether a finding names something the Chief can change by
// re-emitting its JSON.
func (f riskFinding) actionable() bool { return !f.Observational }

// applyRiskGate scores, sizes and checks the final ideas.
//
// It always computes expectancy and position size (an idea that survives should
// arrive with its share count already worked out); it returns the violations for
// the caller to either re-prompt on or act upon.
func applyRiskGate(res *model.IdeasResult, v verified, cfg model.RiskConfig) []riskFinding {
	if res == nil || len(res.Ideas) == 0 {
		return nil
	}
	cfg = riskDefaults(cfg)

	var out []riskFinding
	for i := range res.Ideas {
		out = append(out, gateIdea(&res.Ideas[i], v, cfg)...)
	}
	// The evidence floor is a run-level policy rather than part of an idea's
	// geometry, and it does not apply in single-stock mode: the user named the
	// ticker, so refusing to answer because the news feed had nothing is a
	// non-answer rather than a risk control. In independent mode the pipeline
	// chose the name out of a shortlist of twelve and can simply choose another.
	if model.Mode(res.Mode) != model.ModeSingle {
		// An idea with no recorded domain scores is only a finding when the run
		// produced scores for *something*. If nothing in the book has any, the
		// scoring stage did not run — the degraded path, or a synthesis with no
		// base scores to anchor to — and that is a fault in the run rather than
		// in each idea. Dropping the whole book on it would replace a warning
		// anchorConfidence already gives with an empty result.
		scored := false
		for i := range res.Ideas {
			if len(res.Ideas[i].DomainScores) > 0 {
				scored = true
				break
			}
		}
		if scored {
			for i := range res.Ideas {
				if msg := checkPriceOnlyEvidence(&res.Ideas[i]); msg != "" {
					out = append(out, riskFinding{Ticker: res.Ideas[i].Ticker, Hard: true, Message: msg})
				}
			}
		}
	}
	out = append(out, checkFabricatedNoteDates(res, v.Dates)...)
	out = append(out, gateBook(res, v, cfg)...)
	return out
}

// checkFabricatedNoteDates runs the same date scan over the book-level notes.
//
// checkFabricatedDates only ever read `why` and `position_note`, so `notes` — the
// one place the model writes freely, and the part a reader trusts most — was
// unchecked. The 2026-09-01 run shipped 1,400 characters of dated factual
// narrative there, including two dates nothing had verified.
//
// It carries no confidence penalty because there is no single idea to dock: it is
// a book-level finding, so it becomes a warning and a re-prompt reason.
func checkFabricatedNoteDates(res *model.IdeasResult, verifiedDates map[string]bool) []riskFinding {
	if len(verifiedDates) == 0 || strings.TrimSpace(res.Notes) == "" {
		return nil
	}
	var unknown []string
	seen := map[string]bool{}
	for _, m := range isoDate.FindAllString(res.Notes, -1) {
		if verifiedDates[m] || seen[m] {
			continue
		}
		seen[m] = true
		unknown = append(unknown, m)
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	verb := "appears"
	if len(unknown) > 1 {
		verb = "appear"
	}
	return []riskFinding{{Message: fmt.Sprintf(
		"your `notes` cite %s, which %s in no verified fact this run collected — remove the date or drop the claim; the notes are read as the run's own account of itself",
		strings.Join(unknown, ", "), verb)}}
}

// validateRiskPolicy refuses a σ band no geometry can satisfy.
//
// config.validateRisk already refuses a ceiling of zero, but it cannot check
// ordering: it runs inside config.Load, *before* riskDefaults fills the keys the
// operator left unset, so it only ever sees one side of the band. The realistic
// footgun is writing one side — `stop_sigma_min = 2.5` against the default
// ceiling of 2.0 — and seeing it from config would mean duplicating riskgate's
// constants there, which is the drift the presence-detection work removed. So
// the check lives here, where both effective values exist, and runs on the
// already-defaulted policy.
//
// It is an error rather than a warning because the alternative is what the
// footgun does today: a clean load, then every idea dropped by gateIdea and
// reported as an ordinary run of risk-gate findings, and a shipped empty book
// that looks like the Chief simply wrote nothing sound.
//
// Only strictly unsatisfiable orderings are refused. A band whose floor equals
// its ceiling is absurd but not meaningless — it says "exactly this far" — and
// this function's job is the settings that cannot mean anything, not the ones
// that are merely a bad idea.
func validateRiskPolicy(c model.RiskConfig) error {
	// crossed names the two keys in the operator's terms. A key they never wrote
	// is called a default, because "you set stop_sigma_max = 2.0" is a confusing
	// thing to read when you did not.
	crossed := func(key string, set bool, v float64) string {
		if set {
			return fmt.Sprintf("%s = %g", key, v)
		}
		return fmt.Sprintf("%s = %g (the default, which you did not set)", key, v)
	}

	if c.StopSigmaMin > c.StopSigmaMax {
		return fmt.Errorf("config risk: %s is above %s — no stop can be both at least %gσ and at most %gσ from entry, so every idea would be dropped by the risk gate and the run would ship an empty book",
			crossed("stop_sigma_min", c.Set("stop_sigma_min"), c.StopSigmaMin),
			crossed("stop_sigma_max", c.Set("stop_sigma_max"), c.StopSigmaMax),
			c.StopSigmaMin, c.StopSigmaMax)
	}
	// A target ceiling below the stop floor bounds reward under risk, so the
	// best geometry the band allows still has a reward:risk under 1.
	if c.TargetSigmaMax < c.StopSigmaMin {
		return fmt.Errorf("config risk: %s is below %s — the target may not reach as far as the stop must, so no idea can clear a reward:risk of 1, let alone rr_min",
			crossed("target_sigma_max", c.Set("target_sigma_max"), c.TargetSigmaMax),
			crossed("stop_sigma_min", c.Set("stop_sigma_min"), c.StopSigmaMin))
	}
	return nil
}

// riskDefaults fills a zero-valued policy so the gate is never silently
// disabled by a missing config block.
//
// "Unfilled" is decided by model.RiskConfig.Set, not by the value: every field
// here is a float64 whose zero is also a legal setting, and a floor of zero is
// how an operator disables a floor. Testing `<= 0` instead made that
// unsayable — `cost_bps = 0` came back as 30 — and it did so *after* the config
// loader had already discarded the same value, so fixing one layer alone would
// have changed nothing. A config the operator never wrote carries no explicit
// keys, so a struct built in code (a test, a run with no [risk] block) defaults
// exactly as it always did.
func riskDefaults(c model.RiskConfig) model.RiskConfig {
	fill := func(dst *float64, key string, def float64) {
		if !c.Set(key) && *dst <= 0 {
			*dst = def
		}
	}
	fill(&c.AccountEquity, "account_equity", 100000)
	fill(&c.RiskPerTradePct, "risk_per_trade_pct", 0.5)
	fill(&c.CostBps, "cost_bps", 30)
	fill(&c.RRMin, "rr_min", 1.8)
	fill(&c.StopSigmaMin, "stop_sigma_min", 1.0)
	fill(&c.StopSigmaMax, "stop_sigma_max", 2.0)
	fill(&c.TargetSigmaMax, "target_sigma_max", 3.5)
	// A long may bid up to 1.5σ√5 below the close and a short offer the same
	// distance above; either may only reach 0.5σ√5 the other way. See
	// isPatientEntry.
	fill(&c.EntryPatienceSigma, "entry_patience_sigma", 1.5)
	fill(&c.EntryChaseSigma, "entry_chase_sigma", 0.5)
	fill(&c.CatastropheStopSigma, "catastrophe_stop_sigma", defaultCatastropheStopSigma)
	if c.EntryType == "" {
		c.EntryType = model.EntryMarketOnOpen
	}
	fill(&c.ADVMinUSD, "adv_min_usd", defaultADVMinUSD)
	fill(&c.MaxPairCorr, "max_pair_corr", 0.75)
	fill(&c.MaxPortfolioBeta, "max_portfolio_beta", 1.5)
	if !c.Set("max_per_sector") && c.MaxPerSector <= 0 {
		c.MaxPerSector = defaultMaxPerSector
	}
	// EdgeSigmaDaily is a prior rather than a limit, so a negative value is a
	// real setting — the system is losing money — and must not be read as
	// "unfilled" the way a negative cost would be.
	if !c.Set("edge_sigma_daily") && c.EdgeSigmaDaily == 0 {
		c.EdgeSigmaDaily = defaultEdgeSigmaDaily
	}
	if !c.Set("min_expectancy_r") && c.MinExpectancyR == 0 {
		c.MinExpectancyR = defaultMinExpectancyR
	}
	// MinExpectancyBps is left alone: its default *is* zero, so there is nothing
	// to fill in. It is a secondary floor now, and "loses money outright" is the
	// only verdict a figure denominated in basis points of entry can carry on
	// its own.
	return c
}

// applyEntryPolicy stamps how a newly generated idea enters and, for a
// market_on_open idea, re-bases its levels on verified data. It returns one
// message per change, for the run's warnings.
//
// It runs on every idea a Chief produces, in both research modes, before the
// levels are validated. Ideas already on disk never pass through here, so an
// ideas.json written before entry_type existed keeps replaying as the limit it
// was.
//
// Market-on-open is the default because the live record's limits were
// adversely selected — calls whose limit never traded made +3.09%, the filled
// ones −0.90% — and a limit at the close fills at the next open anyway. So the
// entry is not the model's to choose: it is the verified last close, the price
// the stop and the share count are measured from. The stop is floored at
// catastrophe_stop_sigma·σ_daily·√h from it, widening a nearer or wrong-side
// stop in Go rather than spending the one corrective re-prompt on it: the
// floor is a number the backtest chose, not a judgement the Chief can improve.
func applyEntryPolicy(idea *model.TradeIdea, v verified, cfg model.RiskConfig) []string {
	cfg = riskDefaults(cfg)
	if cfg.EntryType == model.EntryLimit {
		idea.EntryType = model.EntryLimit
		return nil
	}
	idea.EntryType = model.EntryMarketOnOpen
	m, ok := quantFor(v, idea.Ticker)
	if !ok || m.LastClose <= 0 {
		return nil // the gate reports the missing price data
	}
	var msgs []string
	ref := math.Round(m.LastClose*100) / 100
	if idea.Entry > 0 && math.Abs(idea.Entry-ref) >= 0.005 {
		msgs = append(msgs, fmt.Sprintf("entry %.2f replaced by the verified last close %.2f — a market-on-open idea fills at the next open, and its entry is only the reference price",
			idea.Entry, ref))
	}
	idea.Entry = ref
	if m.SigmaDaily <= 0 {
		return msgs // the gate reports that volatility could not be computed
	}
	h := float64(idea.TimeframeDays)
	if h <= 0 {
		h = 10
	}
	// Measured on the unrounded close, the same unit gateIdea checks against.
	floor := cfg.CatastropheStopSigma * m.SigmaDaily * math.Sqrt(h) * m.LastClose
	var widened float64
	if idea.Direction == model.DirectionSell {
		if idea.Stop <= 0 || idea.Stop-ref < floor {
			widened = math.Ceil((ref+floor)*100) / 100
		}
	} else if idea.Stop <= 0 || ref-idea.Stop < floor {
		widened = math.Floor((ref-floor)*100) / 100
	}
	if widened > 0 {
		msgs = append(msgs, fmt.Sprintf("stop %.2f widened to %.2f, the %.1fσ√%.0f catastrophe-stop floor from the reference close %.2f",
			idea.Stop, widened, cfg.CatastropheStopSigma, h, ref))
		idea.Stop = widened
	}
	return msgs
}

// gateIdea runs the per-idea checks and computes sizing and expectancy.
func gateIdea(idea *model.TradeIdea, v verified, cfg model.RiskConfig) []riskFinding {
	var out []riskFinding
	hard := func(format string, args ...any) {
		out = append(out, riskFinding{Ticker: idea.Ticker, Hard: true, Message: fmt.Sprintf(format, args...)})
	}
	if v.Thesis && (idea.TimeframeDays < 10 || idea.TimeframeDays > 15) {
		hard("%s: holding window must be 10–15 sessions", idea.Ticker)
		return out
	}
	// note records something the run should surface but the Chief cannot fix.
	note := func(format string, args ...any) {
		out = append(out, riskFinding{Ticker: idea.Ticker, Observational: true, Message: fmt.Sprintf(format, args...)})
	}

	// Sizing is computed whatever else is wrong: a share count is what makes an
	// idea actionable, and "half size" is not a position.
	//
	// A sizing failure is addressed to the operator ("raise account_equity"), not
	// to the model: the Chief cannot change the account.
	m, haveQuant := quantFor(v, idea.Ticker)
	if msg := sizeIdea(idea, m, cfg); msg != "" {
		note("%s", msg)
	}

	moo := idea.MarketOnOpen()
	if moo {
		// A market_on_open idea needs a reference price and a stop; a target
		// is optional and informational (applyEntryPolicy, scoring.md).
		if idea.Entry <= 0 || idea.Stop <= 0 {
			hard("%s has no usable levels (entry %.2f / stop %.2f) — a market-on-open idea needs its reference close and a catastrophe stop",
				idea.Ticker, idea.Entry, idea.Stop)
			return out
		}
	} else if idea.Entry <= 0 || idea.Stop <= 0 || idea.Target <= 0 {
		hard("%s has no usable levels (entry %.2f / stop %.2f / target %.2f) — every idea needs all three",
			idea.Ticker, idea.Entry, idea.Stop, idea.Target)
		return out
	}

	// A stale bar is a corrigible construction fault, not just something to
	// mention afterwards.
	//
	// The pipeline already refetches once and flags the metric, and the run
	// warns when a stale name ships — all of which is correct and none of which
	// reaches the Chief in a form it can act on. On 2026-09-03 NESTE.HE shipped
	// at rank 4 with entry, stop and target computed to the cent off a session
	// that had already been superseded. This is a soft finding on purpose: the
	// levels can be moved to the newer close and the thesis kept, or the idea
	// dropped, and both are answers the Chief is better placed to choose between
	// than a blanket rule is.
	if staleFor(v, idea.Ticker) {
		out = append(out, riskFinding{Ticker: idea.Ticker, Message: fmt.Sprintf(
			"%s is priced off a bar that trails its own market's last completed session, so its entry, stop and target are computed from a close that has been superseded — re-price it against the newest bar in the quant block, or drop it",
			idea.Ticker)})
	}

	risk := math.Abs(idea.Entry - idea.Stop)
	reward := math.Abs(idea.Target - idea.Entry)
	if moo {
		// No take-profit binds, so there is no reward:risk to floor and no
		// breakeven hit rate the geometry alone implies. A target the Chief
		// chose to state anyway is recorded as it stands, unjudged.
		idea.BreakevenWinRate = 0
	} else if risk > 0 {
		// Rounded like every neighbouring field: the raw quotient shipped as
		// 0.3511450381679389 in ideas.json beside notionals rounded to the cent.
		idea.BreakevenWinRate = math.Round(risk/(risk+reward)*10000) / 10000
		if rr := reward / risk; !v.Thesis && rr < cfg.RRMin {
			hard("%s: reward:risk is %.2f, below the hard floor of %.2f — move the target out or the stop in",
				idea.Ticker, rr, cfg.RRMin)
		}
	}

	if !haveQuant {
		// A statement about what this run could fetch, not about what the Chief
		// wrote. Re-emitting the JSON cannot conjure the price history.
		note("%s has no verified price data, so its levels cannot be checked against realized volatility", idea.Ticker)
		return out
	}

	// The floor is stated in dollars, so it is compared against the converted
	// figure. Comparing it against the raw one — which is Σ close·volume in the
	// listing's own currency — let a Tokyo name turning over ¥20M a day clear a
	// $20M floor by a factor of 150.
	switch {
	case m.AvgDollarVol20USD > 0 && m.AvgDollarVol20USD < cfg.ADVMinUSD:
		hard("%s trades $%.0fM a day, under the $%.0fM liquidity floor — it cannot be sized",
			idea.Ticker, m.AvgDollarVol20USD/1e6, cfg.ADVMinUSD/1e6)
	case m.AvgDollarVol20USD <= 0 && m.AvgDollarVol20 > 0:
		hard("%s turns over %.0fM %s a day but no %s/USD rate was available, so its tradeable size cannot be established against the $%.0fM floor",
			idea.Ticker, m.AvgDollarVol20/1e6, m.Currency, m.Currency, cfg.ADVMinUSD/1e6)
	}

	h := float64(idea.TimeframeDays)
	if h <= 0 {
		h = 10
	}

	// The event and date checks come first because they do not depend on
	// volatility, and they used to sit after the `unit <= 0` return below — so a
	// name whose σ could not be computed silently lost its earnings-window check
	// and its fabricated-date check as well as its geometry checks, and shipped
	// with no finding of any kind. An idea nothing could be verified about must
	// not be indistinguishable from one that passed.
	out = append(out, checkEventWindowAt(idea, v.Events, v.AsOf)...)
	out = append(out, checkFabricatedDates(idea, v.Dates)...)

	unit := m.SigmaDaily * math.Sqrt(h) * m.LastClose // 1σ of the holding period, in price
	if unit <= 0 {
		// Observational: the Chief cannot supply a volatility this run failed to
		// compute. It is recorded so the gap is visible in the warnings rather
		// than absent from them.
		note("%s: realized volatility is not computable from its bars, so the stop/target σ bands and the expectancy simulation were not run on it",
			idea.Ticker)
		return out
	}
	if moo {
		// The catastrophe stop replaces both the stop band and the target
		// band. applyEntryPolicy has already widened a nearer stop to the
		// floor, so a finding here means the levels arrived by some other
		// route.
		floor := cfg.CatastropheStopSigma
		if s := risk / unit; s < floor-1e-6 {
			hard("%s: the stop is %.2fσ from the reference close, inside the %.1fσ catastrophe-stop floor (1σ over %.0f days ≈ %.2f)",
				idea.Ticker, s, floor, h, unit)
		} else if s > catastropheStopCeiling*floor {
			hard("%s: the stop is %.2fσ from the reference close, beyond %.1fσ — that is not a stop the position would be held through, and the risk budget sizes it to almost nothing (1σ over %.0f days ≈ %.2f)",
				idea.Ticker, s, catastropheStopCeiling*floor, h, unit)
		}
	} else if s := risk / unit; !v.Thesis && s < cfg.StopSigmaMin {
		hard("%s: the stop is %.2fσ from entry, inside the %.1fσ floor — that is noise, not risk (1σ over %.0f days ≈ %.2f)",
			idea.Ticker, s, cfg.StopSigmaMin, h, unit)
	} else if s > cfg.StopSigmaMax {
		hard("%s: the stop is %.2fσ from entry, beyond the %.1fσ ceiling — oversized risk (1σ over %.0f days ≈ %.2f)",
			idea.Ticker, s, cfg.StopSigmaMax, h, unit)
	}
	if s := reward / unit; !moo && s > cfg.TargetSigmaMax {
		hard("%s: the target is %.2fσ away, beyond the %.1fσ ceiling — a %.0f-day move that size is not a plan",
			idea.Ticker, s, cfg.TargetSigmaMax, h)
	}

	// Expectancy. A geometry can satisfy every band above and still lose money,
	// because the probability of touching a near stop before a far target is
	// exactly what the bands do not measure.
	cost := effectiveCostBps(cfg, m)
	evBps, evR := simulateExpectancy(idea, m, cfg, cost, int(h))
	if v.RealizedR != nil {
		evR = blendRealized(evR, *v.RealizedR, v.RealizedN)
	}
	idea.ExpectancyBps = math.Round(evBps*10) / 10
	idea.ExpectancyR = math.Round(evR*1000) / 1000
	if moo {
		// Recorded, never gated. The check exists to catch a stop/target
		// geometry that loses money at the assumed edge — a near stop touched
		// before a far target. A market_on_open idea has one barrier, placed
		// at a floor the backtest chose, and a time exit; the simulation's
		// level is then set by the assumed edge alone, and a floor inside an
		// assumed distribution measures the assumption (see
		// defaultMinExpectancyR). The number stays on the idea as a diagnostic.
		return out
	}
	if !v.Thesis && evR < cfg.MinExpectancyR {
		verdict := "the geometry loses money at the assumed edge"
		if evR > 0 {
			verdict = "which is indistinguishable from zero once costs are paid"
		}
		hard("%s: simulated expectancy is %+.3fR (%+.1f bps net of %.0f bps costs), under the %.3fR floor — %s (breakeven win rate %.0f%%). The lever is the holding period, not the reward:risk ratio: a tighter stop is touched more often and lowers this number",
			idea.Ticker, evR, evBps, cost, cfg.MinExpectancyR, verdict, idea.BreakevenWinRate*100)
	} else if !v.Thesis && cfg.MinExpectancyBps > 0 && evBps < cfg.MinExpectancyBps {
		hard("%s: simulated expectancy is %+.1f bps net of %.0f bps costs, under the configured %.0f bps floor (breakeven win rate %.0f%%)",
			idea.Ticker, evBps, cost, cfg.MinExpectancyBps, idea.BreakevenWinRate*100)
	}
	return out
}

// blendRealized moves the simulated expectancy toward the pipeline's own
// measured average R, weighted by how many closed trades stand behind it.
//
// The record used to be imported as a *drift*: avgR·riskFrac/(σ·days), clamped
// at 0.05σ/day. Every term was measured, but the arithmetic had a cliff in it.
// For any realistic avgR that expression lands far above the clamp — with the
// 2026-09-01 book and avgR 0.49 it gave 0.157 to 0.206 on every idea — so the
// 30th closed trade would have flipped the check from rejecting most of the book
// to never firing at all, with no regime in between.
//
// Blending instead is a measurement doing what a measurement can do. The
// simulation supplies the per-idea discrimination the record cannot (it is one
// book-wide number); the record supplies the level the simulation can only
// assume. At n == realizedPriorPairs they count equally and the record's share
// grows from there, so a negative record does pull the gate toward refusing —
// which is the correct response to a system that is losing money — without one
// sample being able to switch the check off in either direction.
func blendRealized(simR, avgR float64, n int) float64 {
	if n <= 0 {
		return simR
	}
	w := float64(n) / float64(n+realizedPriorPairs)
	return w*avgR + (1-w)*simR
}

// sizeIdea converts the account's risk budget and the idea's own stop distance
// into a share count. The Chief used to write "half size" and "full size", which
// are not positions; this is.
//
// The account is in USD and the levels are in whatever the listing trades in, so
// the budget is converted into the local currency before it meets the local stop
// distance, and the resulting exposure is reported back in USD. Doing this
// arithmetic in mixed units is how the 2026-09-01 run shipped its first and
// third ideas with no position at all: Tokyo Electron's ¥8,000 stop distance
// against a "500" budget floors to zero shares, as does SoftBank's ¥840.
//
// It returns a message when no whole share can be bought, because that outcome
// used to be a bare `return` — and Shares/Notional/RiskAmount are `omitempty`,
// so the idea shipped looking complete with the sizing simply absent.
func sizeIdea(idea *model.TradeIdea, m quant.Metrics, cfg model.RiskConfig) string {
	stopDist := math.Abs(idea.Entry - idea.Stop)
	if idea.Entry <= 0 || stopDist <= 0 {
		return ""
	}

	// An empty currency means no metrics at all for this name — quantFor returned
	// a zero value — and the gate has already said so in its own finding. Falling
	// back to the account's currency is the pre-FX behaviour and right for the US
	// names that dominate. It is *not* the silent default this change removes:
	// a name whose currency is known but unconvertible carries the code with a
	// zero rate, and is refused below rather than assumed to be dollars.
	ccy, rate := m.Currency, m.FXToUSD
	if ccy == "" {
		ccy, rate = "USD", 1
	}
	if rate <= 0 {
		return fmt.Sprintf("%s cannot be sized: its levels are in %s and no %s/USD rate was available this run",
			idea.Ticker, ccy, ccy)
	}
	idea.Currency = ccy

	// The budget is USD; entry and stop are local. Convert the budget, not the
	// prices — the prices are what an order is placed at.
	budgetLocal := cfg.AccountEquity * cfg.RiskPerTradePct / 100 / rate
	shares := math.Floor(budgetLocal / stopDist)
	// No single idea may become the book. A tight stop on a cheap stock would
	// otherwise size to many times the account.
	maxShares := math.Floor(cfg.AccountEquity * 0.25 / rate / idea.Entry)
	if shares > maxShares {
		shares = maxShares
	}
	if shares < 1 {
		limit, why := budgetLocal, "the per-trade risk budget"
		if maxShares < 1 {
			limit, why = cfg.AccountEquity*0.25/rate, "the 25%-of-account position cap"
		}
		return fmt.Sprintf(
			"%s cannot be sized at one whole share: %s is %.0f %s against an entry of %.2f and a stop %.2f away — raise account_equity or risk_per_trade_pct, or drop the idea",
			idea.Ticker, why, limit, ccy, idea.Entry, stopDist)
	}
	idea.Shares = int(shares)
	// Exposure is reported in USD so five ideas across four currencies add up.
	idea.Notional = math.Round(shares*idea.Entry*rate*100) / 100
	idea.RiskAmount = math.Round(shares*stopDist*rate*100) / 100
	return ""
}

// simulateExpectancy estimates the trade's expected value by simulating the
// price path to the first barrier it touches. It returns the figure twice: in
// basis points of entry, and as a multiple of the trade's own risk.
//
// The second is the one the gate judges. The first is proportional to the stop
// distance and so measures the name's volatility as much as the construction;
// it is kept because it is what an operator reads and what past runs recorded.
//
// A driftless simulation would be vacuous — with no edge, gambler's ruin makes
// expectancy about −cost for every geometry, so the check would reject
// everything. The edge is therefore an explicit prior (cfg.EdgeSigmaDaily); see
// defaultEdgeSigmaDaily for why it is small.
//
// The seed is derived from the idea itself, so the same idea always scores the
// same number: a risk check that answers differently on a re-run is not a check.
// Draws are antithetic — every z is walked as both +z and −z — which cancels the
// sampling error in the drift between the two halves of each pair.
func simulateExpectancy(idea *model.TradeIdea, m quant.Metrics, cfg model.RiskConfig, costBps float64, days int) (bps, r float64) {
	if m.SigmaDaily <= 0 || idea.Entry <= 0 || days <= 0 {
		return 0, 0
	}
	riskFrac := math.Abs(idea.Entry-idea.Stop) / idea.Entry
	if riskFrac <= 0 {
		return 0, 0
	}
	dir := 1.0
	if idea.Direction == model.DirectionSell {
		dir = -1
	}
	sigma := m.SigmaDaily
	// The Itô correction matters here. Stepping with exp(μ + σz) grows the
	// *expected price* at μ + σ²/2, which handed every idea about 50 bps of free
	// return over ten days at these volatilities — and more the more volatile the
	// name, which is precisely backwards. Subtracting σ²/2 makes the assumed edge
	// mean what it says: an expected return of edge × σ per day.
	drift := cfg.EdgeSigmaDaily*sigma*dir - sigma*sigma/2

	rng := rand.New(rand.NewSource(ideaSeed(idea)))
	draws := make([]float64, days)
	var total float64
	for p := 0; p < expectancyPaths; p++ {
		for d := range draws {
			draws[d] = rng.NormFloat64()
		}
		total += walkPath(idea, drift, sigma, dir, draws, 1)
		total += walkPath(idea, drift, sigma, dir, draws, -1)
	}
	mean := total / float64(2*expectancyPaths)
	bps = mean*10000 - costBps
	return bps, bps / (riskFrac * 10000)
}

// walkPath runs one path and returns its direction-aware return as a fraction of
// entry. sign flips the whole draw sequence, which is what makes the pair
// antithetic.
func walkPath(idea *model.TradeIdea, drift, sigma, dir float64, draws []float64, sign float64) float64 {
	price := idea.Entry
	for _, z := range draws {
		price *= math.Exp(drift + sigma*sign*z)
		// Stop first when a single daily step spans both barriers: within
		// the day we cannot know the order, and assuming the good one is
		// how a backtest flatters itself.
		//
		// A breached stop fills at the price that breached it, not at the
		// stop level: a daily step that jumps from 92 to 85 through a stop
		// at 90 loses 15%, not 10%. Booking the barrier instead was worth a
		// spurious +41 bps here — the simulation was paying itself the gap
		// risk it exists to measure. A target is the other way round: a
		// limit order at that price fills at that price or better, so the
		// target is booked exactly.
		if (dir > 0 && price <= idea.Stop) || (dir < 0 && price >= idea.Stop) {
			return dir * (price - idea.Entry) / idea.Entry
		}
		if idea.Target > 0 && ((dir > 0 && price >= idea.Target) || (dir < 0 && price <= idea.Target)) {
			return dir * (idea.Target - idea.Entry) / idea.Entry
		}
	}
	return dir * (price - idea.Entry) / idea.Entry // marked out at the horizon
}

func ideaSeed(idea *model.TradeIdea) int64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s|%s|%.4f|%.4f|%.4f|%d",
		strings.ToUpper(idea.Ticker), idea.Direction, idea.Entry, idea.Stop, idea.Target, idea.TimeframeDays)
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

// eventAcknowledgement is what an idea must contain somewhere in its own words
// for a scheduled event inside the window to count as accounted for. It is
// deliberately generous: the check exists to catch silence, not to grade prose.
var eventAcknowledgement = []string{"earnings", "report", "results", "event", "catalyst"}

// priceDerivedDomains are the scoring domains whose evidence is computed from
// the same price history the pre-screen composite is. A verdict from one of them
// is a reading of the ranking that selected the name, not a second opinion on
// it.
var priceDerivedDomains = map[string]bool{"quant": true}

// checkPriceOnlyEvidence refuses an idea that no domain outside the price series
// could see.
//
// The base score already prices thin coverage — a domain with no data votes
// zero, so such a name scores low — and `max_thinly_covered` already stops the
// *shortlist* filling with names most domains must abstain on. Neither reaches
// the output, and on 2026-09-04 BAYN.DE and DSFIR.AS shipped at ranks 4 and 5
// with `domain_scores` of quant and macro alone: the composite that selected
// them, and a regime read that had agreed with it on all twelve names. Both
// reported 100% agreement to the Chief, and both were repeat proposals across
// several runs.
//
// A name whose entire case is the ranking that picked it is a screen output, not
// a research conclusion. The pipeline is allowed to have found only three
// tradeable ideas — the Chief's own persona says returning four sound ideas is a
// success and a fifth that fails these tests is worse than nothing — and this is
// one of the ways it says so.
//
// The check reads what a domain *scored*, not what a provider returned: a domain
// that abstained saw the name and had nothing directional to say, which is
// exactly as unhelpful here as never having reached it.
func checkPriceOnlyEvidence(idea *model.TradeIdea) string {
	var priced []string
	for d := range idea.DomainScores {
		if !priceDerivedDomains[d] {
			return ""
		}
		priced = append(priced, d)
	}
	if len(priced) == 0 {
		// No domain scored this name at all. It is the same failure one step
		// further along, and it was the louder one: an idea with an empty
		// `domain_scores` has its confidence clamped to the band around a base
		// of zero and then ships anyway, at rank 3 of 5, looking from the
		// outside exactly like the ideas that had evidence.
		return fmt.Sprintf("%s was scored by no domain at all — its confidence is anchored to a base of zero. "+
			"Drop it, and if nothing else clears the bar, ship fewer ideas and say so in notes", idea.Ticker)
	}
	sort.Strings(priced)
	return fmt.Sprintf("%s is scored by %s alone — every domain that can see something "+
		"other than its price history abstained or had no data. Its whole case is the ranking "+
		"that selected it. Drop it, and if nothing else clears the bar, ship fewer ideas and say so in notes",
		idea.Ticker, strings.Join(priced, " and "))
}

// checkEventWindow penalises an idea whose holding period spans a verified
// earnings date it never mentions.
//
// An unresolved binary event inside the window is the largest uncontrolled risk
// a swing trade carries, and until the calendar existed the pipeline had no way
// to know about one: the news persona was asked for earnings dates, and on a
// search-less engine it supplied them from memory.
func checkEventWindow(idea *model.TradeIdea, events map[string]time.Time) []riskFinding {
	return checkEventWindowAt(idea, events, time.Time{})
}

func checkEventWindowAt(idea *model.TradeIdea, events map[string]time.Time, asOf time.Time) []riskFinding {
	date, ok := events[strings.ToUpper(strings.TrimSpace(idea.Ticker))]
	if !ok {
		return nil
	}
	h := idea.TimeframeDays
	if h <= 0 {
		h = 10
	}
	if asOf.IsZero() {
		asOf = time.Now()
	}
	now := asOf.UTC().Truncate(24 * time.Hour)
	// Trading days to calendar days: five sessions a week, rounded up so the
	// window is never understated.
	if date.Before(now) || date.After(now.AddDate(0, 0, (h*7+4)/5)) {
		return nil
	}
	text := strings.ToLower(idea.PositionNote + " " + idea.Why)
	for _, word := range eventAcknowledgement {
		if strings.Contains(text, word) {
			return nil
		}
	}
	penalise(idea, 10)
	return []riskFinding{{Ticker: idea.Ticker, Message: fmt.Sprintf(
		"earnings on %s falls inside the %d-day window and neither position_note nor why acknowledges it — confidence reduced by 10",
		date.Format("2006-01-02"), h)}}
}

// collectVerifiedDates adds every date one data pack carries to the run's set of
// dates a report may cite. It is the authority checkFabricatedDates measures
// against, so anything it misses becomes an accusation of invention.
//
// The two sources are not the same thing, and conflating them was the bug. A
// fact's AsOf says *when it was collected*, which is right for a headline (its
// publication time) and wrong for a calendar entry: the "Next earnings" fact for
// MRK on 2026-09-01 carries AsOf 2026-09-01 and the value 2026-10-29. Only AsOf
// was collected, so when the Chief correctly wrote "Earnings 2026-10-29
// [verified] — outside the window", the gate reported it as appearing "in no
// verified fact this run collected". That cost the idea 10 points, spent the run's
// one corrective re-prompt, and talked the Chief into deleting three true earnings
// dates and writing into the shipped notes that the run had never held them —
// leaving five position notes telling a trader no earnings date was verified when
// three were.
func collectVerifiedDates(into map[string]bool, pack *marketdata.DataPack) {
	if pack == nil {
		return
	}
	add := func(t time.Time) {
		if !t.IsZero() {
			into[t.UTC().Format("2006-01-02")] = true
		}
	}
	// The scheduled event itself, which no AsOf carries.
	for _, d := range pack.EventDates {
		add(d)
	}
	for _, td := range pack.ByTicker {
		for _, f := range td.Facts {
			add(f.AsOf)
		}
	}
	for _, f := range pack.MacroFacts {
		add(f.AsOf)
	}
}

// isoDate matches the dates an agent writes when it is being specific.
var isoDate = regexp.MustCompile(`\b(20\d{2})-(\d{2})-(\d{2})\b`)

// collectQuantDates registers every date the computed quant pack puts in front
// of the Chief Analyst.
//
// Only Pack.AsOf — the newest bar across the whole shortlist — used to be
// registered. But CompactLine renders each ticker's own AsOf, RegimeBlock
// renders each benchmark's, and buildQuantPack writes a staleness flag naming
// the session a name is trailing; the Chief reads all three, and
// agents/chief-analyst.md tells it to reason about exactly those dates ("a
// `flags:` entry … a stale last bar … is a reason to lower confidence"). The
// 2026-09-01 run carried both 2026-08-31 and 2026-09-01 per-ticker and escaped a
// false accusation only because an AlphaVantage headline happened to be stamped
// with the older one. A run with no US names, or no AlphaVantage key, would have
// docked the Chief 10 points and spent its one corrective re-prompt for quoting
// a date the app itself wrote into the prompt.
func collectQuantDates(into map[string]bool, p *quant.Pack) {
	if p == nil {
		return
	}
	add := func(m quant.Metrics) {
		if m.AsOf != "" {
			into[m.AsOf] = true
		}
		// The flags are app-written prose, and the dates in them are the app's
		// own — a name trailing its market's last completed session.
		for _, f := range m.Flags {
			for _, d := range isoDate.FindAllString(f, -1) {
				into[d] = true
			}
		}
	}
	for _, m := range p.ByTicker {
		add(m)
	}
	for _, m := range p.Benchmarks {
		add(m)
	}
	if p.AsOf != "" {
		into[p.AsOf] = true
	}
}

// checkFabricatedDates flags a specific date in an idea's prose that appears
// nowhere in the run's verified data.
//
// It warns and penalises rather than rejecting: a date can be legitimately
// derived (an entry window, the end of the holding period), so a false positive
// here is cheap and a silent invention is not. A dated claim is the most
// persuasive thing a report can carry and the easiest to make up.
func checkFabricatedDates(idea *model.TradeIdea, verifiedDates map[string]bool) []riskFinding {
	if len(verifiedDates) == 0 {
		return nil // nothing to check against; silence beats a blanket accusation
	}
	var unknown []string
	seen := map[string]bool{}
	for _, m := range isoDate.FindAllString(idea.Why+" "+idea.PositionNote, -1) {
		if verifiedDates[m] || seen[m] {
			continue
		}
		seen[m] = true
		unknown = append(unknown, m)
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	penalise(idea, 10)
	return []riskFinding{{Ticker: idea.Ticker, Message: fmt.Sprintf(
		"cites %s, which appears in no verified fact this run collected — confidence reduced by 10",
		strings.Join(unknown, ", "))}}
}

func penalise(idea *model.TradeIdea, points int) {
	idea.Confidence -= points
	if idea.Confidence < 0 {
		idea.Confidence = 0
	}
}

// gateBook runs the checks that are about the set of ideas rather than any one
// of them. None are hard: dropping a sound idea because of its neighbour is not
// a risk control, so these become re-prompt instructions and then warnings.
func gateBook(res *model.IdeasResult, v verified, cfg model.RiskConfig) []riskFinding {
	var out []riskFinding
	if len(res.Ideas) < 2 {
		return nil
	}

	// Same-direction pairs that move together are one position in two tickets.
	for i := 0; i < len(res.Ideas); i++ {
		for j := i + 1; j < len(res.Ideas); j++ {
			a, b := res.Ideas[i], res.Ideas[j]
			if a.Direction != b.Direction {
				continue
			}
			sa := v.Series[strings.ToUpper(a.Ticker)]
			sb := v.Series[strings.ToUpper(b.Ticker)]
			c, ok := quant.Correlation(sa, sb)
			if !ok || c <= cfg.MaxPairCorr {
				continue
			}
			out = append(out, riskFinding{Message: fmt.Sprintf(
				"%s and %s are both %s and their daily returns correlate %.2f — that is one bet in two tickets; swap the weaker one for something that adds breadth",
				a.Ticker, b.Ticker, a.Direction, c)})
		}
	}

	// Sector concentration.
	sectors := map[string]int{}
	for _, idea := range res.Ideas {
		if c, ok := v.Universe.Lookup(idea.Ticker); ok && c.Sector != "" {
			sectors[c.Sector]++
		}
	}
	names := make([]string, 0, len(sectors))
	for s := range sectors {
		names = append(names, s)
	}
	sort.Strings(names)
	for _, s := range names {
		if sectors[s] > cfg.MaxPerSector {
			out = append(out, riskFinding{Message: fmt.Sprintf(
				"%d of %d ideas are in %s — at most %d may share a sector; replace the weakest with a different one",
				sectors[s], len(res.Ideas), s, cfg.MaxPerSector)})
		}
	}

	// Portfolio beta: gross market exposure, and net directional exposure, both
	// beta-adjusted and measured against the account rather than against the
	// number of ideas.
	//
	// This used to divide by the idea count — an average — which made the
	// ceiling dilutable: adding *any* low-beta name lowered it, whatever it did
	// to the book's actual exposure. On 2026-09-03 the first pass failed at 1.62
	// and the Chief's corrective was to drop ORCL (base 38, five domains) and add
	// 035720.KS (base 27, quant only), saying so plainly — "the slot had to be
	// filled by something low-beta rather than left empty." The gate rewarded
	// padding, which is the opposite of a risk limit.
	//
	// Beta-adjusted notional over equity cannot be gamed that way: every idea
	// adds to the numerator and the denominator is the account. Dropping the
	// high-beta name is the only thing that helps, which is the remedy the
	// finding now names. The unit changed with it — the ceiling reads as a
	// multiple of equity, not as a per-idea average — and at five ideas near the
	// 25%-of-account position cap the two scales are close enough that
	// max_portfolio_beta keeps its 1.5 default.
	var grossBeta, netBeta, sized float64
	unsized := 0
	for _, idea := range res.Ideas {
		m, ok := quantFor(v, idea.Ticker)
		if !ok || m.Benchmark == "" {
			continue
		}
		if idea.Notional <= 0 {
			// Sizing could not produce a whole share. The run already warns
			// about that separately; here it only means this idea's exposure is
			// unknown, and counting it as zero would understate the book.
			unsized++
			continue
		}
		dir := 1.0
		if idea.Direction == model.DirectionSell {
			dir = -1
		}
		grossBeta += math.Abs(m.Beta) * idea.Notional
		netBeta += dir * m.Beta * idea.Notional
		sized += idea.Notional
	}
	if equity := cfg.AccountEquity; equity > 0 && sized > 0 {
		caveat := ""
		if unsized > 0 {
			caveat = fmt.Sprintf(" (%d further idea(s) could not be sized and are not counted)", unsized)
		}
		if gross := grossBeta / equity; gross > cfg.MaxPortfolioBeta {
			out = append(out, riskFinding{Message: fmt.Sprintf(
				"the book's beta-adjusted gross exposure is %.2f× the account, above the %.1f ceiling%s — these names will move together, and the fix is to drop the highest-beta idea, not to add a low-beta one: every position adds to this number",
				gross, cfg.MaxPortfolioBeta, caveat)})
		}
		if net := netBeta / equity; math.Abs(net) > cfg.MaxPortfolioBeta {
			out = append(out, riskFinding{Message: fmt.Sprintf(
				"the book's net beta-adjusted exposure is %+.2f× the account, beyond ±%.1f%s — it is a directional market call, not five trades; drop or shrink the largest same-side position, or add the other side",
				net, cfg.MaxPortfolioBeta, caveat)})
		}
	}

	out = append(out, noteDirectionBalance(res, v)...)
	return out
}

// noteDirectionBalance observes, without instructing, when every idea leans the
// same way as the market already does.
//
// Deliberately log-only, and deliberately not a short quota: in the runs this
// system has produced, the one token short was reliably the worst idea in the
// book. Forcing balance manufactures a bad trade; noticing the imbalance is
// worth saying.
func noteDirectionBalance(res *model.IdeasResult, v verified) []riskFinding {
	if v.Quant == nil || len(v.Quant.Benchmarks) == 0 {
		return nil
	}
	buys := 0
	for _, idea := range res.Ideas {
		if idea.Direction == model.DirectionBuy {
			buys++
		}
	}
	if buys != len(res.Ideas) && buys != 0 {
		return nil
	}
	syms := make([]string, 0, len(v.Quant.Benchmarks))
	for s := range v.Quant.Benchmarks {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	var regimes []string
	for _, s := range syms {
		b := v.Quant.Benchmarks[s]
		regimes = append(regimes, fmt.Sprintf("%s %s (63d %+.1f%%)", s, b.Regime, b.Ret63d*100))
	}
	side := "long"
	if buys == 0 {
		side = "short"
	}
	// Observational, which is what "log-only" above has to mean in code: every
	// finding was being appended to the corrective re-prompt, so this one asked
	// the Chief to fix the very imbalance the comment says not to force.
	return []riskFinding{{Observational: true, Message: fmt.Sprintf(
		"note: all %d ideas are %s, against a market read of %s — the book has no hedge if that read is wrong",
		len(res.Ideas), side, strings.Join(regimes, "; "))}}
}

// dropViolating removes the ideas with unresolved hard findings and renumbers
// what is left.
//
// Shipping four ideas is a success, not a shortfall: the count was a target the
// system met by constructing trades to fill it. An idea whose geometry loses
// money is worse than no idea, because it looks like one.
func dropViolating(res *model.IdeasResult, findings []riskFinding) []string {
	drop := map[string]bool{}
	reasons := map[string]string{}
	for _, f := range findings {
		if f.Hard && f.Ticker != "" {
			key := strings.ToUpper(f.Ticker)
			drop[key] = true
			if reasons[key] == "" {
				reasons[key] = f.Message
			}
		}
	}
	if len(drop) == 0 {
		return nil
	}
	kept := make([]model.TradeIdea, 0, len(res.Ideas))
	var dropped []string
	for _, idea := range res.Ideas {
		if drop[strings.ToUpper(idea.Ticker)] {
			dropped = append(dropped, reasons[strings.ToUpper(idea.Ticker)])
			continue
		}
		idea.Rank = len(kept) + 1
		kept = append(kept, idea)
	}
	res.Ideas = kept
	return dropped
}

func quantFor(v verified, ticker string) (quant.Metrics, bool) {
	if v.Quant == nil {
		return quant.Metrics{}, false
	}
	m, ok := v.Quant.ByTicker[strings.ToUpper(strings.TrimSpace(ticker))]
	return m, ok
}
