package orchestrator

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

type researchTimeFacts struct {
	AsOf              string              `json:"as_of"`
	Sessions          []string            `json:"sessions_1_to_15"`
	CalendarEstimated bool                `json:"calendar_estimated"`
	Closures          []string            `json:"known_closures"`
	Events            []researchEventFact `json:"events"`
	EvidenceAges      []evidenceAge       `json:"evidence_ages"`
	Volatility        string              `json:"volatility_units"`
}

type researchEventFact struct {
	Precision         string `json:"precision"`
	Availability      string `json:"availability"`
	Date              string `json:"date"`
	EvidenceID        string `json:"evidence_id,omitempty"`
	Ordering          string `json:"relative_to_run_date"`
	SessionsAway      int    `json:"sessions_away"`
	InsideWindow      bool   `json:"inside_15_session_window"`
	CalendarEstimated bool   `json:"calendar_estimated"`
}

type evidenceAge struct {
	EvidenceID string   `json:"evidence_id"`
	Hours      *float64 `json:"publication_age_hours,omitempty"`
	Note       string   `json:"note,omitempty"`
}

func temporalFacts(r thesisResearch, now time.Time, cal marketdata.ResearchCalendar, qp *quant.Pack) researchTimeFacts {
	ticker := r.Candidate.Ticker
	calendarAnchor := researchCalendarAnchor(ticker, now)
	dates, estimated := cal.Sessions(ticker, calendarAnchor, 15)
	f := researchTimeFacts{AsOf: now.UTC().Format(time.RFC3339), Sessions: dates, CalendarEstimated: estimated,
		Closures: cal.KnownClosures(ticker, calendarAnchor.Format("2006-01-02"), dates[14])}
	addEvent := func(raw, id string) {
		precision := "date_only"
		ordering := ""
		date, err := time.Parse("2006-01-02", raw)
		if err != nil {
			at, e := time.Parse(time.RFC3339, raw)
			if e != nil {
				return
			}
			precision = "timestamp"
			if at.After(now) {
				ordering = "future"
			} else {
				ordering = "past"
			}
			date = at.In(researchLocation(ticker))
			date = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
		}
		localNow := now.In(researchLocation(ticker))
		today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.UTC)
		order := "same date; time of day may be unknown"
		if date.Before(today) {
			order = "past"
		} else if date.After(today) {
			order = "future"
		}
		if ordering != "" {
			order = ordering
		}
		availability := "historical; verify completed price sessions"
		if order == "future" {
			availability = "future outcome unavailable; use conditional scenarios"
		} else if precision == "date_only" && date.Equal(today) {
			availability = "time unknown; event occurrence is not established"
		}
		f.Events = append(f.Events, researchEventFact{Precision: precision, Availability: availability, Date: raw, EvidenceID: id, Ordering: order,
			SessionsAway: cal.SessionsBetween(ticker, today, date), InsideWindow: !date.Before(today) && date.Format("2006-01-02") <= dates[14], CalendarEstimated: estimated})
	}
	if r.NextEvent != "" {
		addEvent(r.NextEvent, "")
	}
	for _, e := range r.Dossier.Events {
		addEvent(e.OccurredAt, e.EvidenceID)
	}
	for _, d := range r.Documents {
		age := evidenceAge{EvidenceID: d.ID}
		if !d.PublishedAt.IsZero() {
			hours := now.Sub(d.PublishedAt).Hours()
			age.Hours = &hours
			if hours < 0 {
				age.Note = "Publication timestamp is after the run anchor; do not treat as known at generation"
			}
		} else {
			age.Note = "Publication time unknown; retrieval time does not date the evidence"
		}
		f.EvidenceAges = append(f.EvidenceAges, age)
	}
	if qp != nil {
		if m, ok := qp.ByTicker[ticker]; ok && m.SigmaDaily > 0 {
			f.Volatility = fmt.Sprintf("Daily sigma %.3f%%; 10-session scale %.3f%%; 15-session scale %.3f%% (daily sigma * sqrt(sessions)). Returns use percent; sigma is a scale, not a forecast. Compare cumulative abnormal returns over matching windows, not z-scores over unequal durations. Last price date %s.", m.SigmaDaily*100, m.SigmaDaily*math.Sqrt(10)*100, m.SigmaDaily*math.Sqrt(15)*100, m.AsOf)
		}
	}
	return f
}

