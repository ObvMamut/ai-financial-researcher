package backtest

import (
	"math"

	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// E3 (docs/workflow/backtest.md, "E3: which side carries the result"): the
// barrier study's picks were already split long vs short on plain excess
// (Top-5 15-session excess, gross: +1.316% long / +0.111% short in the
// 2026-09-23 run). This adds the beta-adjusted split that split was missing,
// and both the plain and beta-adjusted splits per half, so the pre-registered
// rule below can be evaluated once the lab runs again.
//
// Decision rule, registered before this runs on real data: if shorts are ≤0
// beta-adjusted in both halves, pre-register "long-only merit_veto" as a
// config test for the live shipped arm.

// SideStats is one slice's top-picksPerIndex excess split by direction: the
// mean of dir*excess over the longs and over the shorts, plain (XS) and
// beta-adjusted (BX). NLong/NShort count every pick on that side, whether or
// not its excess happened to be computable (the barrier study's own Longs
// count works the same way).
type SideStats struct {
	NLong         int `json:"n_long"`
	NShort        int `json:"n_short"`
	LongPlainPct  Num `json:"long_plain_pct"`
	ShortPlainPct Num `json:"short_plain_pct"`
	LongBetaPct   Num `json:"long_beta_adj_pct"`
	ShortBetaPct  Num `json:"short_beta_adj_pct"`
}

// SidesReport is E3's three slices: the whole sample and its two halves. No
// per-region breakdown — the task asked only for overall and per half.
type SidesReport struct {
	All SideStats `json:"all"`
	H1  SideStats `json:"h1"`
	H2  SideStats `json:"h2"`
}

// sidesStudy reuses pickTrades' selection unchanged — the composite's five
// largest |score| names per index per week, the same trades the barrier study
// walks — and reports their 15-session directional excess split long vs
// short, plain and beta-adjusted, overall and per half. It duplicates neither
// the top-5 selection (pickTrades) nor the beta adjustment (observe's BX):
// both are read off the trade, not recomputed here.
func sidesStudy(recs []Record, series map[string]*quant.Series, mid string) SidesReport {
	trades := pickTrades(recs, series)
	side := func(keep func(trade) bool) SideStats {
		var st SideStats
		var longPlain, shortPlain, longBeta, shortBeta []float64
		for _, t := range trades {
			if !keep(t) {
				continue
			}
			if t.dir > 0 {
				st.NLong++
				if !math.IsNaN(t.xs15) {
					longPlain = append(longPlain, t.dir*t.xs15)
				}
				if !math.IsNaN(t.bx15) {
					longBeta = append(longBeta, t.dir*t.bx15)
				}
			} else {
				st.NShort++
				if !math.IsNaN(t.xs15) {
					shortPlain = append(shortPlain, t.dir*t.xs15)
				}
				if !math.IsNaN(t.bx15) {
					shortBeta = append(shortBeta, t.dir*t.bx15)
				}
			}
		}
		mlp, _ := meanSD(longPlain)
		msp, _ := meanSD(shortPlain)
		mlb, _ := meanSD(longBeta)
		msb, _ := meanSD(shortBeta)
		st.LongPlainPct, st.ShortPlainPct = Num(100*mlp), Num(100*msp)
		st.LongBetaPct, st.ShortBetaPct = Num(100*mlb), Num(100*msb)
		return st
	}
	return SidesReport{
		All: side(func(trade) bool { return true }),
		H1:  side(func(t trade) bool { return t.date < mid }),
		H2:  side(func(t trade) bool { return t.date >= mid }),
	}
}
