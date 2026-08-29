package orchestrator

import (
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
	return pack.Coverage[t]
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
// feed news, fundamentals and sentiment, are US-only, and so is FRED, which
// feeds macro.
func groundableBy(domain, ticker string) bool {
	if domain == "quant" {
		return true
	}
	return marketdata.IsUSListing(ticker)
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
func coverageGaps(statuses []model.DomainStatus) []domainGap {
	var out []domainGap
	for _, s := range statuses {
		var missed []string
		for _, t := range s.Ungrounded {
			if groundableBy(s.Domain, t) {
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

// quantOnlyNames lists the shortlisted tickers no per-ticker provider can reach,
// sorted. SEC EDGAR and AlphaVantage are US-only, so news, fundamentals and
// sentiment are structurally unable to cover a foreign listing — that is a known
// limit of this run's sources, not a fetch failure, and the run should say so.
func quantOnlyNames(shortlist []model.Candidate) []string {
	var out []string
	for _, c := range shortlist {
		if !marketdata.IsUSListing(c.Ticker) {
			out = append(out, strings.ToUpper(c.Ticker))
		}
	}
	sort.Strings(out)
	return out
}
