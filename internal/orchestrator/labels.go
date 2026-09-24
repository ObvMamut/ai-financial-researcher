package orchestrator

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/parse"
)

// labelDomains are the specialists that emit structured labels. Macro does not:
// a regime is one fact per market, not a fact about a name.
var labelDomains = map[string]bool{"news": true, "fundamentals": true, "quant": true, "sentiment": true}

// labelNoteMax bounds a label's free-text note. The note explains a veto; it is
// not a second report.
const labelNoteMax = 200

var labelDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// parseSpecialistLabels reads the `labels` array of one specialist's structured
// tail and returns the labels it may carry, keyed by shortlisted ticker, plus a
// note for every label it had to refuse.
//
// The labels exist because the literature and this system's own record agree
// on the one role an LLM measurably earns here: turning text into fixed-schema
// facts (Lopez-Lira & Tang; Chen, Kelly & Xiu), not ranking. So every field is
// a closed enum and every one has an unknown state, and the parse is built so
// that anything malformed collapses to unknown — never to a veto. A veto whose
// reason is outside the closed enum is refused outright: an open reason field
// is how a labeller turns back into a picker.
//
// A label is only kept for a name the domain could actually see. A name the run
// had no verified data for in this domain would be labelled from recollection,
// which is exactly what enforceSpecialistTail deletes a score for; a measured
// abstention is different — the domain read the evidence and found nothing
// directional — so its labels stand.
func parseSpecialistLabels(role, stdout string, shortlist []model.Candidate, ungrounded, abstained []string) (map[string]model.NameLabels, []string) {
	if !labelDomains[role] {
		return nil, nil
	}
	raw, ok := parse.LastJSONBlock(stdout)
	if !ok {
		return nil, nil
	}
	var tail map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &tail) != nil {
		return nil, nil
	}
	rawLabels, ok := tail["labels"]
	if !ok || len(rawLabels) == 0 || string(rawLabels) == "null" {
		return nil, nil
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(rawLabels, &entries); err != nil {
		return nil, []string{fmt.Sprintf("%s: unparseable `labels` array (%v) — every label read as unknown", role, err)}
	}

	onShortlist := make(map[string]bool, len(shortlist))
	for _, c := range shortlist {
		onShortlist[normTicker(c.Ticker)] = true
	}
	aliases := shortlistAliases(shortlist)
	blind := map[string]bool{}
	for _, t := range ungrounded {
		blind[normTicker(t)] = true
	}
	for _, t := range abstained {
		delete(blind, normTicker(t))
	}

	out := map[string]model.NameLabels{}
	var notes []string
	for _, e := range entries {
		t := normTicker(jsonString(e["ticker"]))
		if !onShortlist[t] {
			if canonical, ok := aliases[t]; ok && canonical != "" {
				t = canonical
			}
		}
		if t == "" || !onShortlist[t] {
			continue // names nothing the run asked about
		}
		if blind[t] {
			if v := jsonBool(e["veto"]); v != nil && *v {
				notes = append(notes, fmt.Sprintf("%s: vetoed %s with no verified data for it — label ignored", role, t))
			}
			continue
		}
		l := model.NameLabels{
			MoveDriver:      model.NormalizeMoveDriver(jsonString(e["move_driver"])),
			CorporateAction: jsonBool(e["corporate_action"]),
			Note:            truncateRunes(strings.TrimSpace(jsonString(e["note"])), labelNoteMax),
		}
		l.PendingBinaryEvent = parseBinaryEvent(e["pending_binary_event"])
		if v := jsonBool(e["veto"]); v != nil && *v {
			reason := model.NormalizeVetoReason(jsonString(e["veto_reason"]))
			if reason == "" {
				notes = append(notes, fmt.Sprintf("%s: vetoed %s with reason %q, which is not in the closed enum — not a veto",
					role, t, jsonString(e["veto_reason"])))
			} else {
				l.Veto, l.VetoReason = true, reason
			}
		}
		out[t] = l
	}
	return out, notes
}

// parseBinaryEvent accepts {present, date} or a bare boolean; anything else is
// unknown. A date is kept only when it is a well-formed calendar date.
func parseBinaryEvent(raw json.RawMessage) *model.LabelBinaryEvent {
	if len(raw) == 0 {
		return nil
	}
	if b := jsonBool(raw); b != nil {
		return &model.LabelBinaryEvent{Present: b}
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	ev := &model.LabelBinaryEvent{Present: jsonBool(obj["present"])}
	if d := strings.TrimSpace(jsonString(obj["date"])); labelDate.MatchString(d) {
		ev.Date = d
	}
	if ev.Present == nil && ev.Date == "" {
		return nil
	}
	return ev
}

// jsonBool decodes a JSON boolean, or the strings "true"/"false"; nil for
// anything else, which callers read as unknown.
func jsonBool(raw json.RawMessage) *bool {
	if len(raw) == 0 {
		return nil
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return &b
	}
	switch strings.ToLower(strings.TrimSpace(jsonString(raw))) {
	case "true":
		t := true
		return &t
	case "false":
		f := false
		return &f
	}
	return nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
