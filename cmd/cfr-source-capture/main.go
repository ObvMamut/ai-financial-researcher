// Command cfr-source-capture collects the fixed regional issuer panel once.
// It reads no user configuration or credentials and never initializes models.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/marketdata"
	"github.com/mamut/claude-financial-researcher/internal/model"
)

type capture struct {
	StartedAt   time.Time                           `json:"started_at"`
	FinishedAt  time.Time                           `json:"finished_at"`
	Documents   map[string][]model.EvidenceDocument `json:"documents"`
	TextHashes  map[string]string                   `json:"text_sha256"`
	Coverage    []marketdata.ResearchCoverage       `json:"coverage"`
	Attempts    map[string]int                      `json:"attempts"`
	MaxAttempts int                                 `json:"max_document_attempts_per_company"`
}

func main() {
	out := flag.String("out", "", "new output JSON file (required; refuses overwrite)")
	flag.Parse()
	if *out == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: cfr-source-capture --out new-file.json")
		os.Exit(2)
	}
	if err := collect(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func collect(path string) error {
	// Reserve the destination before requests; an existing capture never reruns.
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	sources, err := marketdata.LoadResearchSources("")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	c := capture{StartedAt: time.Now().UTC(), Documents: map[string][]model.EvidenceDocument{}, TextHashes: map[string]string{}, Attempts: map[string]int{}, MaxAttempts: 8}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ticker := range []string{"ASML.AS", "STLAM.MI", "NOKIA.HE", "2330.TW", "9988.HK", "BHP.AX"} {
		wg.Add(1)
		go func(ticker string) {
			defer wg.Done()
			reader := marketdata.DocumentReader{}
			queue := append([]string(nil), sources[ticker]...)
			if len(queue) == 0 {
				fmt.Fprintf(os.Stderr, "cfr-source-capture: no configured source for %s; skipping\n", ticker)
				mu.Lock()
				c.Documents[ticker] = nil
				c.Attempts[ticker] = 0
				mu.Unlock()
				return
			}
			seed := queue[0]
			seen := map[string]bool{}
			docs := []model.EvidenceDocument{}
			for len(queue) > 0 && len(docs) < 8 && ctx.Err() == nil {
				u := queue[0]
				queue = queue[1:]
				key := marketdata.CanonicalResearchURL(u)
				if seen[key] {
					continue
				}
				seen[key] = true
				doc := reader.Read(ctx, ticker, u)
				doc.Authority = "issuer"
				docs = append(docs, doc)
				// Only issuer-host links, ranked by the production discovery policy.
				for _, link := range doc.Links {
					if marketdata.NormalizeDomain(link) == marketdata.NormalizeDomain(seed) {
						queue = append(queue, link)
					}
				}
				queue = marketdata.RankResearchLinks(queue, seed)
			}
			mu.Lock()
			c.Documents[ticker] = docs
			c.Attempts[ticker] = len(docs)
			for _, d := range docs {
				c.TextHashes[d.ID] = fmt.Sprintf("%x", sha256.Sum256([]byte(d.Text)))
			}
			mu.Unlock()
		}(ticker)
	}
	wg.Wait()
	c.FinishedAt = time.Now().UTC()
	c.Coverage = marketdata.MeasureResearchCoverage(c.Documents)
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		return err
	}
	return f.Sync()
}
