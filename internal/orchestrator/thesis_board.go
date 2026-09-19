package orchestrator

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

// The Chief is the one call whose input size is set by the board rather than
// by one company: every other thesis role sees a single candidate, so its
// prompt grows with that candidate's evidence, while the Chief's grows with
// the number of companies that survived research. That is why the per-company
// allowance this file replaces could not work. `promptDocumentsAt(v.Documents,
// 9000, …)` spent up to 9,000 characters of source text per company with
// nothing counting the companies, so the Chief input fit only by accident:
// the 2026-09-15 run assembled 158,528 of its 196,608 bytes with **three**
// usable dossiers and nine outcome records. Twelve usable dossiers of the
// same shape project to 521,568 bytes of board against a ~181,000-byte board
// budget — 2.88x over — and with every document's text deleted the same
// twelve still need 319,828 bytes, 1.77x over. So allocating source text
// globally is necessary and not sufficient: the records themselves have to be
// projected down as well (plan ruling R4).
//
// This file therefore does two separate things, and they are separate on
// purpose:
//
//  1. a PROJECTION, which is fixed and does not depend on the budget: every
//     company's record is reduced to what the Chief decides from, and every
//     part of it that the Chief re-reads somewhere else in the same prompt is
//     dropped once, named in `projected_away` so an absent field reads as
//     withheld rather than as never written.
//  2. an ALLOCATION, which is global: source text is reserved for every
//     cited claim's required quotations across the whole board first, and
//     only the remainder is spent on optional context, split in proportion to
//     what each company still wants rather than handed out company by company
//     until it runs out.
//
// The order matters. The projection is what makes the required floor small
// enough for twelve companies to fit at all; the allocation is what stops the
// first three companies on the slice from spending the rest's share of what
// is left.

// chiefLegacyEvidenceChars is the per-company source-text allowance this file
// replaces. It survives as the ceiling on one company's *optional* appetite,
// so that a board with room to spare still cannot hand one company unbounded
// source text: no company asks for more than its required quotations plus
// this many characters of context, however small the board is.
const chiefLegacyEvidenceChars = 9000

// boardFitAttempts bounds the convergence loop below. The optional pool is
// counted in bytes but spent in characters, and one character of a Hong Kong
// or Taiwan source is three bytes, so the first split can overshoot. Each
// attempt rescales the pool by the overshoot it measured; the loop always
// ends, because the last attempt spends nothing and a board with no optional
// context is the required floor, which was already checked to fit.
const boardFitAttempts = 6

// Names recorded in chiefCompany.Projected and in the run's board-allocation
// data pack. Each one is a claim about where the Chief reads the same fact
// instead; see boardPlan.render for the argument per name.
const (
	projectedWorking     = "researcher_working_narrative"
	projectedCase        = "dossier_case_narrative"
	projectedClaimText   = "claim_text"
	projectedEventQuotes = "event_passages"
	projectedReviews     = "confirmed_claim_review_reasons"
	projectedScaffold    = "document_scaffolding"
	projectedAges        = "derived_evidence_ages"
	projectedContext     = "optional_source_context"
)

// caseNarrative is the dossier prose the Chief is told to read — "Use each
// dossier's long, short and no-trade comparison" — in the order it is funded.
// It is allocated rather than reserved because it is the one part of a
// company's record that is argument rather than evidence: a board that has
// every accepted quotation, every claim's attribution and every per-claim
// verdict can still be decided on, where a board missing those cannot. The
// three cases come first because the persona names them; catalyst_window
// before invalidation because the catalyst is what makes a 10-15 session
// trade time-bounded, while what would disprove the thesis is also stated by
// the challenge's material_issues and the dossier's unresolved list.
//
// A field is funded whole or not at all. Half a case is not half an argument,
// and a narrative cut mid-sentence is the "tiny prefix masquerading as
// sufficient source reading" that promptDocuments already refuses to produce.
func caseNarrative(d *model.CandidateDossier) []*string {
	return []*string{&d.LongCase, &d.ShortCase, &d.NoTradeCase, &d.CatalystWindow, &d.Invalidation}
}

