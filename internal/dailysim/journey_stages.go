package dailysim

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	toolchain "github.com/FangcunMount/seeddata-runner/internal/chain"
)

func dailySimulationStageEnsureGuardianAccount(ctx context.Context, state *dailySimulationJourneyState) (toolchain.Decision, error) {
	var (
		guardianUserID string
		guardianToken  string
		userCreated    bool
		err            error
	)
	if dailySimulationUsesIAMMockConsumer(state.deps.Config.IAM) {
		release, acquireErr := acquireDailySimulationMockIAMLimiter(ctx, state.mockIAMLimiter)
		if acquireErr != nil {
			return toolchain.Decision{}, acquireErr
		}
		defer release()
		guardianUserID, guardianToken, userCreated, err = ensureDailySimulationGuardianMockConsumer(
			ctx,
			state.deps,
			state.cfg,
			state.profile,
		)
	} else {
		guardianUserID, guardianToken, userCreated, err = ensureDailySimulationGuardianAccount(
			ctx,
			state.deps,
			state.iamBundle,
			state.cfg,
			state.profile,
		)
	}
	if err != nil {
		return toolchain.Decision{}, err
	}

	state.guardianUserID = guardianUserID
	state.guardianToken = guardianToken
	state.outcome.UserCreated = userCreated
	state.outcome.GuardianUserID = strings.TrimSpace(guardianUserID)

	if strings.TrimSpace(guardianToken) == "" {
		return toolchain.Decision{}, fmt.Errorf("daily simulation guardian token is empty")
	}

	state.collectionClient = NewAPIClient(state.deps.CollectionClient.BaseURL(), guardianToken, state.deps.Logger)
	state.collectionClient.SetRetryConfig(state.deps.Config.API.Retry)
	return state.nextDecision(dailySimulationJourneyStageGuardianAccount), nil
}

func dailySimulationStageEnsureTestee(ctx context.Context, state *dailySimulationJourneyState) (toolchain.Decision, error) {
	if state.existingTestee != nil {
		testeeID := strings.TrimSpace(state.existingTestee.ID)
		if testeeID == "" {
			return toolchain.Decision{}, fmt.Errorf("existing testee id is empty")
		}
		state.testee = &TesteeResponse{
			ID:           testeeID,
			Name:         strings.TrimSpace(state.existingTestee.Name),
			IAMProfileID: strings.TrimSpace(nullableString(state.existingTestee.ProfileID)),
		}
		state.outcome.TesteeCreated = false
		state.outcome.TesteeID = testeeID
		state.outcome.IAMProfileID = strings.TrimSpace(nullableString(state.existingTestee.ProfileID))
		return state.nextDecision(dailySimulationJourneyStageTesteeProfile), nil
	}
	// collection CreateTestee 会创建 IAM profile 并建立 guardian ProfileLink；
	// 公开 intake 在无 profile_id 时只会建临时 testee，无法通过 collection 答卷权限校验。
	if state.collectionClient == nil {
		return toolchain.Decision{}, fmt.Errorf("guardian collection client is not initialized before creating testee")
	}
	testee, created, err := ensureDailySimulationTestee(ctx, state.collectionClient, state.cfg, state.profile)
	if err != nil {
		return toolchain.Decision{}, err
	}
	if testee == nil || strings.TrimSpace(testee.ID) == "" {
		return toolchain.Decision{}, fmt.Errorf("collection create testee returned empty id")
	}
	if strings.TrimSpace(testee.IAMProfileID) == "" {
		return toolchain.Decision{}, fmt.Errorf("collection create testee %s returned empty iam_profile_id", strings.TrimSpace(testee.ID))
	}
	state.testee = testee
	state.outcome.TesteeCreated = created
	state.outcome.TesteeID = strings.TrimSpace(testee.ID)
	state.outcome.IAMProfileID = strings.TrimSpace(testee.IAMProfileID)
	state.outcome.IAMProfileLinkID = strings.TrimSpace(testee.IAMProfileLinkID)
	return state.nextDecision(dailySimulationJourneyStageTesteeProfile), nil
}

