package seedapi

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// GetPublishedAssessmentModel 获取不可变的已发布测评模型。
func (c *APIClient) GetPublishedAssessmentModel(ctx context.Context, code, version string) (*PublishedAssessmentModelResponse, error) {
	code = strings.TrimSpace(code)
	version = strings.TrimSpace(version)
	if code == "" {
		return nil, fmt.Errorf("assessment model code is required")
	}
	cacheKey := publishedResourceCacheKey(code, version)
	if version != "" {
		if cached, ok := c.publishedModelCache.Get(cacheKey); ok {
			cloned := cached
			return &cloned, nil
		}
	}

	path := fmt.Sprintf("/api/v1/assessment-models/published/%s", url.PathEscape(code))
	if version != "" {
		path += "?version=" + url.QueryEscape(version)
	}
	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}

	var model PublishedAssessmentModelResponse
	if err := decodeResponseData(resp, &model); err != nil {
		return nil, fmt.Errorf("decode published assessment model response: %w", err)
	}
	if strings.TrimSpace(model.Code) != code {
		return nil, fmt.Errorf("published assessment model code mismatch: requested=%s loaded=%s", code, model.Code)
	}
	if version != "" && strings.TrimSpace(model.Version) != version {
		return nil, fmt.Errorf("published assessment model version mismatch: code=%s requested=%s loaded=%s", code, version, model.Version)
	}
	if version != "" {
		exactKey := publishedResourceCacheKey(model.Code, model.Version)
		c.publishedModelCache.Put(exactKey, model)
	}
	return &model, nil
}

// GetPublishedQuestionnaire 获取 collection-server 中的精确已发布问卷。
func (c *APIClient) GetPublishedQuestionnaire(ctx context.Context, code, version string) (*QuestionnaireDetailResponse, error) {
	code = strings.TrimSpace(code)
	version = strings.TrimSpace(version)
	if code == "" {
		return nil, fmt.Errorf("questionnaire code is required")
	}
	cacheKey := publishedResourceCacheKey(code, version)
	if version != "" {
		if cached, ok := c.questionnaireCache.Get(cacheKey); ok {
			cloned := cached
			return &cloned, nil
		}
	}

	path := fmt.Sprintf("/api/v1/questionnaires/%s", url.PathEscape(code))
	if version != "" {
		path += "?version=" + url.QueryEscape(version)
	}
	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, err
	}

	var detail QuestionnaireDetailResponse
	if err := decodeResponseData(resp, &detail); err != nil {
		return nil, fmt.Errorf("decode published questionnaire response: %w", err)
	}
	if strings.TrimSpace(detail.Code) != code {
		return nil, fmt.Errorf("published questionnaire code mismatch: requested=%s loaded=%s", code, detail.Code)
	}
	if version != "" && strings.TrimSpace(detail.Version) != version {
		return nil, fmt.Errorf("published questionnaire version mismatch: code=%s requested=%s loaded=%s", code, version, detail.Version)
	}
	if version != "" {
		exactKey := publishedResourceCacheKey(detail.Code, detail.Version)
		c.questionnaireCache.Put(exactKey, detail)
	}
	return &detail, nil
}

func publishedResourceCacheKey(code, version string) string {
	code = normalizeSeedCacheKey(code)
	version = normalizeSeedCacheKey(version)
	if code == "" || version == "" {
		return ""
	}
	return code + "@" + version
}
