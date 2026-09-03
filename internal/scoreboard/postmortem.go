package scoreboard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PostMortemFile is the name of the stored lessons, written under the data
// directory and copied into each run beside the calibration it was drawn from.
const PostMortemFile = "postmortem.json"

const (
	// MinClosedForPostMortem is the number of closed trades below which no
	// lessons are drawn at all. It matches MinClosedForFeedback for the same
	// reason: below it the record cannot distinguish a 45% cell from a 55% one,
	// and a Chief told otherwise spends real adjustment band on noise.
	MinClosedForPostMortem = MinClosedForFeedback
	// MinCellN is the smallest cell a lesson may be drawn from. Four trades is
	// not a tendency; the persona says so and this enforces it.
	MinCellN = 5
	// MaxLessons bounds the block. It sits in a prompt beside five specialist
	// reports and a base-score table, and a page of retrospective would displace
	// the evidence it is meant to qualify.
	MaxLessons = 8
)

// Lesson is one finding about the pipeline's own record: a named cell, its
// count, what the record shows and what to do differently.
type Lesson struct {
	Cell    string `json:"cell"`
	N       int    `json:"n"`
	Finding string `json:"finding"`
	Action  string `json:"action"`
}

// WeightSuggestion is advisory and never applied. It surfaces in the run log for
// a human to act on, because a pipeline that silently re-weights itself on its
// own reading of its own record has no control arm left.
type WeightSuggestion struct {
	Domain    string `json:"domain"`
	Direction string `json:"direction"`
	Reason    string `json:"reason"`
}

// PostMortem is the parsed, enforced output of the post-mortem agent.
type PostMortem struct {
	ComputedAt string   `json:"computed_at"`
	NClosed    int      `json:"n_closed"`
	Lessons    []Lesson `json:"lessons"`
	// Rejected lists lessons that were deleted, and why. A post-mortem is the
	// easiest output in this system to invent — it is a narrative about the
	// system's own performance, written by the system — so the deletions are
	// recorded rather than silently applied, exactly as they are for a
	// specialist's structured tail.
	Rejected           []string           `json:"rejected,omitempty"`
	WeightSuggestions  []WeightSuggestion `json:"weight_suggestions,omitempty"`
	AttributionSummary []string           `json:"attribution,omitempty"`
}

// postMortemTail is the shape the agent emits.
type postMortemTail struct {
	NClosed           int                `json:"n_closed"`
	Lessons           []Lesson           `json:"lessons"`
	WeightSuggestions []WeightSuggestion `json:"weight_suggestions"`
}

var weightDomains = map[string]bool{
	"quant": true, "news": true, "fundamentals": true, "sentiment": true, "macro": true,
}

// ParsePostMortem reads the agent's JSON tail and enforces it against the
// attribution it was given.
//
// The rules are the ones the persona states, applied rather than requested:
// a lesson must name a cell that actually exists in the table, its count must
// match the table's, the cell must clear MinCellN, and there is a hard ceiling
// on how many survive. A weight suggestion must name a real domain and a real
// direction. Everything deleted is recorded with its reason.
func ParsePostMortem(tailJSON string, a *Attribution) (*PostMortem, error) {
	var t postMortemTail
	if err := json.Unmarshal([]byte(tailJSON), &t); err != nil {
		return nil, fmt.Errorf("post-mortem tail: %w", err)
	}
	pm := &PostMortem{
		ComputedAt: time.Now().UTC().Format(time.RFC3339),
		NClosed:    a.closedCount(),
	}
	cells := a.Cells(MinCellN)
	counts := a.cellCounts()

	for _, l := range t.Lessons {
		key := strings.ToLower(strings.TrimSpace(l.Cell))
		switch {
		case key == "" || l.Finding == "":
			pm.Rejected = append(pm.Rejected, fmt.Sprintf("a lesson with no cell or no finding: %+v", l))
		case !cells[key]:
			// The one rule that matters. Everything else here is tidying; this
			// is what stops a retrospective being written rather than read.
			pm.Rejected = append(pm.Rejected, fmt.Sprintf(
				"%q names no cell in the attribution table with at least %d closed trades", l.Cell, MinCellN))
		case len(pm.Lessons) >= MaxLessons:
			pm.Rejected = append(pm.Rejected, fmt.Sprintf("%q is past the %d-lesson ceiling", l.Cell, MaxLessons))
		default:
			// The count is the table's, not the agent's: a lesson that overstates
			// its own sample is the specific error this whole block exists to
			// prevent, and correcting it is better than deleting the lesson.
			if n, ok := counts[key]; ok && n != l.N {
				if l.N != 0 {
					pm.Rejected = append(pm.Rejected, fmt.Sprintf(
						"%q claimed n=%d; the table says n=%d, and the table's number was kept", l.Cell, l.N, n))
				}
				l.N = n
			}
			pm.Lessons = append(pm.Lessons, l)
		}
	}

	for _, w := range t.WeightSuggestions {
		d := strings.ToLower(strings.TrimSpace(w.Domain))
		dir := strings.ToLower(strings.TrimSpace(w.Direction))
		if !weightDomains[d] || (dir != "up" && dir != "down") || w.Reason == "" {
			pm.Rejected = append(pm.Rejected, fmt.Sprintf("weight suggestion %+v is not a domain, a direction and a reason", w))
			continue
		}
		pm.WeightSuggestions = append(pm.WeightSuggestions, WeightSuggestion{Domain: d, Direction: dir, Reason: w.Reason})
	}

	if a != nil {
		pm.AttributionSummary = a.Lines(MinCellN)
	}
	return pm, nil
}

