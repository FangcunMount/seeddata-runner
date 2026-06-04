package plansubmit

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
)

const (
	planOpenTaskPageSize             = 100
	planOpenTaskMaxPages             = 200
	planOpenTaskSubmitRequestTimeout = 15 * time.Second
	planOpenTaskSubmitHTTPRetryMax   = 0
	planOpenTaskSubmitMaxAttempts    = 2
	planOpenTaskSubmitRetryBackoff   = 2 * time.Second
	planOpenTaskRecentSubmitTTL      = 10 * time.Minute
)

type planTaskJob struct {
	testeeID string
	task     TaskResponse
}

type planTaskLogger interface {
	Warnw(string, ...interface{})
	Debugw(string, ...interface{})
	Infow(string, ...interface{})
}

type recentPlanTaskTracker struct {
	mu          sync.Mutex
	ttl         time.Duration
	submittedAt map[string]time.Time
}

// newRecentPlanTaskTracker 创建最近计划任务跟踪器
func newRecentPlanTaskTracker(ttl time.Duration) *recentPlanTaskTracker {
	if ttl <= 0 {
		ttl = planOpenTaskRecentSubmitTTL
	}
	return &recentPlanTaskTracker{
		ttl:         ttl,
		submittedAt: make(map[string]time.Time),
	}
}

// Seen 检查任务是否已提交
func (t *recentPlanTaskTracker) Seen(taskID string) bool {
	if t == nil {
		return false
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return false
	}

	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(now)

	until, ok := t.submittedAt[taskID]
	return ok && now.Before(until)
}

func (t *recentPlanTaskTracker) Remember(taskID string) {
	if t == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}

	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(now)
	t.submittedAt[taskID] = now.Add(t.ttl)
}

func (t *recentPlanTaskTracker) pruneLocked(now time.Time) {
	for taskID, expiresAt := range t.submittedAt {
		if !now.Before(expiresAt) {
			delete(t.submittedAt, taskID)
		}
	}
}

func listOpenPlanTaskJobs(
	ctx context.Context,
	gateway planTaskSubmitGateway,
	logger planTaskLogger,
	planID string,
	verbose bool,
) ([]planTaskJob, error) {
	tasks, err := listPlanTaskWindowTasks(ctx, gateway, logger, planSubmitOpenedTaskWindowRequest(planID, time.Now()), verbose)
	if err != nil {
		return nil, err
	}
	return appendOpenPlanTaskJobs(nil, tasks, normalizePlanID(planID), logger, verbose), nil
}

func listDailyPlanTaskJobs(
	ctx context.Context,
	gateway planTaskSubmitGateway,
	logger planTaskLogger,
	planID string,
	completionPercent int,
	testeeSource string,
	now time.Time,
	verbose bool,
) ([]planTaskJob, error) {
	planID = normalizePlanID(planID)
	if planID == "" {
		return nil, fmt.Errorf("plan-id is required")
	}
	if completionPercent <= 0 {
		return nil, nil
	}
	if completionPercent > 100 {
		completionPercent = 100
	}

	tasks, err := listPlanTaskWindowTasks(ctx, gateway, logger, planSubmitDailyTaskWindowRequest(planID, now), verbose)
	if err != nil {
		return nil, err
	}

	dailyTasks := filterPlanSubmitTasksForDay(tasks, now, logger, verbose)
	dailyTasks = filterPlanSubmitTasksByTesteeSource(ctx, gateway, logger, dailyTasks, testeeSource)
	completedCount := countPlanSubmitTasksByStatus(dailyTasks, "completed")
	targetCompletedCount := planSubmitTargetCompletedCount(len(dailyTasks), completionPercent)
	remainingSubmitQuota := targetCompletedCount - completedCount
	if remainingSubmitQuota <= 0 {
		return nil, nil
	}

	jobs := appendOpenPlanTaskJobs(nil, dailyTasks, planID, logger, verbose)
	if remainingSubmitQuota < len(jobs) {
		jobs = jobs[:remainingSubmitQuota]
	}
	return jobs, nil
}

