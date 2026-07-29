package dailysim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	toolanswersheet "github.com/FangcunMount/seeddata-runner/internal/answersheet"
	"github.com/FangcunMount/seeddata-runner/internal/historicalseed"
)

func TestSubmitDailySimulationAnswerSheetUsesCollectionAndReadiness(t *testing.T) {
	var readinessCalls atomic.Int32
	var waited time.Duration
	originalWait := dailySimulationReadinessWait
	dailySimulationReadinessWait = func(_ context.Context, delay time.Duration) error {
		waited = delay
		return nil
	}
	t.Cleanup(func() { dailySimulationReadinessWait = originalWait })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets":
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"total":0,"items":[]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/answersheets":
			var body struct {
				IdempotencyKey string `json:"idempotency_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.IdempotencyKey == "" || r.Header.Get("X-Request-ID") == "" {
				t.Fatalf("submission identity missing: body=%+v request_id=%q", body, r.Header.Get("X-Request-ID"))
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"code":0,"message":"accepted","data":{"status":"accepted","request_id":"request-1","answersheet_id":"9001"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets/9001/assessment-readiness":
			if readinessCalls.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"status":"pending","answersheet_id":"9001","next_poll_after_ms":2400}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"status":"ready","answersheet_id":"9001","assessment_id":"8001"}}`))
		default:
			t.Fatalf("unexpected daily submission request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	ledger, err := toolanswersheet.NewSubmissionLedger(filepath.Join(t.TempDir(), "daily-submissions.json"), "daily")
	if err != nil {
		t.Fatal(err)
	}
	logger := log.New(log.NewOptions())
	client := NewAPIClient(server.URL, "guardian-token", logger)
	state := &dailySimulationJourneyState{
		deps: &dependencies{
			APIClient:             NewAPIClient(server.URL, "admin-token", logger),
			CollectionClient:      client,
			DailySubmissionLedger: ledger,
		},
		collectionClient: client,
		guardianUserID:   "7001",
		profile: dailySimulationProfile{
			RunDate: time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC), Index: 1,
		},
		target: &dailySimulationResolvedTarget{
			TargetType: "scale", TargetCode: "MODEL", TargetVersion: "1",
			QuestionnaireCode: "Q", QuestionnaireVersion: "1", RequiresAssessment: true,
		},
	}
	req := SubmitAnswerSheetRequest{
		QuestionnaireCode: "Q", QuestionnaireVersion: "1", TesteeID: 42,
		Answers: []Answer{{QuestionCode: "q1", QuestionType: "Radio", Value: "A"}},
	}
	result, err := submitDailySimulationAnswerSheet(context.Background(), state, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.AnswerSheetID != "9001" || result.AssessmentID != "8001" || result.Status != dailySimulationSubmissionAssessmentReady {
		t.Fatalf("unexpected submission result: %+v", result)
	}
	if waited != 2400*time.Millisecond {
		t.Fatalf("readiness wait=%s, want 2.4s", waited)
	}
}

func TestDailySimulationReadinessDelayClampsServerHint(t *testing.T) {
	if got := dailySimulationReadinessDelay(0); got != 2*time.Second {
		t.Fatalf("default delay=%s", got)
	}
	if got := dailySimulationReadinessDelay(1); got != 250*time.Millisecond {
		t.Fatalf("minimum delay=%s", got)
	}
	if got := dailySimulationReadinessDelay(60_000); got != 10*time.Second {
		t.Fatalf("maximum delay=%s", got)
	}
}

func TestWaitForDailySimulationReportRequiresInterpretedTerminal(t *testing.T) {
	var calls atomic.Int32
	originalWait := dailySimulationReadinessWait
	dailySimulationReadinessWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { dailySimulationReadinessWait = originalWait })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/assessments/assessment-1/wait-report" {
			t.Fatalf("unexpected report request: %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"processing","next_poll_after_ms":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"status":"interpreted"}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "guardian-token", log.New(log.NewOptions()))
	if err := waitForDailySimulationReport(context.Background(), client, "assessment-1", 42); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("report calls=%d, want 2", calls.Load())
	}
}

