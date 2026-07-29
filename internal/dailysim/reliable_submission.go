package dailysim

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	toolanswersheet "github.com/FangcunMount/seeddata-runner/internal/answersheet"
	"github.com/FangcunMount/seeddata-runner/internal/historicalseed"
	"github.com/FangcunMount/seeddata-runner/internal/scheduler"
)

const (
	dailySimulationReadinessDefaultDelay = 2 * time.Second
	dailySimulationReadinessMinDelay     = 250 * time.Millisecond
	dailySimulationReadinessMaxDelay     = 10 * time.Second
)

var (
	errDailySimulationAssessmentPending = errors.New("assessment remains pending")
	errHistoricalSubmissionPending      = errors.New("historical submission has not reached its required terminal stage")
)

type dailySimulationSubmissionStatus string

const (
	dailySimulationSubmissionDurableAccepted dailySimulationSubmissionStatus = "durable_accepted"
	dailySimulationSubmissionAcceptedPending dailySimulationSubmissionStatus = "accepted_pending"
	dailySimulationSubmissionAssessmentReady dailySimulationSubmissionStatus = "assessment_ready"
	dailySimulationSubmissionReportGenerated dailySimulationSubmissionStatus = "report_generated"
)

type dailySimulationSubmissionResult struct {
	Status        dailySimulationSubmissionStatus
	AnswerSheetID string
	AssessmentID  string
	OutcomeID     string
	ReportID      string
	ServerStages  map[string]HistoricalStageRecord
}

type dailySimulationPendingReport struct {
	client      *APIClient
	adminClient *APIClient
	ledger      toolanswersheet.SubmissionStore
	logicalID   string
	testeeID    uint64
	taskID      string
	historical  historicalseed.Context
	result      dailySimulationSubmissionResult
}

var (
	dailySimulationReadinessNow  = time.Now
	dailySimulationReadinessWait = scheduler.Wait
)

func submitDailySimulationAnswerSheet(ctx context.Context, state *dailySimulationJourneyState, req SubmitAnswerSheetRequest) (dailySimulationSubmissionResult, error) {
	result, pending, err := prepareDailySimulationAnswerSheet(ctx, state, req)
	if err != nil || pending == nil {
		return result, err
	}
	return pending.Wait(ctx)
}

