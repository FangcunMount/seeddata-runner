package dailysim

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"context"

	"github.com/FangcunMount/seeddata-runner/internal/historicalseed"
)

const historicalTimezone = "Asia/Shanghai"

type historicalCutoffKey struct{}

func withHistoricalCutoff(ctx context.Context, cutoff time.Time) context.Context {
	return context.WithValue(ctx, historicalCutoffKey{}, cutoff)
}

func historicalCutoffFromContext(ctx context.Context) (time.Time, bool) {
	cutoff, ok := ctx.Value(historicalCutoffKey{}).(time.Time)
	return cutoff, ok && !cutoff.IsZero()
}

type HistoricalBackfillOptions struct {
	From     string
	To       string
	BatchID  string
	Resume   bool
	StateDir string
	CountMin int
	CountMax int
	Workers  int
}

type HistoricalTargetManifest struct {
	TargetType           string `json:"target_type"`
	TargetCode           string `json:"target_code"`
	TargetVersion        string `json:"target_version"`
	QuestionnaireCode    string `json:"questionnaire_code"`
	QuestionnaireVersion string `json:"questionnaire_version"`
	RequiresAssessment   bool   `json:"requires_assessment"`
}

type HistoricalPlanManifest struct {
	ID            string   `json:"id"`
	ScaleCode     string   `json:"scale_code"`
	ScheduleType  string   `json:"schedule_type"`
	TriggerTime   string   `json:"trigger_time"`
	Interval      int      `json:"interval"`
	TotalTimes    int      `json:"total_times"`
	FixedDates    []string `json:"fixed_dates,omitempty"`
	RelativeWeeks []int    `json:"relative_weeks,omitempty"`
	Status        string   `json:"status"`
}

type HistoricalAdditionalScenarioManifest struct {
	ScenarioID string `json:"scenario_id"`
	TargetKey  string `json:"target_key"`
}

type HistoricalLocalStageRecord struct {
	ScenarioID       string    `json:"scenario_id"`
	Stage            string    `json:"stage"`
	PayloadHash      string    `json:"payload_hash"`
	Status           string    `json:"status"`
	GuardianUserID   string    `json:"guardian_user_id,omitempty"`
	IAMProfileID     string    `json:"iam_profile_id,omitempty"`
	IAMProfileLinkID string    `json:"iam_profile_link_id,omitempty"`
	TesteeID         string    `json:"testee_id,omitempty"`
	UserCreated      bool      `json:"user_created"`
	TesteeCreated    bool      `json:"testee_created"`
	EnrollmentID     string    `json:"enrollment_id,omitempty"`
	TaskIDs          []string  `json:"task_ids,omitempty"`
	AnswerSheetIDs   []string  `json:"answersheet_ids,omitempty"`
	AssessmentIDs    []string  `json:"assessment_ids,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type HistoricalScenarioManifest struct {
	ScenarioID          string                                 `json:"scenario_id"`
	BusinessDate        string                                 `json:"business_date"`
	Journey             string                                 `json:"journey"`
	TargetKey           string                                 `json:"target_key"`
	GuardianUserID      string                                 `json:"guardian_user_id,omitempty"`
	IAMProfileID        string                                 `json:"iam_profile_id,omitempty"`
	IAMProfileLinkID    string                                 `json:"iam_profile_link_id,omitempty"`
	TesteeID            string                                 `json:"testee_id,omitempty"`
	TesteeCreated       bool                                   `json:"testee_created"`
	EntryID             string                                 `json:"entry_id,omitempty"`
	PlanID              string                                 `json:"plan_id,omitempty"`
	EnrollmentID        string                                 `json:"enrollment_id,omitempty"`
	TaskIDs             []string                               `json:"task_ids,omitempty"`
	CompletedTaskIDs    []string                               `json:"completed_task_ids,omitempty"`
	ChildScenarioIDs    []string                               `json:"child_scenario_ids,omitempty"`
	AdditionalScenarios []HistoricalAdditionalScenarioManifest `json:"additional_scenarios,omitempty"`
	AnswerSheetID       string                                 `json:"answersheet_id,omitempty"`
	AnswerSheetIDs      []string                               `json:"answersheet_ids,omitempty"`
	AssessmentID        string                                 `json:"assessment_id,omitempty"`
	AssessmentIDs       []string                               `json:"assessment_ids,omitempty"`
	OutcomeID           string                                 `json:"outcome_id,omitempty"`
	OutcomeIDs          []string                               `json:"outcome_ids,omitempty"`
	ReportID            string                                 `json:"report_id,omitempty"`
	ReportIDs           []string                               `json:"report_ids,omitempty"`
	GenerationID        string                                 `json:"report_generation_id,omitempty"`
	ReportRunID         string                                 `json:"report_run_id,omitempty"`
	Terminal            string                                 `json:"terminal"`
}

type HistoricalManifest struct {
	Version     int                                   `json:"version"`
	BatchID     string                                `json:"batch_id"`
	OrgID       int64                                 `json:"org_id"`
	From        string                                `json:"from"`
	To          string                                `json:"to"`
	Timezone    string                                `json:"timezone"`
	CreatedAt   time.Time                             `json:"created_at"`
	UpdatedAt   time.Time                             `json:"updated_at"`
	Targets     map[string]HistoricalTargetManifest   `json:"targets"`
	Plans       map[string]HistoricalPlanManifest     `json:"plans"`
	Scenarios   map[string]HistoricalScenarioManifest `json:"scenarios"`
	DailyCounts map[string]int                        `json:"daily_counts"`
}

type HistoricalCheckpoint struct {
	Version          int       `json:"version"`
	BatchID          string    `json:"batch_id"`
	From             string    `json:"from"`
	To               string    `json:"to"`
	CompletedThrough string    `json:"completed_through,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type HistoricalVerification struct {
	BatchID           string   `json:"batch_id"`
	Complete          bool     `json:"complete"`
	CompletedThrough  string   `json:"completed_through,omitempty"`
	ExpectedDays      int      `json:"expected_days"`
	RecordedDays      int      `json:"recorded_days"`
	ExpectedScenarios int      `json:"expected_scenarios"`
	RecordedScenarios int      `json:"recorded_scenarios"`
	ServerStageCount  int      `json:"server_stage_count"`
	MissingStages     []string `json:"missing_stages,omitempty"`
}

