package orchestrator

import (
	"fmt"
	"math"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// promptSection is one independently named, independently measured piece of
// a prompt's data block. It replaces thesis_budget.go's deleted post-hoc
// marker-scanning function: a section's size that is only knowable after the
// whole string has been assembled cannot be budgeted, trimmed or refused by
// name, which is exactly what allocating a prompt across sections requires —
// and which Tasks 12 and 13 need to do with it.
//
// Body already carries whatever label text and leading separator the
// original inline concatenation used (e.g. "\nDossier hash: " + hash) — a
// section's Body is exactly the bytes it contributes to the assembled
// prompt, nothing more and nothing less, so assembleSections needs no
// separate notion of a separator between sections: concatenation is literal.
type promptSection struct {
	Name      string // stable identifier, e.g. "evidence", "request_results"
	Body      string
	Mandatory bool // mandatory sections are never trimmed; they fail the call instead
}

// noSectionLimit retains complete refused inputs for audit and unit fixtures.
const noSectionLimit = math.MaxInt

// assembleSections concatenates sections in order, each measured by its own
// exact byte length before assembly — never inferred afterward by searching
// the assembled string for marker text, the way the deleted function did.
// Optional sections are dropped from the tail (the highest remaining
// index) until the total fits limit; every drop is named, in drop order, in
// omitted.
//
// sizes reports only the sections that made it into text: a section that was
// dropped contributes nothing to the assembled prompt, so it must not keep
// contributing to its accounting either. That makes sum(sizes) equal
// len(text) exactly, always, unconditionally — there is never a residual
// byte attributable to nothing, which the deleted marker scan could not
// promise (the September 15 audit's SNOW/OKTA/ORCL residuals turned out to
// be exactly the marker strings' own bytes, and five other labels had no map
// entry at all and were silently folded into whichever earlier marker
// preceded them). Two sections sharing a Name would break that invariant the
// same way — the second would silently overwrite the first in sizes while
// text correctly kept both bodies — so a duplicate name is rejected outright
// rather than accumulated: callers (Task 12's per-company sections onto one
// board, in particular) must namespace their own names uniquely.
//
// A mandatory overflow — the mandatory sections alone already exceed limit,
// so no amount of optional dropping can help — returns a promptCapacityError
// naming every mandatory section still present, so the caller can say which
// requirement did not fit rather than only that something did not.
func assembleSections(s []promptSection, limit int) (text string, sizes map[string]int, omitted []string, err error) {
	seen := make(map[string]bool, len(s))
	for _, sec := range s {
		if seen[sec.Name] {
			return "", nil, nil, fmt.Errorf("assembleSections: duplicate section name %q — every section's Name must be unique, or its bytes would silently overwrite another's in sizes", sec.Name)
		}
		seen[sec.Name] = true
	}
	kept := append([]promptSection(nil), s...)
	total := 0
	for _, sec := range kept {
		total += len(sec.Body)
	}
	for total > limit {
		i := lastOptional(kept)
		if i < 0 {
			names := make([]string, len(kept))
			for j, sec := range kept {
				names[j] = sec.Name
			}
			return "", nil, omitted, promptCapacityError{fmt.Errorf("mandatory prompt sections (%s) require %d bytes, over the %d byte limit", strings.Join(names, ", "), total, limit), names}
		}
		total -= len(kept[i].Body)
		omitted = append(omitted, kept[i].Name)
		kept = append(kept[:i], kept[i+1:]...)
	}
	sizes = make(map[string]int, len(kept))
	var b strings.Builder
	b.Grow(total)
	for _, sec := range kept {
		sizes[sec.Name] = len(sec.Body)
		b.WriteString(sec.Body)
	}
	return b.String(), sizes, omitted, nil
}

// lastOptional returns the index of the last (highest-index) non-mandatory
// section — "the tail" sections are dropped from — or -1 when none remain.
func lastOptional(s []promptSection) int {
	for i := len(s) - 1; i >= 0; i-- {
		if !s[i].Mandatory {
			return i
		}
	}
	return -1
}

// singleSection wraps an already-assembled string as one atomic, mandatory
// section, for every call site that has no finer structure worth naming
// (schema repair, compaction, screening, event discovery, macro). It keeps
// "data" as the component name so a call that never decomposed still
// measures exactly as it always did — profile.Components has always had a
// "data" entry for these calls, and still does.
func singleSection(body string) []promptSection {
	return []promptSection{{Name: "data", Body: body, Mandatory: true}}
}

// challengeSections appends previous_challenge to base()'s sections for a
// challenge call — both the initial challenge("") and the final
// challenge("-final") (after a revision) share this one construction.
//
// previous_challenge is mandatory. agents/thesis-challenger.md instructs the
// model unconditionally: "On final review judge whether the previous
// material issues were actually resolved by the new evidence" — and nothing
// in Go backstops that if the model never sees it: validateReviewConsistency
// (thesis.go) takes only the current dossier and the current review, never
// the previous challenge, so nothing downstream would notice a "supported"
// verdict that never re-examined what the prior round found wrong. That is
// the same principle reviewPlans applies to all six of its own sections
// (idea/dossier/evidence are exactly what that call reviews); previous
// material issues are exactly what a final challenge reviews. It is also
// live, not hypothetical: ORCL's real challenge-final overflow (see the
// comment above challenge's call site in thesis.go) carried a 9,477-byte
// previous_challenge — a droppable previous_challenge would have let the
// first real budget "fix" that overflow by silently deleting the one
// section the call exists to use, trading a loud failure for a silent
// well-formed "supported" verdict with nothing left to catch it.
func challengeSections(base []promptSection, previous model.ThesisChallenge) []promptSection {
	return append(base, previousChallengeSection(previous))
}

// previousChallengeSection is challengeSections' own appended section,
// pulled out so a caller that must know its size BEFORE challengeSections
// runs — thesis.go's base(), sizing the evidence section for whatever this
// call appends afterward — computes the exact same bytes challengeSections
// will actually append, from one definition, rather than a hand-copied
// second construction that could drift from it.
func previousChallengeSection(previous model.ThesisChallenge) promptSection {
	return promptSection{Name: "previous_challenge", Mandatory: true, Body: "\nPrevious challenge:\n" + jsonText(previous)}
}
