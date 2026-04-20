package plansubmit

import (
	"time"

	"github.com/FangcunMount/seeddata-runner/internal/scheduler"
)

type planSubmitCycleDecision struct {
	Continue      bool
	Active        bool
	SleepDuration time.Duration
}

type planSubmitCycleStateMachine struct {
	continuous bool
	intervals  scheduler.Interval
}

func newPlanSubmitCycleStateMachine(opts planOpenTaskSubmitOptions) planSubmitCycleStateMachine {
	return planSubmitCycleStateMachine{
		continuous: opts.Continuous,
		intervals: scheduler.NewInterval(
			opts.IdleInterval,
			opts.ActiveInterval,
			planSubmitOpenTasksDaemonIdleSleep,
			planSubmitOpenTasksDaemonActiveSleep,
		),
	}
}

func (m planSubmitCycleStateMachine) IdleInterval() time.Duration {
	return m.intervals.IdleInterval
}

func (m planSubmitCycleStateMachine) ActiveInterval() time.Duration {
	return m.intervals.ActiveInterval
}

func (m planSubmitCycleStateMachine) Next(stats *planOpenTaskSubmitStats) planSubmitCycleDecision {
	if !m.continuous {
		return planSubmitCycleDecision{}
	}
	active := normalizePlanOpenTaskSubmitStats(stats).HasActivity()
	return planSubmitCycleDecision{
		Continue:      true,
		Active:        active,
		SleepDuration: m.intervals.NextDelay(active),
	}
}
