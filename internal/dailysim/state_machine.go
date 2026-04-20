package dailysim

import (
	"strings"
	"time"
)

// dailySimulationDecision 定义每日模拟用户决策
type dailySimulationDecision struct {
	RunDate        time.Time
	SlotTime       time.Time
	WaitDuration   time.Duration
	RemainingQuota int
}

// dailySimulationStateMachine 定义每日模拟用户状态机
type dailySimulationStateMachine struct {
	schedule dailySimulationSchedule
}

// newDailySimulationStateMachine 创建每日模拟用户状态机
func newDailySimulationStateMachine(schedule dailySimulationSchedule) dailySimulationStateMachine {
	return dailySimulationStateMachine{schedule: schedule}
}

// Next 返回下一轮模拟用户决策
func (m dailySimulationStateMachine) Next(now time.Time, state *dailySimulationDaemonState) dailySimulationDecision {
	now = now.In(time.Local)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	todayStart := m.schedule.SlotTime(today)
	todayEnd := m.schedule.EndTime(today)
	remainingQuota := m.remainingQuota(today, state)

	if remainingQuota == 0 {
		nextDay := today.Add(24 * time.Hour)
		nextSlot := m.schedule.SlotTime(nextDay)
		return dailySimulationDecision{
			RunDate:        nextDay,
			SlotTime:       nextSlot,
			WaitDuration:   nextSlot.Sub(now),
			RemainingQuota: remainingQuota,
		}
	}

	if now.Before(todayStart) {
		return dailySimulationDecision{
			RunDate:        today,
			SlotTime:       todayStart,
			WaitDuration:   todayStart.Sub(now),
			RemainingQuota: remainingQuota,
		}
	}

	if latestSlot, ok := m.schedule.LatestEligibleSlot(now, today); ok {
		if !m.hasCompletedSlot(state, latestSlot) {
			return dailySimulationDecision{
				RunDate:        today,
				SlotTime:       latestSlot,
				WaitDuration:   0,
				RemainingQuota: remainingQuota,
			}
		}

		nextSlot := latestSlot.Add(m.schedule.Interval)
		if nextSlot.Before(todayEnd) || nextSlot.Equal(todayEnd) {
			return dailySimulationDecision{
				RunDate:        today,
				SlotTime:       nextSlot,
				WaitDuration:   nextSlot.Sub(now),
				RemainingQuota: remainingQuota,
			}
		}
	}

	nextDay := today.Add(24 * time.Hour)
	nextSlot := m.schedule.SlotTime(nextDay)
	return dailySimulationDecision{
		RunDate:        nextDay,
		SlotTime:       nextSlot,
		WaitDuration:   nextSlot.Sub(now),
		RemainingQuota: remainingQuota,
	}
}

// MarkSuccess 标记成功
func (m dailySimulationStateMachine) MarkSuccess(
	state *dailySimulationDaemonState,
	runDate time.Time,
	slotTime time.Time,
	completedAt time.Time,
	count int,
) *dailySimulationDaemonState {
	if state == nil {
		state = &dailySimulationDaemonState{}
	}

	runDay := runDate.In(time.Local)
	runDayKey := runDay.Format("2006-01-02")

	state.LastSuccessDate = runDayKey
	state.LastSuccessAt = completedAt.In(time.Local)
	state.LastCompletedSlot = slotTime.In(time.Local)
	if state.DailyUserCountDate != runDayKey {
		state.DailyUserCountDate = runDayKey
		state.DailyUserCount = 0
	}
	state.DailyUserCount += count
	return state
}

// remainingQuota 返回剩余配额
func (m dailySimulationStateMachine) remainingQuota(day time.Time, state *dailySimulationDaemonState) int {
	if m.schedule.DailyMaxUsers <= 0 {
		return -1
	}
	if state == nil {
		return m.schedule.DailyMaxUsers
	}
	dayKey := day.In(time.Local).Format("2006-01-02")
	if strings.TrimSpace(state.DailyUserCountDate) != dayKey {
		return m.schedule.DailyMaxUsers
	}
	remaining := m.schedule.DailyMaxUsers - state.DailyUserCount
	if remaining < 0 {
		return 0
	}
	return remaining
}

// hasCompletedSlot 判断是否已完成 slot
func (m dailySimulationStateMachine) hasCompletedSlot(state *dailySimulationDaemonState, slot time.Time) bool {
	if state == nil || state.LastCompletedSlot.IsZero() {
		return false
	}
	completed := state.LastCompletedSlot.In(time.Local)
	return completed.Equal(slot.In(time.Local)) || completed.After(slot.In(time.Local))
}
