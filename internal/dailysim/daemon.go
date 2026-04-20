package dailysim

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	toolprogress "github.com/FangcunMount/seeddata-runner/internal/progress"
	"github.com/FangcunMount/seeddata-runner/internal/scheduler"
)

const (
	dailySimulationDaemonDefaultRunAt       = "10:00"                                              // 每日模拟用户守护进程默认运行时间
	dailySimulationDaemonDefaultWindowEndAt = "18:00"                                              // 每日模拟用户守护进程默认结束时间
	dailySimulationDaemonDefaultInterval    = 30 * time.Minute                                     // 每日模拟用户守护进程默认轮询间隔
	dailySimulationDaemonDefaultRetryDelay  = 30 * time.Minute                                     // 每日模拟用户守护进程默认重试延迟
	dailySimulationDaemonDefaultStateFile   = ".seeddata-cache/daily-simulation-daemon-state.json" // 每日模拟用户守护进程默认状态文件
)

type dailySimulationScenario struct {
	ClinicianID string
	Entry       *AssessmentEntryResponse
	Target      *dailySimulationResolvedTarget
}

type dailySimulationDaemonState struct {
	LastSuccessDate          string    `json:"lastSuccessDate"`
	LastSuccessAt            time.Time `json:"lastSuccessAt"`
	LastCompletedSlot        time.Time `json:"lastCompletedSlot"`
	DailyUserCountDate       string    `json:"dailyUserCountDate"`
	DailyUserCount           int       `json:"dailyUserCount"`
	LastAfterHoursCatchupDay string    `json:"lastAfterHoursCatchupDay"`
	LastAfterHoursCatchupAt  time.Time `json:"lastAfterHoursCatchupAt"`
}

type dailySimulationSchedule struct {
	scheduler.Window
	DailyMaxUsers int
}

type dailySimulationBatchOptions struct {
	ReuseOnly              bool
	ExistingTesteesByIndex map[int]*ApiserverTesteeResponse
	JobIndexes             []int
}

/**
 * 守护进程：模拟用户注册、建档、扫码、填报
 *
 * @param ctx 上下文
 * @param deps 依赖
 */
func seedDailySimulationDaemon(ctx context.Context, deps *dependencies) error {
	cfg := deps.Config.DailySimulation
	if cfg.IsZero() {
		return fmt.Errorf("dailySimulation config is required for daily_simulation_daemon step")
	}

	// 解析每日模拟用户调度配置
	schedule, err := resolveDailySimulationSchedule(cfg)
	if err != nil {
		return err
	}
	// 创建每日模拟用户状态机
	stateMachine := newDailySimulationStateMachine(schedule)
	stateStore := newDailySimulationStateStore(cfg.StateFile)
	// 解析每日模拟用户重试延迟
	retryDelay, err := parseDailySimulationRetryDelay(cfg.RetryDelay)
	if err != nil {
		return err
	}

	deps.Logger.Infow("Daily simulation daemon started",
		"window_start_at", fmt.Sprintf("%02d:%02d", schedule.StartAt.Hour, schedule.StartAt.Minute),
		"window_end_at", fmt.Sprintf("%02d:%02d", schedule.EndAt.Hour, schedule.EndAt.Minute),
		"interval", schedule.Interval.String(),
		"daily_max_users", schedule.DailyMaxUsers,
		"retry_delay", retryDelay.String(),
		"state_file", stateStore.Path(),
		"count_min", cfg.CountMin,
		"count_max", cfg.CountMax,
		"count_per_run", cfg.CountPerRun,
		"focus_clinicians_per_run_min", cfg.FocusCliniciansPerRunMin,
		"focus_clinicians_per_run_max", cfg.FocusCliniciansPerRunMax,
	)

	for {
		now := time.Now().In(time.Local)
		state, err := stateStore.Load()
		if err != nil {
			return err
		}

		state, handled, err := maybeHandleDailySimulationAfterHoursCatchup(ctx, deps, cfg, schedule, stateMachine, state, now)
		if err != nil {
			deps.Logger.Warnw("Daily simulation daemon after-hours catchup failed",
				"run_date", time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).Format("2006-01-02"),
				"retry_delay", retryDelay.String(),
				"error", err.Error(),
			)
			if err := scheduler.Wait(ctx, retryDelay); err != nil {
				return err
			}
			continue
		}
		if handled {
			if err := stateStore.Save(state); err != nil {
				return err
			}
			continue
		}

		decision := stateMachine.Next(now, state)
		if decision.WaitDuration > 0 {
			deps.Logger.Infow("Daily simulation daemon waiting for next run",
				"next_run_date", decision.RunDate.Format("2006-01-02"),
				"next_run_at", decision.SlotTime.Format(time.RFC3339),
				"remaining_daily_quota", decision.RemainingQuota,
				"sleep", decision.WaitDuration.String(),
			)
			if err := scheduler.Wait(ctx, decision.WaitDuration); err != nil {
				return err
			}
			continue
		}

		count, err := resolveDailySimulationBatchCount(cfg, decision.RunDate, decision.RemainingQuota)
		if err != nil {
			return err
		}

		if err := runDailySimulationBatch(ctx, deps, cfg, decision.RunDate, count, "daily_simulation_daemon"); err != nil {
			deps.Logger.Warnw("Daily simulation daemon batch failed",
				"run_date", decision.RunDate.Format("2006-01-02"),
				"count", count,
				"retry_delay", retryDelay.String(),
				"error", err.Error(),
			)
			if err := scheduler.Wait(ctx, retryDelay); err != nil {
				return err
			}
			continue
		}

		state = stateMachine.MarkSuccess(state, decision.RunDate, decision.SlotTime, time.Now().In(time.Local), count)
		if err := stateStore.Save(state); err != nil {
			return err
		}
	}
}