func listPlanTaskWindowTasks(
	ctx context.Context,
	gateway planTaskSubmitGateway,
	logger planTaskLogger,
	req ListPlanTaskWindowRequest,
	verbose bool,
) ([]TaskResponse, error) {
	req.PlanID = normalizePlanID(req.PlanID)
	if req.PlanID == "" {
		return nil, fmt.Errorf("plan-id is required")
	}
	req.Status = normalizeTaskStatus(req.Status)
	resourceID := req.PlanID
	tasks := make([]TaskResponse, 0, planOpenTaskPageSize)
	for page := 1; ; page++ {
		if page > planOpenTaskMaxPages {
			logger.Warnw("Plan task window pagination stopped at safety cap",
				"plan_id", resourceID,
				"status", req.Status,
				"planned_after", req.PlannedAfter,
				"planned_before", req.PlannedBefore,
				"max_pages", planOpenTaskMaxPages,
				"tasks_loaded", len(tasks),
			)
			break
		}

		pageReq := req
		pageReq.Page = page
		pageReq.PageSize = planOpenTaskPageSize

		var windowResp *PlanTaskWindowResponse
		err := runSeedPlanOperationWithRecovery(ctx, logger, verbose, "list_plan_tasks_window", resourceID, func() error {
			resp, err := gateway.ListPlanTaskWindow(ctx, pageReq)
			if err != nil {
				return err
			}
			windowResp = resp
			return nil
		})
		if err != nil {
			return nil, err
		}
		if windowResp == nil {
			windowResp = &PlanTaskWindowResponse{}
		}

		tasks = append(tasks, windowResp.Tasks...)
		if !hasMorePlanTaskWindow(windowResp, pageReq.Page, pageReq.PageSize) {
			return tasks, nil
		}
	}
	return tasks, nil
}

func appendOpenPlanTaskJobs(
	jobs []planTaskJob,
	tasks []TaskResponse,
	planID string,
	logger planTaskLogger,
	verbose bool,
) []planTaskJob {
	sortedTasks := append([]TaskResponse(nil), tasks...)
	sortTasksBySeq(sortedTasks)

	for _, task := range sortedTasks {
		taskTesteeID := strings.TrimSpace(task.TesteeID)
		switch normalizeTaskStatus(task.Status) {
		case "opened":
			if taskTesteeID == "" {
				logger.Warnw("Skipping opened task without testee_id",
					"plan_id", planID,
					"task_id", task.ID,
				)
				continue
			}
			jobs = append(jobs, planTaskJob{
				testeeID: taskTesteeID,
				task:     task,
			})
		default:
			if verbose {
				logger.Debugw("Skipping non-open plan task while scanning daemon backlog",
					"plan_id", planID,
					"task_id", task.ID,
					"status", task.Status,
				)
			}
		}
	}
	return jobs
}

func filterPlanSubmitTasksForDay(tasks []TaskResponse, now time.Time, logger planTaskLogger, verbose bool) []TaskResponse {
	if len(tasks) == 0 {
		return nil
	}
	year, month, day := now.Date()
	filtered := make([]TaskResponse, 0, len(tasks))
	for _, task := range tasks {
		plannedAt, err := parsePlanSubmitTaskTime(task.PlannedAt, now.Location())
		if err != nil {
			if verbose {
				logger.Debugw("Skipping plan task with invalid planned_at",
					"task_id", task.ID,
					"planned_at", task.PlannedAt,
					"error", err.Error(),
				)
			}
			continue
		}
		taskYear, taskMonth, taskDay := plannedAt.In(now.Location()).Date()
		if taskYear == year && taskMonth == month && taskDay == day {
			filtered = append(filtered, task)
		}
	}
	sortTasksBySeq(filtered)
	return filtered
}

func filterPlanSubmitTasksByTesteeSource(
	ctx context.Context,
	gateway planTaskSubmitGateway,
	logger planTaskLogger,
	tasks []TaskResponse,
	testeeSource string,
) []TaskResponse {
	testeeSource = strings.TrimSpace(testeeSource)
	if len(tasks) == 0 || testeeSource == "" {
		return tasks
	}

	sourceByTesteeID := make(map[string]string)
	filtered := make([]TaskResponse, 0, len(tasks))
	for _, task := range tasks {
		testeeID := strings.TrimSpace(task.TesteeID)
		if testeeID == "" {
			continue
		}
		source, exists := sourceByTesteeID[testeeID]
		if !exists {
			testee, err := gateway.GetTesteeByID(ctx, testeeID)
			if err != nil {
				logger.Warnw("Skipping plan task because testee source lookup failed",
					"task_id", task.ID,
					"testee_id", testeeID,
					"error", err.Error(),
				)
				sourceByTesteeID[testeeID] = ""
				continue
			}
			source = ""
			if testee != nil {
				source = strings.TrimSpace(testee.Source)
			}
			sourceByTesteeID[testeeID] = source
		}
		if source != testeeSource {
			continue
		}
		filtered = append(filtered, task)
	}
	return filtered
}

func countPlanSubmitTasksByStatus(tasks []TaskResponse, status string) int {
	status = normalizeTaskStatus(status)
	count := 0
	for _, task := range tasks {
		if normalizeTaskStatus(task.Status) == status {
			count++
		}
	}
	return count
}

func planSubmitTargetCompletedCount(totalTasks, completionPercent int) int {
	if totalTasks <= 0 || completionPercent <= 0 {
		return 0
	}
	if completionPercent >= 100 {
		return totalTasks
	}
	return (totalTasks*completionPercent + 99) / 100
}

