package model

import "math"

// DefaultDomainWeights is the horizon-matched weighting: quant is the only
// domain covered for every name, computed rather than recalled, and measured
// over exactly this horizon, so it carries the most; fundamentals says least
// about the next three weeks.
//
// Macro carries **zero**, which does not mean the domain was deleted. It still
// runs, and its regime read still reaches the Chief — as context, the way an
// enforcement-noticed report does. What it no longer does is vote.
//
// A regime is a backdrop shared by every name in a market. Scoring it per name
// turns one fact into twelve, and the persona's own cap said so ("if this domain
// scores twelve names 7, it has said nothing that distinguishes any of them, and
// the weighting will treat that as twelve independent confirmations"). The cap
// bounded the magnitude and could not touch the shape.
//
// What made that fatal rather than merely wasteful is that the twelve were not
// independent of the *funnel* either. The persona instructed the analyst to
// judge whether the regime supported each name's **nominated direction** — a
// confirmation task, structurally unable to return the opposite sign. Measured
// across the stored runs it never did: 12 of 12 in each of the last three runs,
// with its signed scores correlating 0.85-0.99 against quant's, which reads the
// same price history the nomination came from. Ten per cent of the weight was
// the pre-screen composite agreeing with itself, and because it covered every
// name it was the vote that carried the thinly-covered ones — two of the five
// ideas shipped on 2026-09-04 were scored by quant and macro alone and reported
// 100% agreement to the Chief.
//
// Zero removes all of it in one place: computeBaseScores skips a non-positive
// weight before accumulating, so macro no longer contributes a vote, no longer
// enters the Consensus ratio, and no longer counts toward covered weight — which
// also drops an unmapped foreign listing's expected coverage to quant alone,
// where the thinly-covered cap can see it.
func DefaultDomainWeights() DomainWeights {
	return DomainWeights{
		Quant:        0.35,
		News:         0.30,
		Fundamentals: 0.18,
		Sentiment:    0.17,
		Macro:        0.00,
	}
}

// Map flattens the weights into the domain keys the specialists emit.
func (w DomainWeights) Map() map[string]float64 {
	return map[string]float64{
		"quant":        w.Quant,
		"news":         w.News,
		"fundamentals": w.Fundamentals,
		"sentiment":    w.Sentiment,
		"macro":        w.Macro,
	}
}

// Total is the sum of the positive weights.
func (w DomainWeights) Total() float64 {
	total := 0.0
	for _, v := range w.Map() {
		if v > 0 {
			total += v
		}
	}
	return total
}

// ReferenceStrength is the strength each domain's own rubric treats as the top
// of the band it routinely uses, and the scale a base score is expressed
// against.
//
// It lives here rather than in the orchestrator because the scoreboard has to
// read a past idea's recorded domain scores on the same scale as a live run
// produces them; two copies of this table would be two answers to one question.
//
// The values are the personas', not this file's: agents/{quant,news,
// fundamentals,sentiment}.md all describe 7–8 as "these agree" and reserve 9–10
// for "rare", while agents/macro.md states 5 as its ceiling outright absent a
// dated sector driver. Scoring against 10 across the board meant the top quarter
// of the scale was unreachable by construction.
var ReferenceStrength = map[string]int{
	"quant":        8,
	"news":         8,
	"fundamentals": 8,
	"sentiment":    8,
	"macro":        5,
}

// defaultReferenceStrength is used for a domain not in the table, so adding a
// sixth domain does not silently score it against zero.
const defaultReferenceStrength = 8

// ReferenceTotal is the largest weighted vote the domains can jointly express:
// every domain agreeing at the top of the band its own rubric permits.
//
// It sums over *all* the weighted domains, never only the ones that scored a
// given name. A per-name denominator would mean a missing domain no longer
// shrinks the magnitude, which is how one loud domain came to outrank five that
// partly disagreed. Dividing every name by the same constant is a positive
// scalar multiply: it changes the scale and never the ordering.
func ReferenceTotal(w DomainWeights) float64 {
	total := 0.0
	for domain, weight := range w.Map() {
		if weight <= 0 {
			continue
		}
		r, ok := ReferenceStrength[domain]
		if !ok {
			r = defaultReferenceStrength
		}
		total += weight * float64(r) / 10
	}
	return total
}

// ScaledConfidence re-expresses a set of signed per-domain strengths (−10…+10,
// the shape TradeIdea.DomainScores records) as a 0–100 confidence on the current
// scale. It reports false when there is nothing to score.
//
// The scoreboard uses it to put ideas from different eras of this codebase on
// one axis: an idea's stored Confidence is on whichever scale its run used, but
// its DomainScores are raw evidence and can be re-read at any time.
func ScaledConfidence(w DomainWeights, domainScores map[string]int) (int, bool) {
	if len(domainScores) == 0 {
		return 0, false
	}
	reference := ReferenceTotal(w)
	if reference <= 0 {
		return 0, false
	}
	weights := w.Map()
	weighted := 0.0
	for domain, strength := range domainScores {
		weight, ok := weights[domain]
		if !ok || weight <= 0 {
			continue
		}
		if strength > 10 {
			strength = 10
		} else if strength < -10 {
			strength = -10
		}
		weighted += weight * float64(strength) / 10
	}
	scaled := math.Abs(weighted) / reference
	if scaled > 1 {
		scaled = 1
	}
	return int(scaled*100 + 0.5), true
}
