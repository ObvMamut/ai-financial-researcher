package model

import (
	"runtime/debug"
	"testing"
)

func TestParseBuildSettingsReadsTheThreeVCSFacts(t *testing.T) {
	rev, dirty, at := parseBuildSettings([]debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: "abc123"},
		{Key: "vcs.modified", Value: "true"},
		{Key: "vcs.time", Value: "2026-09-24T07:28:25Z"},
	})
	if rev != "abc123" || dirty == nil || !*dirty || at != "2026-09-24T07:28:25Z" {
		t.Errorf("got rev=%q dirty=%v time=%q", rev, dirty, at)
	}
}

func TestParseBuildSettingsCleanTree(t *testing.T) {
	_, dirty, _ := parseBuildSettings([]debug.BuildSetting{
		{Key: "vcs.revision", Value: "abc123"},
		{Key: "vcs.modified", Value: "false"},
	})
	if dirty == nil || *dirty {
		t.Errorf("dirty = %v, want a false pointer for a clean tree", dirty)
	}
}

// A go run/go test binary carries no vcs.* settings at all (this module's own
// `go test` binaries confirm it — see the package comment on CurrentBuildInfo);
// an empty settings list must record that honestly, never fake a revision or
// guess a dirty state.
func TestParseBuildSettingsNoVCSInfo(t *testing.T) {
	rev, dirty, at := parseBuildSettings(nil)
	if rev != "" || dirty != nil || at != "" {
		t.Errorf("got rev=%q dirty=%v time=%q, want all empty/nil", rev, dirty, at)
	}
}

// CurrentBuildInfo must never panic, whatever ReadBuildInfo returns for the
// process actually running the test.
func TestCurrentBuildInfoDoesNotPanic(t *testing.T) {
	CurrentBuildInfo()
}
