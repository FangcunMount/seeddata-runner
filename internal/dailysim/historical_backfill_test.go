package dailysim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	"github.com/FangcunMount/seeddata-runner/internal/historicalseed"
)

func TestHistoricalRangeHas573CompleteNaturalDays(t *testing.T) {
	location, err := time.LoadLocation(historicalTimezone)
	if err != nil {
		t.Fatal(err)
	}
	from, to, err := parseHistoricalDateRange("2025-01-01", "2026-07-27", location)
	if err != nil {
		t.Fatal(err)
	}
	if days := int(to.Sub(from).Hours()/24) + 1; days != 573 {
		t.Fatalf("historical range days=%d want=573", days)
	}
}

func TestHistoricalCountAndTimelineAreDeterministicAndBounded(t *testing.T) {
	location, _ := time.LoadLocation(historicalTimezone)
	day := time.Date(2025, 1, 1, 0, 0, 0, 0, location)
	first := DeterministicHistoricalCount("batch", day, 40, 200)
	second := DeterministicHistoricalCount("batch", day, 40, 200)
	if first != second || first < 40 || first > 200 {
		t.Fatalf("count not deterministic/bounded: %d %d", first, second)
	}
	one := BuildHistoricalTimeline("batch", day, 7)
	two := BuildHistoricalTimeline("batch", day, 7)
	if one.ReportGeneratedAt == nil || two.ReportGeneratedAt == nil || !one.ReportGeneratedAt.Equal(*two.ReportGeneratedAt) {
		t.Fatal("timeline is not deterministic")
	}
	for name, at := range map[string]*time.Time{
		"testee": one.TesteeCreatedAt, "resolve": one.EntryResolvedAt, "intake": one.EntryIntakeAt, "enroll": one.EnrollmentJoinedAt,
		"filled": one.AnswerSheetFilledAt, "assessment": one.AssessmentCreatedAt, "outcome": one.EvaluatedAt, "report": one.ReportGeneratedAt,
	} {
		if at == nil || at.In(location).Format("2006-01-02") != "2025-01-01" {
			t.Fatalf("%s escaped business day: %v", name, at)
		}
	}
	if !one.TesteeCreatedAt.Before(*one.EntryResolvedAt) {
		t.Fatalf("testee_created_at=%s must be before entry_resolved_at=%s", one.TesteeCreatedAt, one.EntryResolvedAt)
	}
}

func TestHistoricalTaskTimelineStartsAtPlannedAtAndStaysOnBusinessDay(t *testing.T) {
	location, _ := time.LoadLocation(historicalTimezone)
	plannedAt := time.Date(2025, 1, 1, 23, 30, 0, 0, location)
	timeline, err := BuildHistoricalTaskTimeline("batch", plannedAt, 3)
	if err != nil {
		t.Fatal(err)
	}
	if timeline.TaskOpenedAt == nil || !timeline.TaskOpenedAt.Equal(plannedAt) {
		t.Fatalf("task_opened_at=%v, want %s", timeline.TaskOpenedAt, plannedAt)
	}
	for name, at := range map[string]*time.Time{"filled": timeline.AnswerSheetFilledAt, "assessment": timeline.AssessmentCreatedAt, "outcome": timeline.EvaluatedAt, "report": timeline.ReportGeneratedAt} {
		if at == nil || at.In(location).Format("2006-01-02") != "2025-01-01" {
			t.Fatalf("%s escaped business day: %v", name, at)
		}
	}
	if timeline.AnswerSheetFilledAt.Before(*timeline.TaskOpenedAt) || timeline.TaskCompletedAt.Before(*timeline.AssessmentSubmittedAt) || timeline.EvaluatedAt.Before(*timeline.TaskCompletedAt) || timeline.ReportGeneratedAt.Before(*timeline.EvaluatedAt) {
		t.Fatalf("task timeline ordering is invalid: %+v", timeline)
	}
}

