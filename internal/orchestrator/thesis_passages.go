package orchestrator

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

func validateClaimPassages(claims []model.ResearchClaim, docs []model.EvidenceDocument) []string {
	by := map[string]model.EvidenceDocument{}
	for _, d := range docs {
		if d.Error == "" {
			by[d.ID] = d
		}
	}
	var problems []string
	for _, c := range claims {
		covered := map[string]bool{}
		for _, p := range c.Passages {
			d, ok := by[p.EvidenceID]
			cited := false
			for _, id := range c.EvidenceIDs {
				if id == p.EvidenceID {
					cited = true
				}
			}
			if !ok || !cited || len([]rune(strings.TrimSpace(p.Quote))) < 30 || !strings.Contains(d.Text, p.Quote) || strings.TrimSpace(p.IssuerRole) == "" {
				problems = append(problems, "ungrounded source passage or missing issuer role for claim "+c.ID)
				continue
			}
			covered[p.EvidenceID] = true
		}
		for _, id := range c.EvidenceIDs {
			if !covered[id] {
				problems = append(problems, "claim "+c.ID+" needs a quoted passage and issuer role for "+id)
			}
		}
	}
	return problems
}

// A supported review must address every dossier claim. It may add claims, but
// cannot support the same claim while silently changing who acted in its source.
// Every problem is returned categorised; the historical repeated-claim path
// below raises only blocking categories, as every problem blocked when it ran.
func validateReviewConsistency(d model.CandidateDossier, review model.ThesisChallenge) []model.MaterialIssue {
	if review.ContractVersion != 0 {
		return compactReviewProblems(d, review)
	}
	if review.Verdict != "supported" {
		return nil
	}
	by := map[string]model.ResearchClaim{}
	for _, c := range review.Claims {
		by[c.ID] = c
	}
	var problems []model.MaterialIssue
	for _, c := range d.Claims {
		r, ok := by[c.ID]
		if !ok {
			problems = append(problems, issue(model.IssueForm, "review did not address claim "+c.ID))
			continue
		}
		for _, p := range c.Passages {
			matched := false
			for _, rp := range r.Passages {
				if p.EvidenceID == rp.EvidenceID {
					matched = true
					if !strings.EqualFold(strings.TrimSpace(p.IssuerRole), strings.TrimSpace(rp.IssuerRole)) {
						problems = append(problems, issue(model.IssueAttribution, "conflicting issuer roles in claim "+c.ID+"; resolve attribution before support"))
					}
				}
			}
			if !matched {
				problems = append(problems, issue(model.IssueAttribution, "review omitted source attribution for claim "+c.ID))
			}
		}
	}
	return problems
}

