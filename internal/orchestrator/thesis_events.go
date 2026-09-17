package orchestrator

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// releaseSession maps actual release timestamps to the market session that can
// first price them. Unsupported market hours return zero rather than inventing
// US session timing for a foreign release. Regular closes omit early closures.
func releaseSession(ticker string, at time.Time) time.Time {
	date, _ := releaseSessionCalendar(ticker, at, marketdata.ResearchCalendar{})
	return date
}

func releaseSessionCalendar(ticker string, at time.Time, cal marketdata.ResearchCalendar) (time.Time, bool) {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	zone := "America/New_York"
	closeHour, closeMinute := 16, 0
	suffix := ""
	if i := strings.LastIndex(ticker, "."); i >= 0 {
		suffix = ticker[i+1:]
	}
	switch suffix {
	case "", "A", "B": // US tickers and share classes
	case "T":
		zone = "Asia/Tokyo"
		closeHour = 15
		closeMinute = 30
	case "HK":
		zone = "Asia/Hong_Kong"
	case "TW", "TWO":
		zone = "Asia/Taipei"
		closeHour = 13
		closeMinute = 30
	case "KS", "KQ":
		zone = "Asia/Seoul"
		closeHour = 15
		closeMinute = 30
	case "SI":
		zone = "Asia/Singapore"
		closeHour = 17
	case "L":
		zone = "Europe/London"
		closeHour = 16
		closeMinute = 30
	case "DE", "AS", "PA", "MC", "MI", "BR", "HE":
		zone = "Europe/Paris"
		closeHour = 17
		closeMinute = 30
	default:
		return time.Time{}, true
	}
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return time.Time{}, true
	}
	local := at.In(loc)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	close := closeHour*60 + closeMinute
	if early, ok := cal.EarlyCloseMinutes(ticker, date.Format("2006-01-02")); ok {
		close = early
	}
	if local.Hour()*60+local.Minute() >= close {
		date = date.AddDate(0, 0, 1)
	}
	return cal.OnOrAfter(ticker, date)
}
func addReleaseReactions(r *thesisResearch, pack *quant.Pack, series map[string]*quant.Series, now time.Time, calendars ...marketdata.ResearchCalendar) {
	cal := marketdata.ResearchCalendar{}
	if len(calendars) > 0 {
		cal = calendars[0]
	}
	if pack == nil {
		return
	}
	m, ok := pack.ByTicker[r.Candidate.Ticker]
	if !ok {
		return
	}
	docs := map[string]model.EvidenceDocument{}
	for _, d := range r.Documents {
		docs[d.ID] = d
	}
	for _, ev := range r.Dossier.Events {
		source, ok := docs[ev.EvidenceID]
		at, e := time.Parse(time.RFC3339, ev.OccurredAt)
		invalid := !ok || e != nil || at.After(now) || ev.Kind != "earnings" || source.Kind != "document" || source.Error != "" || len(ev.Passage) < 30 || !strings.Contains(source.Text, ev.Passage)
		// Exact source quotation is necessary, but not sufficient: the independent
		// challenger still verifies that it describes an earnings announcement.
		if !invalid {
			dateFound := false
			for _, layout := range []string{"2006-01-02", "January 2, 2006", "January 02, 2006", "Jan 2, 2006", "2 January 2006"} {
				if strings.Contains(strings.ToLower(ev.Passage), strings.ToLower(at.Format(layout))) {
					dateFound = true
				}
			}
			invalid = !dateFound
		}
		if invalid {
			r.Dossier.Unresolved = appendUnique(r.Dossier.Unresolved, "Unverified announcement timestamp: "+ev.OccurredAt)
			r.Dossier.Status = "watchlist"
			continue
		}
		bench := series[m.Benchmark]
		if bench == nil {
			r.Dossier.Unresolved = appendUnique(r.Dossier.Unresolved, "No benchmark for abnormal earnings reaction")
			r.Dossier.Status = "watchlist"
			continue
		}
		session, estimated := releaseSessionCalendar(r.Candidate.Ticker, at, cal)
		if session.IsZero() {
			r.Dossier.Unresolved = appendUnique(r.Dossier.Unresolved, "Unverified market hours for announcement reaction")
			r.Dossier.Status = "watchlist"
			continue
		}
		d, ok := computeDrift(series[r.Candidate.Ticker], bench, session, m.SigmaDaily)
		if !ok || !releasePricesAligned(series[r.Candidate.Ticker], bench, session) {
			r.Eligibility = model.BlockedPrices
			r.Dossier.Unresolved = appendUnique(r.Dossier.Unresolved, "Awaiting sufficient post-event prices and aligned benchmark bars for announcement reaction")
			r.Dossier.Status = "watchlist"
			continue
		}
		body := fmt.Sprintf("Announcement %s, source %s; first priceable session %s (calendar estimated=%t). Two-session abnormal reaction %.2f sigma, cumulative abnormal reaction %.2f%%, cumulative abnormal move since %.2f%%, fully retraced=%t. %d sessions since. Announcement attribution requires the independent source challenge; this is not a reaction to the quarterly filing date.", ev.OccurredAt, ev.EvidenceID, d.Date, estimated, d.GapZ, 100*math.Expm1(d.gapLog), 100*math.Expm1(d.postLog), d.retraced(), d.Sessions)
		r.Documents = append(r.Documents, model.EvidenceDocument{ID: marketdata.EvidenceID(r.Candidate.Ticker, ev.EvidenceID, body), Ticker: r.Candidate.Ticker, Title: "Announcement reaction (computed)", Kind: "computed", EventTime: ev.OccurredAt, Text: body, RetrievedAt: now})
	}
	r.Documents = marketdata.DeduplicateEvidence(r.Documents)
}

// The legacy drift proxy tolerates missing benchmark dates. A source-attributed
// abnormal reaction must compare the same closes, not silently substitute a
// later benchmark session or an unadjusted stock return.
func releasePricesAligned(stock, bench *quant.Series, session time.Time) bool {
	if stock == nil || bench == nil {
		return false
	}
	i := sort.Search(len(stock.Bars), func(i int) bool { return stock.Bars[i].Date >= session.Format("2006-01-02") })
	if i <= 0 || i+1 >= len(stock.Bars) {
		return false
	}
	for _, date := range []string{stock.Bars[i-1].Date, stock.Bars[i+1].Date, stock.Bars[len(stock.Bars)-1].Date} {
		j := sort.Search(len(bench.Bars), func(j int) bool { return bench.Bars[j].Date >= date })
		if j >= len(bench.Bars) || bench.Bars[j].Date != date || bench.Bars[j].Close <= 0 {
			return false
		}
	}
	return true
}
