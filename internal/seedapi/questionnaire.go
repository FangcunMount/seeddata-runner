package seedapi

import (
	"context"
	"encoding/json"
	"fmt"
)

// GetScale 获取测评模型详情（兼容旧命名；实际调用 assessment-models）。
// apiserver 与 collection-server 均提供 GET /api/v1/assessment-models/{code}。
func (c *APIClient) GetScale(ctx context.Context, code string) (*ScaleResponse, error) {
	cacheKey := normalizeSeedCacheKey(code)
	if cacheKey != "" {
		c.scaleCacheMu.RLock()
		cached := c.scaleCache[cacheKey]
		c.scaleCacheMu.RUnlock()
		if cached != nil {
			cloned := *cached
			return &cloned, nil
		}
	}

	resp, err := c.doRequest(ctx, "GET", fmt.Sprintf("/api/v1/assessment-models/%s", code), nil)
	if err != nil {
		return nil, err
	}

	dataBytes, err := json.Marshal(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("marshal response data: %w", err)
	}

	var model assessmentModelResponse
	if err := json.Unmarshal(dataBytes, &model); err != nil {
		return nil, fmt.Errorf("unmarshal assessment model response: %w", err)
	}
	sResp := model.toScaleResponse()

	if cacheKey != "" {
		cloned := sResp
		c.scaleCacheMu.Lock()
		c.scaleCache[cacheKey] = &cloned
		c.scaleCacheMu.Unlock()
	}

	return &sResp, nil
}

// assessmentModelResponse 对齐 apiserver/collection 的 assessment-models 摘要。
type assessmentModelResponse struct {
	Code                 string `json:"code"`
	Title                string `json:"title"`
	Status               string `json:"status"`
	Version              string `json:"version,omitempty"`
	QuestionnaireCode    string `json:"questionnaire_code,omitempty"`
	QuestionnaireVersion string `json:"questionnaire_version,omitempty"`
}

func (m assessmentModelResponse) toScaleResponse() ScaleResponse {
	return ScaleResponse{
		Code:                 m.Code,
		Title:                m.Title,
		Status:               m.Status,
		Version:              m.Version,
		QuestionnaireCode:    m.QuestionnaireCode,
		QuestionnaireVersion: m.QuestionnaireVersion,
	}
}

// GetQuestionnaireDetail 获取问卷详情（collection-server）。
func (c *APIClient) GetQuestionnaireDetail(ctx context.Context, code string) (*QuestionnaireDetailResponse, error) {
	cacheKey := normalizeSeedCacheKey(code)
	if cacheKey != "" {
		c.questionnaireCacheMu.RLock()
		cached := c.questionnaireCache[cacheKey]
		c.questionnaireCacheMu.RUnlock()
		if cached != nil {
			cloned := *cached
			return &cloned, nil
		}
	}

	resp, err := c.doRequest(ctx, "GET", fmt.Sprintf("/api/v1/questionnaires/%s", code), nil)
	if err != nil {
		return nil, err
	}

	dataBytes, err := json.Marshal(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("marshal response data: %w", err)
	}

	var detailResp QuestionnaireDetailResponse
	if err := json.Unmarshal(dataBytes, &detailResp); err != nil {
		return nil, fmt.Errorf("unmarshal questionnaire response: %w", err)
	}

	if cacheKey != "" {
		cloned := detailResp
		c.questionnaireCacheMu.Lock()
		c.questionnaireCache[cacheKey] = &cloned
		c.questionnaireCacheMu.Unlock()
	}

	return &detailResp, nil
}