func RunHistoricalBackfill(ctx context.Context, deps *Dependencies, opts HistoricalBackfillOptions) error {
	if deps == nil || deps.Config == nil {
		return fmt.Errorf("historical backfill dependencies are required")
	}
	location, err := time.LoadLocation(historicalTimezone)
	if err != nil {
		return err
	}
	from, to, err := parseHistoricalDateRange(opts.From, opts.To, location)
	if err != nil {
		return err
	}
	opts.From = from.Format("2006-01-02")
	opts.To = to.Format("2006-01-02")
	opts.BatchID = strings.TrimSpace(opts.BatchID)
	if opts.BatchID == "" {
		return fmt.Errorf("historical batch_id is required")
	}
	if deps.Config.Global.OrgID <= 0 {
		return fmt.Errorf("historical org_id must be positive")
	}
	if len(deps.APIClient.HistoricalSecret()) == 0 || len(deps.CollectionClient.HistoricalSecret()) == 0 {
		return fmt.Errorf("QS_HISTORICAL_CONTEXT_SECRET is required for historical backfill")
	}
	if opts.StateDir = strings.TrimSpace(opts.StateDir); opts.StateDir == "" {
		opts.StateDir = filepath.Join(".seeddata-cache", "historical")
	}
	if opts.CountMin <= 0 {
		opts.CountMin = 40
	}
	if opts.CountMax <= 0 {
		opts.CountMax = 200
	}
	if opts.CountMax < opts.CountMin {
		return fmt.Errorf("historical countMax must be >= countMin")
	}
	if opts.Workers <= 0 {
		opts.Workers = 5
	}

	checkpointPath, manifestPath := historicalPaths(opts.StateDir, opts.BatchID)
	checkpoint := HistoricalCheckpoint{}
	checkpointExists, err := loadSecureJSON(checkpointPath, &checkpoint)
	if err != nil {
		return err
	}
	manifest := HistoricalManifest{}
	manifestExists, err := loadSecureJSON(manifestPath, &manifest)
	if err != nil {
		return err
	}
	if checkpointExists && !opts.Resume {
		return fmt.Errorf("historical checkpoint already exists for batch %s; use --resume", opts.BatchID)
	}
	if checkpointExists && (checkpoint.BatchID != opts.BatchID || checkpoint.From != opts.From || checkpoint.To != opts.To) {
		return fmt.Errorf("historical checkpoint identity conflicts with requested batch/range")
	}
	if manifestExists && (manifest.BatchID != opts.BatchID || manifest.From != opts.From || manifest.To != opts.To || manifest.OrgID != deps.Config.Global.OrgID) {
		return fmt.Errorf("historical manifest identity conflicts with requested batch/range/org")
	}
	if !manifestExists {
		now := time.Now().UTC()
		manifest = HistoricalManifest{
			Version: 1, BatchID: opts.BatchID, OrgID: deps.Config.Global.OrgID, From: opts.From, To: opts.To,
			Timezone: historicalTimezone, CreatedAt: now, UpdatedAt: now,
			Targets: make(map[string]HistoricalTargetManifest), Plans: make(map[string]HistoricalPlanManifest), Scenarios: make(map[string]HistoricalScenarioManifest), DailyCounts: make(map[string]int),
		}
	}
	if manifest.Targets == nil {
		manifest.Targets = make(map[string]HistoricalTargetManifest)
	}
	if manifest.Scenarios == nil {
		manifest.Scenarios = make(map[string]HistoricalScenarioManifest)
	}
	if manifest.Plans == nil {
		manifest.Plans = make(map[string]HistoricalPlanManifest)
	}
	if manifest.DailyCounts == nil {
		manifest.DailyCounts = make(map[string]int)
	}
	if !checkpointExists {
		checkpoint = HistoricalCheckpoint{Version: 1, BatchID: opts.BatchID, From: opts.From, To: opts.To}
	}

	start := from
	if checkpoint.CompletedThrough != "" {
		completed, err := time.ParseInLocation("2006-01-02", checkpoint.CompletedThrough, location)
		if err != nil {
			return fmt.Errorf("parse checkpoint completed_through: %w", err)
		}
		start = completed.AddDate(0, 0, 1)
	}
	var manifestMu sync.Mutex
	ctx = withHistoricalCutoff(ctx, to.AddDate(0, 0, 1))
	for day := start; !day.After(to); day = day.AddDate(0, 0, 1) {
		dayKey := day.Format("2006-01-02")
		if err := freezeHistoricalPlans(ctx, deps.APIClient, deps.Config.DailySimulation.PlanIDs, &manifest); err != nil {
			return fmt.Errorf("historical plan drift on %s: %w", dayKey, err)
		}
		manifest.UpdatedAt = time.Now().UTC()
		if err := saveSecureJSON(manifestPath, &manifest); err != nil {
			return err
		}
		count := DeterministicHistoricalCount(opts.BatchID, day, opts.CountMin, opts.CountMax)
		cfg := deps.Config.DailySimulation
		cfg.Workers = opts.Workers
		err := runDailySimulationBatchWithOptions(ctx, deps, cfg, day, count, "historical_backfill_"+dayKey, dailySimulationBatchOptions{
			HistoricalBatchID: opts.BatchID,
			ShouldSkipScenario: func(profile dailySimulationProfile, scenario dailySimulationScenario) bool {
				historical := buildHistoricalScenarioContext(opts.BatchID, uint64(deps.Config.Global.OrgID), cfg, profile, scenario)
				manifestMu.Lock()
				defer manifestMu.Unlock()
				recorded, exists := manifest.Scenarios[historical.ScenarioID]
				return exists && strings.TrimSpace(recorded.Terminal) != ""
			},
			RestoreExistingTestee: func(profile dailySimulationProfile, scenario dailySimulationScenario) (*ApiserverTesteeResponse, error) {
				historical := buildHistoricalScenarioContext(opts.BatchID, uint64(deps.Config.Global.OrgID), cfg, profile, scenario)
				return restoreHistoricalExistingTestee(opts.StateDir, opts.BatchID, profile, historical.ScenarioID, cfg)
			},
			ValidateScenario: func(scenario dailySimulationScenario) error {
				manifestMu.Lock()
				defer manifestMu.Unlock()
				if err := freezeHistoricalTarget(&manifest, scenario); err != nil {
					return err
				}
				manifest.UpdatedAt = time.Now().UTC()
				return saveSecureJSON(manifestPath, &manifest)
			},
			OnScenarioComplete: func(profile dailySimulationProfile, scenario dailySimulationScenario, outcome dailySimulationOutcome) error {
				manifestMu.Lock()
				defer manifestMu.Unlock()
				localStages, loadErr := loadHistoricalLocalScenarioStages(opts.StateDir, opts.BatchID, profile.RunDate, buildHistoricalScenarioContext(opts.BatchID, uint64(deps.Config.Global.OrgID), cfg, profile, scenario).ScenarioID)
				if loadErr != nil {
					return loadErr
				}
				recordHistoricalScenario(&manifest, opts.BatchID, profile, scenario, outcome, localStages)
				return nil
			},
			OnHistoricalStageComplete: func(profile dailySimulationProfile, scenario dailySimulationScenario, historical historicalseed.Context, stage dailySimulationJourneyStage, outcome dailySimulationOutcome, target *dailySimulationResolvedTarget) error {
				manifestMu.Lock()
				defer manifestMu.Unlock()
				if target != nil {
					if err := freezeHistoricalTarget(&manifest, dailySimulationScenario{Target: target}); err != nil {
						return err
					}
				}
				if err := recordHistoricalLocalStage(opts.StateDir, &manifest, profile, scenario, historical, stage, outcome, target); err != nil {
					return err
				}
				return nil
			},
		})
		manifestMu.Lock()
		manifest.UpdatedAt = time.Now().UTC()
		if err == nil {
			manifest.DailyCounts[dayKey] = count
		}
		saveErr := saveSecureJSON(manifestPath, &manifest)
		manifestMu.Unlock()
		if saveErr != nil {
			return saveErr
		}
		if err != nil {
			return fmt.Errorf("historical backfill stopped on %s: %w", dayKey, err)
		}
		checkpoint.CompletedThrough = dayKey
		checkpoint.UpdatedAt = time.Now().UTC()
		if err := saveSecureJSON(checkpointPath, &checkpoint); err != nil {
			return err
		}
	}
	stages, err := loadHistoricalSeedStages(ctx, deps.APIClient, opts.BatchID)
	if err != nil {
		return fmt.Errorf("load terminal historical stage ledger: %w", err)
	}
	mergeHistoricalStageResources(&manifest, stages)
	manifest.UpdatedAt = time.Now().UTC()
	if err := saveSecureJSON(manifestPath, &manifest); err != nil {
		return err
	}
	return nil
}

