package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/parse"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
)

const (
	// postMortemMaxAge is how stale stored lessons may be before a run redraws
	// them. It matches the calibration's: they are drawn from the same replay,
	// and there is nothing new to say until that replay changes.
	postMortemMaxAge = calibrationMaxAge
	// postMortemTimeout bounds the one model call. A run never waits on this: a
	// retrospective is the most optional thing in the pipeline, and holding up
	// today's ideas for a reading of last month's is the wrong trade.
	postMortemTimeout = 3 * time.Minute
	// postMortemTrades is how many closed trades reach the prompt. Newest first,
	// because a lesson drawn from the current pipeline is worth more than one
	// drawn from a version of it that no longer exists.
	postMortemTrades = 40
)

// postMortemResult is what a run's post-mortem stage produced, for the metadata
// and for the Chief's prompt.
type postMortemResult struct {
	PM     *scoreboard.PostMortem
	Status model.DomainStatus
	// Report is the markdown to persist as runs/<ts>/post-mortem.md, or empty
	// when nothing ran.
	Report string
}

// postMortem draws lessons from the pipeline's own closed trades.
//
// The split is deliberate and matches every other domain in this system: Go
// counts, the model reads. The attribution tables are arithmetic over replayed
// trades and are ground truth; the agent's only job is the part counting cannot
// do — reading the reasoning behind the winners and the losers and saying what
// separated them. Its output is then enforced against those same tables, so a
// lesson naming a cell that does not exist is deleted exactly as a specialist's
// score for a name it had no data on is deleted.
//
// It never fails a run. With too thin a record, a failed call, or an unparseable
// answer, the Chief is simply not told any lessons — which is the state every
// fresh install starts in.
func postMortem(ctx context.Context, ch chan<- Event, cfg Config, reg *agents.Registry,
	p *pool, cheapCLI model.CLI, caps agents.Capabilities, yc marketdata.PriceSource) postMortemResult {

	if stored := scoreboard.LoadPostMortem(cfg.DataDir); stored != nil && stored.Age() < postMortemMaxAge {
		return postMortemResult{PM: stored}
	}

	rctx, cancel := context.WithTimeout(ctx, calibrationRefreshTimeout)
	sum, err := scoreboard.Replay(rctx, cfg.RunsDir, yc, cfg.FillWindowDays)
	cancel()
	if err != nil {
		log(ch, fmt.Sprintf("warn: could not replay past ideas for the post-mortem: %v", err))
		return postMortemResult{}
	}
	attr := scoreboard.Attribute(sum)
	if attr == nil || attr.NClosed < scoreboard.MinClosedForPostMortem {
		n := 0
		if attr != nil {
			n = attr.NClosed
		}
		log(ch, fmt.Sprintf("post-mortem: %d of %d closed ideas — too thin to draw a lesson from, so none is drawn",
			n, scoreboard.MinClosedForPostMortem))
		return postMortemResult{}
	}

	attrBlock := scoreboard.AttributionBlock(attr)
	if attrBlock == "" {
		log(ch, fmt.Sprintf("post-mortem: no cell has the %d closed trades a lesson needs", scoreboard.MinCellN))
		return postMortemResult{}
	}

	prompt, err := reg.AssemblePrompt(agents.PromptParams{
		Role:              "post-mortem",
		Mode:              cfg.Mode,
		RunTS:             time.Now(),
		Caps:              caps,
		AttributionBlock:  attrBlock,
		ClosedTradesBlock: scoreboard.ClosedTradesBlock(sum, postMortemTrades),
	})
	if err != nil {
		// A missing persona is a configuration fact, not a failure: agents.v1
		// has no post-mortem.md, so the A/B control arm simply runs without one.
		log(ch, fmt.Sprintf("post-mortem: not run (%v)", err))
		return postMortemResult{}
	}

	log(ch, fmt.Sprintf("Drawing lessons from %d closed ideas…", attr.NClosed))
	agentStatus(ch, "post-mortem", model.StatusRunning, nil)
	r := <-p.submit(cheapCLI, "post-mortem", string(model.StageAnalysis), prompt, postMortemTimeout, cfg.Retry)

	st := model.DomainStatus{
		Domain: "post-mortem", Status: r.Status, Err: r.Err,
		Duration: r.Duration, Attempts: r.Attempts, Tokens: r.Tokens, Usage: r.Usage, Grounded: true,
	}
	if r.Status == model.StatusFailed {
		agentStatus(ch, "post-mortem", model.StatusFailed, &r)
		log(ch, fmt.Sprintf("warn: the post-mortem call failed (%v) — the run continues without lessons", r.Err))
		return postMortemResult{Status: st}
	}

	tail, ok := parse.LastJSONBlock(r.Stdout)
	if !ok {
		st.Status = model.StatusFailed
		st.Err = "no JSON tail in the post-mortem report"
		agentStatus(ch, "post-mortem", model.StatusFailed, &r)
		log(ch, "warn: the post-mortem returned no structured tail — the run continues without lessons")
		return postMortemResult{Status: st, Report: r.Stdout}
	}
	pm, err := scoreboard.ParsePostMortem(tail, attr)
	if err != nil {
		st.Status = model.StatusFailed
		st.Err = err.Error()
		agentStatus(ch, "post-mortem", model.StatusFailed, &r)
		log(ch, fmt.Sprintf("warn: unparseable post-mortem tail (%v) — the run continues without lessons", err))
		return postMortemResult{Status: st, Report: r.Stdout}
	}

	st.ScoredNames = len(pm.Lessons) + len(pm.Rejected)
	// A deleted lesson is reported the way a deleted score is. The failure mode
	// here is not a wrong number, it is a confident sentence about the system's
	// own performance that nothing counted.
	for _, why := range pm.Rejected {
		log(ch, "post-mortem: dropped "+why)
	}
	if len(pm.Rejected) > 0 {
		st.CorrectedScores = append(st.CorrectedScores, pm.Rejected...)
	}
	for _, w := range pm.WeightSuggestions {
		log(ch, fmt.Sprintf("post-mortem suggests weighting %s %s — %s (advisory; change [weights] yourself if you agree)",
			w.Domain, w.Direction, w.Reason))
	}
	log(ch, fmt.Sprintf("post-mortem: %d lesson(s) kept from %d closed ideas", len(pm.Lessons), pm.NClosed))
	agentStatus(ch, "post-mortem", model.StatusDone, &r)

	if err := pm.Save(cfg.DataDir); err != nil {
		log(ch, fmt.Sprintf("warn: could not store the post-mortem: %v", err))
	}
	return postMortemResult{PM: pm, Status: st, Report: r.Stdout}
}
