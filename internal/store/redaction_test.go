package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/model"
	"github.com/mamut/claude-financial-researcher/internal/redact"
)

// Every artifact writer redacts. A provider diagnostic that quotes its own
// request reaches all three sinks — the agent report it lands in, the metadata
// error list, and the persisted evidence pack.
func TestRunArtifactsNeverPersistAConfiguredCredential(t *testing.T) {
	const key = "FRED9d41c8a2b7e6f350"
	redact.Register(key)
	diagnostic := "provider rejected the call: https://example.test/query?api_key=" + key

	run, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.WriteReport("news", "# News\n"+diagnostic+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteMeta(model.RunMeta{DataErrors: []string{diagnostic}}); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteDataPack("research", map[string]any{"errors": []string{diagnostic}}); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteIdeas(&model.IdeasResult{Notes: diagnostic}); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"news.md", "metadata.json", "ideas.json", filepath.Join("data", "research.json")} {
		b, err := os.ReadFile(filepath.Join(run.Dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		body := string(b)
		if strings.Contains(body, key) {
			t.Errorf("%s persisted the credential", name)
		}
		if !strings.Contains(body, redact.Placeholder) || !strings.Contains(body, "provider rejected the call") {
			t.Errorf("%s lost the diagnostic instead of redacting it: %s", name, body)
		}
	}
}
