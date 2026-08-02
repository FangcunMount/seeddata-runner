package dailysim

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	sdk "github.com/FangcunMount/iam/v2/pkg/sdk"
	"github.com/FangcunMount/iam/v2/pkg/sdk/identity"
	toolchain "github.com/FangcunMount/seeddata-runner/internal/chain"
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
	GuardianUserID    string
	TesteeID          string
	IAMProfileID      string
	IAMProfileLinkID  string
	EnrollmentID      string
	TaskIDs           []string
	UserCreated       bool
	TesteeCreated     bool
	PlanEnrolled      bool
	PlanID            string
	EntryResolved     bool
	EntryIntaked      bool
	AnswerSheetID     string
	AssessmentID      string
	ReportStatus      string
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

	submissionOriginRef *OriginRef

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
	state.outcome.ReportStatus = ""
	state.outcome.SkippedSubmission = false
	previousOrigin := state.submissionOriginRef
	state.submissionOriginRef = &OriginRef{Type: "self_service"}
	defer func() { state.submissionOriginRef = previousOrigin }()

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
		"enrollment_id", outcome.EnrollmentID,
		"task_ids", outcome.TaskIDs,
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
		"report_status", outcome.ReportStatus,
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