func prepareDailySimulationAnswerSheet(
	ctx context.Context,
	state *dailySimulationJourneyState,
	req SubmitAnswerSheetRequest,
) (dailySimulationSubmissionResult, *dailySimulationPendingReport, error) {
	if state == nil || state.deps == nil {
		return dailySimulationSubmissionResult{}, nil, fmt.Errorf("daily simulation state is not initialized")
	}
	ledger := state.deps.DailySubmissionLedger
	if ledger == nil {
		return dailySimulationSubmissionResult{}, nil, fmt.Errorf("daily submission ledger is not initialized")
	}
	client := state.collectionClient
	if client == nil {
		return dailySimulationSubmissionResult{}, nil, fmt.Errorf("guardian collection client is not initialized")
	}
	logicalID := dailySimulationSubmissionLogicalID(state, req.TesteeID, req.TaskID)

	_, exists, err := ledger.Get(logicalID)
	if err != nil {
		return dailySimulationSubmissionResult{}, nil, err
	}
	_, isHistorical := historicalseed.FromContext(ctx)
	if !exists && !isHistorical && strings.TrimSpace(state.guardianUserID) != "" {
		legacy, err := findDailySimulationLegacyAnswerSheet(
			ctx, state.deps.APIClient, req.QuestionnaireCode, state.guardianUserID,
		)
		if err != nil {
			return dailySimulationSubmissionResult{}, nil, err
		}
		if legacy != nil {
			record, err := ledger.ReconcileLegacy(logicalID, legacy.ID, req)
			if err != nil {
				return dailySimulationSubmissionResult{}, nil, err
			}
			state.outcome.SkippedSubmission = true
			return finishDailySimulationReadiness(ctx, state, ledger, logicalID, req.TesteeID, req.TaskID, record.AnswerSheetID)
		}
	}

	prepared, err := ledger.Prepare(logicalID, req)
	if err != nil {
		return dailySimulationSubmissionResult{}, nil, err
	}
	historicalContext, historicalRun := historicalseed.FromContext(ctx)
	if prepared.ShouldSubmit && historicalRun {
		if reconciler := historicalDownstreamReconcilerFromContext(ctx); reconciler != nil {
			if err := reconciler.AllowSubmission(); err != nil {
				return dailySimulationSubmissionResult{}, nil, err
			}
		}
	}
	req.IdempotencyKey = prepared.Record.IdempotencyKey
	answerSheetID := strings.TrimSpace(prepared.Record.AnswerSheetID)
	if prepared.ShouldSubmit {
		accepted, err := client.AcceptCollectionAnswerSheet(ctx, req, prepared.Record.RequestID)
		if err != nil {
			return dailySimulationSubmissionResult{}, nil, err
		}
		record, err := ledger.MarkAccepted(logicalID, accepted.AnswerSheetID)
		if err != nil {
			return dailySimulationSubmissionResult{}, nil, err
		}
		answerSheetID = record.AnswerSheetID
	} else {
		state.outcome.SkippedSubmission = true
	}
	if historicalRun {
		if store, ok := ledger.(*historicalStateStore); ok {
			record, err := ledger.MarkAcceptedPending(logicalID)
			if err != nil {
				return dailySimulationSubmissionResult{AnswerSheetID: answerSheetID}, nil, err
			}
			targetKey := strings.Join([]string{state.target.TargetType, state.target.TargetCode}, "/")
			if err := store.putDownstreamSubmitted(historicalDownstreamRecord{
				LogicalID: logicalID, ScenarioID: historicalContext.ScenarioID,
				BusinessDate: scenarioDateFromID(historicalContext.ScenarioID), TaskID: req.TaskID,
				TargetKey: targetKey, AnswerSheetID: record.AnswerSheetID,
				TesteeID: req.TesteeID, RequiresAssessment: state.target.RequiresAssessment,
			}); err != nil {
				return dailySimulationSubmissionResult{AnswerSheetID: answerSheetID}, nil, err
			}
			if reconciler := historicalDownstreamReconcilerFromContext(ctx); reconciler != nil {
				reconciler.Notify()
			}
			return dailySimulationSubmissionResult{
				Status: dailySimulationSubmissionAcceptedPending, AnswerSheetID: record.AnswerSheetID,
			}, nil, nil
		}
	}
	result, pending, err := finishDailySimulationReadiness(ctx, state, ledger, logicalID, req.TesteeID, req.TaskID, answerSheetID)
	if err != nil {
		return result, nil, err
	}
	if pending != nil {
		return result, pending, nil
	}
	if historicalRun {
		result, err = verifyHistoricalSubmissionStages(ctx, state.deps.APIClient, historicalContext, req.TaskID, state.target.RequiresAssessment, result)
		return result, nil, err
	}
	return result, nil, nil
}

func finishDailySimulationReadiness(
	ctx context.Context,
	state *dailySimulationJourneyState,
	ledger toolanswersheet.SubmissionStore,
	logicalID string,
	testeeID uint64,
	taskID string,
	answerSheetID string,
) (dailySimulationSubmissionResult, *dailySimulationPendingReport, error) {
	result := dailySimulationSubmissionResult{AnswerSheetID: strings.TrimSpace(answerSheetID)}
	if !state.target.RequiresAssessment {
		record, ok, err := ledger.Get(logicalID)
		if err != nil {
			return result, nil, err
		}
		if ok && record.Status == toolanswersheet.SubmissionStatusLegacy {
			result.Status = dailySimulationSubmissionDurableAccepted
			return result, nil, nil
		}
		_, err = ledger.MarkCompleted(logicalID, answerSheetID)
		result.Status = dailySimulationSubmissionDurableAccepted
		return result, nil, err
	}
	assessmentID, err := waitForDailySimulationReadiness(ctx, state.collectionClient, answerSheetID, testeeID)
	if errors.Is(err, errDailySimulationAssessmentPending) {
		_, markErr := ledger.MarkAcceptedPending(logicalID)
		result.Status = dailySimulationSubmissionAcceptedPending
		if markErr != nil {
			return result, nil, markErr
		}
		if _, historical := historicalseed.FromContext(ctx); historical {
			return result, nil, fmt.Errorf("%w: answersheet_id=%s", errHistoricalSubmissionPending, answerSheetID)
		}
		return result, nil, nil
	}
	if err != nil {
		return result, nil, err
	}
	result.AssessmentID = strings.TrimSpace(assessmentID)
	result.Status = dailySimulationSubmissionAssessmentReady
	if historical, ok := historicalseed.FromContext(ctx); ok {
		return result, &dailySimulationPendingReport{
			client: state.collectionClient, adminClient: state.deps.APIClient, ledger: ledger,
			logicalID: logicalID, testeeID: testeeID, taskID: taskID,
			historical: historical, result: result,
		}, nil
	}
	record, err := ledger.MarkReady(logicalID, assessmentID)
	if err != nil {
		return result, nil, err
	}
	result.AssessmentID = record.AssessmentID
	return result, nil, nil
}

