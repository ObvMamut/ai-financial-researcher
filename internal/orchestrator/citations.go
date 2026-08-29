package orchestrator

import (
	"regexp"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

// sourceTagRe matches a citation tag such as [source:reuters.com 2026-08-25].
// The domain is the first token; the trailing date is optional because agents
// omit it often enough that a stricter pattern would let tags slip through.
var sourceTagRe = regexp.MustCompile(`\[source:\s*([^\s\]]*)[^\]]*\]`)

// engineWebSearch reports whether an engine can search the web mid-call.
//
// The CLI engines (agy, claude) run a full agent loop with a built-in search
// tool. The OpenAI-compatible HTTP engine sends only model+messages — no tools,
// no search — so an agent told to "search the web" on it has no way to comply
// except by inventing sources. This is the fact the prompt must state and the
// post-check must enforce.
func engineWebSearch(engine model.CLI) bool {
	switch engine {
	case model.CLIApi:
		return false
	default:
		return true
	}
}

// scrubCitations rewrites `[source:domain …]` tags that the engine cannot have
// earned, returning the cleaned report and the fabricated domains.
//
// On a search-less engine any tag whose domain is not vouched for by the run's
// own verified data is fabricated by construction: nothing in the pipeline ever
// fetched that page. Rewriting the tag to `[unverified]` keeps the claim visible
// — the Chief Analyst can still weigh it and discount it — while stripping the
// false provenance that made it look checkable. The returned domain list is what
// marks the report degraded.
func scrubCitations(report string, citable map[string]bool) (cleaned string, fabricated []string) {
	seen := make(map[string]bool)
	cleaned = sourceTagRe.ReplaceAllStringFunc(report, func(tag string) string {
		m := sourceTagRe.FindStringSubmatch(tag)
		raw := strings.TrimSpace(m[1])
		if d := marketdata.NormalizeDomain(raw); d != "" && citable[d] {
			return tag
		}
		if raw == "" {
			raw = "(empty)"
		}
		if !seen[raw] {
			seen[raw] = true
			fabricated = append(fabricated, raw)
		}
		return "[unverified]"
	})
	sort.Strings(fabricated)
	return cleaned, fabricated
}
