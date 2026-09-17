package marketdata

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

func TestRegionMappingCoversCuratedListingsExactlyOnce(t *testing.T) {
	files, err := filepath.Glob("../universe/data/*.csv")
	if err != nil || len(files) == 0 {
		t.Fatal("missing universe")
	}
	all := map[string][]model.EvidenceDocument{}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "ticker,") {
				continue
			}
			ticker := strings.Split(line, ",")[0]
			if ResearchRegion(ticker) == "other/unknown" {
				t.Errorf("unmapped curated ticker %s", ticker)
			}
			all[ticker] = nil
		}
	}
	total := 0
	for _, g := range MeasureResearchCoverage(all) {
		total += g.Eligible
	}
	if total != len(all) {
		t.Fatalf("denominator %d != %d", total, len(all))
	}
	for _, ticker := range []string{"O39.SI", "PTT.BK", "RELIANCE.NS", "BHP.AX"} {
		if ResearchRegion(ticker) != "Asia-Pacific" {
			t.Fatal(ticker)
		}
	}
	if ResearchRegion("ASML") != "US" || ResearchRegion("ASML.AS") != "Europe" || ResearchRegion("X.UNKNOWN") != "other/unknown" {
		t.Fatal("listing regions conflated")
	}
}

func TestNavigationRemovalAndUnicodeExtraction(t *testing.T) {
	text := cleanDocument("<nav>cookie privacy menu</nav><main>企業 quarterly revenue 更新</main><footer>privacy</footer>")
	if strings.Contains(text, "privacy") || !strings.Contains(text, "企業") {
		t.Fatal(text)
	}
	text = cleanDocument(strings.Repeat("界", 40001))
	if len([]rune(text)) != 40000 || strings.ContainsRune(text, '\ufffd') {
		t.Fatal("invalid Unicode truncation")
	}
}

func TestDocumentTruncationReflectsOmittedUnicodeText(t *testing.T) {
	for _, size := range []int{14000, 40000, 40001} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(strings.Repeat("界", size))), Request: r}, nil
			})}
			doc := (DocumentReader{Client: client}).Read(context.Background(), "2330.TW", "https://issuer.example/release")
			if doc.Error != "" || doc.Truncated != (size > 40000) || len([]rune(doc.Text)) != min(size, 40000) {
				t.Fatalf("size=%d text=%d truncated=%v error=%s", size, len([]rune(doc.Text)), doc.Truncated, doc.Error)
			}
		})
	}
}

func TestFrozenResearchFXCannotUseFutureOrStaleRates(t *testing.T) {
	anchor := time.Date(2026, 9, 10, 23, 0, 0, 0, time.UTC)
	s := &ResearchSnapshot{Prices: map[string]*quant.Series{"HKDUSD=X": {Bars: []quant.Bar{{Date: "2026-09-10", Close: .125}, {Date: "2026-09-11", Close: .9}}}}}
	rate, err := NewFXRates(s).ResearchRate(context.Background(), "HKD", anchor)
	if err != nil || rate.USDPerUnit != .125 || rate.Date != "2026-09-10" {
		t.Fatalf("lookahead FX: %+v %v", rate, err)
	}
	s.Prices["HKDUSD=X"].Bars = s.Prices["HKDUSD=X"].Bars[1:]
	if _, err = NewFXRates(s).ResearchRate(context.Background(), "HKD", anchor); err == nil {
		t.Fatal("future-only rate accepted")
	}
	if _, err = NewFXRates(s).ResearchRate(context.Background(), "EUR", anchor); err == nil {
		t.Fatal("missing frozen FX fetched live")
	}
}
