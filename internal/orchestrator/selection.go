package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/parse"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

// merit_veto selection (docs/workflow/independent-research.md, "Selection").
//
// The 2026-09-23 attribution study measured every model stage above the funnel
// and found nothing: specialist domain ICs of −0.08 to +0.07 pooled, the top
// third of base scores doing worst, and the Chief's picks (+0.55%) level with
// the names it left out (+0.58%). The scouts' shortlist was the only arm whose
// interval cleared zero. So under this policy the models stop ranking. Go ships
// the top of the shortlist by the merit the funnel already computed, in the
// scout's direction; the specialists and the Chief can only *remove* a name, and
// only for a reason from a closed list. The Chief still writes the prose, and
// its own ranking is recorded as a shadow so the scoreboard can keep measuring
// whether it would have done better.
const (
	// meritVetoTopN is how many ideas the policy ships, the same as the Chief's.
	meritVetoTopN = 5
	// meritVetoHoldDays is the time exit, in sessions: the horizon the
	// backtest and the scoreboard's control arms measure.
	meritVetoHoldDays = 15
)

// Why an unshipped row did not ship.
const (
	excludedNoDirection = "no_direction"
	excludedVetoed      = "vetoed"
	excludedRiskGate    = "risk_gate"
	excludedSectorCap   = "sector_cap"
	excludedCorrelated  = "correlated"
	excludedBelowCut    = "below_cut"
)

// useMeritVeto reports whether this run selects by merit. Only legacy
// independent research does: single-stock mode has no scout direction and no
// shortlist to rank, and thesis mode has its own selection.
func useMeritVeto(cfg Config) bool {
	return cfg.Selection == model.SelectionMeritVeto && cfg.Mode == model.ModeIndependent && cfg.ResearchMode != "thesis"
}

func biasDirection(b model.Bias) model.Direction {
	switch b {
	case model.BiasBullish:
		return model.DirectionBuy
	case model.BiasBearish:
		return model.DirectionSell
	}
	return ""
}

// buildSelectionRows is one row per shortlisted name, in merit order, carrying
// every label and veto the specialists gave it and its computed base score. It
// is built under both policies, so the labels are measured whichever one ships.
func buildSelectionRows(cfg Config, shortlist []model.Candidate, ps *Prescreen, bases []BaseScore,
	labels map[string]map[string]model.NameLabels, v verified) []model.SelectionRow {

	coverage := func(t string) float64 { return expectedCoverage(cfg.Weights, t) }
	baseBy := make(map[string]BaseScore, len(bases))
	for _, b := range bases {
		baseBy[normTicker(b.Ticker)] = b
	}
	rows := make([]model.SelectionRow, 0, len(shortlist))
	for _, c := range shortlist {
		t := normTicker(c.Ticker)
		r := model.SelectionRow{
			Ticker: c.Ticker, Name: c.Name, Index: c.Index, Sector: c.Sector, Setup: c.Setup,
			Direction: biasDirection(c.Bias),
			Merit:     math.Round(meritScore(ps, coverage, c)*10000) / 10000,
		}
		if m, ok := quantFor(v, t); ok && m.LastClose > 0 {
			r.Close = math.Round(m.LastClose*100) / 100
		}
		if b, ok := baseBy[t]; ok {
			r.DomainScores = b.Domains
			if r.Direction != "" {
				r.BaseConfidence = b.For(r.Direction)
			}
		}
		if ls := labels[t]; len(ls) > 0 {
			r.Labels = ls
			domains := make([]string, 0, len(ls))
			for d := range ls {
				domains = append(domains, d)
			}
			sort.Strings(domains)
			for _, d := range domains {
				if l := ls[d]; l.Veto {
					r.Vetoes = append(r.Vetoes, model.Veto{Source: d, Reason: l.VetoReason, Note: l.Note})
				}
			}
			r.Vetoed = len(r.Vetoes) > 0
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Merit > rows[j].Merit })
	for i := range rows {
		rows[i].MeritRank = i + 1
	}
	return rows
}

