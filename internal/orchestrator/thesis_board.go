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
//     only the remainder is spent on optional context. That remainder funds
//     two tiers — the dossiers' case narrative, by rank across the whole
//     board, and then source context, split in proportion to what each
//     company still wants — and what the first tier cannot spend flows to the
//     second rather than being burned. Neither tier is ever handed out
//     company by company until it runs out.
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
	// projectedUnselectable replaces a whole evidence record: the dossier's
	// claims, events, case narrative and cited sources, and the review's
	// per-claim record. It applies only to a dossier its final review left
	// unsupported, which the Chief may not select.
	projectedUnselectable = "evidence_of_unselectable_dossier"
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
// That indivisibility is why fundCaseNarrative funds this order by rank
// across the whole board rather than by splitting a pool between companies.
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

// chiefPromptBuilder assembles the Chief's prompt sections around a board
// sized for what the rest of THAT call carries — which is not the same for
// every Chief call.
//
// The Chief is called up to twice on a thesis run: once to produce ideas, and
// once to correct them. The corrective call sends the same four sections plus
// two more MANDATORY ones, `corrective_findings` and `previous_chief_response`.
// Because the board is deliberately built to spend its whole budget, anything
// appended after it is appended past the input limit: measured on the twelve-
// company September 15 board, the initial prompt lands 1,015 bytes under the
// 196,608-byte limit, and even a deliberately modest corrective payload (five
// ideas with a 600-character `why` and three 200-character findings) is 4,421
// bytes. From eight usable dossiers upward the corrective call could therefore
// never be assembled at all, and finalizeThesis deleted every idea carrying a
// non-observational finding instead of letting the Chief revise it — reported
// as a component-less input_capacity error one call downstream, which is the
// very diagnostic failure this board exists to remove.
//
// Sizing the board for the extra sections at the moment they are known, rather
// than reserving a flat allowance in chiefBoardBudget, is what keeps the
// initial call whole. The reservation would have to be an upper bound on a
// previous response (the Chief's own ResponseBytes is 24 KB) plus a findings
// allowance, and a twelve-company board's required floor already occupies
// 169,696 of its 180,944 bytes: subtracting a 24 KB reserve from every board
// would turn a board that fits today into a capacity error. The corrective
// board instead gives up only optional context, and only on the call that
// needs the room.
type chiefPromptBuilder struct {
	budget   int
	prefix   string
	other    []promptSection
	research []thesisResearch
}

func (t *thesisRunner) chiefPrompt(research []thesisResearch, other []promptSection, prefix string) (chiefPromptBuilder, error) {
	budget, err := t.chiefBoardBudget(other, prefix)
	return chiefPromptBuilder{budget: budget, prefix: prefix, other: other, research: research}, err
}

// sections returns the Chief's sections for one call, with the board budgeted
// for whatever extra mandatory sections that call appends. extra is measured
// redacted, exactly as chiefBoardBudget measures every other section and
// exactly as preparePrompt will measure it.
func (b chiefPromptBuilder) sections(extra ...promptSection) ([]promptSection, boardAllocation, error) {
	reserve := 0
	for _, sec := range extra {
		reserve += len(redact.String(sec.Body))
	}
	board, alloc, err := chiefBoard(b.research, b.budget-reserve)
	out := make([]promptSection, 0, len(b.other)+len(extra)+1)
	out = append(out, promptSection{Name: "company_board", Mandatory: true, Body: b.prefix + jsonText(board)})
	out = append(out, b.other...)
	return append(out, extra...), alloc, err
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
	TextBudget    int `json:"text_budget"`
	RequiredBytes int `json:"required_bytes"`
	BoardBytes    int `json:"board_bytes"`
	OptionalPool  int `json:"optional_pool_bytes"`
	// NarrativeBytes is what the case-narrative tier took out of
	// OptionalPool; the rest of the pool went to source context. Both tiers
	// are recorded because the split between them is the allocation decision
	// an operator most needs to see, and a per-company grant alone cannot
	// show it.
	NarrativeBytes int          `json:"case_narrative_bytes"`
	Attempts       int          `json:"fit_attempts"`
	Projected      []string     `json:"projected_away"`
	Companies      []boardShare `json:"companies"`
}

