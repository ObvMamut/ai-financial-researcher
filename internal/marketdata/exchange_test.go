package marketdata

import "testing"

// The two suffix tables answer the same question and must not drift: a symbol
// foreignExchanges calls foreign but exchanges has never heard of would resolve
// to usExchange and be silently priced in dollars on New York's clock — the
// exact failure this table exists to end.
func TestEveryForeignSuffixHasAnExchange(t *testing.T) {
	for suffix := range foreignExchanges {
		e, ok := exchanges[suffix]
		if !ok {
			t.Errorf(".%s is in foreignExchanges but has no exchange entry — it would default to USD/NYSE", suffix)
			continue
		}
		if e.currency == "" {
			t.Errorf(".%s has no currency", suffix)
		}
		if e.currency == "USD" {
			t.Errorf(".%s is marked foreign but quotes in USD", suffix)
		}
		if e.closeUTC < 0 || e.closeUTC > 23 {
			t.Errorf(".%s has close hour %d, want 0-23", suffix, e.closeUTC)
		}
	}
	for suffix := range exchanges {
		if !foreignExchanges[suffix] {
			t.Errorf(".%s has an exchange entry but foreignExchanges does not list it — "+
				"the two tables disagree about what counts as foreign", suffix)
		}
	}
}

func TestExchangeForResolvesSuffixes(t *testing.T) {
	cases := []struct {
		ticker   string
		currency string
		close    int
	}{
		{"AMGN", "USD", usClose},
		{"MU", "USD", usClose},           // a bare symbol, not the .MU Munich suffix
		{"BRK.B", "USD", usClose},        // a US share class, not a foreign listing
		{"8035.T", "JPY", 8},             // Tokyo closes at 06:00 UTC
		{"BAYN.DE", "EUR", 19},           // Xetra
		{"STLAM.MI", "EUR", 19},          // Borsa Italiana
		{"000660.KS", "KRW", 9},          // Seoul
		{"D05.SI", "SGD", 12},            // Singapore, not Stuttgart
		{"SHEL.L", "GBP", 19},            // London
		{"WHATEVER.ZZZ", "USD", usClose}, // an unknown suffix errs late, never early
	}
	for _, c := range cases {
		if got := CurrencyOf(c.ticker); got != c.currency {
			t.Errorf("CurrencyOf(%s) = %s, want %s", c.ticker, got, c.currency)
		}
		if got := MarketCloseUTC(c.ticker); got != c.close {
			t.Errorf("MarketCloseUTC(%s) = %d, want %d", c.ticker, got, c.close)
		}
	}
}

// London quotes in pence, so its rate is GBPUSD/100. Missing that overstates a
// UK listing's turnover and price by a hundredfold, in the direction that makes
// it look liquid.
func TestLondonIsQuotedInPence(t *testing.T) {
	if !exchangeFor("SHEL.L").pence {
		t.Fatal(".L must be marked as a minor-unit market")
	}
	f := NewFXRates(nil)
	const gbpUSD = 1.27
	if got, want := f.scale(exchangeFor("SHEL.L"), gbpUSD), gbpUSD/100; got != want {
		t.Errorf("pence scaling = %.6f, want %.6f", got, want)
	}
	if got := f.scale(exchangeFor("BAYN.DE"), 1.08); got != 1.08 {
		t.Errorf("a major-unit market must not be scaled, got %.6f", got)
	}
}

// A US listing must not touch the network, and must not be cached as if it had.
func TestFXRatesShortCircuitsUSListings(t *testing.T) {
	f := NewFXRates(nil) // a nil client would panic if it were used
	rate, code, err := f.ToUSD(t.Context(), "AMGN")
	if err != nil || rate != 1 || code != "USD" {
		t.Errorf("ToUSD(AMGN) = (%v, %q, %v), want (1, USD, nil)", rate, code, err)
	}
	if usd, ok := f.Convert(t.Context(), "AMGN", 500e6); !ok || usd != 500e6 {
		t.Errorf("Convert(AMGN, 500e6) = (%v, %v), want (500e6, true)", usd, ok)
	}
}