func restoreHistoricalExistingTestee(stateDir, batchID string, profile dailySimulationProfile, scenarioID string, cfg DailySimulationConfig) (*ApiserverTesteeResponse, error) {
	records, err := loadHistoricalLocalScenarioStages(stateDir, batchID, profile.RunDate, scenarioID)
	if err != nil {
		return nil, err
	}
	record, ok := records[string(dailySimulationJourneyStageTesteeProfile)]
	if !ok {
		return nil, nil
	}
	if record.Status != "completed" || strings.TrimSpace(record.TesteeID) == "" || strings.TrimSpace(record.IAMProfileID) == "" {
		return nil, fmt.Errorf("historical testee_profile stage is incomplete for scenario %s", scenarioID)
	}
	profileID := strings.TrimSpace(record.IAMProfileID)
	birthday, err := dailySimulationBirthdayPtr(profile.ChildDOB)
	if err != nil {
		return nil, err
	}
	return &ApiserverTesteeResponse{
		ID: strings.TrimSpace(record.TesteeID), ProfileID: &profileID, Name: strings.TrimSpace(profile.ChildName),
		Gender: dailySimulationGenderString(profile.ChildGender), Birthday: birthday,
		Tags: append([]string(nil), cfg.TesteeTags...), Source: normalizeDailySimulationSource(cfg.TesteeSource),
	}, nil
}

func freezeHistoricalPlans(ctx context.Context, client *APIClient, configured []FlexibleID, manifest *HistoricalManifest) error {
	if client == nil || manifest == nil {
		return fmt.Errorf("historical plan freeze dependencies are required")
	}
	for _, planID := range collectDailySimulationPlanIDs(configured) {
		plan, err := client.GetPlan(ctx, planID)
		if err != nil {
			return fmt.Errorf("load plan %s: %w", planID, err)
		}
		frozen := HistoricalPlanManifest{
			ID: plan.ID, ScaleCode: plan.ScaleCode, ScheduleType: plan.ScheduleType, TriggerTime: plan.TriggerTime,
			Interval: plan.Interval, TotalTimes: plan.TotalTimes, FixedDates: append([]string(nil), plan.FixedDates...),
			RelativeWeeks: append([]int(nil), plan.RelativeWeeks...), Status: plan.Status,
		}
		if existing, ok := manifest.Plans[planID]; ok {
			if !reflect.DeepEqual(existing, frozen) {
				return fmt.Errorf("plan %s changed: frozen=%+v current=%+v", planID, existing, frozen)
			}
			continue
		}
		manifest.Plans[planID] = frozen
	}
	return nil
}

