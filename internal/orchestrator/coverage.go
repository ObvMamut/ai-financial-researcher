package orchestrator

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/parse"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// coveredBy reports whether a role's verified evidence covers one ticker.
//
// Each specialist is grounded by a different artifact: the quant role reads the
// computed metrics pack, everyone else reads the provider pack for their domain.
// Macro is the exception — its evidence is the regime (rates, spreads, CPI),
// which is not per-ticker at all, so macro facts ground every name or none.
func coveredBy(role string, pack *marketdata.DataPack, quantPack *quant.Pack, ticker string) bool {
	if marketdata.IsRegimeDomain(role) {
		return len(pack.MacroFacts) > 0
	}
	t := strings.ToUpper(ticker)
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
	// Macro's evidence is not keyed by ticker, and the quant pack is built
	// outside the provider pack; check both directly.
	if marketdata.IsRegimeDomain(role) {
		return len(pack.MacroFacts) > 0
	}
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

// overclaimedCoverage returns the ungrounded tickers an agent left out of its
// `missing` array — names it had no verified data for but wrote up anyway.
//
// A report with no parseable JSON tail returns nothing: that is a parse problem,
// reported elsewhere, not a coverage claim.
func overclaimedCoverage(report string, ungrounded []string) []string {
	if len(ungrounded) == 0 {
		return nil
	}
	raw, ok := parse.LastJSONBlock(report)
	if !ok {
		return nil
	}
	var res struct {
		Missing []string `json:"missing"`
	}
	if json.Unmarshal([]byte(raw), &res) != nil {
		return nil
	}
	claimed := make(map[string]bool, len(res.Missing))
	for _, m := range res.Missing {
		claimed[strings.ToUpper(strings.TrimSpace(m))] = true
	}
	var out []string
	for _, t := range ungrounded {
		if !claimed[t] {
			out = append(out, t)
		}
	}
	return out
}

// zeroCoverage lists the per-ticker domains that finished the run with no
// verified data for a single shortlisted name. Their reports are written
// entirely from model recollection, however confident they read — so a run
// containing one is degraded. Before this, only an outright agent failure could
// stop a run claiming `complete`: the first full DeepSeek run reported
// `complete` on 0/12 sentiment coverage and 1/12 news coverage.
//
// macro is excluded because its evidence is the market regime (rates, spreads,
// CPI), which is not per-ticker at all.
func zeroCoverage(statuses []model.DomainStatus) []string {
	var out []string
	for _, s := range statuses {
		if marketdata.IsRegimeDomain(s.Domain) || s.Grounded {
			continue
		}
		out = append(out, s.Domain)
	}
	sort.Strings(out)
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
