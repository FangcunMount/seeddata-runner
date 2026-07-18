package dailysim

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	identityv2 "github.com/FangcunMount/iam/v2/api/grpc/iam/identity/v2"
	sdk "github.com/FangcunMount/iam/v2/pkg/sdk"
	sdkerrors "github.com/FangcunMount/iam/v2/pkg/sdk/errors"
	"github.com/FangcunMount/iam/v2/pkg/sdk/identity"
	toolchain "github.com/FangcunMount/seeddata-runner/internal/chain"
	"github.com/FangcunMount/seeddata-runner/internal/scheduler"
	"github.com/FangcunMount/seeddata-runner/internal/seediauth"
)

const (
	dailySimulationDefaultCount     = 10
	dailySimulationDefaultWorkers   = 4
	dailySimulationAnswerSheetPage  = 100
	dailySimulationDefaultPhonePref = "+86199"
	dailySimulationDefaultEmailHost = "fangcunmount.com"
	dailySimulationDefaultPassword  = "DailySim@123"
	dailySimulationDefaultSource    = "daily_simulation"
	dailySimulationDeviceIDPrefix   = "seeddata-daily"
	seedAssessmentPollTimeout       = 5 * time.Minute
	dailySimulationMockIAMTimeout   = 15 * time.Second
	dailySimulationMockIAMRetryMax  = 1
)

var (
	dailySimulationMockIAMRetryMinDelay = 500 * time.Millisecond
	dailySimulationMockIAMRetryMaxDelay = 2 * time.Second
)

type dailySimulationIAMBundle struct {
	client   *sdk.Client
	identity *identity.Client
}

type dailySimulationResolvedTarget struct {
	TargetType           string
	TargetCode           string
	TargetVersion        string
	QuestionnaireCode    string
	QuestionnaireVersion string
	QuestionnaireTitle   string
	QuestionnaireDetail  *QuestionnaireDetailResponse
	RequiresAssessment   bool
}

type dailySimulationProfile struct {
	Index         int
	RunDate       time.Time
	GuardianName  string
	GuardianPhone string
	GuardianEmail string
	ChildName     string
	ChildDOB      string
	ChildGender   uint8
}

type dailySimulationOutcome struct {
	UserCreated       bool
	TesteeCreated     bool
	PlanEnrolled      bool
	PlanID            string
	EntryResolved     bool
	EntryIntaked      bool
	AnswerSheetID     string
	AssessmentID      string
	SkippedSubmission bool
	JourneyTarget     string
	StopReason        string
}

type dailySimulationCounters struct {
	userCreated       int64
	testeeCreated     int64
	enrolled          int64
	resolved          int64
	intaked           int64
	submitted         int64
	skippedSubmission int64
	assessmentCreated int64
	failed            int64
}

type dailySimulationJourneyTarget string

const (
	dailySimulationJourneyRegisterOnly dailySimulationJourneyTarget = "register_only"
	dailySimulationJourneyCreateTestee dailySimulationJourneyTarget = "create_testee"
	dailySimulationJourneyResolveEntry dailySimulationJourneyTarget = "resolve_entry"
	dailySimulationJourneySubmitAnswer dailySimulationJourneyTarget = "submit_answer"
)

type dailySimulationJourneyStage string

const (
	dailySimulationJourneyStageGuardianAccount dailySimulationJourneyStage = "guardian_account"
	dailySimulationJourneyStageTesteeProfile   dailySimulationJourneyStage = "testee_profile"
	dailySimulationJourneyStagePlanEnrollment  dailySimulationJourneyStage = "plan_enrollment"
	dailySimulationJourneyStageAssessmentEntry dailySimulationJourneyStage = "assessment_entry"
	dailySimulationJourneyStageAnswerSheet     dailySimulationJourneyStage = "answersheet_submit"
)

type dailySimulationJourneyState struct {
	deps        *dependencies
	iamBundle   *dailySimulationIAMBundle
	cfg         DailySimulationConfig
	profile     dailySimulationProfile
	clinicianID string
	entry       *AssessmentEntryResponse
	target      *dailySimulationResolvedTarget
	planID      string

	journeyTarget    dailySimulationJourneyTarget
	guardianUserID   string
	guardianToken    string
	mockIAMLimiter   chan struct{}
	collectionClient *APIClient
	existingTestee   *ApiserverTesteeResponse
	testee           *TesteeResponse
	outcome          dailySimulationOutcome
}

func (c *dailySimulationCounters) add(outcome dailySimulationOutcome) {
	if outcome.UserCreated {
		atomic.AddInt64(&c.userCreated, 1)
	}
	if outcome.TesteeCreated {
		atomic.AddInt64(&c.testeeCreated, 1)
	}
	if outcome.PlanEnrolled {
		atomic.AddInt64(&c.enrolled, 1)
	}
	if outcome.EntryResolved {
		atomic.AddInt64(&c.resolved, 1)
	}
	if outcome.EntryIntaked {
		atomic.AddInt64(&c.intaked, 1)
	}
	if strings.TrimSpace(outcome.AnswerSheetID) != "" {
		atomic.AddInt64(&c.submitted, 1)
	}
	if outcome.SkippedSubmission {
		atomic.AddInt64(&c.skippedSubmission, 1)
	}
	if strings.TrimSpace(outcome.AssessmentID) != "" {
		atomic.AddInt64(&c.assessmentCreated, 1)
	}
}