func dailySimulationStageEnrollPlan(ctx context.Context, state *dailySimulationJourneyState) (toolchain.Decision, error) {
	if strings.TrimSpace(state.planID) == "" {
		return toolchain.Decision{}, fmt.Errorf("dailySimulation.planIds resolved empty plan")
	}
	if state.testee == nil || strings.TrimSpace(state.testee.ID) == "" {
		return toolchain.Decision{}, fmt.Errorf("testee is not initialized before plan enrollment")
	}
	enrollment, err := state.deps.APIClient.EnrollTesteeInPlan(ctx, EnrollTesteeRequest{
		PlanID:    state.planID,
		TesteeID:  state.testee.ID,
		StartDate: state.profile.RunDate.Format("2006-01-02"),
	})
	if err != nil {
		return toolchain.Decision{}, err
	}
	state.outcome.PlanEnrolled = true
	if enrollment != nil {
		state.outcome.EnrollmentID = strings.TrimSpace(enrollment.EnrollmentID)
		state.outcome.TaskIDs = state.outcome.TaskIDs[:0]
		for _, task := range enrollment.Tasks {
			if taskID := strings.TrimSpace(task.ID); taskID != "" {
				state.outcome.TaskIDs = append(state.outcome.TaskIDs, taskID)
			}
		}
	}
	return toolchain.Next(), nil
}

func dailySimulationStageEnsureEntryAccess(ctx context.Context, state *dailySimulationJourneyState) (toolchain.Decision, error) {
	if state.entry == nil || strings.TrimSpace(state.entry.ID) == "" || strings.TrimSpace(state.entry.Token) == "" {
		return toolchain.Decision{}, fmt.Errorf("assessment entry is not initialized")
	}
	hasEntryRelation := false
	if state.testee != nil && strings.TrimSpace(state.testee.ID) != "" {
		var err error
		hasEntryRelation, err = hasAssessmentEntryRelation(ctx, state.deps.APIClient, state.testee.ID, state.entry.ID)
		if err != nil {
			return toolchain.Decision{}, err
		}
	}

	// 每次访问都走公开 resolve，确保 entry_opened 行为事件落到对应 clinician。
	if _, err := state.deps.APIClient.ResolveAssessmentEntry(ctx, state.entry.Token); err != nil {
		return toolchain.Decision{}, err
	}
	state.outcome.EntryResolved = true
	if hasEntryRelation {
		state.outcome.EntryIntaked = true
		return state.nextDecision(dailySimulationJourneyStageAssessmentEntry), nil
	}

	req, err := buildDailySimulationAssessmentEntryIntakeRequest(state)
	if err != nil {
		return toolchain.Decision{}, err
	}
	previousTesteeID := dailySimulationTesteeID(state.testee)
	intakeResp, err := state.deps.APIClient.IntakeAssessmentEntry(ctx, state.entry.Token, req)
	if err != nil {
		return toolchain.Decision{}, err
	}
	if err := applyDailySimulationAssessmentEntryIntakeResult(state, intakeResp, previousTesteeID); err != nil {
		return toolchain.Decision{}, err
	}
	state.outcome.EntryIntaked = true
	return state.nextDecision(dailySimulationJourneyStageAssessmentEntry), nil
}

