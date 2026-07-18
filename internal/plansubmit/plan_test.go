package plansubmit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FangcunMount/seeddata-runner/internal/seedconfig"
)

func TestNewPlanQuestionnaireVersionMismatchError(t *testing.T) {
	err := newPlanQuestionnaireVersionMismatchError("SAS-TEST", "QNR-001", "1.0.1", "6.0.1")
	if err == nil {
		t.Fatal("expected mismatch error")
	}

	msg := err.Error()
	for _, expected := range []string{
		"scale_code=SAS-TEST",
		"questionnaire_code=QNR-001",
		"requested_version=1.0.1",
		"loaded_version=6.0.1",
	} {
		if !strings.Contains(msg, expected) {
			t.Fatalf("expected error message to contain %q, got %q", expected, msg)
		}
	}
}

func TestNormalizePlanWorkers(t *testing.T) {
	tests := []struct {
		name      string
		workers   int
		testeeCnt int
		expected  int
	}{
		{name: "default to one", workers: 0, testeeCnt: 10, expected: 1},
		{name: "cap by task count", workers: 8, testeeCnt: 3, expected: 3},
		{name: "keep explicit worker count", workers: 4, testeeCnt: 10, expected: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizePlanWorkers(tt.workers, tt.testeeCnt); got != tt.expected {
				t.Fatalf("normalizePlanWorkers(%d, %d)=%d, want=%d", tt.workers, tt.testeeCnt, got, tt.expected)
			}
		})
	}
}

func TestOptionsFromConfigUsesPlanIDs(t *testing.T) {
	completionPercent := 60
	cfg := &seedconfig.Config{
		DailySimulation: seedconfig.DailySimulationConfig{
			TesteeSource: "daily_simulation",
		},
		PlanSubmit: seedconfig.PlanSubmitConfig{
			PlanIDs:           []seedconfig.FlexibleID{"614333603412718126", "614187067651404334", "614333603412718126"},
			Workers:           2,
			CompletionPercent: &completionPercent,
			IdleInterval:      "45s",
			ActiveInterval:    "10s",
		},
	}

	opts, err := optionsFromConfig(cfg)
	if err != nil {
		t.Fatalf("optionsFromConfig returned error: %v", err)
	}
	if len(opts.PlanIDs) != 2 {
		t.Fatalf("expected 2 normalized plan ids, got %#v", opts.PlanIDs)
	}
	if opts.PlanIDs[0] != "614333603412718126" || opts.PlanIDs[1] != "614187067651404334" {
		t.Fatalf("unexpected plan ids: %#v", opts.PlanIDs)
	}
	if opts.Workers != 2 {
		t.Fatalf("unexpected workers: %d", opts.Workers)
	}
	if opts.CompletionPercent != 60 {
		t.Fatalf("unexpected completion percent: %d", opts.CompletionPercent)
	}
	if opts.TesteeSource != "daily_simulation" {
		t.Fatalf("unexpected testee source: %q", opts.TesteeSource)
	}
	if opts.IdleInterval != 45*time.Second {
		t.Fatalf("unexpected idle interval: %s", opts.IdleInterval)
	}
	if opts.ActiveInterval != 10*time.Second {
		t.Fatalf("unexpected active interval: %s", opts.ActiveInterval)
	}
}

func TestPlanSubmitCycleStateMachineUsesActivityAwareIntervals(t *testing.T) {
	stateMachine := newPlanSubmitCycleStateMachine(planOpenTaskSubmitOptions{
		IdleInterval:   30 * time.Minute,
		ActiveInterval: time.Hour,
		Continuous:     true,
	})

	idleDecision := stateMachine.Next(&planOpenTaskSubmitStats{})
	if !idleDecision.Continue {
		t.Fatal("expected continuous state machine to continue after idle cycle")
	}
	if idleDecision.Active {
		t.Fatal("expected idle cycle to be inactive")
	}
	if idleDecision.SleepDuration != 30*time.Minute {
		t.Fatalf("unexpected idle delay: %s", idleDecision.SleepDuration)
	}

	openedDecision := stateMachine.Next(&planOpenTaskSubmitStats{OpenedCount: 1})
	if !openedDecision.Active {
		t.Fatal("expected opened tasks to mark cycle active")
	}
	if openedDecision.SleepDuration != time.Hour {
		t.Fatalf("unexpected active delay for opened tasks: %s", openedDecision.SleepDuration)
	}

	submittedDecision := stateMachine.Next(&planOpenTaskSubmitStats{SubmittedCount: 1})
	if !submittedDecision.Active {
		t.Fatal("expected submitted tasks to mark cycle active")
	}
	if submittedDecision.SleepDuration != time.Hour {
		t.Fatalf("unexpected active delay for submitted tasks: %s", submittedDecision.SleepDuration)
	}
}

