package orchestrator

import (
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

func TestExtractLastJSON(t *testing.T) {
	text := `Some analysis text.

` + "```json" + `
{"index":"sp500","candidates":[]}
` + "```" + `

More text.

` + "```json" + `
{"index":"nq100","candidates":[{"ticker":"NVDA","name":"NVIDIA","bias":"bullish","reason":"breakout"}]}
` + "```"

	got, ok := extractLastJSON(text)
	if !ok {
		t.Fatal("extractLastJSON: not found")
	}
	if got == "" {
		t.Fatal("extractLastJSON: empty")
	}
	t.Logf("extracted: %s", got)
}

func TestParseIdeas(t *testing.T) {
	stdout := `Chief analyst synthesis here.

` + "```json" + `
{
  "mode": "independent",
  "generated_at": "2026-06-01T10:00:00Z",
  "ideas": [
    {
      "rank": 1,
      "ticker": "AAPL",
      "name": "Apple Inc.",
      "index": "nq100",
      "direction": "BUY",
      "confidence": 75,
      "why": "Technical breakout with macro tailwinds."
    }
  ],
  "notes": ""
}
` + "```"

	ideas, err := parseIdeas(stdout)
	if err != nil {
		t.Fatalf("parseIdeas: %v", err)
	}
	if len(ideas.Ideas) != 1 {
		t.Fatalf("expected 1 idea, got %d", len(ideas.Ideas))
	}
	if ideas.Ideas[0].Direction != model.DirectionBuy {
		t.Errorf("direction: got %q want BUY", ideas.Ideas[0].Direction)
	}
	if ideas.Ideas[0].Confidence != 75 {
		t.Errorf("confidence: got %d want 75", ideas.Ideas[0].Confidence)
	}
}

func TestParseIdeasValidation(t *testing.T) {
	uni, _ := universe.Load()
	cfg := Config{Mode: model.ModeIndependent}

	// Invalid direction (should be dropped)
	stdout := "```json\n{\"mode\":\"independent\",\"generated_at\":\"2026-06-01T10:00:00Z\",\"ideas\":[{\"rank\":1,\"ticker\":\"AAPL\",\"name\":\"Apple\",\"index\":\"nq100\",\"direction\":\"HOLD\",\"confidence\":50,\"why\":\"test\"}],\"notes\":\"\"}\n```"
	ideas, err := parseIdeas(stdout)
	if err != nil {
		t.Fatalf("parseIdeas: %v", err)
	}
	validateIdeas(ideas, cfg, uni, nil)
	if len(ideas.Ideas) != 0 {
		t.Errorf("expected 0 ideas after dropping HOLD, got %d", len(ideas.Ideas))
	}

	// Confidence out of range (should be clamped)
	stdout2 := "```json\n{\"mode\":\"independent\",\"generated_at\":\"2026-06-01T10:00:00Z\",\"ideas\":[{\"rank\":1,\"ticker\":\"AAPL\",\"name\":\"Apple\",\"index\":\"nq100\",\"direction\":\"BUY\",\"confidence\":150,\"why\":\"test\"}],\"notes\":\"\"}\n```"
	ideas2, err2 := parseIdeas(stdout2)
	if err2 != nil {
		t.Fatalf("parseIdeas: %v", err2)
	}
	validateIdeas(ideas2, cfg, uni, nil)
	if len(ideas2.Ideas) != 1 {
		t.Fatalf("expected 1 idea, got %d", len(ideas2.Ideas))
	}
	if ideas2.Ideas[0].Confidence != 100 {
		t.Errorf("expected confidence 100, got %d", ideas2.Ideas[0].Confidence)
	}
}

func TestParseScoutResult(t *testing.T) {
	stdout := `Scout analysis.

` + "```json" + `
{
  "index": "sp500",
  "candidates": [
    {"ticker": "AAPL", "name": "Apple", "bias": "bullish", "reason": "breakout"},
    {"ticker": "NVDA", "name": "NVIDIA", "bias": "bullish", "reason": "AI tailwind"}
  ]
}
` + "```"

	candidates := parseScoutResult(stdout, "sp500")
	if len(candidates) != 2 {
		t.Errorf("expected 2 candidates, got %d", len(candidates))
	}
	if candidates[0].Ticker != "AAPL" {
		t.Errorf("first ticker: got %q want AAPL", candidates[0].Ticker)
	}
	for _, c := range candidates {
		if c.Index != "sp500" {
			t.Errorf("%s: index = %q, want sp500", c.Ticker, c.Index)
		}
	}
}
