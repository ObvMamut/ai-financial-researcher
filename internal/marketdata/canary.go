package marketdata

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The canary checks every external source this pipeline reads against a
// known-good expectation, through the same provider code a run uses, and says
// in one line each whether that source still answers the way the pipeline
// assumes.
//
// It exists because the live pipeline's failures are silent. On 2026-10-07
// three were found by hand in one day: Yahoo's search had stopped answering for
// any foreign local symbol (0 of 89 names had a headline), option chains
// fetched before the US open were blind and were being cached for the day, and
// Alpaca's contentless filter was hiding every ADR issuer story. None raised an
// error; each read as a quiet market. A canary run before a live run turns those
// into a failing line.
//
// AlphaVantage is deliberately not probed: its free key allows 25 requests a
// day, shared with the earnings calendar a run needs.

// Canary statuses.
const (
	CanaryPass = "pass"
	CanaryFail = "fail"
	CanarySkip = "skip"
)

// CanaryResult is one probe's verdict.
type CanaryResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// CanaryConfig is what the probes need. Missing keys skip the probes that need
// them rather than failing them: an unconfigured source is not a broken one.
type CanaryConfig struct {
	AlpacaKeyID, AlpacaSecret string
	ContactEmail              string
	Cache                     *Cache
	Now                       time.Time
}

// canaryMaxBarAge is how old a daily bar may be and still count as current: a
// long weekend plus one holiday.
const canaryMaxBarAge = 5 * 24 * time.Hour

// canaryCharts are one US listing and one from each region the universe spans.
var canaryCharts = []string{"AAPL", "SAP.DE", "7203.T", "0700.HK"}

// RunCanary runs every probe in order and returns their results.
func RunCanary(ctx context.Context, cfg CanaryConfig) []CanaryResult {
	if cfg.Now.IsZero() {
		cfg.Now = time.Now()
	}
	var out []CanaryResult
	for _, sym := range canaryCharts {
		out = append(out, probeChart(ctx, cfg, sym))
	}
	out = append(out,
		probeOptions(ctx),
		probeNewsByName(ctx),
		probeAlpacaBars(ctx, cfg),
		probeAlpacaNews(ctx, cfg),
		probeSEC(ctx, cfg),
	)
	return out
}

// CanaryHealthy reports whether no probe failed.
func CanaryHealthy(results []CanaryResult) bool {
	for _, r := range results {
		if r.Status == CanaryFail {
			return false
		}
	}
	return true
}

func probeChart(ctx context.Context, cfg CanaryConfig, sym string) CanaryResult {
	r := CanaryResult{Name: "yahoo-chart " + sym}
	s, err := NewYahooClient(cfg.Cache).HistoryFresh(ctx, sym)
	if err != nil {
		r.Status, r.Detail = CanaryFail, err.Error()
		return r
	}
	if s == nil || len(s.Bars) == 0 {
		r.Status, r.Detail = CanaryFail, "no bars returned"
		return r
	}
	last := s.Bars[len(s.Bars)-1].Date
	d, err := time.Parse("2006-01-02", last)
	if err != nil {
		r.Status, r.Detail = CanaryFail, "unparseable bar date "+last
		return r
	}
	if cfg.Now.Sub(d) > canaryMaxBarAge {
		r.Status, r.Detail = CanaryFail, fmt.Sprintf("newest bar %s is stale (%d bars)", last, len(s.Bars))
		return r
	}
	r.Status, r.Detail = CanaryPass, fmt.Sprintf("%d bars, newest %s", len(s.Bars), last)
	return r
}

func probeOptions(ctx context.Context) CanaryResult {
	r := CanaryResult{Name: "yahoo-options AAPL"}
	td, err := NewYahooOptionsProvider().Fetch(ctx, "sentiment", "AAPL")
	if err != nil {
		r.Status, r.Detail = CanaryFail, err.Error()
		return r
	}
	for _, d := range td.Diagnostics {
		if d.Reason == "off_session" {
			r.Status, r.Detail = CanaryPass, "before the US session: the legs abstain as designed ("+d.Message+")"
			return r
		}
	}
	if len(td.Warnings) > 0 {
		r.Status, r.Detail = CanaryFail, td.Warnings[0]
		return r
	}
	if len(td.Facts) == 0 {
		r.Status, r.Detail = CanaryFail, "the chain produced no facts"
		return r
	}
	r.Status, r.Detail = CanaryPass, fmt.Sprintf("%d facts", len(td.Facts))
	return r
}

