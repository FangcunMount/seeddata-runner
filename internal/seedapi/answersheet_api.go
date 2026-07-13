package seedapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/FangcunMount/seeddata-runner/internal/scheduler"
)

const (
	collectionSubmitPollTimeout  = 2 * time.Minute
	collectionSubmitPollInterval = 500 * time.Millisecond
)

type collectionSubmitAnswer struct {
	QuestionCode string `json:"question_code"`
	QuestionType string `json:"question_type"`
	Score        uint32 `json:"score,omitempty"`
	Value        string `json:"value"`
}

type collectionSubmitAnswerSheetRequest struct {
	QuestionnaireCode    string                   `json:"questionnaire_code"`
	QuestionnaireVersion string                   `json:"questionnaire_version"`
	Title                string                   `json:"title"`
	TesteeID             uint64                   `json:"testee_id"`
	TaskID               string                   `json:"task_id,omitempty"`
	Answers              []collectionSubmitAnswer `json:"answers"`
}

// SubmitAnswerSheet 提交答卷（collection-server，异步 202 + submit-status 轮询）。
func (c *APIClient) SubmitAnswerSheet(ctx context.Context, req SubmitAnswerSheetRequest) (*SubmitAnswerSheetResponse, error) {
	wireReq, err := toCollectionSubmitAnswerSheetRequest(req)
	if err != nil {
		return nil, err
	}

	resp, err := c.doRequest(ctx, "POST", "/api/v1/answersheets", wireReq)
	if err != nil {
		return nil, err
	}

	var accepted CollectionSubmitAcceptedResponse
	if err := decodeResponseData(resp, &accepted); err != nil {
		return nil, fmt.Errorf("decode collection submit accepted response: %w", err)
	}
	requestID := strings.TrimSpace(accepted.RequestID)
	if requestID == "" {
		// 兼容偶发同步返回 {id,...} 的旧形态。
		var legacy SubmitAnswerSheetResponse
		if err := decodeResponseData(resp, &legacy); err == nil && strings.TrimSpace(legacy.ID) != "" {
			return &legacy, nil
		}
		return nil, fmt.Errorf("collection submit accepted without request_id")
	}

	status, err := c.waitCollectionAnswerSheetSubmit(ctx, requestID)
	if err != nil {
		return nil, err
	}
	return &SubmitAnswerSheetResponse{
		ID:           strings.TrimSpace(status.AnswerSheetID),
		AssessmentID: strings.TrimSpace(status.AssessmentID),
		Message:      strings.TrimSpace(status.Status),
	}, nil
}

func (c *APIClient) waitCollectionAnswerSheetSubmit(ctx context.Context, requestID string) (*CollectionSubmitStatusResponse, error) {
	deadline := time.Now().Add(collectionSubmitPollTimeout)
	var lastStatus, lastAnswerSheetID string
	for {
		status, err := c.GetAnswerSheetSubmitStatus(ctx, requestID)
		if err != nil {
			return nil, err
		}
		lastStatus = status.Status
		lastAnswerSheetID = status.AnswerSheetID
		switch strings.ToLower(strings.TrimSpace(status.Status)) {
		case "done":
			if strings.TrimSpace(status.AnswerSheetID) == "" {
				return nil, fmt.Errorf("collection submit done without answersheet_id: request_id=%s", requestID)
			}
			return status, nil
		case "failed":
			return nil, fmt.Errorf("collection submit failed: request_id=%s", requestID)
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf(
				"collection submit not ready before timeout: request_id=%s last_status=%s answersheet_id=%s",
				requestID,
				lastStatus,
				lastAnswerSheetID,
			)
		}
		if err := scheduler.Wait(ctx, collectionSubmitPollInterval); err != nil {
			return nil, err
		}
	}
}

// GetAnswerSheetSubmitStatus 查询 collection 答卷提交状态。
func (c *APIClient) GetAnswerSheetSubmitStatus(ctx context.Context, requestID string) (*CollectionSubmitStatusResponse, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, fmt.Errorf("request_id is required")
	}
	path := fmt.Sprintf("/api/v1/answersheets/submit-status?request_id=%s", urlQueryEscape(requestID))
	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	var status CollectionSubmitStatusResponse
	if err := decodeResponseData(resp, &status); err != nil {
		return nil, fmt.Errorf("decode collection submit status response: %w", err)
	}
	return &status, nil
}

func toCollectionSubmitAnswerSheetRequest(req SubmitAnswerSheetRequest) (collectionSubmitAnswerSheetRequest, error) {
	answers := make([]collectionSubmitAnswer, 0, len(req.Answers))
	for _, answer := range req.Answers {
		value, err := marshalCollectionAnswerValue(answer.Value)
		if err != nil {
			return collectionSubmitAnswerSheetRequest{}, fmt.Errorf(
				"marshal collection answer value for question %s: %w",
				answer.QuestionCode,
				err,
			)
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

// SubmitAnswerSheetAdmin 管理员提交答卷（apiserver）。
func (c *APIClient) SubmitAnswerSheetAdmin(ctx context.Context, req AdminSubmitAnswerSheetRequest) (*SubmitAnswerSheetResponse, error) {
	resp, err := c.doRequest(ctx, "POST", "/api/v1/answersheets/admin-submit", req)
	if err != nil {
		return nil, err
	}

	dataBytes, err := json.Marshal(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("marshal response data: %w", err)
	}

	var submitResp SubmitAnswerSheetResponse
	if err := json.Unmarshal(dataBytes, &submitResp); err != nil {
		return nil, fmt.Errorf("unmarshal submit response: %w", err)
	}

	return &submitResp, nil
}

// ListAdminAnswerSheets 查询管理端答卷列表（apiserver）。
func (c *APIClient) ListAdminAnswerSheets(ctx context.Context, questionnaireCode string, fillerID uint64, page, pageSize int) (*AdminAnswerSheetListResponse, error) {
	path := fmt.Sprintf(
		"/api/v1/answersheets?page=%d&page_size=%d&questionnaire_code=%s&filler_id=%d",
		page,
		pageSize,
		urlQueryEscape(questionnaireCode),
		fillerID,
	)
	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}

	var listResp AdminAnswerSheetListResponse
	if err := decodeResponseData(resp, &listResp); err != nil {
		return nil, fmt.Errorf("decode admin answersheet list response: %w", err)
	}
	return &listResp, nil
}

// SubmitAnswerSheetAdminWithPolicy 管理员提交答卷并覆盖超时/重试。
func (c *APIClient) SubmitAnswerSheetAdminWithPolicy(ctx context.Context, req AdminSubmitAnswerSheetRequest, timeout time.Duration, retryMax int) (*SubmitAnswerSheetResponse, error) {
	resp, err := c.doRequestWithRetryTimeoutAndLimit(ctx, "POST", "/api/v1/answersheets/admin-submit", req, true, timeout, retryMax)
	if err != nil {
		return nil, err
	}

	dataBytes, err := json.Marshal(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("marshal response data: %w", err)
	}

	var submitResp SubmitAnswerSheetResponse
	if err := json.Unmarshal(dataBytes, &submitResp); err != nil {
		return nil, fmt.Errorf("unmarshal submit response: %w", err)
	}

	return &submitResp, nil
}
