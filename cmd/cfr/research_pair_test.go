package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/config"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/orchestrator"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

func pairFixture(t *testing.T) (*config.Settings, model.RunRequest, string) {
	t.Helper()
	binary, err := filepath.Abs("../../testdata/fakebin/claude")
	if err != nil {
		t.Fatal(err)
	}
	s := &config.Settings{CheapEngine: "local", Local: model.APIConfig{Model: "fixture", BaseURL: "http://fixture.invalid"}, AgentsDir: "../../agents", Binaries: map[model.CLI]string{model.CLIClaude: binary}, FillWindowDays: 3}
	return s, model.RunRequest{Mode: model.ModeSingle, Ticker: "AAPL"}, filepath.Join(t.TempDir(), "experiment")
}

func pairFakeCapture(ctx context.Context, cfg marketdata.SnapshotCaptureConfig) (*marketdata.ResearchSnapshot, error) {
	return &marketdata.ResearchSnapshot{Version: 1, AsOf: time.Now().UTC(), Tickers: cfg.Tickers, Prices: map[string]*quant.Series{"AAPL": {Symbol: "AAPL", Bars: []quant.Bar{{Date: "2026-09-04", Close: 100}}}}}, ctx.Err()
}

func pairFakeRun(t *testing.T, cfg orchestrator.Config) <-chan orchestrator.Event {
	t.Helper()
	meta, err := store.LoadMeta(cfg.EvaluationRun.Dir)
	if err != nil {
		t.Fatal(err)
	}
	meta.Outcome = "complete"
	meta.SchemaVersion = 1
	if cfg.ResearchMode == "thesis" {
		meta.SchemaVersion = 2
	}
	if err := cfg.EvaluationRun.WriteMeta(*meta); err != nil {
		t.Fatal(err)
	}
	ideas := model.IdeasResult{GeneratedAt: cfg.Frozen.AsOf.Format(time.RFC3339), ResearchMode: cfg.ResearchMode, SchemaVersion: meta.SchemaVersion, Mode: string(cfg.Mode), Ideas: []model.TradeIdea{}}
	if err := cfg.EvaluationRun.WriteIdeas(&ideas); err != nil {
		t.Fatal(err)
	}
	ch := make(chan orchestrator.Event, 1)
	ch <- orchestrator.Event{Type: orchestrator.EventComplete, Meta: meta}
	close(ch)
	return ch
}

func TestPairRegistersBeforeCollectionAndIsolatesArms(t *testing.T) {
	s, req, dir := pairFixture(t)
	var reg pairRegistration
	var order []string
	capture := func(ctx context.Context, cfg marketdata.SnapshotCaptureConfig) (*marketdata.ResearchSnapshot, error) {
		if err := readPairJSON(filepath.Join(dir, "registration.json"), &reg); err != nil {
			t.Fatal(err)
		}
		if reg.RegisteredAt.After(time.Now()) || len(reg.Files) == 0 {
			t.Fatal("missing prior registration")
		}
		return pairFakeCapture(ctx, cfg)
	}
	run := func(ctx context.Context, cfg orchestrator.Config) <-chan orchestrator.Event {
		order = append(order, cfg.ResearchMode)
		if cfg.DataDir != filepath.Join(cfg.EvaluationRun.Dir, "cache") {
			t.Fatal("shared arm cache")
		}
		if !cfg.Frozen.AsOf.Equal(cfg.EvaluationRun.TS) || cfg.Frozen.AsOf.Before(reg.RegisteredAt) {
			t.Fatal("wrong cutoff")
		}
		if cfg.Frozen.Prices["AAPL"].LastClose() != 100 {
			t.Fatal("input mutation crossed arms")
		}
		ch := pairFakeRun(t, cfg)
		cfg.Frozen.Prices["AAPL"].Bars[0].Close = 999
		return ch
	}
	code, err := collectAndRunPair(context.Background(), s, req, dir, false, nil, capture, run)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if !reflect.DeepEqual(reg.Order, order) {
		t.Fatalf("unregistered order: %v vs %v", reg.Order, order)
	}
	report, _, err := evaluatePair(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Pairs) != 1 || report.Pairs[0].Status != "matched_declared_inputs" {
		t.Fatalf("pair audit: %+v", report.Pairs)
	}
	if _, err := collectAndRunPair(context.Background(), s, req, dir, false, nil, capture, run); err == nil {
		t.Fatal("overwrote existing experiment")
	}
}

