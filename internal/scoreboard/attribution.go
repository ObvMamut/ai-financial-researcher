package scoreboard

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Attribution is the deterministic half of the self-analysis loop: it asks not
// whether the pipeline made money but *which kinds of call* it made money on.
//
// Every cell here is a count over closed trades, computed in Go from the replay.
// Nothing in it is a judgement. The judgement is the post-mortem agent's job,
// and it may only make one about a cell that appears here — which is what keeps
// a narrative about the pipeline's own performance from being invented the way
// a domain report can be.
type Attribution struct {
	NClosed int `json:"n_closed"`

	// BySetup is the shape of the trade rather than the name: direction, the
	// market regime the quant pack read, how volatile the name was, and where it
	// sat against its 52-week high. It is the cell a "why did this work" answer
	// actually lives in — "long continuation in a trending tape" is a thing a
	// system can be good or bad at, and "AAPL" is not.
	BySetup map[string]Bucket `json:"by_setup,omitempty"`
	// ByCoverage and ByConsensus split the record on the two axes the base score
	// conflates. Together they answer the question the single number cannot be
	// asked: is a thin idea every domain agreed on better or worse than a
	// well-covered one whose domains fought?
	ByCoverage  map[string]Bucket `json:"by_coverage,omitempty"`
	ByConsensus map[string]Bucket `json:"by_consensus,omitempty"`
	// BySector is the record by what the run screened the name as.
	BySector map[string]Bucket `json:"by_sector,omitempty"`

	// Fills is the entry-limit record, and it is measured over *every* replayed
	// idea rather than only the closed ones — because an idea that never filled
	// never became a trade, and that is invisible everywhere else in this
	// package.
	//
	// It is the loudest silent failure the pipeline has: of 94 ideas across the
	// stored runs only 5 had ever closed. Everything before 2026-07-19 carried
	// no entry, stop or target at all and was skipped outright; of the rest, the
	// limits that did exist have to actually trade before any of the machinery
	// above measures anything. A loop that cannot see its own unfilled rate
	// cannot learn from it.
	Fills FillRecord `json:"fills"`
}

// FillRecord is how the entry limits themselves performed.
type FillRecord struct {
	// Replayable is every idea with levels; Skipped those with none at all.
	Replayable int `json:"replayable"`
	Skipped    int `json:"skipped"`
	Filled     int `json:"filled"`
	Unfilled   int `json:"unfilled"`
	// StillOpen is ideas whose fill window has not run out yet — neither a fill
	// nor a miss.
	StillOpen int `json:"still_open"`
	Errored   int `json:"errored"`
	// ByOffset buckets the *planned* entry by how far it sat from the price at
	// generation, in percent, signed toward the trade's own direction: negative
	// means the limit asked for a better price than the tape was offering.
	// A limit placed further away fills less often; this is where that trade-off
	// becomes a number instead of a preference.
	ByOffset map[string]FillBucket `json:"by_offset,omitempty"`
}

// FillBucket is the fill record for one slice of entry offsets.
type FillBucket struct {
	N        int     `json:"n"`
	Filled   int     `json:"filled"`
	FillRate float64 `json:"fill_rate"`
	// AvgR over the closed trades in this slice, so a bucket that fills rarely
	// but pays well is distinguishable from one that simply fills rarely.
	AvgR   float64 `json:"avg_r"`
	Closed int     `json:"closed"`
}