func buildHistoricalScenarioContext(batchID string, orgID uint64, cfg DailySimulationConfig, profile dailySimulationProfile, scenario dailySimulationScenario) historicalseed.Context {
	journey := resolveDailySimulationJourneyTarget(cfg, profile.RunDate, profile.Index)
	targetCode := "unknown"
	if scenario.Target != nil && strings.TrimSpace(scenario.Target.TargetCode) != "" {
		targetCode = strings.TrimSpace(scenario.Target.TargetCode)
	}
	scenarioID := fmt.Sprintf("%s/%d/%s/%s", profile.RunDate.Format("2006-01-02"), profile.Index, journey, targetCode)
	return historicalseed.Context{BatchID: batchID, ScenarioID: scenarioID, OrgID: orgID, Version: historicalseed.Version1, Timeline: BuildHistoricalTimeline(batchID, profile.RunDate, profile.Index)}
}

func BuildHistoricalTimeline(batchID string, day time.Time, index int) historicalseed.Timeline {
	location := day.Location()
	base := time.Date(day.Year(), day.Month(), day.Day(), 8, 0, 0, 0, location)
	base = base.Add(time.Duration(deterministicHistoricalInt(batchID, day, index, "prepare", 181)) * time.Minute)
	resolved := base.Add(5 * time.Minute)
	intake := resolved.Add(2 * time.Minute)
	enrolled := intake.Add(time.Minute)
	filled := enrolled.Add(time.Duration(10+deterministicHistoricalInt(batchID, day, index, "answer-delay", 31)) * time.Minute)
	assessmentCreated := filled.Add(time.Second)
	assessmentSubmitted := filled.Add(2 * time.Second)
	evaluated := filled.Add(5 * time.Second)
	reported := filled.Add(8 * time.Second)
	return historicalseed.Timeline{
		EntryResolvedAt: &resolved, EntryIntakeAt: &intake, EnrollmentJoinedAt: &enrolled,
		AnswerSheetFilledAt: &filled,
		AssessmentCreatedAt: &assessmentCreated, AssessmentSubmittedAt: &assessmentSubmitted,
		EvaluatedAt: &evaluated, ReportGeneratedAt: &reported,
	}
}

func BuildHistoricalTaskTimeline(batchID string, plannedAt time.Time, index int) (historicalseed.Timeline, error) {
	location := plannedAt.Location()
	dayEnd := time.Date(plannedAt.Year(), plannedAt.Month(), plannedAt.Day(), 23, 59, 59, 0, location)
	reported := plannedAt.Add(time.Duration(18+deterministicHistoricalInt(batchID, plannedAt, index, "task-answer-delay", 31)) * time.Minute)
	if reported.After(dayEnd) {
		reported = dayEnd
	}
	filled := reported.Add(-8 * time.Second)
	if filled.Before(plannedAt) {
		return historicalseed.Timeline{}, fmt.Errorf("task planned_at %s leaves no same-day report window", plannedAt.Format(time.RFC3339))
	}
	assessmentCreated := filled.Add(time.Second)
	assessmentSubmitted := filled.Add(2 * time.Second)
	completed := filled.Add(3 * time.Second)
	evaluated := filled.Add(5 * time.Second)
	return historicalseed.Timeline{
		TaskOpenedAt: &plannedAt, TaskCompletedAt: &completed, AnswerSheetFilledAt: &filled,
		AssessmentCreatedAt: &assessmentCreated, AssessmentSubmittedAt: &assessmentSubmitted,
		EvaluatedAt: &evaluated, ReportGeneratedAt: &reported,
	}, nil
}

func DeterministicHistoricalCount(batchID string, day time.Time, minCount, maxCount int) int {
	if maxCount <= minCount {
		return minCount
	}
	return minCount + deterministicHistoricalInt(batchID, day, 0, "daily-count", maxCount-minCount+1)
}

func deterministicHistoricalInt(batchID string, day time.Time, index int, purpose string, modulus int) int {
	if modulus <= 1 {
		return 0
	}
	hash := fnv.New64a()
	_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%s", strings.TrimSpace(batchID), day.Format("2006-01-02"), index, purpose)
	return int(hash.Sum64() % uint64(modulus))
}

func namespaceHistoricalProfile(batchID string, profile dailySimulationProfile) dailySimulationProfile {
	namespace := deterministicHistoricalInt(batchID, time.Time{}, 0, "identity-namespace", 100)
	days := int(profile.RunDate.Unix() / int64(24*time.Hour/time.Second))
	if days < 0 {
		days = -days
	}
	sequence := (days%1000)*1000 + profile.Index
	phone := strings.TrimSpace(profile.GuardianPhone)
	if len(phone) >= 8 {
		phone = phone[:len(phone)-8] + fmt.Sprintf("%02d%06d", namespace, sequence)
	}
	email := strings.TrimSpace(profile.GuardianEmail)
	if strings.LastIndex(email, "@") > 0 {
		email = fmt.Sprintf("hist%02d.%s", namespace, email)
	}
	profile.GuardianPhone = phone
	profile.GuardianEmail = strings.ToLower(email)
	return profile
}

