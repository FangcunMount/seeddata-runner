package seedapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/answersheets" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"id":"123","message":"ok"}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	_, err := client.SubmitAnswerSheet(context.Background(), SubmitAnswerSheetRequest{
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
