package plansubmit

import (
	"context"
	"time"
)

type planTaskSubmitCycle struct {
	gateway              planTaskSubmitAPIGateway
	submitClient         adminAnswerSheetSubmitClient
	logger               planTaskLogger
	planID               string
	scaleCode            string
	questionnaireVersion string
	detail               *QuestionnaireDetailResponse
	workers              int
	completionPercent    int
	testeeSource         string
	ledger               *SubmissionLedger
	verbose              bool
}

func newPlanTaskSubmitCycle(
	gateway planTaskSubmitAPIGateway,
	submitClient adminAnswerSheetSubmitClient,
	logger planTaskLogger,
	planID string,
	scaleCode string,
	questionnaireVersion string,
	detail *QuestionnaireDetailResponse,
	workers int,
	completionPercent int,
	testeeSource string,
	ledger *SubmissionLedger,
	verbose bool,
) planTaskSubmitCycle {
	return planTaskSubmitCycle{
		gateway:              gateway,
		submitClient:         submitClient,
		logger:               logger,
		planID:               planID,
		scaleCode:            scaleCode,
		questionnaireVersion: questionnaireVersion,
		detail:               detail,
		workers:              workers,
		completionPercent:    completionPercent,
		testeeSource:         testeeSource,
		ledger:               ledger,
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
		c.scaleCode,
		c.questionnaireVersion,
		c.detail,
		c.workers,
		c.ledger,
		c.verbose,
	)
	if err := executor.Execute(ctx, jobs, stats); err != nil {
		return stats, err
	}
	return stats, nil
}

func (c planTaskSubmitCycle) listJobs(ctx context.Context) ([]planTaskJob, error) {
	return listDailyPlanTaskJobs(ctx, c.gateway, c.logger, c.planID, c.completionPercent, c.testeeSource, time.Now(), c.verbose)
}