func TestPlanSubmitCycleStateMachineStopsWhenNotContinuous(t *testing.T) {
	stateMachine := newPlanSubmitCycleStateMachine(planOpenTaskSubmitOptions{
		Continuous: false,
	})

	decision := stateMachine.Next(&planOpenTaskSubmitStats{OpenedCount: 1})
	if decision.Continue {
		t.Fatal("expected non-continuous state machine to stop after one cycle")
	}
	if decision.Active {
		t.Fatal("expected stop decision to leave active=false")
	}
	if decision.SleepDuration != 0 {
		t.Fatalf("expected stop decision to avoid sleeping, got %s", decision.SleepDuration)
	}
}

func TestPlanOpenTaskSubmitStatsMergeAndActivity(t *testing.T) {
	stats := newPlanOpenTaskSubmitStats()
	if stats.HasActivity() {
		t.Fatal("expected empty stats to have no activity")
	}

	stats.RecordOpenedCount(2)
	stats.RecordExecutionCounts(1, 1, 0)
	stats.RecordListLoadFailure()

	other := newPlanOpenTaskSubmitStats()
	other.RecordOpenedCount(3)
	other.RecordExecutionCounts(2, 0, 1)

	stats.Merge(other)

	if !stats.HasActivity() {
		t.Fatal("expected merged stats to report activity")
	}
	if stats.OpenedCount != 5 {
		t.Fatalf("expected merged opened_count=5, got %d", stats.OpenedCount)
	}
	if stats.SubmittedCount != 3 {
		t.Fatalf("expected merged submitted_count=3, got %d", stats.SubmittedCount)
	}
	if stats.SkippedCount != 1 {
		t.Fatalf("expected merged skipped_count=1, got %d", stats.SkippedCount)
	}
	if stats.FailedTaskListLoads != 1 {
		t.Fatalf("expected merged failed_task_list_loads=1, got %d", stats.FailedTaskListLoads)
	}
	if stats.FailedTaskExecutions != 1 {
		t.Fatalf("expected merged failed_task_executions=1, got %d", stats.FailedTaskExecutions)
	}
}

func TestBuildPlanTaskSubmitRequestIncludesTaskID(t *testing.T) {
	detail := &QuestionnaireDetailResponse{
		Code:    "QNR-001",
		Title:   "Test Questionnaire",
		Version: "1.0.0",
		Questions: []QuestionResponse{
			{
				Code:  "Q1",
				Type:  questionTypeRadio,
				Title: "Question 1",
				Options: []OptionResponse{
					{Code: "A", Content: "A", Score: 1},
					{Code: "B", Content: "B", Score: 2},
				},
			},
		},
	}

	req, err := buildPlanTaskSubmitRequest(
		"plan-1",
		detail,
		"1.0.0",
		TaskResponse{
			ID:       "2001",
			TesteeID: "1001",
			Status:   "opened",
		},
		false,
		newSeeddataLogger(false),
	)
	if err != nil {
		t.Fatalf("buildPlanTaskSubmitRequest returned error: %v", err)
	}
	if req == nil {
		t.Fatal("expected non-nil request")
	}
	if req.TaskID != "2001" {
		t.Fatalf("expected task_id=2001, got %q", req.TaskID)
	}
	if req.TesteeID != 1001 {
		t.Fatalf("expected testee_id=1001, got %d", req.TesteeID)
	}
}

