package dailysim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
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
	"github.com/FangcunMount/seeddata-runner/internal/historicalseed"
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
	GuardianUserID      string
	TesteeID            string
	IAMProfileID        string
	IAMProfileLinkID    string
	EnrollmentID        string
	TaskIDs             []string
	CompletedTaskIDs    []string
	ChildScenarioIDs    []string
	AdditionalScenarios []HistoricalAdditionalScenarioManifest
	AnswerSheetIDs      []string
	AssessmentIDs       []string
	UserCreated         bool
	TesteeCreated       bool
	PlanEnrolled        bool
	PlanID              string
	EntryResolved       bool
	EntryIntaked        bool
	AnswerSheetID       string
	AssessmentID        string
	SkippedSubmission   bool
	JourneyTarget       string
	StopReason          string
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
	dailySimulationJourneyStageEntryResolve    dailySimulationJourneyStage = "entry_resolve"
	dailySimulationJourneyStageEntryIntake     dailySimulationJourneyStage = "entry_intake"
	dailySimulationJourneyStagePlanEnrollment  dailySimulationJourneyStage = "plan_enrollment"
	dailySimulationJourneyStageAnswerSheet     dailySimulationJourneyStage = "answersheet_submit"
)

type dailySimulationJourneyState struct {
	deps          *dependencies
	iamBundle     *dailySimulationIAMBundle
	cfg           DailySimulationConfig
	profile       dailySimulationProfile
	clinicianID   string
	entry         *AssessmentEntryResponse
	target        *dailySimulationResolvedTarget
	planID        string
	selectedTasks []historicalSelectedTask

	journeyTarget    dailySimulationJourneyTarget
	guardianUserID   string
	guardianToken    string
	mockIAMLimiter   chan struct{}
	collectionClient *APIClient
	existingTestee   *ApiserverTesteeResponse
	testee           *TesteeResponse
	outcome          dailySimulationOutcome
}

type historicalSelectedTask struct {
	ID        string
	Context   historicalseed.Context
	PlannedAt time.Time
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
		dailySimulationScenario{ClinicianID: clinicianID, Entry: entry, Target: target},
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
	scenario dailySimulationScenario,
	additionalTargets []*dailySimulationResolvedTarget,
	mockIAMLimiter chan struct{},
	existingTestee *ApiserverTesteeResponse,
) (dailySimulationOutcome, error) {
	journeyTarget, planID := resolveDailySimulationScenarioIdentity(cfg, profile.RunDate, profile.Index, scenario)
	state := &dailySimulationJourneyState{
		deps:           deps,
		iamBundle:      iamBundle,
		cfg:            cfg,
		profile:        profile,
		clinicianID:    scenario.ClinicianID,
		entry:          scenario.Entry,
		target:         scenario.Target,
		mockIAMLimiter: mockIAMLimiter,
		existingTestee: existingTestee,
		planID:         planID,
		journeyTarget:  journeyTarget,
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
		toolchain.FuncHandler[dailySimulationJourneyState]{HandlerName: string(dailySimulationJourneyStageEntryResolve), HandlerFunc: dailySimulationStageResolveEntry},
		toolchain.FuncHandler[dailySimulationJourneyState]{HandlerName: string(dailySimulationJourneyStageEntryIntake), HandlerFunc: dailySimulationStageIntakeEntry},
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
		scenario.ClinicianID,
		scenario.Entry,
		scenario.Target,
		dailySimulationTesteeID(state.testee),
		state.guardianUserID,
		state.outcome,
	); err != nil {
		return state.outcome, err
	}
	if state.journeyTarget != dailySimulationJourneySubmitAnswer {
		return state.outcome, nil
	}
	if executor := historicalSubmissionExecutorFromContext(ctx); executor != nil && len(additionalTargets) > 0 {
		if err := simulateHistoricalAdditionalTargets(ctx, executor, deps, profile, state, additionalTargets); err != nil {
			return state.outcome, err
		}
		return state.outcome, nil
	}
	for _, additionalTarget := range additionalTargets {
		if err := simulateDailyUserAdditionalTarget(ctx, deps, profile, state, additionalTarget); err != nil {
			return state.outcome, err
		}
	}
	return state.outcome, nil
}