func freezeHistoricalTarget(manifest *HistoricalManifest, scenario dailySimulationScenario) error {
	if manifest == nil || scenario.Target == nil {
		return fmt.Errorf("historical scenario target is required")
	}
	target := HistoricalTargetManifest{
		TargetType: scenario.Target.TargetType, TargetCode: scenario.Target.TargetCode, TargetVersion: scenario.Target.TargetVersion,
		QuestionnaireCode: scenario.Target.QuestionnaireCode, QuestionnaireVersion: scenario.Target.QuestionnaireVersion,
		RequiresAssessment: scenario.Target.RequiresAssessment,
	}
	key := strings.Join([]string{target.TargetType, target.TargetCode}, "/")
	if frozen, exists := manifest.Targets[key]; exists {
		if frozen != target {
			return fmt.Errorf("historical target version drift for %s: frozen=%+v current=%+v", key, frozen, target)
		}
		return nil
	}
	manifest.Targets[key] = target
	return nil
}

func VerifyHistoricalBackfillWithServer(ctx context.Context, deps *Dependencies, stateDir, batchID string) (HistoricalVerification, error) {
	verification, err := VerifyHistoricalBackfill(stateDir, batchID)
	if err != nil {
		return HistoricalVerification{}, err
	}
	manifest, err := LoadHistoricalManifest(stateDir, batchID)
	if err != nil {
		return HistoricalVerification{}, err
	}
	if deps == nil || deps.APIClient == nil {
		return HistoricalVerification{}, fmt.Errorf("historical verification API client is required")
	}
	allStages, err := loadHistoricalSeedStages(ctx, deps.APIClient, batchID)
	if err != nil {
		return HistoricalVerification{}, fmt.Errorf("load server historical stages: %w", err)
	}
	localStages, err := loadAllHistoricalLocalStages(stateDir, batchID)
	if err != nil {
		return HistoricalVerification{}, fmt.Errorf("load local historical stages: %w", err)
	}
	byScenario := make(map[string]map[string]struct{})
	for _, record := range allStages {
		if byScenario[record.ScenarioID] == nil {
			byScenario[record.ScenarioID] = make(map[string]struct{})
		}
		byScenario[record.ScenarioID][record.Stage] = struct{}{}
	}
	missing := make([]string, 0)
	for scenarioID, scenario := range manifest.Scenarios {
		for _, stage := range expectedLocalStages(manifest, scenario) {
			if _, ok := localStages[scenarioID+"\x00"+stage]; !ok {
				missing = append(missing, "local:"+scenarioID+":"+stage)
			}
		}
		expected := expectedServerStages(manifest, scenario)
		for _, stage := range expected {
			if _, ok := byScenario[scenarioID][stage]; !ok {
				missing = append(missing, scenarioID+":"+stage)
			}
		}
		for _, childScenarioID := range scenario.ChildScenarioIDs {
			for _, stage := range expectedChildLocalStages(manifest, scenario.TargetKey) {
				if _, ok := localStages[childScenarioID+"\x00"+stage]; !ok {
					missing = append(missing, "local:"+childScenarioID+":"+stage)
				}
			}
			for _, stage := range expectedChildServerStages(manifest, scenario) {
				if _, ok := byScenario[childScenarioID][stage]; !ok {
					missing = append(missing, childScenarioID+":"+stage)
				}
			}
		}
		for _, additional := range scenario.AdditionalScenarios {
			for _, stage := range expectedChildLocalStages(manifest, additional.TargetKey) {
				if stage == "task_open" || stage == "task_complete" {
					continue
				}
				if _, ok := localStages[additional.ScenarioID+"\x00"+stage]; !ok {
					missing = append(missing, "local:"+additional.ScenarioID+":"+stage)
				}
			}
			for _, stage := range expectedAdditionalServerStages(manifest, additional) {
				if _, ok := byScenario[additional.ScenarioID][stage]; !ok {
					missing = append(missing, additional.ScenarioID+":"+stage)
				}
			}
		}
	}
	verification.ServerStageCount = len(allStages)
	verification.MissingStages = missing
	verification.Complete = verification.Complete && len(missing) == 0
	return verification, nil
}

func loadHistoricalSeedStages(ctx context.Context, client *APIClient, batchID string) ([]HistoricalStageRecord, error) {
	if client == nil {
		return nil, fmt.Errorf("historical stage API client is required")
	}
	const pageSize = 10000
	allStages := make([]HistoricalStageRecord, 0)
	for offset := 0; ; offset += pageSize {
		page, err := client.ListHistoricalSeedStages(ctx, batchID, offset, pageSize)
		if err != nil {
			return nil, err
		}
		allStages = append(allStages, page.Stages...)
		if len(page.Stages) < pageSize {
			return allStages, nil
		}
	}
}

