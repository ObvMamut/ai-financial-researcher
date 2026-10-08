package backtest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/universe"
)

// fakeAsOf serves fakeLoader's series for every symbol, whatever the asof.
type fakeAsOf struct{ asofs []time.Time }

func (f *fakeAsOf) HistoryAsOf(ctx context.Context, symbols []string, _, _, asof time.Time) (map[string]*quant.Series, error) {
	f.asofs = append(f.asofs, asof)
	out := map[string]*quant.Series{}
	for _, s := range symbols {
		out[s], _ = fakeLoader{}.HistoryRange(ctx, s, "", 0)
	}
	return out, nil
}

// The point-in-time mode must change only the universe, never the arithmetic:
// given a history in which today's sample was a member throughout, and the
// same prices, it reproduces the sample replay exactly.
func TestPointInTimeWithTheSampleAsItsHistoryReproducesTheSampleReplay(t *testing.T) {
	uni, err := universe.Load()
	if err != nil {
		t.Fatal(err)
	}
	h := &universe.History{Index: "nq100", Baseline: day("2015-01-01")}
	for _, c := range uni.Constituents("nq100") {
		h.Intervals = append(h.Intervals, universe.Interval{Ticker: strings.ToUpper(c.Ticker), From: day("2015-01-01")})
	}
	base := Config{Years: 1, Indices: []string{"nq100"}, Now: day("2022-09-30")}
	sample, err := Run(context.Background(), fakeLoader{}, uni, base)
	if err != nil {
		t.Fatal(err)
	}
	pitCfg := base
	pitCfg.Universe, pitCfg.AsOf = UniversePIT, &fakeAsOf{}
	pitCfg.Histories, pitCfg.Sectors = map[string]*universe.History{"nq100": h}, map[string]string{}
	pit, err := Run(context.Background(), fakeLoader{}, uni, pitCfg)
	if err != nil {
		t.Fatal(err)
	}
	if pit.Universe != UniversePIT || pit.Survivorship != PITSurvivorship {
		t.Errorf("point-in-time result is not labelled: universe %q", pit.Universe)
	}
	// The N tests' gating differs by design (run with a register family on
	// PIT US, comparison on the sample), but their statistics must not.
	for i, s := range sample.Anomalies.Tests {
		p := pit.Anomalies.Tests[i]
		if s.ID != p.ID || s.Mean != p.Mean || s.T != p.T || s.NDates != p.NDates ||
			s.Halves["H1"] != p.Halves["H1"] || s.Halves["H2"] != p.Halves["H2"] {
			t.Errorf("%s: sample mean %v t %v n %d halves %v, pit mean %v t %v n %d halves %v",
				s.ID, s.Mean, s.T, s.NDates, s.Halves, p.Mean, p.T, p.NDates, p.Halves)
		}
	}
	if pit.Anomalies.RegisterFamily.FamilySize != 21 {
		t.Errorf("point-in-time nq100 run: register family %d, want 21", pit.Anomalies.RegisterFamily.FamilySize)
	}
	// Normalise what is supposed to differ, then require byte equality.
	pit.Universe, pit.Survivorship, pit.FilingsNote = sample.Universe, sample.Survivorship, sample.FilingsNote
	pit.Anomalies = sample.Anomalies
	a, _ := json.Marshal(sample)
	b, _ := json.Marshal(pit)
	if string(a) != string(b) {
		t.Fatalf("the point-in-time replay of an unchanged universe differs from the sample replay\nsample: %.400s\npit:    %.400s", a, b)
	}
}

