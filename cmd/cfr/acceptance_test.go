package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/config"
	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

func TestAcceptanceManifestUsesResolvedSettingsWithoutDispatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "custom.md"), []byte("custom persona"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &config.Settings{AgentsDir: dir, ChiefEngine: "api", ChiefAPI: model.APIConfig{BaseURL: "https://chief.example.test", Model: "fixture-chief", APIKey: "k7z4p1q9-fixture-key-one"}, CheapEngine: model.CLILocal, Local: model.APIConfig{BaseURL: "http://local.invalid/v1", Model: "fixture-local"}, Binaries: map[model.CLI]string{model.CLIClaude: "/must-not-run"}}
	redact.Register(s.ChiefAPI.APIKey)
	read := func() acceptanceManifest {
		var b bytes.Buffer
		if err := writeAcceptanceManifest(s, []string{"--indices", "eu50,asia100"}, &b, io.Discard); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(), s.ChiefAPI.APIKey) {
			t.Fatal("credential leaked")
		}
		var m acceptanceManifest
		if err := json.Unmarshal(b.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := read()
	if len(m.PersonaHashes) != 1 || m.PersonaHashes["custom"] == "" {
		t.Fatalf("custom directory ignored: %v", m.PersonaHashes)
	}
	caps := m.Configuration["provider_caps"].(map[string]any)
	chief := caps["chief_api"].(map[string]any)
	if chief["model"] != "fixture-chief" || chief["base_url"] != "https://chief.example.test" || chief["max_tokens"] != float64(8192) {
		t.Fatalf("wrong chief configuration: %v", chief)
	}
	if caps["chief_fallback"].(map[string]any)["max_tokens"] != float64(32768) || m.Configuration["chief_fallback_active"] != false {
		t.Fatal("wrong fallback gate/cap")
	}
	if m.CallCeiling != 12 {
		t.Fatalf("call ceiling %d", m.CallCeiling)
	}
	s.ChiefAPI.APIKey = "k7z4p1q9-fixture-key-two"
	if next := read(); next.ConfigHash != m.ConfigHash {
		t.Fatal("credential changed public fingerprint")
	}
	s.Local.MaxTokens = 1234
	if next := read(); next.ConfigHash == m.ConfigHash {
		t.Fatal("effective token cap absent from fingerprint")
	}
}

func TestManifestGateFindsEscapedCredentialsAndPrefixes(t *testing.T) {
	secret := "j9x3q5v7-escaped-\"credential\\value"
	redact.Register(secret)
	for _, value := range []string{secret, secret[:8]} {
		b, _ := json.Marshal(map[string]any{"value": value})
		var parsed any
		if err := json.Unmarshal(b, &parsed); err != nil {
			t.Fatal(err)
		}
		if !manifestContainsSecret(parsed) {
			t.Fatal("missed credential after JSON decoding")
		}
	}
}
