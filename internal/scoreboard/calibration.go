package scoreboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// CalibrationFile is the name of the stored track record, written under the
// data directory and copied into each run.
const CalibrationFile = "calibration.json"

const (
	// MinClosedForFeedback is the number of closed trades below which the track
	// record is withheld from the Chief. Nine trades cannot tell a 45% domain
	// from a 55% one, and a Chief told otherwise will spend its adjustment band
	// on noise — which is worse than having no record at all.
	MinClosedForFeedback = 10
	// MinClosedForEdge is the number of closed trades below which the risk
	// gate keeps its assumed edge prior rather than the measured one. The
	// expectancy check multiplies this number across every idea, so importing
	// it from a thin sample would let one lucky month turn the check off.
	MinClosedForEdge = 30
)

// Calibration is the system's own realized record, computed from the replayed
// scoreboard. It exists so the Chief Analyst and the risk gate can be told what
// this pipeline has actually achieved rather than what it assumes.
type Calibration struct {
	ComputedAt      string  `json:"computed_at"`
	NClosed         int     `json:"n_closed"`
	WinRate         float64 `json:"win_rate"`
	AvgPnLPct       float64 `json:"avg_pnl_pct"`
	AvgR            float64 `json:"avg_r"`
	AvgExcessPnLPct float64 `json:"avg_excess_pnl_pct,omitempty"`
	AvgBarsHeld     float64 `json:"avg_bars_held,omitempty"`

	// Domains is scored over the closed trades each domain backed; Confidence
	// over the bucket an idea falls in when its recorded domain scores are
	// re-read on the current scale (see comparableConfidence — an idea's stated
	// number is on whichever scale its own run used, and this codebase has had
	// three). Both answer the question the Chief cannot answer from inside one
	// run: was this kind of call right last time?
	Domains    map[string]Bucket `json:"domains,omitempty"`
	Confidence map[string]Bucket `json:"confidence,omitempty"`
	Directions map[string]Bucket `json:"directions,omitempty"`
}

// Calibrate reduces a replayed summary to the record worth feeding back.
func Calibrate(s *Summary) *Calibration {
	if s == nil {
		return nil
	}
	c := &Calibration{
		ComputedAt:      time.Now().UTC().Format(time.RFC3339),
		NClosed:         s.Closed,
		WinRate:         s.WinRate,
		AvgPnLPct:       s.AvgPnL,
		AvgR:            s.AvgR,
		AvgExcessPnLPct: s.AvgExcess,
		Domains:         s.ByDomain,
		Confidence:      s.ByConfidence,
		Directions:      s.ByDirection,
	}
	var bars, n float64
	for _, e := range s.Entries {
		if !e.Outcome.closed() {
			continue
		}
		bars += float64(e.BarsHeld)
		n++
	}
	if n > 0 {
		c.AvgBarsHeld = round2(bars / n)
	}
	return c
}

// Save writes the calibration into dir as calibration.json.
func (c *Calibration) Save(dir string) error {
	if c == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, CalibrationFile), append(b, '\n'), 0o644)
}

// LoadCalibration reads dir/calibration.json, returning nil when there is none.
// A missing record is an absence, not an error: the first run of a fresh
// install has nothing to be calibrated against.
func LoadCalibration(dir string) *Calibration {
	b, err := os.ReadFile(filepath.Join(dir, CalibrationFile))
	if err != nil {
		return nil
	}
	var c Calibration
	if err := json.Unmarshal(b, &c); err != nil {
		return nil
	}
	return &c
}

// Age reports how long ago the calibration was computed. A zero or unparseable
// timestamp reads as very old, so a malformed file gets refreshed rather than
// trusted.
func (c *Calibration) Age() time.Duration {
	if c == nil {
		return 1 << 62
	}
	t, err := time.Parse(time.RFC3339, c.ComputedAt)
	if err != nil {
		return 1 << 62
	}
	return time.Since(t)
}

// RealizedEdge returns the measured average R per closed trade, and whether
// there is enough closed history to act on it.
func (c *Calibration) RealizedEdge() (float64, bool) {
	if c == nil || c.NClosed < MinClosedForEdge {
		return 0, false
	}
	return c.AvgR, true
}

// Block renders the track record for the chief-analyst prompt. It is empty
// below MinClosedForFeedback, and deliberately short: it sits beside five full
// specialist reports, and a page of statistics would displace the evidence it
// is meant to weight.
func (c *Calibration) Block() string {
	if c == nil || c.NClosed < MinClosedForFeedback {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "### Track record (computed from %d closed ideas)\n\n", c.NClosed)
	sb.WriteString("This pipeline's own realized results, from replaying past ideas through their\n")
	sb.WriteString("daily bars. It is a measurement, not a target.\n\n")
	fmt.Fprintf(&sb, "- Overall: %.0f%% closed at a profit · avg %+.2fR · avg hold %.0f sessions\n",
		c.WinRate*100, c.AvgR, c.AvgBarsHeld)
	// The legacy bucket is deliberately withheld from the Chief: those ideas'
	// confidence was asserted by a model on a scale this run does not use, so
	// telling it to "move away from that bucket" would be advice about a
	// measurement that no longer exists.
	if line := calibrationLine(c.Confidence, comparableConfidenceBuckets()); line != "" {
		fmt.Fprintf(&sb, "- By computed confidence: %s\n", line)
	}
	if line := calibrationLine(c.Domains, sortedKeys(c.Domains)); line != "" {
		fmt.Fprintf(&sb, "- By domain backing the trade: %s\n", line)
	}
	if line := calibrationLine(c.Directions, sortedKeys(c.Directions)); line != "" {
		fmt.Fprintf(&sb, "- By direction: %s\n", line)
	}
	sb.WriteString("\nHow to use it: a domain whose backing wins near half the time carries no\n")
	sb.WriteString("information — do not spend an adjustment on it. Where a confidence bucket's\n")
	sb.WriteString("realized win rate is far from the confidence it stated, move away from that\n")
	sb.WriteString("bucket. Small n means small conclusions; say so rather than overfitting.\n")
	return sb.String()
}

// calibrationLine renders "key n·win%·avgR" cells for one slice, skipping empty
// buckets. Each cell carries its n so a 100% built on two trades cannot be read
// as a 100% built on twenty.
func calibrationLine(m map[string]Bucket, order []string) string {
	var parts []string
	for _, k := range order {
		b, ok := m[k]
		if !ok || b.N == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d·%.0f%%·%+.2fR", k, b.N, b.WinRate*100, b.AvgR))
	}
	return strings.Join(parts, " · ")
}

// personaKey identifies the exact prompt set a run used: the directory it was
// loaded from, plus a digest of every persona hash in it.
//
// The directory name alone is not enough — editing one persona in place leaves
// the name unchanged while changing the thing being measured, and attributing
// those results to the old prompts is the experiment silently going wrong. The
// digest alone is not enough either: nobody can read it. Both.
//
// A run that recorded no persona hashes returns "", and such runs are pooled
// into no arm rather than into a plausible-looking default.
func personaKey(m *model.RunMeta) string {
	if m == nil || len(m.PersonaSHA) == 0 {
		return ""
	}
	roles := make([]string, 0, len(m.PersonaSHA))
	for r := range m.PersonaSHA {
		roles = append(roles, r)
	}
	sort.Strings(roles)

	h := sha256.New()
	for _, r := range roles {
		fmt.Fprintf(h, "%s:%s\n", r, m.PersonaSHA[r])
	}
	digest := hex.EncodeToString(h.Sum(nil))[:6]

	if m.PersonaSet == "" {
		return digest
	}
	return m.PersonaSet + "@" + digest
}
