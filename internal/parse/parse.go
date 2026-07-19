// Package parse extracts machine-readable payloads from agent markdown output.
// Shared by the orchestrator (ideas, scout results) and the TUI (specialist
// score tails for the detail view) without creating an import cycle.
package parse

import "regexp"

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