func (pending *dailySimulationPendingReport) Wait(ctx context.Context) (dailySimulationSubmissionResult, error) {
	if pending == nil {
		return dailySimulationSubmissionResult{}, fmt.Errorf("daily simulation pending report is nil")
	}
	result := pending.result
	if err := waitForDailySimulationReport(ctx, pending.client, result.AssessmentID, pending.testeeID); err != nil {
		return result, err
	}
	result.Status = dailySimulationSubmissionReportGenerated
	record, err := pending.ledger.MarkReady(pending.logicalID, result.AssessmentID)
	if err != nil {
		return result, err
	}
	result.AssessmentID = record.AssessmentID
	return verifyHistoricalSubmissionStages(ctx, pending.adminClient, pending.historical, pending.taskID, true, result)
}

func verifyHistoricalSubmissionStages(
	ctx context.Context,
	client *APIClient,
	historical historicalseed.Context,
	taskID string,
	requiresAssessment bool,
	result dailySimulationSubmissionResult,
) (dailySimulationSubmissionResult, error) {
	if client == nil {
		return result, fmt.Errorf("historical stage API client is not initialized")
	}
	response, err := client.ListHistoricalScenarioStages(ctx, historical.BatchID, historical.ScenarioID)
	if err != nil {
		return result, fmt.Errorf("load historical terminal stages for %s: %w", historical.ScenarioID, err)
	}
	byStage := make(map[string]HistoricalStageRecord, len(response.Stages))
	for _, record := range response.Stages {
		if !strings.EqualFold(strings.TrimSpace(record.Status), "completed") || strings.TrimSpace(record.ResourceID) == "" {
			continue
		}
		byStage[strings.TrimSpace(record.Stage)] = record
	}
	return verifyHistoricalSubmissionStageMap(historical, taskID, requiresAssessment, result, byStage)
}

func verifyHistoricalSubmissionStageMap(
	historical historicalseed.Context,
	taskID string,
	requiresAssessment bool,
	result dailySimulationSubmissionResult,
	byStage map[string]HistoricalStageRecord,
) (dailySimulationSubmissionResult, error) {
	required := []string{"answersheet_submit"}
	if strings.TrimSpace(taskID) != "" {
		required = append([]string{"task_open"}, required...)
		required = append(required, "task_complete")
	}
	if requiresAssessment {
		required = append(required, "assessment_created", "assessment_submitted", "outcome_committed", "report_generated")
	}
	for _, stage := range required {
		if _, ok := byStage[stage]; !ok {
			return result, fmt.Errorf("%w: scenario=%s missing_stage=%s", errHistoricalSubmissionPending, historical.ScenarioID, stage)
		}
	}
	if got := byStage["answersheet_submit"].ResourceID; result.AnswerSheetID != "" && got != result.AnswerSheetID {
		return result, fmt.Errorf("historical answersheet stage resource %s conflicts with accepted answersheet %s", got, result.AnswerSheetID)
	}
	if taskID = strings.TrimSpace(taskID); taskID != "" {
		if byStage["task_open"].ResourceID != taskID || byStage["task_complete"].ResourceID != taskID {
			return result, fmt.Errorf("historical task stages conflict with task %s", taskID)
		}
	}
	if requiresAssessment {
		createdID := byStage["assessment_created"].ResourceID
		if createdID != byStage["assessment_submitted"].ResourceID || result.AssessmentID != "" && createdID != result.AssessmentID {
			return result, fmt.Errorf("historical assessment stages conflict with ready assessment %s", result.AssessmentID)
		}
		result.AssessmentID = createdID
		result.OutcomeID = byStage["outcome_committed"].ResourceID
		result.ReportID = byStage["report_generated"].ResourceID
		result.Status = dailySimulationSubmissionReportGenerated
	}
	result.AnswerSheetID = byStage["answersheet_submit"].ResourceID
	result.ServerStages = byStage
	return result, nil
}

func waitForDailySimulationReport(ctx context.Context, client *APIClient, assessmentID string, testeeID uint64) error {
	deadline := dailySimulationReadinessNow().Add(seedAssessmentPollTimeout)
	for {
		result, err := client.WaitAssessmentReport(ctx, assessmentID, testeeID, 20)
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(result.Status)) {
		case "interpreted":
			return nil
		case "failed":
			reason := strings.TrimSpace(result.Reason)
			if reason == "" {
				reason = strings.TrimSpace(result.Message)
			}
			return fmt.Errorf("report generation failed for assessment %s: %s", assessmentID, reason)
		case "processing", "pending", "":
		default:
			return fmt.Errorf("unexpected report status %q for assessment %s", result.Status, assessmentID)
		}
		if dailySimulationReadinessNow().After(deadline) {
			return fmt.Errorf("report generation timed out for assessment %s", assessmentID)
		}
		delay := dailySimulationReadinessDelay(result.NextPollAfterMs)
		if err := dailySimulationReadinessWait(ctx, delay); err != nil {
			return err
		}
	}
}

