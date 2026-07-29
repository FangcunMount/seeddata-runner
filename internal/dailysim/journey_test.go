package dailysim

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	sdkerrors "github.com/FangcunMount/iam/v2/pkg/sdk/errors"
	"github.com/FangcunMount/seeddata-runner/internal/historicalseed"
	"github.com/FangcunMount/seeddata-runner/internal/seedconfig"
)

func TestResolveDailySimulationJourneyTargetStable(t *testing.T) {
	cfg := DailySimulationConfig{
		JourneyMix: DailySimulationJourneyMixConfig{
			RegisterOnlyWeight: 10,
			CreateTesteeWeight: 20,
			ResolveEntryWeight: 30,
			SubmitAnswerWeight: 40,
		},
	}
	runDate := time.Date(2026, 4, 19, 0, 0, 0, 0, time.Local)

	first := resolveDailySimulationJourneyTarget(cfg, runDate, 7)
	second := resolveDailySimulationJourneyTarget(cfg, runDate, 7)
	if first != second {
		t.Fatalf("expected stable journey target, got %q and %q", first, second)
	}
	switch first {
	case dailySimulationJourneyRegisterOnly,
		dailySimulationJourneyCreateTestee,
		dailySimulationJourneyResolveEntry,
		dailySimulationJourneySubmitAnswer:
	default:
		t.Fatalf("unexpected journey target %q", first)
	}
}

func TestResolveDailySimulationJourneyTargetDefaultsToSubmit(t *testing.T) {
	target := resolveDailySimulationJourneyTarget(DailySimulationConfig{}, time.Date(2026, 4, 19, 0, 0, 0, 0, time.Local), 1)
	if target != dailySimulationJourneySubmitAnswer {
		t.Fatalf("expected default target %q, got %q", dailySimulationJourneySubmitAnswer, target)
	}
}

