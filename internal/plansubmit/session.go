package plansubmit

import (
	"context"

	"github.com/FangcunMount/component-base/pkg/log"
)

// planTaskSubmitGateway 定义计划提交任务提交器
type planTaskSubmitGateway interface {
	GetPlan(ctx context.Context, planID string) (*PlanResponse, error)
	GetScale(ctx context.Context, code string) (*ScaleResponse, error)
	GetQuestionnaireDetail(ctx context.Context, code string) (*QuestionnaireDetailResponse, error)
	ListPlanTaskWindow(ctx context.Context, req ListPlanTaskWindowRequest) (*PlanTaskWindowResponse, error)
}

// 确保 apiPlanTaskSubmitGateway 实现了 planTaskSubmitGateway 接口
var _ planTaskSubmitGateway = &apiPlanTaskSubmitGateway{}

// apiPlanTaskSubmitGateway 定义计划提交任务提交器
type apiPlanTaskSubmitGateway struct {
	client *APIClient
}

// newPlanTaskSubmitGateway 创建计划提交任务提交器
func newPlanTaskSubmitGateway(client *APIClient) planTaskSubmitGateway {
	if client == nil {
		return nil
	}
	return &apiPlanTaskSubmitGateway{client: client}
}

// GetPlan 获取计划
func (g *apiPlanTaskSubmitGateway) GetPlan(ctx context.Context, planID string) (*PlanResponse, error) {
	return g.client.GetPlan(ctx, planID)
}

// GetScale 获取量表
func (g *apiPlanTaskSubmitGateway) GetScale(ctx context.Context, code string) (*ScaleResponse, error) {
	return g.client.GetScale(ctx, code)
}

// GetQuestionnaireDetail 获取问卷详情
func (g *apiPlanTaskSubmitGateway) GetQuestionnaireDetail(ctx context.Context, code string) (*QuestionnaireDetailResponse, error) {
	return g.client.GetQuestionnaireDetail(ctx, code)
}

// ListPlanTaskWindow 查询任务窗口
func (g *apiPlanTaskSubmitGateway) ListPlanTaskWindow(ctx context.Context, req ListPlanTaskWindowRequest) (*PlanTaskWindowResponse, error) {
	return g.client.ListPlanTaskWindow(ctx, req)
}

// planTaskSubmitSession 定义计划提交任务提交器
type planTaskSubmitSession struct {
	deps   *dependencies
	logger log.Logger
	orgID  int64
	planID string
	// 计划提交任务提交器
	gateway planTaskSubmitGateway
	// 计划
	plan *PlanResponse
}
