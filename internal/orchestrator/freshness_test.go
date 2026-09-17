package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
)

// nyClose is the US close hour, taken from the real table so this test cannot
// drift away from the code it is checking.
var nyClose = marketdata.MarketCloseUTC("AMGN")

func at(t *testing.T, ts string) time.Time {
	t.Helper()
	now, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatal(err)
	}
	return now
}

// A Saturday run priced 8 of 12 names off Thursday closes: the US names' cache
// entries predated Friday's bar while FCX and the Asian names had it. Nothing
// compared the two, so precise entries shipped off a session-old close.
func TestStaleTickersAgainstTheirOwnMarket(t *testing.T) {
	// Saturday noon: every market's last completed session is Friday 2026-08-28.
	sat := at(t, "2026-08-29T12:00:00Z")
	asOf := map[string]string{
		"NVDA": "2026-08-27", // Thursday
		"MU":   "2026-08-27",
		"FCX":  "2026-08-28", // Friday
		"TSM":  "2026-08-28",
	}
	if got, want := staleTickers(asOf, sat), []string{"MU", "NVDA"}; !equalStrings(got, want) {
		t.Errorf("staleTickers = %v, want %v", got, want)
	}
	if got := staleTickers(map[string]string{"A": "2026-08-28", "B": "2026-08-28"}, sat); len(got) != 0 {
		t.Errorf("staleTickers = %v on a uniform pack, want none", got)
	}
	// A missing date cannot be judged and must not be reported as stale.
	if got := staleTickers(map[string]string{"A": "", "B": "2026-08-28"}, sat); len(got) != 0 {
		t.Errorf("staleTickers = %v, want none — an unknown date is not evidence of staleness", got)
	}
}

// The exact shape of the 2026-09-01 run: 12:56 UTC on a Tuesday, Tokyo and
// Frankfurt done for the day, New York not yet open. Comparing every name to the
// newest bar *anyone* had declared all seven US names stale against a Japanese
// session, wasted seven refetches, and cost AMGN three confidence points.
func TestStaleTickersDoesNotJudgeNewYorkByTokyosClock(t *testing.T) {
	now := at(t, "2026-09-01T12:56:00Z")
	asOf := map[string]string{
		"8035.T":   "2026-09-01", // Tokyo closed at 06:00 UTC
		"9984.T":   "2026-09-01",
		"BAYN.DE":  "2026-09-01", // Frankfurt still open, but 08-31 would be stale
		"STLAM.MI": "2026-09-01",
		"AMGN":     "2026-08-31", // NYSE opens at 13:30 UTC — this is current
		"MU":       "2026-08-31",
		"ORCL":     "2026-08-31",
	}
	if got := staleTickers(asOf, now); len(got) != 0 {
		t.Errorf("staleTickers = %v, want none — every name is on its own market's last session", got)
	}

	// The genuine case still fires: a US name two sessions behind is stale even
	// though the US market has not opened today.
	asOf["ORCL"] = "2026-08-28"
	if got, want := staleTickers(asOf, now), []string{"ORCL"}; !equalStrings(got, want) {
		t.Errorf("staleTickers = %v, want %v", got, want)
	}
	// So is a Tokyo name that missed today's session, which the old global-max
	// comparison would also have caught only by accident.
	asOf["8035.T"] = "2026-08-31"
	if got, want := staleTickers(asOf, now), []string{"8035.T", "ORCL"}; !equalStrings(got, want) {
		t.Errorf("staleTickers = %v, want %v", got, want)
	}
}

// Stale prices reached data_errors and stopped there, so a run could ship an
// entry, a stop and a target computed to the cent off a superseded session with
// nothing in `warnings` about it. A stale name no idea rests on is a data note; a
// stale name in the output is a caveat on a level someone would trade.
func TestStaleIdeasNamesOnlyTheOnesThatShipped(t *testing.T) {
	pack := &quant.Pack{Stale: []string{"8035.T", "ORCL"}}
	res := &model.IdeasResult{Ideas: []model.TradeIdea{
		{Ticker: "MU"}, {Ticker: "orcl"}, {Ticker: "BAYN.DE"},
	}}
	if got, want := staleIdeas(pack, res), []string{"ORCL"}; !equalStrings(got, want) {
		t.Errorf("staleIdeas = %v, want %v — 8035.T was dropped before shipping", got, want)
	}
	if got := staleIdeas(&quant.Pack{}, res); len(got) != 0 {
		t.Errorf("staleIdeas = %v on a fresh pack, want none", got)
	}
	if got := staleIdeas(pack, &model.IdeasResult{}); len(got) != 0 {
		t.Errorf("staleIdeas = %v with no ideas, want none", got)
	}
}

// The pack can be stale as a whole — every name a session behind — which no
// cross-ticker comparison can see. Each market's own last completed session is
// the reference.
func TestLastTradingDayPerMarket(t *testing.T) {
	cases := []struct {
		now   string // a wall-clock instant in UTC
		close int
		want  string
	}{
		// Saturday and Sunday both look back to Friday.
		{"2026-08-29T12:00:00Z", nyClose, "2026-08-28"},
		{"2026-08-30T12:00:00Z", nyClose, "2026-08-28"},
		// Monday before the US close still expects Friday's bar.
		{"2026-08-31T12:00:00Z", nyClose, "2026-08-28"},
		// Monday well after the close expects Monday's.
		{"2026-08-31T23:00:00Z", nyClose, "2026-08-31"},
		// Mid-week after the close.
		{"2026-09-02T23:00:00Z", nyClose, "2026-09-02"},
		// Mid-week before it: the previous weekday.
		{"2026-09-02T09:00:00Z", nyClose, "2026-09-01"},
		// Tokyo closes at 06:00 UTC, so by 09:00 the same day's bar exists —
		// the hour at which the US is still on yesterday's.
		{"2026-09-02T09:00:00Z", 8, "2026-09-02"},
		// Before Tokyo's own close it is still yesterday's bar.
		{"2026-09-02T07:00:00Z", 8, "2026-09-01"},
	}
	for _, c := range cases {
		if got := lastTradingDay(at(t, c.now), c.close); got != c.want {
			t.Errorf("lastTradingDay(%s, close=%d) = %s, want %s", c.now, c.close, got, c.want)
		}
	}
}