func mergeHistoricalStageResources(manifest *HistoricalManifest, stages []HistoricalStageRecord) {
	if manifest == nil {
		return
	}
	childParents := make(map[string]string)
	for scenarioID, scenario := range manifest.Scenarios {
		for _, childID := range scenario.ChildScenarioIDs {
			childParents[childID] = scenarioID
		}
		for _, additional := range scenario.AdditionalScenarios {
			childParents[additional.ScenarioID] = scenarioID
		}
	}
	for _, stage := range stages {
		manifestScenarioID := stage.ScenarioID
		scenario, ok := manifest.Scenarios[manifestScenarioID]
		if !ok {
			manifestScenarioID = childParents[stage.ScenarioID]
			scenario, ok = manifest.Scenarios[manifestScenarioID]
		}
		if !ok {
			continue
		}
		switch stage.Stage {
		case "entry_resolve":
			scenario.EntryID = stage.ResourceID
		case "entry_intake":
			scenario.TesteeID = stage.ResourceID
		case "plan_enrollment":
			scenario.EnrollmentID = stage.ResourceID
		case "task_open", "task_complete":
			scenario.TaskIDs = appendUniqueString(scenario.TaskIDs, stage.ResourceID)
		case "answersheet_submit":
			scenario.AnswerSheetID = stage.ResourceID
			scenario.AnswerSheetIDs = appendUniqueString(scenario.AnswerSheetIDs, stage.ResourceID)
		case "assessment_created", "assessment_submitted":
			scenario.AssessmentID = stage.ResourceID
			scenario.AssessmentIDs = appendUniqueString(scenario.AssessmentIDs, stage.ResourceID)
		case "outcome_committed":
			scenario.OutcomeID = stage.ResourceID
			scenario.OutcomeIDs = appendUniqueString(scenario.OutcomeIDs, stage.ResourceID)
		case "report_generated":
			scenario.ReportID = stage.ResourceID
			scenario.ReportIDs = appendUniqueString(scenario.ReportIDs, stage.ResourceID)
			var payload struct {
				GenerationID string `json:"generation_id"`
				RunID        string `json:"run_id"`
			}
			if json.Unmarshal(stage.PayloadJSON, &payload) == nil {
				scenario.GenerationID = payload.GenerationID
				scenario.ReportRunID = payload.RunID
			}
		}
		manifest.Scenarios[manifestScenarioID] = scenario
	}
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func expectedServerStages(manifest HistoricalManifest, scenario HistoricalScenarioManifest) []string {
	switch scenario.Journey {
	case string(dailySimulationJourneyResolveEntry):
		return []string{"entry_resolve"}
	case string(dailySimulationJourneySubmitAnswer):
		stages := []string{"entry_resolve", "entry_intake", "plan_enrollment"}
		if len(scenario.ChildScenarioIDs) == 0 {
			stages = append(stages, "answersheet_submit")
		}
		if target, ok := manifest.Targets[scenario.TargetKey]; ok && target.RequiresAssessment {
			if len(scenario.ChildScenarioIDs) == 0 {
				stages = append(stages, "assessment_created", "assessment_submitted", "outcome_committed", "report_generated")
			}
		}
		return stages
	default:
		return nil
	}
}

func expectedChildServerStages(manifest HistoricalManifest, scenario HistoricalScenarioManifest) []string {
	stages := []string{"task_open", "task_complete", "answersheet_submit"}
	if target, ok := manifest.Targets[scenario.TargetKey]; ok && target.RequiresAssessment {
		stages = append(stages, "assessment_created", "assessment_submitted", "outcome_committed", "report_generated")
	}
	return stages
}

func expectedAdditionalServerStages(manifest HistoricalManifest, additional HistoricalAdditionalScenarioManifest) []string {
	stages := []string{"answersheet_submit"}
	if target, ok := manifest.Targets[additional.TargetKey]; ok && target.RequiresAssessment {
		stages = append(stages, "assessment_created", "assessment_submitted", "outcome_committed", "report_generated")
	}
	return stages
}

func expectedLocalStages(manifest HistoricalManifest, scenario HistoricalScenarioManifest) []string {
	stages := []string{"guardian_account"}
	switch scenario.Journey {
	case string(dailySimulationJourneyRegisterOnly):
		return stages
	case string(dailySimulationJourneyCreateTestee):
		return append(stages, "testee_profile")
	case string(dailySimulationJourneyResolveEntry):
		return append(stages, "testee_profile", "entry_resolve")
	case string(dailySimulationJourneySubmitAnswer):
		stages = append(stages, "testee_profile", "entry_resolve", "entry_intake", "plan_enrollment")
		if len(scenario.ChildScenarioIDs) == 0 {
			stages = append(stages, expectedAnswerLocalStages(manifest, scenario.TargetKey)...)
		}
	}
	return stages
}

func expectedChildLocalStages(manifest HistoricalManifest, targetKey string) []string {
	return append([]string{"task_open"}, append(expectedAnswerLocalStages(manifest, targetKey), "task_complete")...)
}

func expectedAnswerLocalStages(manifest HistoricalManifest, targetKey string) []string {
	stages := []string{"answersheet_submit"}
	if target, ok := manifest.Targets[targetKey]; ok && target.RequiresAssessment {
		stages = append(stages, "assessment_created", "outcome_committed", "report_generated")
	}
	return stages
}

func recordHistoricalLocalStage(
	stateDir string,
	manifest *HistoricalManifest,
	profile dailySimulationProfile,
	scenario dailySimulationScenario,
	historical historicalseed.Context,
	stage dailySimulationJourneyStage,
	outcome dailySimulationOutcome,
	target *dailySimulationResolvedTarget,
) error {
	if manifest == nil {
		return fmt.Errorf("historical manifest is required")
	}
	ledger, err := loadHistoricalLocalScenarioLedger(stateDir, manifest.BatchID, profile.RunDate, historical.ScenarioID)
	if err != nil {
		return err
	}
	targetKey := ""
	if target != nil {
		targetKey = strings.Join([]string{target.TargetType, target.TargetCode, target.TargetVersion, target.QuestionnaireCode, target.QuestionnaireVersion}, "/")
	}
	fingerprintPayload := struct {
		BatchID, ScenarioID, Stage, BusinessDate, Journey, TargetKey string
		Index                                                        int
		EntryID                                                      string
	}{
		BatchID: manifest.BatchID, ScenarioID: historical.ScenarioID, Stage: string(stage),
		BusinessDate: profile.RunDate.Format("2006-01-02"), Journey: outcome.JourneyTarget,
		TargetKey: targetKey, Index: profile.Index,
	}
	if scenario.Entry != nil {
		fingerprintPayload.EntryID = scenario.Entry.ID
	}
	payload, err := json.Marshal(fingerprintPayload)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	record := ledger.Records[string(stage)]
	if record.PayloadHash != "" && record.PayloadHash != hash {
		return fmt.Errorf("historical local stage payload conflict: scenario=%s stage=%s", historical.ScenarioID, stage)
	}
	record.ScenarioID, record.Stage, record.PayloadHash, record.Status = historical.ScenarioID, string(stage), hash, "completed"
	record.UserCreated = record.UserCreated || outcome.UserCreated
	record.TesteeCreated = record.TesteeCreated || outcome.TesteeCreated
	record.GuardianUserID = firstHistoricalValue(record.GuardianUserID, outcome.GuardianUserID)
	record.IAMProfileID = firstHistoricalValue(record.IAMProfileID, outcome.IAMProfileID)
	record.IAMProfileLinkID = firstHistoricalValue(record.IAMProfileLinkID, outcome.IAMProfileLinkID)
	record.TesteeID = firstHistoricalValue(record.TesteeID, outcome.TesteeID)
	record.EnrollmentID = firstHistoricalValue(record.EnrollmentID, outcome.EnrollmentID)
	allTaskIDs := append([]string(nil), outcome.TaskIDs...)
	allTaskIDs = append(allTaskIDs, outcome.CompletedTaskIDs...)
	record.TaskIDs = appendUniqueStrings(record.TaskIDs, allTaskIDs)
	record.AnswerSheetIDs = appendUniqueStrings(record.AnswerSheetIDs, outcome.AnswerSheetIDs)
	record.AssessmentIDs = appendUniqueStrings(record.AssessmentIDs, outcome.AssessmentIDs)
	record.UpdatedAt = time.Now().UTC()
	ledger.Records[string(stage)] = record
	return saveSecureJSON(historicalLocalScenarioStagePath(stateDir, manifest.BatchID, profile.RunDate, historical.ScenarioID), &ledger)
}

type historicalLocalScenarioLedger struct {
	Version    int                                   `json:"version"`
	BatchID    string                                `json:"batch_id"`
	ScenarioID string                                `json:"scenario_id"`
	Records    map[string]HistoricalLocalStageRecord `json:"records"`
}

func historicalLocalScenarioStagePath(stateDir, batchID string, day time.Time, scenarioID string) string {
	checkpointPath, _ := historicalPaths(stateDir, batchID)
	digest := sha256.Sum256([]byte(strings.TrimSpace(scenarioID)))
	return filepath.Join(filepath.Dir(checkpointPath), "stages", day.Format("2006-01-02"), hex.EncodeToString(digest[:16])+".json")
}

func loadHistoricalLocalScenarioLedger(stateDir, batchID string, day time.Time, scenarioID string) (historicalLocalScenarioLedger, error) {
	path := historicalLocalScenarioStagePath(stateDir, batchID, day, scenarioID)
	ledger := historicalLocalScenarioLedger{}
	exists, err := loadSecureJSON(path, &ledger)
	if err != nil {
		return historicalLocalScenarioLedger{}, err
	}
	if exists && (ledger.Version != 1 || ledger.BatchID != batchID || ledger.ScenarioID != scenarioID) {
		return historicalLocalScenarioLedger{}, fmt.Errorf("historical local stage ledger identity conflict for %s", scenarioID)
	}
	if !exists {
		ledger = historicalLocalScenarioLedger{Version: 1, BatchID: batchID, ScenarioID: scenarioID, Records: make(map[string]HistoricalLocalStageRecord)}
	}
	if ledger.Records == nil {
		ledger.Records = make(map[string]HistoricalLocalStageRecord)
	}
	return ledger, nil
}

func loadHistoricalLocalScenarioStages(stateDir, batchID string, day time.Time, scenarioID string) (map[string]HistoricalLocalStageRecord, error) {
	ledger, err := loadHistoricalLocalScenarioLedger(stateDir, batchID, day, scenarioID)
	if err != nil {
		return nil, err
	}
	return ledger.Records, nil
}

func loadAllHistoricalLocalStages(stateDir, batchID string) (map[string]HistoricalLocalStageRecord, error) {
	checkpointPath, _ := historicalPaths(stateDir, batchID)
	root := filepath.Join(filepath.Dir(checkpointPath), "stages")
	days, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return map[string]HistoricalLocalStageRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make(map[string]HistoricalLocalStageRecord)
	for _, day := range days {
		if !day.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, day.Name()))
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
				continue
			}
			var ledger historicalLocalScenarioLedger
			if _, err := loadSecureJSON(filepath.Join(root, day.Name(), file.Name()), &ledger); err != nil {
				return nil, err
			}
			if ledger.Version != 1 || ledger.BatchID != batchID || strings.TrimSpace(ledger.ScenarioID) == "" {
				return nil, fmt.Errorf("invalid historical local stage ledger %s", file.Name())
			}
			for stage, record := range ledger.Records {
				result[ledger.ScenarioID+"\x00"+stage] = record
			}
		}
	}
	return result, nil
}