// markShipped records, under either policy, which rows became ideas.
func markShipped(rows []model.SelectionRow, ideas []model.TradeIdea) {
	rank := make(map[string]int, len(ideas))
	for _, i := range ideas {
		rank[normTicker(i.Ticker)] = i.Rank
	}
	for i := range rows {
		if r, ok := rank[normTicker(rows[i].Ticker)]; ok {
			rows[i].Selected, rows[i].ShippedRank = true, r
		}
	}
}

// meritVetoRisk is the risk policy merit_veto ideas are built and gated under.
// The policy has no one to place a limit or a target, so its ideas are always
// market-on-open behind the catastrophe stop, whatever risk.entry_type says.
func meritVetoRisk(r model.RiskConfig) model.RiskConfig {
	r = riskDefaults(r)
	r.EntryType = model.EntryMarketOnOpen
	return r
}

// meritWhy is the prose an idea carries when the Chief wrote none for it.
func meritWhy(r model.SelectionRow) string {
	setup := r.Setup
	if setup == "" {
		setup = "no pre-screen row"
	}
	return fmt.Sprintf("Merit-veto selection: merit rank %d (merit %+.2f, setup %s) at the scout's direction; no specialist or Chief veto, risk gate passed.",
		r.MeritRank, r.Merit, setup)
}

// meritCandidates builds the mechanical idea for every row that can ship —
// directional and unvetoed — and gates each one on its own. It returns the
// eligible ideas in merit order and the per-ticker warnings validation raised,
// and marks every row it rules out.
func meritCandidates(cfg Config, rows []model.SelectionRow, v verified) ([]model.TradeIdea, map[string][]warning) {
	risk := meritVetoRisk(cfg.Risk)
	vcfg := cfg
	vcfg.Risk = risk
	res := &model.IdeasResult{Mode: string(cfg.Mode)}
	rowBy := map[string]*model.SelectionRow{}
	for i := range rows {
		r := &rows[i]
		rowBy[normTicker(r.Ticker)] = r
		switch {
		case r.Direction == "":
			r.Excluded = excludedNoDirection
			continue
		case r.Vetoed:
			r.Excluded = excludedVetoed
			continue
		}
		res.Ideas = append(res.Ideas, model.TradeIdea{
			Ticker: r.Ticker, Name: r.Name, Index: r.Index, Direction: r.Direction,
			Confidence: r.BaseConfidence, BaseConfidence: r.BaseConfidence, DomainScores: r.DomainScores,
			EntryType: model.EntryMarketOnOpen, TimeframeDays: meritVetoHoldDays, Why: meritWhy(*r),
		})
	}
	byTicker := map[string][]warning{}
	for _, w := range validateIdeas(res, vcfg, v) {
		byTicker[normTicker(w.Ticker)] = append(byTicker[normTicker(w.Ticker)], w)
	}
	anyScored := false
	for _, b := range v.Bases {
		if len(b.Domains) > 0 {
			anyScored = true
			break
		}
	}
	var eligible []model.TradeIdea
	for _, idea := range res.Ideas {
		probe := idea
		var hard string
		for _, f := range gateIdea(&probe, v, risk) {
			if f.Hard {
				hard = f.Message
				break
			}
		}
		if hard == "" && anyScored {
			hard = checkPriceOnlyEvidence(&probe)
		}
		if hard != "" {
			r := rowBy[normTicker(idea.Ticker)]
			r.Excluded, r.RiskGate = excludedRiskGate, hard
			continue
		}
		eligible = append(eligible, idea)
	}
	return eligible, byTicker
}

// pairCorrelation names the already-picked name a skipped one moves with.
type pairCorrelation struct {
	With string
	Rho  float64
}

