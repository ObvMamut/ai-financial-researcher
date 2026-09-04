package orchestrator

import (
	"math"
	"sort"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// Earnings drift: the one signal in the funnel whose horizon is the one this
// system trades.
//
// Every term in the pre-screen composite is a trailing return over three or
// twelve months. Those are real cross-sectional factors and the code that
// computes them already says what is wrong with leaning on them here: a
// twelve-month trend measured to a month ago "is right about the next twelve
// months rather than the next fortnight". Nothing in the ranking was about the
// next fortnight.
//
// Post-earnings announcement drift is. Prices continue in the direction of an
// earnings surprise for weeks after the report, which is exactly the window
// every idea in this pipeline is written for.
//
// # The surprise is measured as the market's reaction, not as a beat or a miss
//
// The reaction *is* the surprise, and it is the better predictor besides: a
// company can beat a consensus everyone had already revised toward and go
// nowhere. It also needs no data this pipeline does not already hold — the price
// bars are fetched for the composite, and the report date comes from one small
// daily index. A consensus-estimate feed would be a new keyed provider bought to
// produce a worse number.
//
// # The event window is two sessions, and that is not a choice about accuracy
//
// EDGAR's index carries the filing *date* and not the hour. A report filed
// before the open is priced that day; one filed after the close is priced the
// next. Measuring day 0 alone silently drops every post-close filer's reaction
// and reads their pre-announcement drift instead. Spanning [0, +1] covers both
// without picking, at the cost of one extra session of noise — which the σ
// normalisation below charges for honestly.

// driftRead is one name's earnings-day repricing and what is left of it.
type driftRead struct {
	// GapZ is the abnormal return over the event window, in units of the name's
	// own daily volatility. Signed: positive is a jump, negative a drop.
	//
	// Abnormal means net of the name's index over the same two sessions. A
	// market that fell 3% on the day of a report did not say anything about the
	// report, and without the adjustment every filer in a bad week reads as a
	// disappointment.
	//
	// Expressed in σ because "a 6% move" is a different event for a utility and
	// for a semiconductor, and the whole pipeline already sizes risk this way.
	GapZ float64
	// PostZ is the move since the event window closed, same units, signed the
	// same way. A negative PostZ against a positive GapZ is the market taking
	// the reaction back.
	PostZ float64
	// Sessions is how many have closed since the event window.
	Sessions int
	// Date is the filing date the window was built around.
	Date string
}

// retraced reports whether the market has given back the whole reaction. There
// is nothing left to drift on, whatever the report said.
func (d driftRead) retraced() bool {
	return d.GapZ != 0 && sameSign(d.PostZ, -d.GapZ) && math.Abs(d.PostZ) >= math.Abs(d.GapZ)
}

// Score is the reaction decayed toward the end of the drift window, and zero
// once the reaction has been retraced or the window has run out.
//
// The decay is linear rather than fitted. Fitting a shape to this pipeline's own
// history would need closed trades it does not have yet — five at the time of
// writing — and a curve fitted to five observations is a decoration on an
// assumption. Linear says the honest thing: a report loses relevance steadily,
// and after a trading month it is not news.
//
// It is deliberately on the same σ scale as the composite it sits beside, so the
// merge can compare a drift candidate against a continuation one without a
// second set of units to reconcile.
func (d driftRead) Score() float64 {
	if d.Sessions < 0 || d.Sessions > driftWindowSessions || d.retraced() {
		return 0
	}
	decay := 1 - float64(d.Sessions)/float64(driftWindowSessions)
	return d.GapZ * decay
}

// computeDrift measures the reaction to a report filed on `date`.
//
// bench may be nil, in which case the raw return is used and the read is worth
// less — a name whose index could not be fetched is compared against nothing.
// ok is false when the window cannot be built at all: no bar on or after the
// filing date, no session after it, or no volatility to scale by.
func computeDrift(s, bench *quant.Series, date time.Time, sigmaDaily float64) (driftRead, bool) {
	if s == nil || sigmaDaily <= 0 {
		return driftRead{}, false
	}
	day := date.Format("2006-01-02")
	i := sort.Search(len(s.Bars), func(i int) bool { return s.Bars[i].Date >= day })
	// The window runs from the close *before* the filing day to the close after
	// it, so it contains the filing day's own move whichever side of the open
	// the report landed on.
	if i <= 0 || i+1 >= len(s.Bars) {
		return driftRead{}, false
	}
	before, after := s.Bars[i-1].Close, s.Bars[i+1].Close
	if before <= 0 || after <= 0 {
		return driftRead{}, false
	}
	gap := after/before - 1
	last := s.Bars[len(s.Bars)-1].Close
	if last <= 0 {
		return driftRead{}, false
	}
	post := last/after - 1

	// Net of the index over the identical dates, so a market-wide move on the
	// day of a report is not read as the report.
	if bench != nil {
		if br, ok := returnBetweenDates(bench, s.Bars[i-1].Date, s.Bars[i+1].Date); ok {
			gap -= br
		}
		if br, ok := returnBetweenDates(bench, s.Bars[i+1].Date, s.Bars[len(s.Bars)-1].Date); ok {
			post -= br
		}
	}

	sessions := len(s.Bars) - 1 - (i + 1)
	// √2 for the two-session event window and √sessions for the drift since:
	// a move accumulated over a month is not the same size of surprise as the
	// same move in two days.
	d := driftRead{
		GapZ:     gap / (sigmaDaily * math.Sqrt2),
		Sessions: sessions,
		Date:     s.Bars[i].Date,
	}
	if sessions > 0 {
		d.PostZ = post / (sigmaDaily * math.Sqrt(float64(sessions)))
	}
	return d, true
}

// returnBetweenDates is the simple return of a series between two dates, using
// the first bar on or after each. It mirrors the scoreboard's helper of the same
// shape; the two packages cannot share one without the orchestrator importing
// the scoreboard's replay internals for four lines of arithmetic.
func returnBetweenDates(s *quant.Series, from, to string) (float64, bool) {
	at := func(d string) (float64, bool) {
		i := sort.Search(len(s.Bars), func(i int) bool { return s.Bars[i].Date >= d })
		if i >= len(s.Bars) || s.Bars[i].Close <= 0 {
			return 0, false
		}
		return s.Bars[i].Close, true
	}
	a, ok1 := at(from)
	b, ok2 := at(to)
	if !ok1 || !ok2 || a <= 0 {
		return 0, false
	}
	return b/a - 1, true
}

// isDrift selects a name that repriced on its own report and has kept the move.
//
// It is tested *first* in classifySetups, before the shapes read off the
// trailing returns. A name that jumped 3σ on its 10-Q last week is a name whose
// dominant fact is the report; calling it a "pullback" because its last month
// disagrees with its last year describes a mechanism that is not what is driving
// it, and puts it in a table the scout reads for a different reason entirely.
// The reaction must clear driftMinSigma *and* still clear driftMinScore after
// decay. The second test is what releases a name back to the trailing-return
// archetypes once its report is history, rather than holding it in a table where
// it ranks last and out of the tables where it would otherwise have been seen.
func isDrift(r PrescreenRow) bool {
	return r.ReportDate != "" &&
		r.DriftSessions >= 0 && r.DriftSessions <= driftWindowSessions &&
		math.Abs(r.GapZ) >= driftMinSigma &&
		math.Abs(r.Drift) >= driftMinScore
}

// driftDirection is the side a drift candidate is a candidate on: the sign of
// the reaction, not the sign of the composite.
//
// This is the whole reason the drift table cannot reuse the split the other
// archetypes use. Those are ranked by the composite and the composite's sign is
// their direction, so walking the ranking from one end yields one side. A drift
// name's direction is the sign of its own earnings gap, which is frequently the
// *opposite* of its trailing composite — a name that has run all year and then
// missed is exactly the case, and it is a short standing at the top of the
// ranking.
func driftDirection(r PrescreenRow) float64 { return r.Drift }
