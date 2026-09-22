package orchestrator

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

func dossierHash(d model.CandidateDossier) string {
	b, _ := json.Marshal(d)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// wrapPrompt puts one already-assembled data block inside its role's persona
// and contract lines. It is split out of preparePrompt because the Chief has
// to know how many bytes that wrapper costs *before* it can decide how large
// a board fits inside the remainder (promptWrapperBytes below); measuring the
// wrapper any other way would be a reconstruction of this function rather
// than this function.
func (t *thesisRunner) wrapPrompt(role, data string) (string, error) {
	prompt, err := t.reg.AssemblePrompt(agents.PromptParams{Role: role, Mode: t.cfg.Mode, RunTS: t.run.TS, DataBlock: data})
	if err != nil {
		return "", err
	}
	prompt += fmt.Sprintf("\nResearch contract: use only supplied evidence; no independent tools. Maximum response %d UTF-8 bytes.\n", t.cfg.Research.Budgets.ForRole(role).ResponseBytes)
	if strings.HasPrefix(role, "thesis-") {
		prompt += "Return requests to Go. Return only one fenced JSON object, with no additional essay.\n"
	}
	return redact.String(prompt), nil
}

// promptWrapperBytes is what wrapPrompt costs for one role, independently of
// what it wraps: a one-byte probe measures it exactly, because the wrapper is
// literal concatenation either side of the data block. The probe is non-empty
// on purpose — AssemblePrompt writes a thesis role's data block with a
// newline either side only when there is a data block, and the Chief always
// has one.
func (t *thesisRunner) promptWrapperBytes(role string) (int, error) {
	const probe = "\x01"
	prompt, err := t.wrapPrompt(role, probe)
	if err != nil {
		return 0, err
	}
	return len(prompt) - len(probe), nil
}

func (t *thesisRunner) preparePrompt(role, name string, sections []promptSection, outputTokens int) (string, *model.PromptProfile, error) {
	budget := t.cfg.Research.Budgets.ForRole(role)
	// Redact each section before assembly, not the joined string afterward:
	// assembleSections' sizes must match the bytes that actually reach the
	// prompt, and a credential redacted post-assembly (e.g. one echoed back
	// inside a retrieval-diagnostics section) would otherwise change a
	// section's length after it was already measured.
	redacted := make([]promptSection, len(sections))
	for i, sec := range sections {
		sec.Body = redact.String(sec.Body)
		redacted[i] = sec
	}
	wrapper, err := t.promptWrapperBytes(role)
	if err != nil {
		return "", nil, err
	}
	data, sizes, omitted, capacityErr := assembleSections(redacted, budget.InputBytes-wrapper)
	if capacityErr != nil {
		if promptFailureKind(capacityErr) != "input_capacity" {
			return "", nil, capacityErr
		}
		// Persist the complete refused input for audit. Nothing was dispatched
		// or actually omitted from this artifact.
		data, sizes, omitted, err = assembleSections(redacted, noSectionLimit)
		if err != nil {
			return "", nil, err
		}
	}
	prompt, err := t.wrapPrompt(role, data)
	if err != nil {
		return "", nil, err
	}
	// Components is a flat partition of len(prompt): "instructions" (the
	// persona wrapper and contract line above, i.e. prompt minus data) plus
	// each section's own exact size, populated directly from sizes rather
	// than re-derived by scanning data for marker substrings. There is
	// deliberately no separate "data" aggregate alongside its own children —
	// that overlap was the historical Components' own defect (a Chief
	// profile could record "data" 150,876 alongside company_board 142,756
	// plus macro/quant/risk_policy, summing to more than the prompt itself).
	// sum(Components) now equals len(prompt) exactly, always.
	profile := &model.PromptProfile{VisibleEvidence: visibleEvidence(data), Version: 2, Bytes: len(prompt), EstimatedTokens: (len(prompt) + 2) / 3, EstimateMethod: "ceil(UTF-8 bytes/3); heuristic, not provider usage or a context guarantee", InputLimit: budget.InputBytes, ResponseLimit: budget.ResponseBytes, OutputTokenLimit: outputTokens, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(prompt))), Components: map[string]int{"instructions": len(prompt) - len(data)}, Omitted: omitted}
	for key, size := range sizes {
		profile.Components[key] = size
	}
	if strings.Contains(data, `"omitted_text":true`) {
		profile.Compactions = append(profile.Compactions, "selected_source_passages")
	}
	if role == "thesis-chief" {
		profile.Compactions = append(profile.Compactions, "chief_context_projection")
	}

	if err = t.run.WriteDataPack("input-"+name, struct {
		Role    string               `json:"role"`
		Data    string               `json:"data"`
		Prompt  string               `json:"prompt"`
		Profile *model.PromptProfile `json:"profile"`
	}{role, data, prompt, profile}); err != nil {
		return "", profile, err
	}
	if capacityErr != nil {
		return "", profile, capacityErr
	}
	if strings.Contains(data, `"omitted_claim_ids":[`) {
		return "", profile, promptCapacityError{fmt.Errorf("input capacity exceeded: required source passages do not fit the evidence budget"), []string{"evidence"}}
	}
	if len(prompt) > budget.InputBytes {
		var mandatory []string
		for _, sec := range redacted {
			if sec.Mandatory {
				mandatory = append(mandatory, sec.Name)
			}
		}
		return "", profile, promptCapacityError{fmt.Errorf("input capacity exceeded for %s: %d > %d bytes; required context cannot be omitted", role, len(prompt), budget.InputBytes), mandatory}
	}
	return prompt, profile, nil
}

