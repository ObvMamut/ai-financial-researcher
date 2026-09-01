package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/parse"
)

// enforcement records what the app had to correct in one specialist's
// structured tail before handing the report to the Chief Analyst.
type enforcement struct {
	// Corrected lists shortlisted tickers the agent scored without verified
	// data. Their scores were deleted and the names moved to `missing`.
	Corrected []string
	// OffShortlist lists tickers the agent scored that were never requested —
	// hallucinated symbols. Their scores were deleted and nothing was added to
	// `missing`, because the run never asked about them.
	OffShortlist []string
	// SelfContradicted lists tickers the agent put in *both* `scores` and
	// `missing`. Its own report disclaims the score, so the score goes.
	SelfContradicted []string
	// Abstained is the subset of Corrected whose evidence was present but not
	// directional — sentiment looking at real filings and a real option chain and
	// finding nothing a direction can be built on.
	//
	// It is tracked separately only so the notice can say the true thing. Both
	// sets lose their scores identically, but telling the Chief "this run had no
	// verified sentiment data for AMGN" when the run had two months of Form 4s
	// and a full chain is a false statement in the one place this pipeline works
	// hardest to keep honest.
	Abstained []string
}

// Any reports whether anything at all had to be corrected.
func (e enforcement) Any() bool {
	return len(e.Corrected) > 0 || len(e.OffShortlist) > 0 || len(e.SelfContradicted) > 0
}

// removed is every ticker whose score was deleted, sorted — the set the prose
// note warns the Chief about.
func (e enforcement) removed() []string {
	seen := map[string]bool{}
	for _, t := range e.Corrected {
		seen[t] = true
	}
	for _, t := range e.SelfContradicted {
		seen[t] = true
	}
	return sortedKeys(seen)
}

