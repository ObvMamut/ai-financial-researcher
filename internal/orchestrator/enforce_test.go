package orchestrator

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/parse"
)

// report builds a specialist report with the given structured tail.
// sl builds the shortlist argument from bare tickers, for tests that do not
// care about the company names the alias resolution reads.
func sl(tickers ...string) []model.Candidate {
	out := make([]model.Candidate, 0, len(tickers))
	for _, t := range tickers {
		out = append(out, model.Candidate{Ticker: t})
	}
	return out
}

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

	out, res, err := enforceSpecialistTail("sentiment", in, []string{"MSFT"}, nil, sl("AAPL", "MSFT"))
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

	out, res, err := enforceSpecialistTail("news", in, nil, nil, sl("AAPL"))
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

	out, res, err := enforceSpecialistTail("news", in, []string{"MSFT"}, nil, sl("AAPL", "MSFT"))
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

	out, _, err := enforceSpecialistTail("news", in, []string{"MSFT", "NVDA"}, nil, sl("AAPL", "MSFT", "NVDA"))
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
	if _, _, err := enforceSpecialistTail("news", "I cannot help with that request.", nil, nil, sl("AAPL")); err == nil {
		t.Fatal("expected an error for a report with no JSON tail")
	}
	if _, _, err := enforceSpecialistTail("news", report("p", `{"domain":"news",`), nil, nil, sl("AAPL")); err == nil {
		t.Fatal("expected an error for a malformed JSON tail")
	}
}

// Macro is a regime domain: it either covers everything or nothing. When FRED
// is down, every ticker is ungrounded and the whole scores array goes.
func TestEnforceStripsEntireRegimeDomain(t *testing.T) {
	in := report("prose", `{"domain":"macro","scores":[
	  {"ticker":"AAPL","strength":8},{"ticker":"MSFT","strength":7}
	],"missing":[]}`)

	out, res, err := enforceSpecialistTail("macro", in, []string{"AAPL", "MSFT"}, nil, sl("AAPL", "MSFT"))
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

	out, res, err := enforceSpecialistTail("sentiment", in, nil, nil, sl("AMGN", "ORCL"))
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

	out, res, err := enforceSpecialistTail("macro", in, []string{"9984.T"}, nil, sl("9984.T", "AMGN"))
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
	out, res, err := enforceSpecialistTail("quant", in, nil, nil, sl("AMGN"))
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
		sl("AMGN", "BAYN.DE"))
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

// A self-contradicted name landed in the "This run had no verified X data for …"
// sentence, which is false about it: the run *did* have data, and the agent's own
// `missing` array is what took the score away. In a block whose whole purpose is
// keeping the Chief's picture of the evidence accurate, that sentence was a lie
// about the only name it named.
func TestEnforceNoticeSeparatesADisownedScoreFromAbsentData(t *testing.T) {
	in := report("Positioning notes.", `{"domain":"sentiment","scores":[
	  {"ticker":"ORCL","bias":"bearish","strength":5,"note":"crowded calls"},
	  {"ticker":"BAYN.DE","bias":"bearish","strength":5,"note":"no data at all"}
	],"missing":["ORCL"]}`)

	out, res, err := enforceSpecialistTail("sentiment", in,
		[]string{"BAYN.DE"}, nil, sl("ORCL", "BAYN.DE"))
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	if !equalStrings(res.SelfContradicted, []string{"ORCL"}) {
		t.Errorf("SelfContradicted = %v, want [ORCL]", res.SelfContradicted)
	}
	if res.Scored != 2 {
		t.Errorf("Scored = %d, want 2 — the denominator counts what the agent wrote", res.Scored)
	}

	if strings.Contains(out, "no verified sentiment data for ORCL") {
		t.Errorf("a name the run had data for was reported as uncovered:\n%s", out)
	}
	if !strings.Contains(out, "no verified sentiment data for BAYN.DE") {
		t.Errorf("the genuinely uncovered name lost its own sentence:\n%s", out)
	}
	if !strings.Contains(out, "placed ORCL in *both* its `scores` and its own `missing` array") {
		t.Errorf("the disowned score is not explained:\n%s", out)
	}
}