func maybeHandleDailySimulationAfterHoursCatchup(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
	schedule dailySimulationSchedule,
	stateMachine dailySimulationStateMachine,
	state *dailySimulationDaemonState,
	now time.Time,
) (*dailySimulationDaemonState, bool, error) {
	now = now.In(time.Local)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	todayEnd := schedule.EndTime(today)
	if !now.After(todayEnd) {
		return state, false, nil
	}

	dayKey := today.Format("2006-01-02")
	if state != nil && strings.TrimSpace(state.LastAfterHoursCatchupDay) == dayKey {
		return state, false, nil
	}

	targetCount, err := resolveDailySimulationBatchCount(cfg, today, -1)
	if err != nil {
		return state, false, err
	}
	existingTesteesByIndex, err := loadDailySimulationExistingTesteesByIndex(ctx, deps, cfg, today, targetCount)
	if err != nil {
		return state, false, err
	}
	jobIndexes := sortedDailySimulationExistingIndexes(existingTesteesByIndex)
	if len(jobIndexes) > 0 {
		deps.Logger.Infow("Daily simulation daemon running after-hours catchup",
			"run_date", dayKey,
			"window_end_at", todayEnd.Format(time.RFC3339),
			"existing_testees", len(existingTesteesByIndex),
			"catchup_testees", len(jobIndexes),
		)
		if err := runDailySimulationBatchWithOptions(
			ctx,
			deps,
			cfg,
			today,
			len(jobIndexes),
			"daily_simulation_daemon_after_hours",
			dailySimulationBatchOptions{
				ReuseOnly:              true,
				ExistingTesteesByIndex: existingTesteesByIndex,
				JobIndexes:             jobIndexes,
			},
		); err != nil {
			return state, false, err
		}
	} else {
		deps.Logger.Infow("Daily simulation daemon skipping after-hours catchup",
			"run_date", dayKey,
			"window_end_at", todayEnd.Format(time.RFC3339),
			"existing_testees", 0,
			"reason", "no_existing_mock_testees",
		)
	}

	state = stateMachine.MarkAfterHoursCatchup(state, today, todayEnd, now)
	return state, true, nil
}

