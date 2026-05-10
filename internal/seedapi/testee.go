package seedapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// CreateCollectionTestee 创建 collection 受试者。
func (c *APIClient) CreateCollectionTestee(ctx context.Context, req CollectionCreateTesteeRequest) (*TesteeResponse, error) {
	resp, err := c.doRequest(ctx, "POST", "/api/v1/testees", req)
	if err != nil {
		return nil, err
	}

	var testeeResp TesteeResponse
	if err := decodeResponseData(resp, &testeeResp); err != nil {
		return nil, fmt.Errorf("decode create testee response: %w", err)
	}
	return &testeeResp, nil
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

// AssignClinicianTesteeWithRelationType 按指定关系类型建立受试者分配（apiserver）。
func (c *APIClient) AssignClinicianTesteeWithRelationType(ctx context.Context, relationType string, req AssignClinicianTesteeRequest) (*RelationResponse, error) {
	path := "/api/v1/clinician-testee-relations/assign"
	switch strings.ToLower(strings.TrimSpace(relationType)) {
	case "primary":
		path = "/api/v1/clinician-testee-relations/assign-primary"
	case "collaborator":
		path = "/api/v1/clinician-testee-relations/assign-collaborator"
	case "attending", "", "assigned":
		path = "/api/v1/clinician-testee-relations/assign-attending"
	}

	resp, err := c.doRequest(ctx, "POST", path, req)
	if err != nil {
		return nil, err
	}

	var relationResp RelationResponse
	if err := decodeResponseData(resp, &relationResp); err != nil {
		return nil, fmt.Errorf("decode clinician-testee relation response: %w", err)
	}
	return &relationResp, nil
}