func caseNarrativeBytes(d model.CandidateDossier) int {
	total := 0
	for _, field := range caseNarrative(&d) {
		total += len(*field)
	}
	return total
}

// chiefBoardBudget is the Chief board's byte budget: the role's whole input
// limit less everything else that will be in the prompt. It is a measurement,
// not an estimate — the persona wrapper and contract lines are measured by
// wrapping a probe through the same function that wraps the real data block,
// and every other section is measured on the same redacted body
// assembleSections will measure. The board section's own label prefix is
// charged too, because chiefBoard budgets the JSON that follows it.
//
// The optional macro section is charged at its actual size rather than
// skipped. Charging it is what guarantees it is never dropped: assembleSections
// only reaches for an optional section when the total overflows, and a board
// sized with macro already paid for cannot overflow. Reacting to macro's
// absence still happens, and exactly — an absent macro report leaves only its
// own label, and those bytes go to the board.
func (t *thesisRunner) chiefBoardBudget(other []promptSection, prefix string) (int, error) {
	wrapper, err := t.promptWrapperBytes("thesis-chief")
	if err != nil {
		return 0, err
	}
	used := wrapper + len(redact.String(prefix))
	for _, sec := range other {
		used += len(redact.String(sec.Body))
	}
	return t.cfg.Research.Budgets.ForRole("thesis-chief").InputBytes - used, nil
}

// chiefEvidence is the Chief's view of one cited source. It exists rather
// than reusing model.EvidenceDocument because two of the fields to drop are
// time.Time, and `omitempty` does not omit a struct: a blanked `retrieved_at`
// still costs its full 37 bytes on the wire, 211 times over on a twelve-
// company board. Listing the retained fields is also the clearest possible
// statement of the projection — what is not here is what ruling R4 calls
// "document scaffolding beyond what identifies and attributes the source".
//
// Dropped, with the reason each is not decision input:
//   - url: the Chief cannot fetch it. `id` identifies the source and
//     `source`/`authority` attribute it — and `authority` is Go's own reading
//     of that host, already computed, so the host adds nothing a reader could
//     act on.
//   - retrieved_at: when Go fetched the page, not when the issuer published.
//     `published_at` is the date every temporal judgement uses, and
//     model.EvidenceDocument's own comment says reporting period and event
//     time must not be inferred from retrieval time in the first place.
//   - title: the retrieved page's own headline. `id` is what the Chief cites
//     and what Go validates against; `source`, `authority`, `kind`,
//     `reporting_period` and `published_at` say who issued it, of what, and
//     when. A headline is the publisher's framing of a source the Chief is
//     told to treat as untrusted data, and it is the one scaffolding field
//     that competes directly with the dossier case narrative the persona
//     instructs the Chief to read: 40 bytes on every one of a twelve-company
//     board's ~211 sources is the difference between a board with a long and
//     short case for each company and one with neither. (visibleAt's future-
//     publication marker survives regardless: it sets `error` as well as the
//     title it overwrites.)
//   - links, parent_id: navigation structure of the crawl.
//
// Retained deliberately: selected_spans, because visibleEvidence() reads it
// back out of the assembled prompt to count what the model could actually
// see, and a document without it is not counted as visible at all.
type chiefEvidence struct {
	NavigationOnly  bool                 `json:"navigation_only,omitempty"`
	OmittedClaimIDs []string             `json:"omitted_claim_ids,omitempty"`
	Authority       string               `json:"authority,omitempty"`
	SelectedSpans   []model.EvidenceSpan `json:"selected_spans,omitempty"`
	OmittedText     bool                 `json:"omitted_text,omitempty"`
	ID              string               `json:"id"`
	Ticker          string               `json:"ticker"`
	Kind            string               `json:"kind"`
	PublishedAt     string               `json:"published_at,omitempty"`
	EventTime       string               `json:"event_time,omitempty"`
	ReportingPeriod string               `json:"reporting_period,omitempty"`
	Text            string               `json:"text"`
	Error           string               `json:"error,omitempty"`
	Source          string               `json:"source,omitempty"`
	Truncated       bool                 `json:"truncated,omitempty"`
}