// probeNewsByName checks the company-name path a foreign listing with no US
// line depends on, and reports when the local-symbol search answers again.
func probeNewsByName(ctx context.Context) CanaryResult {
	r := CanaryResult{Name: "yahoo-news 0700.HK by name"}
	ctx = WithCompanyNames(ctx, func(t string) []string {
		if strings.EqualFold(t, "0700.HK") {
			return []string{"Tencent Holdings Ltd."}
		}
		return nil
	})
	p, ok := NewYahooNewsProvider().(*yahooNewsProvider)
	if !ok {
		r.Status, r.Detail = CanaryFail, "unexpected provider type"
		return r
	}
	local, err := p.search(ctx, "0700.HK")
	if err != nil {
		r.Status, r.Detail = CanaryFail, err.Error()
		return r
	}
	td, err := p.Fetch(ctx, "news", "0700.HK")
	if err != nil {
		r.Status, r.Detail = CanaryFail, err.Error()
		return r
	}
	tagged := 0
	for _, f := range td.Facts {
		if strings.Contains(f.Label, "tagged to this ticker") {
			tagged++
		}
	}
	note := ""
	if len(local.News) > 0 {
		note = fmt.Sprintf("; the local-symbol search returns %d items again, so the name fallback may be redundant", len(local.News))
	}
	if tagged == 0 {
		r.Status, r.Detail = CanaryFail, fmt.Sprintf("no headline about Tencent (%d facts)%s", len(td.Facts), note)
		return r
	}
	r.Status, r.Detail = CanaryPass, fmt.Sprintf("%d headlines about Tencent%s", tagged, note)
	return r
}

func probeAlpacaBars(ctx context.Context, cfg CanaryConfig) CanaryResult {
	r := CanaryResult{Name: "alpaca-bars AAPL"}
	a := NewAlpacaPrices(cfg.AlpacaKeyID, cfg.AlpacaSecret, nil)
	if !a.Available() {
		r.Status, r.Detail = CanarySkip, "no Alpaca key configured"
		return r
	}
	s, err := a.HistoryFresh(ctx, "AAPL")
	if err != nil {
		r.Status, r.Detail = CanaryFail, err.Error()
		return r
	}
	if s == nil || len(s.Bars) == 0 {
		r.Status, r.Detail = CanaryFail, "no bars returned"
		return r
	}
	last := s.Bars[len(s.Bars)-1].Date
	if d, err := time.Parse("2006-01-02", last); err != nil || cfg.Now.Sub(d) > canaryMaxBarAge {
		r.Status, r.Detail = CanaryFail, "newest bar "+last+" is stale"
		return r
	}
	r.Status, r.Detail = CanaryPass, fmt.Sprintf("%d bars, newest %s (SIP feed)", len(s.Bars), last)
	return r
}

func probeAlpacaNews(ctx context.Context, cfg CanaryConfig) CanaryResult {
	r := CanaryResult{Name: "alpaca-news AAPL"}
	p := NewAlpacaNewsProvider(cfg.AlpacaKeyID, cfg.AlpacaSecret)
	if !p.Available() {
		r.Status, r.Detail = CanarySkip, "no Alpaca key configured"
		return r
	}
	td, err := p.Fetch(ctx, "news", "AAPL")
	if err != nil {
		r.Status, r.Detail = CanaryFail, err.Error()
		return r
	}
	if len(td.Facts) == 0 {
		detail := "no headlines for AAPL in 21 days"
		if len(td.Warnings) > 0 {
			detail = td.Warnings[0]
		}
		r.Status, r.Detail = CanaryFail, detail
		return r
	}
	r.Status, r.Detail = CanaryPass, fmt.Sprintf("%d headlines", len(td.Facts))
	return r
}

func probeSEC(ctx context.Context, cfg CanaryConfig) CanaryResult {
	r := CanaryResult{Name: "sec-submissions AAPL"}
	if cfg.ContactEmail == "" {
		r.Status, r.Detail = CanarySkip, "no contact_email configured"
		return r
	}
	hist, missing := NewFilingHistorySource(cfg.ContactEmail, cfg.Cache).FilingHistory(ctx, []string{"AAPL"}, cfg.Now.AddDate(-1, 0, 0))
	h, ok := hist["AAPL"]
	if !ok {
		r.Status, r.Detail = CanaryFail, "AAPL unresolved: "+strings.Join(missing, "; ")
		return r
	}
	if len(h.Earnings) == 0 || len(h.Reports) == 0 {
		r.Status, r.Detail = CanaryFail, fmt.Sprintf("a year of AAPL filings shows %d earnings releases and %d reports", len(h.Earnings), len(h.Reports))
		return r
	}
	r.Status, r.Detail = CanaryPass, fmt.Sprintf("%d earnings releases, %d reports in the last year", len(h.Earnings), len(h.Reports))
	return r
}
