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

	out, res, err := enforceSpecialistTail("sentiment", in, []string{"MSFT"}, nil, []string{"AAPL", "MSFT"})
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

	out, res, err := enforceSpecialistTail("news", in, nil, nil, []string{"AAPL"})
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

	out, res, err := enforceSpecialistTail("news", in, []string{"MSFT"}, nil, []string{"AAPL", "MSFT"})
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

	out, _, err := enforceSpecialistTail("news", in, []string{"MSFT", "NVDA"}, nil, []string{"AAPL", "MSFT", "NVDA"})
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
	if _, _, err := enforceSpecialistTail("news", "I cannot help with that request.", nil, nil, []string{"AAPL"}); err == nil {
		t.Fatal("expected an error for a report with no JSON tail")
	}
	if _, _, err := enforceSpecialistTail("news", report("p", `{"domain":"news",`), nil, nil, []string{"AAPL"}); err == nil {
		t.Fatal("expected an error for a malformed JSON tail")
	}
}

// Macro is a regime domain: it either covers everything or nothing. When FRED
// is down, every ticker is ungrounded and the whole scores array goes.
func TestEnforceStripsEntireRegimeDomain(t *testing.T) {
	in := report("prose", `{"domain":"macro","scores":[
	  {"ticker":"AAPL","strength":8},{"ticker":"MSFT","strength":7}
	],"missing":[]}`)

	out, res, err := enforceSpecialistTail("macro", in, []string{"AAPL", "MSFT"}, nil, []string{"AAPL", "MSFT"})
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

// The 2026-09-01 sentiment report scored ORCL "neutral, strength 3" and listed
// ORCL in its own `missing` array. Enforcement only ever checked the agent
// against the app's computed coverage — ORCL is a US listing, so it was
// grounded — and never against the agent's own report. Both survived into the
// artifact the Chief read, and the base score counted 15% of the domain weight
// as covered on the strength of a number the report disowned.
func TestEnforceDropsScoresTheAgentItselfCallsMissing(t *testing.T) {
	in := report("Positioning notes.", `{"domain":"sentiment","scores":[
	  {"ticker":"AMGN","bias":"bearish","strength":4,"note":"routine insider selling"},
	  {"ticker":"ORCL","bias":"neutral","strength":3,"note":"positioning evidence is empty"}
	],"missing":["ORCL"]}`)

	out, res, err := enforceSpecialistTail("sentiment", in, nil, nil, []string{"AMGN", "ORCL"})
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	if want := []string{"ORCL"}; !equalStrings(res.SelfContradicted, want) {
		t.Errorf("SelfContradicted = %v, want %v", res.SelfContradicted, want)
	}
	if len(res.Corrected) != 0 {
		t.Errorf("Corrected = %v — ORCL is grounded, the agent just disclaimed it", res.Corrected)
	}

	got := tailOf(t, out)
	if len(got.Scores) != 1 || got.Scores[0].Ticker != "AMGN" {
		t.Errorf("scores = %+v, want AMGN only", got.Scores)
	}
	if !equalStrings(got.Missing, []string{"ORCL"}) {
		t.Errorf("missing = %v, want [ORCL]", got.Missing)
	}
}

// Deleting a score was never enough. Macro's scores for the four non-US names
// were correctly removed as ungrounded on 2026-09-01, and its paragraphs about
// them stayed — so the Chief read the prose and took two −3 adjustments from a
// domain the app had just ruled could not see those names.
func TestEnforceLabelsProseAboutStrippedNames(t *testing.T) {
	in := report(
		"9984.T is shorted against ^N225 mean-reverting, which is a headwind for the bearish call.",
		`{"domain":"macro","scores":[
	  {"ticker":"9984.T","bias":"bearish","strength":3,"note":"regime leans"},
	  {"ticker":"AMGN","bias":"bullish","strength":4,"note":"SPX near highs"}
	],"missing":[]}`)

	out, res, err := enforceSpecialistTail("macro", in, []string{"9984.T"}, nil, []string{"9984.T", "AMGN"})
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	if want := []string{"9984.T"}; !equalStrings(res.Corrected, want) {
		t.Fatalf("Corrected = %v, want %v", res.Corrected, want)
	}
	// The prose survives — some of what it leans on is verified regime data the
	// Chief gets separately — but it is now labelled.
	if !strings.Contains(out, "headwind for the bearish call") {
		t.Error("the agent's reasoning was deleted; it should be labelled, not removed")
	}
	for _, want := range []string{"Enforcement notice", "9984.T", "unscored context", "must not be used to adjust a base score"} {
		if !strings.Contains(out, want) {
			t.Errorf("removal note missing %q:\n%s", want, out)
		}
	}
	// The note must precede the prose it is about, or it is a footnote.
	if strings.Index(out, "Enforcement notice") > strings.Index(out, "headwind") {
		t.Error("the notice must come before the prose it qualifies")
	}
}

// A report with nothing to correct must not grow a notice.
func TestEnforceAddsNoNoticeWhenNothingWasRemoved(t *testing.T) {
	in := report("prose", `{"domain":"quant","scores":[
	  {"ticker":"AMGN","bias":"bullish","strength":7,"note":"ok"}
	],"missing":[]}`)
	out, res, err := enforceSpecialistTail("quant", in, nil, nil, []string{"AMGN"})
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	if res.Any() {
		t.Errorf("nothing should have been corrected, got %+v", res)
	}
	if strings.Contains(out, "Enforcement notice") {
		t.Error("an honest report must not be annotated")
	}
	if out != in {
		t.Error("an honest report must come back byte-identical")
	}
}

// TestEnforceNoticeDistinguishesAbstentionFromAbsentData keeps a true statement
// in front of the Chief Analyst.
//
// Both removals delete the score, but they say opposite things about the run.
// Sentiment stands down on a name whose insider filings and option chain were
// both fetched and both read as non-directional — telling the Chief the run "had
// no verified sentiment data" for it would be false, in the one block whose whole
// job is to keep the Chief's picture of the evidence accurate.
func TestEnforceNoticeDistinguishesAbstentionFromAbsentData(t *testing.T) {
	in := report("Positioning notes.", `{"domain":"sentiment","scores":[
	  {"ticker":"AMGN","bias":"bearish","strength":6,"note":"routine insider selling"},
	  {"ticker":"BAYN.DE","bias":"bearish","strength":5,"note":"no data at all"}
	],"missing":[]}`)

	out, res, err := enforceSpecialistTail("sentiment", in,
		[]string{"AMGN", "BAYN.DE"}, // both lose their scores
		[]string{"AMGN"},            // but only AMGN was an abstention
		[]string{"AMGN", "BAYN.DE"})
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	if !equalStrings(res.Abstained, []string{"AMGN"}) {
		t.Errorf("Abstained = %v, want [AMGN]", res.Abstained)
	}
	if !equalStrings(res.Corrected, []string{"AMGN", "BAYN.DE"}) {
		t.Errorf("Corrected = %v, want both — an abstention still loses its score", res.Corrected)
	}

	if !strings.Contains(out, "no verified sentiment data for BAYN.DE") {
		t.Errorf("the genuinely uncovered name is not reported as such:\n%s", out)
	}
	if strings.Contains(out, "no verified sentiment data for AMGN") {
		t.Errorf("an abstention was reported as absent data:\n%s", out)
	}
	if !strings.Contains(out, "did* have sentiment data for AMGN") {
		t.Errorf("the abstention is not explained:\n%s", out)
	}
	if !strings.Contains(out, "not the same as absent evidence") {
		t.Errorf("the notice does not draw the distinction it exists to draw:\n%s", out)
	}

	got := tailOf(t, out)
	if len(got.Scores) != 0 {
		t.Errorf("scores = %+v, want none", got.Scores)
	}
}

// An abstention-free run must read exactly as it did before.
func TestEnforceNoticeUnchangedWithoutAbstentions(t *testing.T) {
	in := report("prose", `{"domain":"macro","scores":[{"ticker":"AAPL","strength":8}],"missing":[]}`)
	out, _, err := enforceSpecialistTail("macro", in, []string{"AAPL"}, nil, []string{"AAPL"})
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	if !strings.Contains(out, "This run had no verified macro data for AAPL, so its score below was deleted") {
		t.Errorf("the ordinary notice changed shape:\n%s", out)
	}
	if strings.Contains(out, "did* have") {
		t.Errorf("an abstention clause appeared with no abstentions:\n%s", out)
	}
}
