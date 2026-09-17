package orchestrator

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

func validateClaims(claims []model.ResearchClaim, docs []model.EvidenceDocument, ticker string) []string {
	by := map[string]model.EvidenceDocument{}
	for _, d := range docs {
		if d.Ticker == ticker && d.Error == "" {
			by[d.ID] = d
		}
	}
	var errs []string
	seen := map[string]bool{}
	for _, c := range claims {
		if c.ID == "" || seen[c.ID] || strings.TrimSpace(c.Text) == "" || (c.Kind != "observation" && c.Kind != "inference") {
			errs = append(errs, "invalid or duplicate claim "+c.ID)
		}
		seen[c.ID] = true
		if len(c.EvidenceIDs) == 0 {
			errs = append(errs, "claim has no evidence: "+c.ID)
		}
		for _, id := range c.EvidenceIDs {
			if _, ok := by[id]; !ok {
				errs = append(errs, "unknown/unavailable evidence "+id)
			}
		}
	}
	return errs
}
func validateDossier(r *thesisResearch) {
	d := &r.Dossier
	problems := validateClaims(d.Claims, r.Documents, r.Candidate.Ticker)
	problems = append(problems, reasoningReferences(*d)...)
	problems = append(problems, validateClaimPassages(d.Claims, r.Documents)...)
	for _, s := range []string{d.LongCase, d.ShortCase, d.NoTradeCase} {
		if strings.TrimSpace(s) == "" {
			problems = appendUnique(problems, "explicit long, short and no-trade reasoning required")
		}
	}
	if d.PreferredDirection != "BUY" && d.PreferredDirection != "SELL" && d.PreferredDirection != "NONE" {
		problems = append(problems, "preferred_direction must be BUY, SELL or NONE")
	}
	if d.Status == "supported" && d.PreferredDirection == "NONE" {
		problems = append(problems, "no supported trade direction")
	}
	if d.Ticker != r.Candidate.Ticker {
		problems = append(problems, "dossier issuer mismatch")
	}
	if d.Status != "supported" && d.Status != "watchlist" && d.Status != "rejected" {
		problems = append(problems, "invalid dossier status")
	}
	if len(d.Requests) > 0 {
		problems = append(problems, "research requests remain unanswered")
	}
	for _, s := range []string{d.Hypothesis, d.Changed, d.Expectations, d.Underappreciated, d.Mechanism, d.PricedIn, d.Counterargument, d.Invalidation, d.CatalystWindow} {
		if strings.TrimSpace(s) == "" {
			problems = append(problems, "incomplete thesis")
			break
		}
	}
	if len(d.Claims) == 0 {
		problems = append(problems, "no grounded claims")
	}
	if d.EvidenceQuality != "strong" && d.EvidenceQuality != "mixed" {
		problems = append(problems, "insufficient evidence quality")
	}
	cited := map[string]bool{}
	for _, c := range d.Claims {
		for _, id := range c.EvidenceIDs {
			cited[id] = true
		}
	}
	substantive := false
	for _, doc := range r.Documents {
		if cited[doc.ID] && marketdata.SubstantiveResearchDocument(doc) {
			substantive = true
		}
	}
	if !substantive {
		problems = append(problems, "no cited source document; headline/factor evidence alone is insufficient")
	}
	for _, s := range problems {
		found := false
		for _, u := range d.Unresolved {
			if s == u {
				found = true
			}
		}
		if !found {
			d.Unresolved = append(d.Unresolved, s)
		}
	}
	if len(d.Unresolved) > 0 && d.Status != "rejected" {
		d.Status = "watchlist"
	}
}
func validateThesisResult(res *model.IdeasResult, research []thesisResearch, v verified, cfg Config, now time.Time, cal marketdata.ResearchCalendar) []riskFinding {
	var out []riskFinding
	by := map[string]thesisResearch{}
	for _, r := range research {
		by[r.Candidate.Ticker] = r
	}
	seen := map[string]bool{}
	max := 5
	if cfg.Mode == model.ModeSingle {
		max = 1
	}
	res.Mode = string(cfg.Mode)
	res.GeneratedAt = now.UTC().Format(time.RFC3339)
	res.SchemaVersion = 2
	res.ResearchMode = "thesis"
	for i := range res.Ideas {
		idea := &res.Ideas[i]
		idea.Ticker = strings.ToUpper(strings.TrimSpace(idea.Ticker))
		ticker := idea.Ticker
		hard := func(s string) { out = append(out, riskFinding{Ticker: ticker, Hard: true, Message: ticker + ": " + s}) }
		r, ok := by[ticker]
		if !ok {
			hard("not a researched candidate")
			continue
		}
		if seen[ticker] {
			hard("duplicate idea")
		}
		seen[ticker] = true
		if i >= max {
			hard("exceeds maximum idea count")
		}
		if r.Dossier.Status != "supported" || r.Challenge.Verdict != "supported" || len(r.Challenge.MaterialIssues) > 0 || len(r.Challenge.Requests) > 0 {
			hard("thesis or challenge unresolved")
		}
		if r.Eligibility != "" {
			out = append(out, riskFinding{Ticker: ticker, Hard: true, Blocked: r.Eligibility, Message: ticker + ": " + r.Dossier.Hypothesis})
		}
		if idea.Thesis != nil {
			for _, problem := range targetProvenanceProblems(r.Dossier, *idea.Thesis) {
				hard(problem)
			}
		}
		idea.Name = r.Candidate.Name
		idea.Index = r.Candidate.Index
		idea.Setup = r.Candidate.Setup
		idea.Rank = i + 1
		idea.ResearchMode = "thesis"
		idea.Confidence = 0
		idea.BaseConfidence = 0
		idea.DomainScores = nil
		idea.Consensus = 0
		if idea.Direction != model.DirectionBuy && idea.Direction != model.DirectionSell {
			hard("invalid trade direction")
		}
		if string(idea.Direction) != r.Dossier.PreferredDirection {
			hard("selected direction has not passed the dossier's independent review")
		}
		if idea.TimeframeDays == 0 {
			idea.TimeframeDays = 15
		}
		if idea.TimeframeDays < 10 || idea.TimeframeDays > 15 {
			hard("holding window must be 10–15 sessions")
			continue
		}
		if idea.Thesis == nil {
			hard("missing thesis and execution reasoning")
			continue
		}
		th := idea.Thesis
		for _, condition := range r.Dossier.EntryConditions {
			th.Prerequisites = appendUnique(th.Prerequisites, condition)
		}
		th.Monitoring = append([]string(nil), r.Dossier.Monitoring...)
		for _, s := range []string{idea.Why, th.WhyNow, th.Invalidation, th.CatalystWindow, th.EntryReason, th.StopReason, th.TargetReason} {
			if strings.TrimSpace(s) == "" {
				hard("incomplete thesis or level reasoning")
				break
			}
		}
		if len(th.EvidenceIDs) == 0 {
			hard("no cited evidence")
		}
		claims := []model.ResearchClaim{{ID: "selection", Kind: "inference", Text: idea.Why, EvidenceIDs: th.EvidenceIDs}}
		for _, s := range validateClaimsAt(claims, r.Documents, ticker, now) {
			hard(s)
		}
		th.EvidenceQuality = r.Dossier.EvidenceQuality
		if th.OutcomeLow <= 0 || th.OutcomeHigh <= th.OutcomeLow || idea.Target < th.OutcomeLow || idea.Target > th.OutcomeHigh {
			hard("target outside supported outcome range")
		}
		if !((idea.Direction == model.DirectionBuy && idea.Stop < idea.Entry && idea.Entry < idea.Target) || (idea.Direction == model.DirectionSell && idea.Target < idea.Entry && idea.Entry < idea.Stop)) {
			hard("invalid entry/stop/target ordering")
		}
		risk := math.Abs(idea.Entry - idea.Stop)
		if risk > 0 {
			idea.RiskReward = math.Round(math.Abs(idea.Target-idea.Entry)/risk*100) / 100
		}
		m, have := quantFor(v, ticker)
		if !have || m.LastClose <= 0 || m.SigmaDaily <= 0 {
			hard("no verified price/volatility")
		}
		if staleFor(v, ticker) {
			hard("stale price; cannot publish actionable levels")
		}
		idea.PriceAtGeneration = m.LastClose
		th.ExpiresOn, th.CalendarEstimated = cal.SessionDate(ticker, researchCalendarAnchor(ticker, now), idea.TimeframeDays)
		th.EntryExpiresOn, _ = cal.SessionDate(ticker, researchCalendarAnchor(ticker, now), 3)
		// A verified event is an absolute cutoff, not an invitation to assume that
		// a delayed fill can be carried into earnings.
		if r.NextEvent != "" && r.NextEvent <= th.ExpiresOn {
			d, e := time.Parse("2006-01-02", r.NextEvent)
			if e == nil {
				last, estimated := cal.Before(ticker, d)
				th.ExpiresOn = last.Format("2006-01-02")
				th.CalendarEstimated = th.CalendarEstimated || estimated
				th.Prerequisites = appendUnique(th.Prerequisites, "Exit before verified earnings on "+r.NextEvent)
				minimum, _ := cal.SessionDate(ticker, researchCalendarAnchor(ticker, now), 10)
				if th.ExpiresOn < minimum {
					hard("verified earnings leave fewer than 10 sessions for the thesis")
				}
			}
		}
		if th.EntryExpiresOn > th.ExpiresOn {
			th.EntryExpiresOn = th.ExpiresOn
		}
		if th.ExpiresOn <= now.Format("2006-01-02") {
			hard("thesis expires before a trade can be entered")
		}
		if r.NextEvent == "" {
			th.Prerequisites = appendUnique(th.Prerequisites, "Verify the earnings/event calendar before entry; no date was retrieved")
		}
		if th.CalendarEstimated {
			th.Prerequisites = appendUnique(th.Prerequisites, "Verify projected exchange session dates and closures before entry")
		}
		if idea.Direction == model.DirectionSell {
			th.Prerequisites = appendUnique(th.Prerequisites, "Verify borrow availability and total borrow costs before short entry")
		}
		idea.Status = "actionable"
		if len(th.Prerequisites) > 0 {
			idea.Status = "conditional"
		}
		// Preserve the asymmetric patient/chase limits without legacy sigma floors.
		dev := math.Abs(idea.Entry - m.LastClose)
		band := cfg.Risk.EntryChaseSigma
		if isPatientEntry(idea.Direction, idea.Entry-m.LastClose) {
			band = cfg.Risk.EntryPatienceSigma
		}
		if have && dev > band*m.SigmaDaily*math.Sqrt(5)*m.LastClose {
			hard("entry exceeds patient/chase band")
		}
	}
	out = append(out, applyRiskGate(res, v, cfg.Risk)...)
	for _, idea := range res.Ideas {
		if idea.Shares <= 0 {
			out = append(out, riskFinding{Ticker: idea.Ticker, Hard: true, Message: idea.Ticker + ": cannot size a whole-share position with verified FX and risk budget"})
		}
	}
	return out
}