func simulateHistoricalAdditionalTargets(
	ctx context.Context,
	executor *historicalSubmissionExecutor,
	deps *dependencies,
	profile dailySimulationProfile,
	state *dailySimulationJourneyState,
	targets []*dailySimulationResolvedTarget,
) error {
	testeeID := parseID(dailySimulationTesteeID(state.testee))
	if testeeID == 0 {
		return fmt.Errorf("invalid testee id for historical additional targets")
	}
	futures := make([]historicalSubmissionFuture, 0, len(targets))
	for _, target := range targets {
		if target == nil || target.QuestionnaireDetail == nil {
			return fmt.Errorf("historical additional target is not initialized")
		}
		historical, ok := historicalseed.FromContext(ctx)
		if !ok {
			return fmt.Errorf("historical context is required for concurrent additional target")
		}
		historical.ScenarioID = fmt.Sprintf("%s/%d/%s/%s", profile.RunDate.Format("2006-01-02"), profile.Index, dailySimulationJourneySubmitAnswer, target.TargetCode)
		targetCtx := historicalseed.WithContext(ctx, historical)
		additional := HistoricalAdditionalScenarioManifest{
			ScenarioID: historical.ScenarioID,
			TargetKey:  strings.Join([]string{target.TargetType, target.TargetCode}, "/"),
		}
		state.outcome.AdditionalScenarios = appendHistoricalAdditionalScenario(state.outcome.AdditionalScenarios, additional)
		jobState := cloneDailySimulationJourneyStateForSubmission(state)
		jobState.target = target
		future, err := executor.Submit(HistoricalSubmissionJob{
			ScenarioID: historical.ScenarioID,
			TargetKey:  additional.TargetKey,
		}, func(jobCtx context.Context) (historicalSubmissionJobResult, error) {
			jobCtx = mergeHistoricalSubmissionContext(jobCtx, targetCtx)
			if err := runDailySimulationSubmissionJob(jobCtx, jobState, "", target.QuestionnaireDetail, testeeID); err != nil {
				return historicalSubmissionJobResult{Outcome: jobState.outcome}, err
			}
			if err := logDailySimulationOutcome(
				deps, profile, jobState.clinicianID, jobState.entry, target,
				dailySimulationTesteeID(jobState.testee), jobState.guardianUserID, jobState.outcome,
			); err != nil {
				return historicalSubmissionJobResult{Outcome: jobState.outcome}, err
			}
			return historicalSubmissionJobResult{Outcome: jobState.outcome, ReportGenerated: target.RequiresAssessment}, nil
		})
		if err != nil {
			return err
		}
		futures = append(futures, future)
	}
	for _, future := range futures {
		result, err := future.Wait()
		if err != nil {
			return err
		}
		mergeDailySimulationSubmissionOutcome(&state.outcome, result.Outcome)
	}
	return nil
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
	selectedTasks := state.selectedTasks
	state.selectedTasks = nil
	defer func() { state.selectedTasks = selectedTasks }()
	if historical, ok := historicalseed.FromContext(ctx); ok {
		historical.ScenarioID = fmt.Sprintf("%s/%d/%s/%s", profile.RunDate.Format("2006-01-02"), profile.Index, dailySimulationJourneySubmitAnswer, target.TargetCode)
		ctx = historicalseed.WithContext(ctx, historical)
		state.outcome.AdditionalScenarios = append(state.outcome.AdditionalScenarios, HistoricalAdditionalScenarioManifest{
			ScenarioID: historical.ScenarioID,
			TargetKey:  strings.Join([]string{target.TargetType, target.TargetCode}, "/"),
		})
	}

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
	if historical, ok := historicalseed.FromContext(ctx); ok {
		if snapshot, exists := historicalScenarioSnapshot(ctx, historical.ScenarioID); exists {
			if record, completed := snapshot.Local[string(dailySimulationJourneyStageGuardianAccount)]; completed && record.Status == "completed" {
				guardianUserID = strings.TrimSpace(record.GuardianUserID)
				if guardianUserID == "" {
					return toolchain.Decision{}, fmt.Errorf("historical guardian_account stage has empty guardian_user_id")
				}
				guardianToken, err = restoreDailySimulationGuardianSession(ctx, state.deps, state.cfg, state.profile)
				if err != nil {
					return toolchain.Decision{}, err
				}
				return completeDailySimulationGuardianStage(ctx, state, guardianUserID, guardianToken, record.UserCreated)
			}
		}
	}
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
	return completeDailySimulationGuardianStage(ctx, state, guardianUserID, guardianToken, userCreated)
}

func completeDailySimulationGuardianStage(ctx context.Context, state *dailySimulationJourneyState, guardianUserID, guardianToken string, userCreated bool) (toolchain.Decision, error) {
	state.guardianUserID = guardianUserID
	state.outcome.GuardianUserID = guardianUserID
	state.guardianToken = guardianToken
	state.outcome.UserCreated = userCreated

	if strings.TrimSpace(guardianToken) == "" {
		return toolchain.Decision{}, fmt.Errorf("daily simulation guardian token is empty")
	}

	state.collectionClient = NewAPIClient(state.deps.CollectionClient.BaseURL(), guardianToken, state.deps.Logger)
	state.collectionClient.SetRetryConfig(state.deps.Config.API.Retry)
	state.collectionClient.SetHistoricalSecret(state.deps.CollectionClient.HistoricalSecret())
	if err := state.recordHistoricalLocalStage(ctx, dailySimulationJourneyStageGuardianAccount); err != nil {
		return toolchain.Decision{}, err
	}
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
		state.outcome.IAMProfileID = state.testee.IAMProfileID
		if historical, ok := historicalseed.FromContext(ctx); ok {
			if snapshot, exists := historicalScenarioSnapshot(ctx, historical.ScenarioID); exists {
				if record, completed := snapshot.Local[string(dailySimulationJourneyStageTesteeProfile)]; completed && record.Status == "completed" {
					if strings.TrimSpace(record.TesteeID) != testeeID {
						return toolchain.Decision{}, fmt.Errorf("historical testee_profile resource %s conflicts with restored testee %s", record.TesteeID, testeeID)
					}
					state.outcome.TesteeCreated = record.TesteeCreated
					state.outcome.IAMProfileID = firstHistoricalValue(record.IAMProfileID, state.outcome.IAMProfileID)
					state.outcome.IAMProfileLinkID = strings.TrimSpace(record.IAMProfileLinkID)
				}
			}
		}
		if err := state.recordHistoricalLocalStage(ctx, dailySimulationJourneyStageTesteeProfile); err != nil {
			return toolchain.Decision{}, err
		}
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
	if err := state.recordHistoricalLocalStage(ctx, dailySimulationJourneyStageTesteeProfile); err != nil {
		return toolchain.Decision{}, err
	}
	return state.nextDecision(dailySimulationJourneyStageTesteeProfile), nil
}

