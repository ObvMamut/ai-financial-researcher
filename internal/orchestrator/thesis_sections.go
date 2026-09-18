package orchestrator

import (
	"fmt"
	"math"
	"strings"
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

// noSectionLimit is the limit assembleSections is called with at every
// production site today. This task's job is to make section sizes visible
// and named, not to start dropping them under a real budget — that overflow
// question belongs to later tasks (see task-11-context.md's "do not try to
// solve the overflow here"). Passing an effectively unbounded limit here
// keeps every prompt byte-for-byte identical to what the old concatenation
// produced; preparePrompt's existing len(prompt) > budget.InputBytes check,
// unchanged below, still rejects an oversized prompt exactly as before.
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
// len(text) exactly, always — there is never a residual byte attributable to
// nothing, which the deleted marker scan could not promise (the September 15
// audit's SNOW/OKTA/ORCL residuals turned out to be exactly the marker
// strings' own bytes, and five other labels had no map entry at all and were
// silently folded into whichever earlier marker preceded them).
//
// A mandatory overflow — the mandatory sections alone already exceed limit,
// so no amount of optional dropping can help — returns a promptCapacityError
// naming every mandatory section still present, so the caller can say which
// requirement did not fit rather than only that something did not.
func assembleSections(s []promptSection, limit int) (text string, sizes map[string]int, omitted []string, err error) {
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
			return "", nil, omitted, promptCapacityError{fmt.Errorf("mandatory prompt sections (%s) require %d bytes, over the %d byte limit", strings.Join(names, ", "), total, limit)}
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