// Attribute reduces a replayed summary to the attribution tables. It returns nil
// for a nil or legacy summary: the legacy mark-to-market has no fills, no
// barriers and no consensus, so none of these questions can be asked of it.
func Attribute(s *Summary) *Attribution {
	if s == nil || !s.Replay {
		return nil
	}
	a := &Attribution{NClosed: s.Closed, Fills: FillRecord{Skipped: s.Skipped}}

	setup, coverage, consensus, sector := accs{}, accs{}, accs{}, accs{}
	offsets := map[string]*fillAcc{}

	// The fill census counts every idea; the outcome cells count independent
	// ones. The asymmetry is deliberate, and it is not a compromise: a
	// re-proposal of the same name carries its *own* limit at its own price, so
	// it is a genuine second observation of whether a limit that far away
	// fills — while being the same observation of whether the call was right.
	independent, _ := DedupeMask(s.Entries, DefaultDedupeWindowDays)

	for i, e := range s.Entries {
		a.Fills.Replayable++
		switch {
		case e.Outcome == OutcomeError:
			a.Fills.Errored++
		case e.Outcome == OutcomeUnfilled:
			a.Fills.Unfilled++
		case e.EntryFilled > 0:
			a.Fills.Filled++
		default:
			a.Fills.StillOpen++
		}

		// The offset record spans every idea, filled or not: measuring it over
		// the fills alone would be measuring the survivors.
		if key, ok := entryOffsetBucket(e); ok && e.Outcome != OutcomeError && e.Outcome != OutcomeOpen {
			f, exists := offsets[key]
			if !exists {
				f = &fillAcc{}
				offsets[key] = f
			}
			f.add(e)
		}

		if !e.Outcome.closed() || !independent[i] {
			continue
		}
		setup.add(setupKey(e), e)
		coverage.add(coverageBucket(e), e)
		consensus.add(consensusBucket(e), e)
		sector.add(e.Sector, e)
	}

	a.BySetup, a.ByCoverage = setup.buckets(), coverage.buckets()
	a.ByConsensus, a.BySector = consensus.buckets(), sector.buckets()
	if len(offsets) > 0 {
		a.Fills.ByOffset = make(map[string]FillBucket, len(offsets))
		for k, f := range offsets {
			a.Fills.ByOffset[k] = f.bucket()
		}
	}
	return a
}

// fillAcc accumulates the fill record for one offset slice.
type fillAcc struct {
	n, filled, closed int
	r                 float64
}

func (f *fillAcc) add(e Entry) {
	f.n++
	if e.EntryFilled > 0 {
		f.filled++
	}
	if e.Outcome.closed() && e.hasR() {
		f.closed++
		f.r += e.RiskAdjPnL
	}
}

func (f *fillAcc) bucket() FillBucket {
	b := FillBucket{N: f.n, Filled: f.filled, Closed: f.closed}
	if f.n > 0 {
		b.FillRate = round2(float64(f.filled) / float64(f.n))
	}
	if f.closed > 0 {
		b.AvgR = round2(f.r / float64(f.closed))
	}
	return b
}

// setupKey names the shape of a trade. It is deliberately coarse: four
// dimensions at two or three levels each already produce more cells than this
// pipeline has closed trades, and a cell of one is not a finding.
//
// The fuller key — direction x regime x vol bucket x price-to-52-week-high —
// is the one worth having, and Entry carries none of those three fields yet.
// Adding them is only worth the plumbing once the closed-trade count can fill
// the cells: direction x stop-width is 6 cells and the post-mortem already
// refuses a lesson under 5 closed trades per cell, so the fuller key needs
// roughly an order of magnitude more history than exists. Widen it when the
// record passes ~100 closed ideas, not before.
func setupKey(e Entry) string {
	dir := strings.ToLower(e.Direction)
	if dir == "" {
		dir = "?"
	}
	return dir + "/" + riskBucket(e)
}

// riskBucket describes how much room the trade gave itself, in the only unit
// available after the fact: the stop distance as a share of the entry.
func riskBucket(e Entry) string {
	entry := e.EntryFilled
	if entry <= 0 {
		entry = e.EntryPlanned
	}
	if entry <= 0 || e.Stop <= 0 {
		return "unknown-risk"
	}
	switch frac := math.Abs(entry-e.Stop) / entry; {
	case frac < 0.05:
		return "tight-stop"
	case frac < 0.10:
		return "medium-stop"
	default:
		return "wide-stop"
	}
}

