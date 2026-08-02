package dailysim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	toolanswersheet "github.com/FangcunMount/seeddata-runner/internal/answersheet"
)

func TestSubmitDailySimulationAnswerSheetUsesCollectionAndReadiness(t *testing.T) {
	var readinessCalls atomic.Int32
	var reportCalls atomic.Int32
	var waited []time.Duration
	originalWait := dailySimulationReadinessWait
	dailySimulationReadinessWait = func(_ context.Context, delay time.Duration) error {
		waited = append(waited, delay)
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/assessments/8001/wait-report":
			if reportCalls.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"code":0,"data":{"status":"processing","next_poll_after_ms":500}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"interpreted"}}`))
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
	if err := submitDailySimulationAnswerSheet(context.Background(), state, req); err != nil {
		t.Fatal(err)
	}
	if state.outcome.AnswerSheetID != "9001" || state.outcome.AssessmentID != "8001" || state.outcome.ReportStatus != "interpreted" {
		t.Fatalf("unexpected daily outcome: %+v", state.outcome)
	}
	if len(waited) != 2 || waited[0] != 2400*time.Millisecond || waited[1] != 500*time.Millisecond {
		t.Fatalf("poll waits=%v", waited)
	}
	record, ok, err := ledger.Get(dailySimulationSubmissionLogicalID(state, req.TesteeID, req.TaskID))
	if err != nil || !ok || record.Status != toolanswersheet.SubmissionStatusReady || record.AssessmentID != "8001" {
		t.Fatalf("ready ledger record=%+v ok=%v err=%v", record, ok, err)
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
	if err := submitDailySimulationAnswerSheet(context.Background(), state, req); !errors.Is(err, errDailySimulationAssessmentPending) {
		t.Fatalf("expected retryable readiness error, got %v", err)
	}
	record, ok, err := ledger.Get(dailySimulationSubmissionLogicalID(state, req.TesteeID, req.TaskID))
	if err != nil || !ok {
		t.Fatalf("load pending submission: ok=%v err=%v", ok, err)
	}
	if record.Status != toolanswersheet.SubmissionStatusAcceptedPending || record.AnswerSheetID != "9002" {
		t.Fatalf("unexpected timeout record: %+v", record)
	}
}

func TestDailySimulationRetryFromAcceptedPendingDoesNotSubmitAgain(t *testing.T) {
	current := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	originalNow := dailySimulationReadinessNow
	originalWait := dailySimulationReadinessWait
	dailySimulationReadinessNow = func() time.Time { return current }
	dailySimulationReadinessWait = func(context.Context, time.Duration) error {
		current = current.Add(seedAssessmentPollTimeout + time.Second)
		return nil
	}
	t.Cleanup(func() {
		dailySimulationReadinessNow = originalNow
		dailySimulationReadinessWait = originalWait
	})

	var postCalls atomic.Int32
	var ready atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets":
			_, _ = w.Write([]byte(`{"code":0,"data":{"total":0,"items":[]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/answersheets":
			postCalls.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"accepted","answersheet_id":"answer-retry"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets/answer-retry/assessment-readiness":
			if !ready.Load() {
				_, _ = w.Write([]byte(`{"code":0,"data":{"status":"pending"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"ready","assessment_id":"assessment-retry"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/assessments/assessment-retry/wait-report":
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"interpreted"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	state, ledger := newDailySubmissionTestState(t, server.URL, true, 3)
	req := dailySubmissionTestRequest()
	if err := submitDailySimulationAnswerSheet(context.Background(), state, req); !errors.Is(err, errDailySimulationAssessmentPending) {
		t.Fatalf("first attempt error=%v", err)
	}
	ready.Store(true)
	if err := submitDailySimulationAnswerSheet(context.Background(), state, req); err != nil {
		t.Fatal(err)
	}
	if postCalls.Load() != 1 {
		t.Fatalf("answer sheet POST calls=%d, want 1", postCalls.Load())
	}
	record, ok, err := ledger.Get(dailySimulationSubmissionLogicalID(state, req.TesteeID, req.TaskID))
	if err != nil || !ok || record.Status != toolanswersheet.SubmissionStatusReady || record.AssessmentID != "assessment-retry" {
		t.Fatalf("ready ledger record=%+v ok=%v err=%v", record, ok, err)
	}
}

func TestDailySimulationReportTimeoutRetainsReadyAndRetriesWithoutSubmit(t *testing.T) {
	current := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	originalNow := dailySimulationReadinessNow
	originalWait := dailySimulationReadinessWait
	dailySimulationReadinessNow = func() time.Time { return current }
	dailySimulationReadinessWait = func(context.Context, time.Duration) error {
		current = current.Add(seedAssessmentPollTimeout + time.Second)
		return nil
	}
	t.Cleanup(func() {
		dailySimulationReadinessNow = originalNow
		dailySimulationReadinessWait = originalWait
	})

	var postCalls atomic.Int32
	var readinessCalls atomic.Int32
	var interpreted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets":
			_, _ = w.Write([]byte(`{"code":0,"data":{"total":0,"items":[]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/answersheets":
			postCalls.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"accepted","answersheet_id":"answer-report"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets/answer-report/assessment-readiness":
			readinessCalls.Add(1)
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"ready","assessment_id":"assessment-report"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/assessments/assessment-report/wait-report":
			if interpreted.Load() {
				_, _ = w.Write([]byte(`{"code":0,"data":{"status":"interpreted"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"processing"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	state, ledger := newDailySubmissionTestState(t, server.URL, true, 4)
	req := dailySubmissionTestRequest()
	if err := submitDailySimulationAnswerSheet(context.Background(), state, req); !errors.Is(err, errDailySimulationReportPending) {
		t.Fatalf("first attempt error=%v", err)
	}
	record, ok, err := ledger.Get(dailySimulationSubmissionLogicalID(state, req.TesteeID, req.TaskID))
	if err != nil || !ok || record.Status != toolanswersheet.SubmissionStatusReady || record.AssessmentID != "assessment-report" {
		t.Fatalf("ready ledger record=%+v ok=%v err=%v", record, ok, err)
	}
	interpreted.Store(true)
	if err := submitDailySimulationAnswerSheet(context.Background(), state, req); err != nil {
		t.Fatal(err)
	}
	if postCalls.Load() != 1 || readinessCalls.Load() != 1 {
		t.Fatalf("post_calls=%d readiness_calls=%d", postCalls.Load(), readinessCalls.Load())
	}
}

func TestDailySimulationReportFailureRetainsReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets":
			_, _ = w.Write([]byte(`{"code":0,"data":{"total":0,"items":[]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/answersheets":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"accepted","answersheet_id":"answer-failed"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets/answer-failed/assessment-readiness":
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"ready","assessment_id":"assessment-failed"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/assessments/assessment-failed/wait-report":
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"failed","reason":"worker failed"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	state, ledger := newDailySubmissionTestState(t, server.URL, true, 5)
	req := dailySubmissionTestRequest()
	err := submitDailySimulationAnswerSheet(context.Background(), state, req)
	if err == nil || !strings.Contains(err.Error(), "worker failed") {
		t.Fatalf("report failure error=%v", err)
	}
	record, ok, getErr := ledger.Get(dailySimulationSubmissionLogicalID(state, req.TesteeID, req.TaskID))
	if getErr != nil || !ok || record.Status != toolanswersheet.SubmissionStatusReady || record.AssessmentID != "assessment-failed" {
		t.Fatalf("ready ledger record=%+v ok=%v err=%v", record, ok, getErr)
	}
}

func TestDailySimulationQuestionnaireStopsAtDurableAcceptance(t *testing.T) {
	var downstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets":
			_, _ = w.Write([]byte(`{"code":0,"data":{"total":0,"items":[]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/answersheets":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"accepted","answersheet_id":"answer-questionnaire"}}`))
		default:
			downstreamCalls.Add(1)
			t.Fatalf("questionnaire called downstream endpoint: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	state, ledger := newDailySubmissionTestState(t, server.URL, false, 6)
	req := dailySubmissionTestRequest()
	if err := submitDailySimulationAnswerSheet(context.Background(), state, req); err != nil {
		t.Fatal(err)
	}
	if downstreamCalls.Load() != 0 {
		t.Fatalf("downstream calls=%d", downstreamCalls.Load())
	}
	record, ok, err := ledger.Get(dailySimulationSubmissionLogicalID(state, req.TesteeID, req.TaskID))
	if err != nil || !ok || record.Status != toolanswersheet.SubmissionStatusCompleted {
		t.Fatalf("completed ledger record=%+v ok=%v err=%v", record, ok, err)
	}
}

func TestDailySimulationAdditionalTargetUsesSelfServiceAndWaitsForReport(t *testing.T) {
	var origin OriginRef
	var reportCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets":
			_, _ = w.Write([]byte(`{"code":0,"data":{"total":0,"items":[]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/answersheets":
			var body struct {
				OriginRef OriginRef `json:"origin_ref"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			origin = body.OriginRef
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"accepted","answersheet_id":"answer-additional"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/answersheets/answer-additional/assessment-readiness":
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"ready","assessment_id":"assessment-additional"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/assessments/assessment-additional/wait-report":
			reportCalls.Add(1)
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"interpreted"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	state, _ := newDailySubmissionTestState(t, server.URL, true, 7)
	state.testee = &TesteeResponse{ID: "42"}
	state.target = &dailySimulationResolvedTarget{
		TargetType: "scale", TargetCode: "EXTRA", TargetVersion: "1",
		QuestionnaireCode: "Q-EXTRA", QuestionnaireVersion: "1", QuestionnaireTitle: "Extra",
		QuestionnaireDetail: &QuestionnaireDetailResponse{Questions: []QuestionResponse{{
			Code: "q1", Type: "Radio", Options: []OptionResponse{{Code: "A"}},
		}}},
		RequiresAssessment: true,
	}
	if err := simulateDailyUserAdditionalTarget(context.Background(), state.deps, state.profile, state, state.target); err != nil {
		t.Fatal(err)
	}
	if origin.Type != "self_service" || origin.ID != "" {
		t.Fatalf("additional target origin=%+v", origin)
	}
	if reportCalls.Load() != 1 || state.outcome.ReportStatus != "interpreted" {
		t.Fatalf("report_calls=%d outcome=%+v", reportCalls.Load(), state.outcome)
	}
}

func TestDailySimulationSubmissionIdentityPreservesPrimaryLedgerCompatibility(t *testing.T) {
	state := &dailySimulationJourneyState{
		profile: dailySimulationProfile{RunDate: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Index: 7},
		entry:   &AssessmentEntryResponse{ID: "entry-1"},
		target: &dailySimulationResolvedTarget{
			TargetType: "scale", TargetCode: "MODEL", TargetVersion: "1",
			QuestionnaireCode: "Q", QuestionnaireVersion: "2",
		},
	}
	legacy := "daily|20250101|7|42|scale|MODEL|1|Q|2"
	if got := dailySimulationSubmissionLogicalID(state, 42, ""); got != legacy {
		t.Fatalf("primary logical id=%q, want %q", got, legacy)
	}
	state.submissionOriginRef = &OriginRef{Type: "self_service"}
	if got := dailySimulationSubmissionLogicalID(state, 42, ""); got != legacy+"|origin:self_service:v1" {
		t.Fatalf("self-service logical id=%q", got)
	}
}

func newDailySubmissionTestState(t *testing.T, serverURL string, requiresAssessment bool, index int) (*dailySimulationJourneyState, *toolanswersheet.SubmissionLedger) {
	t.Helper()
	ledger, err := toolanswersheet.NewSubmissionLedger(filepath.Join(t.TempDir(), "daily-submissions.json"), "daily")
	if err != nil {
		t.Fatal(err)
	}
	logger := log.New(log.NewOptions())
	client := NewAPIClient(serverURL, "guardian-token", logger)
	state := &dailySimulationJourneyState{
		deps: &dependencies{
			Logger:                logger,
			APIClient:             NewAPIClient(serverURL, "admin-token", logger),
			CollectionClient:      client,
			DailySubmissionLedger: ledger,
		},
		collectionClient: client,
		guardianUserID:   "7001",
		entry:            &AssessmentEntryResponse{ID: "entry-1"},
		profile: dailySimulationProfile{
			RunDate: time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC), Index: index,
		},
		target: &dailySimulationResolvedTarget{
			TargetType: "scale", TargetCode: "MODEL", TargetVersion: "1",
			QuestionnaireCode: "Q", QuestionnaireVersion: "1", RequiresAssessment: requiresAssessment,
		},
	}
	return state, ledger
}

func dailySubmissionTestRequest() SubmitAnswerSheetRequest {
	return SubmitAnswerSheetRequest{
		QuestionnaireCode: "Q", QuestionnaireVersion: "1", TesteeID: 42,
		OriginRef: &OriginRef{Type: "assessment_entry", ID: "entry-1"},
		Answers:   []Answer{{QuestionCode: "q1", QuestionType: "Radio", Value: "A"}},
	}
}
