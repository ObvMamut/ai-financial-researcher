package scoreboard

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"time"
)

// How uncertain an arm's average excess is.
//
// An average of eighty calls reads as a finding whatever it is. Whether it is
// one depends on how much it would move on a different sample of the same
// process, and the counts in this repository are small enough that the honest
// answer is usually "a lot". The interval is the number that says so.
//
// The resampling unit is the week, not the call. Calls made in the same week
// ride the same market, the same sector rotation and often the same names under
// different tickets; treating them as independent draws would shrink the
// interval by a factor of roughly the square root of the calls per week and
// report precision the sample does not have.

// ExcessCI is a percentile bootstrap interval on an average excess, in percent.
type ExcessCI struct {
	Low   float64 `json:"low"`
	High  float64 `json:"high"`
	Weeks int     `json:"weeks"`
}

// Contains reports whether v lies inside the interval.
func (c ExcessCI) Contains(v float64) bool { return c.Low <= v && v <= c.High }

const (
	bootstrapDraws = 4000
	// The seed is fixed so the same history always prints the same interval: an
	// interval that moved between two invocations on unchanged data would be read
	// as the data having changed.
	bootstrapSeed = 20260923
	// ciAlpha is the two-sided tail mass: a 95% interval.
	ciAlpha = 0.05
)

// minCIWeeks is the fewest distinct weeks an interval is computed from. With
// one week there is nothing to resample; with two, every draw is one of three
// averages. Below this the interval is withheld rather than printed tight.
const minCIWeeks = 2

// weekKey is the ISO week a call was generated in.
func weekKey(generatedAt string) (string, bool) {
	d, err := time.Parse("2006-01-02", dateOf(generatedAt))
	if err != nil {
		return "", false
	}
	y, w := d.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w), true
}

// weekSums groups the closed calls' excess by week, as a (sum, count) pair so a
// resample can pool the calls of the weeks it draws rather than averaging week
// means — a week with five calls should weigh five times a week with one.
type weekSum struct {
	sum float64
	n   int
}

func groupByWeek(es []Entry) map[string]weekSum {
	out := map[string]weekSum{}
	for _, e := range es {
		if !e.CallDone {
			continue
		}
		k, ok := weekKey(e.GeneratedAt)
		if !ok {
			continue
		}
		w := out[k]
		w.sum += e.CallExcessPct
		w.n++
		out[k] = w
	}
	return out
}

func weekKeys(ms ...map[string]weekSum) []string {
	seen := map[string]bool{}
	var keys []string
	for _, m := range ms {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// excessCI is the week-clustered bootstrap interval on the mean excess of the
// closed calls in es. ok is false when the calls span fewer than minCIWeeks.
func excessCI(es []Entry) (ExcessCI, bool) {
	weeks := groupByWeek(es)
	keys := weekKeys(weeks)
	if len(keys) < minCIWeeks {
		return ExcessCI{}, false
	}
	rng := rand.New(rand.NewPCG(bootstrapSeed, uint64(len(keys))))
	means := make([]float64, 0, bootstrapDraws)
	for range bootstrapDraws {
		var sum float64
		var n int
		for range keys {
			w := weeks[keys[rng.IntN(len(keys))]]
			sum += w.sum
			n += w.n
		}
		means = append(means, sum/float64(n))
	}
	return percentileCI(means, len(keys)), true
}

// excessDiffCI is the interval on mean(over) − mean(under), resampling the weeks
// of both arms together. The arms are scored over the same calendar, so a week
// the market fell hurts both, and drawing their weeks independently would count
// that shared move as disagreement between them. A draw in which either arm has
// no calls is discarded rather than scored as zero.
func excessDiffCI(over, under []Entry) (ExcessCI, bool) {
	a, b := groupByWeek(over), groupByWeek(under)
	keys := weekKeys(a, b)
	if len(a) < minCIWeeks || len(b) < minCIWeeks {
		return ExcessCI{}, false
	}
	rng := rand.New(rand.NewPCG(bootstrapSeed, uint64(len(keys))))
	diffs := make([]float64, 0, bootstrapDraws)
	for range bootstrapDraws {
		var sa, sb float64
		var na, nb int
		for range keys {
			k := keys[rng.IntN(len(keys))]
			sa += a[k].sum
			na += a[k].n
			sb += b[k].sum
			nb += b[k].n
		}
		if na == 0 || nb == 0 {
			continue
		}
		diffs = append(diffs, sa/float64(na)-sb/float64(nb))
	}
	if len(diffs) < bootstrapDraws/2 {
		return ExcessCI{}, false
	}
	return percentileCI(diffs, len(keys)), true
}

func percentileCI(xs []float64, weeks int) ExcessCI {
	sort.Float64s(xs)
	at := func(q float64) float64 {
		i := int(q * float64(len(xs)-1))
		return round2(xs[i])
	}
	return ExcessCI{Low: at(ciAlpha / 2), High: at(1 - ciAlpha/2), Weeks: weeks}
}
