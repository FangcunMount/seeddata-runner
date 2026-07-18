package plansubmit

import (
	"context"
	"fmt"
	"strings"

	"github.com/FangcunMount/component-base/pkg/log"
	toolanswersheet "github.com/FangcunMount/seeddata-runner/internal/answersheet"
	"github.com/FangcunMount/seeddata-runner/internal/seedconfig"
)

type planTaskSubmitSessionFactory struct {
	deps              *dependencies
	logger            log.Logger
	orgID             int64
	apiGateway        planTaskSubmitAPIGateway
	collectionGateway planTaskSubmitCollectionGateway
}

func newPlanTaskSubmitSessionFactory(
	ctx context.Context,
	deps *dependencies,
) (*planTaskSubmitSessionFactory, error) {
	if deps == nil {
		return nil, fmt.Errorf("dependencies are nil")
	}
	if deps.Config == nil {
		return nil, fmt.Errorf("seeddata config is nil")
	}
	if deps.APIClient == nil {
		return nil, fmt.Errorf("api client is not initialized")
	}
	if deps.CollectionClient == nil {
		return nil, fmt.Errorf("collection client is not initialized")
	}
	if deps.PlanSubmissionLedger == nil {
		stateFile := strings.TrimSpace(deps.Config.PlanSubmit.SubmissionStateFile)
		if stateFile == "" {
			stateFile = seedconfig.DefaultPlanSubmitSubmissionStateFile
		}
		ledger, err := toolanswersheet.NewSubmissionLedger(stateFile, "plan")
		if err != nil {
			return nil, fmt.Errorf("initialize plan submission ledger: %w", err)
		}
		deps.PlanSubmissionLedger = ledger
	}

	orgID := deps.Config.Global.OrgID
	if orgID <= 0 {
		return nil, fmt.Errorf("global.orgId must be set in seeddata config")
	}

	logger := deps.Logger
	prewarmAPIToken(ctx, deps.APIClient, orgID, logger)

	apiGateway := newPlanTaskSubmitAPIGateway(deps.APIClient)
	collectionGateway := newPlanTaskSubmitCollectionGateway(deps.CollectionClient)
	if apiGateway == nil || collectionGateway == nil {
		return nil, fmt.Errorf("initialize opened-task submit gateways: client is nil")
	}

	return &planTaskSubmitSessionFactory{
		deps:              deps,
		logger:            logger,
		orgID:             orgID,
		apiGateway:        apiGateway,
		collectionGateway: collectionGateway,
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
		deps:              f.deps,
		logger:            f.logger,
		orgID:             f.orgID,
		planID:            planID,
		apiGateway:        f.apiGateway,
		collectionGateway: f.collectionGateway,
		plan:              planResp,
	}, nil
}

func (f *planTaskSubmitSessionFactory) loadPlan(
	ctx context.Context,
	planID string,
) (*PlanResponse, error) {
	planResp, err := f.apiGateway.GetPlan(ctx, planID)
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
