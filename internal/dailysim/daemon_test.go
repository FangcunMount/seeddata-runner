package dailysim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/FangcunMount/seeddata-runner/internal/scheduler"
	seedapi "github.com/FangcunMount/seeddata-runner/internal/seedapi"
)

func TestResolveDailySimulationBatchCountStableRange(t *testing.T) {
	cfg := DailySimulationConfig{
		CountMin: 20,
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
	if first < 20 || first > 50 {
		t.Fatalf("expected count within [20,50], got %d", first)
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

func TestSelectDailySimulationAdditionalTargetsForTesteeStableSubset(t *testing.T) {
	cfg := DailySimulationConfig{
		AdditionalTargetMaxCount: 2,
	}
	runDate := time.Date(2026, 4, 19, 0, 0, 0, 0, time.Local)
	targets := []*dailySimulationResolvedTarget{
		{TargetCode: "SAS"},
		{TargetCode: "PHQ9"},
		{TargetCode: "GAD7"},
	}

	first := selectDailySimulationAdditionalTargetsForTestee(targets, cfg, runDate, 7)
	second := selectDailySimulationAdditionalTargetsForTestee(targets, cfg, runDate, 7)
	if len(first) < 1 || len(first) > 2 {
		t.Fatalf("expected selected targets within [1,2], got %d", len(first))
	}
	if len(second) != len(first) {
		t.Fatalf("expected stable selected count, got %d and %d", len(first), len(second))
	}
	for idx := range first {
		if first[idx].TargetCode != second[idx].TargetCode {
			t.Fatalf("expected stable target selection at %d: %s vs %s", idx, first[idx].TargetCode, second[idx].TargetCode)
		}
	}
	seen := make(map[string]struct{}, len(first))
	for _, target := range first {
		code := target.TargetCode
		if _, exists := seen[code]; exists {
			t.Fatalf("expected unique selected target code, got duplicate %s", code)
		}
		seen[code] = struct{}{}
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

func TestDailySimulationStateMachineMarkAfterHoursCatchup(t *testing.T) {
	schedule := dailySimulationSchedule{
		Window:        mustTestWindowSchedule(t, scheduler.Clock{Hour: 10, Minute: 0}, scheduler.Clock{Hour: 18, Minute: 0}, 30*time.Minute),
		DailyMaxUsers: 60,
	}
	stateMachine := newDailySimulationStateMachine(schedule)

	state := &dailySimulationDaemonState{
		DailyUserCountDate: "2026-04-17",
		DailyUserCount:     20,
	}
	runDate := time.Date(2026, 4, 17, 0, 0, 0, 0, time.Local)
	slotTime := time.Date(2026, 4, 17, 18, 0, 0, 0, time.Local)
	completedAt := time.Date(2026, 4, 17, 19, 7, 0, 0, time.Local)

	state = stateMachine.MarkAfterHoursCatchup(state, runDate, slotTime, completedAt)
	if state.LastSuccessDate != "2026-04-17" {
		t.Fatalf("unexpected last success date: %q", state.LastSuccessDate)
	}
	if !state.LastSuccessAt.Equal(completedAt) {
		t.Fatalf("unexpected last success at: %s", state.LastSuccessAt)
	}
	if !state.LastCompletedSlot.Equal(slotTime) {
		t.Fatalf("unexpected last completed slot: %s", state.LastCompletedSlot)
	}
	if state.LastAfterHoursCatchupDay != "2026-04-17" {
		t.Fatalf("unexpected last after-hours catchup day: %q", state.LastAfterHoursCatchupDay)
	}
	if !state.LastAfterHoursCatchupAt.Equal(completedAt) {
		t.Fatalf("unexpected last after-hours catchup at: %s", state.LastAfterHoursCatchupAt)
	}
	if state.DailyUserCount != 20 {
		t.Fatalf("expected daily user count to remain unchanged, got %d", state.DailyUserCount)
	}
}

func TestDailySimulationAfterHoursCatchupHandledRespectsCurrentWindowEnd(t *testing.T) {
	day := time.Date(2026, 4, 20, 0, 0, 0, 0, time.Local)
	state := &dailySimulationDaemonState{
		LastAfterHoursCatchupDay: "2026-04-20",
		LastAfterHoursCatchupAt:  time.Date(2026, 4, 20, 18, 5, 0, 0, time.Local),
	}

	if !dailySimulationAfterHoursCatchupHandled(state, day, time.Date(2026, 4, 20, 18, 0, 0, 0, time.Local)) {
		t.Fatalf("expected after-hours catchup to be treated as handled for the original window end")
	}
	if dailySimulationAfterHoursCatchupHandled(state, day, time.Date(2026, 4, 20, 19, 0, 0, 0, time.Local)) {
		t.Fatalf("expected after-hours catchup to rerun when the current window end moves later")
	}
}

func TestDailySimulationAfterHoursCatchupHandledRequiresCatchupTimestamp(t *testing.T) {
	day := time.Date(2026, 4, 20, 0, 0, 0, 0, time.Local)
	state := &dailySimulationDaemonState{
		LastAfterHoursCatchupDay: "2026-04-20",
	}

	if dailySimulationAfterHoursCatchupHandled(state, day, time.Date(2026, 4, 20, 18, 0, 0, 0, time.Local)) {
		t.Fatalf("expected legacy state without catchup timestamp to rerun catchup")
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

func TestMatchDailySimulationExistingTesteesByIndex(t *testing.T) {
	cfg := DailySimulationConfig{
		TesteeSource: "daily_simulation",
		TesteeTags:   []string{"seed"},
	}
	runDate := time.Date(2026, 4, 20, 0, 0, 0, 0, time.Local)
	firstProfile := buildDailySimulationProfile(cfg, runDate, 0)
	secondProfile := buildDailySimulationProfile(cfg, runDate, 1)
	firstBirthday, err := parseDailySimulationDOB(firstProfile.ChildDOB)
	if err != nil {
		t.Fatalf("parse first birthday: %v", err)
	}
	secondBirthday, err := parseDailySimulationDOB(secondProfile.ChildDOB)
	if err != nil {
		t.Fatalf("parse second birthday: %v", err)
	}

	items := []*ApiserverTesteeResponse{
		{
			ID:        "t-1",
			Name:      firstProfile.ChildName,
			Gender:    dailySimulationProfileGender(firstProfile.ChildGender),
			Birthday:  firstBirthday,
			Tags:      []string{"seed"},
			Source:    "daily_simulation",
			Guardians: []GuardianResponse{{Phone: firstProfile.GuardianPhone}},
			CreatedAt: runDate.Add(2 * time.Hour),
		},
		{
			ID:        "ignored-other-day",
			Name:      secondProfile.ChildName,
			Gender:    dailySimulationProfileGender(secondProfile.ChildGender),
			Birthday:  secondBirthday,
			Tags:      []string{"seed"},
			Source:    "daily_simulation",
			Guardians: []GuardianResponse{{Phone: secondProfile.GuardianPhone}},
			CreatedAt: runDate.Add(-2 * time.Hour),
		},
		{
			ID:        "ignored-wrong-source",
			Name:      secondProfile.ChildName,
			Gender:    dailySimulationProfileGender(secondProfile.ChildGender),
			Birthday:  secondBirthday,
			Tags:      []string{"seed"},
			Source:    "manual",
			Guardians: []GuardianResponse{{Phone: secondProfile.GuardianPhone}},
			CreatedAt: runDate.Add(3 * time.Hour),
		},
	}

	matched := matchDailySimulationExistingTesteesByIndex(cfg, runDate, 2, items)
	if len(matched) != 1 {
		t.Fatalf("expected 1 matched testee, got %d", len(matched))
	}
	if got := matched[1]; got == nil || got.ID != "t-1" {
		t.Fatalf("expected index 1 to match testee t-1, got %+v", got)
	}
	if got := matched[2]; got != nil {
		t.Fatalf("expected index 2 to remain unmatched, got %+v", got)
	}
}

func TestMatchDailySimulationExistingTesteesByIndexRequiresExpectedGuardian(t *testing.T) {
	cfg := DailySimulationConfig{
		TesteeSource: "daily_simulation",
		TesteeTags:   []string{"seed"},
	}
	runDate := time.Date(2026, 4, 20, 0, 0, 0, 0, time.Local)
	profile := buildDailySimulationProfile(cfg, runDate, 0)
	birthday, err := parseDailySimulationDOB(profile.ChildDOB)
	if err != nil {
		t.Fatalf("parse birthday: %v", err)
	}

	items := []*ApiserverTesteeResponse{
		{
			ID:        "wrong-guardian",
			Name:      profile.ChildName,
			Gender:    dailySimulationProfileGender(profile.ChildGender),
			Birthday:  birthday,
			Tags:      []string{"seed"},
			Source:    "daily_simulation",
			Guardians: []GuardianResponse{{Phone: "+8619904209999"}},
			CreatedAt: runDate.Add(1 * time.Hour),
		},
		{
			ID:       "multi-guardian",
			Name:     profile.ChildName,
			Gender:   dailySimulationProfileGender(profile.ChildGender),
			Birthday: birthday,
			Tags:     []string{"seed"},
			Source:   "daily_simulation",
			Guardians: []GuardianResponse{
				{Phone: profile.GuardianPhone},
				{Phone: "+8619904209998"},
			},
			CreatedAt: runDate.Add(2 * time.Hour),
		},
		{
			ID:        "expected-guardian",
			Name:      profile.ChildName,
			Gender:    dailySimulationProfileGender(profile.ChildGender),
			Birthday:  birthday,
			Tags:      []string{"seed"},
			Source:    "daily_simulation",
			Guardians: []GuardianResponse{{Phone: profile.GuardianPhone}},
			CreatedAt: runDate.Add(3 * time.Hour),
		},
	}

	matched := matchDailySimulationExistingTesteesByIndex(cfg, runDate, 1, items)
	if len(matched) != 1 {
		t.Fatalf("expected 1 matched testee, got %d", len(matched))
	}
	if got := matched[1]; got == nil || got.ID != "expected-guardian" {
		t.Fatalf("expected index 1 to match expected-guardian, got %+v", got)
	}
}

func TestSortedDailySimulationExistingIndexes(t *testing.T) {
	indexes := sortedDailySimulationExistingIndexes(map[int]*ApiserverTesteeResponse{
		5: {ID: "t-5"},
		2: {ID: "t-2"},
		9: nil,
	})
	if len(indexes) != 2 {
		t.Fatalf("expected 2 indexes, got %d", len(indexes))
	}
	if indexes[0] != 1 || indexes[1] != 4 {
		t.Fatalf("unexpected sorted indexes: %#v", indexes)
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

func TestListDailySimulationTesteesByOrgUsesApiserverMaxPageSize(t *testing.T) {
	t.Helper()

	requests := make([]url.Values, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Query())
		switch r.URL.Query().Get("page") {
		case "1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    0,
				"message": "success",
				"data": map[string]any{
					"items": []map[string]any{
						{
							"id":         "t-1",
							"name":       "child-1",
							"created_at": "2026-04-20T10:00:00+08:00",
							"updated_at": "2026-04-20T10:00:00+08:00",
						},
					},
					"total":       2,
					"page":        1,
					"page_size":   100,
					"total_pages": 2,
				},
			})
		case "2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    0,
				"message": "success",
				"data": map[string]any{
					"items": []map[string]any{
						{
							"id":         "t-2",
							"name":       "child-2",
							"created_at": "2026-04-20T10:01:00+08:00",
							"updated_at": "2026-04-20T10:01:00+08:00",
						},
					},
					"total":       2,
					"page":        2,
					"page_size":   100,
					"total_pages": 2,
				},
			})
		default:
			t.Fatalf("unexpected page query: %s", r.URL.RawQuery)
		}
	}))
	defer server.Close()

	runDate := time.Date(2026, 4, 20, 0, 0, 0, 0, time.Local)
	items, err := listDailySimulationTesteesByOrg(context.Background(), seedapi.NewAPIClient(server.URL, "", nil), 1, runDate)
	if err != nil {
		t.Fatalf("listDailySimulationTesteesByOrg returned error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 testees, got %d", len(items))
	}
	if len(requests) != 2 {
		t.Fatalf("expected 2 paged requests, got %d", len(requests))
	}
	for idx, query := range requests {
		if got := query.Get("page_size"); got != "100" {
			t.Fatalf("request %d used unexpected page_size %q", idx+1, got)
		}
		if got := query.Get("created_start_date"); got != "2026-04-20" {
			t.Fatalf("request %d used unexpected created_start_date %q", idx+1, got)
		}
		if got := query.Get("created_end_date"); got != "2026-04-20" {
			t.Fatalf("request %d used unexpected created_end_date %q", idx+1, got)
		}
	}
}