// enforceSpecialistTail rewrites a specialist report so its structured tail
// matches the data the run actually assembled.
//
// Coverage used to be advisory: `overclaimedCoverage` logged a warning when an
// agent scored a name it had no data for, and the invented score flowed into
// synthesis regardless. On 2026-08-28 every specialist reported `"missing": []`
// against near-zero real coverage, and the run shipped as `outcome: complete`
// with the highest confidence of any recent run — the pipeline was rewarding
// confabulation. Computed coverage is the authority now: a score for a ticker
// the domain could not see is deleted, and the ticker is unioned into `missing`.
//
// The convention this establishes, and that the personas and output-schema doc
// state: a ticker appears in `missing` if and only if the domain had no data for
// it, and in `scores` if and only if it was evaluated on data. Nothing is scored
// at strength 0 or 1 to mean "no data", and nothing is silently omitted.
//
// The report is rewritten in place — same prose, same fences — so the artifact
// on disk is exactly what the Chief Analyst read.
//
// An unparseable or absent tail returns an error: a refusal, a truncated
// response, or free prose is not a usable domain report, and treating non-empty
// stdout as success is how those reached synthesis unnoticed.
func enforceSpecialistTail(role, stdout string, ungrounded, abstained, shortlist []string) (string, enforcement, error) {
	var res enforcement

	raw, ok := parse.LastJSONBlock(stdout)
	if !ok {
		return stdout, res, fmt.Errorf("no structured JSON tail in %s report", role)
	}

	// Decode into an ordered generic form: the tail carries fields this app does
	// not model (per-domain extras the personas ask for), and re-marshalling
	// through a typed struct would silently drop them.
	var tail map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &tail); err != nil {
		return stdout, res, fmt.Errorf("unparseable JSON tail in %s report: %w", role, err)
	}

	var scores []map[string]json.RawMessage
	if rawScores, ok := tail["scores"]; ok && len(rawScores) > 0 {
		if err := json.Unmarshal(rawScores, &scores); err != nil {
			return stdout, res, fmt.Errorf("unparseable `scores` array in %s report: %w", role, err)
		}
	}

	onShortlist := make(map[string]bool, len(shortlist))
	for _, t := range shortlist {
		onShortlist[normTicker(t)] = true
	}
	isUngrounded := make(map[string]bool, len(ungrounded))
	for _, t := range ungrounded {
		isUngrounded[normTicker(t)] = true
	}
	isAbstention := make(map[string]bool, len(abstained))
	for _, t := range abstained {
		isAbstention[normTicker(t)] = true
	}

	// What the agent itself said it had no data for. Read before the scores are
	// walked, because a name in both arrays is a score its own author disclaims.
	declared := map[string]bool{}
	if rawMissing, ok := tail["missing"]; ok && len(rawMissing) > 0 {
		var missing []string
		if err := json.Unmarshal(rawMissing, &missing); err != nil {
			return stdout, res, fmt.Errorf("unparseable `missing` array in %s report: %w", role, err)
		}
		for _, m := range missing {
			if n := normTicker(m); n != "" {
				declared[n] = true
			}
		}
	}

	kept := make([]map[string]json.RawMessage, 0, len(scores))
	corrected := map[string]bool{}
	offShortlist := map[string]bool{}
	selfContradicted := map[string]bool{}
	for _, s := range scores {
		t := normTicker(jsonString(s["ticker"]))
		switch {
		case t == "":
			// A score with no ticker names nothing; it cannot be attributed.
			continue
		case !onShortlist[t]:
			offShortlist[t] = true
		case isUngrounded[t]:
			corrected[t] = true
		case declared[t]:
			// Scored *and* declared missing. Enforcement only ever checked the
			// agent against the app's computed coverage, never against its own
			// report, so on 2026-09-01 the sentiment tail scored ORCL "neutral,
			// strength 3" and listed ORCL in `missing` — both survived into the
			// artifact the Chief read, and the base score counted 15% of the
			// domain weight as covered on the strength of a score the report
			// disowned. The stated invariant below says a ticker is in one array
			// or the other; this makes it true.
			selfContradicted[t] = true
		default:
			kept = append(kept, s)
		}
	}
	res.Corrected = sortedKeys(corrected)
	res.OffShortlist = sortedKeys(offShortlist)
	res.SelfContradicted = sortedKeys(selfContradicted)
	abstentions := map[string]bool{}
	for t := range corrected {
		if isAbstention[t] {
			abstentions[t] = true
		}
	}
	res.Abstained = sortedKeys(abstentions)

	// `missing` is the union of what the agent declared and what it wrongly
	// scored, normalized and deduped. Off-shortlist names are excluded: the run
	// never asked about them, so they are not gaps in this domain's coverage.
	for t := range corrected {
		declared[t] = true
	}
	missing := sortedKeys(declared)

	// Nothing to correct: hand the report back byte-identical.
	if !res.Any() && sameStringSet(missing, tail["missing"]) {
		return stdout, res, nil
	}

	scoresJSON, err := json.Marshal(kept)
	if err != nil {
		return stdout, res, fmt.Errorf("re-marshal %s scores: %w", role, err)
	}
	missingJSON, err := json.Marshal(missing)
	if err != nil {
		return stdout, res, fmt.Errorf("re-marshal %s missing: %w", role, err)
	}
	tail["scores"] = scoresJSON
	tail["missing"] = missingJSON

	rewritten, err := json.MarshalIndent(tail, "", "  ")
	if err != nil {
		return stdout, res, fmt.Errorf("re-marshal %s tail: %w", role, err)
	}
	out, ok := parse.ReplaceLastJSONBlock(stdout, string(rewritten))
	if !ok {
		return stdout, res, fmt.Errorf("could not splice the corrected tail into the %s report", role)
	}
	return withRemovalNote(out, role, res), res, nil
}