func firstHistoricalValue(existing, candidate string) string {
	if strings.TrimSpace(existing) != "" {
		return existing
	}
	return strings.TrimSpace(candidate)
}

func appendUniqueStrings(values []string, candidates []string) []string {
	for _, candidate := range candidates {
		values = appendUniqueString(values, candidate)
	}
	return values
}

func recordHistoricalScenario(manifest *HistoricalManifest, batchID string, profile dailySimulationProfile, scenario dailySimulationScenario, outcome dailySimulationOutcome, localStages map[string]HistoricalLocalStageRecord) {
	context := buildHistoricalScenarioContext(batchID, uint64(manifest.OrgID), DailySimulationConfig{JourneyMix: DailySimulationJourneyMixConfig{SubmitAnswerWeight: 100}}, profile, scenario)
	// Use the actual journey selected by the state machine, not the helper's default.
	context.ScenarioID = fmt.Sprintf("%s/%d/%s/%s", profile.RunDate.Format("2006-01-02"), profile.Index, outcome.JourneyTarget, scenario.Target.TargetCode)
	targetKey := strings.Join([]string{scenario.Target.TargetType, scenario.Target.TargetCode}, "/")
	existing := manifest.Scenarios[context.ScenarioID]
	if local, ok := localStages[string(dailySimulationJourneyStageGuardianAccount)]; ok {
		outcome.UserCreated = outcome.UserCreated || local.UserCreated
		outcome.GuardianUserID = firstHistoricalValue(outcome.GuardianUserID, local.GuardianUserID)
	}
	if local, ok := localStages[string(dailySimulationJourneyStageTesteeProfile)]; ok {
		outcome.TesteeCreated = outcome.TesteeCreated || local.TesteeCreated
		outcome.TesteeID = firstHistoricalValue(outcome.TesteeID, local.TesteeID)
		outcome.IAMProfileID = firstHistoricalValue(outcome.IAMProfileID, local.IAMProfileID)
		outcome.IAMProfileLinkID = firstHistoricalValue(outcome.IAMProfileLinkID, local.IAMProfileLinkID)
	}
	manifest.Scenarios[context.ScenarioID] = HistoricalScenarioManifest{
		ScenarioID: context.ScenarioID, BusinessDate: profile.RunDate.Format("2006-01-02"), Journey: outcome.JourneyTarget, TargetKey: targetKey,
		GuardianUserID: outcome.GuardianUserID, IAMProfileID: outcome.IAMProfileID, IAMProfileLinkID: outcome.IAMProfileLinkID, TesteeID: outcome.TesteeID,
		TesteeCreated: outcome.TesteeCreated || existing.TesteeCreated,
		EntryID:       scenario.Entry.ID, PlanID: outcome.PlanID, EnrollmentID: outcome.EnrollmentID, TaskIDs: append([]string(nil), outcome.TaskIDs...),
		CompletedTaskIDs: append([]string(nil), outcome.CompletedTaskIDs...), ChildScenarioIDs: append([]string(nil), outcome.ChildScenarioIDs...),
		AdditionalScenarios: append([]HistoricalAdditionalScenarioManifest(nil), outcome.AdditionalScenarios...),
		AnswerSheetID:       outcome.AnswerSheetID, AnswerSheetIDs: append([]string(nil), outcome.AnswerSheetIDs...),
		AssessmentID: outcome.AssessmentID, AssessmentIDs: append([]string(nil), outcome.AssessmentIDs...), Terminal: outcome.StopReason,
	}
}

