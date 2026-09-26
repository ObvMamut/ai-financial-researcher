package model

import "runtime/debug"

// CurrentBuildInfo reads this process's own module build info and returns the
// three VCS facts RunMeta records: the commit the running binary was built
// from, whether the working tree had uncommitted changes at build time, and
// when it was built. `go build` (and `go install`) embed these from a git
// checkout automatically; `go run` and `go test` binaries generally do not —
// ReadBuildInfo still succeeds, but the vcs.* settings are simply absent.
// That case returns zero values ("", nil, "") rather than a substituted
// default: an empty revision must never be mistaken for a real one.
func CurrentBuildInfo() (revision string, dirty *bool, buildTime string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", nil, ""
	}
	return parseBuildSettings(info.Settings)
}

// parseBuildSettings pulls the three VCS facts out of a debug.BuildInfo
// settings list. Split out from CurrentBuildInfo so the mapping itself is
// testable against a synthetic settings list — a real go-build binary's
// vcs.* values are only available from within a test that shells out to `go
// build` (see cmd/cfr's TestBuildRevisionIsRecordedWhenBuiltWithGoBuild).
func parseBuildSettings(settings []debug.BuildSetting) (revision string, dirty *bool, buildTime string) {
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			switch s.Value {
			case "true":
				v := true
				dirty = &v
			case "false":
				v := false
				dirty = &v
			}
		case "vcs.time":
			buildTime = s.Value
		}
	}
	return revision, dirty, buildTime
}