func (a *Attribution) closedCount() int {
	if a == nil {
		return 0
	}
	return a.NClosed
}

// cellCounts is every cell's own n, for correcting a lesson that misstates it.
func (a *Attribution) cellCounts() map[string]int {
	out := map[string]int{}
	if a == nil {
		return out
	}
	for _, m := range []map[string]Bucket{a.BySetup, a.ByCoverage, a.ByConsensus, a.BySector} {
		for k, b := range m {
			out[strings.ToLower(k)] = b.N
		}
	}
	for k, b := range a.Fills.ByOffset {
		out[strings.ToLower(k)] = b.N
	}
	return out
}

// Block renders the lessons for the chief-analyst prompt. It is empty below
// MinClosedForPostMortem and when nothing survived enforcement — an empty
// section header would read as a finding of "no lessons", which is a different
// claim from "not enough record to draw one".
func (p *PostMortem) Block() string {
	if p == nil || p.NClosed < MinClosedForPostMortem || len(p.Lessons) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "### Lessons from %d closed ideas\n\n", p.NClosed)
	sb.WriteString("Drawn from this pipeline's own replayed trades, and checked against the counted record ")
	sb.WriteString("before you saw them: every lesson below names a cell that exists and carries that cell's own `n`. ")
	sb.WriteString("They qualify the base scores; they are not evidence about any name in today's shortlist, ")
	sb.WriteString("and an adjustment made on one stays inside the same band as every other.\n\n")
	for _, l := range p.Lessons {
		fmt.Fprintf(&sb, "- **%s** (n=%d) — %s", l.Cell, l.N, strings.TrimSpace(l.Finding))
		if l.Action != "" {
			fmt.Fprintf(&sb, " → %s", strings.TrimSpace(l.Action))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// Age reports how long ago the post-mortem was computed, so a run can decide
// whether to redo it. A zero or unparseable stamp reads as very old.
func (p *PostMortem) Age() time.Duration {
	if p == nil {
		return 1 << 62
	}
	t, err := time.Parse(time.RFC3339, p.ComputedAt)
	if err != nil {
		return 1 << 62
	}
	return time.Since(t)
}

// Save writes the post-mortem under dir.
func (p *PostMortem) Save(dir string) error {
	if p == nil || dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, PostMortemFile), data, 0o644)
}

// LoadPostMortem reads dir/postmortem.json, returning nil when there is none.
func LoadPostMortem(dir string) *PostMortem {
	b, err := os.ReadFile(filepath.Join(dir, PostMortemFile))
	if err != nil {
		return nil
	}
	var p PostMortem
	if err := json.Unmarshal(b, &p); err != nil {
		return nil
	}
	return &p
}

// AttributionBlock renders the counted record for the post-mortem prompt. Cells
// under MinCellN are not shown at all: an agent told to ignore a number it can
// see will eventually use it.
func AttributionBlock(a *Attribution) string {
	lines := a.Lines(MinCellN)
	if len(lines) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("### Attribution (computed, ground truth)\n\n")
	fmt.Fprintf(&sb, "Every cell below is a count over this pipeline's %d closed trades, computed in-process. ",
		a.closedCount())
	fmt.Fprintf(&sb, "Cells with fewer than %d closed trades are not shown, because they cannot support a claim. ", MinCellN)
	sb.WriteString("You may only draw a lesson about a cell that appears here, spelled the way it is spelled here.\n\n")
	for _, l := range lines {
		sb.WriteString("- " + l + "\n")
	}
	return sb.String()
}

// ClosedTradesBlock renders one line per closed trade with the reasoning behind
// it. This is the half of the evidence the arithmetic cannot produce: the
// attribution counts outcomes, and only the stated `why` says what the winners
// had in common.
func ClosedTradesBlock(s *Summary, max int) string {
	if s == nil {
		return ""
	}
	var closedEntries []Entry
	for _, e := range s.Entries {
		if e.Outcome.closed() {
			closedEntries = append(closedEntries, e)
		}
	}
	if len(closedEntries) == 0 {
		return ""
	}
	// Newest first: a lesson drawn from the current pipeline is worth more than
	// one drawn from a version of it that no longer exists.
	sort.SliceStable(closedEntries, func(i, j int) bool {
		return closedEntries[i].GeneratedAt > closedEntries[j].GeneratedAt
	})
	if max > 0 && len(closedEntries) > max {
		closedEntries = closedEntries[:max]
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "### Closed trades (%d most recent)\n\n", len(closedEntries))
	sb.WriteString("Each is a trade that reached its target, its stop, or the end of its window. ")
	sb.WriteString("`base` and `agree` are the computed score and the domain agreement behind it at generation; ")
	sb.WriteString("`why` is what the Chief Analyst said at the time.\n\n")
	for _, e := range closedEntries {
		fmt.Fprintf(&sb, "- **%s %s** (%s, %s) — %s at %+.2fR (%+.1f%%) after %d sessions · base %d, agree %.0f%%, domains %s\n",
			e.Direction, e.Ticker, e.GeneratedAt, e.Sector, e.Outcome, e.RiskAdjPnL, e.PnLPct, e.BarsHeld,
			e.BaseConfidence, e.Consensus*100, domainScoreList(e.DomainScores))
		if e.Why != "" {
			fmt.Fprintf(&sb, "  - why: %s\n", strings.TrimSpace(e.Why))
		}
	}
	return sb.String()
}

func domainScoreList(m map[string]int) string {
	if len(m) == 0 {
		return "none recorded"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %+d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}
