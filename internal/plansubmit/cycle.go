package plansubmit

import "context"

type planTaskSubmitCycle struct {
	gateway              planTaskSubmitGateway
	submitClient         adminAnswerSheetSubmitClient
	logger               planTaskLogger
	planID               string
	questionnaireVersion string
	detail               *QuestionnaireDetailResponse
	workers              int
	tracker              *recentPlanTaskTracker
	verbose              bool
}

func newPlanTaskSubmitCycle(
	gateway planTaskSubmitGateway,
	submitClient adminAnswerSheetSubmitClient,
	logger planTaskLogger,
	planID string,
	questionnaireVersion string,
	detail *QuestionnaireDetailResponse,
	workers int,
	tracker *recentPlanTaskTracker,
	verbose bool,
) planTaskSubmitCycle {
	return planTaskSubmitCycle{
		gateway:              gateway,
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

func (c planTaskSubmitCycle) Execute(ctx context.Context) (*planOpenTaskSubmitStats, error) {
	stats := newPlanOpenTaskSubmitStats()
	jobs, err := c.listJobs(ctx)
	if err != nil {
		stats.RecordListLoadFailure()
		return stats, err
	}

	stats.RecordOpenedCount(len(jobs))
	if len(jobs) == 0 {
		return stats, nil
	}

	executor := newPlanTaskBatchExecutor(
		c.submitClient,
		c.logger,
		c.planID,
		c.questionnaireVersion,
		c.detail,
		c.workers,
		c.tracker,
		c.verbose,
	)
	if err := executor.Execute(ctx, jobs, stats); err != nil {
		return stats, err
	}
	return stats, nil
}

func (c planTaskSubmitCycle) listJobs(ctx context.Context) ([]planTaskJob, error) {
	return listOpenPlanTaskJobs(ctx, c.gateway, c.logger, c.planID, c.verbose)
}
