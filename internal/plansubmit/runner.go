package plansubmit

import (
	"context"
	"fmt"
)

type planTaskSubmitRunner struct {
	session   *planTaskSubmitSession
	detail    *QuestionnaireDetailResponse
	tracker   *recentPlanTaskTracker
	scaleResp *ScaleResponse
}

func newPlanTaskSubmitRunners(
	ctx context.Context,
	deps *dependencies,
	opts planOpenTaskSubmitOptions,
	stateMachine planSubmitCycleStateMachine,
) ([]planTaskSubmitRunner, error) {
	planIDs := normalizePlanIDs(opts.PlanIDs)
	if len(planIDs) == 0 {
		return nil, fmt.Errorf("plan-ids are required for the open-task submit daemon")
	}

	runners := make([]planTaskSubmitRunner, 0, len(planIDs))
	for _, planID := range planIDs {
		runner, err := newPlanTaskSubmitRunner(ctx, deps, planID, opts.Verbose)
		if err != nil {
			return nil, err
		}
		runner.logStarted(deps.Logger, opts, stateMachine)
		runners = append(runners, runner)
	}
	return runners, nil
}

func newPlanTaskSubmitRunner(
	ctx context.Context,
	deps *dependencies,
	planID string,
	verbose bool,
) (planTaskSubmitRunner, error) {
	bootstrap, err := newPlanTaskSubmitBootstrap(ctx, deps, planID, verbose)
	if err != nil {
		return planTaskSubmitRunner{}, err
	}
	return bootstrap.BuildRunner(), nil
}

func (r planTaskSubmitRunner) runCycle(
	ctx context.Context,
	submitClient adminAnswerSheetSubmitClient,
	logger planTaskLogger,
	opts planOpenTaskSubmitOptions,
) (*planOpenTaskSubmitStats, error) {
	cycle := newPlanTaskSubmitCycle(
		r.session.gateway,
		submitClient,
		logger,
		r.session.planID,
		r.scaleResp.QuestionnaireVersion,
		r.detail,
		opts.Workers,
		opts.CompletionPercent,
		opts.TesteeSource,
		r.tracker,
		opts.Verbose,
	)
	return cycle.Execute(ctx)
}

func (r planTaskSubmitRunner) logStarted(
	logger planTaskLogger,
	opts planOpenTaskSubmitOptions,
	stateMachine planSubmitCycleStateMachine,
) {
	logger.Infow("Plan opened-task answersheet daemon started",
		"plan_id", r.session.planID,
		"org_id", r.session.orgID,
		"scale_code", r.session.plan.ScaleCode,
		"questionnaire_code", r.scaleResp.QuestionnaireCode,
		"questionnaire_version", r.scaleResp.QuestionnaireVersion,
		"workers", opts.Workers,
		"completion_percent", opts.CompletionPercent,
		"testee_source", opts.TesteeSource,
		"continuous", opts.Continuous,
		"idle_interval", stateMachine.IdleInterval().String(),
		"active_interval", stateMachine.ActiveInterval().String(),
	)
}

func (r planTaskSubmitRunner) logCycleCompleted(
	logger planTaskLogger,
	cycle int,
	stats *planOpenTaskSubmitStats,
) {
	stats = normalizePlanOpenTaskSubmitStats(stats)
	logger.Infow("Plan opened-task answersheet cycle completed",
		"plan_id", r.session.planID,
		"cycle", cycle,
		"opened_tasks", stats.OpenedCount,
		"submitted_answersheets", stats.SubmittedCount,
		"skipped_tasks", stats.SkippedCount,
		"failed_task_list_loads", stats.FailedTaskListLoads,
		"failed_task_executions", stats.FailedTaskExecutions,
	)
}
