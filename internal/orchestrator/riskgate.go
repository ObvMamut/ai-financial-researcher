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
	// expectancyPaths is the Monte-Carlo sample size. Five thousand paths puts
	// the standard error on a hit probability under one point, which is finer
	// than any decision made from it.
	expectancyPaths = 5000
	// riskGateMaxSectorShare is how many of the top ideas may share a sector
	// before the book is one bet in several tickets.
	riskGateMaxSectorShare = 2
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
)

// riskFinding is one gate violation.
type riskFinding struct {
	Ticker string
	// Message is written to be usable verbatim as a re-prompt instruction.
	Message string
	// Hard marks a finding that costs the idea its place if a corrective
	// re-prompt does not fix it. Book-level findings are not hard: dropping a
	// sound idea because of its neighbour is not a risk control.
	Hard bool
}

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
	out = append(out, gateBook(res, v, cfg)...)
	return out
}

// riskDefaults fills a zero-valued policy so the gate is never silently
// disabled by a missing config block.
func riskDefaults(c model.RiskConfig) model.RiskConfig {
	if c.AccountEquity <= 0 {
		c.AccountEquity = 100000
	}
	if c.RiskPerTradePct <= 0 {
		c.RiskPerTradePct = 0.5
	}
	if c.CostBps <= 0 {
		c.CostBps = 30
	}
	if c.RRMin <= 0 {
		c.RRMin = 1.8
	}
	if c.StopSigmaMin <= 0 {
		c.StopSigmaMin = 1.0
	}
	if c.StopSigmaMax <= 0 {
		c.StopSigmaMax = 2.0
	}
	if c.TargetSigmaMax <= 0 {
		c.TargetSigmaMax = 3.5
	}
	if c.ADVMinUSD <= 0 {
		c.ADVMinUSD = defaultADVMinUSD
	}
	if c.MaxPairCorr <= 0 {
		c.MaxPairCorr = 0.75
	}
	if c.MaxPortfolioBeta <= 0 {
		c.MaxPortfolioBeta = 1.5
	}
	if c.EdgeSigmaDaily <= 0 {
		c.EdgeSigmaDaily = defaultEdgeSigmaDaily
	}
	return c
}

// gateIdea runs the per-idea checks and computes sizing and expectancy.
func gateIdea(idea *model.TradeIdea, v verified, cfg model.RiskConfig) []riskFinding {
	var out []riskFinding
	hard := func(format string, args ...any) {
		out = append(out, riskFinding{Ticker: idea.Ticker, Hard: true, Message: fmt.Sprintf(format, args...)})
	}
	soft := func(format string, args ...any) {
		out = append(out, riskFinding{Ticker: idea.Ticker, Message: fmt.Sprintf(format, args...)})
	}

	// Sizing is computed whatever else is wrong: a share count is what makes an
	// idea actionable, and "half size" is not a position.
	sizeIdea(idea, cfg)

	if idea.Entry <= 0 || idea.Stop <= 0 || idea.Target <= 0 {
		hard("%s has no usable levels (entry %.2f / stop %.2f / target %.2f) — every idea needs all three",
			idea.Ticker, idea.Entry, idea.Stop, idea.Target)
		return out
	}

	risk := math.Abs(idea.Entry - idea.Stop)
	reward := math.Abs(idea.Target - idea.Entry)
	if risk > 0 {
		idea.BreakevenWinRate = risk / (risk + reward)
		if rr := reward / risk; rr < cfg.RRMin {
			hard("%s: reward:risk is %.2f, below the hard floor of %.2f — move the target out or the stop in",
				idea.Ticker, rr, cfg.RRMin)
		}
	}

	m, haveQuant := quantFor(v, idea.Ticker)
	if !haveQuant {
		soft("%s has no verified price data, so its levels cannot be checked against realized volatility", idea.Ticker)
		return out
	}

	if m.AvgDollarVol20 > 0 && m.AvgDollarVol20 < cfg.ADVMinUSD {
		hard("%s trades $%.0fM a day, under the $%.0fM liquidity floor — it cannot be sized",
			idea.Ticker, m.AvgDollarVol20/1e6, cfg.ADVMinUSD/1e6)
	}

	h := float64(idea.TimeframeDays)
	if h <= 0 {
		h = 10
	}
	unit := m.SigmaDaily * math.Sqrt(h) * m.LastClose // 1σ of the holding period, in price
	if unit <= 0 {
		return out
	}
	if s := risk / unit; s < cfg.StopSigmaMin {
		hard("%s: the stop is %.2fσ from entry, inside the %.1fσ floor — that is noise, not risk (1σ over %.0f days ≈ %.2f)",
			idea.Ticker, s, cfg.StopSigmaMin, h, unit)
	} else if s > cfg.StopSigmaMax {
		hard("%s: the stop is %.2fσ from entry, beyond the %.1fσ ceiling — oversized risk (1σ over %.0f days ≈ %.2f)",
			idea.Ticker, s, cfg.StopSigmaMax, h, unit)
	}
	if s := reward / unit; s > cfg.TargetSigmaMax {
		hard("%s: the target is %.2fσ away, beyond the %.1fσ ceiling — a %.0f-day move that size is not a plan",
			idea.Ticker, s, cfg.TargetSigmaMax, h)
	}

	// Expectancy. A geometry can satisfy every band above and still lose money,
	// because the probability of touching a near stop before a far target is
	// exactly what the bands do not measure.
	ec := cfg
	if v.RealizedR != nil {
		if e, ok := realizedEdgeSigma(idea, m, *v.RealizedR, h); ok {
			ec.EdgeSigmaDaily = e
		}
	}
	ev := simulateExpectancy(idea, m, ec, int(h))
	idea.ExpectancyBps = math.Round(ev*10) / 10
	if ev <= 0 {
		hard("%s: simulated expectancy is %+.0f bps net of %.0f bps costs — the geometry loses money at the assumed edge (breakeven win rate %.0f%%)",
			idea.Ticker, ev, cfg.CostBps, idea.BreakevenWinRate*100)
	}

	out = append(out, checkEventWindow(idea, v.Events)...)
	out = append(out, checkFabricatedDates(idea, v.Dates)...)
	return out
}