// runDailySimulationBatch 运行每日模拟用户批量
func runDailySimulationBatch(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
	runDate time.Time,
	count int,
	progressLabel string,
) error {
	return runDailySimulationBatchWithOptions(ctx, deps, cfg, runDate, count, progressLabel, dailySimulationBatchOptions{})
}

func runDailySimulationBatchWithOptions(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
	runDate time.Time,
	count int,
	progressLabel string,
	options dailySimulationBatchOptions,
) error {
	if count <= 0 {
		return fmt.Errorf("%s requires count > 0", progressLabel)
	}

	var (
		iamBundle      *dailySimulationIAMBundle
		mockIAMLimiter chan struct{}
		err            error
	)
	// mock-consumer 模式走内部 REST + 现有密码登录，不再初始化 IAM gRPC bundle。
	if !dailySimulationUsesIAMMockConsumer(deps.Config.IAM) {
		iamBundle, err = newDailySimulationIAMBundle(ctx, deps.Config.IAM, deps.Config.Global.OrgID)
		if err != nil {
			return err
		}
	} else {
		mockIAMLimiter = newDailySimulationMockIAMLimiter(deps.Config.IAM)
	}
	defer func() {
		if iamBundle != nil && iamBundle.client != nil {
			_ = iamBundle.client.Close()
		}
	}()

	// 解析每日模拟用户场景
	scenarios, err := resolveDailySimulationScenariosForRun(ctx, deps, cfg, runDate)
	if err != nil {
		return err
	}
	// 如果解析的场景为空，则返回错误
	if len(scenarios) == 0 {
		return fmt.Errorf("%s resolved zero scenarios", progressLabel)
	}

	existingTesteesByIndex := options.ExistingTesteesByIndex
	if existingTesteesByIndex == nil {
		existingTesteesByIndex, err = loadDailySimulationExistingTesteesByIndex(ctx, deps, cfg, runDate, count)
		if err != nil {
			return err
		}
	}
	jobIndexes := append([]int(nil), options.JobIndexes...)
	if len(jobIndexes) == 0 {
		jobIndexes = make([]int, 0, count)
		for idx := 0; idx < count; idx++ {
			jobIndexes = append(jobIndexes, idx)
		}
	}
	jobCount := len(jobIndexes)
	if jobCount == 0 {
		return fmt.Errorf("%s resolved zero job indexes", progressLabel)
	}

	// 规范化每日模拟用户工作线程数量
	workers := normalizeDailySimulationWorkers(cfg.Workers, jobCount)

	if dailySimulationUsesIAMMockConsumer(deps.Config.IAM) {
		existingCount := len(existingTesteesByIndex)
		newTesteesNeeded := max(0, count-existingCount)
		if options.ReuseOnly {
			newTesteesNeeded = 0
		}
		deps.Logger.Infow("Daily simulation existing mock testees resolved",
			"run_date", runDate.Format("2006-01-02"),
			"target_count", count,
			"job_count", jobCount,
			"existing_testees", existingCount,
			"new_testees_needed", newTesteesNeeded,
			"reuse_only", options.ReuseOnly,
			"testee_source", normalizeDailySimulationSource(cfg.TesteeSource),
		)
	}

	// 创建每日模拟用户进度条
	progress := toolprogress.New(progressLabel+" users", jobCount)
	defer progress.Close()

	// 创建每日模拟用户工作通道
	jobs := make(chan int)
	// 创建每日模拟用户工作组
	var wg sync.WaitGroup
	var counters dailySimulationCounters
	// 创建每日模拟用户失败锁
	var failureMu sync.Mutex
	// 创建每日模拟用户失败列表
	failures := make([]string, 0, 8)

	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				// 如果上下文取消，则返回
				select {
				case <-ctx.Done():
					return
				default:
				}

				// 构建每日模拟用户配置
				profile := buildDailySimulationProfile(cfg, runDate, idx)
				existingTestee := existingTesteesByIndex[profile.Index]
				if options.ReuseOnly && existingTestee == nil {
					counters.addFailure()
					failureMu.Lock()
					if len(failures) < 8 {
						failures = append(failures, fmt.Sprintf("idx=%d guardian=%s child=%s clinician=%s journey=%s err=%s", profile.Index, profile.GuardianEmail, profile.ChildName, "", dailySimulationJourneySubmitAnswer, "after-hours reuse batch resolved missing existing testee"))
					}
					failureMu.Unlock()
					progress.Increment()
					continue
				}
				scenario := scenarios[idx%len(scenarios)]
				// 模拟每日模拟用户
				outcome, simErr := simulateDailyUser(
					ctx,
					deps,
					iamBundle,
					cfg,
					profile,
					scenario.ClinicianID,
					scenario.Entry,
					scenario.Target,
					mockIAMLimiter,
					existingTestee,
				)
				// 如果模拟用户失败，则记录失败
				if simErr != nil {
					deps.Logger.Warnw("Daily simulation user failed",
						"index", profile.Index,
						"run_date", runDate.Format("2006-01-02"),
						"guardian_phone", profile.GuardianPhone,
						"guardian_email", profile.GuardianEmail,
						"child_name", profile.ChildName,
						"clinician_id", scenario.ClinicianID,
						"entry_id", scenario.Entry.ID,
						"target_code", scenario.Target.TargetCode,
						"journey_target", outcome.JourneyTarget,
						"error", simErr.Error(),
					)
					counters.addFailure()
					failureMu.Lock()
					if len(failures) < 8 {
						failures = append(failures, fmt.Sprintf("idx=%d guardian=%s child=%s clinician=%s journey=%s err=%v", profile.Index, profile.GuardianEmail, profile.ChildName, scenario.ClinicianID, outcome.JourneyTarget, simErr))
					}
					failureMu.Unlock()
				} else {
					counters.add(outcome)
				}
				progress.Increment()
			}
		}()
	}

	for _, idx := range jobIndexes {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- idx:
		}
	}
	close(jobs)
	wg.Wait()
	progress.Complete()

	selectedClinicians := make([]string, 0, len(scenarios))
	for _, scenario := range scenarios {
		selectedClinicians = append(selectedClinicians, strings.TrimSpace(scenario.ClinicianID))
	}

	deps.Logger.Infow("Daily simulation batch completed",
		"label", progressLabel,
		"run_date", runDate.Format("2006-01-02"),
		"count", count,
		"job_count", jobCount,
		"reuse_only", options.ReuseOnly,
		"workers", workers,
		"selected_clinicians", selectedClinicians,
		"users_created", atomic.LoadInt64(&counters.userCreated),
		"children_created", atomic.LoadInt64(&counters.childCreated),
		"testees_created", atomic.LoadInt64(&counters.testeeCreated),
		"plan_enrolled", atomic.LoadInt64(&counters.enrolled),
		"entries_resolved", atomic.LoadInt64(&counters.resolved),
		"entries_intaked", atomic.LoadInt64(&counters.intaked),
		"answersheets_submitted", atomic.LoadInt64(&counters.submitted),
		"submissions_skipped", atomic.LoadInt64(&counters.skippedSubmission),
		"assessments_found", atomic.LoadInt64(&counters.assessmentCreated),
		"failed", atomic.LoadInt64(&counters.failed),
	)
	if len(failures) > 0 {
		deps.Logger.Warnw("Daily simulation failure samples", "count", len(failures), "samples", failures)
	}
	if atomic.LoadInt64(&counters.failed) > 0 {
		return fmt.Errorf("%s completed with %d failures", progressLabel, atomic.LoadInt64(&counters.failed))
	}
	return nil
}