func TestPairFailureRetainsOtherArmAndFailureMetadata(t *testing.T) {
	s, req, dir := pairFixture(t)
	calls := 0
	run := func(ctx context.Context, cfg orchestrator.Config) <-chan orchestrator.Event {
		calls++
		if calls > 1 {
			return pairFakeRun(t, cfg)
		}
		ch := make(chan orchestrator.Event, 2)
		ch <- orchestrator.Event{Type: orchestrator.EventStatus, Report: &model.Report{Agent: "fixture", Status: model.StatusFailed, Attempts: 1, Tokens: 7, Usage: []model.TokenUsage{{CompletionTokens: new(7)}}}}
		ch <- orchestrator.Event{Type: orchestrator.EventError, Message: "fixture failure"}
		close(ch)
		return ch
	}
	code, err := collectAndRunPair(context.Background(), s, req, dir, false, nil, pairFakeCapture, run)
	if err != nil || code != 3 || calls != 2 {
		t.Fatalf("code=%d calls=%d err=%v", code, calls, err)
	}
	var reg pairRegistration
	if err := readPairJSON(filepath.Join(dir, "registration.json"), &reg); err != nil {
		t.Fatal(err)
	}
	meta, err := store.LoadMeta(filepath.Join(dir, "runs", reg.Order[0]))
	if err != nil || meta.Outcome != "failed" || len(meta.Domains) != 1 || meta.Domains[0].Tokens != 7 {
		t.Fatalf("failure metadata=%+v err=%v", meta, err)
	}
}

func TestPairCancellationAndTamperingPreventLaunch(t *testing.T) {
	for _, cancel := range []bool{true, false} {
		t.Run(fmt.Sprint(cancel), func(t *testing.T) {
			s, req, dir := pairFixture(t)
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			capture := func(ctx context.Context, cfg marketdata.SnapshotCaptureConfig) (*marketdata.ResearchSnapshot, error) {
				if cancel {
					stop()
				} else if err := os.WriteFile(filepath.Join(dir, "personas", "chief-analyst.md"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
				return pairFakeCapture(ctx, cfg)
			}
			if _, err := collectAndRunPair(ctx, s, req, dir, false, nil, capture, func(context.Context, orchestrator.Config) <-chan orchestrator.Event {
				t.Fatal("launched after cancellation/tampering")
				return nil
			}); err == nil {
				t.Fatal("failure hidden")
			}
			var state pairState
			if err := readPairJSON(filepath.Join(dir, "state.json"), &state); err != nil {
				t.Fatal(err)
			}
			if state.Status != "failed" || state.Arms["legacy"].Status != "not_started" || state.Arms["thesis"].Status != "not_started" {
				t.Fatalf("lost failure: %+v", state)
			}
		})
	}
}

func TestPairOutcomeRefreshPreservesGenerationArtifacts(t *testing.T) {
	s, req, dir := pairFixture(t)
	if _, err := collectAndRunPair(context.Background(), s, req, dir, false, nil, pairFakeCapture, func(ctx context.Context, cfg orchestrator.Config) <-chan orchestrator.Event {
		return pairFakeRun(t, cfg)
	}); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	err := filepath.WalkDir(filepath.Join(dir, "runs"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		before[path] = pairHash(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	prices := &marketdata.ResearchSnapshot{Prices: map[string]*quant.Series{"AAPL": {Symbol: "AAPL", Bars: []quant.Bar{{Date: "2026-09-08", Close: 101}}}}}
	if _, _, err := evaluatePair(context.Background(), dir, prices); err != nil {
		t.Fatal(err)
	}
	report, _, err := evaluatePair(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for path, hash := range before {
		b, err := os.ReadFile(path)
		if err != nil || pairHash(b) != hash {
			t.Fatalf("generation artifact changed: %s", path)
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "outcomes", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("outcomes=%v err=%v", files, err)
	}
	var saved pairOutcomes
	if err := readPairJSON(files[0], &saved); err != nil {
		t.Fatal(err)
	}
	if report.AsOf != saved.FetchedAt.Format(time.RFC3339) {
		t.Fatal("offline evaluation advanced the outcome snapshot clock")
	}

	if err := os.WriteFile(filepath.Join(dir, "runs", "legacy", "data/evaluation-snapshot.json"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := evaluatePair(context.Background(), dir, nil); err == nil {
		t.Fatal("accepted changed generation corpus")
	}
}