func chiefEvidenceView(docs []model.EvidenceDocument) []chiefEvidence {
	out := make([]chiefEvidence, 0, len(docs))
	for _, d := range docs {
		e := chiefEvidence{NavigationOnly: d.NavigationOnly, OmittedClaimIDs: d.OmittedClaimIDs,
			Authority: d.Authority, SelectedSpans: d.SelectedSpans, OmittedText: d.OmittedText,
			ID: d.ID, Ticker: d.Ticker, Kind: d.Kind, EventTime: d.EventTime,
			ReportingPeriod: d.ReportingPeriod, Text: d.Text, Error: d.Error, Source: d.Source,
			Truncated: d.Truncated}
		if !d.PublishedAt.IsZero() {
			e.PublishedAt = d.PublishedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, e)
	}
	return out
}

// Prompt views never alter full research artifacts. Only cited sources enter
// selection; failed candidates remain explicitly present with their outcomes.
type chiefCompany struct {
	Candidate   model.Candidate         `json:"candidate"`
	Outcome     model.ResearchOutcome   `json:"outcome"`
	Eligibility string                  `json:"eligibility,omitempty"`
	Dossier     *model.CandidateDossier `json:"dossier,omitempty"`
	Challenge   *model.ThesisChallenge  `json:"challenge,omitempty"`
	Temporal    researchTimeFacts       `json:"temporal_facts"`
	Documents   []chiefEvidence         `json:"documents,omitempty"`
	// Projected names what this record carries elsewhere instead of inline.
	// Without it a blanked narrative field reads as a researcher who wrote
	// nothing, which is a different and worse fact than one whose prose was
	// withheld for capacity.
	Projected []string `json:"projected_away,omitempty"`
}

// boardShare records one company's part of the global allocation.
type boardShare struct {
	Ticker         string `json:"ticker"`
	Required       int    `json:"required_chars"`
	NarrativeNeed  int    `json:"case_narrative_need_bytes"`
	NarrativeGrant int    `json:"case_narrative_granted_bytes"`
	ContextNeed    int    `json:"source_context_need_chars"`
	ContextGrant   int    `json:"source_context_granted_chars"`
}

// boardAllocation is the audit trail for one board: what the budget was, what
// the required floor cost, and what each company was granted on top of it.
type boardAllocation struct {
	TextBudget    int          `json:"text_budget"`
	RequiredBytes int          `json:"required_bytes"`
	BoardBytes    int          `json:"board_bytes"`
	OptionalPool  int          `json:"optional_pool_bytes"`
	Attempts      int          `json:"fit_attempts"`
	Projected     []string     `json:"projected_away"`
	Companies     []boardShare `json:"companies"`
}

// boardPlan is one company's inputs to the allocation, computed once so that
// neither pass has to redo them and so that nothing in either pass depends on
// the company's position in the slice.
type boardPlan struct {
	research  thesisResearch
	usable    bool
	anchor    time.Time
	docs      []model.EvidenceDocument // anchor-filtered, cited only
	claims    []model.ResearchClaim
	cited     map[string]bool
	required  int // characters of required quotation
	narrative int // bytes of case narrative this company would like to carry
	need      int // characters of optional context this company could still use
}

func planCompany(r thesisResearch) boardPlan {
	p := boardPlan{research: r}
	if r.researchFailed() || r.Eligibility != "" {
		return p
	}
	p.usable = true
	p.anchor, _ = time.Parse(time.RFC3339, r.Temporal.AsOf)
	p.claims = evidenceClaims(r.Dossier, r.Challenge.Claims...)
	p.cited = map[string]bool{}
	for _, claim := range p.claims {
		for _, id := range claim.EvidenceIDs {
			p.cited[id] = true
		}
	}
	for _, e := range r.Dossier.Events {
		p.cited[e.EvidenceID] = true
	}
	for _, doc := range r.Documents {
		if p.cited[doc.ID] {
			doc.Links = nil
			p.docs = append(p.docs, doc)
		}
	}
	p.docs = visibleAt(p.docs, p.anchor)
	p.required = requiredChars(p.docs, p.claims)
	// Unmet need is measured, not assumed: render the company's evidence once
	// with its required quotations already paid for plus the old per-company
	// allowance on top, and see how much of that allowance it actually takes.
	// Proportioning on total evidence instead would let a company with twenty
	// long uncited pages outbid one with four short sources that still need
	// their supporting passages. Measuring above the required floor rather
	// than inside it also stops a company whose quotations alone exceed the
	// old cap from reporting no appetite at all — under the old rule such a
	// company did not get context, it got a capacity error.
	full := promptDocuments(p.docs, p.required+chiefLegacyEvidenceChars, p.claims...)
	used := 0
	for _, d := range full {
		used += len([]rune(d.Text))
	}
	p.need = max(0, used-p.required)
	p.narrative = caseNarrativeBytes(r.Dossier)
	return p
}

