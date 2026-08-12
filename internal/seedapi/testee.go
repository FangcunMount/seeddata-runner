package seedapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CreateCollectionTestee 创建 collection 受试者。
func (c *APIClient) CreateCollectionTestee(ctx context.Context, req CollectionCreateTesteeRequest) (*TesteeResponse, error) {
	// The collection create endpoint has no idempotency key. Retrying a request
	// after a timeout or 5xx can create another IAM profile/link and testee even
	// when the first request was committed successfully.
	resp, err := c.doRequestWithRetryTimeoutAndLimit(
		ctx,
		http.MethodPost,
		"/api/v1/testees",
		req,
		true,
		c.httpClient.Timeout,
		0,
	)
	if err != nil {
		return nil, err
	}

	var testeeResp TesteeResponse
	if err := decodeResponseData(resp, &testeeResp); err != nil {
		return nil, fmt.Errorf("decode create testee response: %w", err)
	}
	return &testeeResp, nil
}

// ListCollectionTestees lists testees linked to the authenticated guardian.
func (c *APIClient) ListCollectionTestees(ctx context.Context, offset, limit int) (*CollectionTesteeListResponse, error) {
	path := fmt.Sprintf("/api/v1/testees?offset=%d&limit=%d", offset, limit)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var listResp CollectionTesteeListResponse
	if err := decodeResponseData(resp, &listResp); err != nil {
		return nil, fmt.Errorf("decode collection testee list response: %w", err)
	}
	return &listResp, nil
}

// ListTesteesByOrg 获取受试者列表（apiserver）。
func (c *APIClient) ListTesteesByOrg(ctx context.Context, orgID int64, page, pageSize int) (*ApiserverTesteeListResponse, error) {
	path := fmt.Sprintf("/api/v1/testees?org_id=%d&page=%d&page_size=%d", orgID, page, pageSize)
	return c.listTesteesByOrgPath(ctx, path, orgID, page, pageSize)
}

// ListTesteesByOrgCreatedOnDate 获取指定日期创建的受试者列表（apiserver）。
func (c *APIClient) ListTesteesByOrgCreatedOnDate(ctx context.Context, orgID int64, day time.Time, page, pageSize int) (*ApiserverTesteeListResponse, error) {
	day = day.In(time.Local)
	date := day.Format("2006-01-02")
	path := fmt.Sprintf(
		"/api/v1/testees?org_id=%d&page=%d&page_size=%d&created_start_date=%s&created_end_date=%s",
		orgID,
		page,
		pageSize,
		urlQueryEscape(date),
		urlQueryEscape(date),
	)
	return c.listTesteesByOrgPath(ctx, path, orgID, page, pageSize)
}

// GetTesteeByID 根据 ID 获取受试者详情（apiserver）。
func (c *APIClient) GetTesteeByID(ctx context.Context, testeeID string) (*ApiserverTesteeResponse, error) {
	testeeID = strings.TrimSpace(testeeID)
	if testeeID == "" {
		return nil, fmt.Errorf("testee_id is required")
	}
	resp, err := c.doRequest(ctx, "GET", fmt.Sprintf("/api/v1/testees/%s", urlQueryEscape(testeeID)), nil)
	if err != nil {
		return nil, fmt.Errorf("get testee: testee_id=%s: %w", testeeID, err)
	}

	dataBytes, err := json.Marshal(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("marshal response data: %w", err)
	}

	var testeeResp ApiserverTesteeResponse
	if err := json.Unmarshal(dataBytes, &testeeResp); err != nil {
		return nil, fmt.Errorf("unmarshal testee response: %w", err)
	}
	return &testeeResp, nil
}

func (c *APIClient) listTesteesByOrgPath(ctx context.Context, path string, orgID int64, page, pageSize int) (*ApiserverTesteeListResponse, error) {
	resp, err := c.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("list testees: org_id=%d page=%d page_size=%d: %w", orgID, page, pageSize, err)
	}

	dataBytes, err := json.Marshal(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("marshal response data: %w", err)
	}

	var listResp ApiserverTesteeListResponse
	if err := json.Unmarshal(dataBytes, &listResp); err != nil {
		return nil, fmt.Errorf("unmarshal testees response: %w", err)
	}

	return &listResp, nil
}

// GetTesteeClinicians 获取受试者当前有效的从业者关系（apiserver）。
func (c *APIClient) GetTesteeClinicians(ctx context.Context, testeeID string) (*TesteeClinicianRelationListResponse, error) {
	resp, err := c.doRequest(ctx, "GET", fmt.Sprintf("/api/v1/testees/%s/clinician-relations", testeeID), nil)
	if err != nil {
		return nil, err
	}

	var relationResp TesteeClinicianRelationListResponse
	if err := decodeResponseData(resp, &relationResp); err != nil {
		return nil, fmt.Errorf("decode testee clinician relations response: %w", err)
	}
	return &relationResp, nil
}
