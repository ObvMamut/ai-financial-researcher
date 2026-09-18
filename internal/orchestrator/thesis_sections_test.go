package orchestrator

import (
	"errors"
	"reflect"
	"strings"
	"testing"
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