func dailySimulationStageEnrollPlan(ctx context.Context, state *dailySimulationJourneyState) (toolchain.Decision, error) {
	if strings.TrimSpace(state.planID) == "" {
		return toolchain.Decision{}, fmt.Errorf("dailySimulation.planIds resolved empty plan")
	}
	if state.testee == nil || strings.TrimSpace(state.testee.ID) == "" {
		return toolchain.Decision{}, fmt.Errorf("testee is not initialized before plan enrollment")
	}
	var enrollment *EnrollmentResponse
	var historicalPlan *PlanResponse
	if historical, ok := historicalseed.FromContext(ctx); ok {
		record, completed, err := completedHistoricalServerStage(ctx, historical.ScenarioID, string(dailySimulationJourneyStagePlanEnrollment))
		if err != nil {
			return toolchain.Decision{}, err
		}
		if completed {
			enrollment, err = restoreHistoricalEnrollment(ctx, state, record)
			if err != nil {
				return toolchain.Decision{}, err
			}
		}
		historicalPlan, err = state.deps.APIClient.GetPlan(ctx, state.planID)
		if err != nil {
			return toolchain.Decision{}, fmt.Errorf("load historical plan %s: %w", state.planID, err)
		}
		if historicalPlan == nil {
			return toolchain.Decision{}, fmt.Errorf("load historical plan %s: empty response", state.planID)
		}
		if err := validateHistoricalPlanScale(state.planID, historicalPlan.ScaleCode, state.target.TargetCode, "scenario target"); err != nil {
			return toolchain.Decision{}, err
		}
	}
	if enrollment == nil {
		var err error
		enrollment, err = state.deps.APIClient.EnrollTesteeInPlan(ctx, EnrollTesteeRequest{
			PlanID:    state.planID,
			TesteeID:  state.testee.ID,
			StartDate: state.profile.RunDate.Format("2006-01-02"),
		})
		if err != nil {
			return toolchain.Decision{}, err
		}
	}
	state.outcome.PlanEnrolled = true
	if enrollment != nil {
		state.outcome.EnrollmentID = strings.TrimSpace(enrollment.EnrollmentID)
		for _, task := range enrollment.Tasks {
			if taskID := strings.TrimSpace(task.ID); taskID != "" {
				state.outcome.TaskIDs = append(state.outcome.TaskIDs, taskID)
			}
		}
	}
	if historical, ok := historicalseed.FromContext(ctx); ok && enrollment != nil {
		for _, task := range enrollment.Tasks {
			taskID := strings.TrimSpace(task.ID)
			if taskID == "" {
				continue
			}
			plannedAt, parseErr := parseHistoricalTaskPlannedAt(task.PlannedAt, state.profile.RunDate.Location())
			if parseErr != nil {
				return toolchain.Decision{}, fmt.Errorf("parse historical plan task %s planned_at: %w", taskID, parseErr)
			}
			if cutoff, limited := historicalCutoffFromContext(ctx); limited {
				if !plannedAt.Before(cutoff) {
					continue
				}
			}
			if deterministicHistoricalInt(historical.BatchID, state.profile.RunDate, state.profile.Index, "task-complete:"+taskID, 100) >= 60 {
				continue
			}
			businessDay := plannedAt.In(state.profile.RunDate.Location())
			timeline, timelineErr := BuildHistoricalTaskTimeline(historical.BatchID, businessDay, state.profile.Index)
			if timelineErr != nil {
				return toolchain.Decision{}, fmt.Errorf("build historical plan task %s timeline: %w", taskID, timelineErr)
			}
			child := historicalseed.Context{
				BatchID: historical.BatchID, OrgID: historical.OrgID, Version: historicalseed.Version1,
				ScenarioID: fmt.Sprintf("%s/%d/%s/%s", businessDay.Format("2006-01-02"), state.profile.Index, dailySimulationJourneySubmitAnswer, taskID),
				Timeline:   timeline,
			}
			recovery := HistoricalPlanTaskRecovery{
				ScenarioID: child.ScenarioID,
				TaskID:     taskID,
				PlannedAt:  plannedAt.Format(time.RFC3339Nano),
				TargetKey:  strings.Join([]string{state.target.TargetType, state.target.TargetCode}, "/"),
			}
			if err := recordHistoricalPlanTaskDiscovery(ctx, historical, recovery); err != nil {
				return toolchain.Decision{}, fmt.Errorf("persist historical plan task %s discovery: %w", taskID, err)
			}
			state.selectedTasks = append(state.selectedTasks, historicalSelectedTask{ID: taskID, Context: child, PlannedAt: plannedAt})
			state.outcome.ChildScenarioIDs = append(state.outcome.ChildScenarioIDs, child.ScenarioID)
		}
	}
	if err := state.recordHistoricalLocalStage(ctx, dailySimulationJourneyStagePlanEnrollment); err != nil {
		return toolchain.Decision{}, err
	}
	return toolchain.Next(), nil
}

