package scoreboard

import (
	"context"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// The horizon measurement: did the name move the way the idea said it would,
// over the idea's own stated holding period?
//
// This is a different question from the one the barrier replay answers, and it
// is the question this project exists to get right. The replay reports which of
// the stop and the target was touched first, which is what a *trader* needs; it
// cannot report whether a call was directionally correct, because the two come
// apart in both directions. An idea can be right about the next three weeks and
// still be stopped out by noise on day two, and an idea can be flatly wrong and
// expire having touched neither barrier — three of the five closed trades in the
// history this was written against expired exactly that way, and the record had
// no way to say whether any of them had been right.
//
// Two figures are produced, and the distinction between them matters:
//
//   - The *call* is anchored at the price the idea was generated off. It is the
//     directional claim on its own, with the entry-limit lottery removed: an
//     idea whose limit never traded still said the stock would fall, and was
//     still either right or wrong about that.
//   - The *trade* is anchored at the fill, and exists only for ideas that
//     filled. It is what the account would have seen.
//
// Both are also reported net of the name's own index benchmark, because "the
// long was right" and "the market went up" are not the same finding.

// horizonResult is one measured window.
type horizonResult struct {
	pct      float64 // direction-signed return over the window, in percent
	bench    float64 // the benchmark's raw return over the same window, percent
	excess   float64 // direction-signed return less the benchmark's
	endDate  string
	complete bool
}

// measureHorizon computes the signed return from anchor over the h sessions of
// bars, and the same window's benchmark move.
//
// bars must already start at the session *after* the anchor: the anchor is the
// close the idea was priced off, and including it would measure a zero-length
// window. complete is false when fewer than h sessions have elapsed, which is
// the normal state of a recent idea and must never be read as a flat result.
func measureHorizon(ctx context.Context, cache *seriesCache, bars []quant.Bar, anchor float64,
	dir model.Direction, h int, benchSym, anchorDate string) horizonResult {

	var out horizonResult
	if anchor <= 0 || h <= 0 || len(bars) < h {
		return out
	}
	end := bars[h-1]
	if end.Close <= 0 {
		return out
	}
	out.complete = true
	out.endDate = end.Date
	out.pct = pnl(dir, anchor, end.Close)
	out.excess = out.pct

	// The benchmark is looked up by date rather than by index, so a name whose
	// exchange holidays differ from its benchmark's cannot silently compare two
	// different windows.
	if benchSym == "" || anchorDate == "" {
		return out
	}
	b, err := cache.get(ctx, benchSym, "")
	if err != nil || b == nil {
		return out
	}
	br, ok := returnBetween(b, anchorDate, out.endDate)
	if !ok {
		return out
	}
	out.bench = round2(br * 100)
	// pnl() has already flipped the sign for a short, so the benchmark is added
	// rather than subtracted: a short that fell less than the market did badly.
	if dir == model.DirectionSell {
		out.excess = round2(out.pct + out.bench)
	} else {
		out.excess = round2(out.pct - out.bench)
	}
	return out
}

// benchmarkFor is the index series a name is measured against. It is a thin
// wrapper so the horizon code and the barrier replay cannot drift apart on
// which benchmark a name belongs to.
func benchmarkFor(index, ticker string) string { return universe.BenchmarkFor(index, ticker) }

// horizonHit reports whether a completed window went the way the idea said.
// A window that has not completed is not a miss, and a dead-flat close is not a
// hit — it is the absence of the move that was claimed.
func horizonHit(r horizonResult) bool { return r.complete && r.pct > 0 }