func TestHistoricalIdentityNamespaceIsStableAndBatchSpecific(t *testing.T) {
	location, _ := time.LoadLocation(historicalTimezone)
	profile := dailySimulationProfile{Index: 7, RunDate: time.Date(2025, 1, 1, 0, 0, 0, 0, location), GuardianPhone: "+8619901010007", GuardianEmail: "guardian@example.com"}
	first := namespaceHistoricalProfile("batch-a", profile)
	second := namespaceHistoricalProfile("batch-a", profile)
	other := namespaceHistoricalProfile("batch-b", profile)
	if first != second {
		t.Fatalf("historical identity is not stable: %+v %+v", first, second)
	}
	if first.GuardianPhone == other.GuardianPhone || first.GuardianEmail == other.GuardianEmail {
		t.Fatalf("historical batch namespace did not change identity: first=%+v other=%+v", first, other)
	}
}

func TestFreezeHistoricalPlansRejectsIncompatibleTargetWithoutMutatingManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/plans/compatible":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": PlanResponse{ID: "compatible", ScaleCode: "3adyDE", Status: "active"}})
		case "/api/v1/plans/incompatible":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": PlanResponse{ID: "incompatible", ScaleCode: "yGtSs1", Status: "active"}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	manifest := HistoricalManifest{Plans: make(map[string]HistoricalPlanManifest)}
	client := NewAPIClient(server.URL, "token", log.New(log.NewOptions()))
	err := freezeHistoricalPlans(context.Background(), client, []FlexibleID{"compatible", "incompatible"}, "3adyDE", &manifest)
	if err == nil || !strings.Contains(err.Error(), "plan incompatible scale yGtSs1 does not match historical target 3adyDE") {
		t.Fatalf("expected historical plan target conflict, got %v", err)
	}
	if len(manifest.Plans) != 0 {
		t.Fatalf("failed plan preflight mutated manifest: %+v", manifest.Plans)
	}
}

func TestHistoricalManifestMergesChildTerminalResourcesIntoParent(t *testing.T) {
	parentID := "2025-01-01/7/submit_answer/MODEL"
	childID := "2025-01-08/7/submit_answer/task-1"
	manifest := HistoricalManifest{Scenarios: map[string]HistoricalScenarioManifest{
		parentID: {ScenarioID: parentID, ChildScenarioIDs: []string{childID}},
	}}
	payload, err := json.Marshal(map[string]string{"generation_id": "gen-1", "run_id": "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	mergeHistoricalStageResources(&manifest, []HistoricalStageRecord{
		{ScenarioID: childID, Stage: "answersheet_submit", ResourceID: "answer-1"},
		{ScenarioID: childID, Stage: "outcome_committed", ResourceID: "outcome-1"},
		{ScenarioID: childID, Stage: "report_generated", ResourceID: "report-1", PayloadJSON: payload},
	})
	got := manifest.Scenarios[parentID]
	if len(got.AnswerSheetIDs) != 1 || got.AnswerSheetIDs[0] != "answer-1" ||
		len(got.OutcomeIDs) != 1 || got.OutcomeIDs[0] != "outcome-1" ||
		len(got.ReportIDs) != 1 || got.ReportIDs[0] != "report-1" ||
		got.GenerationID != "gen-1" || got.ReportRunID != "run-1" {
		t.Fatalf("child resources were not merged into parent: %+v", got)
	}
}

func TestHistoricalExpectedStagesSeparateParentAndPlanTaskChild(t *testing.T) {
	manifest := HistoricalManifest{Targets: map[string]HistoricalTargetManifest{
		"scale/MODEL": {RequiresAssessment: true},
	}}
	parent := HistoricalScenarioManifest{
		Journey: string(dailySimulationJourneySubmitAnswer), TargetKey: "scale/MODEL",
		ChildScenarioIDs: []string{"child"},
	}
	if got := expectedServerStages(manifest, parent); !equalStrings(got, []string{"entry_resolve", "entry_intake", "plan_enrollment"}) {
		t.Fatalf("parent expected stages=%v", got)
	}
	if got := expectedChildServerStages(manifest, parent); !equalStrings(got, []string{
		"task_open", "answersheet_submit", "assessment_created", "assessment_submitted", "task_complete", "outcome_committed", "report_generated",
	}) {
		t.Fatalf("child expected stages=%v", got)
	}
}

