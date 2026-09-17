package scoreboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/store"
)

// Pairing is declared explicitly; closeness in time alone never implies a pair.
// Hashes verify only the declared snapshot scope, not all information seen by a
// model. Registration time is an operator declaration, not a trusted timestamp.
type ResearchPairSpec struct {
	ID             string            `json:"id"`
	RegisteredAt   string            `json:"registered_at"`
	LegacyRun      string            `json:"legacy_run"`
	ThesisRun      string            `json:"thesis_run"`
	MaxSkewSeconds int               `json:"max_skew_seconds"`
	SnapshotSHA256 map[string]string `json:"snapshot_sha256"`
}

type ResearchPairAudit struct {
	ID               string                       `json:"id"`
	LegacyRun        string                       `json:"legacy_run"`
	ThesisRun        string                       `json:"thesis_run"`
	Status           string                       `json:"status"`
	Issues           []string                     `json:"issues"`
	SnapshotSHA256   map[string]string            `json:"snapshot_sha256"`
	Deltas           map[string]ResearchPairDelta `json:"deltas,omitempty"`
	OverlappingPairs []string                     `json:"overlapping_pairs,omitempty"`
}

type ResearchPairDelta struct {
	State            string   `json:"state"`
	LegacyN          int      `json:"legacy_n"`
	ThesisN          int      `json:"thesis_n"`
	ExcessDifference *float64 `json:"thesis_minus_legacy_mean_excess_pct,omitempty"`
}

