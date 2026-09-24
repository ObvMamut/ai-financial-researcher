package orchestrator

import (
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func labelShortlist() []model.Candidate {
	return []model.Candidate{
		{Ticker: "NVDA", Name: "NVIDIA Corporation", Bias: model.BiasBullish},
		{Ticker: "JPM", Name: "JPMorgan Chase & Co.", Bias: model.BiasBullish},
		{Ticker: "NKE", Name: "Nike Inc.", Bias: model.BiasBearish},
		{Ticker: "7203.T", Name: "Toyota Motor Corporation", Bias: model.BiasBullish},
	}
}

func TestLabelsParseIntoTheClosedSchema(t *testing.T) {
	report := "prose\n\n```json\n" + `{"domain":"news","scores":[],"missing":[],"labels":[
  {"ticker":"NVDA","move_driver":"Earnings","pending_binary_event":{"present":true,"date":"2026-10-01"},"corporate_action":false,"veto":true,"veto_reason":"binary_event_inside_window","note":"prints inside the window"},
  {"ticker":"JPM","move_driver":"rotation","pending_binary_event":{"present":"maybe","date":"next week"},"corporate_action":"yes"},
  {"ticker":"Nike","veto":true,"veto_reason":"vibes"},
  {"ticker":"ZZZZ","veto":true,"veto_reason":"data_error"},
  {"ticker":"7203.T","veto":true,"veto_reason":"data_error"}
]}` + "\n```\n"
	labels, notes := parseSpecialistLabels("news", report, labelShortlist(), []string{"7203.T"}, nil)

	nv, ok := labels["NVDA"]
	if !ok || !nv.Veto || nv.VetoReason != model.VetoBinaryEventInsideWindow {
		t.Fatalf("NVDA veto lost: %+v", nv)
	}
	if nv.MoveDriver != "earnings" || nv.PendingBinaryEvent == nil || nv.PendingBinaryEvent.Date != "2026-10-01" ||
		nv.PendingBinaryEvent.Present == nil || !*nv.PendingBinaryEvent.Present {
		t.Errorf("NVDA labels not normalised: %+v", nv)
	}
	if nv.CorporateAction == nil || *nv.CorporateAction {
		t.Errorf("NVDA corporate_action = %v, want an explicit false", nv.CorporateAction)
	}

	// Anything outside the schema is unknown, and unknown is never a veto.
	jpm := labels["JPM"]
	if jpm.MoveDriver != model.MoveDriverUnknown || jpm.PendingBinaryEvent != nil || jpm.CorporateAction != nil || jpm.Veto {
		t.Errorf("malformed JPM labels must read as unknown: %+v", jpm)
	}
	// A company name resolves to its ticker; a reason outside the enum is refused.
	if nke, ok := labels["NKE"]; !ok || nke.Veto {
		t.Errorf("NKE: an open-reason veto must not stand: %+v (present %v)", nke, ok)
	}
	if !strings.Contains(strings.Join(notes, "\n"), `"vibes"`) {
		t.Errorf("the refused veto is not reported: %v", notes)
	}
	if _, ok := labels["ZZZZ"]; ok {
		t.Error("an off-shortlist label was kept")
	}
	// No verified data in this domain for the name: its veto is recollection.
	if _, ok := labels["7203.T"]; ok {
		t.Error("a label for a name the domain could not see was kept")
	}
}

func TestAMeasuredAbstentionKeepsItsLabels(t *testing.T) {
	report := "```json\n" + `{"domain":"sentiment","scores":[],"missing":["NKE"],"labels":[{"ticker":"NKE","veto":true,"veto_reason":"fraud_or_litigation_shock"}]}` + "\n```"
	labels, _ := parseSpecialistLabels("sentiment", report, labelShortlist(), []string{"NKE"}, []string{"NKE"})
	if !labels["NKE"].Veto {
		t.Errorf("sentiment read NKE's evidence and abstained on direction; its veto must stand: %+v", labels)
	}
}

func TestMissingOrBrokenLabelsAreUnknownNotAnError(t *testing.T) {
	noLabels := "```json\n{\"domain\":\"quant\",\"scores\":[],\"missing\":[]}\n```"
	if l, n := parseSpecialistLabels("quant", noLabels, labelShortlist(), nil, nil); len(l) != 0 || len(n) != 0 {
		t.Errorf("an absent labels array must be silent: %v %v", l, n)
	}
	broken := "```json\n{\"domain\":\"quant\",\"labels\":{\"NVDA\":\"veto\"}}\n```"
	if l, n := parseSpecialistLabels("quant", broken, labelShortlist(), nil, nil); len(l) != 0 || len(n) != 1 {
		t.Errorf("a malformed labels array must be reported once and yield nothing: %v %v", l, n)
	}
	if l, _ := parseSpecialistLabels("macro", broken, labelShortlist(), nil, nil); l != nil {
		t.Error("macro does not label names")
	}
}