func TestDailySimulationSubmissionLogicalIDPreservesLegacyWithoutTask(t *testing.T) {
	state := &dailySimulationJourneyState{
		profile: dailySimulationProfile{RunDate: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Index: 7},
		target: &dailySimulationResolvedTarget{
			TargetType: "scale", TargetCode: "MODEL", TargetVersion: "1",
			QuestionnaireCode: "Q", QuestionnaireVersion: "2",
		},
	}
	legacy := "daily|20250101|7|42|scale|MODEL|1|Q|2"
	if got := dailySimulationSubmissionLogicalID(state, 42, ""); got != legacy {
		t.Fatalf("logical id=%q, want legacy %q", got, legacy)
	}
	if got := dailySimulationSubmissionLogicalID(state, 42, "task-1"); got != legacy+"|task-1" {
		t.Fatalf("task logical id=%q", got)
	}
	state.submissionOriginRef = &OriginRef{Type: "self_service"}
	if got := dailySimulationSubmissionLogicalID(state, 42, ""); got != legacy+"|origin:self_service:v1" {
		t.Fatalf("self-service logical id=%q", got)
	}
}

func TestDailySimulationSubmissionOriginRef(t *testing.T) {
	state := &dailySimulationJourneyState{entry: &AssessmentEntryResponse{ID: " entry-1 "}}

	if got := dailySimulationSubmissionOriginRef(state, ""); got.Type != "assessment_entry" || got.ID != "entry-1" {
		t.Fatalf("primary origin=%+v", got)
	}
	state.submissionOriginRef = &OriginRef{Type: " self_service "}
	if got := dailySimulationSubmissionOriginRef(state, ""); got.Type != "self_service" || got.ID != "" {
		t.Fatalf("additional origin=%+v", got)
	}
	if got := dailySimulationSubmissionOriginRef(state, " task-1 "); got.Type != "plan_task" || got.ID != "task-1" {
		t.Fatalf("plan-task origin=%+v", got)
	}
}

