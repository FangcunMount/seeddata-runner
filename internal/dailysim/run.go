package dailysim

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	toolprogress "github.com/FangcunMount/seeddata-runner/internal/progress"
)

func runDailySimulationRun(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
	runDate time.Time,
	count int,
	progressLabel string,
) error {
	return runDailySimulationRunWithOptions(ctx, deps, cfg, runDate, count, progressLabel, dailySimulationRunOptions{})
}

func runDailySimulationRunWithOptions(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
	runDate time.Time,
	count int,
	progressLabel string,
	options dailySimulationRunOptions,
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
	routes, err := resolveDailySimulationRoutesForRun(ctx, deps, cfg, runDate)
	if err != nil {
		return err
	}
	// 如果解析的场景为空，则返回错误
	if len(routes) == 0 {
		return fmt.Errorf("%s resolved zero routes", progressLabel)
	}
	additionalTargets, err := resolveDailySimulationAdditionalTargetsForRun(ctx, deps, cfg)
	if err != nil {
		return err
	}

	existingTesteesByIndex := options.ExistingTesteesByIndex
	if existingTesteesByIndex == nil {
		existingTesteesByIndex, err = loadDailySimulationExistingTesteesByIndex(ctx, deps, cfg, runDate, count)
		if err != nil {
			return err
		}
	}
	jobIndexes := append([]int(nil), options.JobIndexes...)
	reuseOnly := options.ExistingTesteesByIndex != nil && len(jobIndexes) > 0
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
		if reuseOnly {
			newTesteesNeeded = 0
		}
		deps.Logger.Infow("Daily simulation existing mock testees resolved",
			"run_date", runDate.Format("2006-01-02"),
			"target_count", count,
			"job_count", jobCount,
			"existing_testees", existingCount,
			"new_testees_needed", newTesteesNeeded,
			"reuse_only", reuseOnly,
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
				if reuseOnly && existingTestee == nil {
					counters.addFailure()
					failureMu.Lock()
					if len(failures) < 8 {
						failures = append(failures, fmt.Sprintf("idx=%d guardian=%s child=%s clinician=%s journey=%s err=%s", profile.Index, profile.GuardianEmail, profile.ChildName, "", dailySimulationJourneySubmitAnswer, "after-hours reuse run resolved missing existing testee"))
					}
					failureMu.Unlock()
					progress.Increment()
					continue
				}
				route := routes[idx%len(routes)]
				selectedAdditionalTargets := selectDailySimulationAdditionalTargetsForTestee(
					additionalTargets,
					cfg,
					runDate,
					profile.Index,
				)
				// 模拟每日模拟用户
				outcome, simErr := simulateDailyUserWithAdditionalTargets(
					ctx,
					deps,
					iamBundle,
					cfg,
					profile,
					route.ClinicianID,
					route.Entry,
					route.Target,
					selectedAdditionalTargets,
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
						"clinician_id", route.ClinicianID,
						"entry_id", route.Entry.ID,
						"target_code", route.Target.TargetCode,
						"additional_target_codes", dailySimulationResolvedTargetCodes(selectedAdditionalTargets),
						"journey_target", outcome.JourneyTarget,
						"error", simErr.Error(),
					)
					counters.addFailure()
					failureMu.Lock()
					if len(failures) < 8 {
						failures = append(failures, fmt.Sprintf("idx=%d guardian=%s child=%s clinician=%s journey=%s err=%v", profile.Index, profile.GuardianEmail, profile.ChildName, route.ClinicianID, outcome.JourneyTarget, simErr))
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

	selectedClinicians := make([]string, 0, len(routes))
	for _, route := range routes {
		selectedClinicians = append(selectedClinicians, strings.TrimSpace(route.ClinicianID))
	}

	deps.Logger.Infow("Daily simulation run completed",
		"label", progressLabel,
		"run_date", runDate.Format("2006-01-02"),
		"count", count,
		"job_count", jobCount,
		"reuse_only", reuseOnly,
		"workers", workers,
		"selected_clinicians", selectedClinicians,
		"users_created", atomic.LoadInt64(&counters.userCreated),
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

// resolveDailySimulationRunCount 解析每日模拟用户批量数量
func resolveDailySimulationRunCount(cfg DailySimulationConfig, runDate time.Time, remainingQuota int) (int, error) {
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
