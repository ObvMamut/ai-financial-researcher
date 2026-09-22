package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

var dossierNarrativeFields = []string{"long_case", "short_case", "no_trade_case", "hypothesis", "changed", "expectations", "underappreciated", "mechanism", "priced_in", "counterargument", "invalidation", "catalyst_window"}

func dossierNarratives(d model.CandidateDossier) map[string]string {
	return map[string]string{"long_case": d.LongCase, "short_case": d.ShortCase, "no_trade_case": d.NoTradeCase, "hypothesis": d.Hypothesis, "changed": d.Changed, "expectations": d.Expectations, "underappreciated": d.Underappreciated, "mechanism": d.Mechanism, "priced_in": d.PricedIn, "counterargument": d.Counterargument, "invalidation": d.Invalidation, "catalyst_window": d.CatalystWindow}
}

// Compare the original wire objects, including unknown fields. Decoding only the
// Go struct would silently permit a compactor to discard fields we do not know yet.
func compactionPreservesEvidence(before, after string) error {
	var a, b map[string]json.RawMessage
	if err := decodeResearch(before, &a); err != nil {
		return err
	}
	if err := decodeResearch(after, &b); err != nil {
		return err
	}
	for _, field := range dossierNarrativeFields {
		var original, compact string
		if err := json.Unmarshal(a[field], &original); err != nil {
			return fmt.Errorf("original %s: %w", field, err)
		}
		if err := json.Unmarshal(b[field], &compact); err != nil {
			return fmt.Errorf("compacted %s: %w", field, err)
		}
		if strings.TrimSpace(original) != "" && strings.TrimSpace(compact) == "" {
			return fmt.Errorf("compaction erased %s", field)
		}
		delete(a, field)
		delete(b, field)
	}
	// UseNumber preserves exact numerical representations rather than rounding
	// large values through float64 while comparing the protected data.
	decode := func(v map[string]json.RawMessage) (any, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		var out any
		err = dec.Decode(&out)
		return out, err
	}
	x, err := decode(a)
	if err != nil {
		return err
	}
	y, err := decode(b)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(x, y) {
		return fmt.Errorf("compaction changed protected claims, evidence, conditions, uncertainty or status")
	}
	return nil
}

// compactionHeadroom absorbs what a model cannot be expected to predict when
// writing to a byte budget rather than a character count: JSON string
// escaping, and the re-serialization drift the PROTECTED (non-narrative)
// fields themselves exhibit across a compaction round-trip. Measured
// directly against the six real Sept 15 compaction pairs
// (task-9-context.md): compactionPreservesEvidence passes on all six — no
// protected content is lost anywhere — but the model re-emits protected
// strings (claims, monitoring) with different escaping, shifting their
// serialized byte count by up to 63 bytes (REGN's claims -55, monitoring -8;
// NOKIA.HE's claims -5; the other four, zero). Both observed swings happened
// to shrink the result, but the mechanism is symmetric: the HTML-escaping
// measurement below independently shows re-encoding can also inflate a
// string, by up to 45 bytes on one artifact. 256 is roughly 4x the worst
// swing actually observed — a measured worst case with margin, not a round
// number someone liked.
const compactionHeadroom = 256

// compactionPerFieldFloor is the minimum byte allowance any currently
// non-empty narrative field receives. Below it a "budget" is not usable
// instruction — a few words is not a shortened case, it is noise.
const compactionPerFieldFloor = 40