// withRemovalNote prepends a warning naming the tickers whose scores were
// deleted, so the Chief Analyst reads the correction rather than only its
// silent effect.
//
// Deleting the score was never enough. The macro report on 2026-09-01 had its
// scores for 8035.T, 9984.T, BMW.DE and STLAM.MI removed as ungrounded, and the
// numbers duly vanished — but its *paragraphs* about those names stayed, and the
// Chief read them and acted:
//
//	"base 40 −3: report contradiction — Macro prose, 'shorted against ^N225
//	 mean-reverting, which is a headwind for the bearish call,' while Macro
//	 scored the name `missing`"
//
// It said plainly what it was doing, and did it twice, moving two of the five
// confidences. So the guarantee that an ungrounded domain contributes nothing
// was not real; it was routed around through prose.
//
// The note does not gag the prose — some of what macro leaned on there is
// verified regime data the Chief receives separately, and deleting an agent's
// reasoning while keeping its conclusions would be worse. It labels it, which is
// what lets the Chief tell context from evidence.
// Abstentions are named separately and truthfully. Both kinds of removal have
// the same effect on the score, but they are opposite statements about the run:
// one says the data was never there, the other says it was there and said
// nothing. Reporting an abstention as absent data would put a false sentence in
// front of the Chief, in the one block whose whole purpose is to keep the Chief's
// picture of the evidence accurate.
func withRemovalNote(report, role string, e enforcement) string {
	removed := e.removed()
	if len(removed) == 0 {
		return report
	}
	abstained := map[string]bool{}
	for _, t := range e.Abstained {
		abstained[t] = true
	}
	var noData, stoodDown []string
	for _, t := range removed {
		if abstained[t] {
			stoodDown = append(stoodDown, t)
		} else {
			noData = append(noData, t)
		}
	}

	var b strings.Builder
	b.WriteString("> **Enforcement notice (added by the app, not by the ")
	b.WriteString(role)
	b.WriteString(" agent).** ")
	if len(noData) > 0 {
		b.WriteString("This run had no verified ")
		b.WriteString(role)
		b.WriteString(" data for ")
		b.WriteString(strings.Join(noData, ", "))
		b.WriteString(", so ")
		b.WriteString(scoreOrScores(len(noData)))
		b.WriteString(" ")
	}
	if len(stoodDown) > 0 {
		b.WriteString("This run *did* have ")
		b.WriteString(role)
		b.WriteString(" data for ")
		b.WriteString(strings.Join(stoodDown, ", "))
		b.WriteString(", but the app's computed verdict read it as carrying no direction, so ")
		b.WriteString(scoreOrScores(len(stoodDown)))
		b.WriteString(" Treat ")
		b.WriteString(thatOrThose(len(stoodDown)))
		b.WriteString(" as evidence of no signal, which is not the same as absent evidence. ")
	}
	b.WriteString("Any prose in this report about ")
	b.WriteString(thatOrThose(len(removed)))
	b.WriteString(" is unscored context: it is not evidence from this domain and must not be used to adjust a base score. ")
	b.WriteString("Where it appeals to market regime, the verified regime block is the authority.\n\n")
	b.WriteString(report)
	return b.String()
}

func scoreOrScores(n int) string {
	if n == 1 {
		return "its score below was deleted and the name was moved to `missing`."
	}
	return "their scores below were deleted and the names were moved to `missing`."
}

func thatOrThose(n int) string {
	if n == 1 {
		return "that name"
	}
	return "those names"
}

// normTicker upper-cases and trims a symbol for comparison.
func normTicker(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// jsonString decodes a JSON string field, returning "" for anything else.
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sameStringSet reports whether raw already decodes to exactly want, so an
// already-honest report can be returned untouched.
func sameStringSet(want []string, raw json.RawMessage) bool {
	var have []string
	if len(raw) > 0 && json.Unmarshal(raw, &have) != nil {
		return false
	}
	if len(have) != len(want) {
		return false
	}
	sort.Strings(have)
	for i := range want {
		if have[i] != want[i] {
			return false
		}
	}
	return true
}
