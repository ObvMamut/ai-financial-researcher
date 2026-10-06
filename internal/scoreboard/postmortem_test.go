package scoreboard

import (
	"os"
	"strings"
	"testing"
)

// attributionWith builds an attribution containing one sector cell of n closed
// trades, which is enough to test what a lesson may name.
func attributionWith(t *testing.T, sector string, n int) *Attribution {
	t.Helper()
	var entries []Entry
	for i := 0; i < n; i++ {
		entries = append(entries, closed("AAA", "BUY", OutcomeTarget, 1.5, func(e *Entry) {
			e.Sector = sector
			e.DomainScores = map[string]int{"quant": 6, "news": 5}
			e.Consensus = 0.95
		}))
	}
	return attributionOf(entries...)
}

// A post-mortem is the easiest output in this system to invent: it is a
// narrative about the system's own performance, written by the system. The one
// rule that makes it checkable is that every lesson must name a cell the
// arithmetic actually produced.
func TestPostMortemDeletesALessonThatNamesNoCell(t *testing.T) {
	a := attributionWith(t, "Energy", 6)
	pm, err := ParsePostMortem(`{"lessons":[
		{"cell":"Energy","n":6,"finding":"energy longs worked","action":"keep"},
		{"cell":"momentum has stopped working","n":40,"finding":"invented","action":"nothing"}
	]}`, a)
	if err != nil {
		t.Fatalf("ParsePostMortem: %v", err)
	}
	if len(pm.Lessons) != 1 || pm.Lessons[0].Cell != "Energy" {
		t.Fatalf("kept lessons = %+v, want only the Energy cell", pm.Lessons)
	}
	if len(pm.Rejected) != 1 || !strings.Contains(pm.Rejected[0], "names no cell") {
		t.Errorf("the invented lesson was not reported as deleted: %v", pm.Rejected)
	}
}

// A cell of four is not a tendency. The persona says so; this is what makes it
// true rather than requested.
func TestPostMortemRefusesAThinCell(t *testing.T) {
	a := attributionWith(t, "Energy", MinCellN-1)
	pm, err := ParsePostMortem(`{"lessons":[{"cell":"Energy","n":4,"finding":"x","action":"y"}]}`, a)
	if err != nil {
		t.Fatalf("ParsePostMortem: %v", err)
	}
	if len(pm.Lessons) != 0 {
		t.Errorf("a %d-trade cell produced a lesson: %+v", MinCellN-1, pm.Lessons)
	}
}

// A lesson that overstates its own sample is the specific error the whole block
// exists to prevent. Correcting the number is better than deleting the lesson,
// but the correction has to be recorded.
func TestPostMortemCorrectsAnOverstatedCount(t *testing.T) {
	a := attributionWith(t, "Energy", 6)
	pm, err := ParsePostMortem(`{"lessons":[{"cell":"energy","n":40,"finding":"x","action":"y"}]}`, a)
	if err != nil {
		t.Fatalf("ParsePostMortem: %v", err)
	}
	if len(pm.Lessons) != 1 || pm.Lessons[0].N != 6 {
		t.Fatalf("lesson = %+v, want n corrected to 6", pm.Lessons)
	}
	if len(pm.Rejected) != 1 || !strings.Contains(pm.Rejected[0], "claimed n=40") {
		t.Errorf("the overstatement was not recorded: %v", pm.Rejected)
	}
}