// A member is scored only inside its membership interval, and a departed
// interval is priced as of its last day in the index.
func TestPointInTimeScoresAMemberOnlyWhileItIsIn(t *testing.T) {
	uni, err := universe.Load()
	if err != nil {
		t.Fatal(err)
	}
	h := &universe.History{Index: "nq100", Baseline: day("2015-01-01")}
	for _, c := range uni.Constituents("nq100") {
		h.Intervals = append(h.Intervals, universe.Interval{Ticker: strings.ToUpper(c.Ticker), From: day("2015-01-01")})
	}
	left := day("2022-03-04")
	h.Intervals = append(h.Intervals, universe.Interval{Ticker: "GONE", From: day("2015-01-01"), To: left})

	loader := &fakeAsOf{}
	cfg := Config{Years: 1, Indices: []string{"nq100"}, Now: day("2022-09-30"),
		Universe: UniversePIT, AsOf: loader, Histories: map[string]*universe.History{"nq100": h}, Sectors: map[string]string{}}
	members := pitMembers("nq100", h, uni.Constituents("nq100"), cfg.Sectors, day("2021-10-01"), day("2022-09-30"))
	var gone Member
	for _, m := range members {
		if m.Constituent.Ticker == "GONE" {
			gone = m
		}
	}
	if gone.Interval == nil || gone.Key != "GONE@2015-01-01" {
		t.Fatalf("departed member = %+v", gone)
	}
	if got := pitAsOf(gone.Interval, cfg.Now); !got.Equal(left.AddDate(0, 0, -1)) {
		t.Errorf("a departed interval is priced as of %s, want its last day in the index", got.Format("2006-01-02"))
	}

	data := Data{Series: map[string]*quant.Series{}, Bench: map[string]*quant.Series{}}
	if _, err := loadPIT(context.Background(), loader, members, data, day("2020-01-01"), cfg.Now); err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if m.Constituent.Ticker == "NDX" {
			continue
		}
		data.Bench[m.Bench], _ = fakeLoader{}.HistoryRange(context.Background(), m.Bench, "", 0)
	}
	recs := BuildPanel(members, data, WeeklyDates(day("2021-10-01"), day("2022-09-30")))
	before := 0
	for _, r := range recs {
		if r.Ticker != "GONE" {
			continue
		}
		if r.Date >= "2022-03-04" {
			t.Fatalf("GONE scored on %s, after it left the index", r.Date)
		}
		before++
	}
	if before == 0 {
		t.Error("GONE was never scored, even while it was a member")
	}
}

// reusedTickerAsOf models what Alpaca did to DOW on 2026-10-08: in the lab's
// 100-symbol batch from 2016-06-29 Dow Inc. came back with no bars, while the
// same batch from its own listing date, 2019-04-02, priced it. STUCK is empty
// from any start.
type reusedTickerAsOf struct{ calls []pitCall }

type pitCall struct {
	symbols     []string
	start, asof time.Time
}

func (f *reusedTickerAsOf) HistoryAsOf(ctx context.Context, symbols []string, start, _, asof time.Time) (map[string]*quant.Series, error) {
	f.calls = append(f.calls, pitCall{symbols: append([]string(nil), symbols...), start: start, asof: asof})
	out := map[string]*quant.Series{}
	for _, s := range symbols {
		if s == "STUCK" || (s == "DOW" && start.Before(day("2019-04-02"))) {
			continue
		}
		out[s], _ = fakeLoader{}.HistoryRange(ctx, s, "", 0)
	}
	return out, nil
}

// A reused ticker whose batch answer is empty is asked once more, alone, from
// its own interval's start and under the same asof, so the mapping is never
// switched off. A member its batch priced is not asked again, and one still
// empty after the retry stays listed as unavailable.
func TestLoadPITRetriesAnEmptyIntervalFromItsOwnStart(t *testing.T) {
	now := day("2026-10-07")
	listed := day("2019-04-02")
	member := func(ticker string, from time.Time) Member {
		return Member{
			Constituent: model.Constituent{Ticker: ticker, Index: "sp500"},
			Interval:    &universe.Interval{Ticker: ticker, From: from},
			Key:         ticker + "@" + from.Format("2006-01-02"),
		}
	}
	members := []Member{member("AAPL", day("2015-01-01")), member("DOW", listed), member("STUCK", listed)}
	loader := &reusedTickerAsOf{}
	data := Data{Series: map[string]*quant.Series{}, Bench: map[string]*quant.Series{}}

	unavailable, err := loadPIT(context.Background(), loader, members, data, day("2016-06-29"), now)
	if err != nil {
		t.Fatal(err)
	}
	if s := data.Series["DOW@2019-04-02"]; s == nil || len(s.Bars) == 0 {
		t.Error("DOW@2019-04-02 was not priced by the retry from its interval start")
	}
	want := []string{"STUCK@2019-04-02: no Alpaca bars as of 2026-10-07"}
	if strings.Join(unavailable, "|") != strings.Join(want, "|") {
		t.Errorf("unavailable = %q, want %q", unavailable, want)
	}
	if len(loader.calls) != 3 {
		t.Fatalf("calls = %d (%+v), want the batch plus one retry each for DOW and STUCK", len(loader.calls), loader.calls)
	}
	for _, c := range loader.calls[1:] {
		if len(c.symbols) != 1 || c.symbols[0] == "AAPL" {
			t.Errorf("retry asked for %v; only an empty member is asked again, alone", c.symbols)
		}
		if !c.start.Equal(listed) || !c.asof.Equal(now) {
			t.Errorf("retry for %v from %s as of %s, want from %s as of %s", c.symbols,
				c.start.Format("2006-01-02"), c.asof.Format("2006-01-02"), listed.Format("2006-01-02"), now.Format("2006-01-02"))
		}
	}
}
