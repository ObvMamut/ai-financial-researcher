package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/mamut/claude-financial-researcher/internal/model"
)

// job is one unit of work submitted to the pool.
type job struct {
	cli     model.CLI
	role    string
	stage   string
	prompt  string
	timeout time.Duration
	retry   model.RetryPolicy
	result  chan<- model.Report
}

// pool is a bounded goroutine pool that runs agent subprocesses.
type pool struct {
	workers  int
	jobs     chan job
	models   map[model.CLI]string // per-CLI model passed to the subprocess via --model
	binaries map[model.CLI]string // per-CLI executable name; empty falls back to the CLI constant
	// geminiSem caps how many Gemini (agy) subprocesses run at once. Concurrent agy
	// processes contend on the OS keyring during auth, which makes silent auth time out
	// and escalates to an interactive browser login; throttling avoids that.
	geminiSem chan struct{}
	wg        sync.WaitGroup
}

func newPool(workers int, models, binaries map[model.CLI]string, geminiConc int) *pool {
	if workers <= 0 {
		workers = 4
	}
	p := &pool{
		workers:  workers,
		jobs:     make(chan job, workers*2),
		models:   models,
		binaries: binaries,
	}
	if geminiConc > 0 {
		p.geminiSem = make(chan struct{}, geminiConc)
	}
	return p
}

// start spawns worker goroutines; ctx cancels all workers when done.
func (p *pool) start(ctx context.Context) {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for j := range p.jobs {
				select {
				case <-ctx.Done():
					j.result <- model.Report{
						Agent:  j.role,
						Stage:  model.Stage(j.stage),
						Status: model.StatusFailed,
						Err:    "context cancelled",
					}
				default:
					// Throttle Gemini (agy) subprocesses to avoid keyring auth contention.
					if j.cli == model.CLIGemini && p.geminiSem != nil {
						p.geminiSem <- struct{}{}
					}
					r := runAgent(ctx, j.cli, j.role, j.stage, j.prompt, j.timeout, j.retry, p.models[j.cli], p.binaries[j.cli])
					if j.cli == model.CLIGemini && p.geminiSem != nil {
						<-p.geminiSem
					}
					j.result <- r
				}
			}
		}()
	}
}

// stop drains the job channel and waits for all workers to finish.
func (p *pool) stop() {
	close(p.jobs)
	p.wg.Wait()
}

// submit enqueues a job and returns a channel that will receive exactly one Report.
func (p *pool) submit(cli model.CLI, role, stage, prompt string, timeout time.Duration, retry model.RetryPolicy) <-chan model.Report {
	ch := make(chan model.Report, 1)
	p.jobs <- job{
		cli:     cli,
		role:    role,
		stage:   stage,
		prompt:  prompt,
		timeout: timeout,
		retry:   retry,
		result:  ch,
	}
	return ch
}