func newDailySimulationMockIAMLimiter(cfg IAMConfig) chan struct{} {
	if !dailySimulationUsesIAMMockConsumer(cfg) {
		return nil
	}
	maxConcurrent := cfg.MockConsumer.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return make(chan struct{}, maxConcurrent)
}

// resolveDailySimulationBatchCount 解析每日模拟用户批量数量
func resolveDailySimulationBatchCount(cfg DailySimulationConfig, runDate time.Time, remainingQuota int) (int, error) {
	minCount := cfg.CountMin
	maxCount := cfg.CountMax
	if minCount == 0 && maxCount == 0 {
		count := cfg.CountPerRun
		if count <= 0 {
			count = dailySimulationDefaultCount
		}
		if remainingQuota > 0 && count > remainingQuota {
			count = remainingQuota
		}
		return count, nil
	}

	switch {
	case minCount <= 0 && maxCount > 0:
		minCount = maxCount
	case maxCount <= 0 && minCount > 0:
		maxCount = minCount
	}
	if minCount <= 0 || maxCount <= 0 {
		return 0, fmt.Errorf("dailySimulation countMin/countMax must be positive")
	}
	if maxCount < minCount {
		return 0, fmt.Errorf("dailySimulation countMax must be >= countMin")
	}
	if minCount == maxCount {
		if remainingQuota > 0 && minCount > remainingQuota {
			return remainingQuota, nil
		}
		return minCount, nil
	}
	rng := newDailySimulationRand("daily-count:" + runDate.Format("20060102"))
	count := minCount + rng.Intn(maxCount-minCount+1)
	if remainingQuota > 0 && count > remainingQuota {
		count = remainingQuota
	}
	return count, nil
}

