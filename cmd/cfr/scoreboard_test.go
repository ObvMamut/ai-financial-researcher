package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/config"
	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
)

func TestResearchComparisonOfflineCLI(t *testing.T) {
	settings := &config.Settings{RunsDir: t.TempDir(), DataDir: t.TempDir()}
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	saved := os.Stdout
	os.Stdout = out
	defer func() { os.Stdout = saved }()
	if code := runScoreboard(settings, []string{"--research-compare", "--offline", "--json", "--research-cost-bps", "0"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, err := out.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var report scoreboard.ResearchComparison
	if err := json.NewDecoder(out).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if report.Cohorts == nil || report.Runs == nil {
		t.Fatal("empty comparison must keep explicit empty arrays")
	}
	if _, err := os.Stat(filepath.Join(settings.DataDir, "calibration.json")); !os.IsNotExist(err) {
		t.Fatal("comparison changed calibration")
	}
}

func TestResearchComparisonCLIRejectsInvalidOptions(t *testing.T) {
	settings := &config.Settings{RunsDir: t.TempDir(), DataDir: t.TempDir()}
	for _, args := range [][]string{{"--offline"}, {"--research-pairs", "pairs.json"}, {"--research-compare", "--control"}, {"--research-compare", "--research-cost-bps", "NaN"}, {"--research-compare", "--research-cost-bps", "-1"}, {"--research-compare", "unexpected"}} {
		if code := runScoreboard(settings, args); code != 2 {
			t.Fatalf("%v: exit=%d", args, code)
		}
	}
}
