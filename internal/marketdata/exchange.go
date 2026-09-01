package marketdata

import "strings"

// One table describing the markets this universe lists on, because two separate
// questions turn out to be the same question.
//
// Both were being answered wrongly by assuming every listing is American:
//
//   - **Currency.** `quant.Metrics.AvgDollarVol20` is Σ close·volume in whatever
//     the listing trades in, but it was named, printed and compared as USD. On
//     2026-09-01 that made asia100's median "ADV" read $10,015M and its maximum
//     $6,760,821M (SK Hynix, ₩6.76tn), so the $20M liquidity floor excluded one
//     name out of 274 — while a Tokyo name turning over ¥20M a day, about
//     $135k and untradeable, cleared it by a factor of 150.
//
//   - **When the session ends.** Stage 1.5 called a name stale if its newest bar
//     trailed the newest bar *anyone in the shortlist* had. That run began at
//     12:56 UTC: Tokyo and Frankfurt had published their 2026-09-01 bars and New
//     York had not opened, so all seven US names were declared stale against a
//     Japanese session, refetched pointlessly, and AMGN lost three confidence
//     points to a flag that meant "the US market has not opened yet".
//
// Both are answered by the exchange suffix, so both live here.
type exchange struct {
	// currency is the ISO code the listing quotes in, as Yahoo's FX pairs spell
	// it. GBp — London's pence — is not an ISO code and is handled by pence.
	currency string
	// pence marks a market quoting in minor units, where the FX rate must be
	// divided by 100. London is the only one in practice, and it is the classic
	// way to be wrong by two orders of magnitude.
	pence bool
	// closeUTC is the hour by which this market's daily bar should be published.
	// It is deliberately an hour or two after the actual close: the question is
	// "should the bar exist by now", and being late only delays a staleness
	// warning, while being early manufactures false ones.
	closeUTC int
}

// usClose is the hour by which a US session's daily bar is published. The NYSE
// close is 20:00 UTC in summer and 21:00 in winter; Yahoo's daily bar settles
// shortly after. 22:00 clears both without needing a tz database.
const usClose = 22

// usExchange is what a symbol with no foreign suffix resolves to — and also what
// an unrecognised suffix resolves to, because 22:00 is the latest close in the
// table and erring late cannot invent a staleness warning.
var usExchange = exchange{currency: "USD", closeUTC: usClose}