func (c *dailySimulationCounters) addFailure() {
	atomic.AddInt64(&c.failed, 1)
}

/**
 * 模拟每日用户
 *
 * @param ctx 上下文
 * @param deps 依赖
 * @param iamBundle IAM 绑定
 * @param cfg 配置
 * @param profile 用户信息
 */
func simulateDailyUser(
	ctx context.Context,
	deps *dependencies,
	iamBundle *dailySimulationIAMBundle,
	cfg DailySimulationConfig,
	profile dailySimulationProfile,
	clinicianID string,
	entry *AssessmentEntryResponse,
	target *dailySimulationResolvedTarget,
	mockIAMLimiter chan struct{},
	existingTestee *ApiserverTesteeResponse,
) (dailySimulationOutcome, error) {
	return simulateDailyUserWithAdditionalTargets(
		ctx,
		deps,
		iamBundle,
		cfg,
		profile,
		clinicianID,
		entry,
		target,
		nil,
		mockIAMLimiter,
		existingTestee,
	)
}

func simulateDailyUserWithAdditionalTargets(
	ctx context.Context,
	deps *dependencies,
	iamBundle *dailySimulationIAMBundle,
	cfg DailySimulationConfig,
	profile dailySimulationProfile,
	clinicianID string,
	entry *AssessmentEntryResponse,
	target *dailySimulationResolvedTarget,
	additionalTargets []*dailySimulationResolvedTarget,
	mockIAMLimiter chan struct{},
	existingTestee *ApiserverTesteeResponse,
) (dailySimulationOutcome, error) {
	state := &dailySimulationJourneyState{
		deps:           deps,
		iamBundle:      iamBundle,
		cfg:            cfg,
		profile:        profile,
		clinicianID:    clinicianID,
		entry:          entry,
		target:         target,
		mockIAMLimiter: mockIAMLimiter,
		existingTestee: existingTestee,
		planID:         selectDailySimulationPlanID(cfg, profile.RunDate, profile.Index),
		journeyTarget:  resolveDailySimulationJourneyTarget(cfg, profile.RunDate, profile.Index),
	}
	if state.entry == nil {
		return state.outcome, fmt.Errorf("daily simulation entry is nil")
	}
	if state.target == nil {
		return state.outcome, fmt.Errorf("daily simulation target is nil")
	}

	state.outcome.JourneyTarget = string(state.journeyTarget)
	state.outcome.PlanID = state.planID

	decision, err := toolchain.Run(ctx, "daily_simulation_user", state,
		toolchain.FuncHandler[dailySimulationJourneyState]{HandlerName: string(dailySimulationJourneyStageGuardianAccount), HandlerFunc: dailySimulationStageEnsureGuardianAccount},
		toolchain.FuncHandler[dailySimulationJourneyState]{HandlerName: string(dailySimulationJourneyStageTesteeProfile), HandlerFunc: dailySimulationStageEnsureTestee},
		toolchain.FuncHandler[dailySimulationJourneyState]{HandlerName: string(dailySimulationJourneyStageAssessmentEntry), HandlerFunc: dailySimulationStageEnsureEntryAccess},
		toolchain.FuncHandler[dailySimulationJourneyState]{HandlerName: string(dailySimulationJourneyStagePlanEnrollment), HandlerFunc: dailySimulationStageEnrollPlan},
		toolchain.FuncHandler[dailySimulationJourneyState]{HandlerName: string(dailySimulationJourneyStageAnswerSheet), HandlerFunc: dailySimulationStageSubmitAnswerSheet},
	)
	if err != nil {
		return state.outcome, err
	}
	if state.outcome.StopReason == "" {
		if decision.StopReason != "" {
			state.outcome.StopReason = decision.StopReason
		} else {
			state.outcome.StopReason = "completed"
		}
	}

	if err := logDailySimulationOutcome(
		deps,
		profile,
		clinicianID,
		entry,
		target,
		dailySimulationTesteeID(state.testee),
		state.guardianUserID,
		state.outcome,
	); err != nil {
		return state.outcome, err
	}
	if state.journeyTarget != dailySimulationJourneySubmitAnswer {
		return state.outcome, nil
	}
	for _, additionalTarget := range additionalTargets {
		if err := simulateDailyUserAdditionalTarget(ctx, deps, profile, state, additionalTarget); err != nil {
			return state.outcome, err
		}
	}
	return state.outcome, nil
}

