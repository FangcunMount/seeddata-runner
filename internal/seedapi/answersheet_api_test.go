package seedapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestSubmitAnswerSheetSerializesCollectionValues(t *testing.T) {
	t.Parallel()

	type capturedAnswer struct {
		QuestionCode string `json:"question_code"`
		QuestionType string `json:"question_type"`
		Value        string `json:"value"`
	}
	type capturedRequest struct {
		IdempotencyKey string           `json:"idempotency_key"`
		OriginRef      *OriginRef       `json:"origin_ref"`
		Answers        []capturedAnswer `json:"answers"`
	}

	var captured capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/answersheets" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Request-ID"); got != "attempt-1" {
			t.Fatalf("unexpected request id %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"code":0,"message":"accepted","data":{"status":"accepted","request_id":"attempt-1","answersheet_id":"123"}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	resp, err := client.AcceptCollectionAnswerSheet(context.Background(), SubmitAnswerSheetRequest{
		QuestionnaireCode:    "QNR-001",
		QuestionnaireVersion: "1.0.0",
		IdempotencyKey:       "seed.daily.1234",
		TesteeID:             1001,
		OriginRef:            &OriginRef{Type: "assessment_entry", ID: "88"},
		Answers: []Answer{
			{QuestionCode: "q1", QuestionType: "Radio", Value: "A"},
			{QuestionCode: "q2", QuestionType: "Checkbox", Value: []string{"B", "C"}},
			{QuestionCode: "q3", QuestionType: "Number", Value: float64(12)},
		},
	}, "attempt-1")
	if err != nil {
		t.Fatalf("AcceptCollectionAnswerSheet returned error: %v", err)
	}
	if resp == nil || resp.AnswerSheetID != "123" || resp.RequestID != "attempt-1" {
		t.Fatalf("unexpected submit response: %+v", resp)
	}
	if captured.IdempotencyKey != "seed.daily.1234" {
		t.Fatalf("unexpected idempotency key %q", captured.IdempotencyKey)
	}
	if captured.OriginRef == nil || captured.OriginRef.Type != "assessment_entry" || captured.OriginRef.ID != "88" {
		t.Fatalf("unexpected origin_ref: %+v", captured.OriginRef)
	}

	if len(captured.Answers) != 3 {
		t.Fatalf("expected 3 answers, got %d", len(captured.Answers))
	}
	if captured.Answers[0].Value != "A" {
		t.Fatalf("expected radio value to remain plain string, got %q", captured.Answers[0].Value)
	}
	if captured.Answers[1].Value != `["B","C"]` {
		t.Fatalf("expected checkbox value JSON string, got %q", captured.Answers[1].Value)
	}
	if captured.Answers[2].Value != `12` {
		t.Fatalf("expected number value JSON string, got %q", captured.Answers[2].Value)
	}
}

func TestGetAssessmentReadiness(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/answersheets/123/assessment-readiness" || r.URL.Query().Get("testee_id") != "1001" {
			t.Fatalf("unexpected readiness request: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"status":"pending","answersheet_id":"123","next_poll_after_ms":2400}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	readiness, err := client.GetAssessmentReadiness(context.Background(), "123", 1001)
	if err != nil {
		t.Fatalf("GetAssessmentReadiness returned error: %v", err)
	}
	if readiness.Status != "pending" || readiness.AnswerSheetID != "123" || readiness.NextPollAfterMs != 2400 {
		t.Fatalf("unexpected readiness response: %+v", readiness)
	}
}

func TestGetPublishedAssessmentModelUsesExactVersion(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/assessment-models/published/3adyDE" || r.URL.Query().Get("version") != "1.0.0" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"code":"3adyDE","title":"Demo","status":"published","version":"1.0.0","questionnaire_code":"QNR-1","questionnaire_version":"6.0.1"}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	scale, err := client.GetPublishedAssessmentModel(context.Background(), "3adyDE", "1.0.0")
	if err != nil {
		t.Fatalf("GetPublishedAssessmentModel returned error: %v", err)
	}
	if scale.Code != "3adyDE" || scale.QuestionnaireCode != "QNR-1" || scale.QuestionnaireVersion != "6.0.1" || scale.Version != "1.0.0" {
		t.Fatalf("unexpected scale response: %+v", scale)
	}
}

func TestGetPublishedQuestionnaireUsesExactVersion(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/questionnaires/QNR-1" || r.URL.Query().Get("version") != "6.0.1" {
			t.Fatalf("unexpected questionnaire request: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"code":"QNR-1","title":"Demo","status":"published","version":"6.0.1","type":"MedicalScale","questions":[]}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	detail, err := client.GetPublishedQuestionnaire(context.Background(), "QNR-1", "6.0.1")
	if err != nil {
		t.Fatalf("GetPublishedQuestionnaire returned error: %v", err)
	}
	if detail.Code != "QNR-1" || detail.Version != "6.0.1" {
		t.Fatalf("unexpected questionnaire: %+v", detail)
	}
}

func TestUnversionedPublishedModelLookupIsNotCached(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("version"); got != "" {
			t.Fatalf("unexpected version query %q", got)
		}
		version := strconv.Itoa(int(calls.Add(1)))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"code":"MODEL","status":"published","version":"` + version + `","questionnaire_code":"Q","questionnaire_version":"` + version + `"}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	first, err := client.GetPublishedAssessmentModel(context.Background(), "MODEL", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.GetPublishedAssessmentModel(context.Background(), "MODEL", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != "1" || second.Version != "2" || calls.Load() != 2 {
		t.Fatalf("unversioned lookup was cached: first=%q second=%q calls=%d", first.Version, second.Version, calls.Load())
	}
}
