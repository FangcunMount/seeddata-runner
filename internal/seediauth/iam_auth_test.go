package seediauth

import (
	"context"
	"encoding/base64"
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

		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"access_token": "token-1",
			},
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

func TestLoginClientBaseURLAcceptsResolvedLoginEndpoint(t *testing.T) {
	got, err := loginClientBaseURL("https://iam.example.com/proxy/api/v2/authn/login")
	if err != nil {
		t.Fatalf("loginClientBaseURL returned error: %v", err)
	}
	if got != "https://iam.example.com/proxy" {
		t.Fatalf("unexpected login client base url: %s", got)
	}
}

func TestParseSeedTokenIdentityReadsIssuerAndAudience(t *testing.T) {
	token := unsignedJWT(t, map[string]any{
		"sub":       "subject-1",
		"user_id":   "1001",
		"tenant_id": "1",
		"iss":       "https://iam.fangcunmount.cn",
		"aud":       []string{"qs-api", "collection-api"},
	})

	identity := parseSeedTokenIdentity(token)
	if identity.Issuer != "https://iam.fangcunmount.cn" {
		t.Fatalf("unexpected issuer: %q", identity.Issuer)
	}
	if got, want := identity.Audience, []string{"qs-api", "collection-api"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("unexpected audience: %#v", got)
	}
}

func unsignedJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal token payload: %v", err)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(data) + ".sig"
}