func TestDailySimulationReadinessTimeoutPersistsAcceptedPending(t *testing.T) {
	current := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	originalNow := dailySimulationReadinessNow
	originalWait := dailySimulationReadinessWait
	dailySimulationReadinessNow = func() time.Time { return current }
	dailySimulationReadinessWait = func(_ context.Context, _ time.Duration) error {
		current = current.Add(seedAssessmentPollTimeout + time.Second)
		return nil
	}
	t.Cleanup(func() {
		dailySimulationReadinessNow = originalNow
		dailySimulationReadinessWait = originalWait
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets":
			_, _ = w.Write([]byte(`{"code":0,"data":{"total":0,"items":[]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/answersheets":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"accepted","answersheet_id":"9002"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets/9002/assessment-readiness":
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"pending","answersheet_id":"9002"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	ledger, err := toolanswersheet.NewSubmissionLedger(filepath.Join(t.TempDir(), "daily-submissions.json"), "daily")
	if err != nil {
		t.Fatal(err)
	}
	logger := log.New(log.NewOptions())
	client := NewAPIClient(server.URL, "guardian-token", logger)
	state := &dailySimulationJourneyState{
		deps: &dependencies{
			APIClient:             NewAPIClient(server.URL, "admin-token", logger),
			CollectionClient:      client,
			DailySubmissionLedger: ledger,
		},
		collectionClient: client,
		guardianUserID:   "7001",
		profile: dailySimulationProfile{
			RunDate: current, Index: 2,
		},
		target: &dailySimulationResolvedTarget{
			TargetType: "scale", TargetCode: "MODEL", TargetVersion: "1",
			QuestionnaireCode: "Q", QuestionnaireVersion: "1", RequiresAssessment: true,
		},
	}
	req := SubmitAnswerSheetRequest{
		QuestionnaireCode: "Q", QuestionnaireVersion: "1", TesteeID: 42,
		Answers: []Answer{{QuestionCode: "q1", QuestionType: "Radio", Value: "A"}},
	}
	result, err := submitDailySimulationAnswerSheet(context.Background(), state, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != dailySimulationSubmissionAcceptedPending {
		t.Fatalf("unexpected pending result: %+v", result)
	}
	record, ok, err := ledger.Get(dailySimulationSubmissionLogicalID(state, req.TesteeID, req.TaskID))
	if err != nil || !ok {
		t.Fatalf("load pending submission: ok=%v err=%v", ok, err)
	}
	if record.Status != toolanswersheet.SubmissionStatusAcceptedPending || record.AnswerSheetID != "9002" {
		t.Fatalf("unexpected timeout record: %+v", record)
	}
}

func TestHistoricalDailySimulationReadinessTimeoutStopsTheDay(t *testing.T) {
	current := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	originalNow := dailySimulationReadinessNow
	originalWait := dailySimulationReadinessWait
	dailySimulationReadinessNow = func() time.Time { return current }
	dailySimulationReadinessWait = func(_ context.Context, _ time.Duration) error {
		current = current.Add(seedAssessmentPollTimeout + time.Second)
		return nil
	}
	t.Cleanup(func() {
		dailySimulationReadinessNow = originalNow
		dailySimulationReadinessWait = originalWait
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/answersheets":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"accepted","answersheet_id":"9003"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets/9003/assessment-readiness":
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"pending","answersheet_id":"9003"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	ledger, err := toolanswersheet.NewSubmissionLedger(filepath.Join(t.TempDir(), "daily-submissions.json"), "daily")
	if err != nil {
		t.Fatal(err)
	}
	logger := log.New(log.NewOptions())
	client := NewAPIClient(server.URL, "guardian-token", logger)
	state := &dailySimulationJourneyState{
		deps:             &dependencies{APIClient: NewAPIClient(server.URL, "admin-token", logger), CollectionClient: client, DailySubmissionLedger: ledger},
		collectionClient: client,
		profile:          dailySimulationProfile{RunDate: current, Index: 3},
		target:           &dailySimulationResolvedTarget{TargetType: "scale", TargetCode: "MODEL", TargetVersion: "1", QuestionnaireCode: "Q", QuestionnaireVersion: "1", RequiresAssessment: true},
	}
	req := SubmitAnswerSheetRequest{QuestionnaireCode: "Q", QuestionnaireVersion: "1", TesteeID: 42, Answers: []Answer{{QuestionCode: "q1", QuestionType: "Radio", Value: "A"}}}
	ctx := historicalseed.WithContext(context.Background(), historicalseed.Context{BatchID: "batch", ScenarioID: "2025-01-01/3/submit_answer/MODEL", OrgID: 1, Version: historicalseed.Version1})
	result, err := submitDailySimulationAnswerSheet(ctx, state, req)
	if !errors.Is(err, errHistoricalSubmissionPending) {
		t.Fatalf("err=%v", err)
	}
	if result.Status != dailySimulationSubmissionAcceptedPending || result.AnswerSheetID != "9003" {
		t.Fatalf("unexpected pending result: %+v", result)
	}
	record, ok, getErr := ledger.Get(dailySimulationSubmissionLogicalID(state, req.TesteeID, req.TaskID))
	if getErr != nil || !ok || record.Status != toolanswersheet.SubmissionStatusAcceptedPending {
		t.Fatalf("pending ledger record=%+v ok=%v err=%v", record, ok, getErr)
	}
}

func TestVerifyHistoricalSubmissionStagesUsesServerFactsAndSlashSafeScenarioQuery(t *testing.T) {
	scenarioID := "2025-01-01/3/submit_answer/task-1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/historical-seed/batches/batch/scenarios" || r.URL.Query().Get("scenario_id") != scenarioID {
			t.Fatalf("unexpected stage query %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"batch_id":"batch","stages":[
			{"stage":"task_open","status":"completed","resource_id":"task-1"},
			{"stage":"answersheet_submit","status":"completed","resource_id":"answer-1"},
			{"stage":"assessment_created","status":"completed","resource_id":"assessment-1"},
			{"stage":"assessment_submitted","status":"completed","resource_id":"assessment-1"},
			{"stage":"task_complete","status":"completed","resource_id":"task-1"},
			{"stage":"outcome_committed","status":"completed","resource_id":"outcome-1"},
			{"stage":"report_generated","status":"completed","resource_id":"report-1"}
		]}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "admin-token", log.New(log.NewOptions()))
	historical := historicalseed.Context{BatchID: "batch", ScenarioID: scenarioID, OrgID: 9, Version: historicalseed.Version1}
	result, err := verifyHistoricalSubmissionStages(context.Background(), client, historical, "task-1", true, dailySimulationSubmissionResult{AnswerSheetID: "answer-1", AssessmentID: "assessment-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != dailySimulationSubmissionReportGenerated || result.OutcomeID != "outcome-1" || result.ReportID != "report-1" || len(result.ServerStages) != 7 {
		t.Fatalf("verified result=%+v", result)
	}
}
