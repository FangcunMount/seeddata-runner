package dailysim

import (
	"testing"
	"time"

	"github.com/FangcunMount/seeddata-runner/internal/scheduler"
)

func TestResolveDailySimulationBatchCountStableRange(t *testing.T) {
	cfg := DailySimulationConfig{
		CountMin: 10,
		CountMax: 50,
	}
	runDate := time.Date(2026, 4, 17, 0, 0, 0, 0, time.Local)

	first, err := resolveDailySimulationBatchCount(cfg, runDate, -1)
	if err != nil {
		t.Fatalf("resolveDailySimulationBatchCount returned error: %v", err)
	}
	second, err := resolveDailySimulationBatchCount(cfg, runDate, -1)
	if err != nil {
		t.Fatalf("resolveDailySimulationBatchCount returned error on second call: %v", err)
	}
	if first != second {
		t.Fatalf("expected stable count for same date, got %d and %d", first, second)
	}
	if first < 10 || first > 50 {
		t.Fatalf("expected count within [10,50], got %d", first)
	}
}

func TestSelectDailySimulationClinicianIDsForRunStableSubset(t *testing.T) {
	clinicianIDs := []string{"c1", "c2", "c3", "c4", "c5", "c6"}
	cfg := DailySimulationConfig{
		FocusCliniciansPerRunMin: 3,
		FocusCliniciansPerRunMax: 5,
	}
	runDate := time.Date(2026, 4, 17, 0, 0, 0, 0, time.Local)

	first, err := selectDailySimulationClinicianIDsForRun(clinicianIDs, cfg, runDate)
	if err != nil {
		t.Fatalf("selectDailySimulationClinicianIDsForRun returned error: %v", err)
	}
	second, err := selectDailySimulationClinicianIDsForRun(clinicianIDs, cfg, runDate)
	if err != nil {
		t.Fatalf("selectDailySimulationClinicianIDsForRun returned error on second call: %v", err)
	}

	if len(first) != len(second) {
		t.Fatalf("expected stable clinician count, got %d and %d", len(first), len(second))
	}
	if len(first) < 3 || len(first) > 5 {
		t.Fatalf("expected selected clinician count within [3,5], got %d", len(first))
	}
	for idx := range first {
		if first[idx] != second[idx] {
			t.Fatalf("expected stable clinician subset order, mismatch at %d: %s vs %s", idx, first[idx], second[idx])
		}
	}
	seen := make(map[string]struct{}, len(first))
	for _, id := range first {
		if _, exists := seen[id]; exists {
			t.Fatalf("expected unique clinician ids, got duplicate %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestSelectDailySimulationPlanIDStable(t *testing.T) {
	cfg := DailySimulationConfig{
		PlanIDs: []FlexibleID{"614333603412718126", "614187067651404334"},
	}
	runDate := time.Date(2026, 4, 19, 0, 0, 0, 0, time.Local)

	first := selectDailySimulationPlanID(cfg, runDate, 7)
	second := selectDailySimulationPlanID(cfg, runDate, 7)
	if first != second {
		t.Fatalf("expected stable plan selection, got %q and %q", first, second)
	}
	if first != "614333603412718126" && first != "614187067651404334" {
		t.Fatalf("unexpected selected plan id %q", first)
	}
}

func TestNextDailySimulationDaemonRun(t *testing.T) {
	t.Run("legacy single run schedule", func(t *testing.T) {
		schedule := dailySimulationSchedule{
			Window: scheduler.NewSingleRunWindow(scheduler.Clock{Hour: 10, Minute: 0}),
		}
		stateMachine := newDailySimulationStateMachine(schedule)

		nowBefore := time.Date(2026, 4, 17, 9, 15, 0, 0, time.Local)
		decision := stateMachine.Next(nowBefore, &dailySimulationDaemonState{})
		if got, want := decision.RunDate.Format("2006-01-02"), "2026-04-17"; got != want {
			t.Fatalf("expected run date %s before scheduled time, got %s", want, got)
		}
		if got, want := decision.SlotTime.Format(time.RFC3339), time.Date(2026, 4, 17, 10, 0, 0, 0, time.Local).Format(time.RFC3339); got != want {
			t.Fatalf("expected slot time %s, got %s", want, got)
		}
		if decision.WaitDuration <= 0 {
			t.Fatalf("expected positive wait before scheduled time, got %s", decision.WaitDuration)
		}
		if decision.RemainingQuota != -1 {
			t.Fatalf("expected unlimited quota, got %d", decision.RemainingQuota)
		}

		nowAfter := time.Date(2026, 4, 17, 10, 5, 0, 0, time.Local)
		decision = stateMachine.Next(nowAfter, &dailySimulationDaemonState{})
		if got, want := decision.RunDate.Format("2006-01-02"), "2026-04-17"; got != want {
			t.Fatalf("expected same-day run date after scheduled time, got %s", got)
		}
		if decision.WaitDuration != 0 {
			t.Fatalf("expected zero wait after scheduled time when not yet successful, got %s", decision.WaitDuration)
		}

		state := &dailySimulationDaemonState{
			LastCompletedSlot: time.Date(2026, 4, 17, 10, 0, 0, 0, time.Local),
		}
		decision = stateMachine.Next(nowAfter, state)
		if got, want := decision.RunDate.Format("2006-01-02"), "2026-04-18"; got != want {
			t.Fatalf("expected next-day run date after same-day completion, got %s", got)
		}
		if got, want := decision.SlotTime.Format(time.RFC3339), time.Date(2026, 4, 18, 10, 0, 0, 0, time.Local).Format(time.RFC3339); got != want {
			t.Fatalf("expected next slot time %s, got %s", want, got)
		}
		if decision.WaitDuration <= 0 {
			t.Fatalf("expected positive wait after same-day completion, got %s", decision.WaitDuration)
		}
	})

	t.Run("window schedule with daily cap", func(t *testing.T) {
		schedule := dailySimulationSchedule{
			Window:        mustTestWindowSchedule(t, scheduler.Clock{Hour: 10, Minute: 0}, scheduler.Clock{Hour: 18, Minute: 0}, 30*time.Minute),
			DailyMaxUsers: 60,
		}
		stateMachine := newDailySimulationStateMachine(schedule)

		now := time.Date(2026, 4, 17, 10, 20, 0, 0, time.Local)
		state := &dailySimulationDaemonState{
			LastCompletedSlot:  time.Date(2026, 4, 17, 10, 0, 0, 0, time.Local),
			DailyUserCountDate: "2026-04-17",
			DailyUserCount:     20,
		}
		decision := stateMachine.Next(now, state)
		if got, want := decision.RunDate.Format("2006-01-02"), "2026-04-17"; got != want {
			t.Fatalf("expected same-day run date, got %s", got)
		}
		if got, want := decision.SlotTime.Format(time.RFC3339), time.Date(2026, 4, 17, 10, 30, 0, 0, time.Local).Format(time.RFC3339); got != want {
			t.Fatalf("expected next slot time %s, got %s", want, got)
		}
		if decision.WaitDuration <= 0 {
			t.Fatalf("expected positive wait to next slot, got %s", decision.WaitDuration)
		}
		if decision.RemainingQuota != 40 {
			t.Fatalf("expected remaining quota 40, got %d", decision.RemainingQuota)
		}

		state.DailyUserCount = 60
		decision = stateMachine.Next(now, state)
		if got, want := decision.RunDate.Format("2006-01-02"), "2026-04-18"; got != want {
			t.Fatalf("expected next-day run date after reaching cap, got %s", got)
		}
		if got, want := decision.SlotTime.Format(time.RFC3339), time.Date(2026, 4, 18, 10, 0, 0, 0, time.Local).Format(time.RFC3339); got != want {
			t.Fatalf("expected next-day first slot %s, got %s", want, got)
		}
		if decision.WaitDuration <= 0 {
			t.Fatalf("expected positive wait after reaching cap, got %s", decision.WaitDuration)
		}
		if decision.RemainingQuota != 0 {
			t.Fatalf("expected zero remaining quota after reaching cap, got %d", decision.RemainingQuota)
		}
	})
}

func TestDailySimulationStateMachineMarkSuccess(t *testing.T) {
	schedule := dailySimulationSchedule{
		Window:        mustTestWindowSchedule(t, scheduler.Clock{Hour: 10, Minute: 0}, scheduler.Clock{Hour: 18, Minute: 0}, 30*time.Minute),
		DailyMaxUsers: 60,
	}
	stateMachine := newDailySimulationStateMachine(schedule)

	state := &dailySimulationDaemonState{
		DailyUserCountDate: "2026-04-16",
		DailyUserCount:     55,
	}
	runDate := time.Date(2026, 4, 17, 0, 0, 0, 0, time.Local)
	slotTime := time.Date(2026, 4, 17, 10, 30, 0, 0, time.Local)
	completedAt := time.Date(2026, 4, 17, 10, 31, 0, 0, time.Local)

	state = stateMachine.MarkSuccess(state, runDate, slotTime, completedAt, 12)
	if state.LastSuccessDate != "2026-04-17" {
		t.Fatalf("unexpected last success date: %q", state.LastSuccessDate)
	}
	if !state.LastSuccessAt.Equal(completedAt) {
		t.Fatalf("unexpected last success at: %s", state.LastSuccessAt)
	}
	if !state.LastCompletedSlot.Equal(slotTime) {
		t.Fatalf("unexpected last completed slot: %s", state.LastCompletedSlot)
	}
	if state.DailyUserCountDate != "2026-04-17" || state.DailyUserCount != 12 {
		t.Fatalf("unexpected daily count state: %#v", state)
	}
}

func mustTestWindowSchedule(t *testing.T, startAt, endAt scheduler.Clock, interval time.Duration) scheduler.Window {
	t.Helper()
	window, err := scheduler.NewWindow(startAt, endAt, interval)
	if err != nil {
		t.Fatalf("NewWindow returned error: %v", err)
	}
	return window
}

func TestResolveDailySimulationBatchCountClampsToRemainingQuota(t *testing.T) {
	cfg := DailySimulationConfig{
		CountPerRun: 20,
	}
	runDate := time.Date(2026, 4, 17, 0, 0, 0, 0, time.Local)

	count, err := resolveDailySimulationBatchCount(cfg, runDate, 7)
	if err != nil {
		t.Fatalf("resolveDailySimulationBatchCount returned error: %v", err)
	}
	if count != 7 {
		t.Fatalf("expected count to be clamped to remaining quota, got %d", count)
	}
}

func TestNewDailySimulationMockIAMLimiter(t *testing.T) {
	if limiter := newDailySimulationMockIAMLimiter(IAMConfig{}); limiter != nil {
		t.Fatalf("expected nil limiter when mock-consumer mode is disabled")
	}

	limiter := newDailySimulationMockIAMLimiter(IAMConfig{
		MockConsumer: IAMMockConsumerConfig{
			Enabled:       true,
			MaxConcurrent: 2,
		},
	})
	if limiter == nil {
		t.Fatalf("expected limiter when mock-consumer mode is enabled")
	}
	if got := cap(limiter); got != 2 {
		t.Fatalf("unexpected limiter capacity: %d", got)
	}
}