// researchFailureNote states, in one line, which half of the pipeline failed.
// It replaces a challenge reason that either does not exist or describes a
// review that never ran.
func researchFailureNote(r thesisResearch) string {
	switch {
	case r.Outcome.Transport == model.OutcomeFailed:
		return "Research did not complete: a model call failed. This is a pipeline failure, not a finding about the company."
	case r.Outcome.Contract == model.OutcomeFailed:
		return fmt.Sprintf("Research did not complete: response budget or compaction validation failed (%d compaction attempts). This is a pipeline failure, not a finding about the company.", r.Outcome.CompactionAttempts)
	case r.Outcome.Parsing == model.OutcomeFailed:
		return fmt.Sprintf("Research did not complete: the response did not meet the JSON/schema contract (%d repair attempts). This is a pipeline failure, not a finding about the company.", r.Outcome.RepairAttempts)
	case r.Outcome.Evidence == model.EvidenceNone:
		return "Research did not complete: retrieval reached no usable source for this company."
	case r.Outcome.Review == model.ReviewUnavailable:
		return "Research did not complete: the independent challenge did not return a verdict."
	}
	return ""
}

func appendUnique(xs []string, s string) []string {
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}
func finalizeThesis(res *model.IdeasResult, research []thesisResearch, findings []riskFinding, v verified, cfg Config, now time.Time) {
	// Both halves of a dropped idea travel together: why it was dropped in
	// words, and what kind of failure that was. A plan whose review call never
	// returned is not a plan the risk gate turned down.
	type block struct{ reason, kind string }
	bad := map[string]block{}
	bookBad := false
	for _, f := range findings {
		if f.Observational {
			continue
		}
		if f.Ticker == "" {
			bookBad = true
			continue
		}
		kind := f.Blocked
		if kind == "" {
			kind = model.BlockedRisk
		}
		bad[f.Ticker] = block{reason: f.Message, kind: kind}
	}
	// Book findings cannot be ignored just because they have no ticker. Keep the
	// model's preference order and remove the lowest-ranked plan until valid.
	kept := []model.TradeIdea{}
	for _, idea := range res.Ideas {
		if _, no := bad[idea.Ticker]; !no {
			kept = append(kept, idea)
		}
	}
	res.Ideas = kept
	if bookBad {
		for len(res.Ideas) > 0 {
			fs := gateBook(res, v, cfg.Risk)
			broken := false
			for _, f := range fs {
				if !f.Observational {
					broken = true
				}
			}
			if !broken {
				break
			}
			i := len(res.Ideas) - 1
			bad[res.Ideas[i].Ticker] = block{reason: "portfolio risk limits", kind: model.BlockedRisk}
			res.Ideas = res.Ideas[:i]
		}
	}
	decisions := map[string]model.SelectionDecision{}
	for _, d := range res.Decisions {
		d.Ticker = strings.ToUpper(strings.TrimSpace(d.Ticker))
		decisions[d.Ticker] = d
	}
	res.Decisions = nil
	for _, r := range research {
		ticker := r.Candidate.Ticker
		d, ok := decisions[ticker]
		if !ok || strings.TrimSpace(d.Reason) == "" {
			d = model.SelectionDecision{Ticker: ticker, Status: "watchlist", Reason: r.Dossier.Hypothesis}
			if d.Reason == "" {
				d.Reason = "No supported research conclusion"
			}
		}
		if d.Status != "rejected" {
			d.Status = "watchlist"
		}
		validIDs := map[string]bool{}
		for _, doc := range r.Documents {
			if doc.Ticker == ticker && doc.Error == "" {
				validIDs[doc.ID] = true
			}
		}
		ids := []string{}
		for _, id := range d.EvidenceIDs {
			if validIDs[id] {
				ids = appendUnique(ids, id)
			}
		}
		d.EvidenceIDs = ids
		// The independent challenge keeps its own words in its own field. It
		// used to be written over the selector's reason, which put back into the
		// final output timing claims the selector had already corrected — and
		// did so on nine of twelve names in the 2026-09-07 run.
		if r.Challenge.Reason != "" && r.Challenge.Verdict != "supported" {
			d.ReviewReason = r.Challenge.Reason
		}
		switch {
		case r.Eligibility == model.BlockedEvent:
			d.Status = "watchlist"
			d.Blocked = model.BlockedEvent
			d.Reason = r.Dossier.Hypothesis
		case r.researchFailed():
			// Research that never completed cannot be a verdict on the company.
			// Claude read BSX's failure correctly and said watchlist; Go then
			// overwrote that with "rejected". The selector's status stands, and
			// the failure is named in Blocked rather than smuggled into it.
			if d.Status == "rejected" {
				d.Status = "watchlist"
			}
			d.Blocked = model.BlockedResearchFailure
			if note := researchFailureNote(r); note != "" {
				d.ReviewReason = note
				if d.Reason == "" {
					d.Reason = note
				}
			}
		case r.Dossier.Status == "rejected":
			d.Status = "rejected"
			d.Blocked = model.BlockedReviewReject
			if r.Challenge.Reason != "" {
				d.ReviewReason = r.Challenge.Reason
			}
		}
		if b, no := bad[ticker]; no {
			d.Reason = b.reason
			d.Blocked = b.kind
			d.Status = "rejected"
			if b.kind == model.BlockedResearchFailure || b.kind == model.BlockedEvent {
				// A plan that lost its review to a pipeline failure is not a
				// company the run rejected; it is one the run could not finish.
				d.Status = "watchlist"
			}
		}
		if r.Eligibility == model.BlockedEvent || r.Eligibility == model.BlockedPrices {
			d.Status = "watchlist"
			d.Blocked = r.Eligibility
			if r.Eligibility == model.BlockedEvent {
				d.Reason = r.Dossier.Hypothesis
			} else {
				d.Reason = "Reassess after sufficient post-event prices and aligned benchmark bars are available; announcement repricing has not been verified."
			}
		}
		for i := range res.Ideas {
			idea := &res.Ideas[i]
			idea.Rank = i + 1
			if idea.Ticker == ticker {
				d.Status = idea.Status
				d.Reason = idea.Why
				d.Blocked = ""
				d.EvidenceIDs = idea.Thesis.EvidenceIDs
			}
		}
		res.Decisions = append(res.Decisions, d)
	}
	res.Mode = string(cfg.Mode)
	res.GeneratedAt = now.Format(time.RFC3339)
	res.SchemaVersion = 2
	res.ResearchMode = "thesis"
	if res.Ideas == nil {
		res.Ideas = []model.TradeIdea{}
	}
	res.Notes = strings.TrimSpace(res.Notes + " Simulated expectancy is a diagnostic under an assumed edge, not a stock-specific return forecast. Conditional plans require the listed checks before entry.")
}