func TestResolveDailySimulationTargetFreezesPublishedModelAndQuestionnaireVersions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/assessment-models/published/MODEL":
			_, _ = w.Write([]byte(`{"code":0,"data":{"code":"MODEL","title":"Model","status":"published","version":"2.0.0","questionnaire_code":"Q","questionnaire_version":"3.0.0"}}`))
		case "/api/v1/questionnaires/Q":
			if got := r.URL.Query().Get("version"); got != "3.0.0" {
				t.Fatalf("questionnaire version=%q", got)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"code":"Q","version":"3.0.0","title":"Questionnaire","questions":[]}}`))
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "token", log.New(log.NewOptions()))
	target, err := resolveDailySimulationTarget(context.Background(), client, client, "scale", "MODEL", "")
	if err != nil {
		t.Fatal(err)
	}
	if target.TargetVersion != "2.0.0" || target.QuestionnaireVersion != "3.0.0" {
		t.Fatalf("resolved target=%+v", target)
	}
}

func TestResolveDailySimulationJourneyTargetStableWithMockConsumerEnabled(t *testing.T) {
	cfg := DailySimulationConfig{
		JourneyMix: DailySimulationJourneyMixConfig{
			RegisterOnlyWeight: 10,
			CreateTesteeWeight: 20,
			ResolveEntryWeight: 30,
			SubmitAnswerWeight: 40,
		},
	}
	runDate := time.Date(2026, 4, 19, 0, 0, 0, 0, time.Local)

	first := resolveDailySimulationJourneyTarget(cfg, runDate, 3)
	second := resolveDailySimulationJourneyTarget(cfg, runDate, 3)
	if first != second {
		t.Fatalf("expected stable journey target with mock-consumer config, got %q and %q", first, second)
	}
	switch first {
	case dailySimulationJourneyRegisterOnly,
		dailySimulationJourneyCreateTestee,
		dailySimulationJourneyResolveEntry,
		dailySimulationJourneySubmitAnswer:
	default:
		t.Fatalf("unexpected journey target %q", first)
	}
}

func TestShouldStopDailySimulationJourneyAfter(t *testing.T) {
	cases := []struct {
		name   string
		target dailySimulationJourneyTarget
		stage  dailySimulationJourneyStage
		want   bool
	}{
		{name: "register stops after guardian", target: dailySimulationJourneyRegisterOnly, stage: dailySimulationJourneyStageGuardianAccount, want: true},
		{name: "register does not stop after entry", target: dailySimulationJourneyRegisterOnly, stage: dailySimulationJourneyStageEntryResolve, want: false},
		{name: "testee stops after testee profile", target: dailySimulationJourneyCreateTestee, stage: dailySimulationJourneyStageTesteeProfile, want: true},
		{name: "testee does not stop after intake", target: dailySimulationJourneyCreateTestee, stage: dailySimulationJourneyStageEntryIntake, want: false},
		{name: "testee does not stop after plan enrollment", target: dailySimulationJourneyCreateTestee, stage: dailySimulationJourneyStagePlanEnrollment, want: false},
		{name: "resolve stops after entry resolve", target: dailySimulationJourneyResolveEntry, stage: dailySimulationJourneyStageEntryResolve, want: true},
		{name: "resolve does not stop after intake", target: dailySimulationJourneyResolveEntry, stage: dailySimulationJourneyStageEntryIntake, want: false},
		{name: "submit stops after submit", target: dailySimulationJourneySubmitAnswer, stage: dailySimulationJourneyStageAnswerSheet, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldStopDailySimulationJourneyAfter(tc.target, tc.stage)
			if got != tc.want {
				t.Fatalf("unexpected stop decision: got=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestResolveDailySimulationIAMMockConsumerEndpointPathDefaults(t *testing.T) {
	got := resolveDailySimulationIAMMockConsumerEndpointPath(IAMConfig{})
	if got != "/api/v2/internal/authn/mock-consumers/ensure" {
		t.Fatalf("unexpected default endpoint path: %q", got)
	}
}

func TestDailySimulationUsesIAMMockConsumer(t *testing.T) {
	if dailySimulationUsesIAMMockConsumer(IAMConfig{}) {
		t.Fatalf("expected mock-consumer mode disabled by default")
	}
	if !dailySimulationUsesIAMMockConsumer(IAMConfig{
		MockConsumer: IAMMockConsumerConfig{Enabled: true},
	}) {
		t.Fatalf("expected mock-consumer mode enabled")
	}
}

func TestDailySimulationTesteeID(t *testing.T) {
	if got := dailySimulationTesteeID(nil); got != "" {
		t.Fatalf("expected empty testee id for nil testee, got %q", got)
	}
	if got := dailySimulationTesteeID(&TesteeResponse{ID: " 615 "}); got != "615" {
		t.Fatalf("expected trimmed testee id, got %q", got)
	}
}

func TestHistoricalIAMMetadataIsExplicitAndOrdinaryRequestIsUnchanged(t *testing.T) {
	base := EnsureIAMMockConsumerRequest{Name: "Guardian"}
	ordinary := withHistoricalIAMMetadata(context.Background(), base, "Guardian")
	if ordinary.Profile != nil || ordinary.Meta != nil {
		t.Fatalf("ordinary mock consumer request changed: %+v", ordinary)
	}
	historicalCtx := historicalseed.WithContext(context.Background(), historicalseed.Context{
		BatchID: "batch", ScenarioID: "2025-01-01/1/register_only/model", OrgID: 1, Version: historicalseed.Version1,
	})
	got := withHistoricalIAMMetadata(historicalCtx, base, "Guardian")
	if got.Profile["nickname"] != "Guardian" || got.Meta["source"] != "seeddata_historical" || got.Meta["seed_batch_id"] != "batch" || got.Meta["seed_scenario_id"] == "" {
		t.Fatalf("historical IAM metadata mismatch: %+v", got)
	}
}

func TestHistoricalPlanCompletesEveryDeterministicallySelectedTaskBeforeCutoff(t *testing.T) {
	location, _ := time.LoadLocation(historicalTimezone)
	runDate := time.Date(2025, 1, 1, 0, 0, 0, 0, location)
	tasks := make([]TaskResponse, 0, 101)
	wantSelected := map[string]struct{}{}
	for index := 0; index < 100; index++ {
		id := fmt.Sprintf("%d", 1000+index)
		tasks = append(tasks, TaskResponse{ID: id, PlannedAt: "2025-01-01T09:00:00+08:00"})
		if deterministicHistoricalInt("batch", runDate, 7, "task-complete:"+id, 100) < 60 {
			wantSelected[id] = struct{}{}
		}
	}
	tasks = append(tasks, TaskResponse{ID: "9999", PlannedAt: "2025-01-04T09:00:00+08:00"})
	opened := map[string]struct{}{}
	discovered := map[string]struct{}{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/plans/enroll":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": EnrollmentResponse{PlanID: "77", EnrollmentID: "88", Tasks: tasks}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/plans/77":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": PlanResponse{ID: "77", ScaleCode: "MODEL"}})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/plans/tasks/") && strings.HasSuffix(r.URL.Path, "/open"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/plans/tasks/"), "/open")
			if _, ok := discovered[id]; !ok {
				t.Fatalf("task %s was opened before its child scenario was persisted", id)
			}
			opened[id] = struct{}{}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": TaskResponse{ID: id}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "token", log.New(log.NewOptions()))
	state := &dailySimulationJourneyState{
		deps: &dependencies{APIClient: client}, planID: "77", testee: &TesteeResponse{ID: "42"},
		profile: dailySimulationProfile{RunDate: runDate, Index: 7},
		target:  &dailySimulationResolvedTarget{TargetCode: "MODEL"},
	}
	historical := historicalseed.Context{BatchID: "batch", ScenarioID: "parent", OrgID: 1, Version: historicalseed.Version1}
	ctx := withHistoricalCutoff(historicalseed.WithContext(context.Background(), historical), time.Date(2025, 1, 3, 0, 0, 0, 0, location))
	ctx = withHistoricalPlanTaskDiscoveryRecorder(ctx, func(_ historicalseed.Context, recovery HistoricalPlanTaskRecovery) error {
		discovered[recovery.TaskID] = struct{}{}
		return nil
	})
	if _, err := dailySimulationStageEnrollPlan(ctx, state); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 0 || len(state.selectedTasks) != len(wantSelected) {
		t.Fatalf("task opening must be deferred to submission workers: opened=%d selected=%d want=%d", len(opened), len(state.selectedTasks), len(wantSelected))
	}
	ctx = withHistoricalLocalStageRecorder(ctx, func(_ historicalseed.Context, _ dailySimulationJourneyStage, _ dailySimulationOutcome, _ *dailySimulationResolvedTarget) error {
		return nil
	})
	for _, task := range state.selectedTasks {
		if err := ensureHistoricalPlanTaskOpen(historicalseed.WithContext(ctx, task.Context), state, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	if len(opened) != len(wantSelected) {
		t.Fatalf("submission workers opened=%d want=%d", len(opened), len(wantSelected))
	}
	for id := range wantSelected {
		if _, ok := opened[id]; !ok {
			t.Fatalf("selected task %s was not opened", id)
		}
	}
	if _, ok := opened["9999"]; ok {
		t.Fatal("task after the inclusive backfill cutoff was opened")
	}
}

func TestHistoricalPlanRejectsIncompatibleTargetBeforeEnrollment(t *testing.T) {
	enrollmentCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/plans/77":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": PlanResponse{ID: "77", ScaleCode: "yGtSs1"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/plans/enroll":
			enrollmentCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": EnrollmentResponse{PlanID: "77", EnrollmentID: "88"}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "token", log.New(log.NewOptions()))
	state := &dailySimulationJourneyState{
		deps:   &dependencies{APIClient: client},
		planID: "77",
		testee: &TesteeResponse{ID: "42"},
		target: &dailySimulationResolvedTarget{TargetCode: "3adyDE"},
	}
	ctx := historicalseed.WithContext(context.Background(), historicalseed.Context{
		BatchID: "batch", ScenarioID: "scenario", OrgID: 1, Version: historicalseed.Version1,
	})

	_, err := dailySimulationStageEnrollPlan(ctx, state)
	if err == nil || !strings.Contains(err.Error(), "scale yGtSs1 does not match scenario target 3adyDE") {
		t.Fatalf("expected historical plan target conflict, got %v", err)
	}
	if enrollmentCalls != 0 {
		t.Fatalf("incompatible historical plan called enrollment API %d times", enrollmentCalls)
	}
}

func TestHistoricalPlanResumeUsesServerEnrollmentAndTaskFactsWithoutMutation(t *testing.T) {
	location, _ := time.LoadLocation(historicalTimezone)
	runDate := time.Date(2025, 1, 1, 0, 0, 0, 0, location)
	taskID := ""
	for candidate := 1000; candidate < 2000; candidate++ {
		value := strconv.Itoa(candidate)
		if deterministicHistoricalInt("batch", runDate, 7, "task-complete:"+value, 100) < 60 {
			taskID = value
			break
		}
	}
	if taskID == "" {
		t.Fatal("failed to find deterministic selected task")
	}
	plannedAt := "2025-01-01T09:00:00+08:00"
	mutationCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/internal/v1/plans/tasks/window":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": PlanTaskWindowResponse{
				Tasks: []TaskResponse{{ID: taskID, PlanID: "77", TesteeID: "42", PlannedAt: plannedAt}}, Page: 1, PageSize: 100,
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/plans/77":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": PlanResponse{ID: "77", ScaleCode: "MODEL"}})
		case r.Method == http.MethodPost && (r.URL.Path == "/api/v1/plans/enroll" || strings.HasSuffix(r.URL.Path, "/open")):
			mutationCalls++
			t.Fatalf("resume repeated historical mutation %s", r.URL.Path)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "token", log.New(log.NewOptions()))
	historical := historicalseed.Context{BatchID: "batch", ScenarioID: "parent", OrgID: 1, Version: historicalseed.Version1}
	childID := fmt.Sprintf("2025-01-01/7/%s/%s", dailySimulationJourneySubmitAnswer, taskID)
	snapshot := &HistoricalDaySnapshot{Scenarios: map[string]HistoricalScenarioSnapshot{
		"parent": {Server: map[string]HistoricalStageRecord{
			"plan_enrollment": {ID: 1, Stage: "plan_enrollment", Status: "completed", ResourceID: "88", PayloadHash: "hash", BusinessAt: runDate.Add(8 * time.Hour), PayloadJSON: json.RawMessage(fmt.Sprintf(`{"enrollment_id":"88","plan_id":"77","task_ids":[%q]}`, taskID))},
		}},
		childID: {Server: map[string]HistoricalStageRecord{
			"task_open": {ID: 2, Stage: "task_open", Status: "completed", ResourceID: taskID, PayloadHash: "hash", BusinessAt: runDate.Add(9 * time.Hour)},
		}},
	}}
	state := &dailySimulationJourneyState{
		deps: &dependencies{APIClient: client}, planID: "77", testee: &TesteeResponse{ID: "42"},
		profile: dailySimulationProfile{RunDate: runDate, Index: 7},
		target:  &dailySimulationResolvedTarget{TargetType: "scale", TargetCode: "MODEL"},
	}
	discoveries := 0
	ctx := historicalseed.WithContext(context.Background(), historical)
	ctx = withHistoricalCutoff(ctx, runDate.AddDate(0, 0, 2))
	ctx = withHistoricalDaySnapshot(ctx, snapshot)
	ctx = withHistoricalPlanTaskDiscoveryRecorder(ctx, func(_ historicalseed.Context, recovery HistoricalPlanTaskRecovery) error {
		discoveries++
		if recovery.TaskID != taskID || recovery.ScenarioID != childID {
			t.Fatalf("unexpected recovery: %+v", recovery)
		}
		return nil
	})
	if _, err := dailySimulationStageEnrollPlan(ctx, state); err != nil {
		t.Fatal(err)
	}
	if mutationCalls != 0 || discoveries != 1 || state.outcome.EnrollmentID != "88" || len(state.selectedTasks) != 1 || state.selectedTasks[0].ID != taskID {
		t.Fatalf("resume outcome=%+v selected=%+v mutations=%d discoveries=%d", state.outcome, state.selectedTasks, mutationCalls, discoveries)
	}
}

func TestHistoricalAnswerResumeMergesServerTerminalWithoutSubmitting(t *testing.T) {
	historical := historicalseed.Context{BatchID: "batch", ScenarioID: "scenario", OrgID: 1, Version: historicalseed.Version1}
	serverStages := map[string]HistoricalStageRecord{
		"answersheet_submit":   {Stage: "answersheet_submit", Status: "completed", ResourceID: "answer-1"},
		"assessment_created":   {Stage: "assessment_created", Status: "completed", ResourceID: "assessment-1"},
		"assessment_submitted": {Stage: "assessment_submitted", Status: "completed", ResourceID: "assessment-1"},
		"outcome_committed":    {Stage: "outcome_committed", Status: "completed", ResourceID: "outcome-1"},
		"report_generated":     {Stage: "report_generated", Status: "completed", ResourceID: "report-1"},
	}
	ctx := historicalseed.WithContext(context.Background(), historical)
	ctx = withHistoricalDaySnapshot(ctx, &HistoricalDaySnapshot{Scenarios: map[string]HistoricalScenarioSnapshot{
		historical.ScenarioID: {Server: serverStages},
	}})
	recorded := make(map[string]int)
	ctx = withHistoricalLocalStageRecorder(ctx, func(_ historicalseed.Context, stage dailySimulationJourneyStage, _ dailySimulationOutcome, _ *dailySimulationResolvedTarget) error {
		recorded[string(stage)]++
		return nil
	})
	state := &dailySimulationJourneyState{
		deps: &dependencies{}, entry: &AssessmentEntryResponse{ID: "entry-1"}, testee: &TesteeResponse{ID: "42"},
		profile: dailySimulationProfile{RunDate: time.Date(2025, 1, 1, 0, 0, 0, 0, time.Local)},
		target: &dailySimulationResolvedTarget{
			QuestionnaireCode: "Q", QuestionnaireVersion: "1", RequiresAssessment: true,
			QuestionnaireDetail: &QuestionnaireDetailResponse{},
		},
	}
	if _, err := dailySimulationStageSubmitAnswerSheet(ctx, state); err != nil {
		t.Fatal(err)
	}
	if state.outcome.AnswerSheetID != "answer-1" || state.outcome.AssessmentID != "assessment-1" {
		t.Fatalf("restored outcome=%+v", state.outcome)
	}
	for _, stage := range []string{"answersheet_submit", "assessment_created", "outcome_committed", "report_generated"} {
		if recorded[stage] != 1 {
			t.Fatalf("local stage %s recorded %d times", stage, recorded[stage])
		}
	}
}

func TestHistoricalEntryResumeUsesServerFactsWithoutRepeatingPublicAPIs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("entry resume repeated public API %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	historical := historicalseed.Context{BatchID: "batch", ScenarioID: "scenario", OrgID: 1, Version: historicalseed.Version1}
	ctx := historicalseed.WithContext(context.Background(), historical)
	ctx = withHistoricalDaySnapshot(ctx, &HistoricalDaySnapshot{Scenarios: map[string]HistoricalScenarioSnapshot{
		historical.ScenarioID: {Server: map[string]HistoricalStageRecord{
			"entry_resolve": {Stage: "entry_resolve", Status: "completed", ResourceID: "entry-1", PayloadHash: "hash", BusinessAt: time.Now()},
			"entry_intake":  {Stage: "entry_intake", Status: "completed", ResourceID: "42", PayloadHash: "hash", BusinessAt: time.Now()},
		}},
	}})
	state := &dailySimulationJourneyState{
		deps:  &dependencies{APIClient: NewAPIClient(server.URL, "token", log.New(log.NewOptions()))},
		entry: &AssessmentEntryResponse{ID: "entry-1", Token: "entry-token"}, testee: &TesteeResponse{ID: "42"},
	}
	if _, err := dailySimulationStageResolveEntry(ctx, state); err != nil {
		t.Fatal(err)
	}
	if _, err := dailySimulationStageIntakeEntry(ctx, state); err != nil {
		t.Fatal(err)
	}
	if !state.outcome.EntryResolved || !state.outcome.EntryIntaked {
		t.Fatalf("entry facts were not restored: %+v", state.outcome)
	}
}

func TestShouldRetryDailySimulationIAMLogin(t *testing.T) {
	if !shouldRetryDailySimulationIAMLogin(context.DeadlineExceeded) {
		t.Fatalf("expected timeout to be retryable")
	}
	if !shouldRetryDailySimulationIAMLogin(assertErr("iam login failed: status=502 body=bad gateway")) {
		t.Fatalf("expected 502 to be retryable")
	}
	if !shouldRetryDailySimulationIAMLogin(sdkerrors.ErrServiceUnavailable) {
		t.Fatalf("expected IAM SDK unavailable error to be retryable")
	}
	if !shouldRetryDailySimulationIAMLogin(assertErr("dial tcp: lookup iam-apiserver on 127.0.0.11:53: no such host")) {
		t.Fatalf("expected Docker DNS failure to be retryable")
	}
	if !shouldRetryDailySimulationIAMLogin(assertErr("dial tcp 172.20.0.10:9080: connect: connection refused")) {
		t.Fatalf("expected connection refusal to be retryable")
	}
	if shouldRetryDailySimulationIAMLogin(assertErr("iam login failed: status=401 body=unauthorized")) {
		t.Fatalf("expected 401 not to be retryable")
	}
}

func TestHistoricalGuardianSessionRestoreHonorsIAMLimiter(t *testing.T) {
	const (
		jobs  = 8
		limit = 2
	)
	var inFlight atomic.Int64
	var maximum atomic.Int64
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/authn/login" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		current := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"access_token": "token-1"},
		})
	}))
	defer server.Close()

	state := &dailySimulationJourneyState{
		deps: &dependencies{
			Logger: log.New(log.NewOptions()),
			Config: &seedconfig.Config{
				Global: seedconfig.GlobalConfig{OrgID: 1},
				IAM: seedconfig.IAMConfig{
					LoginURL:     server.URL + "/api/v2/authn/login",
					MockConsumer: seedconfig.IAMMockConsumerConfig{Enabled: true},
				},
			},
		},
		cfg: DailySimulationConfig{UserPassword: "DailySim@123"},
		profile: dailySimulationProfile{
			GuardianEmail: "guardian@example.com",
			GuardianPhone: "+8619900000001",
			RunDate:       time.Date(2025, 1, 1, 0, 0, 0, 0, time.Local),
		},
		mockIAMLimiter: make(chan struct{}, limit),
	}

	start := make(chan struct{})
	errors := make(chan error, jobs)
	var workers sync.WaitGroup
	for index := 0; index < jobs; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := restoreDailySimulationGuardianSessionWithLimiter(context.Background(), state)
			errors <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != jobs {
		t.Fatalf("login calls=%d, want %d", got, jobs)
	}
	if got := maximum.Load(); got != limit {
		t.Fatalf("maximum IAM login concurrency=%d, want %d", got, limit)
	}
}

func TestEnsureDailySimulationTesteeDoesNotSendSeedTagByDefault(t *testing.T) {
	var captured CollectionCreateTesteeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/testees":
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"id":"testee-1","name":"王子轩","iam_profile_id":"profile-1"}}`))
			return
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	profile := dailySimulationProfile{
		GuardianName: "王敏",
		ChildName:    "王子轩",
		ChildDOB:     "2014-04-20",
		ChildGender:  1,
	}

	testee, created, err := ensureDailySimulationTestee(context.Background(), client, DailySimulationConfig{}, profile)
	if err != nil {
		t.Fatalf("ensure testee: %v", err)
	}
	if !created {
		t.Fatalf("expected created testee")
	}
	if testee == nil || testee.ID != "testee-1" || testee.IAMProfileID != "profile-1" {
		t.Fatalf("unexpected testee response: %+v", testee)
	}
	if len(captured.Tags) != 0 {
		t.Fatalf("expected no default testee tags, got %v", captured.Tags)
	}
	if captured.Name != "王子轩" || captured.Gender != 1 || captured.Birthday != "2014-04-20" {
		t.Fatalf("unexpected create testee payload: %+v", captured)
	}
}

