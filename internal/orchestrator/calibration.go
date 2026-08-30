package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
)

const (
	// calibrationMaxAge is how stale the stored track record may be before a run
	// recomputes it. The replay costs one price fetch per distinct past ticker,
	// all keyless and cached, so refreshing daily is cheap — while refreshing on
	// every run would put a hundred HTTP requests on the hot path for a number
	// that only moves as ideas age.
	calibrationMaxAge = 24 * time.Hour
	// calibrationRefreshTimeout bounds that work. A slow or rate-limited data
	// source must not hold up a run for a number the run can do without.
	calibrationRefreshTimeout = 90 * time.Second
)

// trackRecord returns the pipeline's realized record, refreshing the stored copy
// when it has gone stale.
//
// It never fails a run. With no record the Chief is simply not told one, which
// is the state every fresh install starts in and the honest state until enough
// ideas have closed to say anything.
func trackRecord(ctx context.Context, ch chan<- Event, cfg Config, yc *marketdata.YahooClient) *scoreboard.Calibration {
	cal := scoreboard.LoadCalibration(cfg.DataDir)
	if cal.Age() < calibrationMaxAge {
		return cal
	}

	rctx, cancel := context.WithTimeout(ctx, calibrationRefreshTimeout)
	defer cancel()
	sum, err := scoreboard.Replay(rctx, cfg.RunsDir, yc, cfg.FillWindowDays)
	if err != nil {
		log(ch, fmt.Sprintf("warn: could not refresh the track record: %v", err))
		return cal
	}
	fresh := scoreboard.Calibrate(sum)
	if err := fresh.Save(cfg.DataDir); err != nil {
		log(ch, fmt.Sprintf("warn: could not store the track record: %v", err))
	}
	return fresh
}
