package dailysim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	toolanswersheet "github.com/FangcunMount/seeddata-runner/internal/answersheet"
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
	if err := submitDailySimulationAnswerSheet(context.Background(), state, req); err != nil {
		t.Fatal(err)
	}
	if state.outcome.AnswerSheetID != "9001" || state.outcome.AssessmentID != "8001" {
		t.Fatalf("unexpected daily outcome: %+v", state.outcome)
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
	if err := submitDailySimulationAnswerSheet(context.Background(), state, req); err != nil {
		t.Fatal(err)
	}
	record, ok, err := ledger.Get(dailySimulationSubmissionLogicalID(state, req.TesteeID))
	if err != nil || !ok {
		t.Fatalf("load pending submission: ok=%v err=%v", ok, err)
	}
	if record.Status != toolanswersheet.SubmissionStatusAcceptedPending || record.AnswerSheetID != "9002" {
		t.Fatalf("unexpected timeout record: %+v", record)
	}
}
