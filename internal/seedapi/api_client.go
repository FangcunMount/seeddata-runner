package seedapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	authsignup "github.com/FangcunMount/iam/v2/pkg/sdk/auth/signup"
	"github.com/FangcunMount/seeddata-runner/internal/historicalseed"
	"github.com/FangcunMount/seeddata-runner/internal/scheduler"
)

type RetryConfig struct {
	MaxRetries int    `yaml:"maxRetries"`
	MinDelay   string `yaml:"minDelay"`
	MaxDelay   string `yaml:"maxDelay"`
}

// APIClient HTTP API 客户端
type APIClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
	logger     log.Logger
	tokenMu    sync.RWMutex
	refresher  func(context.Context) (string, error)
	provider   *TokenProvider

	retryMax      int
	retryMinDelay time.Duration
	retryMaxDelay time.Duration

	publishedModelCache *versionedCache[PublishedAssessmentModelResponse]
	questionnaireCache  *versionedCache[QuestionnaireDetailResponse]
	historicalSecret    []byte
}

type TokenProvider struct {
	tokenMu   sync.RWMutex
	refreshMu sync.Mutex
	token     string
	expiresAt time.Time
	refresher func(context.Context) (string, error)
}

const (
	defaultHTTPTimeout   = 30 * time.Second
	seedTokenRefreshSkew = 2 * time.Minute
)

var sharedAPITransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          128,
	MaxIdleConnsPerHost:   64,
	MaxConnsPerHost:       64,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: time.Second,
}

// NewAPIClient 创建 API 客户端
func NewAPIClient(baseURL, token string, logger log.Logger) *APIClient {
	// 确保 baseURL 不以斜杠结尾
	baseURL = strings.TrimSuffix(baseURL, "/")

	retryMax, retryMinDelay, retryMaxDelay := defaultRetryConfig()

	return &APIClient{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout:   defaultHTTPTimeout,
			Transport: sharedAPITransport,
		},
		logger:              logger,
		retryMax:            retryMax,
		retryMinDelay:       retryMinDelay,
		retryMaxDelay:       retryMaxDelay,
		publishedModelCache: newVersionedCache[PublishedAssessmentModelResponse](publishedResourceCacheLimit),
		questionnaireCache:  newVersionedCache[QuestionnaireDetailResponse](publishedResourceCacheLimit),
	}
}

func (c *APIClient) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

func (c *APIClient) SetHistoricalSecret(secret []byte) {
	if c == nil {
		return
	}
	c.historicalSecret = append(c.historicalSecret[:0], secret...)
}

func (c *APIClient) HistoricalSecret() []byte {
	if c == nil {
		return nil
	}
	return append([]byte(nil), c.historicalSecret...)
}

// SetHTTPTimeout updates the underlying HTTP client timeout.
func (c *APIClient) SetHTTPTimeout(timeout time.Duration) {
	if c == nil || c.httpClient == nil || timeout <= 0 {
		return
	}
	c.httpClient.Timeout = timeout
}

// SetTokenRefresher sets a callback to refresh token when needed.
func (c *APIClient) SetTokenRefresher(fn func(context.Context) (string, error)) {
	c.refresher = fn
	if c.provider != nil {
		c.provider.SetRefresher(fn)
	}
}

func NewTokenProvider(initialToken string, refresher func(context.Context) (string, error)) *TokenProvider {
	provider := &TokenProvider{
		refresher: refresher,
	}
	provider.SetToken(initialToken)
	return provider
}

func (p *TokenProvider) SetRefresher(fn func(context.Context) (string, error)) {
	if p == nil {
		return
	}
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()
	p.refresher = fn
}

func (p *TokenProvider) Token() string {
	if p == nil {
		return ""
	}
	p.tokenMu.RLock()
	defer p.tokenMu.RUnlock()
	return p.token
}

func (p *TokenProvider) SetToken(token string) {
	if p == nil {
		return
	}
	token = strings.TrimSpace(token)
	identity := parseSeedTokenIdentity(token)
	p.tokenMu.Lock()
	p.token = token
	p.expiresAt = identity.ExpiresAt
	p.tokenMu.Unlock()
}

func (p *TokenProvider) ExpiresAt() time.Time {
	if p == nil {
		return time.Time{}
	}
	p.tokenMu.RLock()
	defer p.tokenMu.RUnlock()
	return p.expiresAt
}

func (p *TokenProvider) RemainingTTL(now time.Time) time.Duration {
	expiresAt := p.ExpiresAt()
	if expiresAt.IsZero() {
		return 0
	}
	return expiresAt.Sub(now)
}

func (p *TokenProvider) shouldRefresh(now time.Time, minTTL time.Duration) bool {
	if p == nil {
		return false
	}
	expiresAt := p.ExpiresAt()
	if expiresAt.IsZero() {
		return false
	}
	if minTTL < 0 {
		minTTL = 0
	}
	return !expiresAt.After(now.Add(minTTL))
}

