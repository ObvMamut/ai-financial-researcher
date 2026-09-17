package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

func TestClaudeWholeTreeUsageDoesNotDoubleCountMainLoop(t *testing.T) {
	out, usage, err := decodeClaudeOutput(`{"type":"result","subtype":"success","result":"research","usage":{"input_tokens":999,"output_tokens":999},"modelUsage":{"chief":{"inputTokens":10,"outputTokens":5,"cacheReadInputTokens":20,"cacheCreationInputTokens":30},"helper":{"inputTokens":2,"outputTokens":3,"cacheReadInputTokens":4,"cacheCreationInputTokens":6}}}`)
	if err != nil || out != "research" || usage.Incomplete {
		t.Fatalf("decode: %q %+v %v", out, usage, err)
	}
	for name, pair := range map[string][2]*int{
		"prompt":     {usage.PromptTokens, intPointer(72)},
		"completion": {usage.CompletionTokens, intPointer(8)},
		"total":      {usage.TotalTokens, intPointer(80)},
		"cache hit":  {usage.CacheHitTokens, intPointer(24)},
		"cache miss": {usage.CacheMissTokens, intPointer(48)},
	} {
		if pair[0] == nil || *pair[0] != *pair[1] {
			t.Errorf("%s count = %v, want %d", name, pair[0], *pair[1])
		}
	}
}

func intPointer(n int) *int { return &n }

func TestClaudeUsageUnknownZeroAndMainLoop(t *testing.T) {
	for _, tc := range []struct {
		name, raw         string
		known, incomplete bool
	}{
		{"plain wrapper", "research", false, false},
		{"raw research JSON", `{"ideas":[]}`, false, false},
		{"no telemetry", `{"type":"result","subtype":"success","result":"ok"}`, false, true},
		{"main loop", `{"type":"result","subtype":"success","result":"ok","usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`, true, true},
		{"whole tree zero", `{"type":"result","subtype":"success","result":"ok","modelUsage":{"chief":{"inputTokens":0,"outputTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}}}`, true, false},
		{"missing cache", `{"type":"result","subtype":"success","result":"ok","modelUsage":{"chief":{"inputTokens":0,"outputTokens":0}}}`, false, false},
		{"negative count", `{"type":"result","subtype":"success","result":"ok","modelUsage":{"chief":{"inputTokens":-1,"outputTokens":0,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}}}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, usage, err := decodeClaudeOutput(tc.raw)
			if err != nil || (usage.TotalTokens != nil) != tc.known || usage.Incomplete != tc.incomplete {
				t.Fatalf("usage = %+v, error = %v", usage, err)
			}
		})
	}
}

func TestClaudeErrorsRetainReportedUsage(t *testing.T) {
	_, usage, err := decodeClaudeOutput(`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["fixture failed"],"modelUsage":{"chief":{"inputTokens":10,"outputTokens":5,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}}}`)
	if err == nil || !strings.Contains(err.Error(), "fixture failed") || !usage.Incomplete || usage.TotalTokens == nil || *usage.TotalTokens != 15 {
		t.Fatalf("error accounting lost: %+v %v", usage, err)
	}
	if _, _, err := decodeClaudeOutput(`{"type":"result","subtype":"success","result":`); err == nil {
		t.Fatal("malformed envelope accepted")
	}
}

func TestClaudeRetryPreservesEveryAttempt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CFR_USAGE_STATE", filepath.Join(dir, "state"))
	bin := filepath.Join(dir, "claude")
	script := `#!/bin/sh
if [ ! -e "$CFR_USAGE_STATE" ]; then
  touch "$CFR_USAGE_STATE"
  printf '%s' '{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["retry"],"modelUsage":{"chief":{"inputTokens":10,"outputTokens":5,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}}}'
  exit 1
fi
printf '%s' '{"type":"result","subtype":"success","result":"recovered","modelUsage":{"chief":{"inputTokens":20,"outputTokens":7,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}}}'
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	r := runAgent(context.Background(), model.CLIClaude, "chief", "synthesis", "prompt", time.Second, model.RetryPolicy{MaxAttempts: 2}, "", bin, model.APIConfig{})
	if r.Status != model.StatusDone || r.Stdout != "recovered" || r.Tokens != 12 || len(r.Usage) != 2 || !r.Usage[0].Incomplete || r.Usage[1].Incomplete {
		t.Fatalf("retry accounting: %+v", r)
	}
}

func TestClaudeFrozenRestrictionsSurvivePromptPiping(t *testing.T) {
	for _, size := range []int{20, 100000} {
		for _, frozen := range []bool{false, true} {
			ctx := context.Background()
			if frozen {
				ctx = withFrozenModelTools(ctx)
			}
			dir := t.TempDir()
			argsPath, stdinPath := filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
			t.Setenv("CFR_USAGE_ARGS", argsPath)
			t.Setenv("CFR_USAGE_STDIN", stdinPath)
			bin := filepath.Join(dir, "claude")
			script := "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$CFR_USAGE_ARGS\"\ncat > \"$CFR_USAGE_STDIN\"\nprintf '%s' 'research'\n"
			if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			prompt := strings.Repeat("x", size)
			r := runAgent(ctx, model.CLIClaude, "chief", "synthesis", prompt, time.Second, model.RetryPolicy{MaxAttempts: 1}, "fixture", bin, model.APIConfig{})
			if r.Status != model.StatusDone || r.Stdout != "research" {
				t.Fatalf("run failed: %+v", r)
			}
			b, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
			if !slices.Contains(args, "--output-format") || !slices.Contains(args, "json") || slices.Contains(args, "--tools") != frozen || slices.Contains(args, "--bare") {
				t.Fatalf("size %d frozen %v: args = %v", size, frozen, args)
			}
			if frozen {
				for _, flag := range []string{"--safe-mode", "--disable-slash-commands", "--strict-mcp-config"} {
					if !slices.Contains(args, flag) {
						t.Errorf("missing %s", flag)
					}
				}
				for flag, value := range map[string]string{"--tools": "", "--setting-sources": "", "--mcp-config": `{"mcpServers":{}}`, "--disallowedTools": "mcp__*"} {
					i := slices.Index(args, flag)
					if i < 0 || i+1 >= len(args) || args[i+1] != value {
						t.Errorf("flag %s value missing", flag)
					}
				}
			}
			stdin, err := os.ReadFile(stdinPath)
			if err != nil {
				t.Fatal(err)
			}
			if size > 90000 {
				if string(stdin) != prompt || slices.Contains(args, prompt) {
					t.Fatal("large prompt was not piped")
				}
			} else if !slices.Contains(args, prompt) || len(stdin) != 0 {
				t.Fatal("short prompt argument lost")
			}
		}
	}
}