// coverageBucket is how much of the domain weight stood behind the idea, read
// back from the number of domains that scored it. The weights are not recorded
// per idea, so this counts domains rather than weight — coarser, but it is the
// same question and it needs nothing the ideas did not store.
func coverageBucket(e Entry) string {
	switch n := len(e.DomainScores); {
	case n == 0:
		return ""
	case n <= 2:
		return "1-2 domains"
	case n <= 4:
		return "3-4 domains"
	default:
		return "5 domains"
	}
}

// consensusBucket splits on how much the domains that spoke agreed. Ideas from
// before consensus was recorded have none and are left out rather than bucketed
// as zero, which would read as total disagreement.
func consensusBucket(e Entry) string {
	switch {
	case e.Consensus <= 0:
		return ""
	case e.Consensus < 0.5:
		return "split (<50%)"
	case e.Consensus < 0.85:
		return "leaning (50-85%)"
	default:
		return "unanimous (85%+)"
	}
}

// entryOffsetBucket is how far the planned limit sat from the price at
// generation, signed toward the trade's direction: negative asks for a better
// price than the tape was offering, and is the side that goes unfilled.
func entryOffsetBucket(e Entry) (string, bool) {
	if e.PriceAtGen <= 0 || e.EntryPlanned <= 0 {
		return "", false
	}
	off := (e.EntryPlanned - e.PriceAtGen) / e.PriceAtGen * 100
	if strings.EqualFold(e.Direction, "SELL") {
		off = -off
	}
	switch {
	case off < -1.5:
		return "below -1.5% (waiting for a better price)", true
	case off < -0.25:
		return "-1.5% to -0.25%", true
	case off <= 0.25:
		return "at the close (±0.25%)", true
	default:
		return "above +0.25% (chasing)", true
	}
}

// Lines renders the attribution as the lines a post-mortem prompt and the
// scoreboard both read. Cells below minN are dropped rather than shown with a
// caveat: a bucket of two says nothing, and printing it invites a reader to make
// something of it anyway.
func (a *Attribution) Lines(minN int) []string {
	if a == nil {
		return nil
	}
	if minN < 1 {
		minN = 1
	}
	var out []string
	add := func(label string, m map[string]Bucket) {
		for _, k := range sortedBucketKeys(m) {
			b := m[k]
			if b.N < minN {
				continue
			}
			out = append(out, fmt.Sprintf("%s %s: n=%d, %.0f%% won, avg %+.2fR, avg %+.1f%%",
				label, k, b.N, b.WinRate*100, b.AvgR, b.AvgPnL))
		}
	}
	add("setup", a.BySetup)
	add("coverage", a.ByCoverage)
	add("agreement", a.ByConsensus)
	add("sector", a.BySector)

	f := a.Fills
	out = append(out, fmt.Sprintf("fills: %d of %d replayable ideas filled, %d never traded their limit, %d still inside the window, %d had no usable history, %d had no levels to replay",
		f.Filled, f.Replayable, f.Unfilled, f.StillOpen, f.Errored, f.Skipped))
	for _, k := range sortedFillKeys(f.ByOffset) {
		b := f.ByOffset[k]
		if b.N < minN {
			continue
		}
		out = append(out, fmt.Sprintf("entry %s: n=%d, %.0f%% filled, avg %+.2fR over %d closed",
			k, b.N, b.FillRate*100, b.AvgR, b.Closed))
	}
	return out
}

func sortedBucketKeys(m map[string]Bucket) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedFillKeys(m map[string]FillBucket) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Cells is every bucket label the attribution actually contains, which is what
// a lesson has to name to be checkable.
func (a *Attribution) Cells(minN int) map[string]bool {
	out := map[string]bool{}
	if a == nil {
		return out
	}
	if minN < 1 {
		minN = 1
	}
	for _, m := range []map[string]Bucket{a.BySetup, a.ByCoverage, a.ByConsensus, a.BySector} {
		for k, b := range m {
			if b.N >= minN {
				out[strings.ToLower(k)] = true
			}
		}
	}
	for k, b := range a.Fills.ByOffset {
		if b.N >= minN {
			out[strings.ToLower(k)] = true
		}
	}
	return out
}
