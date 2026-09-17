// Package store persists run artifacts under runs/<timestamp>/.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

// Run represents one analysis run's on-disk directory.
type Run struct {
	Dir string    // e.g. "runs/2026-06-01T14-30-05"
	TS  time.Time // creation time
}

// New creates a fresh run directory and returns a Run handle.
func New(baseDir string) (*Run, error) {
	ts := time.Now().UTC()
	dirName := ts.Format("2006-01-02T15-04-05")
	dir := filepath.Join(baseDir, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: mkdir %q: %w", dir, err)
	}
	return &Run{Dir: dir, TS: ts}, nil
}

// WriteReport saves an agent's raw markdown output to <agent>.md.
//
// Every artifact writer here redacts on the way out. A provider that echoes its
// own query string puts a credential into an error, an error into a prompt, and
// a prompt into a model's reply — so the last chance to keep it off disk is the
// byte slice, not the struct field. See internal/redact.
func (r *Run) WriteReport(agent, content string) error {
	path := filepath.Join(r.Dir, agent+".md")
	return os.WriteFile(path, redact.Bytes([]byte(content)), 0o644)
}

// ReadReport reads back a saved report; returns "" if not found (non-fatal).
func (r *Run) ReadReport(agent string) string {
	data, err := os.ReadFile(filepath.Join(r.Dir, agent+".md"))
	if err != nil {
		return ""
	}
	return string(data)
}

// WriteShortlist serialises the merged shortlist to shortlist.json.
func (r *Run) WriteShortlist(candidates []model.Candidate) error {
	return r.writeJSON("shortlist.json", candidates)
}

// WriteIdeas serialises the final trade ideas to ideas.json.
func (r *Run) WriteIdeas(ideas *model.IdeasResult) error {
	return r.writeJSON("ideas.json", ideas)
}

// WriteMeta serialises run metadata to metadata.json.
func (r *Run) WriteMeta(meta model.RunMeta) error {
	return r.writeJSON("metadata.json", meta)
}

// sanitizeSymbol makes a ticker safe as a filename (^GSPC → _GSPC).
func sanitizeSymbol(sym string) string {
	var b []byte
	for i := 0; i < len(sym); i++ {
		c := sym[i]
		if c == '^' || c == '/' || c == '\\' || c == ':' {
			b = append(b, '_')
		} else {
			b = append(b, c)
		}
	}
	return string(b)
}

// WritePrices saves one ticker's daily price series to prices/<ticker>.json.
func (r *Run) WritePrices(ticker string, series any) error {
	dir := filepath.Join(r.Dir, "prices")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(series, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sanitizeSymbol(ticker)+".json"), data, 0o644)
}

