package orchestrator

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// TestSectionSizesAreExactAndNoSectionIsFoldedIntoItsNeighbour is Task 11's
// step 1 acceptance test, verbatim from the plan: a section without a marker
// string used to silently inherit its predecessor's bytes under
// thesis_budget.go's deleted marker-scanning function, which is why the
// SNOW/OKTA/ORCL overflows' component tables could not be reconciled to the
// byte without accounting for the marker strings' own bytes separately.
// assembleSections measures each section directly, so no such folding is
// possible: sizes must be exactly what each Body's own length is.
func TestSectionSizesAreExactAndNoSectionIsFoldedIntoItsNeighbour(t *testing.T) {
	s := []promptSection{
		{Name: "identity", Body: "abc", Mandatory: true},
		{Name: "temporal_facts", Body: "defgh", Mandatory: true},
		{Name: "evidence", Body: strings.Repeat("x", 100)},
	}
	text, sizes, omitted, err := assembleSections(s, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]int{"identity": 3, "temporal_facts": 5, "evidence": 100}
	if !reflect.DeepEqual(sizes, want) {
		t.Fatalf("sizes = %+v, want %+v", sizes, want)
	}
	if len(omitted) != 0 {
		t.Fatalf("omitted = %v, want none — 108 bytes fits comfortably under 1000", omitted)
	}
	sum := 0
	for _, v := range sizes {
		sum += v
	}
	// assembleSections adds no separators of its own — a section's Body is
	// exactly the bytes it contributes — so sum(sizes) must equal len(text)
	// exactly, not merely "plus separators".
	if sum != len(text) {
		t.Fatalf("sum(sizes) = %d, len(text) = %d; they must be equal with zero residual", sum, len(text))
	}
	if want := "abc" + "defgh" + strings.Repeat("x", 100); text != want {
		t.Fatalf("text = %q, want plain in-order concatenation %q", text, want)
	}
}

// TestOptionalSectionsAreDroppedFromTheTailAndNamed is step 2's first test.
//
// The plan's own sketch of this scenario (three 100-byte mandatory sections,
// two 500-byte optional ones, limit 900) does not actually exercise "keeps
// dropping until it fits": removing only the last (tail) 500-byte optional
// already brings 1300 down to 800, which is under 900, so a correct
// "drop from the tail until it fits" implementation would stop after one
// drop — not the two the plan's comment asserts. That is a genuine
// arithmetic inconsistency in the plan text, not a subtlety in the
// algorithm; see the task-11 report for the fuller note. The scenario below
// keeps the same shape (three equal mandatory sections, two equal optional
// ones, tail-first, named) but picks a limit that only fits once *both*
// optionals are gone, so it actually exercises repeated dropping.
func TestOptionalSectionsAreDroppedFromTheTailAndNamed(t *testing.T) {
	s := []promptSection{
		{Name: "mandatory_a", Body: strings.Repeat("a", 100), Mandatory: true},
		{Name: "mandatory_b", Body: strings.Repeat("b", 100), Mandatory: true},
		{Name: "mandatory_c", Body: strings.Repeat("c", 100), Mandatory: true},
		{Name: "optional_a", Body: strings.Repeat("d", 400), Mandatory: false},
		{Name: "optional_b", Body: strings.Repeat("e", 400), Mandatory: false},
	}
	const limit = 500 // 300 (mandatory) + one 400-byte optional is already 700 > 500
	text, sizes, omitted, err := assembleSections(s, limit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{strings.Repeat("a", 100), strings.Repeat("b", 100), strings.Repeat("c", 100)} {
		if !strings.Contains(text, want) {
			t.Fatalf("text is missing a mandatory body (%d bytes of %q)", len(want), want[:1])
		}
	}
	for _, name := range []string{"mandatory_a", "mandatory_b", "mandatory_c"} {
		if _, ok := sizes[name]; !ok {
			t.Errorf("sizes is missing mandatory section %q", name)
		}
	}
	for _, unwanted := range []string{strings.Repeat("d", 400), strings.Repeat("e", 400)} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("text still contains a dropped optional body")
		}
	}
	if want := []string{"optional_b", "optional_a"}; !reflect.DeepEqual(omitted, want) {
		t.Fatalf("omitted = %v, want %v (tail first: the highest-index optional drops first)", omitted, want)
	}
	if len(text) > limit {
		t.Fatalf("len(text) = %d, want <= %d", len(text), limit)
	}
	for _, name := range []string{"optional_a", "optional_b"} {
		if _, ok := sizes[name]; ok {
			t.Errorf("sizes reports a dropped section (%q) — a section that contributed nothing to text must not keep contributing to its accounting", name)
		}
	}
}

