package marketdata

import (
	"net/url"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

type ResearchCoverage struct {
	SubstantivePrimaryDocuments int            `json:"substantive_primary_documents"`
	VisiblePrimaryDocuments     int            `json:"visible_primary_documents"`
	AccessibleDocuments         int            `json:"accessible_documents"`
	FailedDocuments             int            `json:"failed_documents"`
	VisibleDocuments            int            `json:"visible_documents"`
	Origins                     map[string]int `json:"source_hosts"`
	Region                      string         `json:"region"`
	Eligible                    int            `json:"eligible"`
	WithNews                    int            `json:"with_news"`
	WithDocuments               int            `json:"with_documents"`
	SourceDocuments             map[string]int `json:"source_documents"`
}

// MeasureResearchCoverage uses eligible tickers, including empty results, as its
// denominator. Document counts describe source coverage, not independent events.
func MeasureResearchCoverage(all map[string][]model.EvidenceDocument) []ResearchCoverage {
	groups := map[string]*ResearchCoverage{}
	for ticker, docs := range all {
		region := ResearchRegion(ticker)
		g := groups[region]
		if g == nil {
			g = &ResearchCoverage{Region: region, SourceDocuments: map[string]int{}, Origins: map[string]int{}}
			groups[region] = g
		}
		g.Eligible++
		news, full := false, false
		seen := map[string]bool{}
		for _, d := range docs {
			if d.Error != "" && d.Kind == "document" {
				g.FailedDocuments++
			}
			if d.Error != "" || seen[d.ID] || strings.TrimSpace(d.Text) == "" {
				continue
			}
			seen[d.ID] = true
			if d.Kind == "headline" || d.Kind == "summary" || d.Kind == "document" {
				news = true
			}
			if d.Kind == "document" {
				g.AccessibleDocuments++
				if len(d.SelectedSpans) > 0 {
					g.VisibleDocuments++
				}
			}
			if SubstantiveResearchDocument(d) {
				full = true
				if d.Authority == "issuer" || d.Authority == "depositary" {
					g.SubstantivePrimaryDocuments++
					if len(d.SelectedSpans) > 0 {
						g.VisiblePrimaryDocuments++
					}
				}
				source := d.Source
				if source == "" {
					source = "unknown"
				}
				g.SourceDocuments[source]++
				origin := source
				if u, e := url.Parse(d.URL); e == nil && u.Hostname() != "" {
					origin = u.Hostname()
				}
				g.Origins[origin]++
			}
		}
		if news {
			g.WithNews++
		}
		if full {
			g.WithDocuments++
		}
	}
	out := make([]ResearchCoverage, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Region < out[j].Region })
	return out
}

// ResearchRegion describes listing geography, independently of calendar coverage
// and closing hours. Foreign issuers listed in the US count as US listings.
func ResearchRegion(ticker string) string {
	if MarketFor(ticker) == MarketUS {
		return "US"
	}
	suffix := strings.ToUpper(ticker[strings.LastIndex(ticker, ".")+1:])
	for region, list := range map[string]string{
		"Europe":             "L IR AS BR PA LS MC MI VI AT HE TL VS DE F BE DU HM MU SG CO ST OL IC SW WA PR IS ME",
		"Asia-Pacific":       "T HK SS SZ KS KQ TW TWO SI KL BK JK VN NS BO AX NZ",
		"Americas ex-US":     "TO V NE MX SA BA SN CN",
		"Africa/Middle East": "JO TA",
	} {
		for _, s := range strings.Fields(list) {
			if suffix == s {
				return region
			}
		}
	}
	return "other/unknown"
}
