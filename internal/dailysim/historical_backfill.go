package dailysim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FangcunMount/seeddata-runner/internal/historicalseed"
	"golang.org/x/sync/errgroup"
)

const historicalTimezone = "Asia/Shanghai"

type historicalCutoffKey struct{}
type historicalFrozenTargetVersionsKey struct{}
type historicalDaySnapshotKey struct{}
type historicalPlanTaskDiscoveryRecorderKey struct{}

func withHistoricalCutoff(ctx context.Context, cutoff time.Time) context.Context {
	return context.WithValue(ctx, historicalCutoffKey{}, cutoff)
}

func historicalCutoffFromContext(ctx context.Context) (time.Time, bool) {
	cutoff, ok := ctx.Value(historicalCutoffKey{}).(time.Time)
	return cutoff, ok && !cutoff.IsZero()
}

func withHistoricalFrozenTargetVersions(ctx context.Context, versions map[string]string) context.Context {
	copy := make(map[string]string, len(versions))
	for code, version := range versions {
		copy[code] = version
	}
	return context.WithValue(ctx, historicalFrozenTargetVersionsKey{}, copy)
}

func historicalFrozenTargetVersion(ctx context.Context, code string) (string, bool) {
	versions, ok := ctx.Value(historicalFrozenTargetVersionsKey{}).(map[string]string)
	if !ok {
		return "", false
	}
	version, exists := versions[strings.TrimSpace(code)]
	return strings.TrimSpace(version), exists
}

type HistoricalPlanTaskRecovery struct {
	ScenarioID string `json:"scenario_id"`
	TaskID     string `json:"task_id"`
	PlannedAt  string `json:"planned_at"`
	TargetKey  string `json:"target_key"`
}

type HistoricalScenarioSnapshot struct {
	Scenario HistoricalScenarioManifest
	Local    map[string]HistoricalLocalStageRecord
	Server   map[string]HistoricalStageRecord
}

type HistoricalDaySnapshot struct {
	BusinessDate string
	Expected     map[string]HistoricalScenarioManifest
	Scenarios    map[string]HistoricalScenarioSnapshot
}

func withHistoricalDaySnapshot(ctx context.Context, snapshot *HistoricalDaySnapshot) context.Context {
	return context.WithValue(ctx, historicalDaySnapshotKey{}, snapshot)
}

func historicalScenarioSnapshot(ctx context.Context, scenarioID string) (HistoricalScenarioSnapshot, bool) {
	snapshot, _ := ctx.Value(historicalDaySnapshotKey{}).(*HistoricalDaySnapshot)
	if snapshot == nil {
		return HistoricalScenarioSnapshot{}, false
	}
	value, ok := snapshot.Scenarios[strings.TrimSpace(scenarioID)]
	return value, ok
}

type historicalPlanTaskDiscoveryRecorder func(historicalseed.Context, HistoricalPlanTaskRecovery) error

func withHistoricalPlanTaskDiscoveryRecorder(ctx context.Context, recorder historicalPlanTaskDiscoveryRecorder) context.Context {
	return context.WithValue(ctx, historicalPlanTaskDiscoveryRecorderKey{}, recorder)
}

func recordHistoricalPlanTaskDiscovery(ctx context.Context, historical historicalseed.Context, recovery HistoricalPlanTaskRecovery) error {
	recorder, _ := ctx.Value(historicalPlanTaskDiscoveryRecorderKey{}).(historicalPlanTaskDiscoveryRecorder)
	if recorder == nil {
		return nil
	}
	return recorder(historical, recovery)
}

