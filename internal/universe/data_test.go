package universe

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

// The CSVs are hand-maintained data, and on 2026-09-01 twelve of 274 rows were
// symbols Yahoo will not price. All five Singapore names used the company's
// initials rather than its SGX code, so Singapore was entirely absent from
// asia100 while five rows claimed it; `TEMU` is a brand, not a ticker; `WBA` and
// `EDF.PA` are delisted; `BASF.DE` and `DSM.AS` are the wrong codes for BAS.DE
// and DSFIR.AS; and one row had a *company name*, `T-Mobile`, in the ticker
// column, duplicating the TMUS row directly beneath it.
//
// The run reported all twelve as `no price history` and carried on, so the log's
// "screening 59 of nq100" was overstating every index but sp500. These two tests
// are the guard: shape here, and reachability under -tags network.

func allConstituents(t *testing.T) []model.Constituent {
	t.Helper()
	u, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Constituent
	for _, k := range AllIndices() {
		out = append(out, u.Constituents(k)...)
	}
	return out
}

// TestTickersAreWellFormed catches the cheap half without a network: anything
// that is obviously prose rather than a symbol.
func TestTickersAreWellFormed(t *testing.T) {
	for _, c := range allConstituents(t) {
		switch {
		case c.Ticker == "":
			t.Errorf("%s (%s): empty ticker", c.Name, c.Index)
		case strings.ContainsAny(c.Ticker, " \t"):
			t.Errorf("%q in %s contains whitespace — a company name in the ticker column",
				c.Ticker, c.Index)
		case strings.Contains(c.Ticker, "-"):
			// Yahoo writes US share classes with a hyphen (BRK-B) but every other
			// source and these files use the dotted form; yahooSymbol converts.
			// A hyphen here is therefore either a name or the wrong spelling.
			t.Errorf("%q in %s contains a hyphen — write US share classes dotted (BRK.B)",
				c.Ticker, c.Index)
		case c.Ticker != strings.ToUpper(c.Ticker):
			t.Errorf("%q in %s is not upper-case", c.Ticker, c.Index)
		}
	}
}

// TestNoDuplicateCompanyPerIndex catches the other half of the same defect: two
// rows for one company, which is how both `T-Mobile`/`TMUS` and
// `LVMH.PA`/`MC.PA` survived — one of each pair was dead and the live one made
// the index look intact.
func TestNoDuplicateCompanyPerIndex(t *testing.T) {
	seen := map[string]string{} // index|name → ticker
	for _, c := range allConstituents(t) {
		key := c.Index + "|" + strings.ToLower(strings.TrimSpace(c.Name))
		if prev, ok := seen[key]; ok {
			t.Errorf("%s appears twice in %s, as %s and %s — one of them is the dead spelling",
				c.Name, c.Index, prev, c.Ticker)
			continue
		}
		seen[key] = c.Ticker
	}
}

// TestEveryConstituentPrices is the real check and needs the network, so it is
// opt-in: CFR_UNIVERSE_CHECK=1 go test ./internal/universe/
//
// It is the only thing that can tell a valid-looking symbol from a tradeable
// one, and it is what would have caught all twelve dead rows the day they were
// written.
func TestEveryConstituentPrices(t *testing.T) {
	if os.Getenv("CFR_UNIVERSE_CHECK") == "" {
		t.Skip("set CFR_UNIVERSE_CHECK=1 to fetch every constituent from Yahoo")
	}
	yc := marketdata.NewYahooClient(marketdata.NewCache(""))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var dead []string
	for _, c := range allConstituents(t) {
		s, err := yc.History(ctx, c.Ticker)
		if err != nil {
			dead = append(dead, c.Ticker+" ("+c.Index+"): "+err.Error())
			continue
		}
		if len(s.Bars) < 60 {
			dead = append(dead, fmt.Sprintf("%s (%s): only %d bars, too few for the 12-1 term",
				c.Ticker, c.Index, len(s.Bars)))
		}
	}
	if len(dead) > 0 {
		t.Errorf("%d constituents do not price:\n  %s", len(dead), strings.Join(dead, "\n  "))
	}
}
