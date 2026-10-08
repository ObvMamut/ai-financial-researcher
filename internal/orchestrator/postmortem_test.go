package orchestrator

import (
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/scoreboard"
)

// Stored lessons stand in for a fresh draw only when this build's validator
// judged them and they are younger than postMortemMaxAge. A file written before
// the version field existed reads as 0 and is redrawn.
func TestStoredPostMortemIsReusedOnlyUnderTheSameValidator(t *testing.T) {
	at := func(age time.Duration) string { return time.Now().Add(-age).UTC().Format(time.RFC3339) }
	cur := scoreboard.PostMortemValidatorVersion
	for _, c := range []struct {
		name string
		pm   *scoreboard.PostMortem
		want bool
	}{
		{"none stored", nil, false},
		{"fresh, same validator", &scoreboard.PostMortem{ComputedAt: at(time.Hour), ValidatorVersion: cur}, true},
		{"fresh, unrecorded validator", &scoreboard.PostMortem{ComputedAt: at(time.Hour)}, false},
		{"fresh, older validator", &scoreboard.PostMortem{ComputedAt: at(time.Hour), ValidatorVersion: cur - 1}, false},
		{"stale, same validator", &scoreboard.PostMortem{ComputedAt: at(25 * time.Hour), ValidatorVersion: cur}, false},
	} {
		if got := reusablePostMortem(c.pm); got != c.want {
			t.Errorf("%s: reusable = %v, want %v", c.name, got, c.want)
		}
	}
}