func TestHistoricalAdditionalScenarioRequiresAnswerTerminalWithoutPlanTask(t *testing.T) {
	manifest := HistoricalManifest{Targets: map[string]HistoricalTargetManifest{
		"scale/EXTRA": {RequiresAssessment: true},
	}}
	additional := HistoricalAdditionalScenarioManifest{ScenarioID: "child", TargetKey: "scale/EXTRA"}
	if got := expectedAdditionalServerStages(manifest, additional); !equalStrings(got, []string{
		"answersheet_submit", "assessment_created", "assessment_submitted", "outcome_committed", "report_generated",
	}) {
		t.Fatalf("additional expected stages=%v", got)
	}
}

func TestHistoricalLocalStageLedgerPreservesCreationOwnershipAcrossRetry(t *testing.T) {
	stateDir := t.TempDir()
	day := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	profile := dailySimulationProfile{RunDate: day, Index: 7}
	target := &dailySimulationResolvedTarget{TargetType: "scale", TargetCode: "MODEL", TargetVersion: "1", QuestionnaireCode: "Q", QuestionnaireVersion: "1"}
	scenario := dailySimulationScenario{Entry: &AssessmentEntryResponse{ID: "entry-1"}, Target: target}
	historical := historicalseed.Context{BatchID: "batch", ScenarioID: "2025-01-01/7/create_testee/MODEL", OrgID: 9, Version: historicalseed.Version1}
	manifest := HistoricalManifest{BatchID: "batch"}
	created := dailySimulationOutcome{JourneyTarget: string(dailySimulationJourneyCreateTestee), GuardianUserID: "user-1", IAMProfileID: "profile-1", TesteeID: "testee-1", UserCreated: true, TesteeCreated: true}
	if err := recordHistoricalLocalStage(stateDir, &manifest, profile, scenario, historical, dailySimulationJourneyStageTesteeProfile, created, target); err != nil {
		t.Fatal(err)
	}
	retry := created
	retry.UserCreated, retry.TesteeCreated = false, false
	if err := recordHistoricalLocalStage(stateDir, &manifest, profile, scenario, historical, dailySimulationJourneyStageTesteeProfile, retry, target); err != nil {
		t.Fatal(err)
	}
	records, err := loadHistoricalLocalScenarioStages(stateDir, "batch", day, historical.ScenarioID)
	if err != nil {
		t.Fatal(err)
	}
	record := records[string(dailySimulationJourneyStageTesteeProfile)]
	if !record.TesteeCreated || record.TesteeID != "testee-1" {
		t.Fatalf("creation ownership was lost on retry: %+v", record)
	}
	info, err := os.Stat(historicalLocalScenarioStagePath(stateDir, "batch", day, historical.ScenarioID))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("stage ledger mode=%o, want 600", info.Mode().Perm())
	}
}

func TestHistoricalResumeRestoresTesteeWithoutCreatedAtLookup(t *testing.T) {
	stateDir := t.TempDir()
	day := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	profile := dailySimulationProfile{RunDate: day, Index: 7, ChildName: "child", ChildGender: 2, ChildDOB: "2018-02-03"}
	target := &dailySimulationResolvedTarget{TargetType: "scale", TargetCode: "MODEL", TargetVersion: "1", QuestionnaireCode: "Q", QuestionnaireVersion: "1"}
	scenario := dailySimulationScenario{Entry: &AssessmentEntryResponse{ID: "entry-1"}, Target: target}
	historical := historicalseed.Context{BatchID: "batch", ScenarioID: "2025-01-01/7/submit_answer/MODEL", OrgID: 9, Version: historicalseed.Version1}
	manifest := HistoricalManifest{BatchID: "batch"}
	outcome := dailySimulationOutcome{JourneyTarget: string(dailySimulationJourneySubmitAnswer), TesteeID: "testee-1", IAMProfileID: "profile-1", TesteeCreated: true}
	if err := recordHistoricalLocalStage(stateDir, &manifest, profile, scenario, historical, dailySimulationJourneyStageTesteeProfile, outcome, target); err != nil {
		t.Fatal(err)
	}

	restored, err := restoreHistoricalExistingTestee(stateDir, "batch", profile, historical.ScenarioID, DailySimulationConfig{TesteeSource: "seeddata", TesteeTags: []string{"historical"}})
	if err != nil {
		t.Fatal(err)
	}
	if restored == nil || restored.ID != "testee-1" || restored.ProfileID == nil || *restored.ProfileID != "profile-1" || restored.CreatedAt != (time.Time{}) {
		t.Fatalf("restored testee = %+v", restored)
	}
}

