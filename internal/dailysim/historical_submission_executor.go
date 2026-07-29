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
	awaitReport     func(context.Context) (historicalSubmissionJobResult, error)
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

type historicalReportJob struct {
	job    historicalSubmissionJob
	result historicalSubmissionJobResult
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
	ctx                context.Context
	cancel             context.CancelFunc
	logger             log.Logger
	day                string
	parentExpected     int64
	jobs               chan historicalSubmissionJob
	reportJobs         chan historicalReportJob
	workers            sync.WaitGroup
	reportWorkers      sync.WaitGroup
	reporter           sync.WaitGroup
	reporterStop       chan struct{}
	reporterOnce       sync.Once
	startedAt          time.Time
	discovered         atomic.Int64
	completed          atomic.Int64
	reports            atomic.Int64
	submissionInFlight atomic.Int64
	reportInFlight     atomic.Int64
	failed             atomic.Int64
	parentCompleted    atomic.Int64
}

func newHistoricalSubmissionExecutor(
	ctx context.Context,
	logger log.Logger,
	day string,
	parentExpected int,
	submissionWorkers int,
	reportWorkers int,
	reportQueueCapacity int,
	progressInterval time.Duration,
) *historicalSubmissionExecutor {
	if submissionWorkers <= 0 {
		submissionWorkers = 1
	}
	if reportWorkers <= 0 {
		reportWorkers = submissionWorkers
	}
	if reportQueueCapacity <= 0 {
		reportQueueCapacity = historicalSubmissionQueueCapacity
	}
	if progressInterval <= 0 {
		progressInterval = 15 * time.Second
	}
	executorCtx, cancel := context.WithCancel(ctx)
	executor := &historicalSubmissionExecutor{
		ctx: executorCtx, cancel: cancel, logger: logger, day: day,
		parentExpected: int64(parentExpected), jobs: make(chan historicalSubmissionJob, historicalSubmissionQueueCapacity),
		reportJobs:   make(chan historicalReportJob, reportQueueCapacity),
		reporterStop: make(chan struct{}), startedAt: time.Now(),
	}
	for worker := 0; worker < submissionWorkers; worker++ {
		executor.workers.Add(1)
		go executor.runWorker()
	}
	for worker := 0; worker < reportWorkers; worker++ {
		executor.reportWorkers.Add(1)
		go executor.runReportWorker()
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
			e.settle(job, historicalSubmissionJobResult{}, err)
			continue
		}
		e.submissionInFlight.Add(1)
		result, err := job.run(e.ctx)
		e.submissionInFlight.Add(-1)
		if err != nil || result.awaitReport == nil {
			e.settle(job, result, err)
			continue
		}
		select {
		case <-e.ctx.Done():
			e.settle(job, result, e.ctx.Err())
		case e.reportJobs <- historicalReportJob{job: job, result: result}:
		}
	}
}

func (e *historicalSubmissionExecutor) runReportWorker() {
	defer e.reportWorkers.Done()
	for pending := range e.reportJobs {
		if err := e.ctx.Err(); err != nil {
			e.settle(pending.job, pending.result, err)
			continue
		}
		continuation := pending.result.awaitReport
		pending.result.awaitReport = nil
		e.reportInFlight.Add(1)
		result, err := continuation(e.ctx)
		e.reportInFlight.Add(-1)
		if err == nil && result.awaitReport != nil {
			err = fmt.Errorf("historical report continuation returned another deferred report")
		}
		e.settle(pending.job, result, err)
	}
}

func (e *historicalSubmissionExecutor) settle(job historicalSubmissionJob, result historicalSubmissionJobResult, err error) {
	result.awaitReport = nil
	if err != nil {
		err = fmt.Errorf("historical submission job %s: %w", job.descriptor.ScenarioID, err)
		e.failed.Add(1)
	} else {
		e.completed.Add(1)
		if result.ReportGenerated {
			e.reports.Add(1)
		}
	}
	job.result <- historicalSubmissionExecution{result: result, err: err}
	close(job.result)
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
		"in_flight", e.submissionInFlight.Load()+e.reportInFlight.Load(),
		"submission_in_flight", e.submissionInFlight.Load(),
		"report_in_flight", e.reportInFlight.Load(),
		"reports_pending", len(e.reportJobs),
		"report_queue_capacity", cap(e.reportJobs),
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
	close(e.reportJobs)
	e.reportWorkers.Wait()
	e.reporterOnce.Do(func() { close(e.reporterStop) })
	e.reporter.Wait()
	e.cancel()
}