func restoreHistoricalEnrollment(ctx context.Context, state *dailySimulationJourneyState, record HistoricalStageRecord) (*EnrollmentResponse, error) {
	var payload struct {
		EnrollmentID string   `json:"enrollment_id"`
		PlanID       string   `json:"plan_id"`
		TaskIDs      []string `json:"task_ids"`
	}
	if len(record.PayloadJSON) == 0 {
		return nil, fmt.Errorf("historical plan_enrollment stage %d has no payload", record.ID)
	}
	if err := json.Unmarshal(record.PayloadJSON, &payload); err != nil {
		return nil, fmt.Errorf("decode historical plan_enrollment payload: %w", err)
	}
	payload.EnrollmentID = strings.TrimSpace(payload.EnrollmentID)
	payload.PlanID = strings.TrimSpace(payload.PlanID)
	if payload.EnrollmentID == "" || payload.EnrollmentID != strings.TrimSpace(record.ResourceID) {
		return nil, fmt.Errorf("historical plan_enrollment resource %s conflicts with payload enrollment %s", record.ResourceID, payload.EnrollmentID)
	}
	if payload.PlanID != strings.TrimSpace(state.planID) {
		return nil, fmt.Errorf("historical plan_enrollment plan %s conflicts with scenario plan %s", payload.PlanID, state.planID)
	}
	wanted := make(map[string]struct{}, len(payload.TaskIDs))
	for _, rawID := range payload.TaskIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, fmt.Errorf("historical plan_enrollment %s contains empty task id", payload.EnrollmentID)
		}
		if _, exists := wanted[id]; exists {
			return nil, fmt.Errorf("historical plan_enrollment %s contains duplicate task %s", payload.EnrollmentID, id)
		}
		wanted[id] = struct{}{}
	}
	tasks, err := loadHistoricalEnrollmentTasks(ctx, state, wanted)
	if err != nil {
		return nil, err
	}
	return &EnrollmentResponse{PlanID: payload.PlanID, EnrollmentID: payload.EnrollmentID, Idempotent: true, Tasks: tasks}, nil
}

func loadHistoricalEnrollmentTasks(ctx context.Context, state *dailySimulationJourneyState, wanted map[string]struct{}) ([]TaskResponse, error) {
	if len(wanted) == 0 {
		return nil, nil
	}
	location := state.profile.RunDate.Location()
	before := state.profile.RunDate.AddDate(5, 0, 0)
	if cutoff, ok := historicalCutoffFromContext(ctx); ok {
		before = cutoff.Add(-time.Second)
	}
	tasks := make([]TaskResponse, 0, len(wanted))
	seen := make(map[string]struct{}, len(wanted))
	for page := 1; ; page++ {
		response, err := state.deps.APIClient.ListPlanTaskWindow(ctx, ListPlanTaskWindowRequest{
			PlanID:        state.planID,
			TesteeIDs:     []string{state.testee.ID},
			PlannedAfter:  state.profile.RunDate.In(location).Format("2006-01-02 00:00:00"),
			PlannedBefore: before.In(location).Format("2006-01-02 15:04:05"),
			Page:          page,
			PageSize:      100,
		})
		if err != nil {
			return nil, fmt.Errorf("load historical enrollment tasks: %w", err)
		}
		if response == nil {
			return nil, fmt.Errorf("load historical enrollment tasks returned nil page %d", page)
		}
		for _, task := range response.Tasks {
			id := strings.TrimSpace(task.ID)
			if _, ok := wanted[id]; !ok {
				continue
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, fmt.Errorf("historical enrollment task %s appears more than once", id)
			}
			seen[id] = struct{}{}
			tasks = append(tasks, task)
		}
		if !response.HasMore {
			break
		}
		if page >= 10000 {
			return nil, fmt.Errorf("historical enrollment task pagination exceeded safety limit")
		}
	}
	if len(seen) != len(wanted) {
		missing := make([]string, 0, len(wanted)-len(seen))
		for id := range wanted {
			if _, ok := seen[id]; !ok {
				missing = append(missing, id)
			}
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("historical enrollment tasks missing from server read model: %s", strings.Join(missing, ","))
	}
	return tasks, nil
}

func parseHistoricalTaskPlannedAt(raw string, location *time.Location) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("planned_at is empty")
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		var (
			parsed time.Time
			err    error
		)
		if layout == "2006-01-02 15:04:05" || layout == "2006-01-02" {
			parsed, err = time.ParseInLocation(layout, raw, location)
		} else {
			parsed, err = time.Parse(layout, raw)
		}
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time %q", raw)
}