// boardPlan is one company's inputs to the allocation, computed once so that
// neither pass has to redo them and so that nothing in either pass depends on
// the company's position in the slice.
type boardPlan struct {
	research thesisResearch
	usable   bool
	// selectable is a usable dossier whose final review left it supported —
	// the only kind the Chief may turn into a plan.
	selectable bool
	anchor     time.Time
	docs       []model.EvidenceDocument // anchor-filtered, cited only
	claims     []model.ResearchClaim
	cited      map[string]bool
	required   int // characters of required quotation
	narrative  int // bytes of case narrative this company would like to carry
	need       int // characters of optional context this company could still use
}

func planCompany(r thesisResearch) boardPlan {
	p := boardPlan{research: r}
	if r.researchFailed() || r.Eligibility != "" {
		return p
	}
	p.usable = true
	p.anchor, _ = time.Parse(time.RFC3339, r.Temporal.AsOf)
	// A dossier its final review left unsupported cannot become a plan, and
	// the Chief's only job for it is a decision. Its evidence costs as much as
	// a selectable company's — 16–22 KB of required quotation each on the
	// 2026-09-24 run, where eleven researched names needed 204 KB against a
	// 179 KB board and the Chief was never called. It reaches the Chief as its
	// verdict and the reasons for it, and reserves nothing from the budget.
	p.selectable = r.Dossier.Status == "supported"
	if !p.selectable {
		return p
	}
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

// boardBytes measures a board the way preparePrompt will measure it: after
// redaction. chiefBoardBudget subtracts every other section's *redacted*
// length, because that is the body assembleSections receives, and the board
// has to be measured on the same side of the same function — redact.String
// substitutes a 21-byte placeholder, so a registered credential of 8 to 20
// characters echoed back into evidence text or retained in a document's
// `error` makes the board GROW after it was checked. (internal/redact's own
// package doc cites the live case: AlphaVantage answers a rejected call with
// prose quoting the whole query string, apikey included.) A board measured
// raw would then overflow the prompt it was just certified to fit, and
// surface as a component-less input_capacity failure — the exact diagnostic
// the board budget exists to replace.
func boardBytes(board []chiefCompany) int {
	return len(redact.String(jsonText(board)))
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
	alloc.RequiredBytes = boardBytes(board)
	alloc.BoardBytes = alloc.RequiredBytes
	if alloc.RequiredBytes > textBudget {
		// The board is returned with the error, not instead of it. The caller
		// still assembles and still dispatches nothing: preparePrompt refuses
		// the oversized prompt for the primary and the fallback alike, which
		// is what persists a zero-attempt input_capacity failure for each
		// engine. Returning nil here would instead skip the Chief entirely and
		// erase that provenance. What this error adds is the part preparePrompt
		// cannot say — which companies' required records did not fit.
		costReport := requiredCostReport(plans)
		return board, alloc, promptCapacityError{fmt.Errorf(
			"input capacity exceeded: the Chief board's required records and quotations need %d bytes against %d available for %d companies (%s); no optional narrative or source context had been added",
			alloc.RequiredBytes, textBudget, len(plans), strings.Join(costReport, ", ")), costReport}
	}
	contextNeeds := make([]int, len(plans))
	for i, p := range plans {
		contextNeeds[i] = p.need
	}
	pool := textBudget - alloc.RequiredBytes
	narrative, context := make([]int, len(plans)), make([]int, len(plans))
	for attempt := 1; attempt <= boardFitAttempts; attempt++ {
		alloc.Attempts, alloc.OptionalPool = attempt, pool
		// The case narrative is funded first, by rank across the whole board
		// (fundCaseNarrative), and what it actually SPENDS — which for a
		// rank-funded grant is the grant, to the byte — is what leaves the
		// pool. Everything it cannot use goes to source context, which is
		// divisible and can always absorb it. Granting an indivisible tier a
		// share and then never reclaiming what that share failed to buy is
		// what burned the whole optional pool at the calibrated budget: the
		// board came out at exactly the required floor with 100% of the pool
		// spent on nothing.
		narrative = fundCaseNarrative(plans, pool)
		granted := 0
		for _, n := range narrative {
			granted += n
		}
		alloc.NarrativeBytes = granted
		context = splitByNeed(contextNeeds, pool-granted)
		next := renderBoard(plans, narrative, context)
		size := boardBytes(next)
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
		// The strict-progress floor is pool-1, so a rescale that fails to get
		// under budget steps down by exactly one byte per attempt and then
		// collapses to zero on the last one. There is deliberately no
		// intermediate between "converged" and "no optional context at all":
		// a pool the rescale cannot price is a pool this loop has no evidence
		// about, and the floor board is the one size already proven to fit.
		pool = min(scaled, pool-1)
		if pool < 0 || attempt == boardFitAttempts {
			pool = 0
		}
		if pool == 0 {
			narrative, context = fundCaseNarrative(plans, 0), splitByNeed(contextNeeds, 0)
			board, alloc.BoardBytes, alloc.OptionalPool, alloc.NarrativeBytes = renderBoard(plans, narrative, context), alloc.RequiredBytes, 0, 0
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
		rows = append(rows, row{p.research.Candidate.Ticker, len(redact.String(jsonText(p.render(0, 0))))})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].bytes > rows[j].bytes })
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s %d", r.ticker, r.bytes))
	}
	return out
}

