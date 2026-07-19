package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestListRunsToleratesMissingMetadata(t *testing.T) {
	base := t.TempDir()

	// Older run: bare directory, no metadata/ideas.
	if err := os.MkdirAll(filepath.Join(base, "2026-06-01T10-00-00"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Newer, complete run.
	newer := filepath.Join(base, "2026-07-01T10-00-00")
	if err := os.MkdirAll(newer, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Run{Dir: newer}
	if err := r.WriteMeta(model.RunMeta{Mode: "independent", Outcome: "complete", GeneratedAt: "2026-07-01T10:05:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteIdeas(&model.IdeasResult{
		Mode:  "independent",
		Ideas: []model.TradeIdea{{Rank: 1, Ticker: "NVDA", Direction: model.DirectionBuy, Confidence: 80}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteQuantPack(map[string]any{"as_of": "2026-07-01"}); err != nil {
		t.Fatal(err)
	}

	runs, err := ListRuns(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	// Newest first.
	if runs[0].Name != "2026-07-01T10-00-00" {
		t.Errorf("first run = %s, want the newest", runs[0].Name)
	}
	if runs[0].Outcome != "complete" || runs[0].NumIdeas != 1 || !runs[0].HasQuant {
		t.Errorf("newest run summary wrong: %+v", runs[0])
	}
	if runs[1].Outcome != "unknown" || runs[1].NumIdeas != 0 || runs[1].HasQuant {
		t.Errorf("bare run should be unknown/empty: %+v", runs[1])
	}
}

func TestListRunsMissingBaseDir(t *testing.T) {
	runs, err := ListRuns(filepath.Join(t.TempDir(), "nope"))
	if err != nil || runs != nil {
		t.Errorf("missing base dir should be (nil, nil), got %v, %v", runs, err)
	}
}

func TestPricesRoundTripSanitizesSymbols(t *testing.T) {
	dir := t.TempDir()
	r := &Run{Dir: dir}

	type series struct {
		Symbol string `json:"symbol"`
	}
	if err := r.WritePrices("^GSPC", series{Symbol: "^GSPC"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "prices", "_GSPC.json")); err != nil {
		t.Errorf("expected sanitized filename _GSPC.json: %v", err)
	}
	var got series
	ok, err := ReadPrices(dir, "^GSPC", &got)
	if err != nil || !ok || got.Symbol != "^GSPC" {
		t.Errorf("round trip failed: ok=%v err=%v got=%+v", ok, err, got)
	}
	// Absent ticker → (false, nil).
	ok, err = ReadPrices(dir, "MISSING", &got)
	if ok || err != nil {
		t.Errorf("missing prices should be (false, nil), got (%v, %v)", ok, err)
	}
}
