package orchestrator

import (
	"encoding/json"
	"os"
	"testing"
)

// sep15CapacityCase freezes one company's byte-budget outcome from the
// September 15 thesis run (runs/2026-09-15T17-00-30), before the plan's
// capacity fixes. "response" cases are round outputs that failed
// responseCapacity on raw Stdout bytes; RawBytes is the compaction-round
// file's own size and CompactBytes is what Go's encoding/json.Compact
// produces from the same ```json-fenced payload (parse.LastJSONBlock). "input"
// cases are prompts that never dispatched a subprocess at all — preparePrompt
// rejected them on len(prompt) before any model call — so they carry only
// RawBytes; there was never a response to compact.
//
// Later tasks load this manifest to test the fixes this baseline exists to
// measure against (Task 8's normalization pass, Task 9's measured allowance).
// Do not mutate the recorded byte counts; they are captured measurements, not
// tunable expectations.
type sep15CapacityCase struct {
	Ticker       string
	RawBytes     int
	CompactBytes int
	Limit        int
	Kind         string // "response" or "input"
}

type sep15CapacityManifest struct {
	Provenance    string `json:"provenance"`
	ResponseLimit int    `json:"response_limit"`
	InputLimit    int    `json:"input_limit"`
	Cases         []struct {
		Ticker       string `json:"ticker"`
		Kind         string `json:"kind"`
		RawBytes     int    `json:"raw_bytes"`
		CompactBytes int    `json:"compact_bytes"`
	} `json:"cases"`
}

// loadSep15CapacityCases reads testdata/sep15-capacity/manifest.json and
// resolves each case's Limit from the manifest's shared response/input
// budgets rather than repeating it per case.
func loadSep15CapacityCases(t *testing.T) []sep15CapacityCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/sep15-capacity/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m sep15CapacityManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Provenance == "" {
		t.Fatal("capacity manifest is missing its provenance record")
	}
	out := make([]sep15CapacityCase, 0, len(m.Cases))
	for _, c := range m.Cases {
		limit := m.ResponseLimit
		if c.Kind == "input" {
			limit = m.InputLimit
		}
		out = append(out, sep15CapacityCase{Ticker: c.Ticker, RawBytes: c.RawBytes, CompactBytes: c.CompactBytes, Limit: limit, Kind: c.Kind})
	}
	return out
}

// capacityCase returns the one named case from the manifest, failing the test
// if the ticker is not present.
func capacityCase(t *testing.T, ticker string) sep15CapacityCase {
	t.Helper()
	for _, c := range loadSep15CapacityCases(t) {
		if c.Ticker == ticker {
			return c
		}
	}
	t.Fatalf("no capacity case for %s in testdata/sep15-capacity/manifest.json", ticker)
	return sep15CapacityCase{}
}

// TestSep15CapacityManifestReconciles checks the manifest against the counts
// the September 15 audit reconciled against metadata.json: six response-
// capacity failures on the compaction output and three zero-attempt
// input-capacity failures, all of them over their respective budget on raw
// bytes (that overflow is the entire reason they are in this fixture).
func TestSep15CapacityManifestReconciles(t *testing.T) {
	cases := loadSep15CapacityCases(t)
	if len(cases) != 9 {
		t.Fatalf("len(cases) = %d, want 9", len(cases))
	}
	response, input := 0, 0
	for _, c := range cases {
		if c.RawBytes <= c.Limit {
			t.Errorf("%s: raw_bytes %d does not exceed its own limit %d", c.Ticker, c.RawBytes, c.Limit)
		}
		switch c.Kind {
		case "response":
			response++
			if c.Limit != 20480 {
				t.Errorf("%s: response limit = %d, want 20480", c.Ticker, c.Limit)
			}
		case "input":
			input++
			if c.CompactBytes != 0 {
				t.Errorf("%s: input case unexpectedly carries a compact byte count", c.Ticker)
			}
			if c.Limit != 98304 {
				t.Errorf("%s: input limit = %d, want 98304", c.Ticker, c.Limit)
			}
		default:
			t.Errorf("%s: unknown case kind %q", c.Ticker, c.Kind)
		}
	}
	if response != 6 || input != 3 {
		t.Fatalf("response=%d input=%d, want 6 and 3", response, input)
	}
}

// Baseline: describes the defect, not the fix. Task 8 inverts this.
func TestBaselineRawByteBudgetRejectsLLY(t *testing.T) {
	c := capacityCase(t, "LLY")
	if c.RawBytes <= c.Limit {
		t.Fatalf("fixture no longer reproduces the raw-byte overflow")
	}
	if c.CompactBytes > c.Limit {
		t.Fatalf("LLY must be recoverable by normalization alone: %d > %d", c.CompactBytes, c.Limit)
	}
}
