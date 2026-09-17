package marketdata

import (
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

var researchURLDate = regexp.MustCompile(`\d{4}[-/]\d{2}[-/]\d{2}`)

// RankResearchLinks puts issuer results and release pages ahead of navigation.
// It ranks only discovered URLs; it never invents a destination or bypasses the
// reader's URL/DNS/redirect checks. Date-like URL suffixes break ties newest first.
func RankResearchLinks(links []string, issuer string) []string {
	var out []string
	seen := map[string]bool{}
	for _, link := range links {
		u, err := url.Parse(link)
		if err != nil || strings.ContainsAny(u.Path, "{}") {
			continue
		} // unresolved HTML templates are not URLs
		key := CanonicalResearchURL(link)
		if key == CanonicalResearchURL(issuer) || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, link)
	}
	base, _ := url.Parse(issuer)
	score := func(raw string) int {
		u, err := url.Parse(raw)
		if err != nil {
			return -100
		}
		s := 0
		if base != nil && u.Hostname() == base.Hostname() {
			s += 4
			seedPath := strings.TrimRight(strings.ToLower(base.Path), "/")
			if seedPath != "" && strings.HasPrefix(strings.ToLower(u.Path), seedPath+"/") {
				s += 8
			}
		}
		path := strings.ToLower(u.Path)
		if strings.Contains(path, "/document-") || strings.Contains(path, "/announcement/") {
			s += 6
		}
		if ResearchNavigationURL(raw) {
			s -= 30
		}
		for _, word := range []string{"earnings", "quarter", "results", "release", "financial", "news"} {
			if strings.Contains(path, word) {
				s += 3
			}
		}
		for _, word := range []string{"privacy", "cookie", "career", "contact", "login", ".pdf"} {
			if strings.Contains(path, word) {
				s -= 100
			}
		}
		for _, suffix := range []string{".css", ".js", ".png", ".jpg", ".svg", ".ico", ".woff", ".woff2"} {
			if strings.HasSuffix(path, suffix) {
				s -= 1000
			}
		}
		return s
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := score(out[i]), score(out[j])
		if a != b {
			return a > b
		}
		dateA := strings.ReplaceAll(researchURLDate.FindString(out[i]), "/", "-")
		dateB := strings.ReplaceAll(researchURLDate.FindString(out[j]), "/", "-")
		if dateA != dateB {
			return dateA > dateB
		}
		return out[i] > out[j]
	})
	return out
}

// Query strings may identify distinct releases. Only fragments and host/scheme
// case are irrelevant to document identity; paths and queries remain untouched.
func CanonicalResearchURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	u.Scheme, u.Host, u.Fragment, u.RawFragment = strings.ToLower(u.Scheme), strings.ToLower(u.Host), "", ""
	return u.String()
}

// Recognize index/navigation URLs, including language variants and Q4 landing
// pages. This intentionally does not call a long page substantive just because
// its menus exceed the minimum source-text length.
func ResearchNavigationURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	p := strings.ToLower(strings.TrimRight(u.Path, "/"))
	if strings.Contains(p, "/browse-edgar") {
		return true
	}
	if strings.HasSuffix(p, "/default.aspx") {
		p = strings.TrimSuffix(p, "/default.aspx")
	}
	switch path.Base(p) {
	case ".", "/", "overview", "index.php", "home.html", "news", "latest-news", "news-archives", "investor-news", "newsroom", "news-releases", "press-releases", "press-release", "financial-results", "quarterly-results", "ir-news-filings", "ir-financial-reports-quarterly-results", "news-and-resource", "news-press-releases", "resources", "resource-video", "resource-logos", "resource-company", "resource-campus", "esg-resource", "library", "presentations", "events", "upcoming-events", "past-events":
		return true
	}
	return p == ""
}

func SubstantiveResearchDocument(d model.EvidenceDocument) bool {
	return d.Kind == "document" && d.Error == "" && !d.NavigationOnly && !ResearchNavigationURL(d.URL) && len(strings.TrimSpace(d.Text)) >= 200
}

// Public issuer seed pages. Extend via research.sources_file without rebuilding.
const issuerSources = `ticker,url
ABBV,https://news.abbvie.com/
BAC,https://newsroom.bankofamerica.com/
SNOW,https://investors.snowflake.com/overview/default.aspx
TSLA,https://ir.tesla.com/
TTD,https://investors.thetradedesk.com/overview/default.aspx
TTE.PA,https://totalenergies.com/
REGN,https://investor.regeneron.com/news/press-releases
SAN.PA,https://www.sanofi.com/en/media-room/press-releases
NOKIA.HE,https://www.nokia.com/newsroom/stock-exchange-releases/
BHP.AX,https://www.bhp.com/financial-results
AMGN,https://www.amgen.com/newsroom/press-releases
CRWD,https://ir.crowdstrike.com/news-events/press-releases
MU,https://investors.micron.com/overview/default.aspx
CRM,https://investor.salesforce.com/press-releases/default.aspx
ORCL,https://www.oracle.com/news/
ORCL,https://investor.oracle.com/investor-news/default.aspx
PDD,https://investor.pddholdings.com/news-releases
QCOM,https://investor.qualcomm.com/news-events/press-releases
OKTA,https://investor.okta.com/news-and-events/news-releases
SAN.MC,https://www.santander.com/en/press-room/press-releases
STLAM.MI,https://www.stellantis.com/en/news/press-releases
ASML.AS,https://www.asml.com/en/news/press-releases
ASML,https://www.asml.com/en/news/press-releases
7203.T,https://global.toyota/en/newsroom/
2330.TW,https://pr.tsmc.com/english/latest-news
TSM,https://pr.tsmc.com/english/latest-news
005930.KS,https://news.samsung.com/global/
0700.HK,https://www.tencent.com/en-us/articles.html
9988.HK,https://www.alibabagroup.com/en-US/news-and-resource
BABA,https://www.alibabagroup.com/en-US/news-and-resource
SHEL.L,https://www.shell.com/news-and-insights/newsroom.html
SAP.DE,https://news.sap.com/
NESN.SW,https://www.nestle.com/media/pressreleases
BMW.DE,https://www.press.bmwgroup.com/global
BAYN.DE,https://www.bayer.com/media/en-us/
O39.SI,https://www.ocbc.com/group/media
`

// MarkResearchAuthority trusts configured issuer seed hosts, not a model's
// claim about a page. Unknown publishers stay unknown. URLs still pass reader
// destination checks; this label grants no network access.
func MarkResearchAuthority(d *model.EvidenceDocument, sources map[string][]string) {
	d.Authority = ""
	u, err := url.Parse(d.URL)
	if err != nil || u.Hostname() == "" {
		return
	}
	host := strings.ToLower(u.Hostname())
	if d.Source != "" {
		host = strings.ToLower(d.Source)
	} // final response host, including redirects
	for _, raw := range sources[d.Ticker] {
		seed, e := url.Parse(raw)
		if e != nil {
			continue
		}
		root := strings.TrimPrefix(strings.ToLower(seed.Hostname()), "www.")
		if root != "" && (host == root || strings.HasSuffix(host, "."+root)) {
			d.Authority = "issuer"
			return
		}
	}
}
