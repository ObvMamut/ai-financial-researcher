package marketdata

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

func TestCapturedRegionalSources(t *testing.T) {
	var c struct {
		Documents map[string][]model.EvidenceDocument `json:"documents"`
		Hashes    map[string]string                   `json:"text_sha256"`
	}
	b, err := os.ReadFile("testdata/regional-capture-2026-09-21.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"ASML.AS": 5, "STLAM.MI": 0, "NOKIA.HE": 4, "2330.TW": 7, "9988.HK": 6, "BHP.AX": 0}
	for ticker, docs := range c.Documents {
		substantive := 0
		for _, d := range docs {
			if got := fmt.Sprintf("%x", sha256.Sum256([]byte(d.Text))); got != c.Hashes[d.ID] {
				t.Fatalf("capture text changed: %s", d.URL)
			}
			if SubstantiveResearchDocument(d) {
				substantive++
			}
			if d.Error != "" && (len(DocumentDiagnostics(d)) != 1 || DocumentDiagnostics(d)[0].Disposition != "failed") {
				t.Fatalf("lost fetch failure: %s", ticker)
			}
		}
		if substantive != want[ticker] {
			t.Errorf("%s: substantive=%d want=%d", ticker, substantive, want[ticker])
		}
	}
	for _, coverage := range MeasureResearchCoverage(c.Documents) {
		t.Logf("fixed corpus: %+v", coverage)
	}
	seed := "https://pr.tsmc.com/english/news"
	ranked := RankResearchLinks([]string{"https://pr.tsmc.com/japanese/news/3340", "https://pr.tsmc.com/english/news/3338"}, seed)
	if ranked[0] != "https://pr.tsmc.com/english/news/3338" {
		t.Fatalf("translated release displaced seed-language release: %v", ranked)
	}
	if !ResearchNavigationURL("https://investors.thetradedesk.com/sec-filings/default.aspx") {
		t.Fatal("filings index classified as substantive")
	}
}

func TestSourceDiagnosticsSurviveCacheAndFrozenCopies(t *testing.T) {
	secret := "z9p7r5t3-diagnostic-fixture-key"
	redact.Register(secret)
	d := sourceDiagnostic("news", "ASML.AS", "news", "unresolved_symbol", "withheld", "failed "+secret)
	if d.Region != "Europe" || redact.ContainsCredential(d.Message) {
		t.Fatalf("unsafe diagnostic: %+v", d)
	}
	td := TickerData{Ticker: "ASML.AS", Diagnostics: []model.SourceDiagnostic{d}}
	cache := NewCache(t.TempDir())
	if err := cache.Set("fixture", "news", "news", td.Ticker, td); err != nil {
		t.Fatal(err)
	}
	var got TickerData
	if ok, err := cache.Get("fixture", "news", "news", td.Ticker, &got); err != nil || !ok || len(got.Diagnostics) != 1 || got.Diagnostics[0] != d {
		t.Fatalf("cache lost diagnostics: %+v %v", got, err)
	}
	pack := NewDataPack("news")
	pack.ByTicker[td.Ticker] = got
	pack.Diagnostics = []model.SourceDiagnostic{d, sourceDiagnostic("news", "AAPL", "news", "stale", "withheld", "stale")}
	snapshot := &ResearchSnapshot{Packs: map[string]*DataPack{"news": pack}}
	copy := snapshot.BuildPack(context.Background(), "news", []string{td.Ticker})
	if len(copy.Diagnostics) != 1 || copy.Diagnostics[0] != d {
		t.Fatalf("frozen pack failed ticker scoping: %+v", copy.Diagnostics)
	}
	copy.Diagnostics[0].Message = "changed"
	copy.ByTicker[td.Ticker].Diagnostics[0].Message = "changed"
	if pack.Diagnostics[0] != d || pack.ByTicker[td.Ticker].Diagnostics[0] != d {
		t.Fatal("frozen diagnostic mutated source")
	}
}