func TestPlanTaskSubmitSessionFactoryOpensSessionWithoutLocalRuntimeConfig(t *testing.T) {
	const planID = "614333603412718126"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/testees":
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"items":     []any{},
				"page":      1,
				"page_size": 1,
			}})
		case "/api/v1/plans/" + planID:
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"id":         planID,
				"org_id":     1,
				"scale_code": "SAS-TEST",
				"status":     "active",
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	logger := newSeeddataLogger(false)
	deps := &dependencies{
		Logger: logger,
		Config: &SeedConfig{
			Global: GlobalConfig{OrgID: 1},
		},
		APIClient:        NewAPIClient(server.URL, "test-token", logger),
		CollectionClient: NewAPIClient(server.URL, "test-token", logger),
	}

	factory, err := newPlanTaskSubmitSessionFactory(context.Background(), deps)
	if err != nil {
		t.Fatalf("newPlanTaskSubmitSessionFactory returned error: %v", err)
	}

	session, err := factory.OpenSession(context.Background(), planID, false)
	if err != nil {
		t.Fatalf("factory.OpenSession returned error: %v", err)
	}
	if session == nil || session.plan == nil {
		t.Fatalf("expected non-nil submit session and plan")
	}
	if session.plan.ID != planID || session.plan.ScaleCode != "SAS-TEST" {
		t.Fatalf("unexpected loaded plan: %#v", session.plan)
	}
}

func TestPlanTaskSubmitSessionFactoryRejectsInactivePlan(t *testing.T) {
	const planID = "614333603412718126"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/testees":
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"items":     []any{},
				"page":      1,
				"page_size": 1,
			}})
		case "/api/v1/plans/" + planID:
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"id":         planID,
				"org_id":     1,
				"scale_code": "SAS-TEST",
				"status":     "completed",
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	logger := newSeeddataLogger(false)
	deps := &dependencies{
		Logger: logger,
		Config: &SeedConfig{
			Global: GlobalConfig{OrgID: 1},
		},
		APIClient:        NewAPIClient(server.URL, "test-token", logger),
		CollectionClient: NewAPIClient(server.URL, "test-token", logger),
	}

	factory, err := newPlanTaskSubmitSessionFactory(context.Background(), deps)
	if err != nil {
		t.Fatalf("newPlanTaskSubmitSessionFactory returned error: %v", err)
	}

	_, err = factory.OpenSession(context.Background(), planID, false)
	if err == nil {
		t.Fatal("expected inactive plan to be rejected")
	}
	if !strings.Contains(err.Error(), "is not active") {
		t.Fatalf("expected inactive-plan error, got %v", err)
	}
}

func TestPlanTaskSubmitQuestionnaireLoaderUsesAPIGateway(t *testing.T) {
	const planID = "614333603412718126"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/testees":
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"items":     []any{},
				"page":      1,
				"page_size": 1,
			}})
		case "/api/v1/plans/" + planID:
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"id":         planID,
				"org_id":     1,
				"scale_code": "SAS-TEST",
				"status":     "active",
			}})
		case "/api/v1/assessment-models/published/SAS-TEST":
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"code":                  "SAS-TEST",
				"questionnaire_code":    "QNR-TEST",
				"questionnaire_version": "1.0.1",
			}})
		case "/api/v1/questionnaires/QNR-TEST":
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"code":    "QNR-TEST",
				"title":   "Questionnaire",
				"version": "1.0.1",
				"questions": []any{
					map[string]any{"code": "q1", "type": "radio", "title": "Q1", "options": []any{
						map[string]any{"code": "a", "content": "A", "score": 1},
					}},
				},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	logger := newSeeddataLogger(false)
	deps := &dependencies{
		Logger: logger,
		Config: &SeedConfig{
			Global: GlobalConfig{OrgID: 1},
		},
		APIClient:        NewAPIClient(server.URL, "test-token", logger),
		CollectionClient: NewAPIClient(server.URL, "test-token", logger),
	}

	factory, err := newPlanTaskSubmitSessionFactory(context.Background(), deps)
	if err != nil {
		t.Fatalf("newPlanTaskSubmitSessionFactory returned error: %v", err)
	}
	session, err := factory.OpenSession(context.Background(), planID, false)
	if err != nil {
		t.Fatalf("factory.OpenSession returned error: %v", err)
	}
	loader, err := newPlanTaskSubmitQuestionnaireLoader(session)
	if err != nil {
		t.Fatalf("newPlanTaskSubmitQuestionnaireLoader returned error: %v", err)
	}

	scaleResp, detail, err := loader.Load(context.Background(), false)
	if err != nil {
		t.Fatalf("loader.Load returned error: %v", err)
	}
	if scaleResp == nil || scaleResp.QuestionnaireCode != "QNR-TEST" || scaleResp.QuestionnaireVersion != "1.0.1" {
		t.Fatalf("unexpected scale response: %#v", scaleResp)
	}
	if detail == nil || detail.Code != "QNR-TEST" || detail.Version != "1.0.1" {
		t.Fatalf("unexpected questionnaire detail: %#v", detail)
	}
}

