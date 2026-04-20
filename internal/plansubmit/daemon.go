package plansubmit

import (
	"context"
	"fmt"
	"time"

	"github.com/FangcunMount/seeddata-runner/internal/scheduler"
)

const (
	planSubmitOpenTasksDaemonIdleSleep   = 30 * time.Second
	planSubmitOpenTasksDaemonActiveSleep = 5 * time.Second
)

// 守护进程：提交计划任务答卷
func seedPlanSubmitOpenTasksDaemon(
	ctx context.Context,
	deps *dependencies,
	opts planOpenTaskSubmitOptions,
) (*planOpenTaskSubmitStats, error) {
	if deps == nil {
		return nil, fmt.Errorf("dependencies are nil")
	}
	if deps.APIClient == nil {
		return nil, fmt.Errorf("api client is not initialized")
	}

	// 创建计划提交空闲轮询周期状态机
	stateMachine := newPlanSubmitCycleStateMachine(opts)
	// 创建计划提交空闲轮询周期运行器
	runners, err := newPlanTaskSubmitRunners(ctx, deps, opts, stateMachine)
	if err != nil {
		return nil, err
	}

	// 创建计划提交空闲轮询周期统计
	aggregate := newPlanOpenTaskSubmitStats()
	// 创建计划提交空闲轮询周期计数器
	for cycle := 1; ; cycle++ {
		// 创建计划提交空闲轮询周期统计
		cycleStats := newPlanOpenTaskSubmitStats()
		// 运行计划提交空闲轮询周期运行器
		for _, runner := range runners {
			// 运行计划提交空闲轮询周期
			runnerStats, err := runner.runCycle(ctx, deps.APIClient, deps.Logger, opts)
			// 如果计划提交空闲轮询周期统计不为空，则合并
			if runnerStats != nil {
				cycleStats.Merge(runnerStats)
				aggregate.Merge(runnerStats)
			}
			if err != nil {
				return aggregate, err
			}
			runner.logCycleCompleted(deps.Logger, cycle, runnerStats)
		}

		// 获取计划提交空闲轮询周期决策
		decision := stateMachine.Next(cycleStats)
		if !decision.Continue {
			return aggregate, nil
		}

		deps.Logger.Infow("Plan opened-task answersheet daemon waiting for next cycle",
			"cycle", cycle,
			"sleep", decision.SleepDuration.String(),
			"active", decision.Active,
			"opened_tasks", cycleStats.OpenedCount,
			"submitted_answersheets", cycleStats.SubmittedCount,
		)

		// 等待计划提交空闲轮询周期决策
		if err := scheduler.Wait(ctx, decision.SleepDuration); err != nil {
			return aggregate, err
		}
	}
}