type HistoricalBackfillOptions struct {
	From              string
	To                string
	BatchID           string
	Resume            bool
	StateDir          string
	CountMin          int
	CountMax          int
	Workers           int
	SubmissionWorkers int
	StageReadWorkers  int
	IAMWorkers        int
	ProgressInterval  string
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
	PlanTaskRecoveries  []HistoricalPlanTaskRecovery           `json:"plan_task_recoveries,omitempty"`
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
	if opts.SubmissionWorkers <= 0 {
		opts.SubmissionWorkers = opts.Workers
	}
	if opts.StageReadWorkers <= 0 {
		opts.StageReadWorkers = 1
	}
	if opts.IAMWorkers <= 0 {
		opts.IAMWorkers = 1
	}
	progressInterval := 15 * time.Second
	if strings.TrimSpace(opts.ProgressInterval) != "" {
		progressInterval, err = time.ParseDuration(opts.ProgressInterval)
		if err != nil || progressInterval <= 0 {
			return fmt.Errorf("invalid historical progress interval %q", opts.ProgressInterval)
		}
	}

	store, err := openHistoricalStateStore(opts.StateDir, opts.BatchID, false)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	identity, err := store.loadIdentity()
	if err != nil {
		return err
	}
	expectedIdentity := historicalStateIdentity{Version: historicalStateDBVersion, BatchID: opts.BatchID, OrgID: deps.Config.Global.OrgID, From: opts.From, To: opts.To}
	if identity != expectedIdentity {
		return fmt.Errorf("historical state identity conflict: stored=%+v requested=%+v", identity, expectedIdentity)
	}
	checkpoint, err := store.loadCheckpoint()
	if err != nil {
		return err
	}
	manifest, err := store.loadManifest()
	if err != nil {
		return err
	}
	if !opts.Resume && (strings.TrimSpace(checkpoint.CompletedThrough) != "" || len(manifest.Scenarios) > 0) {
		return fmt.Errorf("historical checkpoint already exists for batch %s; use --resume", opts.BatchID)
	}
	if checkpoint.BatchID != opts.BatchID || checkpoint.From != opts.From || checkpoint.To != opts.To {
		return fmt.Errorf("historical checkpoint identity conflicts with requested batch/range")
	}
	if manifest.BatchID != opts.BatchID || manifest.From != opts.From || manifest.To != opts.To || manifest.OrgID != deps.Config.Global.OrgID {
		return fmt.Errorf("historical manifest identity conflicts with requested batch/range/org")
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
	runDeps := *deps
	runDeps.DailySubmissionLedger = store
	deps = &runDeps

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
		cfg, frozenVersions, primaryTargetCode, freezeErr := freezeHistoricalConfiguredTargets(ctx, deps, deps.Config.DailySimulation, &manifest)
		if freezeErr != nil {
			return fmt.Errorf("historical target drift on %s: %w", dayKey, freezeErr)
		}
		dayCtx := withHistoricalFrozenTargetVersions(ctx, frozenVersions)
		if err := freezeHistoricalPlans(dayCtx, deps.APIClient, deps.Config.DailySimulation.PlanIDs, primaryTargetCode, &manifest); err != nil {
			return fmt.Errorf("historical plan drift on %s: %w", dayKey, err)
		}
		manifest.UpdatedAt = time.Now().UTC()
		if err := persistHistoricalFrozenConfiguration(store, manifest); err != nil {
			return err
		}
		count := DeterministicHistoricalCount(opts.BatchID, day, opts.CountMin, opts.CountMax)
		recoveringDay := historicalManifestContainsDay(manifest, dayKey)
		if err := store.putDayState(dayKey, "running"); err != nil {
			return err
		}
		daySnapshot, snapshotErr := buildHistoricalDaySnapshot(dayCtx, deps, cfg, &manifest, day, count, opts.StateDir, store, opts.StageReadWorkers, recoveringDay)
		if snapshotErr != nil {
			return fmt.Errorf("historical snapshot stopped on %s: %w", dayKey, snapshotErr)
		}
		for _, scenario := range daySnapshot.Expected {
			if err := store.putScenario(scenario); err != nil {
				return err
			}
		}
		dayCtx = withHistoricalDaySnapshot(dayCtx, daySnapshot)
		executor := newHistoricalSubmissionExecutor(dayCtx, deps.Logger, dayKey, count, opts.SubmissionWorkers, progressInterval)
		dayCtx = withHistoricalSubmissionExecutor(dayCtx, executor)
		cfg.Workers = opts.Workers
		err := runDailySimulationBatchWithOptions(dayCtx, deps, cfg, day, count, "historical_backfill_"+dayKey, dailySimulationBatchOptions{
			HistoricalBatchID: opts.BatchID,
			IAMWorkers:        opts.IAMWorkers,
			ShouldSkipScenario: func(profile dailySimulationProfile, scenario dailySimulationScenario) bool {
				historical := buildHistoricalScenarioContext(opts.BatchID, uint64(deps.Config.Global.OrgID), cfg, profile, scenario)
				manifestMu.Lock()
				recorded, exists := manifest.Scenarios[historical.ScenarioID]
				manifestMu.Unlock()
				skip := exists && strings.TrimSpace(recorded.Terminal) != ""
				if skip {
					executor.MarkParentCompleted()
				}
				return skip
			},
			RestoreExistingTestee: func(profile dailySimulationProfile, scenario dailySimulationScenario) (*ApiserverTesteeResponse, error) {
				historical := buildHistoricalScenarioContext(opts.BatchID, uint64(deps.Config.Global.OrgID), cfg, profile, scenario)
				return restoreHistoricalExistingTesteeFromStore(store, historical.ScenarioID, profile, cfg)
			},
			ValidateScenario: func(scenario dailySimulationScenario) error {
				manifestMu.Lock()
				if err := freezeHistoricalTarget(&manifest, scenario); err != nil {
					manifestMu.Unlock()
					return err
				}
				key := strings.Join([]string{scenario.Target.TargetType, scenario.Target.TargetCode}, "/")
				manifest.UpdatedAt = time.Now().UTC()
				frozen, header := manifest.Targets[key], manifest
				manifestMu.Unlock()
				if err := store.putTarget(key, frozen); err != nil {
					return err
				}
				return store.putManifestHeader(header)
			},
			OnScenarioComplete: func(profile dailySimulationProfile, scenario dailySimulationScenario, outcome dailySimulationOutcome) error {
				scenarioID := buildHistoricalScenarioContext(opts.BatchID, uint64(deps.Config.Global.OrgID), cfg, profile, scenario).ScenarioID
				localStages, loadErr := store.loadLocalStages(scenarioID)
				if loadErr != nil {
					return loadErr
				}
				manifestMu.Lock()
				recordHistoricalScenario(&manifest, opts.BatchID, profile, scenario, outcome, localStages)
				recorded := manifest.Scenarios[scenarioID]
				manifestMu.Unlock()
				if err := store.putScenario(recorded); err != nil {
					return err
				}
				executor.MarkParentCompleted()
				return nil
			},
			OnHistoricalStageComplete: func(profile dailySimulationProfile, scenario dailySimulationScenario, historical historicalseed.Context, stage dailySimulationJourneyStage, outcome dailySimulationOutcome, target *dailySimulationResolvedTarget) error {
				if target != nil {
					manifestMu.Lock()
					if err := freezeHistoricalTarget(&manifest, dailySimulationScenario{Target: target}); err != nil {
						manifestMu.Unlock()
						return err
					}
					key := strings.Join([]string{target.TargetType, target.TargetCode}, "/")
					frozen := manifest.Targets[key]
					manifestMu.Unlock()
					if err := store.putTarget(key, frozen); err != nil {
						return err
					}
				}
				return recordHistoricalLocalStageInStore(store, &manifest, profile, scenario, historical, stage, outcome, target)
			},
			OnHistoricalPlanTaskFound: func(_ dailySimulationProfile, _ dailySimulationScenario, historical historicalseed.Context, recovery HistoricalPlanTaskRecovery) error {
				manifestMu.Lock()
				parent := manifest.Scenarios[historical.ScenarioID]
				manifestMu.Unlock()
				if strings.TrimSpace(parent.ScenarioID) == "" {
					return fmt.Errorf("historical parent scenario %s is not materialized before task discovery", historical.ScenarioID)
				}
				if err := store.putPlanTask(historical.ScenarioID, recovery); err != nil {
					return err
				}
				manifestMu.Lock()
				parent = manifest.Scenarios[historical.ScenarioID]
				for _, existing := range parent.PlanTaskRecoveries {
					if existing.TaskID == recovery.TaskID {
						if existing != recovery {
							manifestMu.Unlock()
							return fmt.Errorf("historical plan task recovery conflict for %s", recovery.TaskID)
						}
						manifestMu.Unlock()
						return nil
					}
				}
				parent.PlanTaskRecoveries = append(parent.PlanTaskRecoveries, recovery)
				parent.ChildScenarioIDs = appendUniqueString(parent.ChildScenarioIDs, recovery.ScenarioID)
				parent.TaskIDs = appendUniqueString(parent.TaskIDs, recovery.TaskID)
				manifest.Scenarios[historical.ScenarioID] = parent
				manifest.UpdatedAt = time.Now().UTC()
				manifestMu.Unlock()
				return store.putScenario(parent)
			},
		})
		executor.Close()
		var dayVerifyErr error
		if err == nil {
			stages, stageErr := loadHistoricalSeedStagesForDayConcurrent(ctx, deps.APIClient, manifest, day, opts.StageReadWorkers)
			if stageErr != nil {
				dayVerifyErr = fmt.Errorf("load historical stages for %s: %w", dayKey, stageErr)
			} else {
				mergeHistoricalStageResources(&manifest, stages)
				dayVerifyErr = verifyHistoricalDayWithStore(store, &manifest, day, count, stages)
			}
		}
		manifestMu.Lock()
		manifest.UpdatedAt = time.Now().UTC()
		saveErr := persistHistoricalDay(store, manifest, dayKey)
		manifestMu.Unlock()
		if saveErr != nil {
			return saveErr
		}
		if err != nil {
			return fmt.Errorf("historical backfill stopped on %s: %w", dayKey, err)
		}
		if dayVerifyErr != nil {
			return fmt.Errorf("historical backfill verification stopped on %s: %w", dayKey, dayVerifyErr)
		}
		checkpoint.CompletedThrough = dayKey
		checkpoint.UpdatedAt = time.Now().UTC()
		if err := store.putDayState(dayKey, "verified"); err != nil {
			return err
		}
		if err := store.putCheckpoint(checkpoint); err != nil {
			return err
		}
		if err := store.exportJSON(opts.StateDir, opts.BatchID); err != nil {
			return err
		}
	}
	stages, err := loadHistoricalSeedStages(ctx, deps.APIClient, opts.BatchID)
	if err != nil {
		return fmt.Errorf("load terminal historical stage ledger: %w", err)
	}
	mergeHistoricalStageResources(&manifest, stages)
	if err := verifyHistoricalBatchWithStore(store, &manifest, from, to, stages); err != nil {
		return fmt.Errorf("terminal historical batch verification failed: %w", err)
	}
	manifest.UpdatedAt = time.Now().UTC()
	if err := persistHistoricalManifest(store, manifest); err != nil {
		return err
	}
	return store.exportJSON(opts.StateDir, opts.BatchID)
}

func historicalManifestContainsDay(manifest HistoricalManifest, day string) bool {
	for _, scenario := range manifest.Scenarios {
		if scenario.BusinessDate == day || scenarioDateFromID(scenario.ScenarioID) == day {
			return true
		}
	}
	return false
}

