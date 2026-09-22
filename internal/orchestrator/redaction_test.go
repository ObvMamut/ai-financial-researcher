package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

// A prompt is assembled from persisted evidence and retrieval diagnostics, so a
// provider that echoed its key put that key in front of the model. The engine
// boundary redacts once, for every role and every engine.
func TestPromptsAndReportsCannotCarryACredential(t *testing.T) {
	const key = "sk-2f9a4c17e08b3d6510"
	redact.Register(key)
	// A CLI that echoes both its prompt and a line on stderr: the two ways a
	// credential got back out of an engine call.
	bin := filepath.Join(t.TempDir(), "echo-agent")
	script := "#!/bin/sh\ncat <<'EOS' >&2\nconfigured with " + key + "\nEOS\nprintf '%s' \"$2\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	prompt := "Retrieval diagnostics: https://example.test/query?apikey=" + key
	r := runAgent(context.Background(), model.CLIGemini, "news", "analysis", prompt,
		5*time.Second, model.RetryPolicy{MaxAttempts: 1}, "", bin, model.APIConfig{})
	if strings.Contains(r.Stdout, key) {
		t.Errorf("the engine was handed the credential: %s", r.Stdout)
	}
	if !strings.Contains(r.Stdout, redact.Placeholder) {
		t.Errorf("prompt redaction dropped the diagnostic: %s", r.Stdout)
	}
	if strings.Contains(r.Err, key) {
		t.Errorf("engine stderr persisted the credential: %s", r.Err)
	}
}

// TestPromptsAndReportsCannotCarryACredential above asserts on r.Stdout, which
// runAgent's INBOUND scrub (runner.go, after callAgent returns) redacts no
// matter what reached the engine — its fake CLI echoes the prompt back via
// stdout, so that assertion passes even if callAgent's own OUTBOUND scrub
// (prompt = redact.String(prompt), immediately before the CLI argv is built)
// never ran. This test observes the value actually placed on the CLI's argv
// directly, by having the fake CLI write the prompt it received to a file
// instead of echoing it back through the channel runAgent itself cleans up
// afterward.
func TestOutboundPromptToEngineCannotCarryACredential(t *testing.T) {
	const key = "sk-9c31f7a2b6d04e185f"
	redact.Register(key)
	dir := t.TempDir()
	bin := filepath.Join(dir, "capture-agent")
	outFile := filepath.Join(dir, "outbound-prompt.txt")
	// $2 is the prompt argument ("-p" is $1). Writing it to a file — rather
	// than to stdout, which runAgent redacts after the fact regardless — is
	// what makes this an assertion on the wire-bound value.
	script := "#!/bin/sh\nprintf '%s' \"$2\" > \"" + outFile + "\"\nprintf 'ok'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	prompt := "Retrieval diagnostics: https://example.test/query?apikey=" + key
	r := runAgent(context.Background(), model.CLIGemini, "news", "analysis", prompt,
		5*time.Second, model.RetryPolicy{MaxAttempts: 1}, "", bin, model.APIConfig{})
	if r.Status != model.StatusDone {
		t.Fatalf("fake CLI call did not complete: %+v", r)
	}
	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("reading captured outbound prompt: %v", err)
	}
	if strings.Contains(string(got), key) {
		t.Fatalf("the credential reached the engine's argv: %s", got)
	}
	if !strings.Contains(string(got), redact.Placeholder) {
		t.Fatalf("outbound redaction dropped the diagnostic instead of replacing it: %s", got)
	}
}