func earningsBlocked(ticker, next string, now time.Time, cal marketdata.ResearchCalendar) bool {
	event, err := time.Parse("2006-01-02", next)
	if err != nil || next < researchCalendarAnchor(ticker, now).Format("2006-01-02") {
		return false
	}
	last, _ := cal.Before(ticker, event)
	minimum, _ := cal.SessionDate(ticker, researchCalendarAnchor(ticker, now), 10)
	return last.Format("2006-01-02") < minimum
}

func (t *thesisRunner) deferForEarnings(out *thesisResearch, qp *quant.Pack) {
	out.Eligibility = model.BlockedEvent
	out.Dossier = model.CandidateDossier{Ticker: out.Candidate.Ticker, Status: "watchlist", EvidenceQuality: "insufficient",
		Hypothesis: "Reassess after verified earnings on " + out.NextEvent + " and observable post-event prices; fewer than ten sessions remain before the event."}
	out.Outcome.Transport = model.OutcomeNotRun
	out.Outcome.Parsing = model.OutcomeNotRun
	out.Outcome.Review = model.OutcomeNotRun
	out.Outcome.Evidence = classifyEvidence(out.Documents)
	out.Outcome.Decision = "watchlist"
	out.Outcome.Notes = append(out.Outcome.Notes, out.Dossier.Hypothesis)
	out.Temporal = temporalFacts(*out, t.run.TS, t.calendar, qp)
	if err := t.run.WriteDataPack(fmt.Sprintf("research-%x", []byte(out.Candidate.Ticker)), out); err != nil {
		out.Errors = append(out.Errors, err.Error())
	}
}

func researchLocation(ticker string) *time.Location {
	zone := "America/New_York"
	suffix := ticker[strings.LastIndex(ticker, ".")+1:]
	switch suffix {
	case "T":
		zone = "Asia/Tokyo"
	case "HK":
		zone = "Asia/Hong_Kong"
	case "TW", "TWO":
		zone = "Asia/Taipei"
	case "KS", "KQ":
		zone = "Asia/Seoul"
	case "SI":
		zone = "Asia/Singapore"
	case "AX":
		zone = "Australia/Sydney"
	case "L":
		zone = "Europe/London"
	case "HE":
		zone = "Europe/Helsinki"
	case "DE", "AS", "PA", "MC", "MI", "BR":
		zone = "Europe/Paris"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func validateEvidenceTime(r *thesisResearch, anchor time.Time) {
	by := map[string]model.EvidenceDocument{}
	for _, d := range r.Documents {
		by[d.ID] = d
	}
	for _, c := range r.Dossier.Claims {
		for _, id := range c.EvidenceIDs {
			if d, ok := by[id]; ok && d.PublishedAt.After(anchor) {
				r.hold("claim " + c.ID + " cites publication after run anchor: " + id)
			}
		}
	}
}

// Calendar methods operate on civil dates. Carry the listing-local date in UTC
// without converting it back across a date boundary inside the calendar.
func researchCalendarAnchor(ticker string, now time.Time) time.Time {
	local := now.In(researchLocation(ticker))
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

func validateClaimsAt(claims []model.ResearchClaim, docs []model.EvidenceDocument, ticker string, anchor time.Time) []string {
	problems := validateClaims(claims, docs, ticker)
	by := map[string]model.EvidenceDocument{}
	for _, d := range docs {
		by[d.ID] = d
	}
	for _, c := range claims {
		ids := append([]string(nil), c.EvidenceIDs...)
		if c.Comparison != nil && c.Comparison.RatioEvidenceID != "" {
			ids = append(ids, c.Comparison.RatioEvidenceID)
		}
		for _, id := range ids {
			if d, ok := by[id]; ok && d.PublishedAt.After(anchor) {
				problems = appendUnique(problems, "claim "+c.ID+" cites publication after run anchor: "+id)
			}
		}
	}
	return problems
}