func simulateDailyUserAdditionalTarget(
	ctx context.Context,
	deps *dependencies,
	profile dailySimulationProfile,
	state *dailySimulationJourneyState,
	target *dailySimulationResolvedTarget,
) error {
	if target == nil {
		return fmt.Errorf("daily simulation target is nil")
	}
	state.target = target
	state.outcome.AnswerSheetID = ""
	state.outcome.AssessmentID = ""
	state.outcome.SkippedSubmission = false

	if _, err := dailySimulationStageSubmitAnswerSheet(ctx, state); err != nil {
		return err
	}
	return logDailySimulationOutcome(
		deps,
		profile,
		state.clinicianID,
		state.entry,
		target,
		dailySimulationTesteeID(state.testee),
		state.guardianUserID,
		state.outcome,
	)
}

func dailySimulationStageEnsureGuardianAccount(ctx context.Context, state *dailySimulationJourneyState) (toolchain.Decision, error) {
	var (
		guardianUserID string
		guardianToken  string
		userCreated    bool
		err            error
	)
	if dailySimulationUsesIAMMockConsumer(state.deps.Config.IAM) {
		release, err := acquireDailySimulationMockIAMLimiter(ctx, state.mockIAMLimiter)
		if err != nil {
			return toolchain.Decision{}, err
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
			ID:   testeeID,
			Name: strings.TrimSpace(state.existingTestee.Name),
		}
		state.outcome.TesteeCreated = false
		return state.nextDecision(dailySimulationJourneyStageTesteeProfile), nil
	}
	// 受试者档案统一在公开 intake 阶段创建/绑定，避免 collection 预建档或后台 assign 关系。
	return state.nextDecision(dailySimulationJourneyStageTesteeProfile), nil
}