// promptDocuments spends the text budget on cited passages, then substantive
// sources. Metadata survives even when text cannot fit; an omitted passage is
// explicit rather than a tiny prefix masquerading as sufficient source reading.
func promptDocuments(docs []model.EvidenceDocument, budget int, claims ...model.ResearchClaim) []model.EvidenceDocument {
	out := append([]model.EvidenceDocument(nil), docs...)
	byID := map[string][]model.ResearchClaim{}
	for _, c := range claims {
		for _, id := range c.EvidenceIDs {
			byID[id] = append(byID[id], c)
		}
	}
	score := func(d model.EvidenceDocument) int {
		if d.Error != "" {
			return -1
		}
		if len(byID[d.ID]) > 0 {
			return 3
		}
		if d.NavigationOnly || marketdata.ResearchNavigationURL(d.URL) {
			return 0
		}
		if d.Kind == "document" {
			return 2
		}
		return 1
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := score(out[i]), score(out[j]); a != b {
			return a > b
		}
		if !out[i].PublishedAt.Equal(out[j].PublishedAt) {
			return out[i].PublishedAt.After(out[j].PublishedAt)
		}
		return out[i].ID < out[j].ID
	})
	// Reserve every material quote before spending any budget on context.
	required := make([][]model.EvidenceSpan, len(out))
	reserved := 0
	for i, d := range out {
		required[i] = requiredSpans(d, byID[d.ID])
		reserved += spanCost(required[i])
	}
	optional := max(0, budget-reserved)
	remaining := max(0, budget)
	for i := range out {
		d := &out[i]
		original := d.Text
		var text string
		if len(required[i]) > 0 {
			spans := []model.EvidenceSpan{}
			for _, span := range required[i] {
				candidate := mergeEvidenceSpans(append(append([]model.EvidenceSpan(nil), spans...), span))
				if spanCost(candidate) <= remaining {
					spans = candidate
				}
			}
			if reserved <= budget {
				extra := min(5000, optional)
				// Context around a quote is padded by up to 180 characters a
				// side, and narrowed to what the grant can still pay for rather
				// than dropped: the board allocator treats source context as
				// divisible, and a fixed-width window made every grant under
				// ~360 characters buy nothing at all.
				for _, span := range required[i] {
					for _, pad := range []int{180, extra / 2} {
						if pad <= 0 {
							break
						}
						candidate := mergeEvidenceSpans(append(append([]model.EvidenceSpan(nil), spans...), model.EvidenceSpan{Start: max(0, span.Start-pad), End: min(len([]rune(original)), span.End+pad)}))
						cost := spanCost(candidate) - spanCost(spans)
						if cost <= extra {
							spans = candidate
							extra -= cost
							optional -= cost
							break
						}
					}
				}
			}
			text = renderEvidenceSpans(original, spans)
			d.SelectedSpans = spans
		} else {
			limit := min(1600, optional)
			text = relevantPassages(*d, byID[d.ID], limit)
			d.SelectedSpans = selectedSpans(original, text)
			optional -= len([]rune(text))
		}
		d.OmittedText = text != original
		if d.OmittedText {
			d.Truncated = true
		}
		d.OmittedClaimIDs = nil
		for _, c := range byID[d.ID] {
			for _, p := range c.Passages {
				if p.EvidenceID == d.ID && p.Quote != "" && strings.Contains(original, p.Quote) && !strings.Contains(text, p.Quote) {
					d.OmittedClaimIDs = appendUnique(d.OmittedClaimIDs, c.ID)
				}
			}
		}
		d.Text = text
		remaining -= len([]rune(text))
		if len(d.Links) > 12 {
			d.Links = d.Links[:12]
		}
	}
	return out
}

