// Package parse extracts machine-readable payloads from agent markdown output.
// Shared by the orchestrator (ideas, scout results) and the TUI (specialist
// score tails for the detail view) without creating an import cycle.
package parse

import (
	"regexp"
	"strings"
)

// jsonFenceRE matches fenced ```json blocks containing a JSON object.
var jsonFenceRE = regexp.MustCompile("(?s)```json\\s*(\\{.*?\\})\\s*```")

// LastJSONBlock pulls the last fenced ```json ... ``` object from text.
// Agents put their structured tail last, after human-readable prose.
func LastJSONBlock(text string) (string, bool) {
	matches := jsonFenceRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return "", false
	}
	return matches[len(matches)-1][1], true
}

// ReplaceLastJSONBlock swaps the object inside the last fenced ```json block for
// replacement, leaving the surrounding prose — and any earlier blocks — as they
// were. The orchestrator uses it to correct a specialist's structured tail in
// place, so the report artifact on disk is exactly what the Chief Analyst read.
//
// It reports false when there is no block to replace.
func ReplaceLastJSONBlock(text, replacement string) (string, bool) {
	spans := jsonFenceRE.FindAllStringSubmatchIndex(text, -1)
	if len(spans) == 0 {
		return text, false
	}
	last := spans[len(spans)-1]
	// last[2]:last[3] is the captured object; splice around it so the fence
	// markers and whitespace the agent wrote are preserved verbatim.
	var b strings.Builder
	b.WriteString(text[:last[2]])
	b.WriteString(replacement)
	b.WriteString(text[last[3]:])
	return b.String(), true
}