func TestEnsureDailySimulationTesteeNormalizesLegacyGuardianRelation(t *testing.T) {
	var captured CollectionCreateTesteeRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/testees" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"id":"testee-1","name":"王子轩"}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	_, _, err := ensureDailySimulationTestee(context.Background(), client, DailySimulationConfig{
		GuardianRelation: "guardian",
	}, dailySimulationProfile{
		ChildName:   "王子轩",
		ChildDOB:    "2014-04-20",
		ChildGender: 1,
	})
	if err != nil {
		t.Fatalf("ensure testee: %v", err)
	}
	if captured.Relation != seedconfig.DefaultDailySimulationGuardianRelation {
		t.Fatalf("unexpected relation %q", captured.Relation)
	}
}

func TestEnsureDailySimulationGuardianMockConsumerLoginOmitsTenantID(t *testing.T) {
	var capturedLogin map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/internal/authn/mock-consumers/ensure":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    0,
				"message": "success",
				"data": EnsureIAMMockConsumerResponse{
					UserID:          "1001",
					LoginIdentityID: "2001",
					LoginID:         "guardian@example.com",
					IsNewUser:       true,
					IsNewIdentity:   true,
				},
			})
		case "/api/v2/authn/login":
			if err := json.NewDecoder(r.Body).Decode(&capturedLogin); err != nil {
				t.Fatalf("decode login request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":    0,
				"message": "success",
				"data": map[string]any{
					"access_token": unsignedDailySimulationJWT(t, map[string]any{
						"sub":       "1001",
						"user_id":   "1001",
						"tenant_id": "1",
						"aud":       []string{"qs-api", "collection-api"},
					}),
					"token_type": "Bearer",
					"expires_in": 900,
				},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	userID, token, created, err := ensureDailySimulationGuardianMockConsumer(context.Background(), &dependencies{
		Logger: log.New(log.NewOptions()),
		Config: &seedconfig.Config{
			Global: seedconfig.GlobalConfig{OrgID: 1},
			IAM: seedconfig.IAMConfig{
				BaseURL:  server.URL,
				LoginURL: server.URL + "/api/v2/authn/login",
				TenantID: "1",
				MockConsumer: seedconfig.IAMMockConsumerConfig{
					Enabled:      true,
					SharedSecret: "secret",
					EndpointPath: "/api/v2/internal/authn/mock-consumers/ensure",
				},
			},
		},
	}, DailySimulationConfig{UserPassword: "DailySim@123"}, dailySimulationProfile{
		GuardianName:  "Guardian",
		GuardianEmail: "guardian@example.com",
		GuardianPhone: "+8619900000001",
		RunDate:       time.Date(2026, 5, 10, 0, 0, 0, 0, time.Local),
	})
	if err != nil {
		t.Fatalf("ensure guardian mock consumer: %v", err)
	}
	if userID != "1001" || strings.TrimSpace(token) == "" || !created {
		t.Fatalf("unexpected result: userID=%q token=%q created=%v", userID, token, created)
	}

	payload, ok := capturedLogin["method_payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected method_payload object, got %#v", capturedLogin["method_payload"])
	}
	if _, ok := payload["tenant_id"]; ok {
		t.Fatalf("mock consumer login must omit tenant_id, got payload=%#v", payload)
	}
}

