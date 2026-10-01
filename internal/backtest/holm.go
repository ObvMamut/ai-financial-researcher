package backtest

import (
	"math"
	"sort"
)

// normalCDF is the standard normal distribution function Φ(x).
func normalCDF(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }

// oneSidedP is the p-value of t against a registered positive direction.
func oneSidedP(t float64) float64 { return 1 - normalCDF(t) }

// twoSidedP is the p-value of t with no registered direction.
func twoSidedP(t float64) float64 { return 2 * (1 - normalCDF(math.Abs(t))) }

// signedP is the p-value for a registered direction: +1 upper tail, −1 lower
// tail, 0 two-sided (applyScopedBar's sign convention).
func signedP(t float64, sign int) float64 {
	switch {
	case sign > 0:
		return oneSidedP(t)
	case sign < 0:
		return normalCDF(t)
	}
	return twoSidedP(t)
}

// binomialSignP is the exact one-sided sign-test p-value P(X ≥ pos) for
// X ~ Binomial(n, ½).
func binomialSignP(pos, n int) float64 {
	if pos <= 0 {
		return 1
	}
	if pos > n {
		return 0
	}
	lg := func(k int) float64 { v, _ := math.Lgamma(float64(k) + 1); return v }
	var p float64
	for k := pos; k <= n; k++ {
		p += math.Exp(lg(n) - lg(k) - lg(n-k) - float64(n)*math.Ln2)
	}
	return math.Min(1, p)
}

// holm returns the Holm step-down adjusted p-values, in input order:
// adj_(i) = max over j ≤ i of min(1, (m−j+1)·p_(j)). A NaN p is ranked as 1
// (an untestable statistic is the least significant member of the family, which
// still counts toward its size) and its adjusted value is NaN's stand-in, 1.
func holm(ps []float64) []float64 {
	m := len(ps)
	eff := make([]float64, m)
	idx := make([]int, m)
	for i, p := range ps {
		if math.IsNaN(p) {
			p = 1
		}
		eff[i], idx[i] = p, i
	}
	sort.SliceStable(idx, func(a, b int) bool { return eff[idx[a]] < eff[idx[b]] })
	out := make([]float64, m)
	run := 0.0
	for rank, i := range idx {
		run = math.Max(run, math.Min(1, float64(m-rank)*eff[i]))
		out[i] = run
	}
	return out
}

// HolmRow is one test's line in the Holm table.
type HolmRow struct {
	ID    string `json:"id"`
	Sided string `json:"sided"`
	T     Num    `json:"t"`
	P     Num    `json:"p"`
	PHolm Num    `json:"p_holm"`
	Pass  bool   `json:"pass"`
}

// MultipleTesting is the Holm adjustment over every test the run performed.
type MultipleTesting struct {
	FamilySize int       `json:"family_size"`
	Method     string    `json:"method"`
	Rows       []HolmRow `json:"rows"`
}

// sidedness names how a test's p-value was computed.
func sidedness(t TestResult) string {
	switch t.ID {
	case "E1":
		return "binomial sign test"
	case "E3":
		return "no statistic (p = 1)"
	case "E2-1", "E2-3", "E2-off":
		return "two-sided"
	}
	return "one-sided"
}

// buildHolm sets PHolm on each run test in place (ptrs) and returns the table.
func buildHolm(ptrs []*TestResult) MultipleTesting {
	ps := make([]float64, len(ptrs))
	for i, t := range ptrs {
		ps[i] = float64(t.P)
	}
	adj := holm(ps)
	mt := MultipleTesting{FamilySize: len(ptrs), Method: "Holm step-down", Rows: []HolmRow{}}
	for i, t := range ptrs {
		t.PHolm = Num(adj[i])
		mt.Rows = append(mt.Rows, HolmRow{ID: t.ID, Sided: sidedness(*t), T: t.T, P: t.P, PHolm: t.PHolm, Pass: t.Pass})
	}
	key := func(r HolmRow) float64 {
		if math.IsNaN(float64(r.P)) {
			return 1
		}
		return float64(r.P)
	}
	sort.SliceStable(mt.Rows, func(a, b int) bool {
		ka, kb := key(mt.Rows[a]), key(mt.Rows[b])
		if ka != kb {
			return ka < kb
		}
		return mt.Rows[a].ID < mt.Rows[b].ID
	})
	return mt
}