func dailySimulationStageSubmitAnswerSheet(ctx context.Context, state *dailySimulationJourneyState) (toolchain.Decision, error) {
	if state.target == nil || strings.TrimSpace(state.target.QuestionnaireCode) == "" {
		return toolchain.Decision{}, fmt.Errorf("questionnaire target is not initialized")
	}
	if state.testee == nil || strings.TrimSpace(state.testee.ID) == "" {
		return toolchain.Decision{}, fmt.Errorf("testee is not initialized before answersheet submission")
	}
	canonicalTesteeID, err := resolveDailySimulationCanonicalTesteeID(ctx, state)
	if err != nil {
		return toolchain.Decision{}, err
	}
	state.testee.ID = strconv.FormatUint(canonicalTesteeID, 10)
	state.outcome.TesteeID = state.testee.ID
	if state.existingTestee != nil {
		state.existingTestee.ID = state.testee.ID
	}
	testeeID := parseID(state.testee.ID)
	if testeeID == 0 {
		return toolchain.Decision{}, fmt.Errorf("invalid testee id %q", state.testee.ID)
	}
	questionnaireDetail := state.target.QuestionnaireDetail
	if questionnaireDetail == nil {
		return toolchain.Decision{}, fmt.Errorf("questionnaire detail for %s is not preloaded", state.target.QuestionnaireCode)
	}

	rng := newDailySimulationRand(
		"answers:" + state.profile.RunDate.Format("20060102") + ":" + strconv.Itoa(state.profile.Index) + ":" + state.target.QuestionnaireCode,
	)
	answers := buildAnswers(questionnaireDetail, rng)
	invalidAnswers := validateAnswers(questionnaireDetail, answers)
	if len(invalidAnswers) > 0 {
		return toolchain.Decision{}, fmt.Errorf(
			"generated invalid answers for questionnaire %s: %v",
			state.target.QuestionnaireCode,
			invalidAnswers,
		)
	}
	submitReq := SubmitAnswerSheetRequest{
		QuestionnaireCode:    state.target.QuestionnaireCode,
		QuestionnaireVersion: state.target.QuestionnaireVersion,
		Title:                state.target.QuestionnaireTitle,
		TesteeID:             testeeID,
		OriginRef:            dailySimulationSubmissionOriginRef(state, ""),
		Answers:              answers,
	}
	if err := submitDailySimulationAnswerSheet(ctx, state, submitReq); err != nil {
		return toolchain.Decision{}, err
	}
	return state.nextDecision(dailySimulationJourneyStageAnswerSheet), nil
}

func buildDailySimulationAssessmentEntryIntakeRequest(state *dailySimulationJourneyState) (IntakeAssessmentEntryRequest, error) {
	if state == nil {
		return IntakeAssessmentEntryRequest{}, fmt.Errorf("daily simulation state is nil")
	}
	req := IntakeAssessmentEntryRequest{
		Name:   strings.TrimSpace(state.profile.ChildName),
		Gender: dailySimulationGenderString(state.profile.ChildGender),
	}
	birthday, err := dailySimulationBirthdayPtr(state.profile.ChildDOB)
	if err != nil {
		return IntakeAssessmentEntryRequest{}, err
	}
	req.Birthday = birthday

	if state.existingTestee != nil {
		profileID, err := dailySimulationExistingTesteeProfileID(state.existingTestee)
		if err != nil {
			return IntakeAssessmentEntryRequest{}, err
		}
		if profileID == nil {
			return IntakeAssessmentEntryRequest{}, fmt.Errorf("existing testee %s has no profile_id; cannot public-intake assessment entry without duplicating testee", strings.TrimSpace(state.existingTestee.ID))
		}
		req.ProfileID = profileID
		if name := strings.TrimSpace(state.existingTestee.Name); name != "" {
			req.Name = name
		}
		if gender := strings.TrimSpace(state.existingTestee.Gender); gender != "" {
			req.Gender = gender
		}
		if state.existingTestee.Birthday != nil {
			req.Birthday = state.existingTestee.Birthday
		}
	} else {
		profileID, err := dailySimulationTesteeIAMProfileID(state.testee)
		if err != nil {
			return IntakeAssessmentEntryRequest{}, err
		}
		if profileID == nil {
			return IntakeAssessmentEntryRequest{}, fmt.Errorf("testee is missing iam_profile_id; collection create must precede public intake")
		}
		req.ProfileID = profileID
		if state.testee != nil {
			if name := strings.TrimSpace(state.testee.Name); name != "" {
				req.Name = name
			}
		}
	}

	if strings.TrimSpace(req.Name) == "" {
		return IntakeAssessmentEntryRequest{}, fmt.Errorf("daily simulation child name is empty")
	}
	return req, nil
}

