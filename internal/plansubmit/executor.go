package plansubmit

import (
	"context"
	"sync"
	"sync/atomic"

	toolprogress "github.com/FangcunMount/seeddata-runner/internal/progress"
)

type planTaskBatchExecutor struct {
	submitClient         adminAnswerSheetSubmitClient
	logger               planTaskLogger
	planID               string
	questionnaireVersion string
	detail               *QuestionnaireDetailResponse
	workers              int
	tracker              *recentPlanTaskTracker
	verbose              bool
}

func newPlanTaskBatchExecutor(
	submitClient adminAnswerSheetSubmitClient,
	logger planTaskLogger,
	planID string,
	questionnaireVersion string,
	detail *QuestionnaireDetailResponse,
	workers int,
	tracker *recentPlanTaskTracker,
	verbose bool,
) planTaskBatchExecutor {
	return planTaskBatchExecutor{
		submitClient:         submitClient,
		logger:               logger,
		planID:               planID,
		questionnaireVersion: questionnaireVersion,
		detail:               detail,
		workers:              workers,
		tracker:              tracker,
		verbose:              verbose,
	}
}

func (e planTaskBatchExecutor) Execute(ctx context.Context, jobs []planTaskJob, stats *planOpenTaskSubmitStats) error {
	if len(jobs) == 0 {
		return nil
	}

	workers := normalizePlanWorkers(e.workers, len(jobs))
	if workers <= 0 {
		workers = 1
	}

	progress := toolprogress.New("plan_submit_open_tasks_daemon tasks", len(jobs))
	defer progress.Close()

	jobCh := make(chan planTaskJob, len(jobs))
	var submittedCount atomic.Int64
	var skippedCount atomic.Int64
	var failedTaskExecutionCount atomic.Int64
	var workerWG sync.WaitGroup

	for i := 0; i < workers; i++ {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			for job := range jobCh {
				e.processJob(
					ctx,
					job,
					&submittedCount,
					&skippedCount,
					&failedTaskExecutionCount,
					progress,
				)
			}
		}()
	}

	for _, job := range jobs {
		select {
		case <-ctx.Done():
			close(jobCh)
			workerWG.Wait()
			return ctx.Err()
		case jobCh <- job:
		}
	}

	close(jobCh)
	workerWG.Wait()
	progress.Complete()

	if stats != nil {
		stats.RecordExecutionCounts(
			int(submittedCount.Load()),
			int(skippedCount.Load()),
			int(failedTaskExecutionCount.Load()),
		)
	}
	return nil
}

func (e planTaskBatchExecutor) processJob(
	ctx context.Context,
	job planTaskJob,
	submittedCount *atomic.Int64,
	skippedCount *atomic.Int64,
	failedTaskExecutionCount *atomic.Int64,
	progress *toolprogress.Bar,
) {
	defer progress.Increment()

	select {
	case <-ctx.Done():
		return
	default:
	}

	if e.tracker != nil && e.tracker.Seen(job.task.ID) {
		skippedCount.Add(1)
		if e.verbose {
			e.logger.Debugw("Skipping recently submitted opened task",
				"plan_id", e.planID,
				"task_id", job.task.ID,
				"testee_id", job.testeeID,
			)
		}
		return
	}

	req, err := buildPlanTaskSubmitRequest(e.detail, e.questionnaireVersion, job.task, e.verbose, e.logger)
	if err != nil {
		failedTaskExecutionCount.Add(1)
		e.logger.Warnw("Skipping opened task because answersheet request build failed",
			"plan_id", e.planID,
			"task_id", job.task.ID,
			"testee_id", job.testeeID,
			"error", err.Error(),
		)
		return
	}

	if e.verbose {
		logSubmitRequest(e.logger, *req, job.testeeID)
	}

	attempts, err := submitPlanTaskAnswerSheet(ctx, e.submitClient, *req)
	if err != nil {
		failedTaskExecutionCount.Add(1)
		e.logger.Warnw("Opened plan task answersheet submit failed",
			"plan_id", e.planID,
			"task_id", job.task.ID,
			"testee_id", job.testeeID,
			"error", err.Error(),
		)
		return
	}

	if e.tracker != nil {
		e.tracker.Remember(job.task.ID)
	}
	submittedCount.Add(1)
	if e.verbose {
		e.logger.Infow("Opened plan task answersheet submitted",
			"plan_id", e.planID,
			"task_id", job.task.ID,
			"testee_id", job.testeeID,
			"attempts", attempts,
		)
	}
}
