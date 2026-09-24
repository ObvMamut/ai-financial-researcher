package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

// Schema handling for the thesis roles.
//
// The 2026-09-07 run lost half its shortlist to eight decode failures: payloads
// with an array where a string belonged, a string where an array belonged, and
// objects that stopped at the first required field. Two companies finished with
// an empty dossier, and every one of those calls was still recorded `done` —
// metadata could not tell a response from usable research.
//
// Two things follow. A payload states its structural problems explicitly rather
// than failing on the first type error json/encoding happens to hit, and a
// malformed payload buys exactly one repair call before any substantive review.
// A repair re-serialises what the model already said; it never invents a claim,
// and a repaired dossier is recorded as repaired rather than promoted.

// schemaError distinguishes a structurally invalid payload from a transport
// failure. Both stop a dossier; only one of them means the model was reached.
type schemaError struct {
	problems []string
	cause    error
}

func (e schemaError) Error() string {
	if len(e.problems) > 0 {
		return strings.Join(e.problems, "; ")
	}
	if e.cause != nil {
		return e.cause.Error()
	}
	return "invalid schema"
}
func (e schemaError) Unwrap() error { return e.cause }

func claimProblems(cs []model.ResearchClaim) []string {
	var out []string
	for i, c := range cs {
		if strings.TrimSpace(c.ID) == "" {
			out = append(out, fmt.Sprintf("claim %d has no \"id\"", i+1))
		}
		if c.Kind != "observation" && c.Kind != "inference" {
			out = append(out, fmt.Sprintf("claim %q has kind %q; it must be \"observation\" or \"inference\"", c.ID, c.Kind))
		}
	}
	return out
}

// dossierSchema reports what is structurally wrong with a decoded dossier. It
// is separate from validateDossier, which judges whether the research holds up:
// this asks only whether the model returned the object it was asked for.
//
// It checks types and enums, not presence. An absent array and an empty one say
// the same thing about a dossier — no claims is no claims — so demanding `[]`
// would buy a repair call for a payload that was already understood. The two
// places presence *is* checked are the ones where absence is ambiguous: a
// triage result and the Chief's ideas/decisions, where "I selected none" and
// "I did not answer" must not look alike.
//
// Requests are deliberately not checked here. A malformed request is answered
// by the retrieval protocol, which tells the model what the operation was and
// why nothing came of it; failing the whole dossier over one request field
// would throw away the research to correct the postscript.
func dossierSchema(d *model.CandidateDossier) []string {
	out := dossierLimits(d)
	if strings.TrimSpace(d.Ticker) == "" {
		out = append(out, `"ticker" is required`)
	}
	switch d.Status {
	case "supported", "watchlist", "rejected":
	case "":
		out = append(out, `"status" is required; use "supported", "watchlist" or "rejected"`)
	default:
		out = append(out, fmt.Sprintf("status %q is not one of supported, watchlist, rejected", d.Status))
	}
	switch d.EvidenceQuality {
	case "strong", "mixed", "insufficient":
	case "":
		out = append(out, `"evidence_quality" is required; use "strong", "mixed" or "insufficient"`)
	default:
		out = append(out, fmt.Sprintf("evidence_quality %q is not one of strong, mixed, insufficient", d.EvidenceQuality))
	}
	out = append(out, claimProblems(d.Claims)...)
	for i, e := range d.Events {
		if e.Kind == "" || e.OccurredAt == "" || e.EvidenceID == "" || e.Passage == "" {
			out = append(out, fmt.Sprintf("event %d needs kind, occurred_at, evidence_id and passage, or must be omitted", i+1))
		}
	}
	return out
}

// challengeSchema reports what is structurally wrong with a decoded challenge.
func challengeSchema(c *model.ThesisChallenge) []string {
	var out []string
	if strings.TrimSpace(c.Reason) == "" {
		out = append(out, "challenge requires a substantive reason")
	}
	if strings.TrimSpace(c.Ticker) == "" {
		out = append(out, `"ticker" is required`)
	}
	switch c.Verdict {
	case "supported", "revise", "reject":
	case "":
		out = append(out, `"verdict" is required; use "supported", "revise" or "reject"`)
	default:
		out = append(out, fmt.Sprintf("verdict %q is not one of supported, revise, reject", c.Verdict))
	}
	out = append(out, claimProblems(c.Claims)...)
	return out
}

func scoutSchema(r *model.ScoutResult) []string {
	if r.Candidates == nil {
		return []string{`"candidates" must be an array, even when empty`}
	}
	return nil
}

