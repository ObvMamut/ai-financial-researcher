package orchestrator

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// coveredBy reports whether a role's verified evidence covers one ticker.
//
// Each specialist is grounded by a different artifact: the quant role reads the
// computed metrics pack, everyone else reads the provider pack for their domain.
//
// Macro is the exception — its evidence is the regime, which is not per-ticker.
// But the configured regime source is FRED, and every series in it (DGS10,
// T10Y2Y, CPIAUCSL, UNRATE) describes the *United States*. Treating those as a
// backdrop for every listing let 2330.TW be scored "the strongest macro read in
// the shortlist" off the US 10-year, at full weight, and pushed a
// quant-bearish name into the top five. Macro therefore grounds US listings
// only; a foreign name is a gap like any other.
// Sentiment is the second exception, and for a different reason. Its two sources
// are almost always *present* — nearly every US issuer has recent Form 4 filings
// and a listed option chain — but presence is not evidence. Officers are paid in
// stock and sell on schedules set months earlier, so "0 buys vs N sales" is the
// resting state of the market, and a put/call ratio near 1.0 says nothing. Read
// as evidence anyway, that produced a domain that scored one bullish name in 34
// across four runs and levied a flat ~9 points on every long in the book.
//
// So sentiment is grounded by the *computed* verdict in
// marketdata.HasPositioningSignal, not by whether the fetch returned rows. A name
// whose positioning is quiet is a name this domain has nothing to say about, and
// the honest place for it is `missing`.
func coveredBy(role string, pack *marketdata.DataPack, quantPack *quant.Pack, ticker string) bool {
	t := strings.ToUpper(ticker)
	if marketdata.IsRegimeDomain(role) {
		return marketdata.IsUSListing(t) && len(pack.MacroFacts) > 0
	}
	if role == "quant" {
		if quantPack != nil {
			if _, ok := quantPack.ByTicker[t]; ok {
				return true
			}
		}
		return false
	}
	if !pack.Coverage[t] {
		return false
	}
	if role == "sentiment" {
		return marketdata.HasPositioningSignal(pack.ByTicker[t])
	}
	return true
}

// groundedFor reports whether a role had verified evidence for at least one
// shortlisted ticker. It replaces the old `dataBlock != ""` test, which was true
// for every role the moment any macro fact existed — so a sentiment pack holding
// nothing at all still reported itself grounded.
func groundedFor(role string, pack *marketdata.DataPack, quantPack *quant.Pack) bool {
	for t := range pack.Coverage {
		if coveredBy(role, pack, quantPack, t) {
			return true
		}
	}
	// The quant pack is built outside the provider pack, so check it directly.
	if role == "quant" && quantPack != nil {
		return len(quantPack.ByTicker) > 0
	}
	return false
}

