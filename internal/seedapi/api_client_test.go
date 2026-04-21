package seedapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestRegisterIAMChildAcceptsCreatedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %s", r.Method)
		}
		if r.URL.Path != "/api/v1/identity/children/register" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}

		var req IAMChildRegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Relation != "other" {
			t.Fatalf("unexpected relation %q", req.Relation)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","data":{"child":{"id":"child-1","legal_name":"王子轩","dob":"2014-04-20"}}}`))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", nil)
	resp, err := client.RegisterIAMChild(context.Background(), IAMChildRegisterRequest{
		LegalName: "王子轩",
		Gender:    1,
		DOB:       "2014-04-20",
		Relation:  "other",
	})
	if err != nil {
		t.Fatalf("RegisterIAMChild returned error: %v", err)
	}
	if resp == nil || resp.Child == nil {
		t.Fatalf("expected child response, got %+v", resp)
	}
	if resp.Child.ID != "child-1" {
		t.Fatalf("unexpected child id %q", resp.Child.ID)
	}
}
