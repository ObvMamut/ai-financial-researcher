package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/quant"
	"github.com/mamut/claude-financial-researcher/internal/store"
)

type frozenTransport func(*http.Request) (*http.Response, error)

func (f frozenTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func frozenFixture(t *testing.T) Config {
	t.Helper()
	at := time.Date(2026, 9, 4, 22, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	snapshot := &marketdata.ResearchSnapshot{Version: 1, AsOf: at, Tickers: []string{"AAPL"}, Packs: map[string]*marketdata.DataPack{}, Prices: map[string]*quant.Series{}}
	for _, symbol := range []string{"AAPL", "^GSPC"} {
		snapshot.Prices[symbol] = &quant.Series{Symbol: symbol, Bars: []quant.Bar{{Date: "2026-09-04", Open: 100, High: 102, Low: 99, Close: 101, Volume: 1000000}}}
	}
	for _, domain := range []string{"news", "fundamentals", "sentiment", "macro"} {
		pack := marketdata.NewDataPack(domain)
		pack.ByTicker["AAPL"] = marketdata.TickerData{Ticker: "AAPL"}
		snapshot.Packs[domain] = pack
	}
	snapshot.Discovery = snapshot.Packs["news"]
	cfg := Config{Mode: model.ModeSingle, Ticker: "AAPL", ResearchMode: "legacy", AgentsDir: "../../agents", RunsDir: t.TempDir(), DataDir: filepath.Join(dir, "cache"),
		Frozen: snapshot, EvaluationRun: &store.Run{Dir: dir, TS: at}, CheapEngine: model.CLIApi,
		API: model.APIConfig{BaseURL: "http://frozen-model.invalid", Model: "fixture", APIKey: "fixture"}}
	cfg.applyDefaults()
	return cfg
}

func TestFrozenRequiresClosedExecutionConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"missing snapshot", func(c *Config) { c.Frozen = nil }},
		{"missing reservation", func(c *Config) { c.EvaluationRun = nil }},
		{"live tools", func(c *Config) { c.CheapEngine = model.CLIGemini }},
		{"different cutoff", func(c *Config) { c.EvaluationRun.TS = c.EvaluationRun.TS.Add(time.Second) }},
		{"shared cache", func(c *Config) { c.DataDir = t.TempDir() }},
		{"undeclared company", func(c *Config) { c.Ticker = "MSFT" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := frozenFixture(t)
			tc.change(&cfg)
			if err := validateFrozenRun(cfg); err == nil {
				t.Fatal("invalid frozen configuration accepted")
			}
		})
	}
}

func TestFrozenBothPipelinesUseOnlySnapshotAndCommonClock(t *testing.T) {
	var modelCalls, providerCalls atomic.Int32
	previous := http.DefaultTransport
	http.DefaultTransport = frozenTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "frozen-model.invalid" {
			providerCalls.Add(1)
			return nil, fmt.Errorf("unexpected live provider call to %s", r.URL.Host)
		}
		modelCalls.Add(1)
		payload := "```json\n" + `{"domain":"macro","scores":[],"missing":["AAPL"],"ticker":"AAPL","status":"watchlist","evidence_quality":"insufficient","requests":[]}` + "\n```"
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": payload}, "finish_reason": "stop"}}})
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	for _, mode := range []string{"legacy", "thesis"} {
		t.Run(mode, func(t *testing.T) {
			cfg := frozenFixture(t)
			cfg.ResearchMode = mode
			chief := filepath.Join(t.TempDir(), "chief")
			if err := os.WriteFile(chief, []byte("#!/bin/sh\ncat <<'RESULT'\n```json\n{\"generated_at\":\"1999-01-01T00:00:00Z\",\"ideas\":[],\"decisions\":[]}\n```\nRESULT\n"), 0700); err != nil {
				t.Fatal(err)
			}
			cfg.Binaries = map[model.CLI]string{model.CLIClaude: chief}
			ch := make(chan Event, 1000)
			if err := run(context.Background(), cfg, ch); err != nil {
				t.Fatal(err)
			}
			ideas, err := store.LoadIdeas(cfg.EvaluationRun.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if ideas.GeneratedAt != cfg.Frozen.AsOf.Format(time.RFC3339) {
				t.Fatalf("model changed generation anchor: %s", ideas.GeneratedAt)
			}
			var pack quant.Pack
			_, err = store.ReadQuantPack(cfg.EvaluationRun.Dir, &pack)
			if err != nil {
				t.Fatal(err)
			}
			if len(pack.Stale) != 0 {
				t.Fatalf("freshness used wall clock instead of frozen cutoff: %v", pack.Stale)
			}
			for _, artifact := range []string{"calibration.json", "post-mortem.json"} {
				if _, err := os.Stat(filepath.Join(cfg.DataDir, artifact)); !os.IsNotExist(err) {
					t.Fatalf("evaluation accessed feedback artifact %s: %v", artifact, err)
				}
			}
		})
	}
	if modelCalls.Load() == 0 || providerCalls.Load() != 0 {
		t.Fatalf("model calls=%d, live provider calls=%d", modelCalls.Load(), providerCalls.Load())
	}
}