func TestNewPlanTaskSubmitRunnerBuildsRunnerState(t *testing.T) {
	const planID = "614333603412718126"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/testees":
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"items":     []any{},
				"page":      1,
				"page_size": 1,
			}})
		case "/api/v1/plans/" + planID:
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"id":         planID,
				"org_id":     1,
				"scale_code": "SAS-TEST",
				"status":     "active",
			}})
		case "/api/v1/assessment-models/published/SAS-TEST":
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"code":                  "SAS-TEST",
				"questionnaire_code":    "QNR-TEST",
				"questionnaire_version": "1.0.1",
			}})
		case "/api/v1/questionnaires/QNR-TEST":
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"code":    "QNR-TEST",
				"title":   "Questionnaire",
				"version": "1.0.1",
				"questions": []any{
					map[string]any{"code": "q1", "type": "radio", "title": "Q1", "options": []any{
						map[string]any{"code": "a", "content": "A", "score": 1},
					}},
				},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	logger := newSeeddataLogger(false)
	deps := &dependencies{
		Logger: logger,
		Config: &SeedConfig{
			Global: GlobalConfig{OrgID: 1},
		},
		APIClient:        NewAPIClient(server.URL, "test-token", logger),
		CollectionClient: NewAPIClient(server.URL, "test-token", logger),
	}

	runner, err := newPlanTaskSubmitRunner(context.Background(), deps, planID, false)
	if err != nil {
		t.Fatalf("newPlanTaskSubmitRunner returned error: %v", err)
	}
	if runner.session == nil || runner.session.plan == nil {
		t.Fatal("expected runner session and plan to be initialized")
	}
	if runner.scaleResp == nil || runner.scaleResp.QuestionnaireCode != "QNR-TEST" {
		t.Fatalf("unexpected scale response: %#v", runner.scaleResp)
	}
	if runner.detail == nil || runner.detail.Version != "1.0.1" {
		t.Fatalf("unexpected questionnaire detail: %#v", runner.detail)
	}
	if runner.ledger == nil {
		t.Fatal("expected submission ledger to be initialized")
	}
}

