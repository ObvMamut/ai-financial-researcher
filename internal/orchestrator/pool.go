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
	api      model.APIConfig      // config for the CLIApi (OpenAI-compatible) engine
	// throttleCLI + throttleSem cap how many of the cheap-engine's calls run at once.
	// For gemini (agy) this avoids OS-keyring auth contention; for a local model server
	// it avoids overwhelming a single GPU with parallel generations. A nil sem means the
	// cheap engine runs unthrottled (remote APIs like DeepSeek parallelize freely).
	// Synthesis (CLIClaude) is never the cheap engine, so it is never throttled.
	throttleCLI model.CLI
	throttleSem chan struct{}
	wg          sync.WaitGroup
}

func newPool(workers int, models, binaries map[model.CLI]string, api model.APIConfig, throttleCLI model.CLI, throttleConc int) *pool {
	if workers <= 0 {
		workers = 4
	}
	p := &pool{
		workers:     workers,
		jobs:        make(chan job, workers*2),
		models:      models,
		binaries:    binaries,
		api:         api,
		throttleCLI: throttleCLI,
	}
	if throttleConc > 0 {
		p.throttleSem = make(chan struct{}, throttleConc)
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
					// Throttle the cheap engine (agy keyring contention / single-GPU local).
					throttled := j.cli == p.throttleCLI && p.throttleSem != nil
					if throttled {
						p.throttleSem <- struct{}{}
					}
					r := runAgent(ctx, j.cli, j.role, j.stage, j.prompt, j.timeout, j.retry, p.models[j.cli], p.binaries[j.cli], p.api)
					if throttled {
						<-p.throttleSem
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
