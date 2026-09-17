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