// responseCapacity gates a response on the bytes its budget actually bounds:
// the normalized fenced JSON payload, not the raw response text. Formatting
// whitespace an agent's own writing style adds — indentation, wrapped
// narrative lines — inflated RawBytes without spending any of the budget the
// response is supposed to be measured against; a compaction result that
// already fit under the limit was being thrown away for exactly that
// whitespace (see ResponseContractVersion 2's fixture, "LLY" in
// testdata/sep15-capacity/manifest.json). Only PayloadBytes gates now;
// RawBytes is still recorded and still appears in the failure message, for
// audit, but it no longer decides the outcome on its own.
func responseCapacity(r *model.Report, profile *model.PromptProfile) {
	r.Prompt = profile
	if profile == nil || r.Status != model.StatusDone {
		return
	}
	m := measureResponse(r.Stdout)
	profile.Response = &m
	profile.ResponseContractVersion = 2
	if m.PayloadBytes > profile.ResponseLimit {
		r.Status = model.StatusFailed
		r.FailureKind = "response_capacity"
		r.Err = fmt.Sprintf(
			"response capacity exceeded: payload %d > %d bytes (raw %d); complete response retained for audit",
			m.PayloadBytes, profile.ResponseLimit, m.RawBytes)
	}
}

// Inspect structured blocks of the exact supplied data, not full artifacts.
func visibleEvidence(data string) map[string][]string {
	out := map[string][]string{}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case []any:
			for _, x := range v {
				walk(x)
			}
		case map[string]any:
			if v["kind"] == "document" {
				id, _ := v["id"].(string)
				ticker, _ := v["ticker"].(string)
				text, _ := v["text"].(string)
				spans, _ := v["selected_spans"].([]any)
				if id != "" && ticker != "" && text != "" && len(spans) > 0 {
					out[ticker] = appendUnique(out[ticker], id)
				}
			}
			for _, x := range v {
				walk(x)
			}
		}
	}
	for len(data) > 0 {
		at := strings.IndexAny(data, "{[")
		if at < 0 {
			break
		}
		data = data[at:]
		dec := json.NewDecoder(strings.NewReader(data))
		var value any
		if dec.Decode(&value) == nil {
			walk(value)
			data = data[dec.InputOffset():]
		} else {
			data = data[1:]
		}
	}
	return out
}

// Quotations already occur in selected source text. Keep identity/attribution
// references in selection dossiers without serializing the same text twice.
func claimReferences(claims []model.ResearchClaim) []model.ResearchClaim {
	out := append([]model.ResearchClaim(nil), claims...)
	for i := range out {
		out[i].Passages = append([]model.ClaimPassage(nil), out[i].Passages...)
		for j := range out[i].Passages {
			out[i].Passages[j].Quote = ""
		}
	}
	return out
}

func executionPlanHash(idea model.TradeIdea) string {
	b, _ := json.Marshal(idea)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// promptCapacityError distinguishes a capacity refusal (this prompt was never
// dispatched) from any other prompt-preparation failure. omitted names the
// mandatory requirements implicated — see DomainStatus.Omitted's doc comment
// for why that differs from the optional sections omitted from a fitted prompt.
type promptCapacityError struct {
	error
	omitted []string
}

func promptFailureKind(err error) string {
	var capacity promptCapacityError
	if errors.As(err, &capacity) {
		return "input_capacity"
	}
	return "input_preparation"
}

// promptOmitted extracts the mandatory requirements a capacity refusal named,
// or nil for any other error (including no error at all).
func promptOmitted(err error) []string {
	var capacity promptCapacityError
	if errors.As(err, &capacity) {
		return capacity.omitted
	}
	return nil
}