// measureCompaction computes how much room a compaction call actually has,
// instead of assuming a fixed per-field character count. On the six real
// oversized responses that needed compaction, "aim for under 400 characters
// each" (~4,800 bytes total) was between 121 bytes too generous and 668
// bytes too tight — never the right number for any of them
// (task-9-context.md).
//
// PayloadBytes reuses measureResponse (Task 8) directly rather than
// recomputing it: both must agree on what "the payload" is, and duplicating
// the extraction/json.Compact logic here would let the two diverge.
//
// ProtectedBytes is the floor a compaction call may never cut into: decode
// raw into map[string]json.RawMessage, empty every field in
// dossierNarrativeFields — the only fields a compaction call may touch — and
// remeasure. That remeasurement re-marshals the payload, and json.Marshal
// rewrites '<', '>' and '&' as six-byte \u00XX escapes even inside a
// json.RawMessage, which inflates the floor and understates the narrative
// budget that remains; on real artifacts with no narrative fields at all,
// the default marshaller reported a protected floor bigger than the whole
// payload (task-9-context.md). An Encoder with SetEscapeHTML(false) avoids
// it; TestMeasureCompactionProtectedFloorIgnoresHTMLEscaping pins this.
//
// NarrativeBudget is what remains for the twelve narrative fields combined
// once Limit, ProtectedBytes and compactionHeadroom are accounted for.
// PerField splits that budget across the fields that are currently
// non-empty, proportionally to each field's current UTF-8 byte length, with
// a floor of compactionPerFieldFloor for any of them. Feasible is false when
// even those floors cannot fit inside NarrativeBudget: a call that must
// never be dispatched, because no rewrite of the narrative fields alone can
// reach the budget (a protected floor that alone exceeds Limit is the
// extreme case of this, not a separate one — NarrativeBudget is already
// deeply negative and the floor check catches it the same way).
func measureCompaction(raw string, limit int) (model.CompactionAllowance, error) {
	m := measureResponse(raw)
	a := model.CompactionAllowance{PayloadBytes: m.PayloadBytes, Limit: limit, Excess: m.PayloadBytes - limit}

	var obj map[string]json.RawMessage
	if err := decodeResearch(raw, &obj); err != nil {
		return a, err
	}

	fieldSize := make(map[string]int, len(dossierNarrativeFields))
	protected := make(map[string]json.RawMessage, len(obj))
	for k, v := range obj {
		protected[k] = v
	}
	for _, f := range dossierNarrativeFields {
		v, ok := obj[f]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			// Decoded UTF-8 bytes, not the JSON-quoted/escaped form — a
			// different unit than NarrativeBudget, which is derived from
			// compact PAYLOAD bytes where a narrative appears quoted and
			// possibly escaped. Measured on all six real Sept 15 dossiers
			// the gap between the two units is exactly zero in every
			// narrative field (none contains a quote, backslash or control
			// character that JSON would escape), so this is the right unit
			// for real content; compactionHeadroom absorbs the general case
			// where a field does contain one.
			fieldSize[f] = len(s)
		}
		protected[f] = json.RawMessage(`""`)
	}

	var encoded bytes.Buffer
	enc := json.NewEncoder(&encoded)
	enc.SetEscapeHTML(false) // see doc comment: the default marshaller inflates this floor
	if err := enc.Encode(protected); err != nil {
		return a, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, encoded.Bytes()); err != nil {
		return a, err
	}
	a.ProtectedBytes = compact.Len()
	a.NarrativeBudget = limit - a.ProtectedBytes - compactionHeadroom
	a.PerField = allocatePerField(fieldSize, a.NarrativeBudget)
	a.Feasible = perFieldFloorsFit(fieldSize, a.NarrativeBudget)
	return a, nil
}

// perFieldFloorsFit reports whether every currently non-empty narrative
// field can receive at least compactionPerFieldFloor bytes without the sum
// exceeding budget: compactionPerFieldFloor*n <= budget, where n counts the
// currently non-empty fields.
//
// It returns false whenever budget is negative, for ANY n — including n==0
// (no narrative content at all): 0 <= budget is false for a negative
// budget, so a protected floor that alone leaves no room under the limit is
// correctly reported infeasible even though there is, in that shape,
// nothing left to shrink at all.
//
// n==0 combined with a non-negative budget WOULD report true, but that
// combination cannot happen from production: with no narrative content,
// PayloadBytes equals ProtectedBytes exactly, so responseCapacity's own
// PayloadBytes > Limit check — the only path that ever reaches
// measureCompaction — could not have fired in the first place. It is
// reachable only by calling measureCompaction directly, as some tests do to
// isolate this arm.
func perFieldFloorsFit(fieldSize map[string]int, budget int) bool {
	n := 0
	for _, f := range dossierNarrativeFields {
		if fieldSize[f] > 0 {
			n++
		}
	}
	return compactionPerFieldFloor*n <= budget
}