// exchanges maps a Yahoo suffix to its market. Every suffix in foreignExchanges
// (internal/marketdata/edgar.go) appears here, so the two tables cannot drift
// into disagreeing about what counts as foreign.
var exchanges = map[string]exchange{
	// North America. Toronto and the US close together.
	"TO": {currency: "CAD", closeUTC: usClose},
	"V":  {currency: "CAD", closeUTC: usClose},
	"NE": {currency: "CAD", closeUTC: usClose},
	"MX": {currency: "MXN", closeUTC: usClose},
	"SA": {currency: "BRL", closeUTC: usClose},
	"BA": {currency: "ARS", closeUTC: usClose},
	"SN": {currency: "CLP", closeUTC: usClose},

	// Europe: continental cash equities close 17:30 CET (16:30 UTC, 15:30 in
	// summer); London 16:30 GMT. 19:00 covers all of them year-round.
	"L":  {currency: "GBP", pence: true, closeUTC: 19}, // quoted in pence
	"IR": {currency: "EUR", closeUTC: 19},
	"AS": {currency: "EUR", closeUTC: 19},
	"BR": {currency: "EUR", closeUTC: 19},
	"PA": {currency: "EUR", closeUTC: 19},
	"LS": {currency: "EUR", closeUTC: 19},
	"MC": {currency: "EUR", closeUTC: 19},
	"MI": {currency: "EUR", closeUTC: 19},
	"VI": {currency: "EUR", closeUTC: 19},
	"AT": {currency: "EUR", closeUTC: 19},
	"HE": {currency: "EUR", closeUTC: 19},
	"TL": {currency: "EUR", closeUTC: 19},
	"VS": {currency: "EUR", closeUTC: 19},
	// German venues: Xetra plus the regional exchanges Yahoo carries.
	"DE": {currency: "EUR", closeUTC: 19},
	"F":  {currency: "EUR", closeUTC: 19},
	"BE": {currency: "EUR", closeUTC: 19},
	"DU": {currency: "EUR", closeUTC: 19},
	"HM": {currency: "EUR", closeUTC: 19},
	"MU": {currency: "EUR", closeUTC: 19},
	"SG": {currency: "EUR", closeUTC: 19}, // Stuttgart, not Singapore (.SI)
	// Non-euro Europe.
	"CO": {currency: "DKK", closeUTC: 19},
	"ST": {currency: "SEK", closeUTC: 19},
	"OL": {currency: "NOK", closeUTC: 19},
	"IC": {currency: "ISK", closeUTC: 19},
	"SW": {currency: "CHF", closeUTC: 19},
	"WA": {currency: "PLN", closeUTC: 19},
	"PR": {currency: "CZK", closeUTC: 19},
	"IS": {currency: "TRY", closeUTC: 19},
	"ME": {currency: "RUB", closeUTC: 19},
	"JO": {currency: "ZAR", closeUTC: 17}, // JSE closes 15:00 UTC
	"TA": {currency: "ILS", closeUTC: 16}, // TASE closes ~14:30 UTC

	// Asia-Pacific. All close well before the European open.
	"T":   {currency: "JPY", closeUTC: 8},  // Tokyo 15:00 JST = 06:00 UTC
	"HK":  {currency: "HKD", closeUTC: 10}, // 16:00 HKT = 08:00 UTC
	"SS":  {currency: "CNY", closeUTC: 10},
	"SZ":  {currency: "CNY", closeUTC: 10},
	"KS":  {currency: "KRW", closeUTC: 9}, // 15:30 KST = 06:30 UTC
	"KQ":  {currency: "KRW", closeUTC: 9},
	"TW":  {currency: "TWD", closeUTC: 8}, // 13:30 CST = 05:30 UTC
	"TWO": {currency: "TWD", closeUTC: 8},
	"SI":  {currency: "SGD", closeUTC: 12}, // 17:00 SGT = 09:00 UTC
	"KL":  {currency: "MYR", closeUTC: 12},
	"BK":  {currency: "THB", closeUTC: 12},
	"JK":  {currency: "IDR", closeUTC: 12},
	"VN":  {currency: "VND", closeUTC: 12},
	"NS":  {currency: "INR", closeUTC: 12}, // 15:30 IST = 10:00 UTC
	"BO":  {currency: "INR", closeUTC: 12},
	"AX":  {currency: "AUD", closeUTC: 8}, // 16:00 AEST = 06:00 UTC
	"NZ":  {currency: "NZD", closeUTC: 8},
	"CN":  {currency: "CAD", closeUTC: usClose}, // Canadian NEO/CSE
}

// exchangeFor resolves a ticker to its market. A symbol with no suffix, a US
// share-class suffix (BRK.B), or an unrecognised suffix is treated as US.
func exchangeFor(ticker string) exchange {
	i := strings.LastIndex(strings.TrimSpace(ticker), ".")
	if i <= 0 {
		return usExchange
	}
	if e, ok := exchanges[strings.ToUpper(strings.TrimSpace(ticker[i+1:]))]; ok {
		return e
	}
	return usExchange
}

// CurrencyOf returns the ISO currency a ticker trades in. London's GBp is
// reported as GBP; the pence factor lives in the FX rate, not the code, so a
// caller never has to remember it.
func CurrencyOf(ticker string) string { return exchangeFor(ticker).currency }

// MarketCloseUTC returns the hour by which a ticker's market should have
// published the daily bar for a completed session.
func MarketCloseUTC(ticker string) int { return exchangeFor(ticker).closeUTC }