func freezeHistoricalConfiguredTargets(ctx context.Context, deps *Dependencies, cfg DailySimulationConfig, manifest *HistoricalManifest) (DailySimulationConfig, map[string]string, string, error) {
	if deps == nil || deps.APIClient == nil || deps.CollectionClient == nil || manifest == nil {
		return cfg, nil, "", fmt.Errorf("historical target freeze dependencies are required")
	}
	var (
		target *dailySimulationResolvedTarget
		err    error
	)
	if !cfg.EntryID.IsZero() {
		entry, getErr := deps.APIClient.GetAssessmentEntry(ctx, cfg.EntryID.String())
		if getErr != nil {
			return cfg, nil, "", fmt.Errorf("load configured assessment entry %s: %w", cfg.EntryID.String(), getErr)
		}
		if entry == nil {
			return cfg, nil, "", fmt.Errorf("configured assessment entry %s not found", cfg.EntryID.String())
		}
		target, err = resolveDailySimulationTarget(ctx, deps.APIClient, deps.CollectionClient, entry.TargetType, entry.TargetCode, entry.TargetVersion)
	} else {
		target, err = resolveDailySimulationTarget(ctx, deps.APIClient, deps.CollectionClient, cfg.TargetType, cfg.TargetCode, cfg.TargetVersion)
	}
	if err != nil {
		return cfg, nil, "", err
	}
	if err := freezeHistoricalTarget(manifest, dailySimulationScenario{Target: target}); err != nil {
		return cfg, nil, "", err
	}
	versions := map[string]string{target.TargetCode: target.TargetVersion}
	if cfg.EntryID.IsZero() {
		cfg.TargetType, cfg.TargetCode, cfg.TargetVersion = target.TargetType, target.TargetCode, target.TargetVersion
	}
	for _, code := range collectDailySimulationAdditionalTargetCodes(cfg) {
		additional, resolveErr := resolveDailySimulationTarget(ctx, deps.APIClient, deps.CollectionClient, target.TargetType, code, "")
		if resolveErr != nil {
			return cfg, nil, "", resolveErr
		}
		if err := freezeHistoricalTarget(manifest, dailySimulationScenario{Target: additional}); err != nil {
			return cfg, nil, "", err
		}
		versions[additional.TargetCode] = additional.TargetVersion
	}
	return cfg, versions, strings.TrimSpace(target.TargetCode), nil
}

func restoreHistoricalExistingTestee(stateDir, batchID string, profile dailySimulationProfile, scenarioID string, cfg DailySimulationConfig) (*ApiserverTesteeResponse, error) {
	records, err := loadHistoricalLocalScenarioStages(stateDir, batchID, profile.RunDate, scenarioID)
	if err != nil {
		return nil, err
	}
	return restoreHistoricalExistingTesteeFromRecords(records, scenarioID, profile, cfg)
}

func restoreHistoricalExistingTesteeFromStore(store *historicalStateStore, scenarioID string, profile dailySimulationProfile, cfg DailySimulationConfig) (*ApiserverTesteeResponse, error) {
	records, err := store.loadLocalStages(scenarioID)
	if err != nil {
		return nil, err
	}
	return restoreHistoricalExistingTesteeFromRecords(records, scenarioID, profile, cfg)
}

