package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/parse"
)

// enforcement records what the app had to correct in one specialist's
// structured tail before handing the report to the Chief Analyst.
type enforcement struct {
	// Corrected lists shortlisted tickers the agent scored without verified
	// data. Their scores were deleted and the names moved to `missing`.
	Corrected []string
	// OffShortlist lists tickers the agent scored that were never requested —
	// hallucinated symbols. Their scores were deleted and nothing was added to
	// `missing`, because the run never asked about them.
	OffShortlist []string
}

// enforceSpecialistTail rewrites a specialist report so its structured tail
// matches the data the run actually assembled.
//
// Coverage used to be advisory: `overclaimedCoverage` logged a warning when an
// agent scored a name it had no data for, and the invented score flowed into
// synthesis regardless. On 2026-08-28 every specialist reported `"missing": []`
// against near-zero real coverage, and the run shipped as `outcome: complete`
// with the highest confidence of any recent run — the pipeline was rewarding
// confabulation. Computed coverage is the authority now: a score for a ticker
// the domain could not see is deleted, and the ticker is unioned into `missing`.
//
// The convention this establishes, and that the personas and output-schema doc
// state: a ticker appears in `missing` if and only if the domain had no data for
// it, and in `scores` if and only if it was evaluated on data. Nothing is scored
// at strength 0 or 1 to mean "no data", and nothing is silently omitted.
//
// The report is rewritten in place — same prose, same fences — so the artifact
// on disk is exactly what the Chief Analyst read.
//
// An unparseable or absent tail returns an error: a refusal, a truncated
// response, or free prose is not a usable domain report, and treating non-empty
// stdout as success is how those reached synthesis unnoticed.
func enforceSpecialistTail(role, stdout string, ungrounded, shortlist []string) (string, enforcement, error) {
	var res enforcement

	raw, ok := parse.LastJSONBlock(stdout)
	if !ok {
		return stdout, res, fmt.Errorf("no structured JSON tail in %s report", role)
	}

	// Decode into an ordered generic form: the tail carries fields this app does
	// not model (per-domain extras the personas ask for), and re-marshalling
	// through a typed struct would silently drop them.
	var tail map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &tail); err != nil {
		return stdout, res, fmt.Errorf("unparseable JSON tail in %s report: %w", role, err)
	}

	var scores []map[string]json.RawMessage
	if rawScores, ok := tail["scores"]; ok && len(rawScores) > 0 {
		if err := json.Unmarshal(rawScores, &scores); err != nil {
			return stdout, res, fmt.Errorf("unparseable `scores` array in %s report: %w", role, err)
		}
	}

	onShortlist := make(map[string]bool, len(shortlist))
	for _, t := range shortlist {
		onShortlist[normTicker(t)] = true
	}
	isUngrounded := make(map[string]bool, len(ungrounded))
	for _, t := range ungrounded {
		isUngrounded[normTicker(t)] = true
	}

	kept := make([]map[string]json.RawMessage, 0, len(scores))
	corrected := map[string]bool{}
	offShortlist := map[string]bool{}
	for _, s := range scores {
		t := normTicker(jsonString(s["ticker"]))
		switch {
		case t == "":
			// A score with no ticker names nothing; it cannot be attributed.
			continue
		case !onShortlist[t]:
			offShortlist[t] = true
		case isUngrounded[t]:
			corrected[t] = true
		default:
			kept = append(kept, s)
		}
	}
	res.Corrected = sortedKeys(corrected)
	res.OffShortlist = sortedKeys(offShortlist)

	// `missing` is the union of what the agent declared and what it wrongly
	// scored, normalized and deduped. Off-shortlist names are excluded: the run
	// never asked about them, so they are not gaps in this domain's coverage.
	declared := map[string]bool{}
	if rawMissing, ok := tail["missing"]; ok && len(rawMissing) > 0 {
		var missing []string
		if err := json.Unmarshal(rawMissing, &missing); err != nil {
			return stdout, res, fmt.Errorf("unparseable `missing` array in %s report: %w", role, err)
		}
		for _, m := range missing {
			if n := normTicker(m); n != "" {
				declared[n] = true
			}
		}
	}
	for t := range corrected {
		declared[t] = true
	}
	missing := sortedKeys(declared)

	// Nothing to correct: hand the report back byte-identical.
	if len(res.Corrected) == 0 && len(res.OffShortlist) == 0 && sameStringSet(missing, tail["missing"]) {
		return stdout, res, nil
	}

	scoresJSON, err := json.Marshal(kept)
	if err != nil {
		return stdout, res, fmt.Errorf("re-marshal %s scores: %w", role, err)
	}
	missingJSON, err := json.Marshal(missing)
	if err != nil {
		return stdout, res, fmt.Errorf("re-marshal %s missing: %w", role, err)
	}
	tail["scores"] = scoresJSON
	tail["missing"] = missingJSON

	rewritten, err := json.MarshalIndent(tail, "", "  ")
	if err != nil {
		return stdout, res, fmt.Errorf("re-marshal %s tail: %w", role, err)
	}
	out, ok := parse.ReplaceLastJSONBlock(stdout, string(rewritten))
	if !ok {
		return stdout, res, fmt.Errorf("could not splice the corrected tail into the %s report", role)
	}
	return out, res, nil
}

// normTicker upper-cases and trims a symbol for comparison.
func normTicker(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// jsonString decodes a JSON string field, returning "" for anything else.
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sameStringSet reports whether raw already decodes to exactly want, so an
// already-honest report can be returned untouched.
func sameStringSet(want []string, raw json.RawMessage) bool {
	var have []string
	if len(raw) > 0 && json.Unmarshal(raw, &have) != nil {
		return false
	}
	if len(have) != len(want) {
		return false
	}
	sort.Strings(have)
	for i := range want {
		if have[i] != want[i] {
			return false
		}
	}
	return true
}