func waitForDailySimulationReadiness(
	ctx context.Context,
	client *APIClient,
	answerSheetID string,
	testeeID uint64,
) (string, error) {
	if client == nil {
		return "", fmt.Errorf("guardian collection client is not initialized")
	}
	deadline := dailySimulationReadinessNow().Add(seedAssessmentPollTimeout)
	for {
		readiness, err := client.GetAssessmentReadiness(ctx, answerSheetID, testeeID)
		if err != nil {
			return "", err
		}
		switch strings.ToLower(strings.TrimSpace(readiness.Status)) {
		case "ready":
			if strings.TrimSpace(readiness.AssessmentID) == "" {
				return "", fmt.Errorf("assessment readiness returned ready without assessment_id for answersheet %s", answerSheetID)
			}
			return strings.TrimSpace(readiness.AssessmentID), nil
		case "pending":
			// Continue below using the server-provided backoff.
		default:
			return "", fmt.Errorf("unexpected assessment readiness status %q for answersheet %s", readiness.Status, answerSheetID)
		}
		if dailySimulationReadinessNow().After(deadline) {
			return "", errDailySimulationAssessmentPending
		}
		delay := dailySimulationReadinessDelay(readiness.NextPollAfterMs)
		if err := dailySimulationReadinessWait(ctx, delay); err != nil {
			return "", err
		}
	}
}

func dailySimulationReadinessDelay(nextPollAfterMs int) time.Duration {
	if nextPollAfterMs <= 0 {
		return dailySimulationReadinessDefaultDelay
	}
	delay := time.Duration(nextPollAfterMs) * time.Millisecond
	if delay < dailySimulationReadinessMinDelay {
		return dailySimulationReadinessMinDelay
	}
	if delay > dailySimulationReadinessMaxDelay {
		return dailySimulationReadinessMaxDelay
	}
	return delay
}

func dailySimulationSubmissionLogicalID(state *dailySimulationJourneyState, testeeID uint64, taskID string) string {
	parts := []string{
		"daily",
		state.profile.RunDate.Format("20060102"),
		strconv.Itoa(state.profile.Index),
		strconv.FormatUint(testeeID, 10),
		strings.TrimSpace(state.target.TargetType),
		strings.TrimSpace(state.target.TargetCode),
		strings.TrimSpace(state.target.TargetVersion),
		strings.TrimSpace(state.target.QuestionnaireCode),
		strings.TrimSpace(state.target.QuestionnaireVersion),
	}
	// Additional targets used to be submitted with the primary assessment-entry
	// origin and could leave rejected prepared records behind. Keep the legacy
	// identity for primary/plan submissions, but namespace the corrected
	// self-service payload so an interrupted backfill can resume safely.
	if origin := dailySimulationSubmissionOriginRef(state, taskID); origin.Type == "self_service" {
		parts = append(parts, "origin:self_service:v1")
	}
	// Preserve the legacy daemon identity when no Plan task participates. A task
	// suffix is required only for historical scenarios that may submit several
	// AnswerSheets for the same target on different tasks.
	if taskID = strings.TrimSpace(taskID); taskID != "" {
		parts = append(parts, taskID)
	}
	return strings.Join(parts, "|")
}

func findDailySimulationLegacyAnswerSheet(
	ctx context.Context,
	adminClient *APIClient,
	questionnaireCode, guardianUserID string,
) (*AdminAnswerSheetListItem, error) {
	userID := parseID(guardianUserID)
	if userID == 0 {
		return nil, fmt.Errorf("invalid guardian user id %q", guardianUserID)
	}
	resp, err := adminClient.ListAdminAnswerSheets(ctx, questionnaireCode, userID, 1, dailySimulationAnswerSheetPage)
	if err != nil {
		return nil, fmt.Errorf("list legacy admin answersheets for questionnaire %s filler %s: %w", questionnaireCode, guardianUserID, err)
	}
	for _, item := range resp.Items {
		if strings.TrimSpace(item.ID) != "" {
			cloned := item
			return &cloned, nil
		}
	}
	return nil, nil
}