func TestNewPlanTaskSubmitBootstrapLoadsSessionAndQuestionnaire(t *testing.T) {
	const planID = "614333603412718126"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/testees":
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"items":     []any{},
				"page":      1,
				"page_size": 1,
			}})
		case "/api/v1/plans/" + planID:
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"id":         planID,
				"org_id":     1,
				"scale_code": "SAS-TEST",
				"status":     "active",
			}})
		case "/api/v1/assessment-models/published/SAS-TEST":
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"code":                  "SAS-TEST",
				"questionnaire_code":    "QNR-TEST",
				"questionnaire_version": "1.0.1",
			}})
		case "/api/v1/questionnaires/QNR-TEST":
			_ = json.NewEncoder(w).Encode(Response{Code: 0, Message: "ok", Data: map[string]any{
				"code":    "QNR-TEST",
				"title":   "Questionnaire",
				"version": "1.0.1",
				"questions": []any{
					map[string]any{"code": "q1", "type": "radio", "title": "Q1", "options": []any{
						map[string]any{"code": "a", "content": "A", "score": 1},
					}},
				},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	logger := newSeeddataLogger(false)
	deps := &dependencies{
		Logger: logger,
		Config: &SeedConfig{
			Global: GlobalConfig{OrgID: 1},
		},
		APIClient:        NewAPIClient(server.URL, "test-token", logger),
		CollectionClient: NewAPIClient(server.URL, "test-token", logger),
	}

	bootstrap, err := newPlanTaskSubmitBootstrap(context.Background(), deps, planID, false)
	if err != nil {
		t.Fatalf("newPlanTaskSubmitBootstrap returned error: %v", err)
	}
	if bootstrap.session == nil || bootstrap.session.plan == nil {
		t.Fatal("expected bootstrap session and plan to be initialized")
	}
	if bootstrap.scaleResp == nil || bootstrap.scaleResp.QuestionnaireCode != "QNR-TEST" {
		t.Fatalf("unexpected bootstrap scale response: %#v", bootstrap.scaleResp)
	}
	if bootstrap.detail == nil || bootstrap.detail.Version != "1.0.1" {
		t.Fatalf("unexpected bootstrap questionnaire detail: %#v", bootstrap.detail)
	}
}

type pagedPlanTaskSubmitGatewayStub struct {
	taskLists           map[string][]TaskResponse
	testeeSources       map[string]string
	testeeLookupErrors  map[string]error
	getTesteeByIDCalls  []string
	listTaskWindowCalls []ListPlanTaskWindowRequest
}

type planTaskSubmitGatewayStubWithError struct {
	err error
}

func (s *pagedPlanTaskSubmitGatewayStub) GetPlan(ctx context.Context, planID string) (*PlanResponse, error) {
	return nil, nil
}

func (s *pagedPlanTaskSubmitGatewayStub) GetPublishedAssessmentModel(ctx context.Context, code, version string) (*PublishedAssessmentModelResponse, error) {
	return nil, nil
}

func (s *pagedPlanTaskSubmitGatewayStub) GetPublishedQuestionnaire(ctx context.Context, code, version string) (*QuestionnaireDetailResponse, error) {
	return nil, nil
}

func (s *pagedPlanTaskSubmitGatewayStub) GetTesteeByID(ctx context.Context, testeeID string) (*ApiserverTesteeResponse, error) {
	testeeID = strings.TrimSpace(testeeID)
	s.getTesteeByIDCalls = append(s.getTesteeByIDCalls, testeeID)
	if s.testeeLookupErrors != nil {
		if err := s.testeeLookupErrors[testeeID]; err != nil {
			return nil, err
		}
	}
	source := "daily_simulation"
	if s.testeeSources != nil {
		source = s.testeeSources[testeeID]
	}
	return &ApiserverTesteeResponse{ID: testeeID, Source: source}, nil
}

func (s *pagedPlanTaskSubmitGatewayStub) ListPlanTaskWindow(ctx context.Context, req ListPlanTaskWindowRequest) (*PlanTaskWindowResponse, error) {
	s.listTaskWindowCalls = append(s.listTaskWindowCalls, req)
	return buildStubTaskWindowResponse(s.taskLists, req), nil
}

func (s *planTaskSubmitGatewayStubWithError) GetPlan(ctx context.Context, planID string) (*PlanResponse, error) {
	return nil, nil
}

func (s *planTaskSubmitGatewayStubWithError) GetPublishedAssessmentModel(ctx context.Context, code, version string) (*PublishedAssessmentModelResponse, error) {
	return nil, nil
}

func (s *planTaskSubmitGatewayStubWithError) GetPublishedQuestionnaire(ctx context.Context, code, version string) (*QuestionnaireDetailResponse, error) {
	return nil, nil
}

func (s *planTaskSubmitGatewayStubWithError) GetTesteeByID(ctx context.Context, testeeID string) (*ApiserverTesteeResponse, error) {
	return nil, s.err
}

func (s *planTaskSubmitGatewayStubWithError) ListPlanTaskWindow(ctx context.Context, req ListPlanTaskWindowRequest) (*PlanTaskWindowResponse, error) {
	return nil, s.err
}

func buildStubTaskWindowResponse(taskLists map[string][]TaskResponse, req ListPlanTaskWindowRequest) *PlanTaskWindowResponse {
	if taskLists == nil {
		return &PlanTaskWindowResponse{
			Page:     max(req.Page, 1),
			PageSize: max(req.PageSize, 1),
		}
	}

	allTasks := make([]TaskResponse, 0)
	for testeeID, items := range taskLists {
		for _, task := range items {
			if strings.TrimSpace(task.TesteeID) == "" {
				task.TesteeID = testeeID
			}
			if planID := strings.TrimSpace(req.PlanID); planID != "" && strings.TrimSpace(task.PlanID) != "" && strings.TrimSpace(task.PlanID) != planID {
				continue
			}
			if status := normalizeTaskStatus(req.Status); status != "" && normalizeTaskStatus(task.Status) != status {
				continue
			}
			plannedAt := strings.TrimSpace(task.PlannedAt)
			if after := strings.TrimSpace(req.PlannedAfter); after != "" && plannedAt != "" && plannedAt < after {
				continue
			}
			if before := strings.TrimSpace(req.PlannedBefore); before != "" && plannedAt != "" && plannedAt > before {
				continue
			}
			allTasks = append(allTasks, task)
		}
	}

	sort.SliceStable(allTasks, func(i, j int) bool {
		if allTasks[i].TesteeID != allTasks[j].TesteeID {
			return allTasks[i].TesteeID < allTasks[j].TesteeID
		}
		if allTasks[i].Seq != allTasks[j].Seq {
			return allTasks[i].Seq < allTasks[j].Seq
		}
		return allTasks[i].ID < allTasks[j].ID
	})

	page := req.Page
	if page <= 0 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = len(allTasks)
		if pageSize == 0 {
			pageSize = 1
		}
	}
	start := (page - 1) * pageSize
	if start > len(allTasks) {
		start = len(allTasks)
	}
	end := start + pageSize
	if end > len(allTasks) {
		end = len(allTasks)
	}

	return &PlanTaskWindowResponse{
		Tasks:    append([]TaskResponse(nil), allTasks[start:end]...),
		Page:     page,
		PageSize: pageSize,
		HasMore:  end < len(allTasks),
	}
}

func TestListOpenPlanTaskJobsUsesPagedOpenedTasks(t *testing.T) {
	planID := "614333603412718126"
	testeeTasks := make([]TaskResponse, 0, planOpenTaskPageSize+1)
	plannedAt := time.Now().Format("2006-01-02 15:04:05")
	for idx := 0; idx < planOpenTaskPageSize+1; idx++ {
		testeeTasks = append(testeeTasks, TaskResponse{
			ID:        "task-" + strconv.Itoa(idx+1),
			PlanID:    planID,
			TesteeID:  "1001",
			Seq:       idx + 1,
			Status:    "opened",
			PlannedAt: plannedAt,
		})
	}
	testeeTasks[0].ID = "2001"
	testeeTasks[len(testeeTasks)-1].ID = "2201"
	gateway := &pagedPlanTaskSubmitGatewayStub{
		taskLists: map[string][]TaskResponse{
			"1001": testeeTasks,
			"1002": {
				{ID: "2301", PlanID: planID, TesteeID: "1002", Seq: 1, Status: "completed"},
			},
		},
	}

	jobs, err := listOpenPlanTaskJobs(context.Background(), gateway, newSeeddataLogger(false), planID, false)
	if err != nil {
		t.Fatalf("unexpected collect error: %v", err)
	}
	if len(gateway.listTaskWindowCalls) != 2 {
		t.Fatalf("expected 2 paged opened-task calls, got %d", len(gateway.listTaskWindowCalls))
	}
	if len(jobs) != planOpenTaskPageSize+1 {
		t.Fatalf("expected %d opened jobs, got %d", planOpenTaskPageSize+1, len(jobs))
	}
	if jobs[0].task.ID != "2001" || jobs[len(jobs)-1].task.ID != "2201" {
		t.Fatalf("unexpected opened jobs boundary ids: first=%s last=%s", jobs[0].task.ID, jobs[len(jobs)-1].task.ID)
	}
}

func TestListDailyPlanTaskJobsRespectsCompletionPercentQuota(t *testing.T) {
	planID := "614333603412718126"
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.Local)
	gateway := &pagedPlanTaskSubmitGatewayStub{
		taskLists: map[string][]TaskResponse{
			"1001": {
				{ID: "2001", PlanID: planID, TesteeID: "1001", Seq: 1, Status: "completed", PlannedAt: "2026-05-06 09:00:00"},
			},
			"1002": {
				{ID: "2002", PlanID: planID, TesteeID: "1002", Seq: 2, Status: "opened", PlannedAt: "2026-05-06 10:00:00"},
			},
			"1003": {
				{ID: "2003", PlanID: planID, TesteeID: "1003", Seq: 3, Status: "opened", PlannedAt: "2026-05-06 11:00:00"},
			},
			"1004": {
				{ID: "2004", PlanID: planID, TesteeID: "1004", Seq: 4, Status: "opened", PlannedAt: "2026-05-07 09:00:00"},
			},
		},
	}

	jobs, err := listDailyPlanTaskJobs(context.Background(), gateway, newSeeddataLogger(false), planID, 50, "daily_simulation", now, false)
	if err != nil {
		t.Fatalf("unexpected list error: %v", err)
	}
	if len(gateway.listTaskWindowCalls) != 1 {
		t.Fatalf("expected one task-window call, got %d", len(gateway.listTaskWindowCalls))
	}
	if got := gateway.listTaskWindowCalls[0].PlannedAfter; got != "2026-05-06 00:00:00" {
		t.Fatalf("unexpected planned_after: %q", got)
	}
	if got := gateway.listTaskWindowCalls[0].PlannedBefore; got != "2026-05-06 23:59:59" {
		t.Fatalf("unexpected planned_before: %q", got)
	}
	if got := gateway.listTaskWindowCalls[0].Status; got != "" {
		t.Fatalf("expected all task statuses within the daily window, got %q", got)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one opened job after quota, got %d", len(jobs))
	}
	if jobs[0].task.ID != "2002" {
		t.Fatalf("expected earliest opened task 2002, got %s", jobs[0].task.ID)
	}
}