// fundCaseNarrative funds the board's case narrative by RANK rather than by
// company: caseNarrative's fields are funded for every company or for none,
// in the order that function states, for as long as the whole board's cost of
// the next rank still fits the allowance.
//
// Two reasons, and the first is arithmetic. A narrative field is indivisible —
// half a case is not half an argument — so a per-company share of a small pool
// buys nothing at all while consuming all of it. Measured on the twelve-company
// September 15 board, whose fields run 183 to 1,017 bytes: a 1,000-byte pool
// split proportionally granted ~83 bytes per company, funded zero fields, and
// returned nothing, so the board came out at exactly the required floor with
// 100% of its pool spent on nothing; at the calibrated production pool of
// 11,248 the same split granted ~937 bytes per company and left 1,015 bytes
// unspendable, which is why source context was never funded in production at
// all. Funding by rank spends in the only units this tier can use, and hands
// what it cannot use to source context, which is divisible.
//
// The second reason is that the Chief's job is to RANK twelve companies
// against each other. A board where some companies carry a short case and
// others do not invites the comparison to be decided by which argument was
// funded rather than by which is stronger; funding by rank keeps the board
// symmetric, so every company is argued for to the same depth or to none.
//
// The result is a pure function of the whole needs matrix and the allowance,
// never of a company's position in the slice. Each company's grant is exactly
// the sum of the ranks funded, and render spends a grant on whole fields in
// this same order, so the grant is spent to the byte: nothing granted here can
// go unspent, which is the property the proportional split could not offer.
func fundCaseNarrative(plans []boardPlan, allowance int) []int {
	grants := make([]int, len(plans))
	costs := make([][]int, len(plans))
	ranks := 0
	for i, p := range plans {
		if !p.usable {
			continue
		}
		d := p.research.Dossier
		for _, field := range caseNarrative(&d) {
			costs[i] = append(costs[i], len(*field))
		}
		ranks = max(ranks, len(costs[i]))
	}
	for rank := 0; rank < ranks; rank++ {
		cost := 0
		for i := range plans {
			if rank < len(costs[i]) {
				cost += costs[i][rank]
			}
		}
		if cost > allowance {
			break
		}
		allowance -= cost
		for i := range plans {
			if rank < len(costs[i]) {
				grants[i] += costs[i][rank]
			}
		}
	}
	return grants
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
	if !p.selectable {
		return p.renderUnselectable(v)
	}
	docs := promptDocuments(p.docs, p.required+extra, p.claims...)
	// placed is the text each cited source actually contributes to this
	// prompt. It is what decides whether a claim's own words may be dropped,
	// because "the quotation carries the claim" is a statement about this
	// prompt and not about the research artifact. Asking OmittedClaimIDs
	// instead cannot answer it: that list is populated only when a quotation
	// was present in the source and did not fit, and render always calls
	// promptDocuments with at least p.required, so it is never populated from
	// the board at all. The cases it misses are the live ones — a document
	// published after the run anchor (visibleAt blanks its text) and a
	// document that failed retrieval (requiredSpans returns nothing for it) —
	// where the quote exists on the dossier, reaches no document, and the
	// claim would be shipped with neither text nor quotation.
	placed := make(map[string]string, len(docs))
	for _, d := range docs {
		placed[d.ID] = d.Text
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
		// where it actually is — the test is whether the quotation reached a
		// rendered document in this prompt, not whether the dossier recorded
		// one. A claim whose quotation is nowhere keeps its own words, because
		// nothing else in the prompt then says what it claimed.
		quoted := false
		for _, passage := range p.research.Dossier.Claims[i].Passages {
			if passage.Quote != "" && strings.Contains(placed[passage.EvidenceID], passage.Quote) {
				quoted = true
			}
		}
		if quoted {
			claims[i].Text = ""
			droppedText = true
		}
	}
	d.Claims = claims
	events := append([]model.ResearchEvent(nil), d.Events...)
	for i := range events {
		// An event passage is a verbatim source excerpt that evidenceClaims
		// already reserved as a required quotation, so it is in the document
		// text — when the document has text at all. Spec: accepted quotations
		// appear in source text once; once is not zero.
		if events[i].Passage != "" && strings.Contains(placed[events[i].EvidenceID], events[i].Passage) {
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
		// A review that is both supported and attribution-confirmed has no
		// content left beyond the two verdicts recorded beside it, so its
		// prose reason restates them. This fires per review and does not rest
		// on the overall verdict: compactReviewProblems' rule
		// (thesis_contract.go:79) is conditioned on the challenge being
		// `supported` and runs only for ContractVersion 2, so on a revise or
		// reject challenge the clean reviews lose their reasons with no
		// Go-side rule behind the drop. That is intended — R4's must-survive
		// list names the per-claim assessment and attribution, both retained,
		// and the reason is prose. A review that is not clean keeps its
		// reason: that one is the fact the Chief has to weigh.
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

// renderUnselectable is the decision record of a dossier the Chief may not
// select: its status, direction, lean and labels, the unresolved gaps and
// disclosed risks, and the review's verdict with the issues behind it. What is
// withheld is the evidence for a thesis nobody may act on, and the record says
// so rather than reading as research that found nothing.
func (p boardPlan) renderUnselectable(v chiefCompany) chiefCompany {
	d := p.research.Dossier
	d.Claims, d.Events, d.Requests = nil, nil, nil
	d.Hypothesis, d.Changed, d.Expectations = "", "", ""
	d.Underappreciated, d.Mechanism, d.PricedIn, d.Counterargument = "", "", "", ""
	for _, field := range caseNarrative(&d) {
		*field = ""
	}
	v.Dossier = &d
	ch := p.research.Challenge
	ch.Claims, ch.ClaimReviews, ch.Requests = nil, nil, nil
	v.Challenge = &ch
	v.Temporal.EvidenceAges = nil
	v.Projected = []string{projectedUnselectable}
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
