package historicalseed

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	Version1          = 1
	HeaderContext     = "X-QS-Historical-Context"
	HeaderRequestedAt = "X-QS-Historical-Requested-At"
	HeaderSignature   = "X-QS-Historical-Signature"
)

type Timeline struct {
	TesteeCreatedAt       *time.Time `json:"testee_created_at,omitempty"`
	EntryResolvedAt       *time.Time `json:"entry_resolved_at,omitempty"`
	EntryIntakeAt         *time.Time `json:"entry_intake_at,omitempty"`
	EnrollmentJoinedAt    *time.Time `json:"enrollment_joined_at,omitempty"`
	TaskOpenedAt          *time.Time `json:"task_opened_at,omitempty"`
	TaskCompletedAt       *time.Time `json:"task_completed_at,omitempty"`
	AnswerSheetFilledAt   *time.Time `json:"answersheet_filled_at,omitempty"`
	AssessmentCreatedAt   *time.Time `json:"assessment_created_at,omitempty"`
	AssessmentSubmittedAt *time.Time `json:"assessment_submitted_at,omitempty"`
	EvaluatedAt           *time.Time `json:"evaluated_at,omitempty"`
	ReportGeneratedAt     *time.Time `json:"report_generated_at,omitempty"`
}

type Context struct {
	BatchID    string   `json:"batch_id"`
	ScenarioID string   `json:"scenario_id"`
	OrgID      uint64   `json:"org_id"`
	Version    int      `json:"version"`
	Timeline   Timeline `json:"timeline"`
}

type contextKey struct{}

func WithContext(ctx context.Context, historical Context) context.Context {
	return context.WithValue(ctx, contextKey{}, historical)
}

func FromContext(ctx context.Context) (Context, bool) {
	if ctx == nil {
		return Context{}, false
	}
	historical, ok := ctx.Value(contextKey{}).(Context)
	return historical, ok
}

func HeadersFor(method, requestURI string, body []byte, historical Context, requestedAt time.Time, secret []byte) (map[string]string, error) {
	if strings.TrimSpace(historical.BatchID) == "" || strings.TrimSpace(historical.ScenarioID) == "" || historical.OrgID == 0 || historical.Version != Version1 {
		return nil, fmt.Errorf("invalid historical seed context identity")
	}
	if len(secret) == 0 {
		return nil, fmt.Errorf("historical seed HMAC secret is empty")
	}
	payload, err := json.Marshal(historical)
	if err != nil {
		return nil, fmt.Errorf("marshal historical seed context: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	requested := requestedAt.Format(time.RFC3339Nano)
	return map[string]string{
		HeaderContext:     encoded,
		HeaderRequestedAt: requested,
		HeaderSignature:   Sign(method, requestURI, body, requested, encoded, secret),
	}, nil
}

func Sign(method, requestURI string, body []byte, requestedAt, encodedContext string, secret []byte) string {
	bodyHash := sha256.Sum256(body)
	canonical := strings.Join([]string{
		strings.ToUpper(strings.TrimSpace(method)),
		requestURI,
		hex.EncodeToString(bodyHash[:]),
		requestedAt,
		encodedContext,
	}, "\n")
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}