func TestDailySimulationGuardianStagePropagatesMockConsumerError(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":100201,"message":"seed mock secret invalid"}`))
	}))
	defer server.Close()

	state := &dailySimulationJourneyState{
		deps: &dependencies{
			Logger: log.New(log.NewOptions()),
			Config: &seedconfig.Config{
				Global: seedconfig.GlobalConfig{OrgID: 1},
				IAM: seedconfig.IAMConfig{
					BaseURL:  server.URL,
					LoginURL: server.URL + "/api/v2/authn/login",
					MockConsumer: seedconfig.IAMMockConsumerConfig{
						Enabled:      true,
						SharedSecret: "wrong-secret",
						EndpointPath: "/api/v2/internal/authn/mock-consumers/ensure",
					},
				},
			},
		},
		cfg: DailySimulationConfig{UserPassword: "DailySim@123"},
		profile: dailySimulationProfile{
			GuardianName:  "Guardian",
			GuardianEmail: "guardian@example.com",
			GuardianPhone: "+8619900000001",
			RunDate:       time.Date(2025, 1, 1, 0, 0, 0, 0, time.Local),
		},
		mockIAMLimiter: make(chan struct{}, 1),
	}

	_, err := dailySimulationStageEnsureGuardianAccount(context.Background(), state)
	if err == nil {
		t.Fatal("expected mock-consumer error")
	}
	if !strings.Contains(err.Error(), "authentication failed (401)") || !strings.Contains(err.Error(), "seed mock secret invalid") {
		t.Fatalf("expected original mock-consumer error, got %v", err)
	}
	if strings.Contains(err.Error(), "guardian token is empty") {
		t.Fatalf("mock-consumer error was replaced by empty-token error: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("expected one mock-consumer request, got %d", requestCount)
	}
}