// edgeSigmaCap bounds the edge imported from the measured record. Beyond about
// 0.05 the expectancy check stops discriminating between geometries at all
// (measured across the range the σ-bands permit), so a lucky sample must not be
// allowed to switch the check off; the same bound applies to a losing sample so
// one bad quarter cannot reject everything on arithmetic alone.
const edgeSigmaCap = 0.05

// realizedEdgeSigma converts the pipeline's measured average R per closed trade
// into the per-day drift, in σ units, that *this* idea's geometry implies.
//
// The simulation's drift is edge·σ per day, so over the holding period it
// accumulates edge·σ·days of return. The record says a trade of this kind
// returns avgR multiples of its own risk, which for this idea is
// avgR·|entry−stop|/entry. Setting the two equal gives the edge. It is a
// derivation, not another prior: every term in it is measured.
func realizedEdgeSigma(idea *model.TradeIdea, m quant.Metrics, avgR, days float64) (float64, bool) {
	if idea.Entry <= 0 || m.SigmaDaily <= 0 || days <= 0 {
		return 0, false
	}
	riskFrac := math.Abs(idea.Entry-idea.Stop) / idea.Entry
	if riskFrac <= 0 {
		return 0, false
	}
	e := avgR * riskFrac / (m.SigmaDaily * days)
	if e > edgeSigmaCap {
		e = edgeSigmaCap
	} else if e < -edgeSigmaCap {
		e = -edgeSigmaCap
	}
	return e, true
}

// sizeIdea converts the account's risk budget and the idea's own stop distance
// into a share count. The Chief used to write "half size" and "full size", which
// are not positions; this is.
func sizeIdea(idea *model.TradeIdea, cfg model.RiskConfig) {
	stopDist := math.Abs(idea.Entry - idea.Stop)
	if idea.Entry <= 0 || stopDist <= 0 {
		return
	}
	budget := cfg.AccountEquity * cfg.RiskPerTradePct / 100
	shares := math.Floor(budget / stopDist)
	// No single idea may become the book. A tight stop on a cheap stock would
	// otherwise size to many times the account.
	if maxShares := math.Floor(cfg.AccountEquity * 0.25 / idea.Entry); shares > maxShares {
		shares = maxShares
	}
	if shares < 1 {
		return
	}
	idea.Shares = int(shares)
	idea.Notional = math.Round(shares*idea.Entry*100) / 100
	idea.RiskAmount = math.Round(shares*stopDist*100) / 100
}

