package marketdata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func fredServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("CFR_FRED_BASE", srv.URL)
	return srv
}

func TestFredCollectsSeries(t *testing.T) {
	fredServer(t, func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("series_id")
		fmt.Fprintf(w, `{"observations":[{"date":"2026-08-01","value":"4.15","series":%q}]}`, id)
	})

	facts, err := NewFredProvider("key").MacroFetch(context.Background())
	if err != nil {
		t.Fatalf("MacroFetch: %v", err)
	}
	if len(facts) != 4 {
		t.Fatalf("got %d facts, want all 4 series", len(facts))
	}
	if facts[0].AsOf.Format("2006-01-02") != "2026-08-01" {
		t.Errorf("as-of = %v, want the observation date", facts[0].AsOf)
	}
}

// A per-series failure used to `continue` silently: the macro pack rendered
// three indicators as though four had been asked for, and nothing recorded the
// gap. A partial fetch is still useful — but it has to be reported.
func TestFredReportsPerSeriesFailures(t *testing.T) {
	fredServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("series_id") == "UNRATE" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"observations":[{"date":"2026-08-01","value":"4.15"}]}`)
	})

	facts, err := NewFredProvider("key").MacroFetch(context.Background())
	if err == nil {
		t.Fatal("a failed series must be reported, not silently skipped")
	}
	if !strings.Contains(err.Error(), "UNRATE") {
		t.Errorf("err = %v, want the failing series named", err)
	}
	if len(facts) != 3 {
		t.Errorf("got %d facts, want the 3 that succeeded — a partial fetch is still useful", len(facts))
	}
}

// MacroFetch ignored its context entirely, so a cancelled run kept issuing
// requests through all four series.
func TestFredHonoursContextCancellation(t *testing.T) {
	var hits int32
	fredServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, `{"observations":[{"date":"2026-08-01","value":"4.15"}]}`)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := NewFredProvider("key").MacroFetch(ctx); err == nil {
		t.Error("a cancelled context must fail the fetch")
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("issued %d requests on a cancelled context, want 0", got)
	}
}