// decodeThesis decodes a fenced payload and then checks its structure, so a
// caller sees every problem at once instead of whichever type error the decoder
// reached first.
func decodeThesis[T any](raw string, v *T, check func(*T) []string) error {
	var next T
	if err := decodeResearch(raw, &next); err != nil {
		return schemaError{cause: err}
	}
	if problems := check(&next); len(problems) > 0 {
		return schemaError{problems: problems}
	}
	*v = next
	return nil
}

// schemaRepairPrompt asks for the same content in the right shape. It carries
// the model's own previous output so the repair is a re-serialisation, and says
// so explicitly: a repair that adds a claim or an evidence ID is a fabrication,
// not a fix.
func schemaRepairPrompt(err error, previous string) string {
	var sb strings.Builder
	sb.WriteString("Your previous response could not be read as the JSON object your instructions specify.\n")
	sb.WriteString("Problems:\n")
	var se schemaError
	if ok := asSchemaError(err, &se); ok && len(se.problems) > 0 {
		for _, p := range se.problems {
			sb.WriteString("- " + p + "\n")
		}
	} else {
		sb.WriteString("- " + err.Error() + "\n")
	}
	sb.WriteString("\nReturn the same findings again as one fenced JSON object in the exact shape described above.\n")
	sb.WriteString("This is a formatting correction only. Do not add, remove or alter any finding, claim, evidence ID, status or verdict; do not introduce a claim or an evidence ID that is not already in your previous response; do not upgrade a status or a verdict. If a required array has no members, return it empty.\n")
	sb.WriteString("\nYour previous response:\n")

	sb.WriteString(previous)
	sb.WriteString("\n")
	return sb.String()
}

