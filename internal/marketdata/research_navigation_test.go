package marketdata

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestCapturedIssuerLinksSelectReleasesBeforeNavigation(t *testing.T) {
	b, err := os.ReadFile("testdata/research-links-sep13.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Ticker, Seed string
		Links        []string
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		links := RankResearchLinks(c.Links, c.Seed)
		for _, u := range links {
			if CanonicalResearchURL(u) == CanonicalResearchURL(c.Seed) || strings.Contains(u, "%7B") {
				t.Fatalf("duplicate/template survived: %s", u)
			}
		}
		switch c.Ticker {
		case "2330.TW":
			if len(links) == 0 || links[0] != "https://pr.tsmc.com/english/news/3340" {
				t.Fatalf("TSMC release not first: %v", links)
			}
		case "SAN.PA":
			if len(links) == 0 || !strings.Contains(links[0], "/press-releases/2026/") {
				t.Fatalf("Sanofi release not first: %v", links)
			}
		case "MU":
			if len(links) == 0 || !strings.Contains(links[0], "/news/press-release/") {
				t.Fatalf("Micron release not first: %v", links)
			}
		}
		d := model.EvidenceDocument{Kind: "document", URL: c.Seed, Text: strings.Repeat("navigation content ", 100)}
		if SubstantiveResearchDocument(d) {
			t.Fatalf("index counted as evidence: %s", c.Seed)
		}
	}
}

func TestCanonicalResearchURLRetainsMeaningfulQueryAndPath(t *testing.T) {
	a := CanonicalResearchURL(" HTTPS://ISSUER.example/news?q=1#content ")
	if a != "https://issuer.example/news?q=1" {
		t.Fatal(a)
	}
	for _, b := range []string{"https://issuer.example/News?q=1", "https://issuer.example/news?q=2"} {
		if a == CanonicalResearchURL(b) {
			t.Fatal("distinct resource merged")
		}
	}
	links := RankResearchLinks([]string{a + "#one", a + "#two"}, "https://issuer.example/")
	if len(links) != 1 {
		t.Fatal("fragment requests duplicated")
	}
}

func TestIssuerReleaseLinksBeatResourceIndexes(t *testing.T) {
	// Link subsets observed by the bounded September 13 document-reader check.
	for _, c := range []struct {
		seed, want        string
		indexes, releases []string
	}{
		{
			seed:    "https://www.oracle.com/news/",
			want:    "https://www.oracle.com/news/announcement/oracle-and-bloom-energy-announce-support-for-roadrunner-food-bank-2026-09-03/",
			indexes: []string{"https://www.oracle.com/news/resources/"},
			releases: []string{
				"https://www.oracle.com/news/announcement/schloss-elmau-transforms-its-luxury-resort-operations-with-oracle-cloud-2026-09-01/",
				"https://www.oracle.com/news/announcement/oracle-and-bloom-energy-announce-support-for-roadrunner-food-bank-2026-09-03/",
			},
		},
		{
			seed:     "https://www.alibabagroup.com/en-US/news-and-resource",
			want:     "https://www.alibabagroup.com/en-US/document-2034722487995990016",
			indexes:  []string{"https://www.alibabagroup.com/news-press-releases", "https://www.alibabagroup.com/resource-video", "https://www.alibabagroup.com/en-US/esg-resource"},
			releases: []string{"https://www.alibabagroup.com/en-US/document-2034722487995990016", "https://www.alibabagroup.com/en-US/document-2026456290057781248"},
		},
	} {
		links := RankResearchLinks(append(append([]string{}, c.indexes...), c.releases...), c.seed)
		if len(links) == 0 || links[0] != c.want {
			t.Fatalf("release not first for %s: %v", c.seed, links)
		}
		for _, u := range c.indexes {
			if !ResearchNavigationURL(u) {
				t.Fatalf("resource index counted as evidence: %s", u)
			}
		}
		for _, u := range c.releases {
			if ResearchNavigationURL(u) {
				t.Fatalf("release marked navigation: %s", u)
			}
		}
	}
}

func TestPrimaryCoverageExcludesNavigationAndTracksVisibleText(t *testing.T) {
	nav := model.EvidenceDocument{ID: "index", Ticker: "SAN.PA", Kind: "document", URL: "https://issuer.example/news", Authority: "issuer", Text: strings.Repeat("menu ", 100)}
	full := model.EvidenceDocument{ID: "release", Ticker: "SAN.PA", Kind: "document", URL: "https://issuer.example/news/2026/release", Authority: "issuer", Text: strings.Repeat("Quarterly results with uncertainty. ", 20), SelectedSpans: []model.EvidenceSpan{{}}}
	coverage := MeasureResearchCoverage(map[string][]model.EvidenceDocument{"SAN.PA": {nav, full}, "2330.TW": {nav}})
	for _, c := range coverage {
		if c.Region == "Europe" && (c.WithDocuments != 1 || c.SubstantivePrimaryDocuments != 1 || c.VisiblePrimaryDocuments != 1) {
			t.Fatal(c)
		}
		if c.Region == "Asia-Pacific" && (c.WithDocuments != 0 || c.SubstantivePrimaryDocuments != 0) {
			t.Fatal(c)
		}
	}
}