func TestHistoricalLocalStageLedgerRejectsFrozenPayloadDrift(t *testing.T) {
	stateDir := t.TempDir()
	day := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	profile := dailySimulationProfile{RunDate: day, Index: 7}
	scenario := dailySimulationScenario{Entry: &AssessmentEntryResponse{ID: "entry-1"}}
	historical := historicalseed.Context{BatchID: "batch", ScenarioID: "scenario", OrgID: 9, Version: historicalseed.Version1}
	manifest := HistoricalManifest{BatchID: "batch"}
	target := &dailySimulationResolvedTarget{TargetType: "scale", TargetCode: "MODEL", TargetVersion: "1", QuestionnaireCode: "Q", QuestionnaireVersion: "1"}
	outcome := dailySimulationOutcome{JourneyTarget: string(dailySimulationJourneySubmitAnswer)}
	if err := recordHistoricalLocalStage(stateDir, &manifest, profile, scenario, historical, dailySimulationJourneyStageAnswerSheet, outcome, target); err != nil {
		t.Fatal(err)
	}
	drifted := *target
	drifted.QuestionnaireVersion = "2"
	if err := recordHistoricalLocalStage(stateDir, &manifest, profile, scenario, historical, dailySimulationJourneyStageAnswerSheet, outcome, &drifted); err == nil {
		t.Fatal("payload drift must conflict")
	}
}

func TestVerifyHistoricalDayIsTheOnlyWriterOfScenarioTerminalAndCheckpointCount(t *testing.T) {
	stateDir := t.TempDir()
	day := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	profile := dailySimulationProfile{RunDate: day, Index: 0}
	target := &dailySimulationResolvedTarget{TargetType: "scale", TargetCode: "MODEL", TargetVersion: "1", QuestionnaireCode: "Q", QuestionnaireVersion: "1", RequiresAssessment: true}
	scenario := dailySimulationScenario{Entry: &AssessmentEntryResponse{ID: "entry-1"}, Target: target}
	historical := historicalseed.Context{BatchID: "batch", ScenarioID: "2025-01-01/0/register_only/MODEL", OrgID: 9, Version: historicalseed.Version1}
	manifest := HistoricalManifest{Version: 1, BatchID: "batch", OrgID: 9, Targets: map[string]HistoricalTargetManifest{"scale/MODEL": {RequiresAssessment: true}}, Scenarios: map[string]HistoricalScenarioManifest{}, DailyCounts: map[string]int{}}
	outcome := dailySimulationOutcome{JourneyTarget: string(dailySimulationJourneyRegisterOnly), StopReason: "journey_target_reached", GuardianUserID: "user-1"}
	if err := recordHistoricalLocalStage(stateDir, &manifest, profile, scenario, historical, dailySimulationJourneyStageGuardianAccount, outcome, target); err != nil {
		t.Fatal(err)
	}
	local, err := loadHistoricalLocalScenarioStages(stateDir, "batch", day, historical.ScenarioID)
	if err != nil {
		t.Fatal(err)
	}
	recordHistoricalScenario(&manifest, "batch", profile, scenario, outcome, local)
	if manifest.Scenarios[historical.ScenarioID].Terminal != "" {
		t.Fatal("scenario callback must not infer a terminal result")
	}
	if err := verifyHistoricalDay(stateDir, &manifest, day, 1, nil); err != nil {
		t.Fatal(err)
	}
	if manifest.Scenarios[historical.ScenarioID].Terminal != "verified" || manifest.DailyCounts["2025-01-01"] != 1 {
		t.Fatalf("verified manifest=%+v", manifest)
	}
}

