package plansubmit

import "context"

type planTaskSubmitBootstrap struct {
	session   *planTaskSubmitSession
	scaleResp *PublishedAssessmentModelResponse
	detail    *QuestionnaireDetailResponse
}

func newPlanTaskSubmitBootstrap(
	ctx context.Context,
	deps *dependencies,
	planID string,
	verbose bool,
) (planTaskSubmitBootstrap, error) {
	factory, err := newPlanTaskSubmitSessionFactory(ctx, deps)
	if err != nil {
		return planTaskSubmitBootstrap{}, err
	}
	session, err := factory.OpenSession(ctx, planID, verbose)
	if err != nil {
		return planTaskSubmitBootstrap{}, err
	}
	loader, err := newPlanTaskSubmitQuestionnaireLoader(session)
	if err != nil {
		return planTaskSubmitBootstrap{}, err
	}
	scaleResp, detail, err := loader.Load(ctx, verbose)
	if err != nil {
		return planTaskSubmitBootstrap{}, err
	}
	return planTaskSubmitBootstrap{
		session:   session,
		scaleResp: scaleResp,
		detail:    detail,
	}, nil
}

func (b planTaskSubmitBootstrap) BuildRunner() planTaskSubmitRunner {
	return planTaskSubmitRunner{
		session:   b.session,
		detail:    b.detail,
		ledger:    b.session.deps.PlanSubmissionLedger,
		scaleResp: b.scaleResp,
	}
}
