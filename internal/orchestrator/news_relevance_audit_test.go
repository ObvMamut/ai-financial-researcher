package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/store"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// avHeadlineLabelRE matches alphavantage.go's per-headline fact label,
// "Headline N (relevance R.RR, sentiment ...)", and captures the relevance.
var avHeadlineLabelRE = regexp.MustCompile(`^Headline \d+ \(relevance ([\d.]+), sentiment`)

// TestNewsRelevanceAcceptanceAudit re-derives, from runs already on disk, how
// many shipped ideas' news coverage would change under the current
// subject-relevance rule (newsfilter.go's isSubjectRelevant, and — since A1 —
// alphavantage.go's articlesFor and AVRelevanceFloor). It is gated behind
// CFR_NEWS_RELEVANCE_RUNS_DIR because it reads an operator's own runs/
// directory, not a fixture, and it asserts nothing: it exists to reproduce
// the numbers docs/research/2026-09-25-news-relevance.md reports (and its
// 2026-09-27 addendum), on demand, the next time the rule changes — the
// original version of this audit was a one-off script that was never
// committed, which is the "test drift" this branch is named for.
//
// Method, per shipped idea with a `news` domain_scores entry (independent
// mode only — single-stock never gates on the evidence floor, matching
// applyRiskGate in riskgate.go):
//   - A saved fact labelled "Headline N (relevance R, sentiment ...)" is an
//     AlphaVantage item. Before A1, ANY such fact counted as coverage, with
//     no relevance threshold at all. It counts now only if isSubjectRelevant
//     reads its headline/summary as naming the company, or its relevance
//     clears marketdata.AVRelevanceFloor.
//   - A saved fact labelled "... (tagged to this ticker)" is an Alpaca/Yahoo
//     item from before that feed's own subject-relevance fix (F3,
//     2026-09-25); every run saved so far predates the fix, so this is still
//     the label the old rule used for "counts as coverage" there too. It
//     counts now only if isSubjectRelevant reads it the same way.
//   - Any other label ("News Sentiment Score", "Next earnings", "US line",
//     or a post-fix "... (context, not about this company)") is metadata or
//     an already-non-subject item — never evidence of coverage either way,
//     under the old rule or the new one.
//
// If every one of a ticker's facts loses coverage under the new rule while at
// least one had it under the old rule, `news` is dropped from a copy of that
// idea's domain_scores and the real checkPriceOnlyEvidence (riskgate.go,
// unexported, called directly since this test lives in the same package) is
// run against what remains.
//
// Limit, carried over unchanged from the 2026-09-25 audit: a saved fact keeps
// only the rendered headline/summary, never the provider's raw tag list
// (Alpaca's symbols, Yahoo's relatedTickers, AlphaVantage's ticker_sentiment
// array), so isSubjectRelevant's own "<=3 symbols" branch — a short tag list
// is informative on its own — cannot be re-derived from disk. Padding the
// symbols list this test builds to length 4 (the real ticker plus three
// placeholders) keeps the tag-membership gate open while forcing every check
// through the real text match instead of that branch's free pass. This makes
// the audit conservative — an upper bound on how many ideas would flip, not
// an exact replay — exactly the prior round's own stated bias.
func TestNewsRelevanceAcceptanceAudit(t *testing.T) {
	dir := os.Getenv("CFR_NEWS_RELEVANCE_RUNS_DIR")
	if dir == "" {
		t.Skip("set CFR_NEWS_RELEVANCE_RUNS_DIR to a runs/ directory to run this audit")
	}

	uni, err := universe.Load()
	if err != nil {
		t.Fatalf("load universe: %v", err)
	}
	// The same ctx wiring orchestrator.go does once per live run (see its
	// WithCompanyNames call): the company name/alias lookup the text rule
	// reads.
	ctx := marketdata.WithCompanyNames(context.Background(), func(ticker string) []string {
		var names []string
		if c, ok := uni.Lookup(ticker); ok && c.Name != "" {
			names = append(names, c.Name)
		}
		return append(names, universe.AliasesFor(ticker)...)
	})
	textRelevant := func(ticker, headline, summary string) bool {
		return marketdata.IsSubjectRelevant(ctx, []string{ticker, "__pad1__", "__pad2__", "__pad3__"}, headline, summary, ticker)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	var runsSeen, newsScored, flipped, evidenceFloorFails int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		runDir := filepath.Join(dir, e.Name())
		ideas, err := store.LoadIdeas(runDir)
		if err != nil || ideas == nil || len(ideas.Ideas) == 0 {
			continue
		}
		var pack marketdata.DataPack
		if err := store.ReadDataPack(runDir, "news", &pack); err != nil {
			continue
		}
		runsSeen++
		single := model.Mode(ideas.Mode) == model.ModeSingle
		for i := range ideas.Ideas {
			idea := &ideas.Ideas[i]
			if _, ok := idea.DomainScores["news"]; !ok {
				continue
			}
			newsScored++
			if single {
				continue
			}
			td, ok := pack.ByTicker[idea.Ticker]
			if !ok {
				continue
			}
			oldCovered, newCovered := reDeriveNewsCoverage(td, idea.Ticker, textRelevant)
			if !oldCovered || newCovered {
				continue
			}
			flipped++
			scores := map[string]int{}
			for k, v := range idea.DomainScores {
				if k != "news" {
					scores[k] = v
				}
			}
			probe := model.TradeIdea{Ticker: idea.Ticker, DomainScores: scores}
			if msg := checkPriceOnlyEvidence(&probe); msg != "" {
				evidenceFloorFails++
				t.Logf("FLIP+FLOOR %s %s: %s", e.Name(), idea.Ticker, msg)
			} else {
				t.Logf("FLIP %s %s: domain_scores now %v, still clears the evidence floor", e.Name(), idea.Ticker, scores)
			}
		}
	}

	t.Logf("news relevance acceptance audit: %d runs with both ideas.json and data/news.json, "+
		"%d news-scored shipped ideas, %d flip to uncovered, %d of those then fail the evidence floor",
		runsSeen, newsScored, flipped, evidenceFloorFails)
}

// reDeriveNewsCoverage reads one ticker's saved news facts and reports
// whether the ticker counted as covered under the rule that produced them
// (oldCovered) and under the current rule (newCovered). See
// TestNewsRelevanceAcceptanceAudit's doc comment for what each label means.
func reDeriveNewsCoverage(td marketdata.TickerData, ticker string, textRelevant func(ticker, headline, summary string) bool) (oldCovered, newCovered bool) {
	for _, f := range td.Facts {
		title := f.Value
		if i := strings.Index(title, " — "); i > 0 {
			title = title[:i]
		}
		if m := avHeadlineLabelRE.FindStringSubmatch(f.Label); m != nil {
			oldCovered = true
			rel, err := strconv.ParseFloat(m[1], 64)
			if err == nil && (rel >= marketdata.AVRelevanceFloor || textRelevant(ticker, title, f.Summary)) {
				newCovered = true
			}
			continue
		}
		if strings.HasSuffix(f.Label, "(tagged to this ticker)") {
			oldCovered = true
			if textRelevant(ticker, title, f.Summary) {
				newCovered = true
			}
		}
	}
	return oldCovered, newCovered
}