func TestBuildDailySimulationAssessmentEntryIntakeRequestForNewSeedUser(t *testing.T) {
	req, err := buildDailySimulationAssessmentEntryIntakeRequest(&dailySimulationJourneyState{
		profile: dailySimulationProfile{
			ChildName:   " Seed Child ",
			ChildGender: 2,
			ChildDOB:    "2018-01-02",
		},
		testee: &TesteeResponse{
			ID:           "615508260325175854",
			Name:         "Seed Child",
			IAMProfileID: "628990000000000001",
		},
	})
	if err != nil {
		t.Fatalf("build intake request: %v", err)
	}
	if req.ProfileID == nil || *req.ProfileID != 628990000000000001 {
		t.Fatalf("expected collection-created iam_profile_id on intake request: %+v", req)
	}
	if req.Name != "Seed Child" || req.Gender != "female" {
		t.Fatalf("unexpected intake request fields: %+v", req)
	}
	if req.Birthday == nil || req.Birthday.Format("2006-01-02") != "2018-01-02" {
		t.Fatalf("unexpected birthday: %+v", req.Birthday)
	}
}

func TestBuildDailySimulationAssessmentEntryIntakeRequestRejectsNewSeedUserWithoutProfile(t *testing.T) {
	_, err := buildDailySimulationAssessmentEntryIntakeRequest(&dailySimulationJourneyState{
		profile: dailySimulationProfile{ChildName: "Seed Child"},
		testee:  &TesteeResponse{ID: "6153", Name: "Seed Child"},
	})
	if err == nil {
		t.Fatal("expected new seed user without iam_profile_id to be rejected")
	}
}

