package backtest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	// Normalise what is supposed to differ, then require byte equality.
	pit.Universe, pit.Survivorship, pit.FilingsNote = sample.Universe, sample.Survivorship, sample.FilingsNote
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
