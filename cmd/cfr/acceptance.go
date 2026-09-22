package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/agents"
	"github.com/mamut/claude-financial-researcher/internal/config"
	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/orchestrator"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

type manifestProvider struct {
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
}

type acceptanceManifest struct {
	SourceHashes      map[string]string `json:"source_hashes"`
	Version           int               `json:"version"`
	CapturedAt        time.Time         `json:"captured_at"`
	GitRevision       string            `json:"git_revision"`
	DirtyFiles        []manifestFile    `json:"dirty_files"`
	PersonaHashes     map[string]string `json:"persona_hashes"`
	ConfigHash        string            `json:"redacted_config_hash"`
	Configuration     map[string]any    `json:"configuration"`
	OperatorDeadlines map[string]string `json:"operator_deadlines"`
	CallCeiling       int               `json:"worst_case_call_ceiling_per_company"`
}
type manifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func runAcceptanceManifest(s *config.Settings, args []string) int {
	if err := writeAcceptanceManifest(s, args, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "acceptance-manifest: %v\n", redact.Error(err))
		return 1
	}
	return 0
}

func writeAcceptanceManifest(s *config.Settings, args []string, out, diagnostics io.Writer) error {
	fs := flag.NewFlagSet("acceptance-manifest", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	indices := fs.String("indices", "", "intended index selection, comma separated")
	ticker := fs.String("ticker", "", "intended single-stock case")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	req := model.RunRequest{Mode: model.ModeIndependent}
	if *ticker != "" {
		req.Mode, req.Ticker = model.ModeSingle, strings.ToUpper(*ticker)
	}
	if *indices != "" {
		req.Indices = strings.Split(*indices, ",")
	}
	c, fallback, err := orchestrator.AcceptanceConfig(orchestratorConfig(s, req))
	if err != nil {
		return err
	}
	reg, err := agents.Load(c.AgentsDir)
	if err != nil {
		return err
	}
	provider := func(a model.APIConfig) manifestProvider {
		return manifestProvider{redact.String(a.BaseURL), redact.String(a.Model), a.MaxTokens}
	}
	// Explicit allowlist: never marshal Settings, Config or APIConfig. Hash the
	// same secret-free projection we print, including selectors and capability gates.
	projection := map[string]any{
		"research_mode": c.ResearchMode, "mode": c.Mode, "ticker": c.Ticker,
		"indices": c.Indices, "cheap_engine": c.CheapEngine, "chief_engine": c.ChiefEngine,
		"chief_fallback_active": fallback, "chief_fallback_enabled": c.ChiefFallbackEnabled,
		"provider_caps": map[string]manifestProvider{"api": provider(c.API), "local": provider(c.Local), "chief_api": provider(c.ChiefAPI), "chief_fallback": provider(c.ChiefFallback)},
		"research":      c.Research, "retry": c.Retry, "synthesis_max_attempts": c.SynthesisMaxAttempts,
		"timeouts": c.Timeouts, "models": c.Models, "binaries": c.Binaries,
		"agents_dir": c.AgentsDir, "runs_dir": c.RunsDir, "data_dir": c.DataDir,
		"workers": c.Workers, "gemini_concurrency": c.GeminiConcurrency, "local_concurrency": c.LocalConcurrency,
		"risk": c.Risk, "weights": c.Weights, "chief_adjust_band": c.ChiefAdjustBand,
		"prescreen_top_per_index": c.PrescreenTopPerIndex, "prescreen_pullback_per_index": c.PrescreenPullbackPerIndex,
		"prescreen_base_per_index": c.PrescreenBasePerIndex, "prescreen_drift_per_index": c.PrescreenDriftPerIndex,
		"max_shortlist": c.MaxShortlist, "max_per_index": c.MaxPerIndex, "max_thinly_covered": c.MaxThinlyCovered,
		"shortlist_reserve": c.ShortlistReserve, "shortlist_reserve_min_merit": c.ShortlistReserveMinMerit,
		"price_ttl": c.PriceTTL, "data_cache_days": c.DataCacheDays, "fill_window_days": c.FillWindowDays,
		"market_data_configured": map[string]bool{"alphavantage": c.Providers.AlphaVantageKey != "", "fred": c.Providers.FredKey != "", "alpaca": c.Providers.AlpacaKeyID != "" && c.Providers.AlpacaSecret != ""},
	}
	sources, err := marketdata.LoadResearchSources(c.Research.SourcesFile)
	if err != nil {
		return err
	}
	sourceBytes, err := json.Marshal(sources)
	if err != nil {
		return err
	}
	sourceHashes := map[string]string{"resolved_issuer_sources": fmt.Sprintf("%x", sha256.Sum256(sourceBytes))}
	if c.Research.HolidaysFile != "" {
		b, err := os.ReadFile(c.Research.HolidaysFile)
		if err != nil {
			return err
		}
		sourceHashes["holiday_overrides"] = fmt.Sprintf("%x", sha256.Sum256(b))
	}
	projection["contact_email_configured"] = c.Providers.ContactEmail != ""
	canonical, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	revision, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("read git revision: %w", err)
	}
	status, err := exec.Command("git", "status", "--porcelain=v1", "-z", "--untracked-files=all").Output()
	if err != nil {
		return fmt.Errorf("read git status: %w", err)
	}
	dirty := []manifestFile{}
	entries := bytes.Split(status, []byte{0})
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		path := string(entry[3:])
		hash := "absent"
		// Local configuration and generated corpora are never read into this manifest.
		if path != "cfr.toml" && !strings.HasPrefix(path, "runs/") && !strings.HasPrefix(path, ".data/") {
			b, e := os.ReadFile(filepath.Clean(path))
			if e == nil {
				hash = fmt.Sprintf("%x", sha256.Sum256(b))
			} else if !os.IsNotExist(e) {
				return e
			}
		} else {
			hash = "excluded"
		}
		dirty = append(dirty, manifestFile{path, hash})
		if entry[0] == 'R' || entry[0] == 'C' || entry[1] == 'R' || entry[1] == 'C' {
			i++
		}
	}
	m := acceptanceManifest{SourceHashes: sourceHashes, Version: 2, CapturedAt: time.Now().UTC(), GitRevision: strings.TrimSpace(string(revision)), DirtyFiles: dirty, PersonaHashes: reg.PersonaSHA(), ConfigHash: fmt.Sprintf("%x", sha256.Sum256(canonical)), Configuration: projection, OperatorDeadlines: map[string]string{"single_stock_case": "10m", "full_run": "20m", "source": "docs/plans/2026-09-12-reliability-acceptance.md"}, CallCeiling: 2 * (c.Research.Rounds + 3)}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// Check values after JSON decoding too: escaping must not hide credentials
	// containing quotes, control characters or backslashes from the final gate.
	var value any
	if err := json.Unmarshal(b, &value); err != nil {
		return err
	}
	if manifestContainsSecret(value) {
		return fmt.Errorf("refusing to emit manifest containing configured credentials")
	}
	_, err = out.Write(append(b, '\n'))
	return err
}

func manifestContainsSecret(v any) bool {
	switch x := v.(type) {
	case string:
		return redact.ContainsCredential(x)
	case []any:
		for _, item := range x {
			if manifestContainsSecret(item) {
				return true
			}
		}
	case map[string]any:
		for k, item := range x {
			if redact.ContainsCredential(k) || manifestContainsSecret(item) {
				return true
			}
		}
	}
	return false
}
