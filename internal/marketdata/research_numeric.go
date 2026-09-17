package marketdata

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// ResearchCurrency refuses unknown listings instead of inheriting the legacy
// exchange fallback. Unit is separate from the ISO currency (GBP versus pence).
func ResearchCurrency(ticker string) (currency, unit string, err error) {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	unit = "major"
	if MarketFor(ticker) == MarketUS {
		return "USD", unit, nil
	}
	suffix := ticker[strings.LastIndex(ticker, ".")+1:]
	ex, ok := exchanges[suffix]
	if !ok {
		return "", "", fmt.Errorf("unknown listing currency for %s", ticker)
	}
	if ex.pence {
		unit = "minor"
	}
	return ex.currency, unit, nil
}

// ResearchRate uses only completed bars no later than the valuation anchor.
// The underlying PriceSource can be a frozen snapshot; there is no live fallback.
func (f *FXRates) ResearchRate(ctx context.Context, currency string, anchor time.Time) (model.ResearchFX, error) {
	out := model.ResearchFX{Currency: currency}
	if currency == "USD" {
		out.USDPerUnit = 1
		out.Date = anchor.UTC().Format("2006-01-02")
		out.Source = "identity"
		return out, nil
	}
	if f == nil || f.yc == nil {
		return out, fmt.Errorf("dated FX unavailable for %s", currency)
	}
	key := currency + "@" + anchor.UTC().Format(time.RFC3339Nano)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.research == nil {
		f.research = map[string]researchFXResult{}
	}
	if result, ok := f.research[key]; ok {
		return result.Rate, result.Err
	}
	result, err := f.fetchResearchRate(ctx, currency, anchor)
	f.research[key] = researchFXResult{result, err}
	return result, err
}

type researchFXResult struct {
	Rate model.ResearchFX
	Err  error
}

func (f *FXRates) fetchResearchRate(ctx context.Context, currency string, anchor time.Time) (model.ResearchFX, error) {
	out := model.ResearchFX{Currency: currency}
	symbol := currency + "USD=X"
	series, err := f.yc.History(ctx, symbol)
	if err != nil {
		return out, err
	}
	series = CompletedDailySeries(series, symbol, anchor)
	if series == nil || len(series.Bars) == 0 {
		return out, fmt.Errorf("no completed FX observations for %s", currency)
	}
	bar := series.Bars[len(series.Bars)-1]
	date, err := time.Parse("2006-01-02", bar.Date)
	if err != nil || anchor.Sub(date) > 5*24*time.Hour || bar.Close <= 0 || math.IsNaN(bar.Close) || math.IsInf(bar.Close, 0) {
		return out, fmt.Errorf("missing or stale FX for %s", currency)
	}
	out.Date = bar.Date
	out.USDPerUnit = bar.Close
	out.Source = symbol
	return out, nil
}
