package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type fredProvider struct {
	client  *http.Client
	apiKey  string
	baseURL string
}

func NewFredProvider(apiKey string) Provider {
	base := "https://api.stlouisfed.org"
	// CFR_FRED_BASE reroutes series requests (tests, proxies/mirrors).
	if v := os.Getenv("CFR_FRED_BASE"); v != "" {
		base = v
	}
	return &fredProvider{
		client:  &http.Client{Timeout: 10 * time.Second},
		apiKey:  apiKey,
		baseURL: base,
	}
}

func (p *fredProvider) Name() string      { return "FRED" }
func (p *fredProvider) Source() string    { return p.baseURL }
func (p *fredProvider) Domains() []string { return []string{"macro"} }
func (p *fredProvider) Available() bool   { return p.apiKey != "" }

func (p *fredProvider) Fetch(ctx context.Context, domain string, ticker string) (TickerData, error) {
	return TickerData{}, ErrNotApplicable // FRED carries macro series only
}

// fredSeries are the regime indicators the macro specialist reasons from. Every
// one of them describes the United States, which is why macro coverage is
// US-only (see orchestrator.coveredBy).
var fredSeries = []struct {
	id    string
	label string
}{
	{"DGS10", "10-Year Treasury Rate"},
	{"T10Y2Y", "10-Year vs 2-Year Spread"},
	{"CPIAUCSL", "CPI (Inflation)"},
	{"UNRATE", "Unemployment Rate"},
}

// MacroFetch returns the latest observation of each series.
//
// A failed series used to `continue` silently, so the pack rendered three
// indicators as though four had been requested and nothing anywhere recorded
// the gap. Failures are collected and returned alongside whatever did arrive: a
// partial regime read is still worth having, but the run has to know it is
// partial. The error lands in pack.Errors and so in metadata.json.
func (p *fredProvider) MacroFetch(ctx context.Context) ([]Fact, error) {
	if !p.Available() {
		return nil, ErrUnavailable
	}

	var facts []Fact
	var failures []string

	for _, s := range fredSeries {
		fact, err := p.fetchSeries(ctx, s.id, s.label)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", s.id, err))
			// A cancelled or expired context will fail every remaining series
			// identically; stop rather than logging the same thing four times.
			if ctx.Err() != nil {
				break
			}
			continue
		}
		facts = append(facts, fact)
	}

	if len(failures) > 0 {
		return facts, fmt.Errorf("%d of %d series unavailable (%s)",
			len(failures), len(fredSeries), strings.Join(failures, "; "))
	}
	return facts, nil
}

func (p *fredProvider) fetchSeries(ctx context.Context, id, label string) (Fact, error) {
	v := url.Values{}
	v.Set("series_id", id)
	v.Set("api_key", p.apiKey)
	v.Set("file_type", "json")
	v.Set("sort_order", "desc")
	v.Set("limit", "1")

	u := p.baseURL + "/fred/series/observations?" + v.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Fact{}, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return Fact{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Fact{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var data struct {
		Observations []struct {
			Date  string `json:"date"`
			Value string `json:"value"`
		} `json:"observations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return Fact{}, err
	}
	if len(data.Observations) == 0 {
		return Fact{}, fmt.Errorf("no observations returned")
	}

	obs := data.Observations[0]
	// FRED prints "." for a missing observation; carrying that through renders
	// as "10-Year Treasury Rate: ." in the prompt.
	if strings.TrimSpace(obs.Value) == "" || obs.Value == "." {
		return Fact{}, fmt.Errorf("latest observation (%s) has no value", obs.Date)
	}
	asOf, _ := time.Parse("2006-01-02", obs.Date)
	return Fact{
		Label:  label,
		Value:  obs.Value,
		AsOf:   asOf,
		Source: "FRED",
	}, nil
}