func dailySimulationStageEnrollPlan(ctx context.Context, state *dailySimulationJourneyState) (toolchain.Decision, error) {
	if strings.TrimSpace(state.planID) == "" {
		return toolchain.Decision{}, fmt.Errorf("dailySimulation.planIds resolved empty plan")
	}
	if state.testee == nil || strings.TrimSpace(state.testee.ID) == "" {
		return toolchain.Decision{}, fmt.Errorf("testee is not initialized before plan enrollment")
	}
	if _, err := state.deps.APIClient.EnrollTesteeInPlan(ctx, EnrollTesteeRequest{
		PlanID:    state.planID,
		TesteeID:  state.testee.ID,
		StartDate: state.profile.RunDate.Format("2006-01-02"),
	}); err != nil {
		return toolchain.Decision{}, err
	}
	state.outcome.PlanEnrolled = true
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
	state.testee = &TesteeResponse{
		ID:        strings.TrimSpace(resp.Testee.ID),
		Name:      strings.TrimSpace(resp.Testee.Name),
		CreatedAt: resp.Testee.CreatedAt,
		UpdatedAt: resp.Testee.UpdatedAt,
	}
	if state.existingTestee != nil {
		state.existingTestee.ID = state.testee.ID
		state.existingTestee.Name = state.testee.Name
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

func isDailySimulationAPIHTTPStatus(err error, status int) bool {
	if err == nil {
		return false
	}
	statusToken := fmt.Sprintf("http_status=%d", status)
	if strings.Contains(err.Error(), statusToken) {
		return true
	}
	return strings.Contains(err.Error(), fmt.Sprintf("status=%d", status))
}

func (state *dailySimulationJourneyState) nextDecision(stage dailySimulationJourneyStage) toolchain.Decision {
	if shouldStopDailySimulationJourneyAfter(state.journeyTarget, stage) {
		reason := "target_reached:" + string(state.journeyTarget)
		state.outcome.StopReason = reason
		return toolchain.Stop(reason)
	}
	return toolchain.Next()
}

func shouldStopDailySimulationJourneyAfter(target dailySimulationJourneyTarget, stage dailySimulationJourneyStage) bool {
	switch target {
	case dailySimulationJourneyRegisterOnly:
		return stage == dailySimulationJourneyStageGuardianAccount
	case dailySimulationJourneyCreateTestee:
		return stage == dailySimulationJourneyStageAssessmentEntry
	case dailySimulationJourneyResolveEntry:
		return stage == dailySimulationJourneyStageAssessmentEntry
	case dailySimulationJourneySubmitAnswer:
		return stage == dailySimulationJourneyStageAnswerSheet
	default:
		return stage == dailySimulationJourneyStageAnswerSheet
	}
}

func resolveDailySimulationJourneyTarget(cfg DailySimulationConfig, runDate time.Time, index int) dailySimulationJourneyTarget {
	mix := normalizeDailySimulationJourneyMix(cfg.JourneyMix)
	totalWeight := totalDailySimulationJourneyWeight(mix)
	if totalWeight <= 0 {
		return dailySimulationJourneySubmitAnswer
	}

	bucket := int(newDailySimulationRand(
		fmt.Sprintf("journey:%s:%d", runDate.Format("20060102"), index),
	).Int63n(int64(totalWeight)))
	switch {
	case bucket < mix.RegisterOnlyWeight:
		return dailySimulationJourneyRegisterOnly
	case bucket < mix.RegisterOnlyWeight+mix.CreateTesteeWeight:
		return dailySimulationJourneyCreateTestee
	case bucket < mix.RegisterOnlyWeight+mix.CreateTesteeWeight+mix.ResolveEntryWeight:
		return dailySimulationJourneyResolveEntry
	default:
		return dailySimulationJourneySubmitAnswer
	}
}

func normalizeDailySimulationJourneyMix(cfg DailySimulationJourneyMixConfig) DailySimulationJourneyMixConfig {
	if cfg.RegisterOnlyWeight < 0 {
		cfg.RegisterOnlyWeight = 0
	}
	if cfg.CreateTesteeWeight < 0 {
		cfg.CreateTesteeWeight = 0
	}
	if cfg.ResolveEntryWeight < 0 {
		cfg.ResolveEntryWeight = 0
	}
	if cfg.SubmitAnswerWeight < 0 {
		cfg.SubmitAnswerWeight = 0
	}
	if totalDailySimulationJourneyWeight(cfg) == 0 {
		cfg.SubmitAnswerWeight = 100
	}
	return cfg
}

func totalDailySimulationJourneyWeight(cfg DailySimulationJourneyMixConfig) int {
	return cfg.RegisterOnlyWeight + cfg.CreateTesteeWeight + cfg.ResolveEntryWeight + cfg.SubmitAnswerWeight
}

func logDailySimulationOutcome(
	deps *dependencies,
	profile dailySimulationProfile,
	clinicianID string,
	entry *AssessmentEntryResponse,
	target *dailySimulationResolvedTarget,
	testeeID string,
	guardianUserID string,
	outcome dailySimulationOutcome,
) error {
	deps.Logger.Infow("Daily simulation user completed",
		"index", profile.Index,
		"run_date", profile.RunDate.Format("2006-01-02"),
		"guardian_name", profile.GuardianName,
		"guardian_phone", profile.GuardianPhone,
		"guardian_email", profile.GuardianEmail,
		"guardian_user_id", guardianUserID,
		"child_name", profile.ChildName,
		"child_dob", profile.ChildDOB,
		"testee_id", testeeID,
		"clinician_id", clinicianID,
		"plan_id", outcome.PlanID,
		"entry_id", dailySimulationEntryID(entry),
		"target_type", dailySimulationTargetType(target),
		"target_code", dailySimulationTargetCode(target),
		"user_created", outcome.UserCreated,
		"testee_created", outcome.TesteeCreated,
		"plan_enrolled", outcome.PlanEnrolled,
		"entry_resolved", outcome.EntryResolved,
		"entry_intaked", outcome.EntryIntaked,
		"answersheet_id", outcome.AnswerSheetID,
		"assessment_id", outcome.AssessmentID,
		"submission_skipped", outcome.SkippedSubmission,
		"journey_target", outcome.JourneyTarget,
		"stop_reason", outcome.StopReason,
	)
	return nil
}

func dailySimulationTesteeID(testee *TesteeResponse) string {
	if testee == nil {
		return ""
	}
	return strings.TrimSpace(testee.ID)
}

func dailySimulationEntryID(entry *AssessmentEntryResponse) string {
	if entry == nil {
		return ""
	}
	return strings.TrimSpace(entry.ID)
}

func dailySimulationTargetType(target *dailySimulationResolvedTarget) string {
	if target == nil {
		return ""
	}
	return strings.TrimSpace(target.TargetType)
}

func dailySimulationTargetCode(target *dailySimulationResolvedTarget) string {
	if target == nil {
		return ""
	}
	return strings.TrimSpace(target.TargetCode)
}

func ensureDailySimulationEntryAndTarget(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
) (*AssessmentEntryResponse, *dailySimulationResolvedTarget, string, error) {
	var clinicianID string

	if !cfg.EntryID.IsZero() {
		entry, err := deps.APIClient.GetAssessmentEntry(ctx, cfg.EntryID.String())
		if err != nil {
			return nil, nil, "", fmt.Errorf("get daily simulation entry %s: %w", cfg.EntryID.String(), err)
		}
		if entry == nil {
			return nil, nil, "", fmt.Errorf("daily simulation entry %s not found", cfg.EntryID.String())
		}
		if !entry.IsActive {
			entry, err = deps.APIClient.ReactivateAssessmentEntry(ctx, entry.ID)
			if err != nil {
				return nil, nil, "", fmt.Errorf("reactivate daily simulation entry %s: %w", entry.ID, err)
			}
		}
		clinicianID = strings.TrimSpace(entry.ClinicianID)
		if clinicianID == "" {
			return nil, nil, "", fmt.Errorf("daily simulation entry %s has empty clinician_id", entry.ID)
		}
		target, err := resolveDailySimulationTarget(ctx, deps.APIClient, deps.CollectionClient, entry.TargetType, entry.TargetCode, entry.TargetVersion)
		if err != nil {
			return nil, nil, "", err
		}
		return entry, target, clinicianID, nil
	}

	clinicianIDs := collectDailySimulationClinicianIDs(cfg.ClinicianIDs)
	if len(clinicianIDs) == 0 {
		return nil, nil, "", fmt.Errorf("dailySimulation clinicianIds is required when entryId is not set")
	}
	clinicianID = clinicianIDs[0]

	targetType := strings.ToLower(strings.TrimSpace(cfg.TargetType))
	targetCode := strings.TrimSpace(cfg.TargetCode)
	targetVersion := strings.TrimSpace(cfg.TargetVersion)
	if targetType == "" || targetCode == "" {
		return nil, nil, "", fmt.Errorf("dailySimulation targetType and targetCode are required when entryId is not set")
	}

	entries, err := listAllClinicianAssessmentEntries(ctx, deps.APIClient, clinicianID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("list daily simulation clinician assessment entries: %w", err)
	}
	targetKey := assessmentEntryTargetKey(targetType, targetCode, targetVersion)
	for _, item := range entries {
		if item == nil {
			continue
		}
		if assessmentEntryTargetKey(item.TargetType, item.TargetCode, item.TargetVersion) != targetKey {
			continue
		}
		if !item.IsActive {
			item, err = deps.APIClient.ReactivateAssessmentEntry(ctx, item.ID)
			if err != nil {
				return nil, nil, "", fmt.Errorf("reactivate daily simulation entry %s: %w", item.ID, err)
			}
		}
		target, err := resolveDailySimulationTarget(ctx, deps.APIClient, deps.CollectionClient, item.TargetType, item.TargetCode, item.TargetVersion)
		if err != nil {
			return nil, nil, "", err
		}
		return item, target, clinicianID, nil
	}

	entry, err := deps.APIClient.CreateClinicianAssessmentEntry(ctx, clinicianID, CreateAssessmentEntryRequest{
		TargetType:    targetType,
		TargetCode:    targetCode,
		TargetVersion: targetVersion,
	})
	if err != nil {
		return nil, nil, "", fmt.Errorf("create daily simulation entry: %w", err)
	}
	target, err := resolveDailySimulationTarget(ctx, deps.APIClient, deps.CollectionClient, entry.TargetType, entry.TargetCode, entry.TargetVersion)
	if err != nil {
		return nil, nil, "", err
	}
	return entry, target, clinicianID, nil
}

func resolveDailySimulationTarget(
	ctx context.Context,
	apiClient *APIClient,
	collectionClient *APIClient,
	targetType, targetCode, targetVersion string,
) (*dailySimulationResolvedTarget, error) {
	targetType = strings.ToLower(strings.TrimSpace(targetType))
	targetCode = strings.TrimSpace(targetCode)
	targetVersion = strings.TrimSpace(targetVersion)
	if targetType == "" || targetCode == "" {
		return nil, fmt.Errorf("daily simulation targetType and targetCode are required")
	}

	switch targetType {
	case "scale":
		if apiClient == nil {
			return nil, fmt.Errorf("apiserver client is not initialized")
		}
		if collectionClient == nil {
			return nil, fmt.Errorf("collection client is not initialized")
		}
		scaleItem, err := apiClient.GetPublishedAssessmentModel(ctx, targetCode, targetVersion)
		if err != nil {
			return nil, fmt.Errorf("get scale %s: %w", targetCode, err)
		}
		if scaleItem == nil {
			return nil, fmt.Errorf("scale %s not found", targetCode)
		}
		questionnaireVersion := strings.TrimSpace(scaleItem.QuestionnaireVersion)
		if questionnaireVersion == "" {
			return nil, fmt.Errorf("published assessment model %s has empty questionnaire_version", targetCode)
		}
		detail, err := collectionClient.GetPublishedQuestionnaire(ctx, scaleItem.QuestionnaireCode, questionnaireVersion)
		if err != nil {
			return nil, fmt.Errorf("get questionnaire %s for scale %s: %w", scaleItem.QuestionnaireCode, targetCode, err)
		}
		return &dailySimulationResolvedTarget{
			TargetType:           targetType,
			TargetCode:           targetCode,
			TargetVersion:        targetVersion,
			QuestionnaireCode:    strings.TrimSpace(scaleItem.QuestionnaireCode),
			QuestionnaireVersion: questionnaireVersion,
			QuestionnaireTitle:   strings.TrimSpace(detail.Title),
			QuestionnaireDetail:  detail,
			RequiresAssessment:   true,
		}, nil
	case "questionnaire":
		if collectionClient == nil {
			return nil, fmt.Errorf("collection client is not initialized")
		}
		detail, err := collectionClient.GetPublishedQuestionnaire(ctx, targetCode, targetVersion)
		if err != nil {
			return nil, fmt.Errorf("get questionnaire %s: %w", targetCode, err)
		}
		if detail == nil {
			return nil, fmt.Errorf("questionnaire %s not found", targetCode)
		}
		version := targetVersion
		if version == "" {
			version = strings.TrimSpace(detail.Version)
		}
		return &dailySimulationResolvedTarget{
			TargetType:           targetType,
			TargetCode:           targetCode,
			TargetVersion:        targetVersion,
			QuestionnaireCode:    targetCode,
			QuestionnaireVersion: version,
			QuestionnaireTitle:   strings.TrimSpace(detail.Title),
			QuestionnaireDetail:  detail,
			RequiresAssessment:   strings.EqualFold(strings.TrimSpace(detail.Type), questionnaireTypeMedicalScale),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported daily simulation targetType %q", targetType)
	}
}

func newDailySimulationIAMBundle(
	ctx context.Context,
	cfg IAMConfig,
	orgID int64,
) (*dailySimulationIAMBundle, error) {
	if strings.TrimSpace(cfg.GRPC.Address) == "" {
		return nil, fmt.Errorf("daily_simulation requires iam.grpc.address")
	}

	timeout := 15 * time.Second
	if strings.TrimSpace(cfg.GRPC.Timeout) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(cfg.GRPC.Timeout))
		if err != nil {
			return nil, fmt.Errorf("invalid iam.grpc.timeout %q: %w", cfg.GRPC.Timeout, err)
		}
		timeout = parsed
	}

	clientCfg := &sdk.Config{
		Endpoint: cfg.GRPC.Address,
		Timeout:  timeout,
	}
	if cfg.GRPC.RetryMax > 0 {
		clientCfg.Retry = &sdk.RetryConfig{
			Enabled:     true,
			MaxAttempts: cfg.GRPC.RetryMax,
		}
	}
	if cfg.GRPC.TLS.Enabled {
		clientCfg.TLS = &sdk.TLSConfig{
			Enabled:            true,
			CACert:             strings.TrimSpace(cfg.GRPC.TLS.CAFile),
			ClientCert:         strings.TrimSpace(cfg.GRPC.TLS.CertFile),
			ClientKey:          strings.TrimSpace(cfg.GRPC.TLS.KeyFile),
			ServerName:         strings.TrimSpace(cfg.GRPC.TLS.ServerName),
			InsecureSkipVerify: cfg.GRPC.TLS.InsecureSkipVerify,
		}
	}

	client, err := sdk.NewClient(ctx, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("create daily simulation iam grpc client: %w", err)
	}
	return &dailySimulationIAMBundle{
		client:   client,
		identity: client.Identity(),
	}, nil
}

func ensureDailySimulationGuardianAccount(
	ctx context.Context,
	deps *dependencies,
	iamBundle *dailySimulationIAMBundle,
	cfg DailySimulationConfig,
	profile dailySimulationProfile,
) (string, string, bool, error) {
	password := normalizeDailySimulationPassword(cfg.UserPassword)
	userID, err := findDailySimulationIAMUser(ctx, iamBundle, profile.GuardianPhone, profile.GuardianEmail)
	if err != nil {
		return "", "", false, err
	}
	if strings.TrimSpace(userID) == "" {
		return "", "", false, fmt.Errorf("daily_simulation guardian provisioning requires iam.mockConsumer.enabled=true with IAM v2; IAM AuthN gRPC no longer exposes password account onboarding")
	}

	loginURL, err := resolveDailySimulationIAMLoginURL(deps.Config.IAM)
	if err != nil {
		return "", "", false, err
	}
	tenantID := resolveDailySimulationTenantID(deps.Config.IAM, deps.Config.Global.OrgID)
	deviceID := fmt.Sprintf("%s-%s-%03d", dailySimulationDeviceIDPrefix, profile.RunDate.Format("20060102"), profile.Index+1)

	token, err := tryDailySimulationGuardianLogin(ctx, loginURL, tenantID, deviceID, profile.GuardianEmail, profile.GuardianPhone, password, deps.Logger)
	if err == nil {
		return userID, token, false, nil
	}

	return "", "", false, fmt.Errorf("login existing guardian %s: %w; IAM v2 password onboarding is only available through iam.mockConsumer REST ensure", profile.GuardianEmail, err)
}

func ensureDailySimulationGuardianMockConsumer(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
	profile dailySimulationProfile,
) (string, string, bool, error) {
	baseURL, err := resolveDailySimulationIAMBaseURL(deps.Config.IAM)
	if err != nil {
		return "", "", false, err
	}
	endpointPath := resolveDailySimulationIAMMockConsumerEndpointPath(deps.Config.IAM)
	sharedSecret := strings.TrimSpace(deps.Config.IAM.MockConsumer.SharedSecret)
	if sharedSecret == "" {
		return "", "", false, fmt.Errorf("iam.mockConsumer.sharedSecret is required for daily_simulation mock-consumer mode")
	}

	password := normalizeDailySimulationPassword(cfg.UserPassword)
	client := NewAPIClient(baseURL, "", deps.Logger)
	configureDailySimulationMockIAMClient(client)

	ensureResp, err := client.EnsureIAMMockConsumer(ctx, endpointPath, EnsureIAMMockConsumerRequest{
		Name:     profile.GuardianName,
		Phone:    profile.GuardianPhone,
		Email:    profile.GuardianEmail,
		Password: password,
	}, sharedSecret)
	if err != nil {
		return "", "", false, fmt.Errorf("ensure guardian mock-consumer %s: %w", profile.GuardianEmail, err)
	}
	if ensureResp == nil || strings.TrimSpace(ensureResp.UserID) == "" {
		return "", "", false, fmt.Errorf("ensure guardian mock-consumer returned empty user id")
	}

	loginURL, err := resolveDailySimulationIAMLoginURL(deps.Config.IAM)
	if err != nil {
		return "", "", false, err
	}
	// IAM mock-consumer onboarding creates a username identity in the default
	// realm. Password login must therefore omit tenant_id; IAM will default the
	// principal tenant before issuing the token.
	tenantID := ""
	deviceID := fmt.Sprintf("%s-%s-%03d", dailySimulationDeviceIDPrefix, profile.RunDate.Format("20060102"), profile.Index+1)

	token, err := tryDailySimulationGuardianLoginWithRetry(ctx, loginURL, tenantID, deviceID, profile.GuardianEmail, profile.GuardianPhone, password, deps.Logger)
	if err != nil {
		return "", "", false, fmt.Errorf("login guardian %s after ensuring mock-consumer: %w", profile.GuardianEmail, err)
	}
	return strings.TrimSpace(ensureResp.UserID), token, ensureResp.IsNewUser, nil
}

func findDailySimulationIAMUser(
	ctx context.Context,
	iamBundle *dailySimulationIAMBundle,
	phone, email string,
) (string, error) {
	resp, err := iamBundle.identity.SearchUsers(ctx, &identityv2.SearchUsersRequest{
		Phones: []string{normalizePhone(phone)},
		Emails: []string{normalizeEmail(email)},
	})
	if err != nil {
		return "", fmt.Errorf("search iam users by phone/email: %w", err)
	}
	for _, item := range resp.GetUsers() {
		if item == nil {
			continue
		}
		if strings.TrimSpace(item.GetId()) != "" {
			return strings.TrimSpace(item.GetId()), nil
		}
	}
	return "", nil
}

func tryDailySimulationGuardianLogin(
	ctx context.Context,
	loginURL, tenantID, deviceID, email, phone, password string,
	logger log.Logger,
) (string, error) {
	credentials := []string{normalizeEmail(email), normalizePhone(phone)}
	var lastErr error
	for _, username := range credentials {
		if strings.TrimSpace(username) == "" {
			continue
		}
		token, err := fetchTokenFromIAMWithPassword(ctx, loginURL, username, password, tenantID, deviceID, logger)
		if err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
		if err == nil {
			err = fmt.Errorf("iam login returned empty token for username %s", username)
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no available guardian login username")
	}
	return "", lastErr
}

func tryDailySimulationGuardianLoginWithRetry(
	ctx context.Context,
	loginURL, tenantID, deviceID, email, phone, password string,
	logger log.Logger,
) (string, error) {
	const maxAttempts = 2

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		token, err := tryDailySimulationGuardianLogin(ctx, loginURL, tenantID, deviceID, email, phone, password, logger)
		if err == nil {
			return token, nil
		}
		lastErr = err
		if attempt+1 >= maxAttempts || !shouldRetryDailySimulationIAMLogin(err) {
			break
		}
		delay := dailySimulationIAMBackoffDelay(attempt, dailySimulationMockIAMRetryMinDelay, dailySimulationMockIAMRetryMaxDelay)
		if waitErr := scheduler.Wait(ctx, delay); waitErr != nil {
			return "", waitErr
		}
	}
	return "", lastErr
}

func shouldRetryDailySimulationIAMLogin(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sdkerrors.ErrRateLimited) ||
		errors.Is(err, sdkerrors.ErrServiceUnavailable) ||
		errors.Is(err, sdkerrors.ErrTimeout) {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if message == "" {
		return false
	}
	return strings.Contains(message, "context deadline exceeded") ||
		strings.Contains(message, "too many requests") ||
		strings.Contains(message, "service unavailable") ||
		strings.Contains(message, "bad gateway") ||
		strings.Contains(message, "gateway timeout") ||
		strings.Contains(message, "status=429") ||
		strings.Contains(message, "status=502") ||
		strings.Contains(message, "status=503") ||
		strings.Contains(message, "status=504")
}

func dailySimulationIAMBackoffDelay(attempt int, minDelay, maxDelay time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := minDelay << attempt
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

func configureDailySimulationMockIAMClient(client *APIClient) {
	if client == nil {
		return
	}
	client.SetHTTPTimeout(dailySimulationMockIAMTimeout)
	client.SetRetryConfig(RetryConfig{
		MaxRetries: dailySimulationMockIAMRetryMax,
		MinDelay:   dailySimulationMockIAMRetryMinDelay.String(),
		MaxDelay:   dailySimulationMockIAMRetryMaxDelay.String(),
	})
}

func acquireDailySimulationMockIAMLimiter(ctx context.Context, limiter chan struct{}) (func(), error) {
	if limiter == nil {
		return func() {}, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case limiter <- struct{}{}:
		return func() {
			select {
			case <-limiter:
			default:
			}
		}, nil
	}
}

func ensureDailySimulationTestee(
	ctx context.Context,
	collectionClient *APIClient,
	cfg DailySimulationConfig,
	profile dailySimulationProfile,
) (*TesteeResponse, bool, error) {
	testeeResp, err := collectionClient.CreateCollectionTestee(ctx, CollectionCreateTesteeRequest{
		Name:       profile.ChildName,
		Gender:     int32(profile.ChildGender),
		Birthday:   profile.ChildDOB,
		Relation:   normalizeDailySimulationGuardianRelation(cfg.GuardianRelation),
		Tags:       append([]string(nil), cfg.TesteeTags...),
		Source:     normalizeDailySimulationSource(cfg.TesteeSource),
		IsKeyFocus: cfg.IsKeyFocus,
	})
	if err != nil {
		return nil, false, fmt.Errorf("create collection testee %s: %w", profile.ChildName, err)
	}
	return testeeResp, true, nil
}

func hasAssessmentEntryRelation(
	ctx context.Context,
	apiClient *APIClient,
	testeeID, entryID string,
) (bool, error) {
	relations, err := apiClient.GetTesteeClinicians(ctx, testeeID)
	if err != nil {
		return false, fmt.Errorf("list testee clinicians for %s: %w", testeeID, err)
	}
	for _, item := range relations.Items {
		if item == nil || item.Relation == nil {
			continue
		}
		relation := item.Relation
		if !relation.IsActive {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(relation.SourceType), "assessment_entry") {
			continue
		}
		if strings.TrimSpace(nullableString(relation.SourceID)) == strings.TrimSpace(entryID) {
			return true, nil
		}
	}
	return false, nil
}

func buildDailySimulationProfile(cfg DailySimulationConfig, runDate time.Time, idx int) dailySimulationProfile {
	return buildSeedProfile(cfg, runDate, idx)
}

func selectDailySimulationPlanID(cfg DailySimulationConfig, runDate time.Time, index int) string {
	planIDs := collectDailySimulationPlanIDs(cfg.PlanIDs)
	if len(planIDs) == 0 {
		return ""
	}
	if len(planIDs) == 1 {
		return planIDs[0]
	}
	rng := newDailySimulationRand(fmt.Sprintf("plan:%s:%d", runDate.Format("20060102"), index))
	return planIDs[rng.Intn(len(planIDs))]
}

func newDailySimulationRand(seed string) *rand.Rand {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(seed))
	return rand.New(rand.NewSource(int64(hash.Sum64())))
}

func resolveDailySimulationRunDate(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		now := time.Now().In(time.Local)
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local), nil
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.Local), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid dailySimulation.runDate %q", raw)
}