// pickBook walks the eligible ideas in merit order and takes the first topN
// that no one vetoed, holding each sector to the risk gate's own limit and
// skipping a name whose daily returns correlate above maxCorr with a
// same-direction name already in the book (gateBook's own test; maxCorr >= 1
// disables it, and a name with no usable series is never skipped). It returns
// the book, the names the sector limit passed over and the names correlation
// passed over, each with its partner.
func pickBook(eligible []model.TradeIdea, sectorOf map[string]string, vetoed map[string]bool, topN, maxPerSector int,
	series map[string]*quant.Series, maxCorr float64) ([]model.TradeIdea, map[string]bool, map[string]pairCorrelation) {
	var book []model.TradeIdea
	capped := map[string]bool{}
	correlated := map[string]pairCorrelation{}
	perSector := map[string]int{}
	for _, idea := range eligible {
		if len(book) == topN {
			break
		}
		t := normTicker(idea.Ticker)
		if vetoed[t] {
			continue
		}
		if s := sectorOf[t]; s != "" && maxPerSector > 0 && perSector[s] >= maxPerSector {
			capped[t] = true
			continue
		}
		if maxCorr < 1 {
			var hit *pairCorrelation
			for _, b := range book {
				if b.Direction != idea.Direction {
					continue
				}
				c, ok := quant.Correlation(series[strings.ToUpper(b.Ticker)], series[strings.ToUpper(idea.Ticker)])
				if ok && c > maxCorr {
					hit = &pairCorrelation{With: b.Ticker, Rho: c}
					break
				}
			}
			if hit != nil {
				correlated[t] = *hit
				continue
			}
		}
		perSector[sectorOf[t]]++
		idea.Rank = len(book) + 1
		book = append(book, idea)
	}
	return book, capped, correlated
}

// chiefWriterResult is the Chief's answer under merit_veto: prose for the names
// Go selected, optional vetoes from the closed list, and its own ranking of the
// whole shortlist as a shadow.
type chiefWriterResult struct {
	Ideas []struct {
		Ticker       string `json:"ticker"`
		Direction    string `json:"direction"`
		Why          string `json:"why"`
		PositionNote string `json:"position_note"`
	} `json:"ideas"`
	Vetoes []struct {
		Ticker     string `json:"ticker"`
		VetoReason string `json:"veto_reason"`
		Note       string `json:"note"`
	} `json:"vetoes"`
	ShadowRank []string `json:"shadow_rank"`
	Notes      string   `json:"notes"`
}

func parseChiefWriter(stdout string) (*chiefWriterResult, error) {
	raw, ok := parse.LastJSONBlock(stdout)
	if !ok {
		return nil, fmt.Errorf("no fenced ```json block found in chief-analyst output")
	}
	var out chiefWriterResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &out, nil
}

// selectionBlock renders Go's decision for the Chief: the book, the reserves
// that fill it if the Chief vetoes, and everything already ruled out.
func selectionBlock(rows []model.SelectionRow, book, reserves []model.TradeIdea, topN int) string {
	rowBy := map[string]model.SelectionRow{}
	for _, r := range rows {
		rowBy[normTicker(r.Ticker)] = r
	}
	line := func(sb *strings.Builder, idea model.TradeIdea) {
		r := rowBy[normTicker(idea.Ticker)]
		fmt.Fprintf(sb, "- #%d %s %s · merit %+.2f · setup %s · base %d\n",
			r.MeritRank, r.Ticker, r.Direction, r.Merit, orUnknown(r.Setup), r.BaseConfidence)
	}
	var sb strings.Builder
	sb.WriteString("### Go's selection (final: membership, direction and order are not yours to change)\n\n")
	fmt.Fprintf(&sb, "topN = %d. Merit is the pre-screen composite aligned with the scout's direction, scaled by coverage, plus scout agreement; `#n` is the merit rank on the whole shortlist. ", topN)
	fmt.Fprintf(&sb, "Every name enters market-on-open at the next session's open behind a catastrophe stop, with a %d-session time exit and no target.\n\n", meritVetoHoldDays)
	sb.WriteString("**Book** (ships in this order):\n")
	for _, i := range book {
		line(&sb, i)
	}
	if len(reserves) > 0 {
		sb.WriteString("\n**Reserves** (ship, in this order, only in place of a book name you veto):\n")
		for _, i := range reserves {
			line(&sb, i)
		}
	}
	var out []string
	for _, r := range rows {
		why := ""
		switch r.Excluded {
		case excludedNoDirection:
			why = "no scout direction"
		case excludedVetoed:
			var parts []string
			for _, v := range r.Vetoes {
				parts = append(parts, fmt.Sprintf("%s (%s)", v.Source, v.Reason))
			}
			why = "vetoed by " + strings.Join(parts, ", ")
		case excludedRiskGate:
			why = "risk gate: " + r.RiskGate
		default:
			continue
		}
		out = append(out, fmt.Sprintf("- %s %s · %s\n", r.Ticker, r.Direction, why))
	}
	if len(out) > 0 {
		sb.WriteString("\n**Already excluded** (not eligible; rank them in `shadow_rank` all the same):\n")
		for _, l := range out {
			sb.WriteString(l)
		}
	}
	return sb.String()
}