func TestListDailyPlanTaskJobsSkipsWhenDailyTargetAlreadyMet(t *testing.T) {
	planID := "614333603412718126"
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.Local)
	gateway := &pagedPlanTaskSubmitGatewayStub{
		taskLists: map[string][]TaskResponse{
			"1001": {
				{ID: "2001", PlanID: planID, TesteeID: "1001", Seq: 1, Status: "completed", PlannedAt: "2026-05-06 09:00:00"},
			},
			"1002": {
				{ID: "2002", PlanID: planID, TesteeID: "1002", Seq: 2, Status: "opened", PlannedAt: "2026-05-06 10:00:00"},
			},
		},
	}

	jobs, err := listDailyPlanTaskJobs(context.Background(), gateway, newSeeddataLogger(false), planID, 50, "daily_simulation", now, false)
	if err != nil {
		t.Fatalf("unexpected list error: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no jobs after target is met, got %d", len(jobs))
	}
}

func TestListDailyPlanTaskJobsOnlyIncludesSeeddataTestees(t *testing.T) {
	planID := "614333603412718126"
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.Local)
	gateway := &pagedPlanTaskSubmitGatewayStub{
		taskLists: map[string][]TaskResponse{
			"1001": {
				{ID: "2001", PlanID: planID, TesteeID: "1001", Seq: 1, Status: "opened", PlannedAt: "2026-05-06 09:00:00"},
			},
			"1002": {
				{ID: "2002", PlanID: planID, TesteeID: "1002", Seq: 2, Status: "opened", PlannedAt: "2026-05-06 10:00:00"},
			},
			"1003": {
				{ID: "2003", PlanID: planID, TesteeID: "1003", Seq: 3, Status: "opened", PlannedAt: "2026-05-06 11:00:00"},
			},
		},
		testeeSources: map[string]string{
			"1001": "daily_simulation",
			"1002": "manual",
			"1003": "daily_simulation",
		},
	}

	jobs, err := listDailyPlanTaskJobs(context.Background(), gateway, newSeeddataLogger(false), planID, 100, "daily_simulation", now, false)
	if err != nil {
		t.Fatalf("unexpected list error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected two seeddata jobs, got %d", len(jobs))
	}
	if jobs[0].task.ID != "2001" || jobs[1].task.ID != "2003" {
		t.Fatalf("unexpected seeddata task ids: %s, %s", jobs[0].task.ID, jobs[1].task.ID)
	}
}

