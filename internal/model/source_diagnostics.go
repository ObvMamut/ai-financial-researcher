package model

import "fmt"

// SourceDiagnostic records an observation at the retrieval boundary. Reason and
// disposition are stable codes; Message is redacted explanatory text. Historical
// artifacts with no diagnostics remain unclassified.
type SourceDiagnostic struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Ticker      string `json:"ticker,omitempty"`
	Region      string `json:"region,omitempty"`
	Stage       string `json:"stage"`
	Reason      string `json:"reason"`
	Disposition string `json:"disposition"` // failed, withheld, expected, context, omitted
	Message     string `json:"message"`
}

// SourceDiagnosticsLine is shared by text and TUI output. Detailed reasons and
// listing regions remain in metadata; repeat observations count once by ID.
func SourceDiagnosticsLine(meta *RunMeta) string {
	if meta == nil || len(meta.SourceDiagnostics) == 0 {
		return ""
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	for _, d := range meta.SourceDiagnostics {
		if !seen[d.ID] {
			counts[d.Disposition]++
			seen[d.ID] = true
		}
	}
	line := fmt.Sprintf("Sources: %d failed · %d withheld · %d expected coverage gaps · %d context only", counts["failed"], counts["withheld"], counts["expected"], counts["context"])
	if omitted := counts["omitted"]; omitted > 0 {
		// Latent today — no call site emits this disposition yet — but the
		// struct's own doc comment names it as legal, so the tally must not
		// silently drop it the moment one does.
		line += fmt.Sprintf(" · %d omitted", omitted)
	}
	return line
}