// requiredChars is the character cost of every cited claim's quotations for
// one company — the same reservation promptDocuments makes internally, read
// out before the call so the board can reserve all twelve at once.
func requiredChars(docs []model.EvidenceDocument, claims []model.ResearchClaim) int {
	byID := map[string][]model.ResearchClaim{}
	for _, c := range claims {
		for _, id := range c.EvidenceIDs {
			byID[id] = append(byID[id], c)
		}
	}
	total := 0
	for _, d := range docs {
		total += spanCost(requiredSpans(d, byID[d.ID]))
	}
	return total
}

// chiefBoard projects every researched company into the Chief's prompt view
// under one global budget for the assembled board, in bytes.
//
// Required pass: every company's records and every cited claim's required
// quotations. If those alone do not fit, the call fails loudly and names the
// companies, because a board that silently drops a required quotation is a
// board whose claims no longer trace to evidence — the same rule
// promptDocuments already applies inside one company, lifted to the board.
//
// Optional pass: the remainder is split across companies in proportion to
// each one's *unmet* need. Proportioning on unmet need rather than on total
// evidence is what keeps an evidence-heavy company from starving a thin one
// that still has passages to place, and computing the split from the whole
// vector at once — never by walking the slice and spending as it goes — is
// what makes a company's allocation independent of where it sits in the
// slice.
func chiefBoard(research []thesisResearch, textBudget int) ([]chiefCompany, boardAllocation, error) {
	plans := make([]boardPlan, len(research))
	for i, r := range research {
		plans[i] = planCompany(r)
	}
	alloc := boardAllocation{TextBudget: textBudget}
	board := renderBoard(plans, nil, nil)
	alloc.RequiredBytes = len(jsonText(board))
	alloc.BoardBytes = alloc.RequiredBytes
	if alloc.RequiredBytes > textBudget {
		// The board is returned with the error, not instead of it. The caller
		// still assembles and still dispatches nothing: preparePrompt refuses
		// the oversized prompt for the primary and the fallback alike, which
		// is what persists a zero-attempt input_capacity failure for each
		// engine. Returning nil here would instead skip the Chief entirely and
		// erase that provenance. What this error adds is the part preparePrompt
		// cannot say — which companies' required records did not fit.
		return board, alloc, promptCapacityError{fmt.Errorf(
			"input capacity exceeded: the Chief board's required records and quotations need %d bytes against %d available for %d companies (%s); no optional narrative or source context had been added",
			alloc.RequiredBytes, textBudget, len(plans), strings.Join(requiredCostReport(plans), ", "))}
	}
	narrativeNeeds, contextNeeds := make([]int, len(plans)), make([]int, len(plans))
	for i, p := range plans {
		narrativeNeeds[i], contextNeeds[i] = p.narrative, p.need
	}
	pool := textBudget - alloc.RequiredBytes
	narrative, context := make([]int, len(plans)), make([]int, len(plans))
	for attempt := 1; attempt <= boardFitAttempts; attempt++ {
		alloc.Attempts, alloc.OptionalPool = attempt, pool
		// The case narrative is funded before source context, and what it is
		// granted — not what it manages to spend — is what leaves the pool, so
		// the split between the two tiers is a function of the whole need
		// vector rather than of the order companies are filled in.
		narrative = splitByNeed(narrativeNeeds, pool)
		granted := 0
		for _, n := range narrative {
			granted += n
		}
		context = splitByNeed(contextNeeds, pool-granted)
		next := renderBoard(plans, narrative, context)
		size := len(jsonText(next))
		if size <= textBudget {
			board, alloc.BoardBytes = next, size
			break
		}
		// The pool is counted in bytes; source context is spent in characters,
		// and one character of a Hong Kong or Taiwan source is three bytes.
		// Rescale by the overshoot actually measured rather than by a guessed
		// encoding ratio, and require strict progress so the loop cannot
		// stall. The last attempt spends nothing, which is the required floor
		// that was already checked to fit.
		spent := size - alloc.RequiredBytes
		scaled := 0
		if spent > 0 {
			scaled = pool * (textBudget - alloc.RequiredBytes) / spent
		}
		pool = min(scaled, pool-1)
		if pool < 0 || attempt == boardFitAttempts {
			pool = 0
		}
		if pool == 0 {
			narrative, context = splitByNeed(narrativeNeeds, 0), splitByNeed(contextNeeds, 0)
			board, alloc.BoardBytes, alloc.OptionalPool = renderBoard(plans, narrative, context), alloc.RequiredBytes, 0
			break
		}
	}
	for i, p := range plans {
		alloc.Companies = append(alloc.Companies, boardShare{Ticker: p.research.Candidate.Ticker,
			Required: p.required, NarrativeNeed: p.narrative, NarrativeGrant: narrative[i],
			ContextNeed: p.need, ContextGrant: context[i]})
	}
	alloc.Projected = boardProjections(board)
	return board, alloc, nil
}