func loadDailySimulationExistingTesteesByIndex(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
	runDate time.Time,
	count int,
) (map[int]*ApiserverTesteeResponse, error) {
	if !dailySimulationUsesIAMMockConsumer(deps.Config.IAM) || count <= 0 {
		return map[int]*ApiserverTesteeResponse{}, nil
	}
	items, err := listDailySimulationTesteesByOrg(ctx, deps.APIClient, deps.Config.Global.OrgID)
	if err != nil {
		return nil, err
	}
	return matchDailySimulationExistingTesteesByIndex(cfg, runDate, count, items), nil
}

func listDailySimulationTesteesByOrg(
	ctx context.Context,
	client *APIClient,
	orgID int64,
) ([]*ApiserverTesteeResponse, error) {
	if client == nil {
		return nil, fmt.Errorf("daily simulation api client is nil")
	}
	const pageSize = 100 // apiserver /api/v1/testees currently caps page_size at 100.
	page := 1
	items := make([]*ApiserverTesteeResponse, 0, pageSize)
	for {
		resp, err := client.ListTesteesByOrg(ctx, orgID, page, pageSize)
		if err != nil {
			return nil, fmt.Errorf("list testees by org %d page %d: %w", orgID, page, err)
		}
		items = append(items, resp.Items...)
		if len(resp.Items) == 0 || page >= resp.TotalPages {
			break
		}
		page++
	}
	return items, nil
}

func matchDailySimulationExistingTesteesByIndex(
	cfg DailySimulationConfig,
	runDate time.Time,
	count int,
	items []*ApiserverTesteeResponse,
) map[int]*ApiserverTesteeResponse {
	matched := make(map[int]*ApiserverTesteeResponse, count)
	if count <= 0 {
		return matched
	}

	indexesBySignature := make(map[string][]int, count)
	for idx := 0; idx < count; idx++ {
		profile := buildDailySimulationProfile(cfg, runDate, idx)
		signature := dailySimulationProfileSignature(profile)
		indexesBySignature[signature] = append(indexesBySignature[signature], profile.Index)
	}

	candidates := make([]*ApiserverTesteeResponse, 0, len(items))
	for _, item := range items {
		if !isDailySimulationExistingTesteeCandidate(item, cfg, runDate) {
			continue
		}
		candidates = append(candidates, item)
	}
	sort.Slice(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if left.CreatedAt.Equal(right.CreatedAt) {
			return strings.TrimSpace(left.ID) < strings.TrimSpace(right.ID)
		}
		return left.CreatedAt.Before(right.CreatedAt)
	})

	for _, item := range candidates {
		signature := dailySimulationExistingTesteeSignature(item)
		indexes := indexesBySignature[signature]
		if len(indexes) == 0 {
			continue
		}
		index := indexes[0]
		indexesBySignature[signature] = indexes[1:]
		matched[index] = item
	}
	return matched
}

