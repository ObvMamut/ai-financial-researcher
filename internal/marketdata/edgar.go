package marketdata

import (
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

//go:embed data/cik_map.csv
var cikMapRaw string

type edgarProvider struct {
	client       *http.Client
	cikMap       map[string]string
	contactEmail string
}

func NewEdgarProvider(contactEmail string) Provider {
	p := &edgarProvider{
		client:       &http.Client{Timeout: 10 * time.Second},
		cikMap:       make(map[string]string),
		contactEmail: contactEmail,
	}
	p.loadCIKMap()
	return p
}

func (p *edgarProvider) Name() string { return "EDGAR" }
func (p *edgarProvider) Domains() []string { return []string{"fundamentals"} }
func (p *edgarProvider) Available() bool { return p.contactEmail != "" }

func (p *edgarProvider) loadCIKMap() {
	r := csv.NewReader(strings.NewReader(cikMapRaw))
	records, err := r.ReadAll()
	if err != nil {
		return
	}
	for _, rec := range records {
		if len(rec) >= 2 {
			ticker := strings.ToUpper(strings.TrimSpace(rec[0]))
			cik := strings.TrimSpace(rec[1])
			// Pad CIK to 10 digits
			if len(cik) < 10 {
				cik = strings.Repeat("0", 10-len(cik)) + cik
			}
			p.cikMap[ticker] = cik
		}
	}
}

func (p *edgarProvider) Fetch(ctx context.Context, domain string, ticker string) (TickerData, error) {
	if domain != "fundamentals" {
		return TickerData{}, ErrUnavailable
	}
	cik, ok := p.cikMap[strings.ToUpper(ticker)]
	if !ok {
		return TickerData{}, fmt.Errorf("%w: ticker %s not in CIK map", ErrUnavailable, ticker)
	}

	url := fmt.Sprintf("https://data.sec.gov/api/xbrl/companyfacts/CIK%s.json", cik)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return TickerData{}, err
	}
	// SEC requires a User-Agent with contact info
	req.Header.Set("User-Agent", fmt.Sprintf("ClaudeFinancialResearcher/1.0 (%s)", p.contactEmail))

	resp, err := p.client.Do(req)
	if err != nil {
		return TickerData{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return TickerData{}, fmt.Errorf("%w: SEC API returned %d", ErrUnavailable, resp.StatusCode)
	}

	var data struct {
		Facts map[string]map[string]struct {
			Units map[string][]struct {
				Val  float64 `json:"val"`
				Accn string  `json:"accn"`
				FY   int     `json:"fy"`
				FP   string  `json:"fp"`
				End  string  `json:"end"`
			} `json:"units"`
		} `json:"facts"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return TickerData{}, err
	}

	// Extract some key fundamentals
	// common keys: us-gaap:Assets, us-gaap:NetIncomeLoss, us-gaap:Revenues (or SalesRevenueNet)
	td := TickerData{Ticker: ticker}
	
	extract := func(gaapKey string, label string) {
		if gaap, ok := data.Facts["us-gaap"]; ok {
			if fact, ok := gaap[gaapKey]; ok {
				if usd, ok := fact.Units["USD"]; ok && len(usd) > 0 {
					// Get the latest value
					latest := usd[len(usd)-1]
					asOf, _ := time.Parse("2006-01-02", latest.End)
					td.Facts = append(td.Facts, Fact{
						Label:  label,
						Value:  fmt.Sprintf("%.2f", latest.Val),
						AsOf:   asOf,
						Source: "SEC EDGAR",
					})
				}
			}
		}
	}

	extract("Assets", "Total Assets")
	extract("NetIncomeLoss", "Net Income")
	extract("Revenues", "Revenue")
	if len(td.Facts) == 0 {
		extract("SalesRevenueNet", "Revenue")
	}

	return td, nil
}

func (p *edgarProvider) MacroFetch(ctx context.Context) ([]Fact, error) {
	return nil, ErrUnavailable // EDGAR doesn't provide macro data in this format
}