func (p *TokenProvider) RefreshIfNeeded(ctx context.Context, minTTL time.Duration) (bool, error) {
	if p == nil {
		return false, nil
	}

	now := time.Now()
	if !p.shouldRefresh(now, minTTL) {
		return false, nil
	}

	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()

	now = time.Now()
	if !p.shouldRefresh(now, minTTL) {
		return false, nil
	}
	if p.refresher == nil {
		if expiresAt := p.ExpiresAt(); expiresAt.IsZero() || expiresAt.After(now) {
			return false, nil
		}
		return false, fmt.Errorf("token refresher not configured")
	}

	token, err := p.refresher(ctx)
	if err != nil {
		if expiresAt := p.ExpiresAt(); expiresAt.IsZero() || expiresAt.After(now) {
			return false, nil
		}
		return false, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return false, fmt.Errorf("token refresher returned empty token")
	}
	p.SetToken(token)
	return true, nil
}

func (p *TokenProvider) Refresh(ctx context.Context, staleToken string) (string, error) {
	if p == nil {
		return "", fmt.Errorf("token provider is nil")
	}
	current := p.Token()
	if current != "" && staleToken != "" && current != staleToken {
		return current, nil
	}

	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()

	current = p.Token()
	if current != "" && staleToken != "" && current != staleToken {
		return current, nil
	}
	if p.refresher == nil {
		return "", fmt.Errorf("token refresher not configured")
	}

	token, err := p.refresher(ctx)
	if err != nil {
		return "", err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("token refresher returned empty token")
	}
	p.SetToken(token)
	return token, nil
}

func (c *APIClient) SetTokenProvider(provider *TokenProvider) {
	c.provider = provider
	if provider == nil {
		return
	}
	if c.refresher != nil {
		provider.SetRefresher(c.refresher)
	}
	if token := strings.TrimSpace(c.getToken()); token != "" {
		provider.SetToken(token)
	}
}

// SetRetryConfig updates retry settings for the client.
func (c *APIClient) SetRetryConfig(cfg RetryConfig) {
	retryMax, retryMinDelay, retryMaxDelay := defaultRetryConfig()
	if cfg.MaxRetries >= 0 {
		retryMax = cfg.MaxRetries
	}
	if strings.TrimSpace(cfg.MinDelay) != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(cfg.MinDelay)); err == nil {
			retryMinDelay = d
		}
	}
	if strings.TrimSpace(cfg.MaxDelay) != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(cfg.MaxDelay)); err == nil {
			retryMaxDelay = d
		}
	}
	if retryMinDelay <= 0 {
		retryMinDelay = 200 * time.Millisecond
	}
	if retryMaxDelay <= 0 {
		retryMaxDelay = 5 * time.Second
	}
	if retryMax < 0 {
		retryMax = 0
	}
	c.retryMax = retryMax
	c.retryMinDelay = retryMinDelay
	c.retryMaxDelay = retryMaxDelay
}

// SetToken updates the client token safely.
func (c *APIClient) SetToken(token string) {
	if c.provider != nil {
		c.provider.SetToken(token)
	}
	c.tokenMu.Lock()
	c.token = strings.TrimSpace(token)
	c.tokenMu.Unlock()
}

func (c *APIClient) getLocalToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

