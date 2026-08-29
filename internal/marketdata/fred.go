package marketdata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

type fredProvider struct {
	client *http.Client
	apiKey string
}

func NewFredProvider(apiKey string) Provider {
	return &fredProvider{
		client: &http.Client{Timeout: 10 * time.Second},
		apiKey: apiKey,
	}
}

func (p *fredProvider) Name() string      { return "FRED" }
func (p *fredProvider) Source() string    { return "https://api.stlouisfed.org" }
func (p *fredProvider) Domains() []string { return []string{"macro"} }
func (p *fredProvider) Available() bool   { return p.apiKey != "" }

func (p *fredProvider) Fetch(ctx context.Context, domain string, ticker string) (TickerData, error) {
	return TickerData{}, ErrNotApplicable // FRED carries macro series only
}

func (p *fredProvider) MacroFetch(ctx context.Context) ([]Fact, error) {
	if !p.Available() {
		return nil, ErrUnavailable
	}

	series := []struct {
		id    string
		label string
	}{
		{"DGS10", "10-Year Treasury Rate"},
		{"T10Y2Y", "10-Year vs 2-Year Spread"},
		{"CPIAUCSL", "CPI (Inflation)"},
		{"UNRATE", "Unemployment Rate"},
	}

	var facts []Fact
	for _, s := range series {
		v := url.Values{}
		v.Set("series_id", s.id)
		v.Set("api_key", p.apiKey)
		v.Set("file_type", "json")
		v.Set("sort_order", "desc")
		v.Set("limit", "1")

		u := "https://api.stlouisfed.org/fred/series/observations?" + v.Encode()
		resp, err := p.client.Get(u)
		if err != nil {
			continue
		}

		var data struct {
			Observations []struct {
				Date  string `json:"date"`
				Value string `json:"value"`
			} `json:"observations"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		if len(data.Observations) > 0 {
			obs := data.Observations[0]
			asOf, _ := time.Parse("2006-01-02", obs.Date)
			facts = append(facts, Fact{
				Label:  s.label,
				Value:  obs.Value,
				AsOf:   asOf,
				Source: "FRED",
			})
		}
	}

	return facts, nil
}