func TestVerifyHistoricalBackfillRejectsUnverifiedScenarioTerminal(t *testing.T) {
	stateDir := t.TempDir()
	batchID := "terminal-contract"
	checkpointPath, manifestPath := historicalPaths(stateDir, batchID)
	checkpoint := HistoricalCheckpoint{Version: 1, BatchID: batchID, From: "2025-01-01", To: "2025-01-01", CompletedThrough: "2025-01-01"}
	manifest := HistoricalManifest{
		Version: 1, BatchID: batchID, From: "2025-01-01", To: "2025-01-01", Timezone: historicalTimezone,
		DailyCounts: map[string]int{"2025-01-01": 1},
		Scenarios:   map[string]HistoricalScenarioManifest{"scenario": {ScenarioID: "scenario", BusinessDate: "2025-01-01"}},
	}
	if err := saveSecureJSON(checkpointPath, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := saveSecureJSON(manifestPath, &manifest); err != nil {
		t.Fatal(err)
	}
	verification, err := VerifyHistoricalBackfill(stateDir, batchID)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Complete {
		t.Fatal("unverified scenario terminal must keep batch verification incomplete")
	}
}

func TestRequireHistoricalServerStagesRejectsMissingReport(t *testing.T) {
	stages := map[string]map[string]HistoricalStageRecord{"scenario": {
		"answersheet_submit": {Stage: "answersheet_submit", Status: "completed", ResourceID: "answer-1", BusinessAt: time.Now()},
	}}
	if err := requireHistoricalServerStages(stages, "scenario", []string{"answersheet_submit", "report_generated"}); err == nil {
		t.Fatal("missing report_generated must fail the day")
	}
}

func TestValidateHistoricalScenarioStageSetRejectsResourcePayloadAndTimeDrift(t *testing.T) {
	location, _ := time.LoadLocation(historicalTimezone)
	businessAt := time.Date(2025, 1, 1, 9, 0, 0, 0, location)
	manifest := &HistoricalManifest{BatchID: "batch", OrgID: 9}
	parent := HistoricalScenarioManifest{ScenarioID: "2025-01-01/7/submit_answer/MODEL", EntryID: "entry-1"}
	valid := HistoricalStageRecord{
		OrgID: 9, BatchID: "batch", ScenarioID: parent.ScenarioID, Stage: "answersheet_submit",
		Status: "completed", BusinessAt: businessAt, ResourceType: "answer_sheet", ResourceID: "9007199254740993",
		PayloadHash: "hash", PayloadJSON: json.RawMessage(`{"answersheet_id":9007199254740993}`),
	}
	if err := validateHistoricalScenarioStageSet(manifest, parent, parent.ScenarioID, map[string]HistoricalStageRecord{valid.Stage: valid}); err != nil {
		t.Fatalf("valid stage rejected: %v", err)
	}
	tests := map[string]func(*HistoricalStageRecord){
		"resource_type": func(record *HistoricalStageRecord) { record.ResourceType = "assessment" },
		"payload":       func(record *HistoricalStageRecord) { record.PayloadJSON = json.RawMessage(`{"answersheet_id":1}`) },
		"business_day":  func(record *HistoricalStageRecord) { record.BusinessAt = businessAt.AddDate(0, 0, 1) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			record := valid
			mutate(&record)
			if err := validateHistoricalScenarioStageSet(manifest, parent, parent.ScenarioID, map[string]HistoricalStageRecord{record.Stage: record}); err == nil {
				t.Fatalf("%s drift must be rejected", name)
			}
		})
	}
}

func TestVerifyHistoricalSubmissionStageMapRequiresEveryTerminalFact(t *testing.T) {
	historical := historicalseed.Context{BatchID: "batch", ScenarioID: "2025-01-01/7/submit_answer/task-1", OrgID: 9, Version: historicalseed.Version1}
	all := map[string]HistoricalStageRecord{
		"task_open":            {Status: "completed", ResourceID: "task-1"},
		"answersheet_submit":   {Status: "completed", ResourceID: "answer-1"},
		"assessment_created":   {Status: "completed", ResourceID: "assessment-1"},
		"assessment_submitted": {Status: "completed", ResourceID: "assessment-1"},
		"task_complete":        {Status: "completed", ResourceID: "task-1"},
		"outcome_committed":    {Status: "completed", ResourceID: "outcome-1"},
		"report_generated":     {Status: "completed", ResourceID: "report-1"},
	}
	for missing := range all {
		stages := make(map[string]HistoricalStageRecord, len(all)-1)
		for stage, record := range all {
			if stage != missing {
				stages[stage] = record
			}
		}
		if _, err := verifyHistoricalSubmissionStageMap(historical, "task-1", true, dailySimulationSubmissionResult{}, stages); err == nil {
			t.Fatalf("missing %s must reject terminal restore", missing)
		}
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
