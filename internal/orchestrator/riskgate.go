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
	out = append(out, checkEventWindow(idea, v.Events)...)
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