func dailySimulationStageResolveEntry(ctx context.Context, state *dailySimulationJourneyState) (toolchain.Decision, error) {
	if state.entry == nil || strings.TrimSpace(state.entry.ID) == "" || strings.TrimSpace(state.entry.Token) == "" {
		return toolchain.Decision{}, fmt.Errorf("assessment entry is not initialized")
	}
	if historical, ok := historicalseed.FromContext(ctx); ok {
		if record, completed, err := completedHistoricalServerStage(ctx, historical.ScenarioID, string(dailySimulationJourneyStageEntryResolve)); err != nil {
			return toolchain.Decision{}, err
		} else if completed {
			if record.ResourceID != strings.TrimSpace(state.entry.ID) {
				return toolchain.Decision{}, fmt.Errorf("historical entry_resolve resource %s conflicts with entry %s", record.ResourceID, state.entry.ID)
			}
			state.outcome.EntryResolved = true
			if err := state.recordHistoricalLocalStage(ctx, dailySimulationJourneyStageEntryResolve); err != nil {
				return toolchain.Decision{}, err
			}
			return state.nextDecision(dailySimulationJourneyStageEntryResolve), nil
		}
	}
	// 每次访问都走公开 resolve，确保 entry_opened 行为事件落到对应 clinician。
	if _, err := state.deps.APIClient.ResolveAssessmentEntry(ctx, state.entry.Token); err != nil {
		return toolchain.Decision{}, err
	}
	state.outcome.EntryResolved = true
	if err := state.recordHistoricalLocalStage(ctx, dailySimulationJourneyStageEntryResolve); err != nil {
		return toolchain.Decision{}, err
	}
	return state.nextDecision(dailySimulationJourneyStageEntryResolve), nil
}

