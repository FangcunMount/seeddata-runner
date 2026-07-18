package seedapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var safeSubmissionIdempotencyKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

type collectionSubmitAnswer struct {
	QuestionCode string `json:"question_code"`
	QuestionType string `json:"question_type"`
	Score        uint32 `json:"score,omitempty"`
	Value        string `json:"value"`
}

type collectionSubmitAnswerSheetRequest struct {
	QuestionnaireCode    string                   `json:"questionnaire_code"`
	QuestionnaireVersion string                   `json:"questionnaire_version"`
	IdempotencyKey       string                   `json:"idempotency_key"`
	Title                string                   `json:"title"`
	TesteeID             uint64                   `json:"testee_id"`
	TaskID               string                   `json:"task_id,omitempty"`
	Answers              []collectionSubmitAnswer `json:"answers"`
}

// AcceptCollectionAnswerSheet durably accepts an AnswerSheet through collection-server.
// Submission retries are intentionally owned by the caller so every attempt can persist
// a fresh request ID while reusing the same business idempotency key.
func (c *APIClient) AcceptCollectionAnswerSheet(ctx context.Context, req SubmitAnswerSheetRequest, requestID string) (*CollectionSubmitAcceptedResponse, error) {
	wireReq, err := toCollectionSubmitAnswerSheetRequest(req)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{}
	if requestID = strings.TrimSpace(requestID); requestID != "" {
		headers["X-Request-ID"] = requestID
	}
	resp, err := c.doRequestWithHeadersRetryTimeoutAndLimit(
		ctx, http.MethodPost, "/api/v1/answersheets", wireReq, headers, true, c.httpClient.Timeout, 0,
	)
	if err != nil {
		return nil, err
	}

	var accepted CollectionSubmitAcceptedResponse
	if err := decodeResponseData(resp, &accepted); err != nil {
		return nil, fmt.Errorf("decode collection submit accepted response: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(accepted.Status), "accepted") {
		return nil, fmt.Errorf("collection submit returned unexpected status %q", accepted.Status)
	}
	if strings.TrimSpace(accepted.AnswerSheetID) == "" {
		return nil, fmt.Errorf("collection submit accepted without answersheet_id")
	}
	return &accepted, nil
}

// GetAssessmentReadiness queries the only supported AnswerSheet-to-Assessment path.
func (c *APIClient) GetAssessmentReadiness(ctx context.Context, answerSheetID string, testeeID uint64) (*AssessmentReadinessResponse, error) {
	answerSheetID = strings.TrimSpace(answerSheetID)
	if answerSheetID == "" {
		return nil, fmt.Errorf("answersheet_id is required")
	}
	if testeeID == 0 {
		return nil, fmt.Errorf("testee_id is required")
	}
	path := fmt.Sprintf(
		"/api/v1/answersheets/%s/assessment-readiness?testee_id=%d",
		urlQueryEscape(answerSheetID),
		testeeID,
	)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var readiness AssessmentReadinessResponse
	if err := decodeResponseData(resp, &readiness); err != nil {
		return nil, fmt.Errorf("decode assessment readiness response: %w", err)
	}
	return &readiness, nil
}

func toCollectionSubmitAnswerSheetRequest(req SubmitAnswerSheetRequest) (collectionSubmitAnswerSheetRequest, error) {
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if !safeSubmissionIdempotencyKey.MatchString(req.IdempotencyKey) {
		return collectionSubmitAnswerSheetRequest{}, fmt.Errorf("idempotency_key must contain 8-128 safe characters")
	}
	answers := make([]collectionSubmitAnswer, 0, len(req.Answers))
	for _, answer := range req.Answers {
		value, err := marshalCollectionAnswerValue(answer.Value)
		if err != nil {
			return collectionSubmitAnswerSheetRequest{}, fmt.Errorf("marshal collection answer value for question %s: %w", answer.QuestionCode, err)
		}
		answers = append(answers, collectionSubmitAnswer{
			QuestionCode: answer.QuestionCode,
			QuestionType: answer.QuestionType,
			Score:        answer.Score,
			Value:        value,
		})
	}

	return collectionSubmitAnswerSheetRequest{
		QuestionnaireCode:    req.QuestionnaireCode,
		QuestionnaireVersion: req.QuestionnaireVersion,
		IdempotencyKey:       req.IdempotencyKey,
		Title:                req.Title,
		TesteeID:             req.TesteeID,
		TaskID:               req.TaskID,
		Answers:              answers,
	}, nil
}

func marshalCollectionAnswerValue(value interface{}) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case nil:
		return "", nil
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
}

// SubmitAnswerSheetAdminAttempt performs exactly one HTTP attempt with an optional request ID.
func (c *APIClient) SubmitAnswerSheetAdminAttempt(ctx context.Context, req AdminSubmitAnswerSheetRequest, requestID string, timeout time.Duration) (*SubmitAnswerSheetResponse, error) {
	headers := map[string]string{}
	if requestID = strings.TrimSpace(requestID); requestID != "" {
		headers["X-Request-ID"] = requestID
	}
	resp, err := c.doRequestWithHeadersRetryTimeoutAndLimit(
		ctx, http.MethodPost, "/api/v1/answersheets/admin-submit", req, headers, true, timeout, 0,
	)
	if err != nil {
		return nil, err
	}
	var submitResp SubmitAnswerSheetResponse
	if err := decodeResponseData(resp, &submitResp); err != nil {
		return nil, fmt.Errorf("decode admin submit response: %w", err)
	}
	return &submitResp, nil
}

// ListAdminAnswerSheets 查询管理端答卷列表（apiserver）。
func (c *APIClient) ListAdminAnswerSheets(ctx context.Context, questionnaireCode string, fillerID uint64, page, pageSize int) (*AdminAnswerSheetListResponse, error) {
	path := fmt.Sprintf(
		"/api/v1/answersheets?page=%d&page_size=%d&questionnaire_code=%s&filler_id=%d",
		page, pageSize, urlQueryEscape(questionnaireCode), fillerID,
	)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var listResp AdminAnswerSheetListResponse
	if err := decodeResponseData(resp, &listResp); err != nil {
		return nil, fmt.Errorf("decode admin answersheet list response: %w", err)
	}
	return &listResp, nil
}