func TestPostMortemBoundsTheLessonCount(t *testing.T) {
	a := attributionWith(t, "Energy", 6)
	var b strings.Builder
	b.WriteString(`{"lessons":[`)
	for i := 0; i < MaxLessons+3; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"cell":"Energy","n":6,"finding":"x","action":"y"}`)
	}
	b.WriteString(`]}`)

	pm, err := ParsePostMortem(b.String(), a)
	if err != nil {
		t.Fatalf("ParsePostMortem: %v", err)
	}
	if len(pm.Lessons) != MaxLessons {
		t.Errorf("kept %d lessons, want the %d ceiling", len(pm.Lessons), MaxLessons)
	}
}

// A pipeline that silently re-weights itself on its own reading of its own
// record has no control arm left. Suggestions are parsed, validated and shown —
// never applied.
func TestPostMortemValidatesWeightSuggestions(t *testing.T) {
	a := attributionWith(t, "Energy", 6)
	pm, err := ParsePostMortem(`{"lessons":[],"weight_suggestions":[
		{"domain":"Sentiment","direction":"DOWN","reason":"by_domain sentiment 4/17"},
		{"domain":"vibes","direction":"up","reason":"none"},
		{"domain":"quant","direction":"sideways","reason":"none"},
		{"domain":"news","direction":"up","reason":""}
	]}`, a)
	if err != nil {
		t.Fatalf("ParsePostMortem: %v", err)
	}
	if len(pm.WeightSuggestions) != 1 {
		t.Fatalf("suggestions = %+v, want only the well-formed one", pm.WeightSuggestions)
	}
	got := pm.WeightSuggestions[0]
	if got.Domain != "sentiment" || got.Direction != "down" {
		t.Errorf("suggestion = %+v, want normalised sentiment/down", got)
	}
	if len(pm.Rejected) != 3 {
		t.Errorf("rejected %d malformed suggestions, want 3: %v", len(pm.Rejected), pm.Rejected)
	}
}

// An empty section header would read as "we looked and found nothing", which is
// a different claim from "there is not enough record to look".
func TestPostMortemBlockIsSilentUntilItHasSomethingToSay(t *testing.T) {
	if got := (*PostMortem)(nil).Block(); got != "" {
		t.Errorf("a nil post-mortem rendered %q", got)
	}
	thin := &PostMortem{NClosed: MinClosedForPostMortem - 1, Lessons: []Lesson{{Cell: "Energy", N: 6, Finding: "x"}}}
	if got := thin.Block(); got != "" {
		t.Errorf("a thin record rendered a block: %q", got)
	}
	empty := &PostMortem{NClosed: 40}
	if got := empty.Block(); got != "" {
		t.Errorf("no lessons rendered a block: %q", got)
	}
	real := &PostMortem{NClosed: 40, Lessons: []Lesson{{Cell: "sell/wide-stop", N: 6, Finding: "1 for 6", Action: "downweight"}}}
	block := real.Block()
	if !strings.Contains(block, "sell/wide-stop") || !strings.Contains(block, "n=6") {
		t.Errorf("block does not carry the cell and its count: %q", block)
	}
	if !strings.Contains(block, "inside the same band") {
		t.Errorf("block does not bound what the Chief may do with it: %q", block)
	}
}

// The half of the evidence the arithmetic cannot produce.
func TestClosedTradesBlockCarriesTheStatedReasoning(t *testing.T) {
	s := &Summary{Replay: true, Entries: []Entry{
		closed("AAA", "BUY", OutcomeTarget, 1.8, func(e *Entry) {
			e.GeneratedAt, e.Why, e.Sector = "2026-08-01T00:00:00Z", "a dated catalyst inside the window", "Energy"
			e.BaseConfidence, e.Consensus = 53, 0.9
			e.DomainScores = map[string]int{"quant": 6, "news": 6}
		}),
		closed("BBB", "BUY", OutcomeOpen, 0, func(e *Entry) { e.Why = "still running" }),
	}}
	s.aggregate()
	block := ClosedTradesBlock(s, 10)

	if !strings.Contains(block, "a dated catalyst inside the window") {
		t.Errorf("the stated reasoning did not reach the block:\n%s", block)
	}
	if !strings.Contains(block, "base 53") || !strings.Contains(block, "agree 90%") {
		t.Errorf("the computed score behind the trade is missing:\n%s", block)
	}
	if strings.Contains(block, "still running") {
		t.Errorf("an open position is not a closed trade:\n%s", block)
	}
}

func cellAttribution() *Attribution {
	return &Attribution{
		NClosed:    35,
		BySetup:    map[string]Bucket{"sell/wide-stop": {N: 7}, "buy/wide-stop": {N: 8}, "buy/medium-stop": {N: 9}, "drift": {N: 2}},
		ByCoverage: map[string]Bucket{"1-2 domains": {N: 7}, "3-4 domains": {N: 7}, "5 domains": {N: 5}},
		BySector:   map[string]Bucket{"Information Technology": {N: 6}},
		Fills: FillRecord{ByOffset: map[string]FillBucket{
			"-1.5% to -0.25%":                          {N: 26},
			"at the close (±0.25%)":                    {N: 36},
			"below -1.5% (waiting for a better price)": {N: 16},
		}},
	}
}

// The prompt shows cells as "<family> <key>" and tells the model to spell them
// that way, so that spelling has to be accepted.
func TestPostMortemAcceptsCellsSpelledAsTheTableRendersThem(t *testing.T) {
	pm, err := ParsePostMortem(`{"lessons":[
		{"cell":"setup sell/wide-stop","n":0,"finding":"a","action":"b"},
		{"cell":"  Entry At The Close (±0.25%) ","n":1,"finding":"c","action":"d"},
		{"cell":"sell/wide-stop","n":7,"finding":"bare key still works","action":"e"},
		{"cell":"setup drift","n":2,"finding":"thin","action":"f"},
		{"cell":"setup invented","n":9,"finding":"invented","action":"g"}
	]}`, cellAttribution())
	if err != nil {
		t.Fatal(err)
	}
	if len(pm.Lessons) != 3 {
		t.Fatalf("kept %d lessons, want 3: %+v rejected=%v", len(pm.Lessons), pm.Lessons, pm.Rejected)
	}
	if pm.Lessons[0].N != 7 || pm.Lessons[1].N != 36 {
		t.Errorf("n not taken from the table: %+v", pm.Lessons)
	}
}

// Regression: in runs/2026-10-06T18-07-24 all 8 lessons cited cells exactly as
// the table rendered them and every one was rejected.
func TestPostMortemReplayOfTheRunThatRejectedEveryLesson(t *testing.T) {
	raw, err := os.ReadFile("testdata/postmortem-2026-10-06.json")
	if err != nil {
		t.Fatal(err)
	}
	pm, err := ParsePostMortem(string(raw), cellAttribution())
	if err != nil {
		t.Fatal(err)
	}
	// The run produced 8 lessons; the ceiling is MaxLessons, so min(8, MaxLessons) are kept.
	want := 8
	if MaxLessons < want {
		want = MaxLessons
	}
	if len(pm.Lessons) != want {
		t.Fatalf("kept %d lessons, want %d; rejected=%v", len(pm.Lessons), want, pm.Rejected)
	}
}