func sortedDailySimulationExistingIndexes(items map[int]*ApiserverTesteeResponse) []int {
	if len(items) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(items))
	for index, item := range items {
		if item == nil {
			continue
		}
		indexes = append(indexes, index-1)
	}
	sort.Ints(indexes)
	return indexes
}

func isDailySimulationExistingTesteeCandidate(
	item *ApiserverTesteeResponse,
	cfg DailySimulationConfig,
	runDate time.Time,
) bool {
	if item == nil {
		return false
	}
	if strings.TrimSpace(item.Source) != normalizeDailySimulationSource(cfg.TesteeSource) {
		return false
	}
	createdAt := item.CreatedAt.In(time.Local)
	dayStart := time.Date(runDate.Year(), runDate.Month(), runDate.Day(), 0, 0, 0, 0, time.Local)
	dayEnd := dayStart.Add(24 * time.Hour)
	if createdAt.Before(dayStart) || !createdAt.Before(dayEnd) {
		return false
	}
	return containsAllDailySimulationTags(item.Tags, cfg.TesteeTags)
}

func containsAllDailySimulationTags(actual, expected []string) bool {
	if len(expected) == 0 {
		return true
	}
	actualSet := make(map[string]struct{}, len(actual))
	for _, tag := range actual {
		actualSet[strings.TrimSpace(tag)] = struct{}{}
	}
	for _, tag := range expected {
		if _, ok := actualSet[strings.TrimSpace(tag)]; !ok {
			return false
		}
	}
	return true
}

func dailySimulationProfileSignature(profile dailySimulationProfile) string {
	return strings.Join([]string{
		strings.TrimSpace(profile.ChildName),
		strings.TrimSpace(profile.ChildDOB),
		dailySimulationProfileGender(profile.ChildGender),
	}, "|")
}

func dailySimulationExistingTesteeSignature(item *ApiserverTesteeResponse) string {
	birthday := ""
	if item != nil && item.Birthday != nil {
		birthday = item.Birthday.In(time.Local).Format("2006-01-02")
	}
	return strings.Join([]string{
		strings.TrimSpace(item.Name),
		birthday,
		normalizeDailySimulationExistingTesteeGender(item.Gender),
	}, "|")
}

func dailySimulationProfileGender(value uint8) string {
	switch value {
	case 1:
		return "male"
	case 2:
		return "female"
	default:
		return "unknown"
	}
}