// allocatePerField splits budget across the narrative fields that are
// currently non-empty, proportionally to their current size, after first
// reserving compactionPerFieldFloor for each. The last non-empty field (in
// dossierNarrativeFields order) absorbs the rounding remainder, so the
// allocated sum is exactly budget whenever perFieldFloorsFit(fieldSize,
// budget) is true. When it is false, every non-empty field still gets the
// bare floor for diagnostic purposes — the caller must never dispatch on
// this allocation (Feasible is false), so what is reported here is
// informational only, not a budget that fits.
func allocatePerField(fieldSize map[string]int, budget int) map[string]int {
	out := make(map[string]int, len(dossierNarrativeFields))
	var nonEmpty []string
	total := 0
	for _, f := range dossierNarrativeFields {
		if fieldSize[f] > 0 {
			nonEmpty = append(nonEmpty, f)
			total += fieldSize[f]
		} else {
			out[f] = 0
		}
	}
	if len(nonEmpty) == 0 {
		return out
	}
	remaining := budget - compactionPerFieldFloor*len(nonEmpty)
	if remaining < 0 {
		for _, f := range nonEmpty {
			out[f] = compactionPerFieldFloor
		}
		return out
	}
	allocated := 0
	for i, f := range nonEmpty {
		share := compactionPerFieldFloor
		if i == len(nonEmpty)-1 {
			share += remaining - allocated
		} else {
			extra := remaining * fieldSize[f] / total
			share += extra
			allocated += extra
		}
		out[f] = share
	}
	return out
}

// perFieldLines renders the measured allowance for the compaction prompt,
// one line per narrative field in dossierNarrativeFields order, naming both
// the byte budget the model must fit into and the field's current size.
// This replaces a single fixed "aim for under 400 characters each" that was,
// across the six responses that needed it, never the right number for any
// of them (task-9-context.md).
func perFieldLines(perField, current map[string]int) string {
	var sb strings.Builder
	for _, f := range dossierNarrativeFields {
		fmt.Fprintf(&sb, "%s: %d bytes (currently %d)\n", f, perField[f], current[f])
	}
	return strings.TrimRight(sb.String(), "\n")
}

// compactDossier asks the model to shorten a dossier's narrative fields
// alone to fit inside limit, using measureCompaction's per-field allowance
// rather than a fixed guess. It refuses to dispatch a doomed call: when the
// protected content alone leaves no feasible narrative budget, no rewrite of
// the twelve narrative fields can help, and the failure is recorded without
// spending the one compaction attempt on a call that cannot succeed.
func compactDossier(ctx context.Context, t *thesisRunner, role, name, raw string, limit int, out *model.CandidateDossier) (model.DomainStatus, error) {
	allowance, err := measureCompaction(raw, limit)
	if err != nil {
		return model.DomainStatus{Domain: name + "-compaction", Recovery: "compaction", Status: model.StatusFailed, FailureKind: "input_preparation", Contract: model.OutcomeFailed, Payload: model.OutcomeNotAttempted, Err: fmt.Sprintf("compaction allowance could not be measured: %v", err)}, err
	}
	if !allowance.Feasible {
		// Two distinct shapes reach here (see perFieldFloorsFit): a
		// narrative_budget that is itself negative (the protected content
		// alone already leaves no room under the limit), and a
		// narrative_budget that is POSITIVE but too small for the per-field
		// floors to fit — where "already leaves no narrative_budget" would
		// be a false statement next to a positive number. State the actual
		// arithmetic instead, which is true in both cases.
		floors := 0
		for _, v := range allowance.PerField {
			if v > 0 {
				floors += v
			}
		}
		err := fmt.Errorf("compaction is not feasible: a %d-byte narrative_budget (limit %d minus protected_bytes %d minus headroom %d) cannot cover the %d-byte floor required across its non-empty narrative fields", allowance.NarrativeBudget, limit, allowance.ProtectedBytes, compactionHeadroom, floors)
		return model.DomainStatus{Domain: name + "-compaction", Recovery: "compaction", Status: model.StatusFailed, FailureKind: "input_capacity", Contract: model.OutcomeFailed, Payload: model.OutcomeNotAttempted, Allowance: &allowance, Err: err.Error()}, err
	}

	var original model.CandidateDossier
	if err = decodeResearch(raw, &original); err != nil {
		return model.DomainStatus{Domain: name + "-compaction", Recovery: "compaction", Status: model.StatusFailed, FailureKind: "input_preparation", Contract: model.OutcomeFailed, Payload: model.OutcomeNotAttempted, Allowance: &allowance, Err: err.Error()}, err
	}
	originalNarratives := dossierNarratives(original)
	current := make(map[string]int, len(dossierNarrativeFields))
	for _, f := range dossierNarrativeFields {
		current[f] = len(originalNarratives[f])
	}

	prompt := fmt.Sprintf(
		"Compact this complete dossier to fit a %d-byte response budget. "+
			"This consumes the one repair allowance; there is no further repair. "+
			"Shorten only these narrative fields, to at most the UTF-8 byte budget "+
			"given for each — everything else in the payload is already accounted "+
			"for and must not change:\n%s\n"+
			"Preserve every material qualification and counterargument; a shorter "+
			"field that drops a caveat is a failure, not a success. Do not add "+
			"findings or upgrade the verdict. Every other field, including unknown "+
			"fields, claims, exact quotations, evidence IDs, numerical values, "+
			"requests, unresolved questions, conditions, events and status must "+
			"remain byte-identical. Return one complete fenced JSON object with no "+
			"surrounding prose.\nOriginal response:\n%s",
		allowance.Limit, perFieldLines(allowance.PerField, current), raw)

	r, err := t.call(ctx, role, name+"-compaction", prompt, t.cheapTarget())
	s := reportStatus(r)
	s.Recovery, s.Contract, s.Payload = "compaction", model.OutcomeFailed, "invalid"
	s.Allowance = &allowance
	if err != nil {
		if r.Attempts == 0 && r.FailureKind == "input_capacity" {
			s.Payload = model.OutcomeNotAttempted
		}
		return s, err
	}
	var next model.CandidateDossier
	if err = decodeThesis(r.Stdout, &next, currentResearchSchema(dossierSchema)); err == nil {
		err = compactionPreservesEvidence(raw, r.Stdout)
	}
	if err != nil {
		s.Err = err.Error()
		return s, err
	}
	s.OriginalNarratives = originalNarratives
	s.WritingDiagnostics = dossierWritingDiagnostics(&next)
	s.Contract, s.Payload = "compacted", model.OutcomeOK
	*out = next
	return s, nil
}