func TestListDailyPlanTaskJobsSkipsUnverifiedTesteeSource(t *testing.T) {
	planID := "614333603412718126"
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.Local)
	gateway := &pagedPlanTaskSubmitGatewayStub{
		taskLists: map[string][]TaskResponse{
			"1001": {
				{ID: "2001", PlanID: planID, TesteeID: "1001", Seq: 1, Status: "opened", PlannedAt: "2026-05-06 09:00:00"},
			},
			"1002": {
				{ID: "2002", PlanID: planID, TesteeID: "1002", Seq: 2, Status: "opened", PlannedAt: "2026-05-06 10:00:00"},
			},
		},
		testeeSources: map[string]string{
			"1002": "daily_simulation",
		},
		testeeLookupErrors: map[string]error{
			"1001": errors.New("lookup failed"),
		},
	}

	jobs, err := listDailyPlanTaskJobs(context.Background(), gateway, newSeeddataLogger(false), planID, 100, "daily_simulation", now, false)
	if err != nil {
		t.Fatalf("unexpected list error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected only verified seeddata job, got %d", len(jobs))
	}
	if jobs[0].task.ID != "2002" {
		t.Fatalf("expected task_id=2002, got %s", jobs[0].task.ID)
	}
}

func TestRunPlanSubmitOpenTasksCycleSubmitsOnlyFreshOpenedTasks(t *testing.T) {
	planID := "614333603412718126"
	plannedAt := time.Now().Format("2006-01-02 15:04:05")
	gateway := &pagedPlanTaskSubmitGatewayStub{
		taskLists: map[string][]TaskResponse{
			"1001": {
				{ID: "2001", PlanID: planID, TesteeID: "1001", Seq: 1, Status: "opened", PlannedAt: plannedAt},
			},
			"1002": {
				{ID: "2002", PlanID: planID, TesteeID: "1002", Seq: 1, Status: "opened", PlannedAt: plannedAt},
			},
		},
	}
	submitClient := &adminAnswerSheetSubmitClientStub{}

	detail := &QuestionnaireDetailResponse{
		Code:    "QNR-001",
		Title:   "Test Questionnaire",
		Version: "1.0.0",
		Questions: []QuestionResponse{
			{
				Code:  "Q1",
				Type:  questionTypeRadio,
				Title: "Question 1",
				Options: []OptionResponse{
					{Code: "A", Content: "A", Score: 1},
				},
			},
		},
	}
	ledger, err := NewSubmissionLedger(filepath.Join(t.TempDir(), "plan-submissions.json"), "plan")
	if err != nil {
		t.Fatal(err)
	}
	completedReq, err := buildPlanTaskSubmitRequest(
		planID, detail, "1.0.0",
		TaskResponse{ID: "2002", PlanID: planID, TesteeID: "1002", Status: "opened"},
		false, newSeeddataLogger(false),
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := ledger.Prepare("plan|"+planID+"|2002|1002", *completedReq)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.MarkCompleted(prepared.Record.LogicalID, "existing-2002"); err != nil {
		t.Fatal(err)
	}

	cycle := newPlanTaskSubmitCycle(
		gateway,
		submitClient,
		newSeeddataLogger(false),
		planID,
		"SAS-TEST",
		"1.0.0",
		detail,
		2,
		100,
		"daily_simulation",
		ledger,
		false,
	)
	stats, err := cycle.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected cycle error: %v", err)
	}
	if stats.OpenedCount != 2 {
		t.Fatalf("expected opened_count=2, got %+v", stats)
	}
	if stats.SubmittedCount != 1 {
		t.Fatalf("expected submitted_count=1, got %+v", stats)
	}
	if stats.SkippedCount != 1 {
		t.Fatalf("expected skipped_count=1, got %+v", stats)
	}
	if stats.FailedTaskExecutions != 0 {
		t.Fatalf("expected no failed task executions, got %+v", stats)
	}
	if submitClient.withPolicyCalls != 1 {
		t.Fatalf("expected one submit call, got %d", submitClient.withPolicyCalls)
	}
	if submitClient.lastPolicyReq.TaskID != "2001" {
		t.Fatalf("expected task_id=2001 to be submitted, got %+v", submitClient.lastPolicyReq)
	}
}

func TestPlanTaskSubmitCycleRecordsListLoadFailure(t *testing.T) {
	planID := "614333603412718126"
	gateway := &planTaskSubmitGatewayStubWithError{err: errors.New("list tasks failed")}

	cycle := newPlanTaskSubmitCycle(
		gateway,
		&adminAnswerSheetSubmitClientStub{},
		newSeeddataLogger(false),
		planID,
		"SAS-TEST",
		"1.0.0",
		&QuestionnaireDetailResponse{Code: "QNR-001", Version: "1.0.0"},
		1,
		100,
		"daily_simulation",
		nil,
		false,
	)

	stats, err := cycle.Execute(context.Background())
	if err == nil {
		t.Fatal("expected cycle to return list error")
	}
	if stats == nil {
		t.Fatal("expected stats on list error")
	}
	if stats.FailedTaskListLoads != 1 {
		t.Fatalf("expected failed_task_list_loads=1, got %+v", stats)
	}
}
