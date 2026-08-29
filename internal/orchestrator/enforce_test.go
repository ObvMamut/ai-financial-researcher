package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/parse"
)

// report builds a specialist report with the given structured tail.
func report(prose, tail string) string {
	return prose + "\n\n```json\n" + tail + "\n```\n"
}

func tailOf(t *testing.T, out string) struct {
	Domain string `json:"domain"`
	Scores []struct {
		Ticker   string `json:"ticker"`
		Strength int    `json:"strength"`
	} `json:"scores"`
	Missing []string `json:"missing"`
} {
	t.Helper()
	var res struct {
		Domain string `json:"domain"`
		Scores []struct {
			Ticker   string `json:"ticker"`
			Strength int    `json:"strength"`
		} `json:"scores"`
		Missing []string `json:"missing"`
	}
	raw, ok := parse.LastJSONBlock(out)
	if !ok {
		t.Fatalf("no JSON tail in corrected report:\n%s", out)
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("corrected tail is not valid JSON: %v\n%s", err, raw)
	}
	return res
}

// The 2026-08-28 failure mode: a specialist scores names it had no data for and
// reports `"missing": []`. The app must delete those scores and tell the truth.
func TestEnforceStripsUngroundedScores(t *testing.T) {
	in := report("Deep analysis of all four names.", `{"domain":"sentiment","scores":[
	  {"ticker":"AAPL","bias":"bullish","strength":6,"note":"covered"},
	  {"ticker":"MSFT","bias":"bullish","strength":8,"note":"invented"}
	],"missing":[]}`)

	out, res, err := enforceSpecialistTail("sentiment", in, []string{"MSFT"}, []string{"AAPL", "MSFT"})
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	if want := []string{"MSFT"}; !equalStrings(res.Corrected, want) {
		t.Errorf("Corrected = %v, want %v", res.Corrected, want)
	}

	got := tailOf(t, out)
	if len(got.Scores) != 1 || got.Scores[0].Ticker != "AAPL" {
		t.Errorf("scores = %+v, want AAPL only", got.Scores)
	}
	if !equalStrings(got.Missing, []string{"MSFT"}) {
		t.Errorf("missing = %v, want [MSFT]", got.Missing)
	}
	if !strings.Contains(out, "Deep analysis of all four names.") {
		t.Error("prose was lost during correction")
	}
}

// A ticker that is not on the shortlist at all is a hallucination, whether or
// not the domain had data for it.
func TestEnforceDropsOffShortlistTickers(t *testing.T) {
	in := report("prose", `{"domain":"news","scores":[
	  {"ticker":"AAPL","bias":"bullish","strength":6,"note":"ok"},
	  {"ticker":"TSLA","bias":"bearish","strength":7,"note":"never asked for"}
	],"missing":[]}`)

	out, res, err := enforceSpecialistTail("news", in, nil, []string{"AAPL"})
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	if !equalStrings(res.OffShortlist, []string{"TSLA"}) {
		t.Errorf("OffShortlist = %v, want [TSLA]", res.OffShortlist)
	}
	got := tailOf(t, out)
	if len(got.Scores) != 1 || got.Scores[0].Ticker != "AAPL" {
		t.Errorf("scores = %+v, want AAPL only", got.Scores)
	}
	// An invented ticker is not "missing data for a name we asked about"; it
	// was never on the shortlist, so it must not pollute the missing array.
	if len(got.Missing) != 0 {
		t.Errorf("missing = %v, want empty — TSLA was never requested", got.Missing)
	}
}

// An honest report must come through byte-identical: enforcement is a
// correction mechanism, not a rewriter.
func TestEnforceLeavesHonestReportUntouched(t *testing.T) {
	in := report("prose", `{"domain":"news","scores":[{"ticker":"AAPL","bias":"bullish","strength":6,"note":"ok"}],"missing":["MSFT"]}`)

	out, res, err := enforceSpecialistTail("news", in, []string{"MSFT"}, []string{"AAPL", "MSFT"})
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	if len(res.Corrected) != 0 || len(res.OffShortlist) != 0 {
		t.Errorf("honest report reported corrections: %+v", res)
	}
	if out != in {
		t.Errorf("honest report was rewritten:\ngot  %q\nwant %q", out, in)
	}
}

// An ungrounded ticker the agent already declared missing must not be listed
// twice after the union.
func TestEnforceDoesNotDuplicateMissing(t *testing.T) {
	in := report("prose", `{"domain":"news","scores":[{"ticker":"AAPL","strength":6}],"missing":["msft"," NVDA "]}`)

	out, _, err := enforceSpecialistTail("news", in, []string{"MSFT", "NVDA"}, []string{"AAPL", "MSFT", "NVDA"})
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	got := tailOf(t, out)
	if !equalStrings(got.Missing, []string{"MSFT", "NVDA"}) {
		t.Errorf("missing = %v, want [MSFT NVDA] normalized and deduped", got.Missing)
	}
}

// No parseable tail means the report is unusable — a refusal or a truncation.
// The run must not treat it as a successful domain.
func TestEnforceRejectsReportWithNoTail(t *testing.T) {
	if _, _, err := enforceSpecialistTail("news", "I cannot help with that request.", nil, []string{"AAPL"}); err == nil {
		t.Fatal("expected an error for a report with no JSON tail")
	}
	if _, _, err := enforceSpecialistTail("news", report("p", `{"domain":"news",`), nil, []string{"AAPL"}); err == nil {
		t.Fatal("expected an error for a malformed JSON tail")
	}
}

// Macro is a regime domain: it either covers everything or nothing. When FRED
// is down, every ticker is ungrounded and the whole scores array goes.
func TestEnforceStripsEntireRegimeDomain(t *testing.T) {
	in := report("prose", `{"domain":"macro","scores":[
	  {"ticker":"AAPL","strength":8},{"ticker":"MSFT","strength":7}
	],"missing":[]}`)

	out, res, err := enforceSpecialistTail("macro", in, []string{"AAPL", "MSFT"}, []string{"AAPL", "MSFT"})
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	if !equalStrings(res.Corrected, []string{"AAPL", "MSFT"}) {
		t.Errorf("Corrected = %v, want both names", res.Corrected)
	}
	got := tailOf(t, out)
	if len(got.Scores) != 0 {
		t.Errorf("scores = %+v, want none", got.Scores)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
