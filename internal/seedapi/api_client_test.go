package seedapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FangcunMount/seeddata-runner/internal/historicalseed"
)

func TestAPIClientSignsHistoricalContextWithoutChangingOrdinaryRequests(t *testing.T) {
	var historicalHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		historicalHeader = r.Header.Get(historicalseed.HeaderContext)
		if historicalHeader != "" && r.Header.Get(historicalseed.HeaderSignature) == "" {
			t.Fatal("historical context was not signed")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	client.SetHistoricalSecret([]byte("secret"))
	if _, err := client.doRequest(context.Background(), http.MethodGet, "/ordinary", nil); err != nil {
		t.Fatal(err)
	}
	if historicalHeader != "" {
		t.Fatal("ordinary request unexpectedly carried historical context")
	}

	historicalCtx := historicalseed.WithContext(context.Background(), historicalseed.Context{
		BatchID: "batch", ScenarioID: "scenario", OrgID: 1, Version: historicalseed.Version1,
		Timeline: historicalseed.Timeline{EntryResolvedAt: timePtr(time.Date(2025, 1, 1, 8, 5, 0, 0, time.UTC))},
	})
	if _, err := client.doRequest(historicalCtx, http.MethodGet, "/historical?x=1", nil); err != nil {
		t.Fatal(err)
	}
	if historicalHeader == "" {
		t.Fatal("historical request did not carry context")
	}
}

func timePtr(value time.Time) *time.Time { return &value }

func TestListHistoricalScenarioStagesPreservesLargePayloadID(t *testing.T) {
	const answerSheetID = "630459979077268014"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("scenario_id"); got != "2025-01-01/107/submit_answer/task-1" {
			t.Fatalf("scenario_id = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"batch_id":"batch","stages":[{"stage":"answersheet_submit","resource_id":"` + answerSheetID + `","payload_json":{"answersheet_id":` + answerSheetID + `}}]}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	response, err := client.ListHistoricalScenarioStages(context.Background(), "batch", "2025-01-01/107/submit_answer/task-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Stages) != 1 {
		t.Fatalf("stages = %d, want 1", len(response.Stages))
	}
	record := response.Stages[0]
	if record.ResourceID != answerSheetID {
		t.Fatalf("resource_id = %q, want %q", record.ResourceID, answerSheetID)
	}
	var payload struct {
		AnswerSheetID json.Number `json:"answersheet_id"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(record.PayloadJSON)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if got := payload.AnswerSheetID.String(); got != answerSheetID {
		t.Fatalf("payload answersheet_id = %q, want %q", got, answerSheetID)
	}
}

func TestDecodeResponseDataAssessmentEntryListSupportsFormattedTime(t *testing.T) {
	resp := &Response{
		Data: map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{
					"id":             "entry-1",
					"org_id":         "1",
					"clinician_id":   "2",
					"token":          "token-1",
					"target_type":    "scale",
					"target_code":    "SNAP",
					"is_active":      true,
					"expires_at":     "2026-10-14 12:45:08",
					"qrcode_url":     "https://example.com/qrcode",
					"target_version": "v1",
				},
			},
			"total":       1,
			"page":        1,
			"page_size":   20,
			"total_pages": 1,
		},
	}

	var result AssessmentEntryListResponse
	if err := decodeResponseData(resp, &result); err != nil {
		t.Fatalf("decodeResponseData returned error: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result.Items))
	}
	if result.Items[0].ExpiresAt == nil {
		t.Fatal("expected expires_at to be parsed")
	}
	if got, want := result.Items[0].ExpiresAt.Format("2006-01-02 15:04:05"), "2026-10-14 12:45:08"; got != want {
		t.Fatalf("unexpected expires_at: got=%s want=%s", got, want)
	}
}

func TestDecodeResponseDataRejectsInvalidFlexibleTime(t *testing.T) {
	resp := &Response{
		Data: map[string]interface{}{
			"id":         "testee-1",
			"name":       "Alice",
			"created_at": "not-a-time",
			"updated_at": "2026-04-01 10:00:00",
		},
	}

	var result ApiserverTesteeResponse
	if err := decodeResponseData(resp, &result); err == nil {
		t.Fatal("expected decodeResponseData to fail for invalid created_at")
	}
}

func TestCollectionTesteeResponseCarriesProfileLinkIdentity(t *testing.T) {
	var result TesteeResponse
	if err := json.Unmarshal([]byte(`{"id":"10","iam_profile_id":"20","iam_profile_link_id":"30"}`), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != "10" || result.IAMProfileID != "20" || result.IAMProfileLinkID != "30" {
		t.Fatalf("unexpected collection testee identity: %+v", result)
	}
}

func TestDoRequestUnauthorizedIncludesErrorField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"token audience is invalid"}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "bad-token", nil)
	_, err := client.doRequest(context.Background(), http.MethodPost, "/api/v1/testees", nil)
	if err == nil {
		t.Fatal("expected unauthorized error")
	}
	if !strings.Contains(err.Error(), "token audience is invalid") {
		t.Fatalf("expected error field in message, got %v", err)
	}
	if !strings.Contains(err.Error(), "auth_token_present=true") {
		t.Fatalf("expected auth token presence in message, got %v", err)
	}
}

func TestDoRequestSendsBearerAuthorizationHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "token-1", nil)
	if _, err := client.doRequest(context.Background(), http.MethodPost, "/api/v1/testees", nil); err != nil {
		t.Fatalf("doRequest returned error: %v", err)
	}
}