// TestMandatoryOverflowIsACapacityErrorNamingTheRequirements is step 2's
// second test: when the mandatory sections alone exceed the limit, no amount
// of optional dropping can help, and the caller must be told which
// requirements did not fit — exactly what the September 15 audit's generic
// "input capacity exceeded" message could not say.
func TestMandatoryOverflowIsACapacityErrorNamingTheRequirements(t *testing.T) {
	s := []promptSection{
		{Name: "identity", Body: strings.Repeat("a", 600), Mandatory: true},
		{Name: "temporal_facts", Body: strings.Repeat("b", 600), Mandatory: true},
	}
	text, sizes, _, err := assembleSections(s, 900)
	if text != "" {
		t.Fatalf("text = %q, want empty on a mandatory overflow", text)
	}
	if sizes != nil {
		t.Fatalf("sizes = %v, want nil on a mandatory overflow", sizes)
	}
	var capErr promptCapacityError
	if !errors.As(err, &capErr) {
		t.Fatalf("err = %v (%T), want a promptCapacityError", err, err)
	}
	for _, name := range []string{"identity", "temporal_facts"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name mandatory section %q — the caller cannot say which requirement did not fit", err.Error(), name)
		}
	}
}

// TestPromptProfileComponentsPartitionBytesExactly is context.md's own named
// acceptance test: "the sum of components_bytes must equal the assembled
// length exactly, with no residual." It exercises the real preparePrompt
// (not a reimplementation), so a regression that reintroduces an overlapping
// aggregate (the historical "data" key that summed alongside its own
// marker-scanned children) or drops the "instructions" remainder would show
// up here as a sum that no longer equals profile.Bytes.
func TestPromptProfileComponentsPartitionBytesExactly(t *testing.T) {
	runner, _, done := thesisFixture(t, func(string, int) string { return "unused" })
	defer done()
	sections := []promptSection{
		{Name: "identity", Mandatory: true, Body: `Company identity: {"ticker":"AAA"}`},
		{Name: "temporal_facts", Mandatory: true, Body: "\nComputed temporal facts:\n{}"},
		{Name: "evidence", Mandatory: true, Body: "\nEvidence:\n" + strings.Repeat("x", 500)},
		{Name: "retrieval_errors", Mandatory: false, Body: "\nRetrieval diagnostics:\n[]"},
	}
	_, profile, err := runner.preparePrompt("thesis-researcher", "components-sum", sections, 0)
	if err != nil {
		t.Fatalf("preparePrompt: %v", err)
	}
	sum := 0
	for _, v := range profile.Components {
		sum += v
	}
	if sum != profile.Bytes {
		t.Fatalf("sum(Components) = %d, profile.Bytes = %d (residual %d)", sum, profile.Bytes, profile.Bytes-sum)
	}
	if len(profile.Omitted) != 0 {
		t.Fatalf("Omitted = %v, want none — nothing here needs dropping at noSectionLimit", profile.Omitted)
	}
}

