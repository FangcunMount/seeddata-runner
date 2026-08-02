package seedapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIClientNeverSendsRetiredHeaders(t *testing.T) {
	requestCount := 0
	retiredPrefix, err := hex.DecodeString("782d71732d686973746f726963616c2d")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		for name := range r.Header {
			if strings.HasPrefix(strings.ToLower(name), string(retiredPrefix)) {
				t.Fatalf("retired header was sent by %s %s: %s", r.Method, r.URL.Path, name)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	requests := []struct {
		method string
		path   string
		body   any
	}{
		{method: http.MethodPost, path: "/body", body: map[string]string{"value": "1"}},
		{method: http.MethodPost, path: "/no-body"},
		{method: http.MethodGet, path: "/read"},
	}
	for _, request := range requests {
		if _, err := client.doRequest(context.Background(), request.method, request.path, request.body); err != nil {
			t.Fatal(err)
		}
	}
	if requestCount != len(requests) {
		t.Fatalf("request count=%d, want %d", requestCount, len(requests))
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
