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
	validateIdeas(ideas, cfg, verified{Universe: uni})
	if len(ideas.Ideas) != 0 {
		t.Errorf("expected 0 ideas after dropping HOLD, got %d", len(ideas.Ideas))
	}

	// Confidence out of range (should be clamped)
	stdout2 := "```json\n{\"mode\":\"independent\",\"generated_at\":\"2026-06-01T10:00:00Z\",\"ideas\":[{\"rank\":1,\"ticker\":\"AAPL\",\"name\":\"Apple\",\"index\":\"nq100\",\"direction\":\"BUY\",\"confidence\":150,\"why\":\"test\"}],\"notes\":\"\"}\n```"
	ideas2, err2 := parseIdeas(stdout2)
	if err2 != nil {
		t.Fatalf("parseIdeas: %v", err2)
	}
	validateIdeas(ideas2, cfg, verified{Universe: uni})
	if len(ideas2.Ideas) != 1 {
		t.Fatalf("expected 1 idea, got %d", len(ideas2.Ideas))
	}
	if ideas2.Ideas[0].Confidence != 100 {
		t.Errorf("expected confidence 100, got %d", ideas2.Ideas[0].Confidence)
	}
}

// sp500Sample is the constituent slice a scout is screening in these tests.
var sp500Sample = []model.Constituent{
	{Ticker: "AAPL", Name: "Apple Inc.", Sector: "Technology", Index: "sp500"},
	{Ticker: "NVDA", Name: "NVIDIA Corporation", Sector: "Technology", Index: "sp500"},
}

func scoutOutput(body string) string {
	return "Scout analysis.\n\n```json\n" + body + "\n```"
}

func TestParseScoutResult(t *testing.T) {
	stdout := scoutOutput(`{
  "index": "sp500",
  "candidates": [
    {"ticker": "AAPL", "name": "Apple", "bias": "bullish", "reason": "breakout"},
    {"ticker": "NVDA", "name": "NVIDIA", "bias": "bullish", "reason": "AI tailwind"}
  ]
}`)

	candidates, rejected := parseScoutResult(stdout, "sp500", sp500Sample)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}
	if len(rejected) != 0 {
		t.Errorf("rejected %v, want none", rejected)
	}
	if candidates[0].Ticker != "AAPL" {
		t.Errorf("first ticker: got %q want AAPL", candidates[0].Ticker)
	}
	for _, c := range candidates {
		if c.Index != "sp500" {
			t.Errorf("%s: index = %q, want sp500", c.Ticker, c.Index)
		}
	}
	// The universe row is authoritative for identity: the model wrote "Apple",
	// the shortlist and every downstream prompt say what the CSV says.
	if candidates[0].Name != "Apple Inc." || candidates[0].Sector != "Technology" {
		t.Errorf("identity not enriched from the constituent: %+v", candidates[0])
	}
}

func TestParseScoutResultRejectsOffUniverseNominations(t *testing.T) {
	// ZZZZ is not in the index the scout was handed. A nomination the screen
	// cannot have come from is a hallucinated symbol: it must not reach the
	// shortlist, where it would burn a data-provider slot and an analysis slot.
	stdout := scoutOutput(`{
  "index": "sp500",
  "candidates": [
    {"ticker": "ZZZZ", "name": "Zeta Holdings", "bias": "bullish", "reason": "breakout"},
    {"ticker": "nvda", "name": "NVIDIA", "bias": "bearish", "reason": "extended"},
    {"ticker": "", "name": "blank", "bias": "bullish", "reason": "x"}
  ]
}`)

	candidates, rejected := parseScoutResult(stdout, "sp500", sp500Sample)
	if len(candidates) != 1 || candidates[0].Ticker != "NVDA" {
		t.Fatalf("got %+v, want only the canonicalised NVDA", candidates)
	}
	if len(rejected) != 1 || rejected[0] != "ZZZZ" {
		t.Errorf("rejected = %v, want [ZZZZ]", rejected)
	}
	if candidates[0].Bias != model.BiasBearish {
		t.Errorf("bias = %q, want bearish preserved", candidates[0].Bias)
	}
}

func TestParseScoutResultNormalisesBias(t *testing.T) {
	// The merit merge reads Bias to decide which end of the composite a name is
	// strong at, so an unparseable direction must land on neutral rather than
	// on whatever string the model invented.
	stdout := scoutOutput(`{
  "index": "sp500",
  "candidates": [{"ticker": "AAPL", "name": "Apple", "bias": "STRONG BUY", "reason": "x"}]
}`)
	candidates, _ := parseScoutResult(stdout, "sp500", sp500Sample)
	if len(candidates) != 1 || candidates[0].Bias != model.BiasNeutral {
		t.Errorf("got %+v, want bias normalised to neutral", candidates)
	}
}