func TestBuildDailySimulationAssessmentEntryIntakeRequestForExistingProfileTestee(t *testing.T) {
	profileID := "6154"
	birthday := time.Date(2017, 3, 4, 0, 0, 0, 0, time.UTC)
	req, err := buildDailySimulationAssessmentEntryIntakeRequest(&dailySimulationJourneyState{
		profile: dailySimulationProfile{
			ChildName:   "Fallback Child",
			ChildGender: 1,
			ChildDOB:    "2018-01-02",
		},
		existingTestee: &ApiserverTesteeResponse{
			ID:        "6153",
			ProfileID: &profileID,
			Name:      "Existing Child",
			Gender:    "male",
			Birthday:  &birthday,
		},
	})
	if err != nil {
		t.Fatalf("build intake request: %v", err)
	}
	if req.ProfileID == nil || *req.ProfileID != 6154 {
		t.Fatalf("unexpected profile id: %+v", req.ProfileID)
	}
	if req.Name != "Existing Child" || req.Gender != "male" {
		t.Fatalf("unexpected intake request fields: %+v", req)
	}
	if req.Birthday == nil || req.Birthday.Format("2006-01-02") != "2017-03-04" {
		t.Fatalf("unexpected birthday: %+v", req.Birthday)
	}
}

func TestBuildDailySimulationAssessmentEntryIntakeRequestRejectsExistingTesteeWithoutProfile(t *testing.T) {
	_, err := buildDailySimulationAssessmentEntryIntakeRequest(&dailySimulationJourneyState{
		profile: dailySimulationProfile{ChildName: "Seed Child"},
		existingTestee: &ApiserverTesteeResponse{
			ID:   "6153",
			Name: "Existing Child",
		},
	})
	if err == nil {
		t.Fatal("expected existing testee without profile_id to be rejected")
	}
}

func unsignedDailySimulationJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal token payload: %v", err)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(data) + ".sig"
}

func TestResolveDailySimulationCanonicalTesteeIDUsesCurrentTesteeID(t *testing.T) {
	state := &dailySimulationJourneyState{
		testee: &TesteeResponse{ID: "615508260325175854"},
	}

	canonicalID, err := resolveDailySimulationCanonicalTesteeID(context.Background(), state)
	if err != nil {
		t.Fatalf("resolve canonical testee id: %v", err)
	}
	if canonicalID != 615508260325175854 {
		t.Fatalf("unexpected canonical testee id: %d", canonicalID)
	}
}

func TestWaitForDailySimulationReadinessUsesCollectionContract(t *testing.T) {
	const (
		answerSheetID = "615984776595124782"
		testeeID      = "615969746222854702"
		assessmentID  = "615984705628090926"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/answersheets/" + answerSheetID + "/assessment-readiness":
			if got := r.URL.Query().Get("testee_id"); got != testeeID {
				t.Fatalf("unexpected testee_id %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"status":"ready","answersheet_id":"` + answerSheetID + `","assessment_id":"` + assessmentID + `"}}`))
			return
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	logger := log.New(log.NewOptions())
	apiClient := NewAPIClient(server.URL, "", logger)

	gotAssessmentID, err := waitForDailySimulationReadiness(
		context.Background(),
		apiClient,
		answerSheetID,
		parseID(testeeID),
	)
	if err != nil {
		t.Fatalf("wait for assessment: %v", err)
	}
	if gotAssessmentID != assessmentID {
		t.Fatalf("unexpected assessment id: got=%q want=%q", gotAssessmentID, assessmentID)
	}
}

func assertErr(message string) error {
	return testErr(message)
}

type testErr string

func (e testErr) Error() string { return string(e) }
