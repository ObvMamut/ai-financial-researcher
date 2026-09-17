package universe

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
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

// TestNoDuplicateTickerWithinAnIndex guards a data-entry slip that is silent
// until it reaches the statistics.
//
// nq100.csv carried PDD on two rows. The pre-screen fetched and ranked it twice,
// so it counted twice in that index's z-score mean and standard deviation —
// shifting every other name's composite — and could take two of the fifteen slots
// the scout sees. Nothing failed; the ranking was just slightly wrong.
func TestNoDuplicateTickerWithinAnIndex(t *testing.T) {
	u, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, idx := range AllIndices() {
		seen := map[string]bool{}
		for _, c := range u.Constituents(idx) {
			if seen[c.Ticker] {
				t.Errorf("%s lists %s more than once", idx, c.Ticker)
			}
			seen[c.Ticker] = true
		}
	}
}

// TestConstituentFieldsAreInTheRightColumns catches a shifted row. REGN sat in
// nq100.csv as `REGN,...,NASDAQ,Health Care,Health Care` — its country column
// holding a sector — which the parser accepted silently because it only counts
// commas.
func TestConstituentFieldsAreInTheRightColumns(t *testing.T) {
	u, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sectors := map[string]bool{}
	for _, idx := range AllIndices() {
		for _, c := range u.Constituents(idx) {
			if c.Sector != "" {
				sectors[c.Sector] = true
			}
		}
	}
	for _, idx := range AllIndices() {
		for _, c := range u.Constituents(idx) {
			switch {
			case c.Ticker == "" || c.Name == "":
				t.Errorf("%s: row with no ticker or name: %+v", idx, c)
			case c.Sector == "":
				t.Errorf("%s: %s has no sector — the risk gate's concentration check reads it", idx, c.Ticker)
			case sectors[c.Country]:
				t.Errorf("%s: %s has country %q, which is a sector — the row is shifted", idx, c.Ticker, c.Country)
			case len(c.Country) > 3:
				t.Errorf("%s: %s has country %q, want an ISO-style code", idx, c.Ticker, c.Country)
			}
		}
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
		out := CapMerit(input, MeritCaps{Max: 12, PerIndex: 5, Score: score})
		if len(out) != len(input) {
			t.Fatalf("got %d names, want all %d kept", len(out), len(input))
		}
		if out[0].Ticker != "A2" {
			t.Errorf("best-scoring name is %s, want A2 first even when nothing is trimmed", out[0].Ticker)
		}
	})

	t.Run("keeps the best, ordered by score", func(t *testing.T) {
		out := CapMerit(input, MeritCaps{Max: 3, PerIndex: 5, Score: score})
		want := []string{"A2", "B2", "A3"}
		if got := tickers(out); !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("per-index cap shapes the mix", func(t *testing.T) {
		// sp500 owns three of the four best composites; a per-index cap of one
		// forces the third slot to a third index instead of a second S&P name
		// (uncapped the answer would be A2, B2, A3).
		out := CapMerit(input, MeritCaps{Max: 3, PerIndex: 1, Score: score})
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
		out := CapMerit(only, MeritCaps{Max: 4, PerIndex: 2, Score: score})
		want := []string{"A2", "A3", "A4", "A1"}
		if got := tickers(out); !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("ties keep input order", func(t *testing.T) {
		flat := func(model.Candidate) float64 { return 0 }
		out := CapMerit(input, MeritCaps{Max: 3, PerIndex: 5, Score: flat})
		want := []string{"A1", "A2", "A3"}
		if got := tickers(out); !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

// asia100 is this project's own grouping across eight exchanges with no index in
// common, and every one of them was benchmarked against ^N225. O39.SI, a
// Singapore bank, had its beta, its correlation and its displayed RS63 computed
// against the Nikkei.
func TestBenchmarkForMultiMarketIndices(t *testing.T) {
	cases := []struct{ index, ticker, want string }{
		{"asia100", "O39.SI", "^STI"},
		{"asia100", "8035.T", "^N225"},
		{"asia100", "005930.KS", "^KS11"},
		{"asia100", "2330.TW", "^TWII"},
		{"asia100", "HDFCBANK.NS", "^NSEI"},
		{"asia100", "0700.HK", "^HSI"},
		{"asia100", "BHP.AX", "^AXJO"},
		// An unrecognised suffix, or none, falls back to the index default.
		{"asia100", "SOMETHING.ZZ", "^N225"},
		{"asia100", "PLAIN", "^N225"},
		// A single-market index is answered by the index, ticker or not: ^GSPC,
		// ^NDX and ^STOXX50E each describe one market their members belong to.
		{"sp500", "NVDA", "^GSPC"},
		{"nq100", "MU", "^NDX"},
		{"eu50", "ASML.AS", "^STOXX50E"},
		{"eu50", "BMW.DE", "^STOXX50E"},
		{"", "NVDA", "^GSPC"},
	}
	for _, c := range cases {
		if got := BenchmarkFor(c.index, c.ticker); got != c.want {
			t.Errorf("BenchmarkFor(%q, %q) = %s, want %s", c.index, c.ticker, got, c.want)
		}
	}

	// Every suffix asia100 actually contains has a benchmark of its own, or the
	// map has fallen behind the CSV.
	u, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range u.Constituents("asia100") {
		if got := BenchmarkFor("asia100", c.Ticker); got == "^N225" && !strings.HasSuffix(strings.ToUpper(c.Ticker), ".T") {
			t.Errorf("%s falls back to the Nikkei — marketBenchmarks has no entry for its exchange", c.Ticker)
		}
	}
}

// The merge ranked on the pre-screen composite alone, with no notion of whether
// the run's providers could reach a name. SEC EDGAR and AlphaVantage are US-only,
// so a non-US listing with no US line is graded by quant alone — one domain of
// five. On 2026-09-01 that put 7 quant-only names on a 12-name shortlist, and
// three of the five shipped ideas rested on a single domain.
func TestCapMeritBoundsTheNamesOnlyQuantCanGrade(t *testing.T) {
	mk := func(ticker, index string, score float64) model.Candidate {
		return model.Candidate{Ticker: ticker, Name: ticker, Index: index, Reason: fmt.Sprint(score)}
	}
	score := func(c model.Candidate) float64 {
		v, _ := strconv.ParseFloat(c.Reason, 64)
		return v
	}
	// A foreign listing with no US line reaches quant and macro — the two
	// computed from its own bars — but not fundamentals, sentiment or news:
	// 0.45 of the weight where a US name reaches all of it.
	coverage := func(c model.Candidate) float64 {
		if strings.Contains(c.Ticker, ".") {
			return 0.45
		}
		return 1.0
	}

	// The top six composites are all names four of the five domains cannot see.
	input := []model.Candidate{
		mk("8035.T", "asia100", 9), mk("O39.SI", "asia100", 8),
		mk("BAYN.DE", "eu50", 7), mk("NESTE.HE", "eu50", 6),
		mk("BMW.DE", "eu50", 5), mk("STLAM.MI", "eu50", 4),
		mk("MU", "sp500", 3), mk("ORCL", "sp500", 2), mk("TTD", "sp500", 1),
	}

	out := CapMerit(input, MeritCaps{
		Max: 6, PerIndex: 5, ThinlyCovered: 2, CoverageFloor: 0.8,
		Score: score, Coverage: coverage,
	})
	got := tickers(out)
	if want := []string{"8035.T", "O39.SI", "MU", "ORCL", "TTD"}; !equal(got, want) {
		t.Errorf("got %v, want %v — two thinly covered names, then every fully covered one", got, want)
	}
	// The cap is hard in the backfill too. A soft one would be no cap at all:
	// the second pass would refill exactly the slots the first just protected.
	if len(out) >= 6 {
		t.Errorf("got %d names, want fewer than max — the backfill ignored the cap", len(out))
	}

	// A nil Coverage, a zero floor or a negative cap leaves the shortlist
	// untouched: the first two mean the caller cannot answer the question, the
	// third means it answered "do not ask".
	for _, caps := range []MeritCaps{
		{Max: 6, PerIndex: 5, ThinlyCovered: 2, CoverageFloor: 0.8, Score: score},
		{Max: 6, PerIndex: 5, ThinlyCovered: 2, Score: score, Coverage: coverage},
		{Max: 6, PerIndex: 5, ThinlyCovered: -1, CoverageFloor: 0.8, Score: score, Coverage: coverage},
	} {
		if got := len(CapMerit(input, caps)); got != 6 {
			t.Errorf("an unconfigured coverage cap trimmed the shortlist to %d", got)
		}
	}

	// A zero cap admits none of them, and that is the default rather than an
	// edge case: a name under the floor can be scored by the price-derived
	// domains alone, and the risk gate deletes a price-only idea outright. On
	// 2026-09-05 the cap of four seated four such names in a twelve-name
	// shortlist, all four were deleted, and the run shipped two ideas.
	none := CapMerit(input, MeritCaps{
		Max: 6, PerIndex: 5, ThinlyCovered: 0, CoverageFloor: 0.8,
		Score: score, Coverage: coverage,
	})
	if want := []string{"MU", "ORCL", "TTD"}; !equal(tickers(none), want) {
		t.Errorf("a zero cap returned %v, want only the fully covered %v", tickers(none), want)
	}

	// And a floor the sources clear is no cap: the same six names come back. This
	// is what makes the cap loosen by itself as coverage widens — map these
	// listings to US lines and they stop being capped — rather than needing to
	// be retuned. It is also how the cap died in practice: while news counted as
	// globally groundable, every listing in the universe expected 0.70, the
	// production floor of 0.6 was under all of them, and `thinly_covered` was
	// null on every run.
	loose := CapMerit(input, MeritCaps{
		Max: 6, PerIndex: 5, ThinlyCovered: 2, CoverageFloor: 0.4,
		Score: score, Coverage: coverage,
	})
	if got := len(loose); got != 6 {
		t.Errorf("got %d names under a floor every candidate clears, want 6", got)
	}
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

func TestDedupeCountsAgreementBetweenScouts(t *testing.T) {
	// REGN and AMGN were each nominated by the sp500 and nq100 scouts on
	// 2026-09-03 — the strongest cross-index agreement the screening stage can
	// produce — and both were dropped by a merit sort that ranked on the
	// pre-screen composite alone, because the merge left no trace of the second
	// nomination.
	got := Dedupe([]model.Candidate{
		{Ticker: "REGN", Name: "Regeneron", Bias: model.BiasBullish, Index: "sp500"},
		{Ticker: "MU", Name: "Micron", Bias: model.BiasBullish, Index: "sp500"},
		{Ticker: "REGN", Name: "Regeneron", Bias: model.BiasBullish, Index: "nq100"},
	})
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want REGN and MU merged to 2: %+v", len(got), got)
	}
	if got[0].Ticker != "REGN" || got[0].Nominations != 2 {
		t.Errorf("REGN nominations = %d, want 2: %+v", got[0].Nominations, got[0])
	}
	if got[0].Index != "sp500" {
		t.Errorf("the merged row moved index to %q — the first-seen row is authoritative", got[0].Index)
	}
	if got[1].Nominations != 1 {
		t.Errorf("MU nominations = %d, want 1", got[1].Nominations)
	}
	if len(got[0].Contested) != 0 {
		t.Errorf("two scouts agreeing were recorded as contested: %v", got[0].Contested)
	}
}

func TestDedupeRecordsOppositeBiasesRatherThanPickingOne(t *testing.T) {
	// QCOM, 2026-09-03: nq100 nominated it bullish and sp500 bearish. The merge
	// kept whichever came first and said nothing, so the direction the pipeline
	// traded was decided by the order the scouts were collected in.
	got := Dedupe([]model.Candidate{
		{Ticker: "QCOM", Name: "Qualcomm", Bias: model.BiasBearish, Index: "sp500"},
		{Ticker: "QCOM", Name: "Qualcomm", Bias: model.BiasBullish, Index: "nq100"},
	})
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(got), got)
	}
	if got[0].Bias != model.BiasBearish {
		t.Errorf("bias = %s, want the first-seen bearish reading", got[0].Bias)
	}
	if want := []string{"nq100"}; !reflect.DeepEqual(got[0].Contested, want) {
		t.Errorf("contested = %v, want %v", got[0].Contested, want)
	}
	// A contradiction is not agreement, so it must not also pay the bonus.
	if got[0].Nominations != 1 {
		t.Errorf("nominations = %d — an opposite reading is not a second vote", got[0].Nominations)
	}
}

// The merit sort ranks on the pre-screen composite, and that composite is built
// from trailing returns — so left alone it fills the shortlist with whatever has
// run hardest. On 2026-09-04 that was three longs at 0.993, 0.982 and 1.000 of
// their 52-week highs. The reserve holds slots for the other setup archetypes.
func TestCapMeritArchetypeReserve(t *testing.T) {
	mk := func(ticker, setup string) model.Candidate {
		return model.Candidate{Ticker: ticker, Name: ticker, Index: "sp500", Setup: setup}
	}
	notContinuation := func(c model.Candidate) bool { return c.Setup != "" && c.Setup != "continuation" }

	t.Run("holds slots for other archetypes", func(t *testing.T) {
		// Every continuation name outranks every pullback, which is the normal
		// case rather than an adversarial one: the composite rewards having run.
		scores := map[string]float64{"C1": 3.0, "C2": 2.8, "C3": 2.6, "C4": 2.4, "P1": 1.2, "P2": 0.9}
		in := []model.Candidate{
			mk("C1", "continuation"), mk("C2", "continuation"),
			mk("C3", "continuation"), mk("C4", "continuation"),
			mk("P1", "pullback"), mk("P2", "base"),
		}
		out := CapMerit(in, MeritCaps{
			Max: 4, PerIndex: 5, Reserve: 2, ReserveMinMerit: 0.5,
			ReservePredicate: notContinuation,
			Score:            func(c model.Candidate) float64 { return scores[c.Ticker] },
		})
		// Reserved names are selected first but presented in rank order.
		if got, want := tickers(out), []string{"C1", "C2", "P1", "P2"}; !equal(got, want) {
			t.Errorf("got %v, want %v — two slots held for the non-continuation shapes", got, want)
		}
	})

	t.Run("ships short rather than reserving a bad name", func(t *testing.T) {
		// The reserve is soft in one direction only. A pullback below the merit
		// floor leaves the slot empty; it does not get promoted into it.
		scores := map[string]float64{"C1": 3.0, "C2": 2.8, "C3": 2.6, "P1": 0.1}
		in := []model.Candidate{
			mk("C1", "continuation"), mk("C2", "continuation"),
			mk("C3", "continuation"), mk("P1", "pullback"),
		}
		out := CapMerit(in, MeritCaps{
			Max: 3, PerIndex: 5, Reserve: 2, ReserveMinMerit: 0.5,
			ReservePredicate: notContinuation,
			Score:            func(c model.Candidate) float64 { return scores[c.Ticker] },
		})
		if got, want := tickers(out), []string{"C1", "C2", "C3"}; !equal(got, want) {
			t.Errorf("got %v, want %v — P1 is below the merit floor and must not take a reserved slot", got, want)
		}
	})

	t.Run("reserve does not override the per-index cap", func(t *testing.T) {
		scores := map[string]float64{"C1": 3.0, "P1": 2.0, "P2": 1.9}
		in := []model.Candidate{
			{Ticker: "C1", Index: "sp500", Setup: "continuation"},
			{Ticker: "P1", Index: "eu50", Setup: "pullback"},
			{Ticker: "P2", Index: "eu50", Setup: "pullback"},
		}
		out := CapMerit(in, MeritCaps{
			Max: 3, PerIndex: 1, Reserve: 2, ReserveMinMerit: 0.5,
			ReservePredicate: notContinuation,
			Score:            func(c model.Candidate) float64 { return scores[c.Ticker] },
		})
		// P2 is a second eu50 name; the reserve may not smuggle it past PerIndex
		// on the first pass, though the ordinary backfill still rescues it.
		if got, want := tickers(out), []string{"C1", "P1", "P2"}; !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("nil predicate disables the reserve", func(t *testing.T) {
		scores := map[string]float64{"C1": 3.0, "P1": 1.0}
		in := []model.Candidate{mk("C1", "continuation"), mk("P1", "pullback")}
		out := CapMerit(in, MeritCaps{
			Max: 1, PerIndex: 5, Reserve: 2, ReserveMinMerit: 0.5,
			Score: func(c model.Candidate) float64 { return scores[c.Ticker] },
		})
		if got, want := tickers(out), []string{"C1"}; !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

// The risk gate refuses a book with more than two ideas in one sector, and until
// PerSector existed nothing upstream knew that. On 2026-09-05 the merit sort —
// which has no notion of sector — returned a shortlist seven-twelfths
// Information Technology, seven of the eight names that cleared the evidence
// floor were IT, and the gate's own limit then cut the book to two ideas. The
// funnel was maximising exactly the quantity the gate forbids.
func TestCapMeritSpreadsTheShortlistAcrossSectors(t *testing.T) {
	mk := func(ticker, sector string) model.Candidate {
		return model.Candidate{Ticker: ticker, Index: "sp500", Sector: sector,
			Bias: model.BiasBullish, Name: ticker + " Inc"}
	}
	scores := map[string]float64{
		"MU": 9, "PANW": 8, "SNPS": 7, "OKTA": 6, "CRM": 5, // Information Technology
		"MRK": 4, "AMGN": 3, // Health Care
		"PYPL": 2, // Financials
	}
	score := func(c model.Candidate) float64 { return scores[c.Ticker] }
	input := []model.Candidate{
		mk("MU", "Information Technology"), mk("PANW", "Information Technology"),
		mk("SNPS", "Information Technology"), mk("OKTA", "Information Technology"),
		mk("CRM", "Information Technology"),
		mk("MRK", "Health Care"), mk("AMGN", "Health Care"),
		mk("PYPL", "Financials"),
	}

	out := CapMerit(input, MeritCaps{Max: 5, PerSector: 3, Score: score})
	got := tickers(out)
	if want := []string{"MU", "PANW", "SNPS", "MRK", "AMGN"}; !equal(got, want) {
		t.Errorf("got %v, want %v — three IT names, then the next sector", got, want)
	}

	// Soft, like PerIndex: the backfill overrides it rather than hand back a
	// short shortlist. A thin list is worse than a concentrated one, because the
	// gate can decline a crowded sector but cannot conjure a name that never
	// reached it.
	full := CapMerit(input, MeritCaps{Max: 8, PerSector: 3, Score: score})
	if len(full) != 8 {
		t.Errorf("the backfill returned %d of 8 — PerSector must not be able to shorten the list", len(full))
	}
	if first := tickers(full)[:3]; !equal(first, []string{"MU", "PANW", "SNPS"}) {
		t.Errorf("backfilled list starts %v, want the ranking preserved", first)
	}

	// Zero disables it, which is what an unset cap means.
	if got := tickers(CapMerit(input, MeritCaps{Max: 5, Score: score})); !equal(got, []string{"MU", "PANW", "SNPS", "OKTA", "CRM"}) {
		t.Errorf("an unset PerSector trimmed by sector anyway: %v", got)
	}

	// A candidate with no sector is not counted against any cap: an unknown
	// sector is not a sector they all share.
	unsectored := append([]model.Candidate{}, input[:3]...)
	unsectored = append(unsectored, mk("XXX", ""), mk("YYY", ""))
	scores["XXX"], scores["YYY"] = 1, 0
	if got := len(CapMerit(unsectored, MeritCaps{Max: 5, PerSector: 3, Score: score})); got != 5 {
		t.Errorf("unsectored names were capped together, got %d of 5", got)
	}
}
