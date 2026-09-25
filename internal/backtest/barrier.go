package backtest

import (
	"math"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// Barrier study: the composite's top five per index per week, entered at the
// next session's open in the direction of the score's sign and held up to
// barrierHorizon sessions under each exit rule.
const (
	barrierHorizon = 15
	picksPerIndex  = 5
)

// ExitRule is one row of the barrier grid. A zero distance means "no barrier".
type ExitRule struct {
	Label string `json:"label"`
	// StopSigma sets the stop at k·σ_daily·√H from entry; StopFrac at a fixed
	// fraction. At most one is non-zero, and likewise for the target.
	StopSigma float64 `json:"stop_sigma,omitempty"`
	StopFrac  float64 `json:"stop_frac,omitempty"`
	TgtFrac   float64 `json:"target_frac,omitempty"`
}

// ExitRules are the three arms the plan asks the lab to settle: hold to the
// time exit, a catastrophe stop at 2σ·√H, and the live 9% stop / 15% target.
var ExitRules = []ExitRule{
	{Label: "hold (no barriers)"},
	{Label: "2σ·√H stop only", StopSigma: 2},
	{Label: "9% stop / 15% target (current)", StopFrac: 0.09, TgtFrac: 0.15},
}

// ArmStats is one exit rule's outcome over one slice of the trades.
type ArmStats struct {
	N         int `json:"n"`
	MeanPct   Num `json:"mean_net_pct"` // per trade, after 30bp round trip
	Hit       Num `json:"hit_rate"`
	StopShare Num `json:"stop_share"`
	TgtShare  Num `json:"target_share"`
	TimeShare Num `json:"time_share"`
}

// BarrierArm is one exit rule across the whole sample, its halves and sides.
type BarrierArm struct {
	Rule  ExitRule `json:"rule"`
	All   ArmStats `json:"all"`
	H1    ArmStats `json:"h1"`
	H2    ArmStats `json:"h2"`
	Long  ArmStats `json:"long"`
	Short ArmStats `json:"short"`
}

// BarrierReport is the whole grid plus the benchmark-excess view of the same
// picks, which is what separates selection from market direction.
type BarrierReport struct {
	Trades int          `json:"trades"`
	Longs  int          `json:"longs"`
	Arms   []BarrierArm `json:"arms"`
	// XS15GrossPct is the picks' directional 15-session benchmark-excess return
	// from the rebalance close, before costs (the spec's barrier_xs.py).
	XS15GrossPct      Num `json:"xs15_gross_pct"`
	XS15LongGrossPct  Num `json:"xs15_long_gross_pct"`
	XS15ShortGrossPct Num `json:"xs15_short_gross_pct"`
}

type trade struct {
	date  string
	dir   float64
	sigma float64
	path  []quant.Bar // bars P+1 .. P+H
	xs15  float64
	bx15  float64 // beta-adjusted version of xs15 (r.BX[2]); NaN under the same conditions as XS's own BX (E3)
}

// pickTrades selects each (date, index)'s five largest |composite| names with a
// non-zero score and a known σ, and cuts their forward paths.
func pickTrades(recs []Record, series map[string]*quant.Series) []trade {
	type key struct{ date, index string }
	groups := map[key][]Record{}
	var order []key
	for _, r := range recs {
		if s := r.Sig[SigScore]; math.IsNaN(s) || s == 0 || math.IsNaN(r.SigmaDaily) {
			continue
		}
		k := key{r.Date, r.Index}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	var out []trade
	for _, k := range order {
		g := groups[k]
		sort.SliceStable(g, func(i, j int) bool { return math.Abs(g[i].Sig[SigScore]) > math.Abs(g[j].Sig[SigScore]) })
		if len(g) > picksPerIndex {
			g = g[:picksPerIndex]
		}
		for _, r := range g {
			s := series[strings.ToUpper(r.Ticker)]
			if s == nil || r.P+barrierHorizon >= len(s.Bars) {
				continue
			}
			out = append(out, trade{
				date: r.Date, dir: math.Copysign(1, r.Sig[SigScore]), sigma: r.SigmaDaily,
				path: s.Bars[r.P+1 : r.P+1+barrierHorizon], xs15: r.XS[2], bx15: r.BX[2],
			})
		}
	}
	return out
}

// simulate walks one trade through its path. It returns the net return and how
// it ended: 'S' stop, 'W' target, 'T' time exit.
//
// The fill rules follow the spec exactly: entry at the first bar's open; from
// the second bar on, a gap through a barrier exits at the open; an intraday
// touch exits at the barrier; a bar touching both is scored as the stop.
func simulate(t trade, rule ExitRule) (float64, byte) {
	e := t.path[0].Open
	stop := math.Inf(1)
	switch {
	case rule.StopSigma > 0:
		stop = rule.StopSigma * t.sigma * math.Sqrt(barrierHorizon)
	case rule.StopFrac > 0:
		stop = rule.StopFrac
	}
	tgt := math.Inf(1)
	if rule.TgtFrac > 0 {
		tgt = rule.TgtFrac
	}
	d := t.dir
	sp := e * (1 - d*stop)
	tp := e * (1 + d*tgt)
	hasStop, hasTgt := !math.IsInf(stop, 1), !math.IsInf(tgt, 1)

	exit, kind := t.path[len(t.path)-1].Close, byte('T')
	for j, b := range t.path {
		if j > 0 {
			if hasStop && ((d > 0 && b.Open <= sp) || (d < 0 && b.Open >= sp)) {
				exit, kind = b.Open, 'S'
				break
			}
			if hasTgt && ((d > 0 && b.Open >= tp) || (d < 0 && b.Open <= tp)) {
				exit, kind = b.Open, 'W'
				break
			}
		}
		hitS := hasStop && ((d > 0 && b.Low <= sp) || (d < 0 && b.High >= sp))
		hitT := hasTgt && ((d > 0 && b.High >= tp) || (d < 0 && b.Low <= tp))
		if hitS {
			exit, kind = sp, 'S'
			break
		}
		if hitT {
			exit, kind = tp, 'W'
			break
		}
	}
	return d*(exit/e-1) - costPerLeg, kind // one leg, one 30bp round trip
}

func armStats(rets []float64, kinds []byte, keep []bool) ArmStats {
	var st ArmStats
	var sum float64
	var hits, stops, tgts, times int
	for i, r := range rets {
		if !keep[i] {
			continue
		}
		st.N++
		sum += r
		if r > 0 {
			hits++
		}
		switch kinds[i] {
		case 'S':
			stops++
		case 'W':
			tgts++
		default:
			times++
		}
	}
	if st.N == 0 {
		return st
	}
	n := float64(st.N)
	st.MeanPct = Num(100 * sum / n)
	st.Hit, st.StopShare, st.TgtShare, st.TimeShare = Num(float64(hits)/n), Num(float64(stops)/n), Num(float64(tgts)/n), Num(float64(times)/n)
	return st
}

// barrierStudy runs every exit rule over the composite's picks.
func barrierStudy(recs []Record, series map[string]*quant.Series, mid string) BarrierReport {
	trades := pickTrades(recs, series)
	rep := BarrierReport{Trades: len(trades)}
	masks := map[string][]bool{"all": {}, "h1": {}, "h2": {}, "long": {}, "short": {}}
	var xsAll, xsLong, xsShort []float64
	for _, t := range trades {
		masks["all"] = append(masks["all"], true)
		masks["h1"] = append(masks["h1"], t.date < mid)
		masks["h2"] = append(masks["h2"], t.date >= mid)
		masks["long"] = append(masks["long"], t.dir > 0)
		masks["short"] = append(masks["short"], t.dir < 0)
		if t.dir > 0 {
			rep.Longs++
		}
		if !math.IsNaN(t.xs15) {
			v := t.dir * t.xs15
			xsAll = append(xsAll, v)
			if t.dir > 0 {
				xsLong = append(xsLong, v)
			} else {
				xsShort = append(xsShort, v)
			}
		}
	}
	for _, rule := range ExitRules {
		rets := make([]float64, len(trades))
		kinds := make([]byte, len(trades))
		for i, t := range trades {
			rets[i], kinds[i] = simulate(t, rule)
		}
		rep.Arms = append(rep.Arms, BarrierArm{
			Rule:  rule,
			All:   armStats(rets, kinds, masks["all"]),
			H1:    armStats(rets, kinds, masks["h1"]),
			H2:    armStats(rets, kinds, masks["h2"]),
			Long:  armStats(rets, kinds, masks["long"]),
			Short: armStats(rets, kinds, masks["short"]),
		})
	}
	m, _ := meanSD(xsAll)
	ml, _ := meanSD(xsLong)
	ms, _ := meanSD(xsShort)
	rep.XS15GrossPct, rep.XS15LongGrossPct, rep.XS15ShortGrossPct = Num(100*m), Num(100*ml), Num(100*ms)
	return rep
}
