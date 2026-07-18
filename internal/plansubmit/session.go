package plansubmit

import (
	"context"

	"github.com/FangcunMount/component-base/pkg/log"
)

type planTaskSubmitAPIGateway interface {
	GetPlan(ctx context.Context, planID string) (*PlanResponse, error)
	GetPublishedAssessmentModel(ctx context.Context, code, version string) (*PublishedAssessmentModelResponse, error)
	GetTesteeByID(ctx context.Context, testeeID string) (*ApiserverTesteeResponse, error)
	ListPlanTaskWindow(ctx context.Context, req ListPlanTaskWindowRequest) (*PlanTaskWindowResponse, error)
}

type planTaskSubmitCollectionGateway interface {
	GetPublishedQuestionnaire(ctx context.Context, code, version string) (*QuestionnaireDetailResponse, error)
}

var _ planTaskSubmitAPIGateway = &apiPlanTaskSubmitGateway{}
var _ planTaskSubmitCollectionGateway = &collectionPlanTaskSubmitGateway{}

// apiPlanTaskSubmitGateway 定义计划提交任务提交器
type apiPlanTaskSubmitGateway struct {
	client *APIClient
}

type collectionPlanTaskSubmitGateway struct {
	client *APIClient
}

func newPlanTaskSubmitAPIGateway(client *APIClient) planTaskSubmitAPIGateway {
	if client == nil {
		return nil
	}
	return &apiPlanTaskSubmitGateway{client: client}
}

func newPlanTaskSubmitCollectionGateway(client *APIClient) planTaskSubmitCollectionGateway {
	if client == nil {
		return nil
	}
	return &collectionPlanTaskSubmitGateway{client: client}
}

// GetPlan 获取计划
func (g *apiPlanTaskSubmitGateway) GetPlan(ctx context.Context, planID string) (*PlanResponse, error) {
	return g.client.GetPlan(ctx, planID)
}

func (g *apiPlanTaskSubmitGateway) GetPublishedAssessmentModel(ctx context.Context, code, version string) (*PublishedAssessmentModelResponse, error) {
	return g.client.GetPublishedAssessmentModel(ctx, code, version)
}

func (g *collectionPlanTaskSubmitGateway) GetPublishedQuestionnaire(ctx context.Context, code, version string) (*QuestionnaireDetailResponse, error) {
	return g.client.GetPublishedQuestionnaire(ctx, code, version)
}

// GetTesteeByID 获取受试者详情
func (g *apiPlanTaskSubmitGateway) GetTesteeByID(ctx context.Context, testeeID string) (*ApiserverTesteeResponse, error) {
	return g.client.GetTesteeByID(ctx, testeeID)
}

// ListPlanTaskWindow 查询任务窗口
func (g *apiPlanTaskSubmitGateway) ListPlanTaskWindow(ctx context.Context, req ListPlanTaskWindowRequest) (*PlanTaskWindowResponse, error) {
	return g.client.ListPlanTaskWindow(ctx, req)
}

// planTaskSubmitSession 定义计划提交任务提交器
type planTaskSubmitSession struct {
	deps              *dependencies
	logger            log.Logger
	orgID             int64
	planID            string
	apiGateway        planTaskSubmitAPIGateway
	collectionGateway planTaskSubmitCollectionGateway
	// 计划
	plan *PlanResponse
}