// An abstention-free run must read exactly as it did before.
func TestEnforceNoticeUnchangedWithoutAbstentions(t *testing.T) {
	in := report("prose", `{"domain":"macro","scores":[{"ticker":"AAPL","strength":8}],"missing":[]}`)
	out, _, err := enforceSpecialistTail("macro", in, []string{"AAPL"}, nil, sl("AAPL"))
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

// A `neutral` vote is the most expensive answer a domain can give: sign 0
// contributes nothing to the weighted score while still consuming the domain's
// full weight, so it costs more than a gap and earns none of the coverage cap
// relief a gap would. On 2026-09-01 news scored AMGN neutral while its own
// paragraph named a regulator suspending a marketed drug that morning, and
// nothing in the run distinguished that from a name news had nothing on.
func TestNeutralScoresAreRecordedButNotDeleted(t *testing.T) {
	stdout := "Report.\n\n```json\n" + `{"domain":"news","scores":[
      {"ticker":"AMGN","bias":"neutral","strength":0,"note":"mixed"},
      {"ticker":"MRK","bias":"BULLISH","strength":7,"note":"clear"},
      {"ticker":"ORCL","bias":"Neutral","strength":3,"note":"standoff"}
    ],"missing":[]}` + "\n```\n"

	out, enf, err := enforceSpecialistTail("news", stdout, nil, nil, sl("AMGN", "MRK", "ORCL"))
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	if want := []string{"AMGN", "ORCL"}; !reflect.DeepEqual(enf.Neutral, want) {
		t.Errorf("Neutral = %v, want %v (case-insensitive)", enf.Neutral, want)
	}
	// Recorded, not removed: a genuine standoff is a legitimate verdict.
	for _, ticker := range []string{"AMGN", "MRK", "ORCL"} {
		if !strings.Contains(out, ticker) {
			t.Errorf("%s was deleted from the tail; a neutral score is kept", ticker)
		}
	}
	if len(enf.Corrected) != 0 {
		t.Errorf("a neutral score is not a correction: %v", enf.Corrected)
	}
	if enf.Scored != 3 {
		t.Errorf("Scored = %d, want 3", enf.Scored)
	}
}

func TestEnforcementResolvesACompanyNameToItsTicker(t *testing.T) {
	// 2026-09-03: macro scored twelve names and wrote OCBC, KAKAO and MEDIATEK
	// for O39.SI, 035720.KS and 2454.TW — the names in the shortlist block it
	// was handed, which carries `name` beside `ticker`. All three scores were
	// struck as off-shortlist, and those are exactly the names that then shipped
	// as ideas 3 and 5 on one domain each.
	shortlist := []model.Candidate{
		{Ticker: "O39.SI", Name: "Oversea-Chinese Banking Corporation"},
		{Ticker: "035720.KS", Name: "Kakao Corp."},
		{Ticker: "2454.TW", Name: "MediaTek Inc."},
		{Ticker: "MU", Name: "Micron Technology, Inc."},
	}
	in := report("Regional banks are carrying the ^STI.", `{"domain":"macro","scores":[
		{"ticker":"OCBC","bias":"bullish","strength":4},
		{"ticker":"KAKAO","bias":"bearish","strength":3},
		{"ticker":"MEDIATEK","bias":"bullish","strength":5},
		{"ticker":"MU","bias":"neutral","strength":0}
	],"missing":[]}`)

	out, res, err := enforceSpecialistTail("macro", in, nil, nil, shortlist)
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	if len(res.OffShortlist) != 0 {
		t.Errorf("OffShortlist = %v — three real scores were deleted for naming the company", res.OffShortlist)
	}
	want := []string{"KAKAO→035720.KS", "MEDIATEK→2454.TW", "OCBC→O39.SI"}
	if !equalStrings(res.Renamed, want) {
		t.Errorf("Renamed = %v, want %v", res.Renamed, want)
	}
	// The tail must be rewritten to the symbol, or the score is still invisible
	// to base scoring, the Chief's table and the risk gate.
	for _, sym := range []string{`"O39.SI"`, `"035720.KS"`, `"2454.TW"`} {
		if !strings.Contains(out, sym) {
			t.Errorf("the corrected tail does not carry %s:\n%s", sym, out)
		}
	}
	for _, alias := range []string{`"OCBC"`, `"KAKAO"`, `"MEDIATEK"`} {
		if strings.Contains(out, alias) {
			t.Errorf("the corrected tail still scores under %s:\n%s", alias, out)
		}
	}
	if res.Scored != 4 {
		t.Errorf("Scored = %d, want 4", res.Scored)
	}
}

func TestEnforcementStillDeletesAnInventedTicker(t *testing.T) {
	// The alias map must not become a way for a hallucinated symbol to survive.
	shortlist := []model.Candidate{{Ticker: "MU", Name: "Micron Technology, Inc."}}
	in := report("p", `{"domain":"news","scores":[
		{"ticker":"ZZZZ","bias":"bullish","strength":6},
		{"ticker":"MU","bias":"bullish","strength":5}
	],"missing":[]}`)

	_, res, err := enforceSpecialistTail("news", in, nil, nil, shortlist)
	if err != nil {
		t.Fatalf("enforceSpecialistTail: %v", err)
	}
	if !equalStrings(res.OffShortlist, []string{"ZZZZ"}) {
		t.Errorf("OffShortlist = %v, want [ZZZZ]", res.OffShortlist)
	}
}

func TestShortlistAliasesRefuseAnAmbiguousKey(t *testing.T) {
	// Two shortlisted companies whose first word is the same cannot both claim
	// it, and neither may — resolving to one of them would be a coin toss that
	// silently reassigns a score.
	a := shortlistAliases([]model.Candidate{
		{Ticker: "SAN.MC", Name: "Banco Santander"},
		{Ticker: "BBVA.MC", Name: "Banco Bilbao Vizcaya Argentaria"},
	})
	if got, ok := a["BANCO"]; ok && got != "" {
		t.Errorf("BANCO resolved to %q — two shortlisted banks share it", got)
	}
	if a["SANTANDER"] != "" {
		t.Errorf("a non-first word became an alias: %q", a["SANTANDER"])
	}

	// And an alias may never shadow a real shortlisted ticker.
	b := shortlistAliases([]model.Candidate{
		{Ticker: "MU", Name: "Micron Technology, Inc."},
		{Ticker: "035720.KS", Name: "MU Holdings"},
	})
	if b["MU"] != "" {
		t.Errorf("MU as a company name shadowed MU the ticker: %q", b["MU"])
	}
}