func asSchemaError(err error, out *schemaError) bool {
	for err != nil {
		if se, ok := err.(schemaError); ok {
			*out = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// decodeOrRepair decodes one role's response, and buys exactly one repair call
// when it will not decode. The repair happens before any substantive review, so
// a reviewer never reads a half-empty dossier that was only ever a formatting
// accident — and it is bounded at one so a model that cannot produce the schema
// costs one extra call rather than a round of them.
//
// It reports the parsing outcome so the caller can record it: a repaired
// payload is usable research that arrived malformed, which is not the same fact
// as research that arrived clean.
func decodeOrRepair[T any](ctx context.Context, t *thesisRunner, role, name, raw string, v *T, check func(*T) []string) (parsing string, reports []model.DomainStatus, err error) {
	if err = decodeThesis(raw, v, check); err == nil {
		return model.OutcomeOK, nil, nil
	}
	if ctx.Err() != nil {
		return model.OutcomeFailed, nil, err
	}
	first := err
	r, callErr := t.call(ctx, role, name+"-schema-repair", schemaRepairPrompt(first, raw), t.cheapTarget())
	status := reportStatus(r)
	status.Recovery = "schema_repair"
	status.Payload = "invalid"
	if callErr != nil {
		return model.OutcomeFailed, []model.DomainStatus{status}, fmt.Errorf("schema repair unavailable (%v); original: %w", callErr, first)
	}
	if e := decodeThesis(r.Stdout, v, check); e != nil {
		return model.OutcomeFailed, []model.DomainStatus{status}, fmt.Errorf("schema repair did not decode (%v); original: %w", e, first)
	}
	status.Payload = model.OutcomeRepaired
	return model.OutcomeRepaired, []model.DomainStatus{status}, nil
}

// researchCall makes one thesis call from a single plain data string and
// decodes its payload, recording the two facts separately: whether the
// engine answered at all, and whether what it said could be read. Both were
// previously collapsed into a `done` status, so a run whose research half
// returned unreadable JSON reported that its research had completed.
//
// A payload that never decodes leaves the call `done` — it did complete — with
// Payload "invalid". That is the distinction; the run's outcome reads both.
//
// It is a thin wrapper over researchCallSections for the call sites that have
// no named sections worth measuring separately (screening, event discovery);
// see call/callSections (thesis.go) for the equivalent split one level down.
func researchCall[T any](ctx context.Context, t *thesisRunner, role, name, data string, v *T, check func(*T) []string) (reports []model.DomainStatus, transport, parsing string, err error) {
	return researchCallSections(ctx, t, role, name, singleSection(data), v, check)
}

// researchCallSections is researchCall's sibling for the thesis prompt sites
// built from named promptSections (thesis_sections.go) rather than one
// already-concatenated string.
func researchCallSections[T any](ctx context.Context, t *thesisRunner, role, name string, sections []promptSection, v *T, check func(*T) []string) (reports []model.DomainStatus, transport, parsing string, err error) {
	check = currentResearchSchema(check)
	r, callErr := t.callSections(ctx, role, name, sections, t.researchTarget(role))
	status := reportStatus(r)
	if r.FailureKind == "response_capacity" {
		// The engine returned a complete response. Its byte-budget failure is
		// distinct from transport or JSON failure and can spend one compaction.
		status.Status, status.Contract, status.Payload = model.StatusDone, model.OutcomeFailed, "invalid"
		var decoded T
		if err := decodeThesis(r.Stdout, &decoded, check); err != nil {
			return []model.DomainStatus{status}, model.OutcomeOK, model.OutcomeFailed, err
		}
		if dossier, ok := any(v).(*model.CandidateDossier); ok {
			recovery, err := compactDossier(ctx, t, role, name, r.Stdout, r.Prompt.ResponseLimit, dossier)
			if err == nil {
				status.Contract, status.Payload = "compacted", model.OutcomeOK
				clearComputedComparisons(dossier)
			}
			return []model.DomainStatus{status, recovery}, model.OutcomeOK, model.OutcomeOK, err
		}
		return []model.DomainStatus{status}, model.OutcomeOK, model.OutcomeOK, callErr
	}
	if callErr != nil {
		status.Payload = "invalid"
		if r.Attempts == 0 && r.FailureKind == "input_capacity" {
			status.Payload = model.OutcomeNotAttempted
			// Dispatch was refused before a subprocess ran or an HTTP request
			// was sent: neither the transport nor the parser ever touched
			// this call, so neither claim of "failed" is true. The company
			// still fails (see model.OutcomeNotAttempted's own comment) —
			// only this fact about *how* is corrected.
			return []model.DomainStatus{status}, model.OutcomeNotAttempted, model.OutcomeNotAttempted, callErr
		}
		return []model.DomainStatus{status}, model.OutcomeFailed, model.OutcomeFailed, callErr
	}
	parsing, repairs, decodeErr := decodeOrRepair(ctx, t, role, name, r.Stdout, v, check)
	if decodeErr == nil {
		switch value := any(v).(type) {
		case *model.CandidateDossier:
			clearComputedComparisons(value)
			status.WritingDiagnostics = dossierWritingDiagnostics(value)
		case *model.ThesisChallenge:
			if value.Verdict == "supported" {
				for _, c := range value.Claims {
					if c.Comparison != nil {
						value.MaterialIssues = appendIssue(value.MaterialIssues, issue(model.IssueGrounding, "new numerical comparison requires researcher validation: "+c.ID))
					}
				}
			}
		}
	}
	status.Payload = parsing
	status.Contract = model.OutcomeOK
	if decodeErr != nil {
		status.Payload = "invalid"
		status.Err = decodeErr.Error()
	}
	return append([]model.DomainStatus{status}, repairs...), model.OutcomeOK, parsing, decodeErr
}

func clearComputedComparisons(d *model.CandidateDossier) {
	for i := range d.Claims {
		if n := d.Claims[i].Comparison; n != nil {
			n.NormalizedTarget, n.UpsidePct, n.FX, n.Problem = nil, nil, nil, ""
		}
	}
}

// worsePayload keeps the worst parsing outcome a candidate saw. A dossier that
// needed a repair on one round is a repaired dossier however clean the rest were.
func worsePayload(current, next string) string {
	rank := map[string]int{model.OutcomeOK: 0, model.OutcomeRepaired: 1, model.OutcomeNotAttempted: 2, model.OutcomeFailed: 3}
	if current == "" {
		return next
	}
	if rank[next] > rank[current] {
		return next
	}
	return current
}

// classifyEvidence says what the retrieval loop actually reached, independently
// of what the model then claimed about it. A dossier written over nothing but
// headlines is a different failure from one written over nothing at all, and
// neither is a rejection.
func classifyEvidence(docs []model.EvidenceDocument) string {
	substantive, usable := 0, 0
	for _, d := range docs {
		if d.Error != "" {
			continue
		}
		usable++
		if marketdata.SubstantiveResearchDocument(d) {
			substantive++
		}
	}
	switch {
	case substantive > 0:
		return model.EvidenceDocuments
	case usable > 0:
		return model.EvidenceThin
	default:
		return model.EvidenceNone
	}
}

// Historical decoders remain permissive; newly generated research must use the
// current contract, including when a provider omits its version discriminator.
// That includes a dossier's lean and label enums and a review's issue
// categories, which no historical artifact carries.
func currentResearchSchema[T any](check func(*T) []string) func(*T) []string {
	return func(v *T) []string {
		problems := check(v)
		switch value := any(v).(type) {
		case *model.CandidateDossier:
			if value.ContractVersion != 2 {
				problems = append(problems, "new dossiers require contract_version 2")
			}
			problems = append(problems, dossierLabelProblems(value)...)
		case *model.ThesisChallenge:
			if value.ContractVersion != 2 {
				problems = append(problems, "new challenges require contract_version 2")
			}
			problems = append(problems, materialIssueProblems(value)...)
		}
		return problems
	}
}