func restoreHistoricalExistingTesteeFromRecords(records map[string]HistoricalLocalStageRecord, scenarioID string, profile dailySimulationProfile, cfg DailySimulationConfig) (*ApiserverTesteeResponse, error) {
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

func freezeHistoricalPlans(ctx context.Context, client *APIClient, configured []FlexibleID, targetCode string, manifest *HistoricalManifest) error {
	if client == nil || manifest == nil {
		return fmt.Errorf("historical plan freeze dependencies are required")
	}
	planIDs := collectDailySimulationPlanIDs(configured)
	frozenPlans := make(map[string]HistoricalPlanManifest, len(planIDs))
	for _, planID := range planIDs {
		plan, err := client.GetPlan(ctx, planID)
		if err != nil {
			return fmt.Errorf("load plan %s: %w", planID, err)
		}
		if plan == nil {
			return fmt.Errorf("load plan %s: empty response", planID)
		}
		if err := validateHistoricalPlanScale(planID, plan.ScaleCode, targetCode, "historical target"); err != nil {
			return err
		}
		frozen := HistoricalPlanManifest{
			ID: plan.ID, ScaleCode: plan.ScaleCode, ScheduleType: plan.ScheduleType, TriggerTime: plan.TriggerTime,
			Interval: plan.Interval, TotalTimes: plan.TotalTimes, FixedDates: append([]string(nil), plan.FixedDates...),
			RelativeWeeks: append([]int(nil), plan.RelativeWeeks...), Status: plan.Status,
		}
		frozenPlans[planID] = frozen
	}
	for _, planID := range planIDs {
		frozen := frozenPlans[planID]
		if existing, ok := manifest.Plans[planID]; ok {
			if !reflect.DeepEqual(existing, frozen) {
				return fmt.Errorf("plan %s changed: frozen=%+v current=%+v", planID, existing, frozen)
			}
		}
	}
	for _, planID := range planIDs {
		if _, exists := manifest.Plans[planID]; exists {
			continue
		}
		frozen := frozenPlans[planID]
		manifest.Plans[planID] = frozen
	}
	return nil
}

func validateHistoricalPlanScale(planID, planScaleCode, targetCode, targetLabel string) error {
	planID = strings.TrimSpace(planID)
	planScaleCode = strings.TrimSpace(planScaleCode)
	targetCode = strings.TrimSpace(targetCode)
	targetLabel = strings.TrimSpace(targetLabel)
	if targetCode == "" {
		return fmt.Errorf("historical target code is required for plan %s", planID)
	}
	if !strings.EqualFold(planScaleCode, targetCode) {
		return fmt.Errorf("historical plan %s scale %s does not match %s %s", planID, planScaleCode, targetLabel, targetCode)
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
		TesteeCreatedAt: &base, EntryResolvedAt: &resolved, EntryIntakeAt: &intake, EnrollmentJoinedAt: &enrolled,
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
	missing := make([]string, 0)
	location, _ := time.LoadLocation(historicalTimezone)
	from, to, rangeErr := parseHistoricalDateRange(manifest.From, manifest.To, location)
	if rangeErr != nil {
		return HistoricalVerification{}, rangeErr
	}
	var stateStore *historicalStateStore
	if _, statErr := os.Stat(historicalStateDBPath(stateDir, batchID)); statErr == nil {
		stateStore, err = openHistoricalStateStore(stateDir, batchID, true)
		if err != nil {
			return HistoricalVerification{}, err
		}
		defer func() { _ = stateStore.Close() }()
	}
	if stateStore != nil {
		if verifyErr := verifyHistoricalBatchWithStore(stateStore, &manifest, from, to, allStages); verifyErr != nil {
			missing = append(missing, verifyErr.Error())
		}
	} else {
		for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
			expectedCount := manifest.DailyCounts[day.Format("2006-01-02")]
			verifyErr := verifyHistoricalDay(stateDir, &manifest, day, expectedCount, allStages)
			if verifyErr != nil {
				missing = append(missing, day.Format("2006-01-02")+":"+verifyErr.Error())
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

func buildHistoricalDaySnapshot(
	ctx context.Context,
	deps *Dependencies,
	cfg DailySimulationConfig,
	manifest *HistoricalManifest,
	day time.Time,
	expectedCount int,
	stateDir string,
	store *historicalStateStore,
	stageReadWorkers int,
	probeServer bool,
) (*HistoricalDaySnapshot, error) {
	if deps == nil || deps.APIClient == nil || manifest == nil {
		return nil, fmt.Errorf("historical day snapshot dependencies are required")
	}
	scenarios, err := resolveDailySimulationScenariosForRun(ctx, deps, cfg, day)
	if err != nil {
		return nil, err
	}
	if len(scenarios) == 0 {
		return nil, fmt.Errorf("historical day snapshot resolved zero scenarios")
	}
	additionalTargets, err := resolveDailySimulationAdditionalTargetsForRun(ctx, deps, cfg)
	if err != nil {
		return nil, err
	}
	snapshot := &HistoricalDaySnapshot{
		BusinessDate: day.Format("2006-01-02"), Expected: make(map[string]HistoricalScenarioManifest, expectedCount),
		Scenarios: make(map[string]HistoricalScenarioSnapshot),
	}
	for index := 0; index < expectedCount; index++ {
		profile := namespaceHistoricalProfile(manifest.BatchID, buildDailySimulationProfile(cfg, day, index))
		scenario := scenarios[index%len(scenarios)]
		historical := buildHistoricalScenarioContext(manifest.BatchID, uint64(manifest.OrgID), cfg, profile, scenario)
		journey := resolveDailySimulationJourneyTarget(cfg, day, index)
		targetKey := strings.Join([]string{scenario.Target.TargetType, scenario.Target.TargetCode}, "/")
		planID := selectDailySimulationPlanID(cfg, day, index)
		record := manifest.Scenarios[historical.ScenarioID]
		if strings.TrimSpace(record.ScenarioID) == "" {
			record = HistoricalScenarioManifest{
				ScenarioID: historical.ScenarioID, BusinessDate: snapshot.BusinessDate, Journey: string(journey), TargetKey: targetKey,
				EntryID: scenario.Entry.ID, PlanID: planID,
			}
		}
		if record.PlanID == "" {
			record.PlanID = planID
		}
		if record.BusinessDate != snapshot.BusinessDate || record.Journey != string(journey) || record.TargetKey != targetKey || record.EntryID != scenario.Entry.ID || record.PlanID != planID {
			return nil, fmt.Errorf("historical parent scenario identity conflict for %s", historical.ScenarioID)
		}
		if journey == dailySimulationJourneySubmitAnswer {
			for _, target := range selectDailySimulationAdditionalTargetsForTestee(additionalTargets, cfg, day, index) {
				additionalID := fmt.Sprintf("%s/%d/%s/%s", snapshot.BusinessDate, index, dailySimulationJourneySubmitAnswer, target.TargetCode)
				targetKey := strings.Join([]string{target.TargetType, target.TargetCode}, "/")
				for _, existing := range record.AdditionalScenarios {
					if existing.ScenarioID == additionalID && existing.TargetKey != targetKey {
						return nil, fmt.Errorf("historical additional scenario identity conflict for %s", additionalID)
					}
				}
				record.AdditionalScenarios = appendHistoricalAdditionalScenario(record.AdditionalScenarios, HistoricalAdditionalScenarioManifest{
					ScenarioID: additionalID, TargetKey: targetKey,
				})
			}
		}
		manifest.Scenarios[historical.ScenarioID] = record
		snapshot.Expected[historical.ScenarioID] = record
	}

	scenarioIDs := make(map[string]struct{})
	for scenarioID, scenario := range snapshot.Expected {
		scenarioIDs[scenarioID] = struct{}{}
		for _, childID := range scenario.ChildScenarioIDs {
			scenarioIDs[childID] = struct{}{}
		}
		for _, recovery := range scenario.PlanTaskRecoveries {
			scenarioIDs[recovery.ScenarioID] = struct{}{}
		}
		for _, additional := range scenario.AdditionalScenarios {
			scenarioIDs[additional.ScenarioID] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(scenarioIDs))
	for scenarioID := range scenarioIDs {
		ordered = append(ordered, scenarioID)
	}
	sort.Strings(ordered)
	serverResponses := make(map[string]HistoricalStageBatchResponse, len(ordered))
	if probeServer {
		serverResponses, err = loadHistoricalScenarioStageResponses(ctx, deps.APIClient, manifest.BatchID, ordered, stageReadWorkers)
		if err != nil {
			return nil, err
		}
	}
	for _, scenarioID := range ordered {
		var local map[string]HistoricalLocalStageRecord
		var err error
		if store != nil {
			local, err = store.loadLocalStages(scenarioID)
		} else {
			local, err = loadHistoricalLocalScenarioStages(stateDir, manifest.BatchID, day, scenarioID)
		}
		if err != nil {
			return nil, err
		}
		for stage, record := range local {
			if record.ScenarioID != scenarioID || record.Stage != stage || record.Status != "completed" || strings.TrimSpace(record.PayloadHash) == "" {
				return nil, fmt.Errorf("historical snapshot scenario %s has invalid local stage %s", scenarioID, stage)
			}
		}
		response := serverResponses[scenarioID]
		server := make(map[string]HistoricalStageRecord, len(response.Stages))
		for _, record := range response.Stages {
			stage := strings.TrimSpace(record.Stage)
			if stage == "" {
				return nil, fmt.Errorf("historical snapshot scenario %s has empty server stage", scenarioID)
			}
			if _, exists := server[stage]; exists {
				return nil, fmt.Errorf("historical snapshot scenario %s has duplicate server stage %s", scenarioID, stage)
			}
			if record.ScenarioID != scenarioID || record.BatchID != manifest.BatchID || !strings.EqualFold(strings.TrimSpace(record.Status), "completed") || strings.TrimSpace(record.ResourceID) == "" || strings.TrimSpace(record.PayloadHash) == "" || record.BusinessAt.IsZero() {
				return nil, fmt.Errorf("historical snapshot scenario %s has invalid completed server stage %s", scenarioID, stage)
			}
			server[stage] = record
		}
		scenario := manifest.Scenarios[scenarioID]
		parent := scenario
		if strings.TrimSpace(scenario.ScenarioID) == "" {
			for _, candidate := range snapshot.Expected {
				if historicalScenarioContains(candidate, scenarioID) {
					parent = candidate
					scenario = candidate
					scenario.ScenarioID = scenarioID
					scenario.BusinessDate = scenarioDateFromID(scenarioID)
					break
				}
			}
		}
		if err := validateHistoricalScenarioStageSet(manifest, parent, scenarioID, server); err != nil {
			return nil, err
		}
		snapshot.Scenarios[scenarioID] = HistoricalScenarioSnapshot{Scenario: scenario, Local: local, Server: server}
	}
	allStages := make([]HistoricalStageRecord, 0)
	for _, scenario := range snapshot.Scenarios {
		for _, record := range scenario.Server {
			allStages = append(allStages, record)
		}
	}
	mergeHistoricalStageResources(manifest, allStages)
	return snapshot, nil
}

func loadHistoricalScenarioStageResponses(
	ctx context.Context,
	client *APIClient,
	batchID string,
	scenarioIDs []string,
	workers int,
) (map[string]HistoricalStageBatchResponse, error) {
	if client == nil {
		return nil, fmt.Errorf("historical stage API client is required")
	}
	if workers <= 0 {
		workers = 1
	}
	responses := make([]HistoricalStageBatchResponse, len(scenarioIDs))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(workers)
	for index, rawScenarioID := range scenarioIDs {
		index, scenarioID := index, strings.TrimSpace(rawScenarioID)
		group.Go(func() error {
			response, err := client.ListHistoricalScenarioStages(groupCtx, batchID, scenarioID)
			if err != nil {
				return fmt.Errorf("load historical snapshot scenario %s: %w", scenarioID, err)
			}
			if response == nil {
				return fmt.Errorf("load historical snapshot scenario %s returned no response", scenarioID)
			}
			responses[index] = *response
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	result := make(map[string]HistoricalStageBatchResponse, len(scenarioIDs))
	for index, scenarioID := range scenarioIDs {
		result[scenarioID] = responses[index]
	}
	return result, nil
}

func validateHistoricalScenarioStageSet(
	manifest *HistoricalManifest,
	parent HistoricalScenarioManifest,
	scenarioID string,
	records map[string]HistoricalStageRecord,
) error {
	if manifest == nil {
		return fmt.Errorf("historical manifest is required")
	}
	location, err := time.LoadLocation(historicalTimezone)
	if err != nil {
		return err
	}
	businessDay, err := time.ParseInLocation("2006-01-02", scenarioDateFromID(scenarioID), location)
	if err != nil {
		return fmt.Errorf("historical scenario %s has invalid business date: %w", scenarioID, err)
	}
	dayEnd := businessDay.AddDate(0, 0, 1)
	expectedResourceTypes := map[string]string{
		"entry_resolve":        "assessment_entry",
		"entry_intake":         "testee",
		"plan_enrollment":      "plan_enrollment",
		"task_open":            "plan_task",
		"task_complete":        "plan_task",
		"answersheet_submit":   "answer_sheet",
		"assessment_created":   "assessment",
		"assessment_submitted": "assessment",
		"outcome_committed":    "evaluation_outcome",
		"report_generated":     "interpretation_report",
	}
	expectedTaskID := ""
	for _, recovery := range parent.PlanTaskRecoveries {
		if recovery.ScenarioID == scenarioID {
			expectedTaskID = strings.TrimSpace(recovery.TaskID)
			break
		}
	}
	if expectedTaskID == "" {
		expectedTaskID = historicalTaskIDForScenario(parent, scenarioID)
	}
	for stage, record := range records {
		resourceType, supported := expectedResourceTypes[stage]
		if !supported {
			return fmt.Errorf("historical scenario %s has unsupported server stage %s", scenarioID, stage)
		}
		if record.OrgID != uint64(manifest.OrgID) || record.ResourceType != resourceType {
			return fmt.Errorf("historical scenario %s stage %s has org/resource type conflict", scenarioID, stage)
		}
		businessAt := record.BusinessAt.In(location)
		if businessAt.Before(businessDay) || !businessAt.Before(dayEnd) {
			return fmt.Errorf("historical scenario %s stage %s business_at %s is outside its business day", scenarioID, stage, record.BusinessAt.Format(time.RFC3339Nano))
		}
		if len(record.PayloadJSON) == 0 || !json.Valid(record.PayloadJSON) {
			return fmt.Errorf("historical scenario %s stage %s has invalid payload", scenarioID, stage)
		}
		if err := validateHistoricalStagePayload(parent, scenarioID, expectedTaskID, stage, record, records); err != nil {
			return err
		}
	}
	return nil
}

func validateHistoricalStagePayload(
	parent HistoricalScenarioManifest,
	scenarioID, expectedTaskID, stage string,
	record HistoricalStageRecord,
	records map[string]HistoricalStageRecord,
) error {
	payload := make(map[string]any)
	decoder := json.NewDecoder(strings.NewReader(string(record.PayloadJSON)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("decode historical scenario %s stage %s payload: %w", scenarioID, stage, err)
	}
	requireValue := func(key, expected string) error {
		value := strings.TrimSpace(fmt.Sprint(payload[key]))
		if expected == "" {
			if value != "" && value != "<nil>" {
				return fmt.Errorf("historical scenario %s stage %s payload %s=%s must be empty", scenarioID, stage, key, value)
			}
			return nil
		}
		if value != expected {
			return fmt.Errorf("historical scenario %s stage %s payload %s=%s want=%s", scenarioID, stage, key, value, expected)
		}
		return nil
	}
	switch stage {
	case "entry_resolve":
		return requireValue("entry_id", record.ResourceID)
	case "entry_intake":
		if err := requireValue("testee_id", record.ResourceID); err != nil {
			return err
		}
		return requireValue("entry_id", parent.EntryID)
	case "plan_enrollment":
		if err := requireValue("enrollment_id", record.ResourceID); err != nil {
			return err
		}
		return requireValue("plan_id", parent.PlanID)
	case "task_open", "task_complete":
		if expectedTaskID == "" {
			return fmt.Errorf("historical scenario %s stage %s has no discovered plan task", scenarioID, stage)
		}
		return requireValue("task_id", expectedTaskID)
	case "answersheet_submit":
		if value := payload["answersheet_id"]; fmt.Sprint(value) != record.ResourceID {
			return fmt.Errorf("historical scenario %s answersheet payload conflicts with resource %s", scenarioID, record.ResourceID)
		}
		return requireValue("task_id", expectedTaskID)
	case "assessment_created":
		if answer, ok := records["answersheet_submit"]; ok {
			return requireValue("answersheet_id", answer.ResourceID)
		}
	case "assessment_submitted":
		return requireValue("assessment_id", record.ResourceID)
	case "outcome_committed":
		if err := requireValue("outcome_id", record.ResourceID); err != nil {
			return err
		}
		if assessment, ok := records["assessment_created"]; ok {
			return requireValue("assessment_id", assessment.ResourceID)
		}
	case "report_generated":
		if err := requireValue("report_id", record.ResourceID); err != nil {
			return err
		}
		for _, key := range []string{"generation_id", "run_id"} {
			if value := strings.TrimSpace(fmt.Sprint(payload[key])); value == "" || value == "<nil>" {
				return fmt.Errorf("historical scenario %s report payload has empty %s", scenarioID, key)
			}
		}
	}
	return nil
}

func appendHistoricalAdditionalScenario(values []HistoricalAdditionalScenarioManifest, candidate HistoricalAdditionalScenarioManifest) []HistoricalAdditionalScenarioManifest {
	for _, existing := range values {
		if existing.ScenarioID == candidate.ScenarioID {
			if existing.TargetKey != candidate.TargetKey {
				return values
			}
			return values
		}
	}
	return append(values, candidate)
}

func historicalScenarioContains(parent HistoricalScenarioManifest, scenarioID string) bool {
	for _, child := range parent.ChildScenarioIDs {
		if child == scenarioID {
			return true
		}
	}
	for _, recovery := range parent.PlanTaskRecoveries {
		if recovery.ScenarioID == scenarioID {
			return true
		}
	}
	for _, additional := range parent.AdditionalScenarios {
		if additional.ScenarioID == scenarioID {
			return true
		}
	}
	return false
}

func scenarioDateFromID(scenarioID string) string {
	date, _, _ := strings.Cut(strings.TrimSpace(scenarioID), "/")
	return date
}

func completedHistoricalServerStage(ctx context.Context, scenarioID, stage string) (HistoricalStageRecord, bool, error) {
	snapshot, ok := historicalScenarioSnapshot(ctx, scenarioID)
	if !ok {
		return HistoricalStageRecord{}, false, nil
	}
	record, ok := snapshot.Server[strings.TrimSpace(stage)]
	if !ok {
		return HistoricalStageRecord{}, false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(record.Status), "completed") || strings.TrimSpace(record.ResourceID) == "" || record.BusinessAt.IsZero() || strings.TrimSpace(record.PayloadHash) == "" {
		return HistoricalStageRecord{}, false, fmt.Errorf("scenario %s server stage %s is not a valid completion fact", scenarioID, stage)
	}
	return record, true, nil
}

func loadHistoricalSeedStagesForDayConcurrent(ctx context.Context, client *APIClient, manifest HistoricalManifest, day time.Time, workers int) ([]HistoricalStageRecord, error) {
	if client == nil {
		return nil, fmt.Errorf("historical stage API client is required")
	}
	dayKey := day.Format("2006-01-02")
	scenarioIDs := make(map[string]struct{})
	for scenarioID, scenario := range manifest.Scenarios {
		if scenario.BusinessDate != dayKey {
			continue
		}
		if len(expectedServerStages(manifest, scenario)) > 0 {
			scenarioIDs[scenarioID] = struct{}{}
		}
		for _, childID := range scenario.ChildScenarioIDs {
			if len(expectedChildServerStages(manifest, scenario)) > 0 {
				scenarioIDs[childID] = struct{}{}
			}
		}
		for _, additional := range scenario.AdditionalScenarios {
			if len(expectedAdditionalServerStages(manifest, additional)) > 0 {
				scenarioIDs[additional.ScenarioID] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(scenarioIDs))
	for scenarioID := range scenarioIDs {
		ordered = append(ordered, scenarioID)
	}
	sort.Strings(ordered)
	responses, err := loadHistoricalScenarioStageResponses(ctx, client, manifest.BatchID, ordered, workers)
	if err != nil {
		return nil, err
	}
	all := make([]HistoricalStageRecord, 0)
	for _, scenarioID := range ordered {
		all = append(all, responses[scenarioID].Stages...)
	}
	return all, nil
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
	stages := []string{"task_open", "answersheet_submit"}
	if target, ok := manifest.Targets[scenario.TargetKey]; ok && target.RequiresAssessment {
		stages = append(stages, "assessment_created", "assessment_submitted", "task_complete", "outcome_committed", "report_generated")
	} else {
		stages = append(stages, "task_complete")
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
	record, err := buildHistoricalLocalStageRecord(ledger.Records[string(stage)], manifest, profile, scenario, historical, stage, outcome, target)
	if err != nil {
		return err
	}
	ledger.Records[string(stage)] = record
	return saveSecureJSON(historicalLocalScenarioStagePath(stateDir, manifest.BatchID, profile.RunDate, historical.ScenarioID), &ledger)
}

func recordHistoricalLocalStageInStore(
	store *historicalStateStore,
	manifest *HistoricalManifest,
	profile dailySimulationProfile,
	scenario dailySimulationScenario,
	historical historicalseed.Context,
	stage dailySimulationJourneyStage,
	outcome dailySimulationOutcome,
	target *dailySimulationResolvedTarget,
) error {
	if store == nil {
		return fmt.Errorf("historical state store is required")
	}
	records, err := store.loadLocalStages(historical.ScenarioID)
	if err != nil {
		return err
	}
	record, err := buildHistoricalLocalStageRecord(records[string(stage)], manifest, profile, scenario, historical, stage, outcome, target)
	if err != nil {
		return err
	}
	return store.putLocalStage(record)
}

func buildHistoricalLocalStageRecord(
	record HistoricalLocalStageRecord,
	manifest *HistoricalManifest,
	profile dailySimulationProfile,
	scenario dailySimulationScenario,
	historical historicalseed.Context,
	stage dailySimulationJourneyStage,
	outcome dailySimulationOutcome,
	target *dailySimulationResolvedTarget,
) (HistoricalLocalStageRecord, error) {
	if manifest == nil {
		return HistoricalLocalStageRecord{}, fmt.Errorf("historical manifest is required")
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
		return HistoricalLocalStageRecord{}, err
	}
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	if record.PayloadHash != "" && record.PayloadHash != hash {
		return HistoricalLocalStageRecord{}, fmt.Errorf("historical local stage payload conflict: scenario=%s stage=%s", historical.ScenarioID, stage)
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
	return record, nil
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
		GuardianUserID: firstHistoricalValue(outcome.GuardianUserID, existing.GuardianUserID), IAMProfileID: firstHistoricalValue(outcome.IAMProfileID, existing.IAMProfileID), IAMProfileLinkID: firstHistoricalValue(outcome.IAMProfileLinkID, existing.IAMProfileLinkID), TesteeID: firstHistoricalValue(outcome.TesteeID, existing.TesteeID),
		TesteeCreated: outcome.TesteeCreated || existing.TesteeCreated,
		EntryID:       firstHistoricalValue(scenario.Entry.ID, existing.EntryID), PlanID: firstHistoricalValue(outcome.PlanID, existing.PlanID), EnrollmentID: firstHistoricalValue(outcome.EnrollmentID, existing.EnrollmentID), TaskIDs: appendUniqueStrings(append([]string(nil), existing.TaskIDs...), outcome.TaskIDs),
		CompletedTaskIDs: appendUniqueStrings(append([]string(nil), existing.CompletedTaskIDs...), outcome.CompletedTaskIDs), ChildScenarioIDs: appendUniqueStrings(append([]string(nil), existing.ChildScenarioIDs...), outcome.ChildScenarioIDs),
		AdditionalScenarios: appendHistoricalAdditionalScenarios(existing.AdditionalScenarios, outcome.AdditionalScenarios),
		PlanTaskRecoveries:  append([]HistoricalPlanTaskRecovery(nil), existing.PlanTaskRecoveries...),
		AnswerSheetID:       firstHistoricalValue(outcome.AnswerSheetID, existing.AnswerSheetID), AnswerSheetIDs: appendUniqueStrings(append([]string(nil), existing.AnswerSheetIDs...), outcome.AnswerSheetIDs),
		AssessmentID: firstHistoricalValue(outcome.AssessmentID, existing.AssessmentID), AssessmentIDs: appendUniqueStrings(append([]string(nil), existing.AssessmentIDs...), outcome.AssessmentIDs),
		OutcomeID: firstHistoricalValue(existing.OutcomeID, ""), OutcomeIDs: append([]string(nil), existing.OutcomeIDs...),
		ReportID: firstHistoricalValue(existing.ReportID, ""), ReportIDs: append([]string(nil), existing.ReportIDs...),
		GenerationID: existing.GenerationID, ReportRunID: existing.ReportRunID, Terminal: existing.Terminal,
	}
}

func appendHistoricalAdditionalScenarios(values, candidates []HistoricalAdditionalScenarioManifest) []HistoricalAdditionalScenarioManifest {
	result := append([]HistoricalAdditionalScenarioManifest(nil), values...)
	for _, candidate := range candidates {
		found := false
		for _, existing := range result {
			if existing.ScenarioID == candidate.ScenarioID {
				found = true
				break
			}
		}
		if !found {
			result = append(result, candidate)
		}
	}
	return result
}

func verifyHistoricalDay(stateDir string, manifest *HistoricalManifest, day time.Time, expectedCount int, stages []HistoricalStageRecord) error {
	if manifest == nil {
		return fmt.Errorf("historical manifest is required")
	}
	localStages, err := loadAllHistoricalLocalStages(stateDir, manifest.BatchID)
	if err != nil {
		return err
	}
	return verifyHistoricalDayRecords(manifest, day, expectedCount, stages, localStages)
}

func verifyHistoricalDayWithStore(store *historicalStateStore, manifest *HistoricalManifest, day time.Time, expectedCount int, stages []HistoricalStageRecord) error {
	if store == nil || manifest == nil {
		return fmt.Errorf("historical state store and manifest are required")
	}
	localStages, err := store.loadAllLocalStages()
	if err != nil {
		return err
	}
	return verifyHistoricalDayRecords(manifest, day, expectedCount, stages, localStages)
}

func verifyHistoricalDayRecords(manifest *HistoricalManifest, day time.Time, expectedCount int, stages []HistoricalStageRecord, localStages map[string]HistoricalLocalStageRecord) error {
	dayKey := day.Format("2006-01-02")
	serverByScenario := indexHistoricalServerStages(stages)
	parentIDs := make([]string, 0, expectedCount)
	for scenarioID, scenario := range manifest.Scenarios {
		if scenario.BusinessDate == dayKey {
			parentIDs = append(parentIDs, scenarioID)
		}
	}
	return verifyHistoricalDayIndexed(manifest, dayKey, expectedCount, parentIDs, serverByScenario, localStages)
}

func verifyHistoricalDayIndexed(
	manifest *HistoricalManifest,
	dayKey string,
	expectedCount int,
	parentIDs []string,
	serverByScenario map[string]map[string]HistoricalStageRecord,
	localStages map[string]HistoricalLocalStageRecord,
) error {
	if len(parentIDs) != expectedCount {
		return fmt.Errorf("recorded parent scenarios=%d want=%d", len(parentIDs), expectedCount)
	}
	for _, scenarioID := range parentIDs {
		scenario := manifest.Scenarios[scenarioID]
		if err := validateHistoricalPlanRecoveries(scenario); err != nil {
			return err
		}
		if err := requireHistoricalLocalStages(localStages, scenarioID, expectedLocalStages(*manifest, scenario)); err != nil {
			return err
		}
		parentExpected := expectedServerStages(*manifest, scenario)
		if err := requireHistoricalServerStages(serverByScenario, scenarioID, parentExpected); err != nil {
			return err
		}
		if err := requireExactHistoricalServerStageSet(serverByScenario[scenarioID], scenarioID, parentExpected); err != nil {
			return err
		}
		if err := validateHistoricalScenarioStageSet(manifest, scenario, scenarioID, serverByScenario[scenarioID]); err != nil {
			return err
		}
		if err := validateHistoricalScenarioResources(scenario, scenarioID, localStages, serverByScenario[scenarioID], ""); err != nil {
			return err
		}
		for _, childID := range scenario.ChildScenarioIDs {
			if err := requireHistoricalLocalStages(localStages, childID, expectedChildLocalStages(*manifest, scenario.TargetKey)); err != nil {
				return err
			}
			childExpected := expectedChildServerStages(*manifest, scenario)
			if err := requireHistoricalServerStages(serverByScenario, childID, childExpected); err != nil {
				return err
			}
			if err := requireExactHistoricalServerStageSet(serverByScenario[childID], childID, childExpected); err != nil {
				return err
			}
			if err := validateHistoricalScenarioStageSet(manifest, scenario, childID, serverByScenario[childID]); err != nil {
				return err
			}
			if err := validateHistoricalScenarioResources(scenario, childID, localStages, serverByScenario[childID], historicalTaskIDForScenario(scenario, childID)); err != nil {
				return err
			}
		}
		for _, additional := range scenario.AdditionalScenarios {
			localExpected := make([]string, 0)
			for _, stage := range expectedChildLocalStages(*manifest, additional.TargetKey) {
				if stage != "task_open" && stage != "task_complete" {
					localExpected = append(localExpected, stage)
				}
			}
			if err := requireHistoricalLocalStages(localStages, additional.ScenarioID, localExpected); err != nil {
				return err
			}
			additionalExpected := expectedAdditionalServerStages(*manifest, additional)
			if err := requireHistoricalServerStages(serverByScenario, additional.ScenarioID, additionalExpected); err != nil {
				return err
			}
			if err := requireExactHistoricalServerStageSet(serverByScenario[additional.ScenarioID], additional.ScenarioID, additionalExpected); err != nil {
				return err
			}
			if err := validateHistoricalScenarioStageSet(manifest, scenario, additional.ScenarioID, serverByScenario[additional.ScenarioID]); err != nil {
				return err
			}
			if err := validateHistoricalScenarioResources(scenario, additional.ScenarioID, localStages, serverByScenario[additional.ScenarioID], ""); err != nil {
				return err
			}
		}
		scenario.Terminal = "verified"
		manifest.Scenarios[scenarioID] = scenario
	}
	manifest.DailyCounts[dayKey] = expectedCount
	return nil
}

func indexHistoricalServerStages(stages []HistoricalStageRecord) map[string]map[string]HistoricalStageRecord {
	result := make(map[string]map[string]HistoricalStageRecord)
	for _, record := range stages {
		if result[record.ScenarioID] == nil {
			result[record.ScenarioID] = make(map[string]HistoricalStageRecord)
		}
		result[record.ScenarioID][record.Stage] = record
	}
	return result
}

func verifyHistoricalBatchWithStore(
	store *historicalStateStore,
	manifest *HistoricalManifest,
	from, to time.Time,
	stages []HistoricalStageRecord,
) error {
	if store == nil || manifest == nil {
		return fmt.Errorf("historical state store and manifest are required")
	}
	localStages, err := store.loadAllLocalStages()
	if err != nil {
		return err
	}
	serverByScenario := indexHistoricalServerStages(stages)
	parentsByDay := make(map[string][]string)
	for scenarioID, scenario := range manifest.Scenarios {
		parentsByDay[scenario.BusinessDate] = append(parentsByDay[scenario.BusinessDate], scenarioID)
	}
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		dayKey := day.Format("2006-01-02")
		parentIDs := parentsByDay[dayKey]
		sort.Strings(parentIDs)
		expectedCount, exists := manifest.DailyCounts[dayKey]
		if !exists || expectedCount <= 0 {
			return fmt.Errorf("%s: daily checkpoint count is missing", dayKey)
		}
		if err := verifyHistoricalDayIndexed(manifest, dayKey, expectedCount, parentIDs, serverByScenario, localStages); err != nil {
			return fmt.Errorf("%s: %w", dayKey, err)
		}
	}
	return nil
}

func validateHistoricalPlanRecoveries(parent HistoricalScenarioManifest) error {
	byTask := make(map[string]string, len(parent.PlanTaskRecoveries))
	byScenario := make(map[string]string, len(parent.PlanTaskRecoveries))
	location, _ := time.LoadLocation(historicalTimezone)
	for _, recovery := range parent.PlanTaskRecoveries {
		taskID := strings.TrimSpace(recovery.TaskID)
		scenarioID := strings.TrimSpace(recovery.ScenarioID)
		if taskID == "" || scenarioID == "" || strings.TrimSpace(recovery.TargetKey) != strings.TrimSpace(parent.TargetKey) {
			return fmt.Errorf("scenario %s has invalid plan task recovery %+v", parent.ScenarioID, recovery)
		}
		if existing, ok := byTask[taskID]; ok && existing != scenarioID {
			return fmt.Errorf("scenario %s maps task %s to multiple child scenarios", parent.ScenarioID, taskID)
		}
		if existing, ok := byScenario[scenarioID]; ok && existing != taskID {
			return fmt.Errorf("scenario %s maps child %s to multiple tasks", parent.ScenarioID, scenarioID)
		}
		plannedAt, err := parseHistoricalTaskPlannedAt(recovery.PlannedAt, location)
		if err != nil || plannedAt.In(location).Format("2006-01-02") != scenarioDateFromID(scenarioID) {
			return fmt.Errorf("scenario %s has invalid planned_at for child %s", parent.ScenarioID, scenarioID)
		}
		if !containsHistoricalString(parent.TaskIDs, taskID) || !containsHistoricalString(parent.ChildScenarioIDs, scenarioID) {
			return fmt.Errorf("scenario %s plan task recovery %s is missing from manifest resources", parent.ScenarioID, taskID)
		}
		byTask[taskID], byScenario[scenarioID] = scenarioID, taskID
	}
	return nil
}

func requireExactHistoricalServerStageSet(records map[string]HistoricalStageRecord, scenarioID string, expected []string) error {
	wanted := make(map[string]struct{}, len(expected))
	for _, stage := range expected {
		wanted[stage] = struct{}{}
	}
	for stage := range records {
		if _, ok := wanted[stage]; !ok {
			return fmt.Errorf("scenario %s has unexpected completed server stage %s", scenarioID, stage)
		}
	}
	return nil
}

func historicalTaskIDForScenario(parent HistoricalScenarioManifest, scenarioID string) string {
	for _, recovery := range parent.PlanTaskRecoveries {
		if recovery.ScenarioID == scenarioID {
			return strings.TrimSpace(recovery.TaskID)
		}
	}
	_, suffix, found := strings.Cut(strings.TrimSpace(scenarioID), "/")
	for found {
		var next string
		suffix, next, found = strings.Cut(suffix, "/")
		if found {
			suffix = next
		}
	}
	if containsHistoricalString(parent.TaskIDs, suffix) {
		return suffix
	}
	return ""
}

func validateHistoricalScenarioResources(
	parent HistoricalScenarioManifest,
	scenarioID string,
	local map[string]HistoricalLocalStageRecord,
	server map[string]HistoricalStageRecord,
	expectedTaskID string,
) error {
	requireServerID := func(stage, expected string) error {
		record, exists := server[stage]
		if !exists || expected == "" {
			return nil
		}
		if record.ResourceID != expected {
			return fmt.Errorf("scenario %s stage %s resource %s conflicts with manifest %s", scenarioID, stage, record.ResourceID, expected)
		}
		return nil
	}
	if scenarioID == parent.ScenarioID {
		for stage, expected := range map[string]string{
			"entry_resolve": parent.EntryID, "entry_intake": parent.TesteeID, "plan_enrollment": parent.EnrollmentID,
		} {
			if err := requireServerID(stage, expected); err != nil {
				return err
			}
		}
	}
	if expectedTaskID != "" {
		if err := requireServerID("task_open", expectedTaskID); err != nil {
			return err
		}
		if err := requireServerID("task_complete", expectedTaskID); err != nil {
			return err
		}
		if !containsHistoricalString(parent.TaskIDs, expectedTaskID) || !containsHistoricalString(parent.CompletedTaskIDs, expectedTaskID) {
			return fmt.Errorf("scenario %s task %s is not fully represented in manifest", scenarioID, expectedTaskID)
		}
	}
	resourceLists := map[string][]string{
		"answersheet_submit":   parent.AnswerSheetIDs,
		"assessment_created":   parent.AssessmentIDs,
		"assessment_submitted": parent.AssessmentIDs,
		"outcome_committed":    parent.OutcomeIDs,
		"report_generated":     parent.ReportIDs,
	}
	for stage, ids := range resourceLists {
		if record, exists := server[stage]; exists && !containsHistoricalString(ids, record.ResourceID) {
			return fmt.Errorf("scenario %s stage %s resource %s is missing from manifest", scenarioID, stage, record.ResourceID)
		}
	}
	for stage, record := range server {
		localRecord, exists := local[scenarioID+"\x00"+stage]
		if !exists {
			continue
		}
		switch stage {
		case "entry_intake":
			if localRecord.TesteeID != record.ResourceID {
				return fmt.Errorf("scenario %s local testee %s conflicts with server %s", scenarioID, localRecord.TesteeID, record.ResourceID)
			}
		case "plan_enrollment":
			if localRecord.EnrollmentID != record.ResourceID {
				return fmt.Errorf("scenario %s local enrollment %s conflicts with server %s", scenarioID, localRecord.EnrollmentID, record.ResourceID)
			}
		case "task_open", "task_complete":
			if !containsHistoricalString(localRecord.TaskIDs, record.ResourceID) {
				return fmt.Errorf("scenario %s local stage %s is missing task %s", scenarioID, stage, record.ResourceID)
			}
		case "answersheet_submit":
			if !containsHistoricalString(localRecord.AnswerSheetIDs, record.ResourceID) {
				return fmt.Errorf("scenario %s local answersheet stage is missing resource %s", scenarioID, record.ResourceID)
			}
		case "assessment_created", "assessment_submitted":
			if !containsHistoricalString(localRecord.AssessmentIDs, record.ResourceID) {
				return fmt.Errorf("scenario %s local assessment stage is missing resource %s", scenarioID, record.ResourceID)
			}
		}
	}
	return nil
}

func containsHistoricalString(values []string, candidate string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(candidate) {
			return true
		}
	}
	return false
}

func requireHistoricalLocalStages(records map[string]HistoricalLocalStageRecord, scenarioID string, expected []string) error {
	for _, stage := range expected {
		record, ok := records[scenarioID+"\x00"+stage]
		if !ok || record.Status != "completed" {
			return fmt.Errorf("scenario %s missing completed local stage %s", scenarioID, stage)
		}
	}
	return nil
}

func requireHistoricalServerStages(records map[string]map[string]HistoricalStageRecord, scenarioID string, expected []string) error {
	ordered := make([]time.Time, 0, len(expected))
	for _, stage := range expected {
		record, ok := records[scenarioID][stage]
		if !ok || !strings.EqualFold(strings.TrimSpace(record.Status), "completed") || strings.TrimSpace(record.ResourceID) == "" || record.BusinessAt.IsZero() {
			return fmt.Errorf("scenario %s missing completed server stage %s", scenarioID, stage)
		}
		ordered = append(ordered, record.BusinessAt)
	}
	for index := 1; index < len(ordered); index++ {
		if ordered[index].Before(ordered[index-1]) {
			return fmt.Errorf("scenario %s server stage timeline is out of order", scenarioID)
		}
	}
	return nil
}

func VerifyHistoricalBackfill(stateDir, batchID string) (HistoricalVerification, error) {
	var checkpoint HistoricalCheckpoint
	var manifest HistoricalManifest
	var err error
	if _, err := os.Stat(historicalStateDBPath(stateDir, batchID)); err == nil {
		store, openErr := openHistoricalStateStore(stateDir, batchID, true)
		if openErr != nil {
			return HistoricalVerification{}, openErr
		}
		defer func() { _ = store.Close() }()
		checkpoint, err = store.loadCheckpoint()
		if err != nil {
			return HistoricalVerification{}, err
		}
		manifest, err = store.loadManifest()
		if err != nil {
			return HistoricalVerification{}, err
		}
	} else {
		checkpointPath, manifestPath := historicalPaths(stateDir, batchID)
		if exists, loadErr := loadSecureJSON(checkpointPath, &checkpoint); loadErr != nil || !exists {
			if loadErr != nil {
				return HistoricalVerification{}, loadErr
			}
			return HistoricalVerification{}, fmt.Errorf("historical checkpoint not found for batch %s", batchID)
		}
		if exists, loadErr := loadSecureJSON(manifestPath, &manifest); loadErr != nil || !exists {
			if loadErr != nil {
				return HistoricalVerification{}, loadErr
			}
			return HistoricalVerification{}, fmt.Errorf("historical manifest not found for batch %s", batchID)
		}
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
	allTerminalsVerified := true
	for _, scenario := range manifest.Scenarios {
		if scenario.Terminal != "verified" {
			allTerminalsVerified = false
			break
		}
	}
	return HistoricalVerification{
		BatchID: batchID, Complete: checkpoint.CompletedThrough == manifest.To && len(manifest.DailyCounts) == expectedDays && len(manifest.Scenarios) == expectedScenarios && allTerminalsVerified,
		CompletedThrough: checkpoint.CompletedThrough, ExpectedDays: expectedDays, RecordedDays: len(manifest.DailyCounts),
		ExpectedScenarios: expectedScenarios, RecordedScenarios: len(manifest.Scenarios),
	}, nil
}

func LoadHistoricalManifest(stateDir, batchID string) (HistoricalManifest, error) {
	if _, err := os.Stat(historicalStateDBPath(stateDir, batchID)); err == nil {
		store, openErr := openHistoricalStateStore(stateDir, batchID, true)
		if openErr != nil {
			return HistoricalManifest{}, openErr
		}
		defer func() { _ = store.Close() }()
		return store.loadManifest()
	}
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
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}
