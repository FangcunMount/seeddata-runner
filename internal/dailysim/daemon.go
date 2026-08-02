package dailysim

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/FangcunMount/seeddata-runner/internal/scheduler"
)

const (
	dailySimulationDaemonDefaultRunAt       = "10:00"                                              // 每日模拟用户守护进程默认运行时间
	dailySimulationDaemonDefaultWindowEndAt = "18:00"                                              // 每日模拟用户守护进程默认结束时间
	dailySimulationDaemonDefaultInterval    = 30 * time.Minute                                     // 每日模拟用户守护进程默认轮询间隔
	dailySimulationDaemonDefaultRetryDelay  = 30 * time.Minute                                     // 每日模拟用户守护进程默认重试延迟
	dailySimulationDaemonDefaultStateFile   = ".seeddata-cache/daily-simulation-daemon-state.json" // 每日模拟用户守护进程默认状态文件
)

type dailySimulationRoute struct {
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

type dailySimulationRunOptions struct {
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

		count, err := resolveDailySimulationRunCount(cfg, decision.RunDate, decision.RemainingQuota)
		if err != nil {
			return err
		}
		deps.Logger.Infow("Daily simulation daemon starting scheduled run",
			"run_date", decision.RunDate.Format("2006-01-02"),
			"slot_time", decision.SlotTime.Format(time.RFC3339),
			"count", count,
			"remaining_daily_quota", decision.RemainingQuota,
		)

		if err := runDailySimulationRun(ctx, deps, cfg, decision.RunDate, count, "daily_simulation_daemon"); err != nil {
			deps.Logger.Warnw("Daily simulation daemon run failed",
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
	if dailySimulationAfterHoursCatchupHandled(state, today, todayEnd) {
		deps.Logger.Infow("Daily simulation daemon skipping after-hours catchup",
			"run_date", dayKey,
			"window_end_at", todayEnd.Format(time.RFC3339),
			"last_after_hours_catchup_at", state.LastAfterHoursCatchupAt.In(time.Local).Format(time.RFC3339),
			"reason", "already_handled_for_current_window_end",
		)
		return state, false, nil
	}

	targetCount, err := resolveDailySimulationRunCount(cfg, today, -1)
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
		if err := runDailySimulationRunWithOptions(
			ctx,
			deps,
			cfg,
			today,
			len(jobIndexes),
			"daily_simulation_daemon_after_hours",
			dailySimulationRunOptions{
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

func dailySimulationAfterHoursCatchupHandled(state *dailySimulationDaemonState, day time.Time, windowEnd time.Time) bool {
	if state == nil {
		return false
	}
	dayKey := day.In(time.Local).Format("2006-01-02")
	if strings.TrimSpace(state.LastAfterHoursCatchupDay) != dayKey {
		return false
	}
	if state.LastAfterHoursCatchupAt.IsZero() {
		return false
	}
	catchupAt := state.LastAfterHoursCatchupAt.In(time.Local)
	return !catchupAt.Before(windowEnd.In(time.Local))
}

// runDailySimulationRun 运行每日模拟用户批量
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
