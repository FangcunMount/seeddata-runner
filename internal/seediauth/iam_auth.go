package seediauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	"github.com/FangcunMount/iam/v2/pkg/sdk/auth/loginv2"
)

const defaultIAMLoginPath = "/api/v2/authn/login"

type Config struct {
	BaseURL  string
	LoginURL string
	Username string
	Password string
	TenantID string
}

type iamLoginResponse struct {
	Code    *int            `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func FetchTokenFromIAM(ctx context.Context, cfg Config, logger log.Logger) (string, error) {
	loginURL, err := ResolveLoginURL(cfg)
	if err != nil {
		return "", err
	}
	return FetchTokenFromIAMWithPassword(ctx, loginURL, cfg.Username, cfg.Password, cfg.TenantID, "seeddata", logger)
}

func ResolveLoginURL(cfg Config) (string, error) {
	if strings.TrimSpace(cfg.LoginURL) != "" {
		return strings.TrimSpace(cfg.LoginURL), nil
	}
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return "", fmt.Errorf("iam login url is empty")
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse iam base url %q: %w", base, err)
	}
	parsed.Path = appendIAMLoginPath(parsed.Path)
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func FetchTokenFromIAMWithPassword(
	ctx context.Context,
	loginURL, username, password, tenantID, deviceID string,
	logger log.Logger,
) (string, error) {
	if strings.TrimSpace(loginURL) == "" {
		return "", fmt.Errorf("iam login url is empty")
	}
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return "", fmt.Errorf("iam username/password is empty")
	}
	if strings.TrimSpace(deviceID) == "" {
		deviceID = "seeddata"
	}

	var parsedTenantID uint64
	if rawTenantID := strings.TrimSpace(tenantID); rawTenantID != "" {
		parsed, err := strconv.ParseUint(rawTenantID, 10, 64)
		if err != nil {
			return "", fmt.Errorf("parse iam tenant_id %q: %w", rawTenantID, err)
		}
		parsedTenantID = parsed
	}

	loginReq := loginv2.LoginRequest{
		AuthMethod: loginv2.AuthMethodPassword,
		MethodPayload: loginv2.PasswordPayload{
			Username: username,
			Password: password,
			TenantID: parsedTenantID,
		},
		DeviceID: deviceID,
	}
	if err := loginReq.Validate(); err != nil {
		return "", fmt.Errorf("validate iam login request: %w", err)
	}

	reqBody, err := json.Marshal(loginReq)
	if err != nil {
		return "", fmt.Errorf("marshal iam login request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("create iam request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request iam token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read iam response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyStr := string(body)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return "", fmt.Errorf("iam login failed: status=%d body=%s", resp.StatusCode, bodyStr)
	}

	token, err := extractTokenFromIAMResponse(body)
	if err != nil {
		logger.Warnw("IAM login response missing token field")
		return "", err
	}

	identity := parseSeedTokenIdentity(token)
	logger.Infow("IAM token acquired",
		"iam_username", strings.TrimSpace(username),
		"subject", identity.Subject,
		"user_id", identity.UserID,
		"account_id", identity.AccountID,
		"tenant_id", identity.TenantID,
		"issuer", identity.Issuer,
		"audience", identity.Audience,
		"expires_at", identity.ExpiresAt,
	)

	return token, nil
}

func appendIAMLoginPath(path string) string {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	switch {
	case path == "":
		return defaultIAMLoginPath
	case strings.HasSuffix(path, defaultIAMLoginPath):
		return path
	case strings.HasSuffix(path, "/api/v2"):
		return path + "/authn/login"
	default:
		return path + defaultIAMLoginPath
	}
}

func extractTokenFromIAMResponse(body []byte) (string, error) {
	var respWrapper iamLoginResponse
	if err := json.Unmarshal(body, &respWrapper); err != nil {
		return "", fmt.Errorf("unmarshal iam response: %w", err)
	}

	isEnvelope := respWrapper.Code != nil || respWrapper.Message != "" || len(respWrapper.Data) > 0
	if isEnvelope {
		if respWrapper.Code != nil && *respWrapper.Code != 0 {
			return "", fmt.Errorf("iam login error: code=%d message=%s", *respWrapper.Code, respWrapper.Message)
		}
		if token := extractTokenFromIAMData(respWrapper.Data); token != "" {
			return token, nil
		}
		return "", fmt.Errorf("iam login response missing token")
	}

	if token := extractTokenFromIAMData(body); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("iam login response missing token")
}

type seedTokenIdentity struct {
	Subject   string
	UserID    string
	AccountID string
	TenantID  string
	Issuer    string
	Audience  []string
	ExpiresAt time.Time
}

func parseSeedTokenIdentity(token string) seedTokenIdentity {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return seedTokenIdentity{}
	}

	payload, err := decodeSeedTokenSegment(parts[1])
	if err != nil {
		return seedTokenIdentity{}
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return seedTokenIdentity{}
	}

	return seedTokenIdentity{
		Subject:   readStringField(claims, "sub"),
		UserID:    readStringField(claims, "user_id"),
		AccountID: readStringField(claims, "account_id"),
		TenantID:  readStringField(claims, "tenant_id"),
		Issuer:    readStringField(claims, "iss"),
		Audience:  readStringSliceField(claims, "aud"),
		ExpiresAt: readUnixTimeField(claims, "exp"),
	}
}

func decodeSeedTokenSegment(segment string) ([]byte, error) {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return nil, fmt.Errorf("empty token segment")
	}
	if payload, err := base64.RawURLEncoding.DecodeString(segment); err == nil {
		return payload, nil
	}
	return base64.URLEncoding.DecodeString(segment)
}

func extractTokenFromIAMData(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return ""
	}

	if token := readStringField(data, "token"); token != "" {
		return token
	}
	if token := readStringField(data, "access_token"); token != "" {
		return token
	}
	if token := readStringField(data, "accessToken"); token != "" {
		return token
	}

	if tokenPair, ok := data["token_pair"].(map[string]interface{}); ok {
		if token := readStringField(tokenPair, "access_token"); token != "" {
			return token
		}
		if token := readStringField(tokenPair, "accessToken"); token != "" {
			return token
		}
	}

	if tokenPair, ok := data["tokenPair"].(map[string]interface{}); ok {
		if token := readStringField(tokenPair, "access_token"); token != "" {
			return token
		}
		if token := readStringField(tokenPair, "accessToken"); token != "" {
			return token
		}
	}

	return ""
}

func readStringField(data map[string]interface{}, key string) string {
	if value, ok := data[key]; ok {
		if str, ok := value.(string); ok {
			return strings.TrimSpace(str)
		}
	}
	return ""
}

func readStringSliceField(data map[string]interface{}, key string) []string {
	value, ok := data[key]
	if !ok || value == nil {
		return nil
	}
	switch v := value.(type) {
	case string:
		if str := strings.TrimSpace(v); str != "" {
			return []string{str}
		}
	case []interface{}:
		items := make([]string, 0, len(v))
		for _, item := range v {
			str, ok := item.(string)
			if !ok {
				continue
			}
			if str = strings.TrimSpace(str); str != "" {
				items = append(items, str)
			}
		}
		if len(items) > 0 {
			return items
		}
	case []string:
		items := make([]string, 0, len(v))
		for _, item := range v {
			if item = strings.TrimSpace(item); item != "" {
				items = append(items, item)
			}
		}
		if len(items) > 0 {
			return items
		}
	}
	return nil
}

func readUnixTimeField(data map[string]interface{}, key string) time.Time {
	value, ok := data[key]
	if !ok || value == nil {
		return time.Time{}
	}

	switch v := value.(type) {
	case float64:
		return time.Unix(int64(v), 0).UTC()
	case int64:
		return time.Unix(v, 0).UTC()
	case int:
		return time.Unix(int64(v), 0).UTC()
	case json.Number:
		seconds, err := v.Int64()
		if err != nil {
			return time.Time{}
		}
		return time.Unix(seconds, 0).UTC()
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return time.Time{}
		}
		seconds, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return time.Time{}
		}
		return time.Unix(seconds, 0).UTC()
	default:
		return time.Time{}
	}
}
