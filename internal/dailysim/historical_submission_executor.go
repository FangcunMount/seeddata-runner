package dailysim

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
)

const historicalSubmissionQueueCapacity = 48

type historicalSubmissionExecutorKey struct{}

// HistoricalSubmissionJob is an immutable description of one bounded unit of
// historical work. Runtime state is kept in the closure owned by the executor.
type HistoricalSubmissionJob struct {
	ScenarioID string
	TaskID     string
	TargetKey  string
	PlannedAt  time.Time
}

type historicalSubmissionJobResult struct {
	Outcome         dailySimulationOutcome
	ReportGenerated bool
}

type historicalSubmissionJob struct {
	descriptor HistoricalSubmissionJob
	run        func(context.Context) (historicalSubmissionJobResult, error)
	result     chan historicalSubmissionExecution
}

type historicalSubmissionExecution struct {
	result historicalSubmissionJobResult
	err    error
}

type historicalSubmissionFuture struct {
	result <-chan historicalSubmissionExecution
}

func (f historicalSubmissionFuture) Wait() (historicalSubmissionJobResult, error) {
	execution, ok := <-f.result
	if !ok {
		return historicalSubmissionJobResult{}, fmt.Errorf("historical submission future closed without result")
	}
	return execution.result, execution.err
}

type historicalSubmissionExecutor struct {
	ctx             context.Context
	cancel          context.CancelFunc
	logger          log.Logger
	day             string
	parentExpected  int64
	jobs            chan historicalSubmissionJob
	workers         sync.WaitGroup
	reporter        sync.WaitGroup
	reporterStop    chan struct{}
	reporterOnce    sync.Once
	startedAt       time.Time
	discovered      atomic.Int64
	completed       atomic.Int64
	reports         atomic.Int64
	inFlight        atomic.Int64
	failed          atomic.Int64
	parentCompleted atomic.Int64
}

func newHistoricalSubmissionExecutor(
	ctx context.Context,
	logger log.Logger,
	day string,
	parentExpected int,
	workers int,
	progressInterval time.Duration,
) *historicalSubmissionExecutor {
	if workers <= 0 {
		workers = 1
	}
	if progressInterval <= 0 {
		progressInterval = 15 * time.Second
	}
	executorCtx, cancel := context.WithCancel(ctx)
	executor := &historicalSubmissionExecutor{
		ctx: executorCtx, cancel: cancel, logger: logger, day: day,
		parentExpected: int64(parentExpected), jobs: make(chan historicalSubmissionJob, historicalSubmissionQueueCapacity),
		reporterStop: make(chan struct{}), startedAt: time.Now(),
	}
	for worker := 0; worker < workers; worker++ {
		executor.workers.Add(1)
		go executor.runWorker()
	}
	executor.reporter.Add(1)
	go executor.runReporter(progressInterval)
	return executor
}

func withHistoricalSubmissionExecutor(ctx context.Context, executor *historicalSubmissionExecutor) context.Context {
	return context.WithValue(ctx, historicalSubmissionExecutorKey{}, executor)
}

func historicalSubmissionExecutorFromContext(ctx context.Context) *historicalSubmissionExecutor {
	executor, _ := ctx.Value(historicalSubmissionExecutorKey{}).(*historicalSubmissionExecutor)
	return executor
}

func (e *historicalSubmissionExecutor) Submit(descriptor HistoricalSubmissionJob, run func(context.Context) (historicalSubmissionJobResult, error)) (historicalSubmissionFuture, error) {
	if e == nil || run == nil {
		return historicalSubmissionFuture{}, fmt.Errorf("historical submission executor and job are required")
	}
	descriptor.ScenarioID = strings.TrimSpace(descriptor.ScenarioID)
	descriptor.TaskID = strings.TrimSpace(descriptor.TaskID)
	descriptor.TargetKey = strings.TrimSpace(descriptor.TargetKey)
	if descriptor.ScenarioID == "" || descriptor.TargetKey == "" {
		return historicalSubmissionFuture{}, fmt.Errorf("historical submission job scenario_id and target_key are required")
	}
	if descriptor.TaskID != "" && descriptor.PlannedAt.IsZero() {
		return historicalSubmissionFuture{}, fmt.Errorf("historical plan submission job %s is missing planned_at", descriptor.ScenarioID)
	}
	result := make(chan historicalSubmissionExecution, 1)
	job := historicalSubmissionJob{descriptor: descriptor, run: run, result: result}
	select {
	case <-e.ctx.Done():
		return historicalSubmissionFuture{}, e.ctx.Err()
	case e.jobs <- job:
		e.discovered.Add(1)
		return historicalSubmissionFuture{result: result}, nil
	}
}

func (e *historicalSubmissionExecutor) MarkParentCompleted() {
	if e != nil {
		e.parentCompleted.Add(1)
	}
}

func (e *historicalSubmissionExecutor) runWorker() {
	defer e.workers.Done()
	for job := range e.jobs {
		if err := e.ctx.Err(); err != nil {
			job.result <- historicalSubmissionExecution{err: err}
			close(job.result)
			continue
		}
		e.inFlight.Add(1)
		result, err := job.run(e.ctx)
		if err != nil {
			err = fmt.Errorf("historical submission job %s: %w", job.descriptor.ScenarioID, err)
		}
		e.inFlight.Add(-1)
		if err != nil {
			e.failed.Add(1)
			e.cancel()
		} else {
			e.completed.Add(1)
			if result.ReportGenerated {
				e.reports.Add(1)
			}
		}
		job.result <- historicalSubmissionExecution{result: result, err: err}
		close(job.result)
	}
}

func (e *historicalSubmissionExecutor) runReporter(interval time.Duration) {
	defer e.reporter.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			e.logProgress()
		case <-e.reporterStop:
			e.logProgress()
			return
		}
	}
}

func (e *historicalSubmissionExecutor) logProgress() {
	elapsed := time.Since(e.startedAt)
	completed := e.completed.Load()
	throughput := float64(completed)
	if elapsed > 0 {
		throughput /= elapsed.Seconds()
	}
	remaining := e.discovered.Load() - completed
	eta := "pending-discovery"
	if throughput > 0 && remaining >= 0 {
		eta = time.Duration(float64(remaining) / throughput * float64(time.Second)).Round(time.Second).String()
	}
	e.logger.Infow("Historical backfill progress",
		"business_date", e.day,
		"parent_scenarios_completed", e.parentCompleted.Load(),
		"parent_scenarios_total", e.parentExpected,
		"submission_jobs_discovered", e.discovered.Load(),
		"submission_jobs_completed", completed,
		"reports_generated", e.reports.Load(),
		"throughput_per_second", throughput,
		"in_flight", e.inFlight.Load(),
		"failed", e.failed.Load(),
		"eta", eta,
	)
}

func (e *historicalSubmissionExecutor) Close() {
	if e == nil {
		return
	}
	close(e.jobs)
	e.workers.Wait()
	e.reporterOnce.Do(func() { close(e.reporterStop) })
	e.reporter.Wait()
	e.cancel()
}