func loadResearchPairs(path string) ([]ResearchPairSpec, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var document struct {
		Pairs []ResearchPairSpec `json:"pairs"`
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("research pairs: manifest exceeds 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&document); err != nil {
		return nil, fmt.Errorf("research pairs: %w", err)
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("research pairs: expected one JSON object")
	}
	if len(document.Pairs) == 0 {
		return nil, fmt.Errorf("research pairs: nonempty pairs array required")
	}
	ids, runs := map[string]bool{}, map[string]bool{}
	for _, p := range document.Pairs {
		if strings.TrimSpace(p.ID) == "" || ids[p.ID] {
			return nil, fmt.Errorf("research pairs: missing or duplicate id %q", p.ID)
		}
		ids[p.ID] = true
		if _, err := time.Parse(time.RFC3339, p.RegisteredAt); err != nil {
			return nil, fmt.Errorf("pair %s: registered_at must be RFC3339", p.ID)
		}
		for _, r := range []string{p.LegacyRun, p.ThesisRun} {
			if r == "" || r == "." || r == ".." || filepath.Base(r) != r || strings.ContainsAny(r, "/\\") || runs[r] {
				return nil, fmt.Errorf("pair %s: run names must be distinct directory names, used once", p.ID)
			}
			runs[r] = true
		}
		if p.MaxSkewSeconds < 0 || p.MaxSkewSeconds > 3600 {
			return nil, fmt.Errorf("pair %s: max_skew_seconds must be 0–3600", p.ID)
		}
		if len(p.SnapshotSHA256) == 0 {
			return nil, fmt.Errorf("pair %s: snapshot hashes are required", p.ID)
		}
		for name, hash := range p.SnapshotSHA256 {
			if !snapshotPath(name) {
				return nil, fmt.Errorf("pair %s: invalid input snapshot path %q", p.ID, name)
			}
			bytes, err := hex.DecodeString(hash)
			if err != nil || len(bytes) != sha256.Size {
				return nil, fmt.Errorf("pair %s: invalid SHA-256 for %s", p.ID, name)
			}
		}
	}
	return document.Pairs, nil
}

func snapshotPath(name string) bool {
	if !filepath.IsLocal(name) || strings.Contains(name, "\\") || filepath.ToSlash(filepath.Clean(name)) != name {
		return false
	}
	if name == "prescreen.json" || name == "quant.json" {
		return true
	}
	if !strings.HasPrefix(name, "data/") || filepath.Ext(name) != ".json" || strings.Count(name, "/") != 1 {
		return false
	}
	base := strings.TrimPrefix(name, "data/")
	return !strings.HasPrefix(base, "input-") && !strings.HasPrefix(base, "research") && !strings.HasSuffix(base, "-coverage.json") && base != "candidates.json"
}

func auditResearchPairs(ctx context.Context, dir string, specs []ResearchPairSpec, runs []ResearchRunDiagnostics) []ResearchPairAudit {
	by := map[string]ResearchRunDiagnostics{}
	for _, r := range runs {
		by[r.Run] = r
	}
	var out []ResearchPairAudit
	for _, p := range specs {
		if ctx.Err() != nil {
			break
		}
		audit := ResearchPairAudit{ID: p.ID, LegacyRun: p.LegacyRun, ThesisRun: p.ThesisRun, Status: "mismatch", Issues: []string{}, SnapshotSHA256: p.SnapshotSHA256}
		l, haveL := by[p.LegacyRun]
		t, haveT := by[p.ThesisRun]
		if !haveL || !haveT {
			audit.Status = "unavailable"
			audit.Issues = append(audit.Issues, "one or both declared run directories are missing")
			out = append(out, audit)
			continue
		}
		if l.Result != "ok" || t.Result != "ok" || !l.MetadataAvailable || !t.MetadataAvailable {
			audit.Issues = append(audit.Issues, "both runs need readable results and metadata; failures remain in run diagnostics")
		}
		if l.ResearchMode != "legacy" || t.ResearchMode != "thesis" {
			audit.Issues = append(audit.Issues, "declared arms do not have legacy/thesis research modes")
		}
		lt, le := time.Parse(time.RFC3339, l.GeneratedAt)
		tt, te := time.Parse(time.RFC3339, t.GeneratedAt)
		registered, _ := time.Parse(time.RFC3339, p.RegisteredAt)
		if le != nil || te != nil || lt.UTC().Format("2006-01-02") != tt.UTC().Format("2006-01-02") || lt.Sub(tt).Abs() > time.Duration(p.MaxSkewSeconds)*time.Second {
			audit.Issues = append(audit.Issues, "generation timestamps exceed the declared same-day tolerance")
		}
		if registered.After(lt) || registered.After(tt) {
			audit.Issues = append(audit.Issues, "pair registration must precede both generation timestamps")
		}
		lm, lerr := store.LoadMeta(filepath.Join(dir, p.LegacyRun))
		tm, terr := store.LoadMeta(filepath.Join(dir, p.ThesisRun))
		if lerr == nil && terr == nil {
			li, ti := append([]string(nil), lm.Indices...), append([]string(nil), tm.Indices...)
			sort.Strings(li)
			sort.Strings(ti)
			if lm.Mode != tm.Mode || lm.Ticker != tm.Ticker || strings.Join(li, ",") != strings.Join(ti, ",") {
				audit.Issues = append(audit.Issues, "run modes, tickers or index universes differ")
			}
		}
		for _, run := range []string{p.LegacyRun, p.ThesisRun} {
			ideas, ie := store.LoadIdeas(filepath.Join(dir, run))
			meta, me := store.LoadMeta(filepath.Join(dir, run))
			if ie != nil || me != nil || ideas == nil || meta == nil {
				continue // Unreadable artifacts have already rejected the pair.
			}
			it, ie := time.Parse(time.RFC3339, ideas.GeneratedAt)
			mt, me := time.Parse(time.RFC3339, meta.GeneratedAt)
			im, mm := ideas.ResearchMode, meta.ResearchMode
			if im == "" {
				im = "legacy"
			}
			if mm == "" {
				mm = "legacy"
			}
			if ie != nil || me != nil || !it.Equal(mt) || im != mm || ideas.SchemaVersion != meta.SchemaVersion || ideas.Mode != meta.Mode {
				audit.Issues = append(audit.Issues, run+": ideas and metadata identities disagree or are incomplete")
			}
		}
		files := make([]string, 0, len(p.SnapshotSHA256))
		for name := range p.SnapshotSHA256 {
			files = append(files, name)
		}
		sort.Strings(files)
		for _, name := range files {
			for _, run := range []string{p.LegacyRun, p.ThesisRun} {
				hash, err := snapshotHash(ctx, filepath.Join(dir, run), name)
				if err != nil {
					audit.Issues = append(audit.Issues, run+"/"+name+": snapshot unavailable or invalid")
				} else if !strings.EqualFold(hash, p.SnapshotSHA256[name]) {
					audit.Issues = append(audit.Issues, run+"/"+name+": snapshot hash mismatch")
				}
			}
		}
		if len(audit.Issues) == 0 {
			audit.Status = "matched_declared_inputs"
			audit.Deltas = map[string]ResearchPairDelta{}
			for _, arm := range []string{"shipped", "composite", "shortlist"} {
				for _, h := range []int{10, 15} {
					key := fmt.Sprintf("%s/%d", arm, h)
					la, lok := l.Arms[key]
					ta, tok := t.Arms[key]
					d := ResearchPairDelta{State: "unavailable", LegacyN: la.Record.N, ThesisN: ta.Record.N}
					switch {
					case !lok || !tok || la.Unmeasurable > 0 || ta.Unmeasurable > 0:
					case la.Pending > 0 || ta.Pending > 0:
						d.State = "pending"
					case la.Record.N == 0 || ta.Record.N == 0:
						d.State = "empty_arm"
					default:
						d.State = "measured"
						v := round2(ta.Record.AvgExcess - la.Record.AvgExcess)
						d.ExcessDifference = &v
					}
					audit.Deltas[key] = d
				}
			}
		}
		out = append(out, audit)
	}
	// Report overlapping same-ticker windows across pair observations. No pooled
	// significance claim is made from these dependent samples.
	for i := range out {
		for j := 0; j < i; j++ {
			if pairsOverlap(out[i], out[j], by) {
				out[i].OverlappingPairs = append(out[i].OverlappingPairs, out[j].ID)
				out[j].OverlappingPairs = append(out[j].OverlappingPairs, out[i].ID)
			}
		}
	}
	return out
}

func pairsOverlap(a, b ResearchPairAudit, by map[string]ResearchRunDiagnostics) bool {
	for _, an := range []string{a.LegacyRun, a.ThesisRun} {
		for _, bn := range []string{b.LegacyRun, b.ThesisRun} {
			ae, be := by[an].Arms["shipped/15"].Entries, by[bn].Arms["shipped/15"].Entries
			joined := append(append([]Entry(nil), ae...), be...)
			if overlappingResearchCalls(joined) > overlappingResearchCalls(ae)+overlappingResearchCalls(be) {
				return true
			}
		}
	}
	return false
}

func snapshotHash(ctx context.Context, dir, name string) (string, error) {
	// Root confines reads even when an artifact contains a symlink outside the run.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	f, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (256<<20)+1))
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(data) > 256<<20 || !json.Valid(data) {
		return "", fmt.Errorf("snapshot must be valid JSON no larger than 256 MiB")
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}
