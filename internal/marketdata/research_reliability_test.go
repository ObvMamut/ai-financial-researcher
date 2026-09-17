package marketdata

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestOversizedHTMLKeepsBoundedVisibleText(t *testing.T) {
	visible := strings.Repeat("Issuer quarterly revenue and guidance were updated. ", 10)
	body := "<html><body>" + visible + "<script>" + strings.Repeat("secret script content;", 150000) + "</script></body></html>"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	d := (DocumentReader{Client: client}).Read(context.Background(), "AAA", "https://issuer.example/release")
	if d.Error != "" || !d.Truncated || !strings.Contains(d.Text, "quarterly revenue") || strings.Contains(d.Text, "secret") {
		t.Fatalf("unsafe or unusable bounded extraction: %+v", d)
	}
}

func TestResearchLinksPrioritizeIssuerReleases(t *testing.T) {
	links := RankResearchLinks([]string{"https://issuer.example/privacy", "https://other.example/news", "https://issuer.example/news/quarterly-results-2026-09", "https://issuer.example/news/quarterly-results-2026-06"}, "https://issuer.example/news")
	if !strings.HasSuffix(links[0], "2026-09") || !strings.Contains(links[len(links)-1], "privacy") {
		t.Fatalf("unexpected priority: %v", links)
	}
}

func TestProviderDailyRefusalSurvivesRestartButMinuteLimitsDoNot(t *testing.T) {
	dir := t.TempDir()
	l := NewPersistentLimiter(25, 30, 1, dir, "fixture")
	rememberDailyQuota(l, "Please spread out requests to avoid the per-minute rate limit")
	if used, _ := l.DailyBudget(); used != 0 {
		t.Fatal("transient throttle exhausted day")
	}
	rememberDailyQuota(l, "The daily request limit is reached")
	next := NewPersistentLimiter(25, 30, 1, dir, "fixture")
	if err := next.Wait(context.Background()); err == nil {
		t.Fatal("restart spent on a known exhausted quota")
	}
}

func TestResearchCoverageRetainsEmptyRegionalDenominators(t *testing.T) {
	all := map[string][]model.EvidenceDocument{
		"AAA": {{ID: "full", Kind: "document", Source: "issuer", Text: strings.Repeat("release ", 50)}},
		"BBB": nil, "9988.HK": {{ID: "headline", Kind: "headline", Text: "A dated update"}}, "STLAM.MI": nil,
	}
	groups := MeasureResearchCoverage(all)
	for _, g := range groups {
		switch g.Region {
		case "US":
			if g.Eligible != 2 || g.WithDocuments != 1 || g.WithNews != 1 {
				t.Fatalf("bad denominator %+v", g)
			}
		case "Europe":
			if g.Eligible != 1 || g.WithNews != 0 {
				t.Fatalf("lost empty region %+v", g)
			}
		case "Asia-Pacific":
			if g.Eligible != 1 || g.WithDocuments != 0 || g.WithNews != 1 {
				t.Fatalf("headline promoted %+v", g)
			}
		default:
			t.Fatalf("unexpected region %+v", g)
		}
	}
	if len(groups) != 3 {
		t.Fatal("missing region")
	}
}
