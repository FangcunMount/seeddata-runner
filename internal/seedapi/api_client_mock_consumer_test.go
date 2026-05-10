package seedapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FangcunMount/component-base/pkg/log"
)

func TestEnsureIAMMockConsumerSendsSharedSecretAndDecodesResponse(t *testing.T) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/api/v2/internal/authn/mock-consumers/ensure" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("X-IAM-Seed-Secret"); got != "test-secret" {
			t.Fatalf("unexpected secret header: %q", got)
		}

		var req EnsureIAMMockConsumerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Email != "guardian@example.com" {
			t.Fatalf("unexpected email: %q", req.Email)
		}

		_ = json.NewEncoder(w).Encode(Response{
			Code:    0,
			Message: "success",
			Data: EnsureIAMMockConsumerResponse{
				UserID:          "1001",
				LoginIdentityID: "2002",
				LoginID:         "guardian@example.com",
				IsNewUser:       true,
				IsNewIdentity:   true,
			},
		})
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "", log.New(log.NewOptions()))
	resp, err := client.EnsureIAMMockConsumer(context.Background(), "/api/v2/internal/authn/mock-consumers/ensure", EnsureIAMMockConsumerRequest{
		Name:     "Guardian",
		Phone:    "+8619904200001",
		Email:    "guardian@example.com",
		Password: "DailySim@123",
	}, "test-secret")
	if err != nil {
		t.Fatalf("EnsureIAMMockConsumer returned error: %v", err)
	}
	if resp.UserID != "1001" || resp.LoginIdentityID != "2002" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