func dailySimulationExistingTesteeProfileID(testee *ApiserverTesteeResponse) (*uint64, error) {
	if testee == nil || testee.ProfileID == nil {
		return nil, nil
	}
	raw := strings.TrimSpace(*testee.ProfileID)
	if raw == "" {
		return nil, nil
	}
	profileID := parseID(raw)
	if profileID == 0 {
		return nil, fmt.Errorf("invalid existing testee profile_id %q", raw)
	}
	return &profileID, nil
}

func dailySimulationTesteeIAMProfileID(testee *TesteeResponse) (*uint64, error) {
	if testee == nil {
		return nil, nil
	}
	raw := strings.TrimSpace(testee.IAMProfileID)
	if raw == "" {
		return nil, nil
	}
	profileID := parseID(raw)
	if profileID == 0 {
		return nil, fmt.Errorf("invalid testee iam_profile_id %q", raw)
	}
	return &profileID, nil
}

func dailySimulationGenderString(gender uint8) string {
	switch gender {
	case 1:
		return "male"
	case 2:
		return "female"
	default:
		return "unknown"
	}
}

func dailySimulationBirthdayPtr(raw string) (*time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, nil
	}
	layouts := []string{
		"2006-01-02",
		"2006-01-02 15:04:05",
		time.RFC3339,
		time.RFC3339Nano,
	}
	var lastErr error
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return &parsed, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("invalid daily simulation child birthday %q: %w", raw, lastErr)
}

func applyDailySimulationAssessmentEntryIntakeResult(
	state *dailySimulationJourneyState,
	resp *AssessmentEntryIntakeResponse,
	previousTesteeID string,
) error {
	if state == nil {
		return fmt.Errorf("daily simulation state is nil")
	}
	if resp == nil || resp.Testee == nil || strings.TrimSpace(resp.Testee.ID) == "" {
		return fmt.Errorf("assessment entry intake returned empty testee")
	}
	previousProfileID := ""
	if state.testee != nil {
		previousProfileID = strings.TrimSpace(state.testee.IAMProfileID)
	}
	profileID := strings.TrimSpace(nullableString(resp.Testee.ProfileID))
	if profileID == "" {
		profileID = previousProfileID
	}
	state.testee = &TesteeResponse{
		ID:           strings.TrimSpace(resp.Testee.ID),
		Name:         strings.TrimSpace(resp.Testee.Name),
		IAMProfileID: profileID,
		CreatedAt:    resp.Testee.CreatedAt,
		UpdatedAt:    resp.Testee.UpdatedAt,
	}
	state.outcome.TesteeID = state.testee.ID
	state.outcome.IAMProfileID = profileID
	if state.existingTestee != nil {
		state.existingTestee.ID = state.testee.ID
		state.existingTestee.Name = state.testee.Name
		if profileID != "" {
			cloned := profileID
			state.existingTestee.ProfileID = &cloned
		}
		state.existingTestee.CreatedAt = resp.Testee.CreatedAt
		state.existingTestee.UpdatedAt = resp.Testee.UpdatedAt
	}
	if strings.TrimSpace(previousTesteeID) == "" {
		state.outcome.TesteeCreated = true
	}
	return nil
}

func resolveDailySimulationCanonicalTesteeID(ctx context.Context, state *dailySimulationJourneyState) (uint64, error) {
	if state == nil || state.testee == nil {
		return 0, fmt.Errorf("testee is not initialized before canonical resolution")
	}
	if canonicalID := parseID(state.testee.ID); canonicalID > 0 {
		return canonicalID, nil
	}
	return 0, fmt.Errorf("invalid canonical testee id %q", state.testee.ID)
}