// TestChallengeSectionsNeverOmitPreviousChallengeUnderCapacity pins Fix 1 of
// the Task 11 review round: previous_challenge must be mandatory, never a
// silently droppable optional. It drives assembleSections through the real
// production helper (challengeSections, used by thesis.go's challenge
// closure for both challenge("") and challenge("-final")) rather than a
// hand-copied section list, so a regression that flips the flag back at the
// call site is caught here instead of only in a duplicate that could drift
// from it.
//
// agents/thesis-challenger.md instructs the model unconditionally to, on
// final review, "judge whether the previous material issues were actually
// resolved by the new evidence" — and validateReviewConsistency (thesis.go)
// never sees the previous challenge, only the current dossier and the
// current review, so nothing in Go backstops a challenge that never saw what
// it was supposed to re-examine.
func TestChallengeSectionsNeverOmitPreviousChallengeUnderCapacity(t *testing.T) {
	base := []promptSection{
		{Name: "identity", Mandatory: true, Body: strings.Repeat("i", 10)},
		{Name: "temporal_facts", Mandatory: true, Body: strings.Repeat("t", 10)},
		{Name: "evidence", Mandatory: true, Body: strings.Repeat("e", 10)},
		{Name: "source_urls", Mandatory: false, Body: strings.Repeat("s", 10)},
		{Name: "request_results", Mandatory: true, Body: strings.Repeat("r", 10)},
		{Name: "dossier_hash", Mandatory: true, Body: strings.Repeat("h", 10)},
		{Name: "previous_dossier", Mandatory: true, Body: strings.Repeat("p", 10)},
		{Name: "compaction_originals", Mandatory: false, Body: strings.Repeat("c", 10)},
		{Name: "retrieval_errors", Mandatory: false, Body: strings.Repeat("d", 10)},
	}
	previous := model.ThesisChallenge{Ticker: "AAA", Verdict: "revise", MaterialIssues: []model.MaterialIssue{{Issue: "unresolved: does the delivery timing actually resolve expectations"}}}
	sections := challengeSections(base, previous)

	last := sections[len(sections)-1]
	if last.Name != "previous_challenge" || !last.Mandatory {
		t.Fatalf("challengeSections' last section = %+v, want Name=previous_challenge Mandatory=true", last)
	}

	mandatoryBase := 0
	for _, sec := range base {
		if sec.Mandatory {
			mandatoryBase += len(sec.Body)
		}
	}
	// One byte short of fitting every mandatory base section plus
	// previous_challenge. Dropping every optional base section first (the
	// only sections a correct implementation could remove without also
	// touching previous_challenge) still leaves the total exactly one byte
	// over this limit, so reaching it at all requires dropping
	// previous_challenge too — which must never happen silently.
	limit := mandatoryBase + len(last.Body) - 1

	text, _, omitted, err := assembleSections(sections, limit)
	if err == nil {
		t.Fatalf("expected a capacity error naming previous_challenge; got text=%d bytes, omitted=%v", len(text), omitted)
	}
	if !strings.Contains(err.Error(), "previous_challenge") {
		t.Fatalf("error %q does not name previous_challenge — the caller cannot tell this requirement did not fit", err.Error())
	}
	for _, name := range omitted {
		if name == "previous_challenge" {
			t.Fatal("previous_challenge was silently dropped for capacity instead of failing loudly")
		}
	}
}

// TestDuplicateSectionNameIsRejected pins Fix 2 of the Task 11 review round:
// two sections sharing a Name must never silently overwrite each other in
// sizes (sum(sizes) would then undercount while text correctly kept both
// bodies) — exactly the kind of unattributed lie the deleted marker scan
// told. Task 12 is where this can first happen for real: a global Chief
// board merges many companies' sections into one list, and per-company names
// collide unless namespaced.
func TestDuplicateSectionNameIsRejected(t *testing.T) {
	s := []promptSection{
		{Name: "evidence", Body: "company one's evidence", Mandatory: true},
		{Name: "evidence", Body: "company two's evidence", Mandatory: true},
	}
	text, sizes, omitted, err := assembleSections(s, 1000)
	if err == nil {
		t.Fatalf("expected an error for a duplicate section name; got text=%q sizes=%v omitted=%v", text, sizes, omitted)
	}
	if text != "" || sizes != nil {
		t.Fatalf("text = %q, sizes = %v; want both empty when assembly is refused", text, sizes)
	}
	if !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("error %q does not name the duplicate section", err.Error())
	}
}

func TestProductionPromptFitsOptionalsAndPersistsRequiredRefusal(t *testing.T) {
	runner, _, done := thesisFixture(t, func(string, int) string { t.Fatal("must not dispatch"); return "" })
	defer done()
	wrapper, err := runner.promptWrapperBytes("thesis-challenger")
	if err != nil {
		t.Fatal(err)
	}
	sections := []promptSection{{Name: "compaction_originals", Mandatory: true, Body: "material qualification"}, {Name: "retrieval_errors", Body: strings.Repeat("optional", 100)}}
	runner.cfg.Research.Budgets.Challenger.InputBytes = wrapper + len(sections[0].Body)
	prompt, profile, err := runner.preparePrompt("thesis-challenger", "fit-real", sections, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "material qualification") || strings.Contains(prompt, "optional") || !reflect.DeepEqual(profile.Omitted, []string{"retrieval_errors"}) {
		t.Fatalf("incorrect fitted prompt: %+v", profile)
	}
	if profile.Version != 2 || profile.Bytes != runner.cfg.Research.Budgets.Challenger.InputBytes {
		t.Fatalf("incorrect exact profile: %+v", profile)
	}
	runner.cfg.Research.Budgets.Challenger.InputBytes--
	prompt, profile, err = runner.preparePrompt("thesis-challenger", "refuse-real", sections, 8192)
	if prompt != "" || profile == nil || promptFailureKind(err) != "input_capacity" || !reflect.DeepEqual(promptOmitted(err), []string{"compaction_originals"}) {
		t.Fatalf("required original was not refused: %q %+v %v", prompt, profile, err)
	}
	data, saved := readInputPack(t, runner.run.Dir, "refuse-real")
	if !strings.Contains(data, "material qualification") || saved.Bytes != profile.Bytes {
		t.Fatal("refused input not persisted")
	}
}