func (c *APIClient) getToken() string {
	if c.provider != nil {
		if token := c.provider.Token(); token != "" {
			return token
		}
	}
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

func normalizeSeedCacheKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

type TokenIdentity struct {
	Subject   string
	UserID    string
	AccountID string
	TenantID  string
	ExpiresAt time.Time
}

func parseSeedTokenIdentity(token string) TokenIdentity {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return TokenIdentity{}
	}

	payload, err := decodeSeedTokenSegment(parts[1])
	if err != nil {
		return TokenIdentity{}
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return TokenIdentity{}
	}

	return TokenIdentity{
		Subject:   readStringField(claims, "sub"),
		UserID:    readStringField(claims, "user_id"),
		AccountID: readStringField(claims, "account_id"),
		TenantID:  readStringField(claims, "tenant_id"),
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

func readStringField(data map[string]interface{}, key string) string {
	if value, ok := data[key]; ok {
		if str, ok := value.(string); ok {
			return strings.TrimSpace(str)
		}
	}
	return ""
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

// Response 通用 API 响应
type Response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

func responseErrorMessage(body []byte) string {
	var apiResp Response
	if err := json.Unmarshal(body, &apiResp); err == nil {
		if message := strings.TrimSpace(apiResp.Message); message != "" {
			return message
		}
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err == nil {
		for _, key := range []string{"message", "error", "detail"} {
			if message := readStringField(raw, key); message != "" {
				return message
			}
		}
	}

	return truncateResponseBody(body, 300)
}

func truncateResponseBody(body []byte, limit int) string {
	value := strings.TrimSpace(string(body))
	if limit > 0 && len(value) > limit {
		return value[:limit] + "..."
	}
	return value
}

// PublishedAssessmentModelResponse 已发布测评模型响应。
type PublishedAssessmentModelResponse struct {
	Code                 string `json:"code"`
	Title                string `json:"title"`
	Status               string `json:"status"`
	Version              string `json:"version,omitempty"`
	QuestionnaireCode    string `json:"questionnaire_code"`
	QuestionnaireVersion string `json:"questionnaire_version"`
}

// PlanResponse 计划响应。
type PlanResponse struct {
	ID            string   `json:"id"`
	OrgID         int64    `json:"org_id"`
	ScaleCode     string   `json:"scale_code"`
	ScheduleType  string   `json:"schedule_type"`
	TriggerTime   string   `json:"trigger_time"`
	Interval      int      `json:"interval"`
	TotalTimes    int      `json:"total_times"`
	FixedDates    []string `json:"fixed_dates"`
	RelativeWeeks []int    `json:"relative_weeks"`
	Status        string   `json:"status"`
}

type EnrollmentResponse struct {
	PlanID       string         `json:"plan_id"`
	EnrollmentID string         `json:"enrollment_id"`
	Round        uint32         `json:"round"`
	Idempotent   bool           `json:"idempotent"`
	Tasks        []TaskResponse `json:"tasks"`
}

type HistoricalStageRecord struct {
	ID           uint64          `json:"id"`
	OrgID        uint64          `json:"org_id"`
	BatchID      string          `json:"batch_id"`
	ScenarioID   string          `json:"scenario_id"`
	Stage        string          `json:"stage"`
	PayloadHash  string          `json:"payload_hash"`
	Status       string          `json:"status"`
	BusinessAt   time.Time       `json:"business_at"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	PayloadJSON  json.RawMessage `json:"payload_json,omitempty"`
}

type HistoricalStageBatchResponse struct {
	BatchID string                  `json:"batch_id"`
	Offset  int                     `json:"offset"`
	Limit   int                     `json:"limit"`
	Stages  []HistoricalStageRecord `json:"stages"`
}

// TaskResponse 计划任务响应。
type TaskResponse struct {
	ID           string  `json:"id"`
	PlanID       string  `json:"plan_id"`
	Seq          int     `json:"seq"`
	OrgID        int64   `json:"org_id"`
	TesteeID     string  `json:"testee_id"`
	ScaleCode    string  `json:"scale_code"`
	PlannedAt    string  `json:"planned_at"`
	OpenAt       *string `json:"open_at,omitempty"`
	ExpireAt     *string `json:"expire_at,omitempty"`
	CompletedAt  *string `json:"completed_at,omitempty"`
	Status       string  `json:"status"`
	AssessmentID *string `json:"assessment_id,omitempty"`
	EntryToken   string  `json:"entry_token,omitempty"`
	EntryURL     string  `json:"entry_url,omitempty"`
}

// PlanTaskWindowResponse 任务窗口响应。
type PlanTaskWindowResponse struct {
	Tasks    []TaskResponse `json:"tasks"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	HasMore  bool           `json:"has_more"`
}

// ListPlanTaskWindowRequest 查询任务窗口请求。
type ListPlanTaskWindowRequest struct {
	PlanID        string   `json:"plan_id"`
	Status        string   `json:"status,omitempty"`
	TesteeIDs     []string `json:"testee_ids,omitempty"`
	PlannedAfter  string   `json:"planned_after,omitempty"`
	PlannedBefore string   `json:"planned_before,omitempty"`
	Page          int      `json:"page,omitempty"`
	PageSize      int      `json:"page_size,omitempty"`
}

type EnrollTesteeRequest struct {
	PlanID    string `json:"plan_id"`
	TesteeID  string `json:"testee_id"`
	StartDate string `json:"start_date"`
}

// ApiserverTesteeResponse 受试者响应（apiserver）
type ApiserverTesteeResponse struct {
	ID        string             `json:"id"`
	OrgID     string             `json:"org_id,omitempty"`
	ProfileID *string            `json:"profile_id,omitempty"`
	Name      string             `json:"name"`
	Gender    string             `json:"gender,omitempty"`
	Birthday  *time.Time         `json:"birthday,omitempty"`
	Tags      []string           `json:"tags,omitempty"`
	Source    string             `json:"source,omitempty"`
	Guardians []GuardianResponse `json:"guardians,omitempty"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

func (r *ApiserverTesteeResponse) UnmarshalJSON(data []byte) error {
	type alias struct {
		ID        string             `json:"id"`
		OrgID     string             `json:"org_id,omitempty"`
		ProfileID *string            `json:"profile_id,omitempty"`
		Name      string             `json:"name"`
		Gender    string             `json:"gender,omitempty"`
		Birthday  *string            `json:"birthday,omitempty"`
		Tags      []string           `json:"tags,omitempty"`
		Source    string             `json:"source,omitempty"`
		Guardians []GuardianResponse `json:"guardians,omitempty"`
		CreatedAt string             `json:"created_at"`
		UpdatedAt string             `json:"updated_at"`
	}

	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	birthday, err := parseFlexibleSeedNullableTime(raw.Birthday)
	if err != nil {
		return fmt.Errorf("parse birthday: %w", err)
	}
	createdAt, err := parseFlexibleSeedOptionalTimeValue(raw.CreatedAt)
	if err != nil {
		return fmt.Errorf("parse created_at: %w", err)
	}
	updatedAt, err := parseFlexibleSeedOptionalTimeValue(raw.UpdatedAt)
	if err != nil {
		return fmt.Errorf("parse updated_at: %w", err)
	}

	r.ID = raw.ID
	r.OrgID = raw.OrgID
	r.ProfileID = raw.ProfileID
	r.Name = raw.Name
	r.Gender = raw.Gender
	r.Birthday = birthday
	r.Tags = append([]string(nil), raw.Tags...)
	r.Source = raw.Source
	r.Guardians = append([]GuardianResponse(nil), raw.Guardians...)
	r.CreatedAt = createdAt
	r.UpdatedAt = updatedAt
	return nil
}

// GuardianResponse 受试者监护人响应（apiserver）。
type GuardianResponse struct {
	Name     string `json:"name"`
	Relation string `json:"relation,omitempty"`
	Phone    string `json:"phone,omitempty"`
}

// ApiserverTesteeListResponse 受试者列表响应（apiserver）
type ApiserverTesteeListResponse struct {
	Items      []*ApiserverTesteeResponse `json:"items"`
	Total      int64                      `json:"total"`
	Page       int                        `json:"page"`
	PageSize   int                        `json:"page_size"`
	TotalPages int                        `json:"total_pages"`
}

// TesteeResponse 受试者响应（collection-server）
type TesteeResponse struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	IAMUserID        string    `json:"iam_user_id,omitempty"`
	IAMProfileID     string    `json:"iam_profile_id,omitempty"`
	IAMProfileLinkID string    `json:"iam_profile_link_id,omitempty"`
	CreatedAt        time.Time `json:"created_at,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
}

func (r *TesteeResponse) UnmarshalJSON(data []byte) error {
	type alias struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		IAMUserID        string `json:"iam_user_id,omitempty"`
		IAMProfileID     string `json:"iam_profile_id,omitempty"`
		IAMProfileLinkID string `json:"iam_profile_link_id,omitempty"`
		CreatedAt        string `json:"created_at,omitempty"`
		UpdatedAt        string `json:"updated_at,omitempty"`
	}

	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var createdAt time.Time
	var updatedAt time.Time
	var err error
	if strings.TrimSpace(raw.CreatedAt) != "" {
		createdAt, err = parseFlexibleSeedRequiredTime(raw.CreatedAt)
		if err != nil {
			return fmt.Errorf("parse created_at: %w", err)
		}
	}
	if strings.TrimSpace(raw.UpdatedAt) != "" {
		updatedAt, err = parseFlexibleSeedRequiredTime(raw.UpdatedAt)
		if err != nil {
			return fmt.Errorf("parse updated_at: %w", err)
		}
	}

	r.ID = raw.ID
	r.Name = raw.Name
	r.IAMUserID = strings.TrimSpace(raw.IAMUserID)
	r.IAMProfileID = strings.TrimSpace(raw.IAMProfileID)
	r.IAMProfileLinkID = strings.TrimSpace(raw.IAMProfileLinkID)
	r.CreatedAt = createdAt
	r.UpdatedAt = updatedAt
	return nil
}

// ClinicianResponse 临床医师响应（apiserver）。
type ClinicianResponse struct {
	ID                   string  `json:"id"`
	OrgID                string  `json:"org_id"`
	OperatorID           *string `json:"operator_id,omitempty"`
	Name                 string  `json:"name"`
	Department           string  `json:"department,omitempty"`
	Title                string  `json:"title,omitempty"`
	ClinicianType        string  `json:"clinician_type"`
	EmployeeCode         string  `json:"employee_code,omitempty"`
	IsActive             bool    `json:"is_active"`
	AssignedTesteeCount  int64   `json:"assigned_testee_count"`
	AssessmentEntryCount int64   `json:"assessment_entry_count"`
}

// AssessmentEntryResponse 测评入口响应（apiserver）。
type AssessmentEntryResponse struct {
	ID            string     `json:"id"`
	OrgID         string     `json:"org_id"`
	ClinicianID   string     `json:"clinician_id"`
	Token         string     `json:"token"`
	TargetType    string     `json:"target_type"`
	TargetCode    string     `json:"target_code"`
	TargetVersion string     `json:"target_version,omitempty"`
	IsActive      bool       `json:"is_active"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	QRCodeURL     string     `json:"qrcode_url,omitempty"`
}

func (r *AssessmentEntryResponse) UnmarshalJSON(data []byte) error {
	type alias struct {
		ID            string  `json:"id"`
		OrgID         string  `json:"org_id"`
		ClinicianID   string  `json:"clinician_id"`
		Token         string  `json:"token"`
		TargetType    string  `json:"target_type"`
		TargetCode    string  `json:"target_code"`
		TargetVersion string  `json:"target_version,omitempty"`
		IsActive      bool    `json:"is_active"`
		ExpiresAt     *string `json:"expires_at,omitempty"`
		QRCodeURL     string  `json:"qrcode_url,omitempty"`
	}

	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	expiresAt, err := parseFlexibleSeedNullableTime(raw.ExpiresAt)
	if err != nil {
		return fmt.Errorf("parse expires_at: %w", err)
	}

	r.ID = raw.ID
	r.OrgID = raw.OrgID
	r.ClinicianID = raw.ClinicianID
	r.Token = raw.Token
	r.TargetType = raw.TargetType
	r.TargetCode = raw.TargetCode
	r.TargetVersion = raw.TargetVersion
	r.IsActive = raw.IsActive
	r.ExpiresAt = expiresAt
	r.QRCodeURL = raw.QRCodeURL
	return nil
}

// AssessmentEntryListResponse 测评入口列表响应（apiserver）。
type AssessmentEntryListResponse struct {
	Items      []*AssessmentEntryResponse `json:"items"`
	Total      int64                      `json:"total"`
	Page       int                        `json:"page"`
	PageSize   int                        `json:"page_size"`
	TotalPages int                        `json:"total_pages"`
}

// RelationResponse 从业者关系响应（apiserver）。
type RelationResponse struct {
	ID           string     `json:"id"`
	OrgID        string     `json:"org_id"`
	ClinicianID  string     `json:"clinician_id"`
	TesteeID     string     `json:"testee_id"`
	RelationType string     `json:"relation_type"`
	SourceType   string     `json:"source_type"`
	SourceID     *string    `json:"source_id,omitempty"`
	IsActive     bool       `json:"is_active"`
	BoundAt      time.Time  `json:"bound_at"`
	UnboundAt    *time.Time `json:"unbound_at,omitempty"`
}

func (r *RelationResponse) UnmarshalJSON(data []byte) error {
	type alias struct {
		ID           string  `json:"id"`
		OrgID        string  `json:"org_id"`
		ClinicianID  string  `json:"clinician_id"`
		TesteeID     string  `json:"testee_id"`
		RelationType string  `json:"relation_type"`
		SourceType   string  `json:"source_type"`
		SourceID     *string `json:"source_id,omitempty"`
		IsActive     bool    `json:"is_active"`
		BoundAt      string  `json:"bound_at"`
		UnboundAt    *string `json:"unbound_at,omitempty"`
	}

	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	boundAt, err := parseFlexibleSeedRequiredTime(raw.BoundAt)
	if err != nil {
		return fmt.Errorf("parse bound_at: %w", err)
	}
	unboundAt, err := parseFlexibleSeedNullableTime(raw.UnboundAt)
	if err != nil {
		return fmt.Errorf("parse unbound_at: %w", err)
	}

	r.ID = raw.ID
	r.OrgID = raw.OrgID
	r.ClinicianID = raw.ClinicianID
	r.TesteeID = raw.TesteeID
	r.RelationType = raw.RelationType
	r.SourceType = raw.SourceType
	r.SourceID = raw.SourceID
	r.IsActive = raw.IsActive
	r.BoundAt = boundAt
	r.UnboundAt = unboundAt
	return nil
}

// TesteeClinicianRelationResponse 受试者-从业者关系响应（apiserver）。
type TesteeClinicianRelationResponse struct {
	Clinician *ClinicianResponse `json:"clinician"`
	Relation  *RelationResponse  `json:"relation"`
}

// TesteeClinicianRelationListResponse 受试者-从业者关系列表响应（apiserver）。
type TesteeClinicianRelationListResponse struct {
	Items []*TesteeClinicianRelationResponse `json:"items"`
}

// CreateAssessmentEntryRequest 创建测评入口请求（apiserver）。
type CreateAssessmentEntryRequest struct {
	TargetType    string     `json:"target_type"`
	TargetCode    string     `json:"target_code"`
	TargetVersion string     `json:"target_version,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

// IntakeAssessmentEntryRequest 公开入口 intake 请求（apiserver）。
type IntakeAssessmentEntryRequest struct {
	ProfileID *uint64    `json:"profile_id,omitempty"`
	Name      string     `json:"name"`
	Gender    string     `json:"gender,omitempty"`
	Birthday  *time.Time `json:"birthday,omitempty"`
}

// AssessmentEntryResolvedResponse 公开入口解析响应（apiserver）。
type AssessmentEntryResolvedResponse struct {
	Entry     *AssessmentEntryResponse `json:"entry"`
	Clinician *ClinicianResponse       `json:"clinician"`
}

// AssessmentEntryIntakeResponse 公开入口 intake 响应（apiserver）。
type AssessmentEntryIntakeResponse struct {
	Entry      *AssessmentEntryResponse `json:"entry"`
	Clinician  *ClinicianResponse       `json:"clinician"`
	Testee     *ApiserverTesteeResponse `json:"testee"`
	Relation   *RelationResponse        `json:"relation,omitempty"`
	Assignment *RelationResponse        `json:"assignment,omitempty"`
}

func parseFlexibleSeedRequiredTime(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, fmt.Errorf("time is empty")
	}
	return parseFlexibleSeedTime(value)
}

func parseFlexibleSeedOptionalTimeValue(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, nil
	}
	return parseFlexibleSeedTime(value)
}

func parseFlexibleSeedNullableTime(raw *string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	value := strings.TrimSpace(*raw)
	if value == "" {
		return nil, nil
	}
	parsed, err := parseFlexibleSeedTime(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseFlexibleSeedTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	layouts := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02",
	}

	var parsed time.Time
	var err error
	for _, layout := range layouts {
		parsed, err = time.Parse(layout, raw)
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, err
}

type EnsureIAMMockConsumerRequest = authsignup.EnsureMockConsumerRequest
type EnsureIAMMockConsumerResponse = authsignup.EnsureMockConsumerResult

// CollectionCreateTesteeRequest 创建 collection 受试者请求。
type CollectionCreateTesteeRequest struct {
	Name         string   `json:"name"`
	Gender       int32    `json:"gender"`
	Birthday     string   `json:"birthday,omitempty"`
	IDCardNumber string   `json:"id_card_number,omitempty"`
	Relation     string   `json:"relation,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Source       string   `json:"source,omitempty"`
	IsKeyFocus   bool     `json:"is_key_focus,omitempty"`
}

// CollectionSubmitAcceptedResponse collection 答卷异步受理响应。
type CollectionSubmitAcceptedResponse struct {
	Status        string `json:"status"`
	RequestID     string `json:"request_id"`
	AnswerSheetID string `json:"answersheet_id"`
}

// AssessmentReadinessResponse collection Assessment 就绪响应。
type AssessmentReadinessResponse struct {
	Status          string `json:"status"`
	AnswerSheetID   string `json:"answersheet_id"`
	AssessmentID    string `json:"assessment_id,omitempty"`
	NextPollAfterMs int    `json:"next_poll_after_ms,omitempty"`
}

type AssessmentReportStatusResponse struct {
	Status          string `json:"status"`
	Stage           string `json:"stage,omitempty"`
	Message         string `json:"message,omitempty"`
	Reason          string `json:"reason,omitempty"`
	NextPollAfterMs int    `json:"next_poll_after_ms,omitempty"`
	UpdatedAt       int64  `json:"updated_at"`
}

// QuestionnaireDetailResponse 问卷详情响应（collection-server）
type QuestionnaireDetailResponse struct {
	Code      string             `json:"code"`
	Title     string             `json:"title"`
	Status    string             `json:"status"`
	Version   string             `json:"version"`
	Type      string             `json:"type"`
	Questions []QuestionResponse `json:"questions"`
}

// QuestionResponse 问题响应（collection-server）
type QuestionResponse struct {
	Code            string                   `json:"code"`
	Type            string                   `json:"type"`
	Title           string                   `json:"title"`
	Options         []OptionResponse         `json:"options"`
	ValidationRules []ValidationRuleResponse `json:"validation_rules,omitempty"`
}

// OptionResponse 选项响应（collection-server）
type OptionResponse struct {
	Code    string `json:"code"`
	Content string `json:"content"`
	Score   int32  `json:"score"`
}

// ValidationRuleResponse 问题校验规则响应（collection-server）。
type ValidationRuleResponse struct {
	RuleType    string `json:"rule_type"`
	TargetValue string `json:"target_value"`
}

// SubmitAnswerSheetRequest 提交答卷请求（collection-server）
type SubmitAnswerSheetRequest struct {
	QuestionnaireCode    string     `json:"questionnaire_code"`
	QuestionnaireVersion string     `json:"questionnaire_version"`
	IdempotencyKey       string     `json:"idempotency_key"`
	Title                string     `json:"title"`
	TesteeID             uint64     `json:"testee_id"`
	TaskID               string     `json:"task_id,omitempty"`
	OriginRef            *OriginRef `json:"origin_ref,omitempty"`
	Answers              []Answer   `json:"answers"`
}

type OriginRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// AdminSubmitAnswerSheetRequest 管理员提交答卷请求（apiserver）
type AdminSubmitAnswerSheetRequest struct {
	QuestionnaireCode    string     `json:"questionnaire_code"`
	QuestionnaireVersion string     `json:"questionnaire_version"`
	IdempotencyKey       string     `json:"idempotency_key,omitempty"`
	Title                string     `json:"title"`
	TesteeID             uint64     `json:"testee_id"`
	TaskID               string     `json:"task_id,omitempty"`
	OriginRef            *OriginRef `json:"origin_ref,omitempty"`
	WriterID             uint64     `json:"writer_id,omitempty"`
	FillerID             uint64     `json:"filler_id,omitempty"`
	Answers              []Answer   `json:"answers"`
}

// Answer 提交答案
// 注意：Value 使用 interface{} 类型，以支持不同类型的问题答案：
// - Radio: string (选项 code)
// - Checkbox: []string (选项 code 数组)
// - Text/Textarea: string
// - Number: number (float64)
// 注意：Score 字段在 admin-submit 接口中会被忽略，但保留以兼容其他接口
type Answer struct {
	QuestionCode string      `json:"question_code"`
	QuestionType string      `json:"question_type"`
	Score        uint32      `json:"score,omitempty"` // 使用 omitempty，避免发送不必要的字段
	Value        interface{} `json:"value"`
}

// SubmitAnswerSheetResponse 提交答卷响应。
// collection 异步提交完成后 id 为 answersheet_id；admin-submit 同步返回答卷 id。
type SubmitAnswerSheetResponse struct {
	ID           string `json:"id"`
	AssessmentID string `json:"assessment_id,omitempty"`
	Message      string `json:"message,omitempty"`
}

// AdminAnswerSheetListItem 管理端答卷列表项（对齐 AnswerSheetSummaryItem）。
type AdminAnswerSheetListItem struct {
	ID                string `json:"id"`
	QuestionnaireCode string `json:"questionnaire_code"`
	QuestionnaireVer  string `json:"questionnaire_ver,omitempty"`
	Title             string `json:"title"`
	FillerID          string `json:"filler_id"`
	FilledAt          string `json:"filled_at,omitempty"`
}

// AdminAnswerSheetListResponse 管理端答卷列表响应。
type AdminAnswerSheetListResponse struct {
	Total int64                      `json:"total"`
	Items []AdminAnswerSheetListItem `json:"items"`
}

// doRequest 执行 HTTP 请求
func (c *APIClient) doRequest(ctx context.Context, method, path string, body interface{}) (*Response, error) {
	return c.doRequestWithHeadersRetryAndTimeout(ctx, method, path, body, nil, true, c.httpClient.Timeout)
}

func (c *APIClient) doRequestWithRetryAndTimeout(ctx context.Context, method, path string, body interface{}, allowRefresh bool, timeout time.Duration) (*Response, error) {
	return c.doRequestWithHeadersRetryAndTimeout(ctx, method, path, body, nil, allowRefresh, timeout)
}

func (c *APIClient) doRequestWithHeadersRetryAndTimeout(
	ctx context.Context,
	method, path string,
	body interface{},
	headers map[string]string,
	allowRefresh bool,
	timeout time.Duration,
) (*Response, error) {
	return c.doRequestWithHeadersRetryTimeoutAndLimit(ctx, method, path, body, headers, allowRefresh, timeout, c.retryMax)
}

func (c *APIClient) doRequestWithRetryTimeoutAndLimit(
	ctx context.Context,
	method, path string,
	body interface{},
	allowRefresh bool,
	timeout time.Duration,
	retryMax int,
) (*Response, error) {
	return c.doRequestWithHeadersRetryTimeoutAndLimit(ctx, method, path, body, nil, allowRefresh, timeout, retryMax)
}

func (c *APIClient) doRequestWithHeadersRetryTimeoutAndLimit(
	ctx context.Context,
	method, path string,
	body interface{},
	headers map[string]string,
	allowRefresh bool,
	timeout time.Duration,
	retryMax int,
) (*Response, error) {
	url := c.baseURL + path
	httpClient := *c.httpClient
	if timeout > 0 {
		httpClient.Timeout = timeout
	}
	if retryMax < 0 {
		retryMax = 0
	}
	for attempt := 0; attempt <= retryMax; attempt++ {
		if err := c.ensureFreshToken(ctx); err != nil {
			return nil, err
		}

		var reqBody io.Reader
		var rawBody []byte
		if body != nil {
			jsonData, err := json.Marshal(body)
			if err != nil {
				return nil, fmt.Errorf("marshal request body: %w", err)
			}
			rawBody = jsonData
			reqBody = bytes.NewBuffer(rawBody)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		authToken := c.getToken()
		if authToken != "" {
			req.Header.Set("Authorization", "Bearer "+authToken)
		}
		for key, value := range headers {
			if strings.TrimSpace(key) == "" {
				continue
			}
			req.Header.Set(key, value)
		}
		if historical, ok := historicalseed.FromContext(ctx); ok && len(c.historicalSecret) > 0 {
			signedHeaders, err := historicalseed.HeadersFor(method, req.URL.RequestURI(), rawBody, historical, time.Now(), c.historicalSecret)
			if err != nil {
				return nil, err
			}
			for key, value := range signedHeaders {
				req.Header.Set(key, value)
			}
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if shouldRetryNetErr(err) && attempt < retryMax {
				delay := backoffDelay(attempt, c.retryMinDelay, c.retryMaxDelay)
				c.logger.Warnw("seeddata request failed, retrying",
					"url", url,
					"attempt", attempt+1,
					"delay_ms", delay.Milliseconds(),
					"error", err.Error(),
				)
				if err := scheduler.Wait(ctx, delay); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("do request: %w", err)
		}

		respBody, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read response: %w", readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close response: %w", closeErr)
		}

		if isHTTPSuccess(resp.StatusCode) {
			var apiResp Response
			if err := decodeAPIResponse(respBody, &apiResp); err != nil {
				bodyStr := string(respBody)
				if len(bodyStr) > 200 {
					bodyStr = bodyStr[:200] + "..."
				}
				return nil, fmt.Errorf("unmarshal response: %w, url=%s, body=%s", err, url, bodyStr)
			}
			if apiResp.Code != 0 {
				return nil, fmt.Errorf("api error: code=%d, message=%s, http_status=%d", apiResp.Code, apiResp.Message, resp.StatusCode)
			}
			return &apiResp, nil
		}

		if resp.StatusCode == http.StatusUnauthorized {
			if allowRefresh && c.refresher != nil {
				if err := c.refreshToken(ctx); err == nil {
					return c.doRequestWithHeadersRetryTimeoutAndLimit(ctx, method, path, body, headers, false, timeout, retryMax)
				}
			}
			return nil, fmt.Errorf(
				"authentication failed (401): please check your API token. auth_token_present=%t token_length=%d url=%s message=%s",
				strings.TrimSpace(authToken) != "",
				len(strings.TrimSpace(authToken)),
				url,
				responseErrorMessage(respBody),
			)
		}

		if isRetryableStatus(resp.StatusCode) && attempt < retryMax {
			delay, ok := parseRetryAfter(resp.Header.Get("Retry-After"))
			if !ok {
				delay = backoffDelay(attempt, c.retryMinDelay, c.retryMaxDelay)
			}
			if delay > c.retryMaxDelay {
				delay = c.retryMaxDelay
			}
			c.logger.Warnw("seeddata request throttled, retrying",
				"url", url,
				"status", resp.StatusCode,
				"attempt", attempt+1,
				"delay_ms", delay.Milliseconds(),
			)
			if err := scheduler.Wait(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}

		var apiResp Response
		if err := json.Unmarshal(respBody, &apiResp); err == nil {
			bodyStr := string(respBody)
			if resp.StatusCode == http.StatusInternalServerError {
				c.logger.Warnw("API returned 500 error",
					"url", url,
					"code", apiResp.Code,
					"message", apiResp.Message,
					"full_response", bodyStr,
				)
			}
			if len(bodyStr) > 500 {
				bodyStr = bodyStr[:500] + "..."
			}
			return nil, fmt.Errorf("api error: http_status=%d, code=%d, message=%s, body=%s", resp.StatusCode, apiResp.Code, apiResp.Message, bodyStr)
		}

		bodyStr := string(respBody)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return nil, fmt.Errorf("http error: status=%d, url=%s, body=%s", resp.StatusCode, url, bodyStr)
	}

	return nil, fmt.Errorf("request failed after retries: url=%s", url)
}

func isHTTPSuccess(statusCode int) bool {
	return statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices
}

func (c *APIClient) EnsureIAMMockConsumer(
	ctx context.Context,
	path string,
	req EnsureIAMMockConsumerRequest,
	sharedSecret string,
) (*EnsureIAMMockConsumerResponse, error) {
	headers := map[string]string{
		authsignup.SeedMockSecretHeader: strings.TrimSpace(sharedSecret),
	}
	resp, err := c.doRequestWithHeadersRetryAndTimeout(ctx, http.MethodPost, path, req, headers, false, c.httpClient.Timeout)
	if err != nil {
		return nil, err
	}
	var result EnsureIAMMockConsumerResponse
	if err := decodeResponseData(resp, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func defaultRetryConfig() (int, time.Duration, time.Duration) {
	return 3, 200 * time.Millisecond, 5 * time.Second
}

func shouldRetryNetErr(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(value); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(value); err == nil {
		delay := time.Until(t)
		if delay < 0 {
			return 0, false
		}
		return delay, true
	}
	return 0, false
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(strings.TrimSpace(value))
}

func isAPIHTTPStatus(err error, status int) bool {
	if err == nil {
		return false
	}
	statusToken := fmt.Sprintf("http_status=%d", status)
	if strings.Contains(err.Error(), statusToken) {
		return true
	}
	return strings.Contains(err.Error(), fmt.Sprintf("status=%d", status))
}

func backoffDelay(attempt int, minDelay, maxDelay time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := minDelay << attempt
	if delay > maxDelay {
		delay = maxDelay
	}
	jitter := time.Duration(time.Now().UnixNano()%int64(delay/2+1)) - delay/4
	return delay + jitter
}

func (c *APIClient) refreshToken(ctx context.Context) error {
	current := strings.TrimSpace(c.getLocalToken())
	if current == "" {
		current = c.getToken()
	}
	if c.provider != nil {
		token, err := c.provider.Refresh(ctx, current)
		if err != nil {
			return err
		}
		c.SetToken(token)
		return nil
	}
	if c.refresher == nil {
		return fmt.Errorf("token refresher not configured")
	}
	token, err := c.refresher(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("token refresher returned empty token")
	}
	c.SetToken(token)
	return nil
}

func (c *APIClient) ensureFreshToken(ctx context.Context) error {
	if c.provider == nil {
		return nil
	}

	remainingTTL := c.provider.RemainingTTL(time.Now())
	refreshed, err := c.provider.RefreshIfNeeded(ctx, seedTokenRefreshSkew)
	if err != nil {
		return fmt.Errorf("refresh api token before request: %w", err)
	}
	if refreshed {
		c.SetToken(c.provider.Token())
		c.logger.Infow("Seeddata proactively refreshed API token",
			"base_url", c.baseURL,
			"previous_remaining_seconds", int64(remainingTTL/time.Second),
			"expires_at", c.provider.ExpiresAt(),
		)
	}
	return nil
}

// decodeAPIResponse keeps JSON numbers lossless while the generic response
// envelope is decoded through interface{}. Historical resource IDs exceed the
// exact integer range of float64 and are marshalled into typed response data in
// a second step by decodeResponseData.
func decodeAPIResponse(body []byte, out *Response) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("response contains multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeResponseData(resp *Response, out interface{}) error {
	dataBytes, err := json.Marshal(resp.Data)
	if err != nil {
		return fmt.Errorf("marshal response data: %w", err)
	}
	if err := json.Unmarshal(dataBytes, out); err != nil {
		return fmt.Errorf("unmarshal response data: %w", err)
	}
	return nil
}
