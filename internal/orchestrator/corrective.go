package orchestrator

import (
	"fmt"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/parse"
)

// correctivePrompt builds the one corrective re-prompt the Chief Analyst gets.
//
// It used to be `prompt + "CRITICAL: Your previous output failed validation. " +
// reasons`, with the previous output itself left out. That is not a correction —
// it is a second, independent synthesis with a list of complaints attached, and
// the 2026-09-01 run shows exactly what that costs: the second pass reordered the
// book (BBVA.MC 2nd→4th, STLAM.MI 4th→2nd), rewrote every entry, stop and target,
// and dropped a −3 adjustment on STLAM.MI that no finding had objected to, taking
// its confidence from 35 back to 38 with the reasoning silently gone.
//
// Handing back the model's own JSON turns the call into what its name claims: fix
// these, leave the rest.
func correctivePrompt(prompt, previous string, reasons []string) string {
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\n---\n\n## Corrective pass\n\n")
	b.WriteString("Your previous answer to this task is below, followed by what the app's ")
	b.WriteString("deterministic checks found wrong with it.\n\n")

	if prior, ok := parse.LastJSONBlock(previous); ok {
		b.WriteString("### Your previous output\n\n```json\n")
		b.WriteString(strings.TrimSpace(prior))
		b.WriteString("\n```\n\n")
	}

	b.WriteString("### What failed\n\n")
	for _, r := range reasons {
		b.WriteString("- ")
		b.WriteString(r)
		b.WriteString("\n")
	}

	b.WriteString("\n### What to do\n\n")
	b.WriteString("Re-emit the **full** JSON block, changing only what the findings above name. ")
	b.WriteString("An idea no finding mentions must come back with its ranking, confidence, ")
	b.WriteString("levels, timeframe and reasoning unchanged — a corrective pass is not a ")
	b.WriteString("second opinion, and re-deriving the ideas that were already sound loses ")
	b.WriteString("the judgment you spent the first pass on. Where a finding forces a level ")
	b.WriteString("to move, keep the same thesis and say in `notes` what you changed and why. ")
	b.WriteString("Your `notes` must not contradict a fact this run collected: if a finding ")
	b.WriteString("says a date is unverified, drop the claim — do not assert that the run ")
	b.WriteString("holds no such data unless the findings say so.\n\n")
	// Every finding the risk gate writes uses a substitution verb — "swap the
	// weaker one for something that adds breadth", "replace the weakest with a
	// different one" — and the beta finding names no remedy at all. Read beside
	// "changing only what the findings above name", that told the Chief the book
	// had to stay five names long. On 2026-09-03 it dropped ORCL (base 38, five
	// domains) for 035720.KS (base 27, quant only) to satisfy an average-beta
	// ceiling, and said so: "the slot had to be filled by something low-beta
	// rather than left empty."
	//
	// The persona already permits a shorter book. This is the prompt that
	// forbade it.
	b.WriteString("**Removing an idea is always an available answer.** Where a finding asks ")
	b.WriteString("you to swap or replace something, deleting it and shipping fewer ideas ")
	b.WriteString("satisfies the finding too, and is the better answer whenever the ")
	b.WriteString("replacement would be weaker than what it replaces — a name carried by one ")
	b.WriteString("domain, or one you would not have ranked at all. Do not fill a slot to ")
	b.WriteString("keep the count. Re-rank the survivors 1..N contiguously and say in ")
	b.WriteString("`notes` what you dropped and why.\n")
	return b.String()
}

// correctedReport is what lands in chief-analyst.md after a corrective pass:
// the answer that shipped, then the answer that did not, with the findings that
// separated them.
//
// Both are kept deliberately. The rejected pass carries the reasoning behind
// every idea that survived unchanged, and deleting it would leave the artifact
// thinner than the run actually was.
func correctedReport(first, corrected string, reasons []string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(corrected))
	b.WriteString("\n\n---\n\n# Superseded first pass\n\n")
	b.WriteString("The synthesis above is a corrective re-emission. The app's checks rejected ")
	b.WriteString("the first pass for:\n\n")
	for _, r := range reasons {
		fmt.Fprintf(&b, "- %s\n", r)
	}
	b.WriteString("\nThe rejected pass follows in full. Where an idea survived unchanged, its ")
	b.WriteString("reasoning is here rather than above.\n\n")
	b.WriteString(strings.TrimSpace(first))
	b.WriteString("\n")
	return b.String()
}