func relevantPassages(d model.EvidenceDocument, claims []model.ResearchClaim, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(d.Text)
	if len(runes) <= limit {
		return d.Text
	}
	var excerpts []string
	remaining := limit
	add := func(at, length int) {
		if remaining <= 0 {
			return
		}
		if length > remaining {
			return
		}
		start := max(0, at-180)
		if at-start+length > remaining {
			start = at
		}
		end := min(len(runes), max(at+length+180, start+600))
		// Leave room for the separator inside the same strict rune budget.
		separator := 0
		if len(excerpts) > 0 {
			separator = 5
		}
		if remaining <= separator {
			return
		}
		end = min(end, start+remaining-separator)
		s := string(runes[start:end])
		for _, old := range excerpts {
			if strings.Contains(old, s) {
				return
			}
		}
		excerpts = append(excerpts, s)
		remaining -= end - start + separator
	}
	for _, c := range claims {
		for _, p := range c.Passages {
			if p.EvidenceID == d.ID && len(p.Quote) > 0 {
				if at := strings.Index(d.Text, p.Quote); at >= 0 {
					add(len([]rune(d.Text[:at])), len([]rune(p.Quote)))
				}
			}
		}
	}
	// Older artifacts can lack quotes. Locate claim terms without pretending
	// that a keyword match establishes entailment.
	if len(excerpts) == 0 {
		lower := strings.ToLower(d.Text)
		for _, c := range claims {
			words := strings.FieldsFunc(strings.ToLower(c.Text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
			for _, word := range words {
				if len(word) < 6 {
					continue
				}
				if at := strings.Index(lower, word); at >= 0 {
					add(len([]rune(lower[:at])), len([]rune(word)))
					break
				}
			}
		}
	}
	// Before any claim exists, score substantive windows across the entire source.
	if len(excerpts) == 0 {
		for _, at := range substantiveWindows(d) {
			add(at, min(600, remaining))
			if remaining < 200 {
				break
			}
		}
	}
	// Keep issuer/reporting-period context from the start after material quotes.
	if remaining > 5 {
		add(0, min(400, remaining-5))
	}
	return strings.Join(excerpts, " ... ")
}

// Window scoring is retrieval, not entailment. Dates, quantities and issuer/title
// terms find substantive sections without requiring a pre-existing thesis.
func substantiveWindows(d model.EvidenceDocument) []int {
	r := []rune(d.Text)
	type window struct{ at, score int }
	var ws []window
	terms := strings.FieldsFunc(strings.ToLower(d.Title), func(r rune) bool { return !unicode.IsLetter(r) })
	for at := 0; at < len(r); at += 350 {
		part := string(r[at:min(at+600, len(r))])
		lower := strings.ToLower(part)
		score := 0
		for _, word := range []string{"revenue", "earnings", "quarter", "guidance", "results", "outlook", "million", "billion", "營收", "收入", "売上", "決算"} {
			if strings.Contains(lower, word) {
				score += 6
			}
		}
		for _, word := range terms {
			if len([]rune(word)) >= 4 && strings.Contains(lower, word) {
				score += 2
			}
		}
		navigation := 0
		for _, word := range []string{"cookie", "privacy", "sign in", "subscribe", "all rights reserved", "navigation"} {
			if strings.Contains(lower, word) {
				navigation += 8
			}
		}
		// A release section can begin inside a navigation-heavy window. Do
		// not let its preceding menu cancel multiple substantive matches.
		if score >= 12 {
			score -= min(navigation, 6)
		} else {
			score -= navigation
		}

		digits := 0
		for _, v := range part {
			if unicode.IsDigit(v) {
				digits++
			}
		}
		score += min(digits/4, 5)
		ws = append(ws, window{at, score})
	}
	sort.SliceStable(ws, func(i, j int) bool { return ws[i].score > ws[j].score })
	var out []int
	for _, w := range ws {
		if w.score <= 0 {
			continue
		}
		overlap := false
		for _, at := range out {
			if w.at-at < 600 && at-w.at < 600 {
				overlap = true
			}
		}
		if !overlap {
			out = append(out, w.at)
		}
		if len(out) == 3 {
			break
		}
	}
	return out
}

func selectedSpans(original, selected string) []model.EvidenceSpan {
	var out []model.EvidenceSpan
	for _, part := range strings.Split(selected, " ... ") {
		if part == "" {
			continue
		}
		if at := strings.Index(original, part); at >= 0 {
			start := len([]rune(original[:at]))
			out = append(out, model.EvidenceSpan{Start: start, End: start + len([]rune(part))})
		}
	}
	return out
}

// Evidence after the run anchor remains in saved artifacts but cannot inform a
// model's conclusion. Preserve an explicit unavailable record in prompt views.
func promptDocumentsAt(docs []model.EvidenceDocument, budget int, anchor time.Time, claims ...model.ResearchClaim) []model.EvidenceDocument {
	return promptDocuments(visibleAt(docs, anchor), budget, claims...)
}

// visibleAt applies that rule on its own, without spending a text budget, so
// a caller that has to measure a company's required quotations before it
// knows what budget to give it (thesis_board.go) measures them against the
// same documents promptDocumentsAt would have used.
func visibleAt(docs []model.EvidenceDocument, anchor time.Time) []model.EvidenceDocument {
	visible := append([]model.EvidenceDocument(nil), docs...)
	for i := range visible {
		if !anchor.IsZero() && visible[i].PublishedAt.After(anchor) {
			visible[i].Text = ""
			visible[i].Title = "Publication after run anchor"
			visible[i].Error = "Future publication unavailable at generation"
			visible[i].Links = nil
		}
	}
	return visible
}

// quotationRef is one accepted quotation, addressable by the evidence it was
// taken from. requiredQuotations below deduplicates on (EvidenceID, Quote):
// several claims citing the identical accepted words store them once.
type quotationRef struct {
	EvidenceID string `json:"evidence_id"`
	Quote      string `json:"quote"`
}

// requiredQuotations collects every claim's accepted quotation exactly once,
// in first-seen order. Before this, a quotation cited by several claims
// carried its own copy inline on EVERY citing claim wherever that claim's
// dossier or challenge was echoed back as context (previous_dossier,
// previous_challenge, revision_issues) — on top of the SAME text already
// reserved, verbatim, as a required span inside the evidence section
// (promptDocuments/requiredSpans below). dossierReference and
// challengeReference strip the inline copies from the echoed claims; a
// claim's own EvidenceID (unchanged) is enough to resolve it against this
// section instead. Distinct quotes sharing one evidence_id are kept
// separately — deduplication is keyed on the exact (evidence_id, quote)
// pair, never on evidence_id alone, so nothing required is ever dropped.
func requiredQuotations(claims []model.ResearchClaim) []quotationRef {
	seen := map[string]bool{}
	var out []quotationRef
	for _, c := range claims {
		for _, p := range c.Passages {
			quote := strings.TrimSpace(p.Quote)
			if quote == "" {
				continue
			}
			key := p.EvidenceID + "\x00" + p.Quote
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, quotationRef{EvidenceID: p.EvidenceID, Quote: p.Quote})
		}
	}
	return out
}

// dossierReference is what previous_dossier echoes back: the prior dossier a
// call is meant to revise, minus each claim's own copy of its accepted
// quotation (see requiredQuotations). Nothing else changes — the claim keeps
// its own text, evidence IDs and issuer roles, which are not duplicated
// anywhere else and stay exactly as written.
func dossierReference(d model.CandidateDossier) model.CandidateDossier {
	d.Claims = claimReferences(d.Claims)
	return d
}

// challengeReference is the same projection for a ThesisChallenge, used
// wherever a challenge is echoed back as CONTEXT rather than reviewed as the
// current verdict: previous_challenge (challengeSections) and revision_issues
// (thesis.go), both of which serialize a Challenge value whole. It never
// touches ClaimReviews or MaterialIssues — those are prose, not duplicated
// quotation text, and are exactly what a revision or a final challenge has to
// read.
func challengeReference(c model.ThesisChallenge) model.ThesisChallenge {
	c.Claims = claimReferences(c.Claims)
	return c
}

func evidenceClaims(d model.CandidateDossier, extra ...model.ResearchClaim) []model.ResearchClaim {
	claims := append(append([]model.ResearchClaim(nil), d.Claims...), extra...)
	for _, c := range d.Claims {
		if n := c.Comparison; n != nil && n.RatioEvidenceID != "" {
			claims = append(claims, model.ResearchClaim{ID: c.ID + "-ratio", EvidenceIDs: []string{n.RatioEvidenceID}, Passages: []model.ClaimPassage{{EvidenceID: n.RatioEvidenceID, Quote: n.RatioPassage, IssuerRole: "reporting issuer or depositary"}}})
		}
	}
	for i, e := range d.Events {
		claims = append(claims, model.ResearchClaim{ID: fmt.Sprintf("event-%d", i), EvidenceIDs: []string{e.EvidenceID}, Passages: []model.ClaimPassage{{EvidenceID: e.EvidenceID, Quote: e.Passage, IssuerRole: "reporting issuer"}}})
	}
	return claims
}

func requiredSpans(d model.EvidenceDocument, claims []model.ResearchClaim) []model.EvidenceSpan {
	if d.Error != "" {
		return nil
	}
	var spans []model.EvidenceSpan
	for _, c := range claims {
		for _, p := range c.Passages {
			if p.EvidenceID == d.ID && p.Quote != "" {
				if at := strings.Index(d.Text, p.Quote); at >= 0 {
					start := len([]rune(d.Text[:at]))
					spans = append(spans, model.EvidenceSpan{Start: start, End: start + len([]rune(p.Quote))})
				}
			}
		}
	}
	return mergeEvidenceSpans(spans)
}
func mergeEvidenceSpans(spans []model.EvidenceSpan) []model.EvidenceSpan {
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	out := []model.EvidenceSpan{}
	for _, s := range spans {
		if len(out) > 0 && s.Start <= out[len(out)-1].End {
			out[len(out)-1].End = max(out[len(out)-1].End, s.End)
		} else {
			out = append(out, s)
		}
	}
	return out
}
func spanCost(spans []model.EvidenceSpan) int {
	size := max(0, len(spans)-1) * 5
	for _, s := range spans {
		size += s.End - s.Start
	}
	return size
}
func renderEvidenceSpans(text string, spans []model.EvidenceSpan) string {
	runes := []rune(text)
	parts := make([]string, 0, len(spans))
	for _, s := range spans {
		parts = append(parts, string(runes[s.Start:s.End]))
	}
	return strings.Join(parts, " ... ")
}