// requiredCostReport names the companies in a capacity failure largest first,
// with what each one's required records and quotations cost, so the message
// says which requirement did not fit rather than only that something did not.
func requiredCostReport(plans []boardPlan) []string {
	type row struct {
		ticker string
		bytes  int
	}
	rows := make([]row, 0, len(plans))
	for _, p := range plans {
		rows = append(rows, row{p.research.Candidate.Ticker, len(jsonText(p.render(0, 0)))})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].bytes > rows[j].bytes })
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s %d", r.ticker, r.bytes))
	}
	return out
}

// splitByNeed divides pool across needs in proportion to each need, capped at
// that need. Every share is a pure function of its own need, the whole need
// vector and the pool — never of an accumulating remainder — so permuting the
// input permutes the output and changes no company's share. The integer
// division's residue is deliberately left unspent for the same reason: there
// is no order-independent company to give it to.
func splitByNeed(needs []int, pool int) []int {
	out := make([]int, len(needs))
	if pool <= 0 {
		return out
	}
	remaining := append([]int(nil), needs...)
	for round := 0; round < 3; round++ {
		total := 0
		for _, n := range remaining {
			total += n
		}
		if total <= 0 || pool <= 0 {
			break
		}
		granted := 0
		for i, n := range remaining {
			share := n
			if pool < total {
				share = pool * n / total
			}
			out[i] += share
			remaining[i] -= share
			granted += share
		}
		if granted == 0 {
			break
		}
		pool -= granted
	}
	return out
}

func renderBoard(plans []boardPlan, narrative, context []int) []chiefCompany {
	out := make([]chiefCompany, 0, len(plans))
	for i, p := range plans {
		prose, extra := 0, 0
		if narrative != nil {
			prose = narrative[i]
		}
		if context != nil {
			extra = context[i]
		}
		out = append(out, p.render(prose, extra))
	}
	return out
}