func VerifyHistoricalBackfill(stateDir, batchID string) (HistoricalVerification, error) {
	checkpointPath, manifestPath := historicalPaths(stateDir, batchID)
	var checkpoint HistoricalCheckpoint
	if exists, err := loadSecureJSON(checkpointPath, &checkpoint); err != nil || !exists {
		if err != nil {
			return HistoricalVerification{}, err
		}
		return HistoricalVerification{}, fmt.Errorf("historical checkpoint not found for batch %s", batchID)
	}
	var manifest HistoricalManifest
	if exists, err := loadSecureJSON(manifestPath, &manifest); err != nil || !exists {
		if err != nil {
			return HistoricalVerification{}, err
		}
		return HistoricalVerification{}, fmt.Errorf("historical manifest not found for batch %s", batchID)
	}
	location, _ := time.LoadLocation(historicalTimezone)
	from, to, err := parseHistoricalDateRange(manifest.From, manifest.To, location)
	if err != nil {
		return HistoricalVerification{}, err
	}
	expectedDays := int(to.Sub(from).Hours()/24) + 1
	expectedScenarios := 0
	for _, count := range manifest.DailyCounts {
		expectedScenarios += count
	}
	return HistoricalVerification{
		BatchID: batchID, Complete: checkpoint.CompletedThrough == manifest.To && len(manifest.DailyCounts) == expectedDays && len(manifest.Scenarios) == expectedScenarios,
		CompletedThrough: checkpoint.CompletedThrough, ExpectedDays: expectedDays, RecordedDays: len(manifest.DailyCounts),
		ExpectedScenarios: expectedScenarios, RecordedScenarios: len(manifest.Scenarios),
	}, nil
}

func LoadHistoricalManifest(stateDir, batchID string) (HistoricalManifest, error) {
	_, path := historicalPaths(stateDir, batchID)
	var manifest HistoricalManifest
	exists, err := loadSecureJSON(path, &manifest)
	if err != nil {
		return HistoricalManifest{}, err
	}
	if !exists {
		return HistoricalManifest{}, fmt.Errorf("historical manifest not found for batch %s", batchID)
	}
	return manifest, nil
}

func parseHistoricalDateRange(fromRaw, toRaw string, location *time.Location) (time.Time, time.Time, error) {
	from, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(fromRaw), location)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid historical --from date: %w", err)
	}
	to, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(toRaw), location)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid historical --to date: %w", err)
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("historical --to must not be before --from")
	}
	return from, to, nil
}

func historicalPaths(stateDir, batchID string) (string, string) {
	clean := strings.TrimSpace(batchID)
	sum := sha256.Sum256([]byte(clean))
	dir := filepath.Join(strings.TrimSpace(stateDir), hex.EncodeToString(sum[:8]))
	return filepath.Join(dir, "checkpoint.json"), filepath.Join(dir, "manifest.json")
}

func loadSecureJSON(path string, target any) (bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return false, fmt.Errorf("decode %s: %w", path, err)
	}
	return true, nil
}

func saveSecureJSON(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(dir, ".historical-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