func normalizeDailySimulationExistingTesteeGender(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "male":
		return "male"
	case "2", "female":
		return "female"
	case "3", "unknown", "other":
		return "unknown"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

// resolveDailySimulationScenariosForRun 解析每日模拟用户场景
func resolveDailySimulationScenariosForRun(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
	runDate time.Time,
) ([]dailySimulationScenario, error) {
	if !cfg.EntryID.IsZero() {
		entry, target, clinicianID, err := ensureDailySimulationEntryAndTarget(ctx, deps, cfg)
		if err != nil {
			return nil, err
		}
		return []dailySimulationScenario{{
			ClinicianID: clinicianID,
			Entry:       entry,
			Target:      target,
		}}, nil
	}

	selectedClinicianIDs, err := selectDailySimulationClinicianIDsForRun(collectDailySimulationClinicianIDs(cfg.ClinicianIDs), cfg, runDate)
	if err != nil {
		return nil, err
	}

	scenarios := make([]dailySimulationScenario, 0, len(selectedClinicianIDs))
	for _, clinicianID := range selectedClinicianIDs {
		scenarioCfg := cfg
		scenarioCfg.EntryID = FlexibleID("")
		scenarioCfg.ClinicianIDs = []FlexibleID{FlexibleID(clinicianID)}

		entry, target, resolvedClinicianID, err := ensureDailySimulationEntryAndTarget(ctx, deps, scenarioCfg)
		if err != nil {
			return nil, err
		}
		scenarios = append(scenarios, dailySimulationScenario{
			ClinicianID: resolvedClinicianID,
			Entry:       entry,
			Target:      target,
		})
	}
	return scenarios, nil
}

// selectDailySimulationClinicianIDsForRun 选择每日模拟用户临床ID
func selectDailySimulationClinicianIDsForRun(
	clinicianIDs []string,
	cfg DailySimulationConfig,
	runDate time.Time,
) ([]string, error) {
	if len(clinicianIDs) == 0 {
		return nil, fmt.Errorf("dailySimulation clinician scope resolved zero clinicians")
	}
	if len(clinicianIDs) == 1 {
		return clinicianIDs, nil
	}

	minCount := cfg.FocusCliniciansPerRunMin
	maxCount := cfg.FocusCliniciansPerRunMax
	if minCount <= 0 && maxCount <= 0 {
		minCount = len(clinicianIDs)
		maxCount = len(clinicianIDs)
	}
	if minCount <= 0 {
		minCount = 1
	}
	if maxCount <= 0 {
		maxCount = minCount
	}
	if maxCount < minCount {
		return nil, fmt.Errorf("dailySimulation focusCliniciansPerRunMax must be >= focusCliniciansPerRunMin")
	}
	if minCount > len(clinicianIDs) {
		minCount = len(clinicianIDs)
	}
	if maxCount > len(clinicianIDs) {
		maxCount = len(clinicianIDs)
	}

	selectedCount := minCount
	if maxCount > minCount {
		rng := newDailySimulationRand("focus-clinicians:" + runDate.Format("20060102"))
		selectedCount = minCount + rng.Intn(maxCount-minCount+1)
	}

	rng := newDailySimulationRand("focus-clinician-order:" + runDate.Format("20060102"))
	order := rng.Perm(len(clinicianIDs))
	selected := make([]string, 0, selectedCount)
	for _, idx := range order[:selectedCount] {
		selected = append(selected, clinicianIDs[idx])
	}
	return selected, nil
}

// collectDailySimulationClinicianIDs 收集每日模拟用户临床ID
func collectDailySimulationClinicianIDs(ids []FlexibleID) []string {
	collected := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		value := strings.TrimSpace(id.String())
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		collected = append(collected, value)
	}
	return collected
}

// collectDailySimulationPlanIDs 收集每日模拟用户计划ID
func collectDailySimulationPlanIDs(ids []FlexibleID) []string {
	collected := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		value := strings.TrimSpace(id.String())
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		collected = append(collected, value)
	}
	return collected
}

// resolveDailySimulationSchedule 解析每日模拟用户调度配置
func resolveDailySimulationSchedule(cfg DailySimulationConfig) (dailySimulationSchedule, error) {
	resolved, err := scheduler.ResolveWindowConfig(scheduler.WindowConfig{
		RunAt:              cfg.RunAt,
		WindowStartAt:      cfg.WindowStartAt,
		WindowEndAt:        cfg.WindowEndAt,
		Interval:           cfg.Interval,
		DefaultRunAt:       dailySimulationDaemonDefaultRunAt,
		DefaultWindowEndAt: dailySimulationDaemonDefaultWindowEndAt,
		DefaultInterval:    dailySimulationDaemonDefaultInterval.String(),
		FieldPrefix:        "dailySimulation",
	})
	if err != nil {
		return dailySimulationSchedule{}, err
	}
	return dailySimulationSchedule{
		Window:        resolved.Window,
		DailyMaxUsers: cfg.DailyMaxUsers,
	}, nil
}

// parseDailySimulationRetryDelay 解析每日模拟用户重试延迟
func parseDailySimulationRetryDelay(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return dailySimulationDaemonDefaultRetryDelay, nil
	}
	return scheduler.ParseRelativeDuration(raw)
}