func dailySimulationStageIntakeEntry(ctx context.Context, state *dailySimulationJourneyState) (toolchain.Decision, error) {
	if state.entry == nil || strings.TrimSpace(state.entry.ID) == "" || strings.TrimSpace(state.entry.Token) == "" {
		return toolchain.Decision{}, fmt.Errorf("assessment entry is not initialized")
	}
	if historical, ok := historicalseed.FromContext(ctx); ok {
		if record, completed, err := completedHistoricalServerStage(ctx, historical.ScenarioID, string(dailySimulationJourneyStageEntryIntake)); err != nil {
			return toolchain.Decision{}, err
		} else if completed {
			if state.testee == nil || record.ResourceID != strings.TrimSpace(state.testee.ID) {
				return toolchain.Decision{}, fmt.Errorf("historical entry_intake resource %s conflicts with restored testee", record.ResourceID)
			}
			state.outcome.EntryIntaked = true
			if err := state.recordHistoricalLocalStage(ctx, dailySimulationJourneyStageEntryIntake); err != nil {
				return toolchain.Decision{}, err
			}
			return state.nextDecision(dailySimulationJourneyStageEntryIntake), nil
		}
	}
	hasEntryRelation := false
	if state.testee != nil && strings.TrimSpace(state.testee.ID) != "" {
		var err error
		hasEntryRelation, err = hasAssessmentEntryRelation(ctx, state.deps.APIClient, state.testee.ID, state.entry.ID)
		if err != nil {
			return toolchain.Decision{}, err
		}
	}
	if hasEntryRelation {
		state.outcome.EntryIntaked = true
		if err := state.recordHistoricalLocalStage(ctx, dailySimulationJourneyStageEntryIntake); err != nil {
			return toolchain.Decision{}, err
		}
		return state.nextDecision(dailySimulationJourneyStageEntryIntake), nil
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
	if err := state.recordHistoricalLocalStage(ctx, dailySimulationJourneyStageEntryIntake); err != nil {
		return toolchain.Decision{}, err
	}
	return state.nextDecision(dailySimulationJourneyStageEntryIntake), nil
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

	type submissionSpec struct {
		ctx       context.Context
		taskID    string
		plannedAt time.Time
	}
	specs := make([]submissionSpec, 0, max(1, len(state.selectedTasks)))
	if len(state.selectedTasks) == 0 {
		specs = append(specs, submissionSpec{ctx: ctx})
	} else {
		for _, task := range state.selectedTasks {
			specs = append(specs, submissionSpec{
				ctx: historicalseed.WithContext(ctx, task.Context), taskID: task.ID, plannedAt: task.PlannedAt,
			})
		}
	}
	executor := historicalSubmissionExecutorFromContext(ctx)
	if executor == nil {
		for _, spec := range specs {
			if err := runDailySimulationSubmissionJob(spec.ctx, state, spec.taskID, questionnaireDetail, testeeID); err != nil {
				return toolchain.Decision{}, fmt.Errorf("submit historical plan task %s: %w", spec.taskID, err)
			}
		}
		return state.nextDecision(dailySimulationJourneyStageAnswerSheet), nil
	}
	futures := make([]historicalSubmissionFuture, 0, len(specs))
	for _, spec := range specs {
		spec := spec
		jobState := cloneDailySimulationJourneyStateForSubmission(state)
		historical, ok := historicalseed.FromContext(spec.ctx)
		if !ok {
			return toolchain.Decision{}, fmt.Errorf("historical context is required for concurrent submission")
		}
		future, err := executor.Submit(HistoricalSubmissionJob{
			ScenarioID: historical.ScenarioID,
			TaskID:     spec.taskID,
			TargetKey:  strings.Join([]string{state.target.TargetType, state.target.TargetCode}, "/"),
			PlannedAt:  spec.plannedAt,
		}, func(jobCtx context.Context) (historicalSubmissionJobResult, error) {
			jobCtx = mergeHistoricalSubmissionContext(jobCtx, spec.ctx)
			if err := runDailySimulationSubmissionJob(jobCtx, jobState, spec.taskID, questionnaireDetail, testeeID); err != nil {
				return historicalSubmissionJobResult{Outcome: jobState.outcome}, err
			}
			return historicalSubmissionJobResult{Outcome: jobState.outcome, ReportGenerated: jobState.target.RequiresAssessment}, nil
		})
		if err != nil {
			return toolchain.Decision{}, err
		}
		futures = append(futures, future)
	}
	for index, future := range futures {
		result, err := future.Wait()
		if err != nil {
			return toolchain.Decision{}, fmt.Errorf("submit historical plan task %s: %w", specs[index].taskID, err)
		}
		mergeDailySimulationSubmissionOutcome(&state.outcome, result.Outcome)
	}
	return state.nextDecision(dailySimulationJourneyStageAnswerSheet), nil
}

func runDailySimulationSubmissionJob(
	submitCtx context.Context,
	state *dailySimulationJourneyState,
	taskID string,
	questionnaireDetail *QuestionnaireDetailResponse,
	testeeID uint64,
) error {
	if taskID != "" {
		if err := ensureHistoricalPlanTaskOpen(submitCtx, state, taskID); err != nil {
			return err
		}
	}
	submission, restored, err := restoreDailySimulationHistoricalSubmission(submitCtx, state, taskID)
	if err != nil {
		return err
	}
	if !restored {
		rng := newDailySimulationRand(
			"answers:" + state.profile.RunDate.Format("20060102") + ":" + strconv.Itoa(state.profile.Index) + ":" + state.target.QuestionnaireCode + ":" + taskID,
		)
		answers := buildAnswers(questionnaireDetail, rng)
		if invalidAnswers := validateAnswers(questionnaireDetail, answers); len(invalidAnswers) > 0 {
			return fmt.Errorf("generated invalid answers for questionnaire %s: %v", state.target.QuestionnaireCode, invalidAnswers)
		}
		req := SubmitAnswerSheetRequest{
			QuestionnaireCode: state.target.QuestionnaireCode, QuestionnaireVersion: state.target.QuestionnaireVersion,
			Title: state.target.QuestionnaireTitle, TesteeID: testeeID,
			OriginRef: &OriginRef{Type: "assessment_entry", ID: strings.TrimSpace(state.entry.ID)}, Answers: answers,
		}
		if taskID != "" {
			req.TaskID = taskID
			req.OriginRef = &OriginRef{Type: "plan_task", ID: taskID}
		}
		submission, err = submitDailySimulationAnswerSheet(submitCtx, state, req)
		if err != nil {
			return err
		}
	}
	state.outcome.AnswerSheetID = submission.AnswerSheetID
	state.outcome.AssessmentID = submission.AssessmentID
	state.outcome.AnswerSheetIDs = appendUniqueString(state.outcome.AnswerSheetIDs, state.outcome.AnswerSheetID)
	if state.outcome.AssessmentID != "" {
		state.outcome.AssessmentIDs = appendUniqueString(state.outcome.AssessmentIDs, state.outcome.AssessmentID)
	}
	if err := state.recordHistoricalLocalStage(submitCtx, dailySimulationJourneyStageAnswerSheet); err != nil {
		return err
	}
	if state.target.RequiresAssessment {
		for _, stage := range []dailySimulationJourneyStage{"assessment_created", "outcome_committed", "report_generated"} {
			if _, historical := historicalseed.FromContext(submitCtx); historical {
				if _, verified := submission.ServerStages[string(stage)]; !verified {
					return fmt.Errorf("historical server stage %s was not verified", stage)
				}
			}
			if err := state.recordHistoricalLocalStage(submitCtx, stage); err != nil {
				return err
			}
		}
	}
	if taskID != "" {
		if _, historical := historicalseed.FromContext(submitCtx); historical {
			if _, verified := submission.ServerStages["task_complete"]; !verified {
				return fmt.Errorf("historical server stage task_complete was not verified")
			}
		}
		if err := state.recordHistoricalLocalStage(submitCtx, dailySimulationJourneyStage("task_complete")); err != nil {
			return err
		}
		state.outcome.CompletedTaskIDs = appendUniqueString(state.outcome.CompletedTaskIDs, taskID)
	}
	return nil
}

func ensureHistoricalPlanTaskOpen(ctx context.Context, state *dailySimulationJourneyState, taskID string) error {
	historical, ok := historicalseed.FromContext(ctx)
	if !ok {
		return nil
	}
	if record, completed, err := completedHistoricalServerStage(ctx, historical.ScenarioID, "task_open"); err != nil {
		return err
	} else if completed {
		if record.ResourceID != taskID {
			return fmt.Errorf("historical task_open resource %s conflicts with task %s", record.ResourceID, taskID)
		}
	} else if _, err := state.deps.APIClient.OpenPlanTask(ctx, taskID); err != nil {
		return fmt.Errorf("open historical plan task %s: %w", taskID, err)
	}
	return state.recordHistoricalLocalStage(ctx, dailySimulationJourneyStage("task_open"))
}

func cloneDailySimulationJourneyStateForSubmission(state *dailySimulationJourneyState) *dailySimulationJourneyState {
	clone := *state
	clone.selectedTasks = nil
	clone.outcome.TaskIDs = append([]string(nil), state.outcome.TaskIDs...)
	clone.outcome.CompletedTaskIDs = nil
	clone.outcome.ChildScenarioIDs = append([]string(nil), state.outcome.ChildScenarioIDs...)
	clone.outcome.AdditionalScenarios = append([]HistoricalAdditionalScenarioManifest(nil), state.outcome.AdditionalScenarios...)
	clone.outcome.AnswerSheetIDs = nil
	clone.outcome.AssessmentIDs = nil
	clone.outcome.AnswerSheetID = ""
	clone.outcome.AssessmentID = ""
	clone.outcome.SkippedSubmission = false
	return &clone
}

func mergeDailySimulationSubmissionOutcome(target *dailySimulationOutcome, result dailySimulationOutcome) {
	if target == nil {
		return
	}
	if strings.TrimSpace(result.AnswerSheetID) != "" {
		target.AnswerSheetID = result.AnswerSheetID
	}
	if strings.TrimSpace(result.AssessmentID) != "" {
		target.AssessmentID = result.AssessmentID
	}
	target.AnswerSheetIDs = appendUniqueStrings(target.AnswerSheetIDs, result.AnswerSheetIDs)
	target.AssessmentIDs = appendUniqueStrings(target.AssessmentIDs, result.AssessmentIDs)
	target.CompletedTaskIDs = appendUniqueStrings(target.CompletedTaskIDs, result.CompletedTaskIDs)
	target.SkippedSubmission = target.SkippedSubmission || result.SkippedSubmission
}

func mergeHistoricalSubmissionContext(workerCtx, sourceCtx context.Context) context.Context {
	if sourceCtx == nil {
		return workerCtx
	}
	return historicalSubmissionJobContext{Context: workerCtx, values: sourceCtx}
}

type historicalSubmissionJobContext struct {
	context.Context
	values context.Context
}

func (ctx historicalSubmissionJobContext) Value(key any) any {
	return ctx.values.Value(key)
}

func restoreDailySimulationHistoricalSubmission(
	ctx context.Context,
	state *dailySimulationJourneyState,
	taskID string,
) (dailySimulationSubmissionResult, bool, error) {
	historical, ok := historicalseed.FromContext(ctx)
	if !ok {
		return dailySimulationSubmissionResult{}, false, nil
	}
	snapshot, ok := historicalScenarioSnapshot(ctx, historical.ScenarioID)
	if !ok {
		return dailySimulationSubmissionResult{}, false, nil
	}
	if _, completed := snapshot.Server[string(dailySimulationJourneyStageAnswerSheet)]; !completed {
		return dailySimulationSubmissionResult{}, false, nil
	}
	result, err := verifyHistoricalSubmissionStageMap(historical, taskID, state.target.RequiresAssessment, dailySimulationSubmissionResult{}, snapshot.Server)
	if err != nil {
		if errors.Is(err, errHistoricalSubmissionPending) {
			return dailySimulationSubmissionResult{}, false, nil
		}
		return result, false, err
	}
	return result, true, nil
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

type historicalLocalStageRecorder func(historicalseed.Context, dailySimulationJourneyStage, dailySimulationOutcome, *dailySimulationResolvedTarget) error

type historicalLocalStageRecorderKey struct{}

func withHistoricalLocalStageRecorder(ctx context.Context, recorder historicalLocalStageRecorder) context.Context {
	return context.WithValue(ctx, historicalLocalStageRecorderKey{}, recorder)
}

func (state *dailySimulationJourneyState) recordHistoricalLocalStage(ctx context.Context, stage dailySimulationJourneyStage) error {
	if state == nil {
		return nil
	}
	historical, ok := historicalseed.FromContext(ctx)
	if !ok {
		return nil
	}
	recorder, _ := ctx.Value(historicalLocalStageRecorderKey{}).(historicalLocalStageRecorder)
	if recorder == nil {
		return nil
	}
	return recorder(historical, stage, state.outcome, state.target)
}

func shouldStopDailySimulationJourneyAfter(target dailySimulationJourneyTarget, stage dailySimulationJourneyStage) bool {
	switch target {
	case dailySimulationJourneyRegisterOnly:
		return stage == dailySimulationJourneyStageGuardianAccount
	case dailySimulationJourneyCreateTestee:
		return stage == dailySimulationJourneyStageTesteeProfile
	case dailySimulationJourneyResolveEntry:
		return stage == dailySimulationJourneyStageEntryResolve
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

func resolveDailySimulationScenarioIdentity(
	cfg DailySimulationConfig,
	runDate time.Time,
	index int,
	scenario dailySimulationScenario,
) (dailySimulationJourneyTarget, string) {
	journey := scenario.JourneyTarget
	if journey == "" {
		journey = resolveDailySimulationJourneyTarget(cfg, runDate, index)
	}
	planID := strings.TrimSpace(scenario.PlanID)
	if planID == "" {
		planID = selectDailySimulationPlanID(cfg, runDate, index)
	}
	return journey, planID
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
	var frozenTarget *dailySimulationResolvedTarget
	if _, historical := historicalseed.FromContext(ctx); historical && targetVersion == "" {
		resolved, resolveErr := resolveDailySimulationTarget(ctx, deps.APIClient, deps.CollectionClient, targetType, targetCode, "")
		if resolveErr != nil {
			return nil, nil, "", resolveErr
		}
		frozenTarget = resolved
		targetVersion = resolved.TargetVersion
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
	if frozenTarget != nil && strings.TrimSpace(entry.TargetVersion) == targetVersion {
		return entry, frozenTarget, clinicianID, nil
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
		resolvedTargetVersion := strings.TrimSpace(scaleItem.Version)
		if resolvedTargetVersion == "" {
			return nil, fmt.Errorf("published assessment model %s has empty version", targetCode)
		}
		if targetVersion != "" && targetVersion != resolvedTargetVersion {
			return nil, fmt.Errorf("published assessment model %s version drift: requested=%s loaded=%s", targetCode, targetVersion, resolvedTargetVersion)
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
			TargetVersion:        resolvedTargetVersion,
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
		version := strings.TrimSpace(detail.Version)
		if version == "" {
			return nil, fmt.Errorf("published questionnaire %s has empty version", targetCode)
		}
		if targetVersion != "" && targetVersion != version {
			return nil, fmt.Errorf("published questionnaire %s version drift: requested=%s loaded=%s", targetCode, targetVersion, version)
		}
		return &dailySimulationResolvedTarget{
			TargetType:           targetType,
			TargetCode:           targetCode,
			TargetVersion:        version,
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

func restoreDailySimulationGuardianSession(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
	profile dailySimulationProfile,
) (string, error) {
	if deps == nil || deps.Config == nil {
		return "", fmt.Errorf("daily simulation dependencies are required")
	}
	loginURL, err := resolveDailySimulationIAMLoginURL(deps.Config.IAM)
	if err != nil {
		return "", err
	}
	tenantID := resolveDailySimulationTenantID(deps.Config.IAM, deps.Config.Global.OrgID)
	if dailySimulationUsesIAMMockConsumer(deps.Config.IAM) {
		tenantID = ""
	}
	deviceID := fmt.Sprintf("%s-%s-%03d", dailySimulationDeviceIDPrefix, profile.RunDate.Format("20060102"), profile.Index+1)
	token, err := tryDailySimulationGuardianLoginWithRetry(
		ctx, loginURL, tenantID, deviceID, profile.GuardianEmail, profile.GuardianPhone,
		normalizeDailySimulationPassword(cfg.UserPassword), deps.Logger,
	)
	if err != nil {
		return "", fmt.Errorf("restore historical guardian session %s: %w", profile.GuardianEmail, err)
	}
	return token, nil
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

	ensureRequest := EnsureIAMMockConsumerRequest{
		Name:     profile.GuardianName,
		Phone:    profile.GuardianPhone,
		Email:    profile.GuardianEmail,
		Password: password,
	}
	ensureRequest = withHistoricalIAMMetadata(ctx, ensureRequest, profile.GuardianName)
	ensureResp, err := client.EnsureIAMMockConsumer(ctx, endpointPath, ensureRequest, sharedSecret)
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

func withHistoricalIAMMetadata(ctx context.Context, request EnsureIAMMockConsumerRequest, nickname string) EnsureIAMMockConsumerRequest {
	historical, ok := historicalseed.FromContext(ctx)
	if !ok {
		return request
	}
	request.Profile = map[string]string{"nickname": strings.TrimSpace(nickname)}
	request.Meta = map[string]string{
		"source":           "seeddata_historical",
		"seed_batch_id":    historical.BatchID,
		"seed_scenario_id": historical.ScenarioID,
	}
	return request
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