func planSubmitStartOfDay(now time.Time) time.Time {
	year, month, day := now.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, now.Location())
}

func planSubmitEndOfDay(now time.Time) time.Time {
	year, month, day := now.Date()
	return time.Date(year, month, day, 23, 59, 59, 0, now.Location())
}

func planSubmitDailyTaskWindowRequest(planID string, now time.Time) ListPlanTaskWindowRequest {
	return ListPlanTaskWindowRequest{
		PlanID:        normalizePlanID(planID),
		PlannedAfter:  planSubmitStartOfDay(now).Format("2006-01-02 15:04:05"),
		PlannedBefore: planSubmitEndOfDay(now).Format("2006-01-02 15:04:05"),
	}
}

func planSubmitOpenedTaskWindowRequest(planID string, now time.Time) ListPlanTaskWindowRequest {
	req := planSubmitDailyTaskWindowRequest(planID, now)
	req.Status = "opened"
	return req
}

func parsePlanSubmitTaskTime(raw string, loc *time.Location) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if loc == nil {
		loc = time.Local
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02"} {
		if layout == time.RFC3339 {
			if parsed, err := time.Parse(layout, raw); err == nil {
				return parsed, nil
			}
			continue
		}
		if parsed, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time format %q", raw)
}

func hasMorePlanTaskWindow(taskList *PlanTaskWindowResponse, page int, pageSize int) bool {
	if taskList == nil {
		return false
	}
	if taskList.HasMore {
		return true
	}
	if taskList.Page > 0 {
		page = taskList.Page
	}
	if taskList.PageSize > 0 {
		pageSize = taskList.PageSize
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = len(taskList.Tasks)
	}
	return pageSize > 0 && len(taskList.Tasks) >= pageSize
}

func normalizeTaskStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func buildPlanTaskSubmitRequest(
	detail *QuestionnaireDetailResponse,
	questionnaireVersion string,
	task TaskResponse,
	verbose bool,
	logger planTaskLogger,
) (*SubmitAnswerSheetRequest, error) {
	if detail == nil {
		return nil, fmt.Errorf("questionnaire detail is nil")
	}
	if strings.TrimSpace(questionnaireVersion) == "" {
		return nil, fmt.Errorf("questionnaire version is empty")
	}
	if strings.TrimSpace(detail.Version) != questionnaireVersion {
		return nil, fmt.Errorf(
			"questionnaire version mismatch while building plan answersheet: questionnaire_code=%s expected=%s loaded=%s; retry after refreshing the scale/questionnaire cache path",
			detail.Code,
			questionnaireVersion,
			detail.Version,
		)
	}

	testeeID := strings.TrimSpace(task.TesteeID)
	if testeeID == "" {
		return nil, fmt.Errorf("task %s has empty testee_id", task.ID)
	}

	rngSeed := time.Now().UnixNano()
	rngSeed += int64(parseID(testeeID))
	rngSeed += int64(parseID(task.ID))
	rng := rand.New(rand.NewSource(rngSeed))
	answers := buildAnswers(detail, rng)
	if len(answers) == 0 {
		return nil, fmt.Errorf(
			"no supported answers generated for questionnaire %s, question_types=%v",
			detail.Code,
			collectQuestionTypes(detail),
		)
	}
	if verbose {
		logBuiltAnswers(logger, answers, detail.Code, testeeID)
	}

	invalidAnswers := validateAnswers(detail, answers)
	if len(invalidAnswers) > 0 {
		logger.Warnw("Invalid answers detected for plan task submission",
			"testee_id", testeeID,
			"task_id", task.ID,
			"questionnaire_code", detail.Code,
			"invalid_count", len(invalidAnswers),
			"invalid_answers", invalidAnswers,
		)
	}

	testeeIDUint := parseID(testeeID)
	if testeeIDUint == 0 {
		return nil, fmt.Errorf("invalid testee id: %s", testeeID)
	}

	return &SubmitAnswerSheetRequest{
		QuestionnaireCode:    detail.Code,
		QuestionnaireVersion: questionnaireVersion,
		Title:                detail.Title,
		TesteeID:             testeeIDUint,
		TaskID:               task.ID,
		Answers:              answers,
	}, nil
}

func submitPlanTaskAnswerSheet(
	ctx context.Context,
	client adminAnswerSheetSubmitClient,
	req SubmitAnswerSheetRequest,
) (int, error) {
	return submitAdminAnswerSheet(ctx, client, req, adminAnswerSheetSubmitPolicy{
		Timeout:      planOpenTaskSubmitRequestTimeout,
		HTTPRetryMax: planOpenTaskSubmitHTTPRetryMax,
		MaxAttempts:  planOpenTaskSubmitMaxAttempts,
		RetryBackoff: planOpenTaskSubmitRetryBackoff,
		Retryable:    isSeedPlanRecoverableError,
	})
}
