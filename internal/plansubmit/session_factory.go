package plansubmit

import (
	"context"
	"fmt"

	"github.com/FangcunMount/component-base/pkg/log"
)

type planTaskSubmitSessionFactory struct {
	deps    *dependencies
	logger  log.Logger
	orgID   int64
	gateway planTaskSubmitGateway
}

func newPlanTaskSubmitSessionFactory(
	ctx context.Context,
	deps *dependencies,
) (*planTaskSubmitSessionFactory, error) {
	if deps == nil {
		return nil, fmt.Errorf("dependencies are nil")
	}
	if deps.APIClient == nil {
		return nil, fmt.Errorf("api client is not initialized")
	}

	orgID := deps.Config.Global.OrgID
	if orgID <= 0 {
		return nil, fmt.Errorf("global.orgId must be set in seeddata config")
	}

	logger := deps.Logger
	prewarmAPIToken(ctx, deps.APIClient, orgID, logger)

	gateway := newPlanTaskSubmitGateway(deps.APIClient)
	if gateway == nil {
		return nil, fmt.Errorf("initialize opened-task submit gateway: api client is nil")
	}

	return &planTaskSubmitSessionFactory{
		deps:    deps,
		logger:  logger,
		orgID:   orgID,
		gateway: gateway,
	}, nil
}

func (f *planTaskSubmitSessionFactory) OpenSession(
	ctx context.Context,
	planID string,
	verbose bool,
) (*planTaskSubmitSession, error) {
	planID = normalizePlanID(planID)
	if planID == "" {
		return nil, fmt.Errorf("plan-id is required")
	}

	planResp, err := f.loadPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	if err := f.validatePlan(planID, planResp); err != nil {
		return nil, err
	}

	if verbose {
		f.logger.Infow("Opened-task submit session initialized",
			"plan_id", planID,
			"org_id", f.orgID,
			"plan_status", planResp.Status,
			"scale_code", planResp.ScaleCode,
		)
	}

	return &planTaskSubmitSession{
		deps:    f.deps,
		logger:  f.logger,
		orgID:   f.orgID,
		planID:  planID,
		gateway: f.gateway,
		plan:    planResp,
	}, nil
}

func (f *planTaskSubmitSessionFactory) loadPlan(
	ctx context.Context,
	planID string,
) (*PlanResponse, error) {
	planResp, err := f.gateway.GetPlan(ctx, planID)
	if err != nil {
		return nil, fmt.Errorf("load plan %s from apiserver api: %w", planID, err)
	}
	if planResp == nil {
		return nil, fmt.Errorf("plan %s not found", planID)
	}
	return planResp, nil
}

func (f *planTaskSubmitSessionFactory) validatePlan(
	planID string,
	planResp *PlanResponse,
) error {
	if planResp.OrgID != f.orgID {
		return fmt.Errorf("plan %s does not belong to org %d", planID, f.orgID)
	}
	if normalizeTaskStatus(planResp.Status) != "active" {
		return fmt.Errorf("plan %s is not active, current status=%s", planID, planResp.Status)
	}
	return nil
}
