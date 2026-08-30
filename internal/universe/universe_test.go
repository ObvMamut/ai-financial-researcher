package universe

import (
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestLoad(t *testing.T) {
	u, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, idx := range AllIndices() {
		cs := u.Constituents(idx)
		if len(cs) == 0 {
			t.Errorf("Constituents(%q): empty", idx)
		}
		t.Logf("%-10s  %d constituents", idx, len(cs))
	}
}

func TestLookup(t *testing.T) {
	u, _ := Load()

	c, found := u.Lookup("AAPL")
	if !found {
		t.Error("Lookup(AAPL): expected found=true")
	}
	if c.Ticker != "AAPL" {
		t.Errorf("Lookup(AAPL): ticker=%q want AAPL", c.Ticker)
	}

	_, found2 := u.Lookup("XYZNOTREAL")
	if found2 {
		t.Error("Lookup(XYZNOTREAL): expected found=false")
	}
}

func TestDedupe(t *testing.T) {
	input := []model.Candidate{
		{Ticker: "AAPL", Name: "Apple"},
		{Ticker: "MSFT", Name: "Microsoft"},
		{Ticker: "AAPL", Name: "Apple duplicate"},
		{Ticker: "tsla", Name: "Tesla"},
		{Ticker: "TSLA", Name: "Tesla dup"},
	}
	out := Dedupe(input)
	if len(out) != 3 {
		t.Errorf("Dedupe: got %d items, want 3: %v", len(out), out)
	}
	if out[0].Ticker != "AAPL" || out[1].Ticker != "MSFT" {
		t.Errorf("Dedupe: order/content unexpected: %v", out)
	}
}

func TestDedupeCrossListing(t *testing.T) {
	t.Run("merges cross-listing preferring unsuffixed", func(t *testing.T) {
		out := Dedupe([]model.Candidate{
			{Ticker: "ASML", Name: "ASML Holding NV", Index: "nq100"},
			{Ticker: "ASML.AS", Name: "ASML Holding", Index: "eu50"},
		})
		if len(out) != 1 || out[0].Ticker != "ASML" || out[0].Index != "nq100" {
			t.Errorf("got %+v, want single ASML from nq100", out)
		}
	})

	t.Run("suffixed seen first still yields unsuffixed", func(t *testing.T) {
		out := Dedupe([]model.Candidate{
			{Ticker: "ASML.AS", Name: "ASML Holding", Index: "eu50"},
			{Ticker: "ASML", Name: "ASML Holding NV", Index: "nq100"},
		})
		if len(out) != 1 || out[0].Ticker != "ASML" {
			t.Errorf("got %v, want single ASML", tickers(out))
		}
	})

	t.Run("same stem different companies kept", func(t *testing.T) {
		out := Dedupe([]model.Candidate{
			{Ticker: "SAN.MC", Name: "Banco Santander"},
			{Ticker: "SAN.PA", Name: "Sanofi"},
			{Ticker: "2382.HK", Name: "Sunny Optical"},
			{Ticker: "2382.TW", Name: "Quanta Computer"},
		})
		if len(out) != 4 {
			t.Errorf("got %v, want all 4 kept", tickers(out))
		}
	})

	t.Run("class-share dot is not an exchange suffix", func(t *testing.T) {
		out := Dedupe([]model.Candidate{
			{Ticker: "BRK.B", Name: "Berkshire Hathaway"},
			{Ticker: "BRK.A", Name: "Berkshire Hathaway"},
		})
		if len(out) != 2 {
			t.Errorf("got %v, want both share classes kept", tickers(out))
		}
	})
}

func TestCapMerit(t *testing.T) {
	mk := func(ticker, index string) model.Candidate {
		return model.Candidate{Ticker: ticker, Name: ticker, Index: index}
	}
	// Scores are deliberately not in input order: the merge must rank, not
	// round-robin over whatever order the scouts happened to return.
	scores := map[string]float64{
		"A1": 0.5, "A2": 3.0, "A3": 2.5, "A4": 2.0,
		"B1": 1.0, "B2": 2.9,
		"C1": 0.1,
	}
	score := func(c model.Candidate) float64 { return scores[c.Ticker] }
	input := []model.Candidate{
		mk("A1", "sp500"), mk("A2", "sp500"), mk("A3", "sp500"), mk("A4", "sp500"),
		mk("B1", "nq100"), mk("B2", "nq100"),
		mk("C1", "eu50"),
	}

	t.Run("drops nothing under the cap, still ranks", func(t *testing.T) {
		out := CapMerit(input, 12, 5, score)
		if len(out) != len(input) {
			t.Fatalf("got %d names, want all %d kept", len(out), len(input))
		}
		if out[0].Ticker != "A2" {
			t.Errorf("best-scoring name is %s, want A2 first even when nothing is trimmed", out[0].Ticker)
		}
	})

	t.Run("keeps the best, ordered by score", func(t *testing.T) {
		out := CapMerit(input, 3, 5, score)
		want := []string{"A2", "B2", "A3"}
		if got := tickers(out); !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("per-index cap shapes the mix", func(t *testing.T) {
		// sp500 owns three of the four best composites; a per-index cap of one
		// forces the third slot to a third index instead of a second S&P name
		// (uncapped the answer would be A2, B2, A3).
		out := CapMerit(input, 3, 1, score)
		want := []string{"A2", "B2", "C1"}
		if got := tickers(out); !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("cap never shrinks the shortlist below max", func(t *testing.T) {
		// One index selected: a hard per-index cap would return 2 names when 4
		// were asked for. The cap is a diversification preference, so the
		// remaining slots are backfilled by pure merit.
		only := []model.Candidate{mk("A1", "sp500"), mk("A2", "sp500"), mk("A3", "sp500"), mk("A4", "sp500")}
		out := CapMerit(only, 4, 2, score)
		want := []string{"A2", "A3", "A4", "A1"}
		if got := tickers(out); !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("ties keep input order", func(t *testing.T) {
		flat := func(model.Candidate) float64 { return 0 }
		out := CapMerit(input, 3, 5, flat)
		want := []string{"A1", "A2", "A3"}
		if got := tickers(out); !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

func tickers(cs []model.Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Ticker
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