func TestAStaleIdeaBecomesACorrectiveFinding(t *testing.T) {
	// NESTE.HE shipped at rank 4 on 2026-09-03 with levels computed to the cent
	// off a 2026-09-01 bar against a 2026-09-02 session. Every part of the
	// machinery worked — forced refetch, a `stale:` flag on the metric, a
	// run-level warning — and none of it reached the Chief as something to fix.
	qp := quant.NewPack()
	qp.ByTicker["NESTE.HE"] = quant.Metrics{Symbol: "NESTE.HE", Benchmark: "^STOXX50E", Beta: 0.8}
	qp.ByTicker["MU"] = quant.Metrics{Symbol: "MU", Benchmark: "^GSPC", Beta: 2.8}
	qp.Stale = []string{"NESTE.HE"}
	v := verified{Universe: testUniverse(t), Quant: qp}

	stale := model.TradeIdea{
		Ticker: "NESTE.HE", Direction: model.DirectionBuy, Confidence: 24,
		Entry: 32.56, Stop: 30.60, Target: 36.20, TimeframeDays: 15,
	}
	fresh := model.TradeIdea{
		Ticker: "MU", Direction: model.DirectionBuy, Confidence: 46,
		Entry: 956.08, Stop: 900, Target: 1060, TimeframeDays: 15,
	}

	var got []string
	for _, f := range gateIdea(&stale, v, model.RiskConfig{}) {
		if strings.Contains(f.Message, "superseded") {
			got = append(got, f.Message)
			if f.Hard {
				t.Error("a stale bar is a hard failure — the Chief can re-price it instead")
			}
		}
	}
	if len(got) != 1 {
		t.Fatalf("a stale idea produced %d freshness findings, want 1", len(got))
	}
	if !strings.Contains(got[0], "re-price") || !strings.Contains(got[0], "drop it") {
		t.Errorf("the finding names no remedy: %s", got[0])
	}

	for _, f := range gateIdea(&fresh, v, model.RiskConfig{}) {
		if strings.Contains(f.Message, "superseded") {
			t.Errorf("a fresh name was charged with staleness: %s", f.Message)
		}
	}
}

// A name priced off a superseded close cannot produce an actionable idea, and
// the pipeline knew that one stage before it spent five specialist reports on
// it. It used only to append a caveat: the Chief still ranked and priced the
// name, and the risk gate raised the problem afterwards as a *soft* finding
// asking it to "re-price against the newest bar in the quant block". On
// 2026-09-05 the block held no newer bar, so the corrective pass's only
// available answer was deletion, and the run shipped two ideas instead of three.
func TestStalePricedNamesLeaveTheShortlistBeforeTheResearchIsPaidFor(t *testing.T) {
	shortlist := []model.Candidate{
		{Ticker: "MU", Index: "nq100"},
		{Ticker: "STLAM.MI", Index: "eu50"},
		{Ticker: "BAYN.DE", Index: "eu50"},
		{Ticker: "SNPS", Index: "sp500"},
	}
	newPack := func() *quant.Pack {
		p := quant.NewPack()
		for _, c := range shortlist {
			p.ByTicker[strings.ToUpper(c.Ticker)] = quant.Metrics{Symbol: c.Ticker}
		}
		p.Stale = []string{"STLAM.MI", "BAYN.DE"}
		return p
	}

	pack := newPack()
	kept, dropped := dropStalePriced(model.ModeIndependent, shortlist, pack)
	if got := tickerNames(kept); !equalStrings(got, []string{"MU", "SNPS"}) {
		t.Errorf("shortlist = %v, want the two priceable names", got)
	}
	if !equalStrings(dropped, []string{"STLAM.MI", "BAYN.DE"}) {
		t.Errorf("dropped = %v, want both stale names named so the log can report them", dropped)
	}
	// The pack travels into the Chief's prompt, so a dropped name must not
	// survive there as a compact line for a ticker no longer on the shortlist.
	for _, gone := range []string{"STLAM.MI", "BAYN.DE"} {
		if _, ok := pack.ByTicker[gone]; ok {
			t.Errorf("%s stayed in the quant pack after being dropped", gone)
		}
	}

	// Single-stock mode never drops: the user named the ticker, there is no
	// alternative to fall back to, and the flag on the price is the honest answer.
	one := []model.Candidate{{Ticker: "STLAM.MI", Index: "eu50"}}
	if kept, dropped := dropStalePriced(model.ModeSingle, one, newPack()); len(dropped) != 0 || len(kept) != 1 {
		t.Errorf("single-stock mode dropped %v — it has nothing to fall back to", dropped)
	}

	// Nor does a drop that would empty the shortlist: a run that ships flagged
	// ideas is worth more than one that ships none.
	all := newPack()
	all.Stale = []string{"MU", "STLAM.MI", "BAYN.DE", "SNPS"}
	if kept, dropped := dropStalePriced(model.ModeIndependent, shortlist, all); len(dropped) != 0 || len(kept) != 4 {
		t.Errorf("an all-stale shortlist was emptied: kept %v, dropped %v", tickerNames(kept), dropped)
	}
}
