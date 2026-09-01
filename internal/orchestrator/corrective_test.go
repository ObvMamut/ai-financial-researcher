package orchestrator

import (
	"strings"
	"testing"
)

const priorSynthesis = `# Chief Analyst Synthesis

### 1. AMGN — Amgen Inc. (BUY)

**Confluence Math:** ` + "`base 39 = 39`" + `

` + "```json" + `
{
  "mode": "independent",
  "ideas": [
    {"rank": 1, "ticker": "AMGN", "direction": "BUY", "confidence": 39,
     "entry": 429.0, "stop": 395.0, "target": 495.0}
  ]
}
` + "```" + `
`

// TestCorrectivePromptCarriesTheAnswerItIsCorrecting is the 2026-09-01
// regression: the re-prompt named the faults but never showed the model its own
// output, so the "correction" was a second independent synthesis. That run came
// back with the book reordered, every level rewritten, and a justified −3
// adjustment silently dropped from an idea nothing had complained about.
func TestCorrectivePromptCarriesTheAnswerItIsCorrecting(t *testing.T) {
	reasons := []string{"AAA: reward:risk is 1.20, below the hard floor of 1.80"}
	got := correctivePrompt("ORIGINAL TASK", priorSynthesis, reasons)

	if !strings.Contains(got, "ORIGINAL TASK") {
		t.Error("the original task is gone; the model would be correcting in a vacuum")
	}
	if !strings.Contains(got, `"ticker": "AMGN"`) {
		t.Error("the previous output is absent — this is a re-synthesis, not a correction")
	}
	if !strings.Contains(got, reasons[0]) {
		t.Errorf("the finding is missing:\n%s", got)
	}
	// The instruction that stops the model rewriting what was already sound.
	for _, want := range []string{"changing only what the findings above name", "unchanged"} {
		if !strings.Contains(got, want) {
			t.Errorf("re-prompt does not tell the model to leave sound ideas alone (missing %q)", want)
		}
	}
	// And the one that stops it narrating a fact the run does not support — how
	// the shipped notes came to claim the run held no earnings dates when it did.
	if !strings.Contains(got, "must not contradict a fact this run collected") {
		t.Error("re-prompt does not constrain what the model may assert about the run's own data")
	}
}

func TestCorrectivePromptSurvivesAnUnparseablePriorAnswer(t *testing.T) {
	// A first pass that produced no JSON block at all still has to yield a usable
	// re-prompt: the findings are what matter, the prior answer is a courtesy.
	got := correctivePrompt("TASK", "the model refused and wrote prose", []string{"BBB: no usable levels"})
	if !strings.Contains(got, "BBB: no usable levels") {
		t.Error("findings lost when the prior answer had no JSON")
	}
	if strings.Contains(got, "### Your previous output") {
		t.Error("an empty previous-output section was rendered")
	}
}

// TestCorrectedReportKeepsBothPasses guards the artifact. Only the first
// response used to be written, so chief-analyst.md documented ranks, levels and
// confidences that contradicted the ideas.json sitting beside it.
func TestCorrectedReportKeepsBothPasses(t *testing.T) {
	corrected := "# Corrected\n\n```json\n{\"ideas\": []}\n```"
	reasons := []string{"AAA: reward:risk is 1.20, below the hard floor of 1.80"}
	got := correctedReport(priorSynthesis, corrected, reasons)

	if !strings.HasPrefix(got, "# Corrected") {
		t.Error("the shipped synthesis must lead the report — it is what ideas.json was parsed from")
	}
	if !strings.Contains(got, "Superseded first pass") {
		t.Error("the rejected pass is unlabelled")
	}
	if !strings.Contains(got, "base 39 = 39") {
		t.Error("the rejected pass's reasoning is gone, and with it the case for every idea that survived unchanged")
	}
	if !strings.Contains(got, reasons[0]) {
		t.Error("the report does not say why the first pass was rejected")
	}
	if strings.Index(got, "# Corrected") > strings.Index(got, "Superseded") {
		t.Error("the superseded pass is ordered above the shipped one")
	}
}
