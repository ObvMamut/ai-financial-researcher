package scoreboard

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// The thesis arms: does per-company source research beat the funnel it starts
// from?
//
// Thesis mode ships almost nothing, so an arm of its shipped ideas would take
// months to say anything. But every researched name ends with a dossier, and
// every dossier states a lean — BUY or SELL, with a 1–5 conviction — whether or
// not it was supported enough to trade. Scoring every lean at the same fixed
// horizon as the other arms gives the thesis pipeline a record of its own
// judgement on a dozen names a run instead of on zero, and `thesis-lean −
// shortlist` is the comparison the switch-off rule in the improvement plan is
// written against: the shortlist is exactly the set of names thesis research
// was handed.
//
//	thesis                what thesis-mode runs shipped.
//	thesis-lean           every researched name at its dossier lean.
//	thesis-lean-backfill  the leans judged by hand from the dossiers of runs
//	                      that predate the lean field (leans.csv), kept apart
//	                      because a reader, not the researcher, stated them.

// DefaultLeanBackfill is the historical lean file, relative to the repository
// root the command runs from. A missing file leaves the backfill arm empty.
const DefaultLeanBackfill = "docs/research/2026-09-23-evidence/thesis/leans.csv"

// isThesisRun reports whether a run was produced by the thesis research mode.
// ideas.json states it for runs that reached synthesis; metadata.json for the
// rest.
func isThesisRun(ideas *model.IdeasResult, meta *model.RunMeta) bool {
	mode := ""
	if ideas != nil {
		mode = ideas.ResearchMode
	}
	if mode == "" && meta != nil {
		mode = meta.ResearchMode
	}
	return strings.EqualFold(mode, "thesis")
}

// parseLean reads a lean as BUY or SELL. It accepts the qualified form the
// backfill uses ("SELL (weak)") and returns the qualifier separately; anything
// else — "none evident", NONE, an empty field — is not a directional call.
func parseLean(s string) (model.Direction, string, bool) {
	s = strings.TrimSpace(s)
	head, rest, _ := strings.Cut(s, " ")
	var dir model.Direction
	switch strings.ToUpper(head) {
	case "BUY":
		dir = model.DirectionBuy
	case "SELL":
		dir = model.DirectionSell
	default:
		return "", "", false
	}
	qual := strings.Trim(strings.TrimSpace(rest), "()")
	return dir, strings.ToLower(qual), true
}

// leanCalls reads every researched candidate's dossier lean from a run's
// data/research-<hexticker>.json files. The dossier is read by key rather than
// through the orchestrator's type, so an artifact written before the lean
// field existed is simply a name with no lean. closes is the pre-screen's close
// by upper-cased ticker, the anchor the shortlist arm uses too; a name without
// one is anchored by measureCall from its own bars.
func leanCalls(runDir string, closes map[string]float64) (calls []call, nonDirectional int) {
	files, _ := filepath.Glob(filepath.Join(runDir, "data", "research-*.json"))
	sort.Strings(files)
	for _, path := range files {
		if strings.HasSuffix(path, "research-coverage.json") {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rec struct {
			Candidate model.Candidate `json:"candidate"`
			Dossier   map[string]any  `json:"dossier"`
		}
		if json.Unmarshal(b, &rec) != nil || rec.Candidate.Ticker == "" {
			continue
		}
		lean, _ := rec.Dossier["lean"].(string)
		dir, _, ok := parseLean(lean)
		if !ok {
			nonDirectional++
			continue
		}
		conviction := 0
		if v, ok := rec.Dossier["conviction"].(float64); ok && v >= 1 && v <= 5 {
			conviction = int(v)
		}
		c := rec.Candidate
		calls = append(calls, call{ticker: c.Ticker, name: c.Name, index: c.Index,
			direction: dir, anchor: closes[strings.ToUpper(c.Ticker)], setup: c.Setup,
			conviction: conviction})
	}
	return calls, nonDirectional
}

// backfillLean is one row of leans.csv.
type backfillLean struct {
	run, ticker, runDate string
	direction            model.Direction
	strength             string
}

// readLeanBackfill parses leans.csv (run,ticker,run_date,lean,…). Columns are
// located by header name so a reordered file still reads. A missing file is
// not an error: the arm is simply empty.
func readLeanBackfill(path string) (leans []backfillLean, nonDirectional int, err error) {
	if path == "" {
		return nil, 0, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, 0, err
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimSpace(strings.ToLower(h))] = i
	}
	for _, need := range []string{"run", "ticker", "lean"} {
		if _, ok := col[need]; !ok {
			return nil, 0, errors.New("lean backfill: missing column " + need)
		}
	}
	field := func(rec []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		dir, strength, ok := parseLean(field(rec, "lean"))
		if !ok {
			nonDirectional++
			continue
		}
		leans = append(leans, backfillLean{run: field(rec, "run"), ticker: field(rec, "ticker"),
			runDate: field(rec, "run_date"), direction: dir, strength: strength})
	}
	return leans, nonDirectional, nil
}

// runIndexOf maps each ticker a run screened to the index it was screened
// under, from the shortlist first and the pre-screen rows second, so a
// backfilled lean is measured against the same benchmark its run used.
func runIndexOf(meta *model.RunMeta, rows []prescreenRow) map[string]string {
	out := map[string]string{}
	if meta != nil {
		for _, c := range meta.Shortlist {
			if c.Index != "" {
				out[strings.ToUpper(c.Ticker)] = c.Index
			}
		}
	}
	for _, r := range rows {
		k := strings.ToUpper(r.Ticker)
		if _, ok := out[k]; !ok && r.Index != "" {
			out[k] = r.Index
		}
	}
	return out
}

// backfillRun is the run a backfilled lean came from, as far as runsDir knows
// it. A run that is no longer on disk still scores, off the CSV's date.
func backfillRun(runs map[string]store.RunSummary, l backfillLean) (store.RunSummary, string) {
	r, ok := runs[l.run]
	if !ok {
		return store.RunSummary{Name: l.run}, l.runDate
	}
	if r.GeneratedAt == "" {
		return r, l.runDate
	}
	return r, r.GeneratedAt
}