// ReadPrices loads a saved price series into out; false when absent.
func ReadPrices(runDir, ticker string, out any) (bool, error) {
	data, err := os.ReadFile(filepath.Join(runDir, "prices", sanitizeSymbol(ticker)+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, err
	}
	return true, nil
}

// WriteQuantPack saves the computed quant metrics to quant.json.
func (r *Run) WriteQuantPack(pack any) error {
	return r.writeJSON("quant.json", pack)
}

// WritePrescreen saves the Stage 0.5 universe-wide ranking to prescreen.json:
// the parameters it ran with and one row per constituent. It is the only record
// of *why* twelve names out of a few hundred reached the shortlist — the price
// series behind it deliberately stay in the shared data cache rather than
// filling the run directory with a few hundred files.
func (r *Run) WritePrescreen(ps any) error {
	return r.writeJSON("prescreen.json", ps)
}

// ReadQuantPack loads quant.json into out; false when absent (older runs).
func ReadQuantPack(runDir string, out any) (bool, error) {
	data, err := os.ReadFile(filepath.Join(runDir, "quant.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, err
	}
	return true, nil
}

// ReadPrescreen loads prescreen.json into out; false when absent (older runs,
// and every run generated before Stage 0.5 existed).
//
// It is the counterpart of WritePrescreen, and it exists so the scoreboard can
// replay what the *ranking alone* would have picked, with no model involved, as
// a control against what the pipeline actually shipped. The scoreboard cannot
// import the orchestrator's Prescreen type — the orchestrator imports the
// scoreboard — so it decodes the fields it needs from the artifact directly, and
// the artifact's JSON shape is the contract between them.
func ReadPrescreen(runDir string, out any) (bool, error) {
	data, err := os.ReadFile(filepath.Join(runDir, "prescreen.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, err
	}
	return true, nil
}

// WriteDataPack serialises a market data pack to data/<domain>.json.
func (r *Run) WriteDataPack(domain string, pack any) error {
	dir := filepath.Join(r.Dir, "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(pack, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, domain+".json"), redact.Bytes(data), 0o644)
}

// ReadDataPack reads back a run's stored provider pack for one domain. The
// write side has existed since the packs were first persisted; nothing read them
// again, so the per-domain evidence behind a past idea — the headlines, the
// filings, the option chain — was on disk and unreachable. A post-mortem asking
// *why* a domain was right needs exactly that.
func ReadDataPack(runDir, domain string, out any) error {
	data, err := os.ReadFile(filepath.Join(runDir, "data", domain+".json"))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (r *Run) writeJSON(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.Dir, name), redact.Bytes(data), 0o644)
}

// RunSummary is one row in the run-history listing.
type RunSummary struct {
	Dir         string // full path, e.g. "runs/2026-06-01T14-30-05"
	Name        string // directory name (timestamp)
	Mode        string
	Ticker      string
	Outcome     string // complete | degraded | failed | unknown (no metadata)
	GeneratedAt string
	NumIdeas    int
	HasQuant    bool // quant.json present (newer runs)
}

// ListRuns enumerates run directories, newest first. Runs without metadata.json
// (older versions, aborted runs) still appear with outcome "unknown".
func ListRuns(baseDir string) ([]RunSummary, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []RunSummary
	for i := len(entries) - 1; i >= 0; i-- { // names are timestamps → reverse = newest first
		e := entries[i]
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(baseDir, e.Name())
		s := RunSummary{Dir: dir, Name: e.Name(), Outcome: "unknown"}

		var meta model.RunMeta
		if data, err := os.ReadFile(filepath.Join(dir, "metadata.json")); err == nil {
			if json.Unmarshal(data, &meta) == nil {
				s.Mode = meta.Mode
				s.Ticker = meta.Ticker
				s.Outcome = meta.Outcome
				s.GeneratedAt = meta.GeneratedAt
			}
		}
		if ideas, err := LoadIdeas(dir); err == nil && ideas != nil {
			s.NumIdeas = len(ideas.Ideas)
			if s.Mode == "" {
				s.Mode = ideas.Mode
			}
			if s.GeneratedAt == "" {
				s.GeneratedAt = ideas.GeneratedAt
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "quant.json")); err == nil {
			s.HasQuant = true
		}
		out = append(out, s)
	}
	return out, nil
}

// LoadIdeas reads a run's ideas.json; (nil, error) when absent or malformed.
func LoadIdeas(runDir string) (*model.IdeasResult, error) {
	data, err := os.ReadFile(filepath.Join(runDir, "ideas.json"))
	if err != nil {
		return nil, err
	}
	var ideas model.IdeasResult
	if err := json.Unmarshal(data, &ideas); err != nil {
		return nil, err
	}
	return &ideas, nil
}

// LoadMeta reads a run's metadata.json; (nil, error) when absent or malformed.
func LoadMeta(runDir string) (*model.RunMeta, error) {
	data, err := os.ReadFile(filepath.Join(runDir, "metadata.json"))
	if err != nil {
		return nil, err
	}
	var meta model.RunMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// ListReports returns the markdown report filenames in a run dir, sorted.
func ListReports(runDir string) []string {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
			out = append(out, e.Name())
		}
	}
	return out
}

// ReadReportFile reads one report by filename from a run dir ("" if missing).
func ReadReportFile(runDir, name string) string {
	data, err := os.ReadFile(filepath.Join(runDir, name))
	if err != nil {
		return ""
	}
	return string(data)
}

// CleanupOldRuns keeps only the newest N run directories.
func CleanupOldRuns(baseDir string, keep int) error {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return err
	}

	var runs []os.DirEntry
	for _, e := range entries {
		if e.IsDir() {
			runs = append(runs, e)
		}
	}

	if len(runs) <= keep {
		return nil
	}

	// Directories are named by timestamp, so we can just sort by name
	for i := 0; i < len(runs)-keep; i++ {
		path := filepath.Join(baseDir, runs[i].Name())
		if err := os.RemoveAll(path); err != nil {
			fmt.Fprintf(os.Stderr, "warn: cleanup failed for %s: %v\n", path, err)
		}
	}

	return nil
}
