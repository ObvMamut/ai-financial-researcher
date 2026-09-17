package orchestrator

import (
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

func compactDossier(ctx context.Context, t *thesisRunner, role, name, raw string, out *model.CandidateDossier) (model.DomainStatus, error) {
	prompt := "Compact this complete dossier to fit the response byte budget. This consumes the one repair allowance; there is no further repair. Shorten only these narrative fields: " + strings.Join(dossierNarrativeFields, ", ") + ". Aim for under 400 characters each. Preserve all qualifications and counterarguments. Every other field, including unknown fields, claims, exact quotations, evidence IDs, numerical values, requests, unresolved questions, conditions, events and status must remain unchanged. Do not add findings or upgrade the verdict. Return one complete fenced JSON object.\nOriginal response:\n" + raw
	r, err := t.call(ctx, role, name+"-compaction", prompt, t.cheap)
	s := reportStatus(r)
	s.Recovery, s.Contract, s.Payload = "compaction", model.OutcomeFailed, "invalid"
	if err != nil {
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
	var original model.CandidateDossier
	if err = decodeResearch(raw, &original); err != nil {
		return s, err
	}
	s.OriginalNarratives = dossierNarratives(original)
	s.WritingDiagnostics = dossierWritingDiagnostics(&next)
	s.Contract, s.Payload = "compacted", model.OutcomeOK
	*out = next
	return s, nil
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