// simulateExpectancy estimates the trade's expected value in basis points of
// entry, by simulating the price path to the first barrier it touches.
//
// A driftless simulation would be vacuous — with no edge, gambler's ruin makes
// expectancy about −cost for every geometry, so the check would reject
// everything. The edge is therefore an explicit prior (cfg.EdgeSigmaDaily); see
// defaultEdgeSigmaDaily for why it is small.
//
// The seed is derived from the idea itself, so the same idea always scores the
// same number: a risk check that answers differently on a re-run is not a check.
func simulateExpectancy(idea *model.TradeIdea, m quant.Metrics, cfg model.RiskConfig, days int) float64 {
	if m.SigmaDaily <= 0 || idea.Entry <= 0 || days <= 0 {
		return 0
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
	var total float64
	for p := 0; p < expectancyPaths; p++ {
		price := idea.Entry
		outcome := 0.0
		hit := false
		for d := 0; d < days; d++ {
			price *= math.Exp(drift + sigma*rng.NormFloat64())
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
				outcome, hit = dir*(price-idea.Entry)/idea.Entry, true
				break
			}
			if (dir > 0 && price >= idea.Target) || (dir < 0 && price <= idea.Target) {
				outcome, hit = dir*(idea.Target-idea.Entry)/idea.Entry, true
				break
			}
		}
		if !hit {
			outcome = dir * (price - idea.Entry) / idea.Entry // marked out at the horizon
		}
		total += outcome
	}
	return (total/expectancyPaths)*10000 - cfg.CostBps
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

// checkEventWindow penalises an idea whose holding period spans a verified
// earnings date it never mentions.
//
// An unresolved binary event inside the window is the largest uncontrolled risk
// a swing trade carries, and until the calendar existed the pipeline had no way
// to know about one: the news persona was asked for earnings dates, and on a
// search-less engine it supplied them from memory.
func checkEventWindow(idea *model.TradeIdea, events map[string]time.Time) []riskFinding {
	date, ok := events[strings.ToUpper(strings.TrimSpace(idea.Ticker))]
	if !ok {
		return nil
	}
	h := idea.TimeframeDays
	if h <= 0 {
		h = 10
	}
	now := time.Now().UTC().Truncate(24 * time.Hour)
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

// isoDate matches the dates an agent writes when it is being specific.
var isoDate = regexp.MustCompile(`\b(20\d{2})-(\d{2})-(\d{2})\b`)

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
		if sectors[s] > riskGateMaxSectorShare {
			out = append(out, riskFinding{Message: fmt.Sprintf(
				"%d of %d ideas are in %s — at most %d may share a sector; replace the weakest with a different one",
				sectors[s], len(res.Ideas), s, riskGateMaxSectorShare)})
		}
	}

	// Portfolio beta: gross exposure to the market, and net directional exposure.
	var grossBeta, netBeta float64
	counted := 0
	for _, idea := range res.Ideas {
		m, ok := quantFor(v, idea.Ticker)
		if !ok || m.Benchmark == "" {
			continue
		}
		dir := 1.0
		if idea.Direction == model.DirectionSell {
			dir = -1
		}
		grossBeta += math.Abs(m.Beta)
		netBeta += dir * m.Beta
		counted++
	}
	if counted > 0 {
		if avg := grossBeta / float64(counted); avg > cfg.MaxPortfolioBeta {
			out = append(out, riskFinding{Message: fmt.Sprintf(
				"the book's average absolute beta is %.2f, above the %.1f ceiling — these are all high-beta names and they will move together",
				avg, cfg.MaxPortfolioBeta)})
		}
		if math.Abs(netBeta/float64(counted)) > cfg.MaxPortfolioBeta {
			out = append(out, riskFinding{Message: fmt.Sprintf(
				"the book's net signed beta is %+.2f per idea, beyond ±%.1f — it is a directional market call, not five trades",
				netBeta/float64(counted), cfg.MaxPortfolioBeta)})
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
	return []riskFinding{{Message: fmt.Sprintf(
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
