package seedapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		Answers []capturedAnswer `json:"answers"`
	}

	var captured capturedRequest
	var statusCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/answersheets":
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"code":0,"message":"accepted","data":{"status":"queued","request_id":"req-1"}}`))
		case "/api/v1/answersheets/submit-status":
			if got := r.URL.Query().Get("request_id"); got != "req-1" {
				t.Fatalf("unexpected request_id %q", got)
			}
			statusCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if statusCalls.Load() == 1 {
				_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"status":"processing","updated_at":1}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"status":"done","answersheet_id":"123","assessment_id":"456","updated_at":2}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	resp, err := client.SubmitAnswerSheet(context.Background(), SubmitAnswerSheetRequest{
		QuestionnaireCode:    "QNR-001",
		QuestionnaireVersion: "1.0.0",
		TesteeID:             1001,
		Answers: []Answer{
			{QuestionCode: "q1", QuestionType: "Radio", Value: "A"},
			{QuestionCode: "q2", QuestionType: "Checkbox", Value: []string{"B", "C"}},
			{QuestionCode: "q3", QuestionType: "Number", Value: float64(12)},
		},
	})
	if err != nil {
		t.Fatalf("SubmitAnswerSheet returned error: %v", err)
	}
	if resp == nil || resp.ID != "123" || resp.AssessmentID != "456" {
		t.Fatalf("unexpected submit response: %+v", resp)
	}
	if statusCalls.Load() < 2 {
		t.Fatalf("expected submit-status to be polled at least twice, got %d", statusCalls.Load())
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

func TestGetScaleUsesAssessmentModels(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/assessment-models/3adyDE" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"code":"3adyDE","title":"Demo","status":"published","version":"1.0.0","questionnaire_code":"QNR-1","questionnaire_version":"6.0.1"}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	scale, err := client.GetScale(context.Background(), "3adyDE")
	if err != nil {
		t.Fatalf("GetScale returned error: %v", err)
	}
	if scale.Code != "3adyDE" || scale.QuestionnaireCode != "QNR-1" || scale.QuestionnaireVersion != "6.0.1" || scale.Version != "1.0.0" {
		t.Fatalf("unexpected scale response: %+v", scale)
	}
}
