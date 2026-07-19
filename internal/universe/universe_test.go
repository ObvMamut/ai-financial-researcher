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
	tickers := func(cs []model.Candidate) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = c.Ticker
		}
		return out
	}

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

func TestCapBalanced(t *testing.T) {
	mk := func(ticker, index string) model.Candidate {
		return model.Candidate{Ticker: ticker, Name: ticker, Index: index}
	}
	input := []model.Candidate{
		mk("A1", "sp500"), mk("A2", "sp500"), mk("A3", "sp500"), mk("A4", "sp500"),
		mk("B1", "nq100"), mk("B2", "nq100"),
		mk("C1", "eu50"),
	}

	t.Run("no-op under cap", func(t *testing.T) {
		if out := CapBalanced(input, 12); len(out) != len(input) {
			t.Errorf("got %d, want unchanged %d", len(out), len(input))
		}
	})

	t.Run("round-robin across indices", func(t *testing.T) {
		out := CapBalanced(input, 5)
		want := []string{"A1", "B1", "C1", "A2", "B2"}
		if len(out) != len(want) {
			t.Fatalf("got %d items, want %d", len(out), len(want))
		}
		for i, w := range want {
			if out[i].Ticker != w {
				t.Errorf("out[%d] = %s, want %s", i, out[i].Ticker, w)
			}
		}
	})
}
