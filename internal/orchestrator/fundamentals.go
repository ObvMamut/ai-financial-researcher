package orchestrator

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// multipleStaleLimit is how old a filed figure may be, relative to the price it
// would be divided into, before the quotient stops meaning anything. A little
// over a year covers an annual filing plus its lag; past that a P/E is a ratio
// between a current price and a company that no longer exists in that form.
const multipleStaleLimit = 400 * 24 * time.Hour

// enrichFundamentals computes the valuation multiples in Go and appends them to
// the fundamentals pack.
//
// The specialist was asked to judge whether a name was expensive while being
// shown filed dollar amounts and no price at all, so "trading at a rich
// multiple" was an assertion about a number nobody had computed. The inputs are
// all here — shares outstanding and diluted EPS from the filings, the last close
// from the quant pack — and dividing them is arithmetic, not analysis.
//
// When the inputs are too far apart in time to divide, it says so explicitly.
// An absent multiple is filled in from recollection; a line that states the
// multiple is not computable, and why, is not.
func enrichFundamentals(pack *marketdata.DataPack, qp *quant.Pack) {
	if pack == nil || qp == nil {
		return
	}
	for ticker, td := range pack.ByTicker {
		m, ok := qp.ByTicker[strings.ToUpper(ticker)]
		if !ok || m.LastClose <= 0 {
			continue
		}
		priceDate, err := time.Parse("2006-01-02", m.AsOf)
		if err != nil {
			continue
		}

		shares, sharesAsOf, haveShares := numericFact(td.Facts, marketdata.FactShares)
		eps, epsAsOf, haveEPS := numericFact(td.Facts, marketdata.FactEPSDiluted)
		revenue, revAsOf, haveRev := numericFact(td.Facts, marketdata.FactRevenue)

		var added []marketdata.Fact
		note := func(label, value string, asOf time.Time) {
			added = append(added, marketdata.Fact{
				Label: label, Value: value, AsOf: asOf, Source: "computed in-process",
			})
		}
		fresh := func(asOf time.Time) bool {
			return !asOf.IsZero() && priceDate.Sub(asOf) <= multipleStaleLimit
		}

		// The close is in the listing's own currency; every figure EDGAR files is
		// in USD. Dividing one into the other without converting produced
		// "Market cap (computed) = $143.24B" for BBVA.MC on 2026-09-03, off a
		// €25.09 close — €143.24B, which is $166.2B. The rate is already on the
		// metric in hand (quant.Metrics.ApplyFX), so the only question is what to
		// do when there isn't one: say so, rather than print a dollar sign over
		// a number that is not dollars.
		closeUSD, haveUSD := usdClose(m)
		if !haveUSD {
			note("Valuation multiples", fmt.Sprintf(
				"not computable: the close is in %s and no %s/USD rate was available this run, while every figure filed with the SEC is in USD — a ratio across the two would be a currency error, not a valuation",
				m.Currency, m.Currency), priceDate)
			td.Facts = append(td.Facts, added...)
			pack.ByTicker[ticker] = td
			continue
		}
		fxNote := ""
		if m.Currency != "" && m.Currency != "USD" {
			fxNote = fmt.Sprintf(", %s→USD at %.4f", m.Currency, m.FXToUSD)
		}

		var marketCap float64
		if haveShares && shares > 0 {
			marketCap = shares * closeUSD
			if fresh(sharesAsOf) {
				note("Market cap (computed)", fmt.Sprintf("$%s (%.0f shares × close %.2f on %s%s)",
					usdCompact(marketCap), shares, m.LastClose, m.AsOf, fxNote), priceDate)
			} else {
				note("Market cap", fmt.Sprintf("not computable: share count is from %s, %s before the price",
					sharesAsOf.Format("2006-01-02"), humanDays(priceDate.Sub(sharesAsOf))), priceDate)
				marketCap = 0
			}
		} else {
			note("Market cap", "not computable: no share count in this filer's data", priceDate)
		}

		switch {
		case haveEPS && eps > 0 && fresh(epsAsOf):
			note("P/E (computed, trailing diluted)", fmt.Sprintf("%.1f (close %.2f ÷ EPS %.2f as of %s%s)",
				closeUSD/eps, m.LastClose, eps, epsAsOf.Format("2006-01-02"), fxNote), priceDate)
		case haveEPS && eps <= 0:
			note("P/E", fmt.Sprintf("not meaningful: diluted EPS is %.2f", eps), priceDate)
		case haveEPS:
			note("P/E", fmt.Sprintf("not computable: EPS is from %s, %s before the price",
				epsAsOf.Format("2006-01-02"), humanDays(priceDate.Sub(epsAsOf))), priceDate)
		default:
			note("P/E", "not computable: no diluted EPS in this filer's data", priceDate)
		}

		if marketCap > 0 && haveRev && revenue > 0 && fresh(revAsOf) {
			note("P/S (computed)", fmt.Sprintf("%.1f (market cap ÷ revenue %s as of %s)",
				marketCap/revenue, "$"+usdCompact(revenue), revAsOf.Format("2006-01-02")), priceDate)
		}

		td.Facts = append(td.Facts, added...)
		pack.ByTicker[ticker] = td
	}
}

// usdClose is the last close in USD, and false when the listing's currency is
// known but its rate is not. A listing with no currency recorded at all is a US
// one — quantstage only calls ApplyFX where a suffix names a market — so its
// close is already dollars.
func usdClose(m quant.Metrics) (float64, bool) {
	if m.Currency == "" || m.Currency == "USD" {
		return m.LastClose, true
	}
	if m.FXToUSD <= 0 {
		return 0, false
	}
	return m.LastClose * m.FXToUSD, true
}

// numericFact finds a fact by label, tolerating the "(quarterly)" suffix the
// EDGAR extractor appends when it had to fall back to a quarterly frame.
func numericFact(facts []marketdata.Fact, label string) (value float64, asOf time.Time, ok bool) {
	for _, f := range facts {
		if f.Label != label && f.Label != label+" (quarterly)" {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(f.Value), 64)
		if err != nil {
			continue
		}
		return v, f.AsOf, true
	}
	return 0, time.Time{}, false
}

func usdCompact(v float64) string {
	switch {
	case v >= 1e12:
		return fmt.Sprintf("%.2fT", v/1e12)
	case v >= 1e9:
		return fmt.Sprintf("%.2fB", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.2fM", v/1e6)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

func humanDays(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days >= 365 {
		return fmt.Sprintf("%.1f years", float64(days)/365)
	}
	return fmt.Sprintf("%d days", days)
}