// render projects one company at one narrative allowance (bytes) and one
// source-context allowance (characters). Candidate identity, outcome and
// eligibility are never allocated: a company the Chief cannot see is a
// company it cannot write a decision for, and every candidate must get a
// decision whether or not its research produced anything.
func (p boardPlan) render(prose, extra int) chiefCompany {
	c := p.research.Candidate
	c.Reason = ""
	c.Bias = ""
	v := chiefCompany{Candidate: c, Outcome: p.research.Outcome, Eligibility: p.research.Eligibility,
		Temporal: p.research.Temporal}
	if !p.usable {
		v.Temporal.EvidenceAges = nil
		return v
	}
	docs := promptDocuments(p.docs, p.required+extra, p.claims...)
	omitted := map[string]bool{}
	for _, d := range docs {
		for _, id := range d.OmittedClaimIDs {
			omitted[id] = true
		}
	}
	v.Documents = chiefEvidenceView(docs)
	v.Projected = append(v.Projected, projectedScaffold)
	if extra < p.need {
		v.Projected = append(v.Projected, projectedContext)
	}

	d := p.research.Dossier
	d.Requests = nil
	// The researcher's own working narrative goes unconditionally. hypothesis
	// is the one-line form of long_case; changed, expectations,
	// underappreciated, mechanism, priced_in and counterargument are the
	// argument that long_case, short_case and no_trade_case state in full. No
	// field of the Chief's output schema carries any of the seven, and the
	// Chief is told to ground every material claim in supplied evidence, not
	// in the researcher's prose.
	d.Hypothesis, d.Changed, d.Expectations = "", "", ""
	d.Underappreciated, d.Mechanism, d.PricedIn, d.Counterargument = "", "", "", ""
	v.Projected = append(v.Projected, projectedWorking)
	// The case narrative is funded from the global pool, whole field by whole
	// field, in caseNarrative's order.
	fields := caseNarrative(&d)
	withheld := false
	for _, field := range fields {
		if len(*field) > prose {
			if *field != "" {
				withheld = true
				*field = ""
			}
			continue
		}
		prose -= len(*field)
	}
	if withheld {
		v.Projected = append(v.Projected, projectedCase)
	}

	claims := claimReferences(d.Claims)
	droppedText, droppedEvents := false, false
	for i := range claims {
		// Ruling R4: "claim text already carried by its cited passage." Only
		// where it actually is — a claim whose quotation did not fit keeps its
		// own words, because nothing else in the prompt then says what it
		// claimed.
		quoted := false
		for _, passage := range p.research.Dossier.Claims[i].Passages {
			if passage.Quote != "" {
				quoted = true
			}
		}
		if quoted && !omitted[claims[i].ID] {
			claims[i].Text = ""
			droppedText = true
		}
	}
	d.Claims = claims
	events := append([]model.ResearchEvent(nil), d.Events...)
	for i := range events {
		// An event passage is a verbatim source excerpt that evidenceClaims
		// already reserved as a required quotation, so it is in the document
		// text. Spec: accepted quotations appear in source text once.
		if events[i].Passage != "" && !omitted[fmt.Sprintf("event-%d", i)] {
			events[i].Passage = ""
			droppedEvents = true
		}
	}
	d.Events = events
	if droppedText {
		v.Projected = append(v.Projected, projectedClaimText)
	}
	if droppedEvents {
		v.Projected = append(v.Projected, projectedEventQuotes)
	}
	v.Dossier = &d

	ch := p.research.Challenge
	ch.Claims = claimReferences(ch.Claims)
	reviews := append([]model.ClaimReview(nil), ch.ClaimReviews...)
	droppedReviews := false
	for i := range reviews {
		// compactReviewProblems already refuses a supported verdict that
		// retains a disputed or unresolved claim, so on a supported challenge
		// every review reason restates a conclusion Go has verified. A review
		// that is not clean keeps its reason: that one is the fact the Chief
		// has to weigh.
		if reviews[i].Assessment == "supported" && reviews[i].Attribution == "confirmed" {
			reviews[i].Reason = ""
			droppedReviews = true
		}
	}
	ch.ClaimReviews = reviews
	if droppedReviews {
		v.Projected = append(v.Projected, projectedReviews)
	}
	v.Challenge = &ch

	ages := v.Temporal.EvidenceAges[:0:0]
	droppedAges := false
	for _, a := range v.Temporal.EvidenceAges {
		if !p.cited[a.EvidenceID] {
			continue
		}
		// An age in hours is published_at subtracted from as_of, and the
		// Chief has both. A note is not derivable from anything in the
		// prompt, so it stays.
		if a.Note == "" {
			droppedAges = true
			continue
		}
		ages = append(ages, a)
	}
	v.Temporal.EvidenceAges = ages
	if droppedAges {
		v.Projected = append(v.Projected, projectedAges)
	}
	return v
}

func boardProjections(board []chiefCompany) []string {
	var out []string
	for _, c := range board {
		for _, name := range c.Projected {
			out = appendUnique(out, name)
		}
	}
	sort.Strings(out)
	return out
}
