package scoreboard

import (
	"sort"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// Beta-hedged excess: how much of a call's excess is selection, and how much is
// the book simply carrying more market than its benchmark?
//
// The plain excess subtracts one unit of benchmark from every call. A long in a
// name whose beta is 1.6 beats that in any rising market without anyone having
// selected anything, and a short in the same name loses to it; the backtest's
// longs earned +1.32% excess against the shorts' +0.11%, which is the shape that
// exposure produces. Hedging each call at its own beta separates the two: what
// is left is the part of the move the market does not explain.
//
// The beta is point in time. It is estimated from the betaLookback daily
// returns that ended at the anchor date, on the same series the call is scored
// on, so nothing the window later revealed can reach it.

// betaLookback is the number of daily returns the beta is estimated over: about
// six months, long enough to be stable and short enough to be this name's
// current relation to its market.
const betaLookback = 120

// minBetaReturns is the fewest aligned returns a beta is estimated from — the
// same floor internal/quant applies to its own benchmark beta.
const minBetaReturns = 60

// HedgedRecord is an arm's average beta-hedged excess over the calls that had a
// beta. Its N can be smaller than the arm's: a call without enough history
// before its anchor has an excess and no beta.
type HedgedRecord struct {
	N               int     `json:"n"`
	AvgBeta         float64 `json:"avg_beta"`
	AvgHedgedExcess float64 `json:"avg_hedged_excess_pct"`
}

// betaBefore estimates s's beta to b from the aligned daily log returns up to
// and including date.
func betaBefore(s, b *quant.Series, date string) (float64, bool) {
	if s == nil || b == nil || date == "" {
		return 0, false
	}
	upTo := func(x *quant.Series) *quant.Series {
		i := sort.Search(len(x.Bars), func(i int) bool { return x.Bars[i].Date > date })
		return &quant.Series{Symbol: x.Symbol, Bars: x.Bars[:i]}
	}
	ra, rb := quant.AlignedReturns(upTo(s), upTo(b))
	if len(ra) > betaLookback {
		ra, rb = ra[len(ra)-betaLookback:], rb[len(rb)-betaLookback:]
	}
	if len(ra) < minBetaReturns {
		return 0, false
	}
	var ma, mb float64
	for i := range ra {
		ma += ra[i]
		mb += rb[i]
	}
	n := float64(len(ra))
	ma, mb = ma/n, mb/n
	var cov, vb float64
	for i := range ra {
		cov += (ra[i] - ma) * (rb[i] - mb)
		vb += (rb[i] - mb) * (rb[i] - mb)
	}
	if vb == 0 {
		return 0, false
	}
	return cov / vb, true
}

// hedgedExcess is the direction-signed return less beta units of the benchmark.
// pct is already signed for the call; bench is the benchmark's raw move. As in
// measureHorizon, a short is hedged by adding the benchmark back: a short in a
// high-beta name that fell only as far as its beta implied selected nothing.
func hedgedExcess(dir model.Direction, pct, bench, beta float64) float64 {
	if dir == model.DirectionSell {
		return round2(pct + beta*bench)
	}
	return round2(pct - beta*bench)
}

// hedgedRecord averages the closed, hedged calls among es.
func hedgedRecord(es []Entry) *HedgedRecord {
	var r HedgedRecord
	var beta, ex float64
	for _, e := range es {
		if !e.CallDone || !e.CallHedged {
			continue
		}
		r.N++
		beta += e.CallBeta
		ex += e.CallHedgedPct
	}
	if r.N == 0 {
		return nil
	}
	r.AvgBeta = round2(beta / float64(r.N))
	r.AvgHedgedExcess = round2(ex / float64(r.N))
	return &r
}