// abstainedFor lists the tickers a role's sources answered for but had nothing
// directional to say about.
//
// It is deliberately not the same thing as being ungrounded, even though both
// send the name to `missing`. A gap is a failure — a fetch that should have
// worked and did not — and degrades the run. An abstention is the system working:
// sentiment looked at two months of Form 4s and an option chain, found scheduled
// disposals and a put/call ratio of 1.22, and correctly declined to call that a
// direction. Counting the second as the first would mark every healthy run
// degraded.
func abstainedFor(role string, pack *marketdata.DataPack, tickers []string) []string {
	if role != "sentiment" {
		return nil
	}
	var out []string
	for _, t := range tickers {
		u := strings.ToUpper(t)
		if pack.Coverage[u] && !marketdata.HasPositioningSignal(pack.ByTicker[u]) {
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out
}

// ungroundedFor lists the requested tickers a role has no verified evidence for.
// This is the set an honest agent must report in its `missing` array.
func ungroundedFor(role string, pack *marketdata.DataPack, quantPack *quant.Pack, tickers []string) []string {
	var out []string
	for _, t := range tickers {
		if !coveredBy(role, pack, quantPack, t) {
			out = append(out, strings.ToUpper(t))
		}
	}
	sort.Strings(out)
	return out
}

// groundableBy reports whether a domain could, in principle, have covered a
// ticker. Yahoo's chart API — which feeds the quant pack — is global, so every
// shortlisted name is groundable for quant. SEC EDGAR and AlphaVantage, which
// feed news, fundamentals and sentiment, are US-only — but they reach a foreign
// listing through its US line where one exists (2330.TW via TSM), so those
// domains are measured against Reachable, not against the listing's own country.
//
// FRED is different: its series describe the US economy itself, not a company,
// and there is no ADR equivalent for a macro backdrop. Macro stays US-listing
// only.
func groundableBy(domain, ticker string) bool {
	if domain == "quant" {
		return true
	}
	if marketdata.IsRegimeDomain(domain) {
		return marketdata.IsUSListing(ticker)
	}
	return marketdata.Reachable(ticker)
}

// domainGap records the groundable tickers one per-ticker domain finished the
// run without covering.
type domainGap struct {
	Domain  string
	Missing []string
}

// coverageGaps lists the per-ticker domains that did not cover every ticker they
// *could* have covered, with the names they missed.
//
// The rule used to be "zero coverage degrades the run", which only fires when a
// domain covers nothing at all. That is why the first full DeepSeek run reported
// `outcome: complete` while the Chief Analyst called it "SEVERELY DEGRADED": news
// covered 4/12, which is not zero. But 4/12 was really 4 of the *6 groundable*
// names — GE and PLTR were lost to AlphaVantage rate limiting, and nothing caught
// it. Measuring against the achievable subset makes `complete` mean "we got
// everything we could get", which is the honest claim.
//
// macro used to be excluded on the grounds that its evidence is not per-ticker.
// It is now measured like the rest: FRED is a US source, so a US name macro
// failed to ground is a real gap, and a FRED outage degrades the run instead of
// passing silently as `grounded: true` forever.
// A name a domain deliberately stood down on is not a gap — see abstainedFor.
func coverageGaps(statuses []model.DomainStatus) []domainGap {
	var out []domainGap
	for _, s := range statuses {
		abstained := make(map[string]bool, len(s.Abstained))
		for _, t := range s.Abstained {
			abstained[strings.ToUpper(t)] = true
		}
		var missed []string
		for _, t := range s.Ungrounded {
			if groundableBy(s.Domain, t) && !abstained[strings.ToUpper(t)] {
				missed = append(missed, strings.ToUpper(t))
			}
		}
		if len(missed) == 0 {
			continue
		}
		sort.Strings(missed)
		out = append(out, domainGap{Domain: s.Domain, Missing: missed})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}

// confabulationThreshold is the share of a domain's own scored names that has to
// be invented before the run stops describing itself as complete. Half is chosen
// because it is the point past which the report is more assertion than evidence:
// on 2026-09-01 macro scored twelve names and six of them were deleted.
const confabulationThreshold = 0.5

// domainConfabulation is what one domain's structured tail asserted that its
// evidence did not support.
//
// enforceSpecialistTail already deleted every one of these scores and logged a
// warn: line about it — and then it stopped. The run's own verdict was driven
// only by coverage gaps, so metadata.json for that run carried
// domains[macro].corrected_scores with six names in it alongside "warnings": []
// and "outcome": "complete". Half a domain's output was invented and the
// headline said everything was fine. A gap and a confabulation are the same class
// of fact, and the confabulation is the worse of the two, because a gap is
// honest.
type domainConfabulation struct {
	Domain string
	// NoData lists names the domain scored with no verified data behind them.
	NoData []string
	// OffShortlist lists symbols it scored that the run never asked about.
	OffShortlist []string
	// SelfContradicted lists names it scored and declared missing in the same
	// report.
	SelfContradicted []string
	// Overridden lists names it scored after the app's computed verdict said to
	// stand down. The score is deleted like the rest, but this is a disagreement
	// about a verdict rather than an invention — the data was there — so it is
	// reported and never degrades the run.
	Overridden []string
	// Scored is how many names the tail scored in total: the denominator without
	// which none of the above means anything.
	Scored int
}

func (c domainConfabulation) invented() int {
	return len(c.NoData) + len(c.OffShortlist) + len(c.SelfContradicted)
}

// severe reports whether enough of this domain's output was invented to degrade
// the run.
func (c domainConfabulation) severe() bool {
	return c.Scored > 0 && float64(c.invented()) >= confabulationThreshold*float64(c.Scored)
}

// messages renders one line per kind of removal, each naming its own names.
// Lumping them together would say the untrue thing about at least two of them.
func (c domainConfabulation) messages() []string {
	var out []string
	add := func(names []string, what string) {
		if len(names) == 0 {
			return
		}
		out = append(out, fmt.Sprintf("%s scored %d of its %d name(s) %s — scores removed: %s",
			c.Domain, len(names), c.Scored, what, strings.Join(names, ", ")))
	}
	add(c.NoData, "with no verified data behind them")
	add(c.OffShortlist, "that were never on the shortlist")
	add(c.SelfContradicted, "that its own report also declared missing")
	add(c.Overridden, "the computed verdict told it to stand down on")
	return out
}

// confabulations lists the domains whose tails had to be corrected, worst first
// by domain name for a stable artifact.
func confabulations(statuses []model.DomainStatus) []domainConfabulation {
	var out []domainConfabulation
	for _, s := range statuses {
		abstained := make(map[string]bool, len(s.Abstained))
		for _, t := range s.Abstained {
			abstained[strings.ToUpper(t)] = true
		}
		c := domainConfabulation{
			Domain:           s.Domain,
			OffShortlist:     s.OffShortlistScores,
			SelfContradicted: s.SelfContradictedScores,
			Scored:           s.ScoredNames,
		}
		for _, t := range s.CorrectedScores {
			if abstained[strings.ToUpper(t)] {
				c.Overridden = append(c.Overridden, t)
			} else {
				c.NoData = append(c.NoData, t)
			}
		}
		if c.invented() == 0 && len(c.Overridden) == 0 {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}

// quantOnlyNames lists the shortlisted tickers no per-ticker provider can reach,
// sorted. SEC EDGAR and AlphaVantage are US-only, so news, fundamentals and
// sentiment are structurally unable to cover a foreign listing with no US line —
// that is a known limit of this run's sources, not a fetch failure, and the run
// should say so. A name that resolves to a US line (2330.TW → TSM) is reachable
// and does not belong on this list.
func quantOnlyNames(shortlist []model.Candidate) []string {
	var out []string
	for _, c := range shortlist {
		if !marketdata.Reachable(c.Ticker) {
			out = append(out, strings.ToUpper(c.Ticker))
		}
	}
	sort.Strings(out)
	return out
}
