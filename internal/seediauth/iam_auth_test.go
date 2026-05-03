package seediauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FangcunMount/component-base/pkg/log"
)

func TestFetchTokenFromIAMWithPasswordSendsV2PasswordPayload(t *testing.T) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/api/v2/authn/login" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("unexpected accept header: %q", got)
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got := payload["auth_method"]; got != "password" {
			t.Fatalf("unexpected auth_method: %#v", got)
		}
		if got := payload["device_id"]; got != "seeddata" {
			t.Fatalf("unexpected device_id: %#v", got)
		}

		methodPayload, ok := payload["method_payload"].(map[string]any)
		if !ok {
			t.Fatalf("expected method_payload object, got %#v", payload["method_payload"])
		}
		if got := methodPayload["username"]; got != "seed-admin" {
			t.Fatalf("unexpected username: %#v", got)
		}
		if _, ok := methodPayload["tenant_id"].(float64); !ok {
			t.Fatalf("expected tenant_id to marshal as number, got %#v", methodPayload["tenant_id"])
		}
		if got := methodPayload["tenant_id"].(float64); got != 1 {
			t.Fatalf("expected tenant_id=1, got %v", got)
		}

		_ = json.NewEncoder(w).Encode(iamLoginResponse{
			Code: intPtr(0),
			Data: mustRawMessage(t, map[string]any{
				"access_token": "token-1",
			}),
		})
	}))
	defer server.Close()

	token, err := FetchTokenFromIAMWithPassword(context.Background(), server.URL+"/api/v2/authn/login", "seed-admin", "secret", "1", "seeddata", log.New(log.NewOptions()))
	if err != nil {
		t.Fatalf("FetchTokenFromIAMWithPassword returned error: %v", err)
	}
	if token != "token-1" {
		t.Fatalf("unexpected token: %q", token)
	}
}

func TestResolveLoginURLDefaultsToAPIV2(t *testing.T) {
	got, err := ResolveLoginURL(Config{BaseURL: "https://iam.example.com"})
	if err != nil {
		t.Fatalf("ResolveLoginURL returned error: %v", err)
	}
	if got != "https://iam.example.com/api/v2/authn/login" {
		t.Fatalf("unexpected login url: %s", got)
	}
}

func intPtr(value int) *int {
	return &value
}

func mustRawMessage(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal raw message: %v", err)
	}
	return data
}
