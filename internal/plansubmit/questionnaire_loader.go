package plansubmit

import (
	"context"
	"fmt"
	"strings"
)

type planTaskSubmitQuestionnaireLoader struct {
	session *planTaskSubmitSession
}

func newPlanTaskSubmitQuestionnaireLoader(session *planTaskSubmitSession) (*planTaskSubmitQuestionnaireLoader, error) {
	if session == nil {
		return nil, fmt.Errorf("plan task submit session is nil")
	}
	if session.plan == nil {
		return nil, fmt.Errorf("plan is nil")
	}
	if strings.TrimSpace(session.plan.ScaleCode) == "" {
		return nil, fmt.Errorf("plan %s has empty scale_code", session.planID)
	}
	return &planTaskSubmitQuestionnaireLoader{session: session}, nil
}

func (l *planTaskSubmitQuestionnaireLoader) Load(
	ctx context.Context,
	verbose bool,
) (*ScaleResponse, *QuestionnaireDetailResponse, error) {
	scaleResp, err := l.session.gateway.GetScale(ctx, l.session.plan.ScaleCode)
	if err != nil {
		return nil, nil, fmt.Errorf("load scale %s: %w", l.session.plan.ScaleCode, err)
	}
	if scaleResp == nil {
		return nil, nil, fmt.Errorf("scale %s not found", l.session.plan.ScaleCode)
	}
	if strings.TrimSpace(scaleResp.QuestionnaireCode) == "" {
		return nil, nil, fmt.Errorf("scale %s has empty questionnaire_code", l.session.plan.ScaleCode)
	}
	if strings.TrimSpace(scaleResp.QuestionnaireVersion) == "" {
		return nil, nil, fmt.Errorf("scale %s has empty questionnaire_version", l.session.plan.ScaleCode)
	}

	detail, err := l.session.gateway.GetQuestionnaireDetail(ctx, scaleResp.QuestionnaireCode)
	if err != nil {
		return nil, nil, fmt.Errorf("load questionnaire %s: %w", scaleResp.QuestionnaireCode, err)
	}
	if detail == nil {
		return nil, nil, fmt.Errorf("questionnaire %s not found", scaleResp.QuestionnaireCode)
	}
	if strings.TrimSpace(detail.Version) != scaleResp.QuestionnaireVersion {
		return nil, nil, newPlanQuestionnaireVersionMismatchError(
			l.session.plan.ScaleCode,
			scaleResp.QuestionnaireCode,
			scaleResp.QuestionnaireVersion,
			detail.Version,
		)
	}
	if verbose {
		debugLogQuestionnaire(detail, l.session.logger)
	}

	return scaleResp, detail, nil
}
