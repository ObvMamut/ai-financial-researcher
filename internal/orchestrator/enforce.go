package orchestrator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/model"
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
	// Neutral lists the shortlisted tickers the agent scored with a `neutral`
	// bias on evidence it did have. Nothing is deleted — a genuine standoff is a
	// legitimate verdict — but it is the most expensive one available: sign 0
	// contributes nothing to the weighted score while still consuming the
	// domain's full weight, so a neutral vote costs more than a missing one and
	// blocks the coverage cap relief a gap would earn.
	//
	// It is recorded because it was invisible. On 2026-09-01 news scored AMGN
	// `neutral 0` while its own paragraph named a UK regulator suspending a
	// marketed drug that morning, reported by three outlets; that cost the idea
	// 17 points of base score and nothing in the run distinguished it from a
	// name news simply had nothing on.
	Neutral []string
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
	// Scored is how many names the tail scored before any were removed. Without
	// it the lists above have no denominator, and "six corrected" reads the same
	// whether the domain scored six names or forty.
	Scored int
	// Renamed lists the "wrote the company, not the symbol" resolutions, as
	// "KAKAO→035720.KS". These are not corrections — the score stands, under the
	// ticker the run asked about — but they are recorded because they used to be
	// deletions. On 2026-09-03 macro scored twelve names and wrote OCBC, KAKAO
	// and MEDIATEK for O39.SI, 035720.KS and 2454.TW; all three were struck as
	// off-shortlist, and those are exactly the names that then shipped as ideas
	// 3 and 5 on a single domain each.
	Renamed []string
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
func enforceSpecialistTail(role, stdout string, ungrounded, abstained []string, shortlist []model.Candidate) (string, enforcement, error) {
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
	for _, c := range shortlist {
		onShortlist[normTicker(c.Ticker)] = true
	}
	aliases := shortlistAliases(shortlist)
	renamed := map[string]bool{}
	// resolve turns whatever the agent wrote into the ticker the run asked
	// about, where the two name the same company.
	resolve := func(raw string) string {
		t := normTicker(raw)
		if t == "" || onShortlist[t] {
			return t
		}
		if canonical, ok := aliases[t]; ok && canonical != "" {
			renamed[t+"→"+canonical] = true
			return canonical
		}
		return t
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
			if n := resolve(m); n != "" {
				declared[n] = true
			}
		}
	}

	kept := make([]map[string]json.RawMessage, 0, len(scores))
	corrected := map[string]bool{}
	offShortlist := map[string]bool{}
	selfContradicted := map[string]bool{}
	neutral := map[string]bool{}
	for _, s := range scores {
		t := resolve(jsonString(s["ticker"]))
		if t == "" {
			// A score with no ticker names nothing; it cannot be attributed.
			continue
		}
		res.Scored++
		if normTicker(jsonString(s["ticker"])) != t {
			// Rewrite the tail to the symbol the rest of the pipeline keys on.
			// Everything downstream — base scores, the Chief's table, the risk
			// gate — looks names up by ticker, so a score under a company name
			// is invisible to all of it even once it is no longer deleted.
			if raw, err := json.Marshal(t); err == nil {
				s["ticker"] = raw
			}
		}
		switch {
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
			if strings.EqualFold(strings.TrimSpace(jsonString(s["bias"])), "neutral") {
				neutral[t] = true
			}
			kept = append(kept, s)
		}
	}
	res.Neutral = sortedKeys(neutral)
	res.Renamed = sortedKeys(renamed)
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
	if !res.Any() && len(res.Renamed) == 0 && sameStringSet(missing, tail["missing"]) {
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
	disowned := map[string]bool{}
	for _, t := range e.SelfContradicted {
		disowned[t] = true
	}
	var noData, stoodDown, selfDisclaimed []string
	for _, t := range removed {
		switch {
		case abstained[t]:
			stoodDown = append(stoodDown, t)
		case disowned[t]:
			// The run had data and the agent had a score; the agent's own
			// `missing` array is what took the score away.
			selfDisclaimed = append(selfDisclaimed, t)
		default:
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
	// A self-contradicted name gets its own sentence for the same reason an
	// abstention does. The run had data for it; the report scored it and *also*
	// listed it as missing, so its own author disclaimed the number. Saying "this
	// run had no verified data for it" — which is where these names used to land —
	// is a false statement in the one block whose whole job is keeping the Chief's
	// picture of the evidence accurate.
	if len(selfDisclaimed) > 0 {
		b.WriteString("The ")
		b.WriteString(role)
		b.WriteString(" report placed ")
		b.WriteString(strings.Join(selfDisclaimed, ", "))
		b.WriteString(" in *both* its `scores` and its own `missing` array, disclaiming the number it had just written, so ")
		b.WriteString(scoreOrScores(len(selfDisclaimed)))
		b.WriteString(" ")
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
// shortlistAliases maps the ways a specialist might name a shortlisted company
// onto its ticker.
//
// Enforcement compares what the agent wrote against the shortlist by symbol and
// deletes anything that does not match, which is right for a hallucinated ticker
// and wrong for a correct verdict filed under the company's name. Macro wrote
// OCBC, KAKAO and MEDIATEK on 2026-09-03 — the names in its own prose, and in
// the shortlist block it was handed, which carries `name` beside `ticker` — and
// lost all three scores. Ideas 3 and 5 then shipped on quant alone.
//
// Three forms, each only registered when it collides with nothing else on the
// shortlist and is not already a ticker: the full name, its first word, and its
// initials. "Oversea-Chinese Banking Corporation" yields OVERSEACHINESEBANKING,
// OVERSEA and OCBC; "Kakao Corp." yields KAKAO; "MediaTek Inc." yields MEDIATEK.
// An ambiguous key is registered as empty rather than dropped, so a later name
// producing the same key cannot claim it either.
func shortlistAliases(shortlist []model.Candidate) map[string]string {
	tickers := make(map[string]bool, len(shortlist))
	for _, c := range shortlist {
		tickers[normTicker(c.Ticker)] = true
	}
	out := map[string]string{}
	add := func(key, ticker string) {
		if key == "" || tickers[key] {
			return
		}
		if prev, seen := out[key]; seen && prev != ticker {
			out[key] = "" // ambiguous: no name may claim it
			return
		}
		out[key] = ticker
	}
	for _, c := range shortlist {
		words := entityWords(c.Name)
		if len(words) == 0 {
			continue
		}
		t := normTicker(c.Ticker)
		add(strings.ToUpper(strings.Join(words, "")), t)
		add(strings.ToUpper(words[0]), t)
		if len(words) > 1 {
			var initials strings.Builder
			for _, w := range words {
				initials.WriteByte(w[0])
			}
			add(strings.ToUpper(initials.String()), t)
		}
	}
	return out
}

// entityWords splits a company name into its words, on whitespace and on the
// punctuation that separates them. Hyphens split, because "Oversea-Chinese" is
// two words in the initialism its own bank uses.
func entityWords(name string) []string {
	return strings.FieldsFunc(name, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		}
		return true
	})
}

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