func resolveDailySimulationIAMLoginURL(cfg IAMConfig) (string, error) {
	loginURL, err := seediauth.ResolveLoginURL(seediauth.Config{
		BaseURL:  cfg.BaseURL,
		LoginURL: cfg.LoginURL,
	})
	if err != nil {
		return "", fmt.Errorf("iam.loginUrl or iam.baseUrl is required for daily_simulation: %w", err)
	}
	return loginURL, nil
}

func resolveDailySimulationIAMBaseURL(cfg IAMConfig) (string, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return "", fmt.Errorf("iam.baseUrl is required for daily_simulation")
	}
	return base, nil
}

func resolveDailySimulationIAMMockConsumerEndpointPath(cfg IAMConfig) string {
	path := strings.TrimSpace(cfg.MockConsumer.EndpointPath)
	if path == "" {
		return "/api/v2/internal/authn/mock-consumers/ensure"
	}
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}

func dailySimulationUsesIAMMockConsumer(cfg IAMConfig) bool {
	return cfg.MockConsumer.Enabled
}

func resolveDailySimulationTenantID(cfg IAMConfig, orgID int64) string {
	if strings.TrimSpace(cfg.TenantID) != "" {
		return strings.TrimSpace(cfg.TenantID)
	}
	if orgID > 0 {
		return strconv.FormatInt(orgID, 10)
	}
	return ""
}

func normalizeDailySimulationWorkers(value, count int) int {
	if value <= 0 {
		value = dailySimulationDefaultWorkers
	}
	if count > 0 && value > count {
		return count
	}
	return value
}

func normalizeDailySimulationPassword(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return dailySimulationDefaultPassword
	}
	return value
}

func normalizeDailySimulationSource(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return dailySimulationDefaultSource
	}
	return value
}

func parseDailySimulationDOB(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