// meritVetoOutcome is what the merit_veto stage hands back to the run.
type meritVetoOutcome struct {
	Ideas    *model.IdeasResult
	Warnings []warning
	Status   model.DomainStatus
	Accepted bool // the Chief's answer parsed and was applied
	Degraded bool
	Record   *model.SelectionRecord
}

// runMeritVeto selects, calls the Chief once for prose and a shadow ranking,
// applies its vetoes, and gates the final book.
func runMeritVeto(ctx context.Context, ch chan<- Event, cfg Config, run *store.Run, reg *agents.Registry, chiefE chiefEngine,
	shortlist []model.Candidate, rows []model.SelectionRow, v verified, reports []agents.ReportContext, quantBlock string, regime []string) meritVetoOutcome {

	topN := meritVetoTopN
	risk := meritVetoRisk(cfg.Risk)
	sectorOf := map[string]string{}
	rowBy := map[string]*model.SelectionRow{}
	for i := range rows {
		t := normTicker(rows[i].Ticker)
		sectorOf[t] = rows[i].Sector
		rowBy[t] = &rows[i]
	}
	rec := &model.SelectionRecord{Policy: model.SelectionMeritVeto, TopN: topN, Regime: regime}

	eligible, validation := meritCandidates(cfg, rows, v)
	for _, r := range rows {
		switch r.Excluded {
		case excludedVetoed:
			var parts []string
			for _, vt := range r.Vetoes {
				parts = append(parts, vt.Source+": "+vt.Reason)
			}
			log(ch, fmt.Sprintf("selection: %s vetoed (%s)", r.Ticker, strings.Join(parts, "; ")))
		case excludedRiskGate:
			log(ch, fmt.Sprintf("selection: %s refused by the risk gate — %s", r.Ticker, r.RiskGate))
		case excludedNoDirection:
			log(ch, fmt.Sprintf("selection: %s has no scout direction", r.Ticker))
		}
	}
	book, _, _ := pickBook(eligible, sectorOf, nil, topN, risk.MaxPerSector, v.Series, risk.MaxPairCorr)
	inBook := map[string]bool{}
	for _, i := range book {
		inBook[normTicker(i.Ticker)] = true
	}
	var reserves []model.TradeIdea
	for _, i := range eligible {
		if len(reserves) == topN {
			break
		}
		if !inBook[normTicker(i.Ticker)] {
			reserves = append(reserves, i)
		}
	}
	listed := map[string]model.TradeIdea{}
	for _, i := range append(append([]model.TradeIdea(nil), book...), reserves...) {
		listed[normTicker(i.Ticker)] = i
	}

	out := meritVetoOutcome{Record: rec}
	var warnings []warning
	prose := map[string][2]string{}
	chiefVetoed := map[string]bool{}
	var chiefNotes string

	prompt, err := reg.AssemblePrompt(agents.PromptParams{
		Role:           "chief-writer",
		Mode:           cfg.Mode,
		RunTS:          run.TS,
		Shortlist:      shortlist,
		Reports:        reports,
		QuantBlock:     quantBlock,
		SelectionBlock: selectionBlock(rows, book, reserves, topN),
		RegimeBlock:    regimeBlock(regime),
	})
	switch {
	case err != nil:
		rec.Chief = "skipped"
		out.Degraded = true
		log(ch, fmt.Sprintf("warn: assemble chief-writer prompt: %v — shipping the merit selection without prose", err))
		out.Status = model.DomainStatus{Domain: "chief-analyst", Status: model.StatusFailed, Err: "prompt assembly failed: " + err.Error()}
	default:
		t := chiefTarget(chiefE, cfg, chiefInitial)
		agentStatus(ch, "chief-analyst", model.StatusRunning, nil)
		r := runAgent(ctx, t.CLI, "chief-analyst", string(t.Stage), prompt, t.Timeout, t.Retry, t.Model, t.Binary, t.API)
		r.Path = fmt.Sprintf("%s/chief-analyst.md", run.Dir)
		if werr := run.WriteReport("chief-analyst", r.Stdout); werr != nil {
			log(ch, fmt.Sprintf("warn: write chief-analyst report: %v", werr))
		}
		out.Status = model.DomainStatus{Domain: "chief-analyst", Status: r.Status, Err: r.Err,
			Duration: r.Duration, Attempts: r.Attempts, Tokens: r.Tokens, Usage: r.Usage}
		if r.Status == model.StatusFailed {
			agentStatus(ch, "chief-analyst", model.StatusFailed, &r)
			rec.Chief = "failed"
			out.Degraded = true
			log(ch, fmt.Sprintf("warn: Chief failed (%s) — shipping the merit selection without prose", r.Err))
			break
		}
		agentStatus(ch, "chief-analyst", model.StatusDone, &r)
		cw, perr := parseChiefWriter(r.Stdout)
		if perr != nil {
			rec.Chief = "unparseable"
			out.Degraded = true
			log(ch, fmt.Sprintf("warn: Chief JSON unparseable (%v) — shipping the merit selection without prose", perr))
			break
		}
		rec.Chief = "accepted"
		out.Accepted = true
		chiefNotes = strings.TrimSpace(cw.Notes)
		for _, p := range cw.Ideas {
			t := normTicker(p.Ticker)
			idea, ok := listed[t]
			if !ok {
				warnings = append(warnings, warning{Ticker: p.Ticker, Message: "the Chief wrote about a name outside the book and its reserves — ignored; the Chief may not add names"})
				continue
			}
			if d := strings.ToUpper(strings.TrimSpace(p.Direction)); d != "" && d != string(idea.Direction) {
				warnings = append(warnings, warning{Ticker: idea.Ticker, Message: fmt.Sprintf("the Chief wrote %s against the scout's %s — the direction is not the Chief's to change; kept %s", d, idea.Direction, idea.Direction)})
			}
			prose[t] = [2]string{strings.TrimSpace(p.Why), strings.TrimSpace(p.PositionNote)}
		}
		for _, vt := range cw.Vetoes {
			t := normTicker(vt.Ticker)
			row, ok := rowBy[t]
			if !ok {
				warnings = append(warnings, warning{Ticker: vt.Ticker, Message: "the Chief vetoed a name that is not on the shortlist — ignored"})
				continue
			}
			reason := model.NormalizeVetoReason(vt.VetoReason)
			if reason == "" {
				warnings = append(warnings, warning{Ticker: row.Ticker, Message: fmt.Sprintf("the Chief vetoed with reason %q, which is not in the closed enum — not a veto", vt.VetoReason)})
				continue
			}
			row.Vetoes = append(row.Vetoes, model.Veto{Source: "chief", Reason: reason, Note: truncateRunes(strings.TrimSpace(vt.Note), labelNoteMax)})
			row.Vetoed = true
			chiefVetoed[t] = true
			log(ch, fmt.Sprintf("selection: the Chief vetoed %s (%s)", row.Ticker, reason))
		}
		seen := map[string]bool{}
		for _, s := range cw.ShadowRank {
			t := normTicker(s)
			if rowBy[t] == nil || seen[t] {
				continue
			}
			seen[t] = true
			rec.ShadowRank = append(rec.ShadowRank, rowBy[t].Ticker)
			rowBy[t].ChiefShadowRank = len(rec.ShadowRank)
		}
		if len(rec.ShadowRank) == 0 {
			warnings = append(warnings, warning{Message: "the Chief returned no usable shadow_rank — the chief-shadow arm gets nothing from this run"})
		}
	}

	final, capped, correlated := pickBook(eligible, sectorOf, chiefVetoed, topN, risk.MaxPerSector, v.Series, risk.MaxPairCorr)
	for i := range final {
		t := normTicker(final[i].Ticker)
		if p, ok := prose[t]; ok && p[0] != "" {
			final[i].Why, final[i].PositionNote = p[0], p[1]
		}
		warnings = append(warnings, validation[t]...)
	}
	res := &model.IdeasResult{
		Mode:        string(cfg.Mode),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Ideas:       final,
		Selection:   model.SelectionMeritVeto,
		ShadowRank:  rec.ShadowRank,
	}
	findings := applyRiskGate(res, v, risk)
	for _, f := range findings {
		msg := f.Message
		if f.Ticker != "" {
			msg = strings.TrimPrefix(msg, f.Ticker+": ")
		}
		log(ch, "risk: "+f.Message)
		warnings = append(warnings, warning{Ticker: f.Ticker, Message: "risk gate: " + msg})
	}
	if dropped := dropViolating(res, findings); len(dropped) > 0 {
		// Every per-idea check already ran on each candidate alone, so this
		// is defensive: a hard finding here means the book's prose or the
		// final gate disagreed with the probe, and the name does not ship.
		for _, d := range dropped {
			log(ch, "dropped: "+d)
		}
	}
	markShipped(rows, res.Ideas)
	for i := range rows {
		r := &rows[i]
		t := normTicker(r.Ticker)
		switch {
		case r.Selected:
			r.Excluded = ""
		case r.Excluded != "":
		case chiefVetoed[t]:
			r.Excluded = excludedVetoed
		case capped[t]:
			r.Excluded = excludedSectorCap
		case correlated[t].With != "":
			r.Excluded = excludedCorrelated
			r.CorrelatedWith, r.Correlation = correlated[t].With, correlated[t].Rho
		default:
			r.Excluded = excludedBelowCut
		}
	}
	rec.Rows = rows
	for _, r := range rows {
		if r.Excluded == excludedCorrelated {
			log(ch, fmt.Sprintf("selection: %s skipped — correlates %.2f with %s already in the book", r.Ticker, r.Correlation, r.CorrelatedWith))
		}
	}

	notes := fmt.Sprintf("Merit-veto selection: the top %d of %d shortlisted names by pre-screen merit after vetoes and the risk gate, at the scout's direction, market-on-open with a %d-session time exit.",
		len(res.Ideas), len(rows), meritVetoHoldDays)
	if !out.Accepted {
		notes += " Degraded: the Chief's prose was unavailable (" + rec.Chief + "); the selection itself is unaffected."
	}
	if chiefNotes != "" {
		notes += " Chief: " + chiefNotes
	}
	res.Notes = notes
	for _, i := range res.Ideas {
		log(ch, fmt.Sprintf("selection: #%d %s %s (merit rank %d)", i.Rank, i.Ticker, i.Direction, rowBy[normTicker(i.Ticker)].MeritRank))
	}
	out.Ideas, out.Warnings = res, warnings
	return out
}