// compactResults is a second, unrelated kind of compaction from the rest of
// this file: deterministic, Go-only, and spending no model call — it shapes
// the request_results PROMPT SECTION, never out.Results itself. out.Results
// is the durable ledger (thesis.go's investigate persists it whole via
// run.WriteDataPack(safeName, out)); compactResults produces a second,
// smaller view of it for the model to read.
//
// A request answered "fulfilled" keeps its full record — what it added
// (EvidenceIDs) is exactly what the model has to act on. Every other outcome
// (unsupported, unavailable, already attempted, budget exhausted) exists
// only to stop the model asking again, and once recorded that fact does not
// change: the Detail sentence that explains it ("No such operation:
// web_search", "That URL is not in your evidence") was worth saying once, in
// the round it happened, and worth nothing as an unchanging repeat carried
// into every later round. On the three captured SNOW/OKTA/ORCL overflows,
// request_results ran 1,725 to 5,114 bytes of exactly this: a handful of
// distinct requests, each explained at full length, accumulating round over
// round. The compacted view keeps enough of the original request to
// identify it — kind, and whichever of url/evidence_id/observation_date it
// carried — so the model can recognize its own repeated question without
// reading why it failed a second time.
func compactResults(results []model.ResearchResult) []model.ResearchResult {
	out := make([]model.ResearchResult, len(results))
	for i, r := range results {
		if r.Outcome == model.RequestFulfilled {
			out[i] = r
			continue
		}
		out[i] = model.ResearchResult{
			Request: model.ResearchRequest{Kind: r.Request.Kind, URL: r.Request.URL, EvidenceID: r.Request.EvidenceID, ObservationDate: r.Request.ObservationDate},
			Outcome: r.Outcome,
		}
	}
	return out
}

func compactionOriginals(reports []model.DomainStatus) map[string]map[string]string {
	var originals map[string]map[string]string
	for _, report := range reports {
		if len(report.OriginalNarratives) > 0 {
			if originals == nil {
				originals = map[string]map[string]string{}
			}
			originals[report.Domain] = report.OriginalNarratives
		}
	}
	return originals
}

func (r *thesisResearch) addResearchReports(reports []model.DomainStatus) {
	r.Reports = append(r.Reports, reports...)
	for _, report := range reports {
		if report.Recovery != "" && report.Attempts > 0 {
			r.Outcome.RepairAttempts++
		}
		if report.Recovery == "compaction" && report.Attempts > 0 {
			r.Outcome.CompactionAttempts++
		}
		if report.Contract == model.OutcomeFailed {
			r.Outcome.Contract = model.OutcomeFailed
		}
		if r.Outcome.Contract != model.OutcomeFailed {
			if report.Contract == "compacted" {
				r.Outcome.Contract = "compacted"
			} else if r.Outcome.Contract == "" {
				r.Outcome.Contract = report.Contract
			}
		}
	}
}
