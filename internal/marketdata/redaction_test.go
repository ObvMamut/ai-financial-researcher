package marketdata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mamut/claude-financial-researcher/internal/redact"
)

// The live 2026-09-07 thesis run persisted an AlphaVantage diagnostic that
// quoted the request back, apikey included, into metadata.json and from there
// into a researcher's retrieval-diagnostics prompt block. The provider answers
// a rejected call with prose, and prose is not a place anyone was looking.
func TestProviderDiagnosticsCannotCarryTheConfiguredKey(t *testing.T) {
	const key = "AV7QK2ZP4M1XN0BC"
	redact.Register(key)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		echo := r.URL.String() // contains apikey=<key>
		w.Write([]byte(`{"Information": "Invalid API call. Please retry: ` + echo + `"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CFR_AV_BASE", srv.URL)

	pack := NewService(nil, NewAlphaVantageProvider(key, t.TempDir())).
		BuildPack(context.Background(), "news", []string{"NVDA"})

	joined := strings.Join(pack.Errors, " | ")
	if joined == "" {
		t.Fatal("a rejected provider call must still be recorded")
	}
	if strings.Contains(joined, key) {
		t.Fatalf("credential persisted in pack errors: %s", joined)
	}
	if !strings.Contains(joined, redact.Placeholder) {
		t.Fatalf("diagnostic lost its redaction marker: %s", joined)
	}
	if !strings.Contains(joined, "Invalid API call") {
		t.Fatalf("redaction destroyed the audit trail: %s", joined)
	}
}
