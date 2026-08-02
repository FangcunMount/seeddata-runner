package dailysim

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	toolanswersheet "github.com/FangcunMount/seeddata-runner/internal/answersheet"
	"github.com/FangcunMount/seeddata-runner/internal/scheduler"
)

const (
	dailySimulationReadinessDefaultDelay = 2 * time.Second
	dailySimulationReadinessMinDelay     = 250 * time.Millisecond
	dailySimulationReadinessMaxDelay     = 10 * time.Second
)

var (
	errDailySimulationAssessmentPending = errors.New("assessment remains pending")
	errDailySimulationReportPending     = errors.New("assessment report remains pending")

	dailySimulationReadinessNow  = time.Now
	dailySimulationReadinessWait = scheduler.Wait
)

func submitDailySimulationAnswerSheet(ctx context.Context, state *dailySimulationJourneyState, req SubmitAnswerSheetRequest) error {
	if state == nil || state.deps == nil {
		return fmt.Errorf("daily simulation state is not initialized")
	}
	ledger := state.deps.DailySubmissionLedger
	if ledger == nil {
		return fmt.Errorf("daily submission ledger is not initialized")
	}
	client := state.collectionClient
	if client == nil {
		return fmt.Errorf("guardian collection client is not initialized")
	}

	logicalID := dailySimulationSubmissionLogicalID(state, req.TesteeID, req.TaskID)
	record, exists, err := ledger.Get(logicalID)
	if err != nil {
		return err
	}
	if !exists && strings.TrimSpace(state.guardianUserID) != "" {
		legacy, findErr := findDailySimulationLegacyAnswerSheet(
			ctx,
			state.deps.APIClient,
			req.QuestionnaireCode,
			state.guardianUserID,
		)
		if findErr != nil {
			return findErr
		}
		if legacy != nil {
			record, err = ledger.ReconcileLegacy(logicalID, legacy.ID, req)
			if err != nil {
				return err
			}
			exists = true
			state.outcome.SkippedSubmission = true
		}
	}

	prepared, err := ledger.Prepare(logicalID, req)
	if err != nil {
		return err
	}
	record = prepared.Record
	req.IdempotencyKey = record.IdempotencyKey

	if prepared.ShouldSubmit {
		accepted, acceptErr := client.AcceptCollectionAnswerSheet(ctx, req, record.RequestID)
		if acceptErr != nil {
			return acceptErr
		}
		record, err = ledger.MarkAccepted(logicalID, accepted.AnswerSheetID)
		if err != nil {
			return err
		}
	} else {
		state.outcome.SkippedSubmission = true
	}
	state.outcome.AnswerSheetID = strings.TrimSpace(record.AnswerSheetID)

	if !state.target.RequiresAssessment {
		if exists && record.Status == toolanswersheet.SubmissionStatusLegacy {
			return nil
		}
		_, err = ledger.MarkCompleted(logicalID, record.AnswerSheetID)
		return err
	}

	assessmentID := strings.TrimSpace(record.AssessmentID)
	if assessmentID == "" {
		assessmentID, err = waitForDailySimulationReadiness(ctx, client, record.AnswerSheetID, req.TesteeID)
		if errors.Is(err, errDailySimulationAssessmentPending) {
			if _, markErr := ledger.MarkAcceptedPending(logicalID); markErr != nil {
				return markErr
			}
			return fmt.Errorf("%w: answersheet_id=%s", errDailySimulationAssessmentPending, record.AnswerSheetID)
		}
		if err != nil {
			return err
		}
		record, err = ledger.MarkReady(logicalID, assessmentID)
		if err != nil {
			return err
		}
		assessmentID = strings.TrimSpace(record.AssessmentID)
	}

	state.outcome.AssessmentID = assessmentID
	if err := waitForDailySimulationReport(ctx, client, assessmentID, req.TesteeID); err != nil {
		return err
	}
	state.outcome.ReportStatus = "interpreted"
	return nil
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
		default:
			return "", fmt.Errorf("unexpected assessment readiness status %q for answersheet %s", readiness.Status, answerSheetID)
		}
		if !dailySimulationReadinessNow().Before(deadline) {
			return "", errDailySimulationAssessmentPending
		}
		if err := dailySimulationReadinessWait(ctx, dailySimulationReadinessDelay(readiness.NextPollAfterMs)); err != nil {
			return "", err
		}
	}
}

func waitForDailySimulationReport(ctx context.Context, client *APIClient, assessmentID string, testeeID uint64) error {
	if client == nil {
		return fmt.Errorf("guardian collection client is not initialized")
	}
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
		if !dailySimulationReadinessNow().Before(deadline) {
			return fmt.Errorf("%w: assessment_id=%s", errDailySimulationReportPending, assessmentID)
		}
		if err := dailySimulationReadinessWait(ctx, dailySimulationReadinessDelay(result.NextPollAfterMs)); err != nil {
			return err
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
	if origin := dailySimulationSubmissionOriginRef(state, taskID); origin.Type == "self_service" {
		parts = append(parts, "origin:self_service:v1")
	}
	if taskID = strings.TrimSpace(taskID); taskID != "" {
		parts = append(parts, taskID)
	}
	return strings.Join(parts, "|")
}

func dailySimulationSubmissionOriginRef(state *dailySimulationJourneyState, taskID string) *OriginRef {
	if taskID = strings.TrimSpace(taskID); taskID != "" {
		return &OriginRef{Type: "plan_task", ID: taskID}
	}
	if state != nil && state.submissionOriginRef != nil {
		return &OriginRef{
			Type: strings.TrimSpace(state.submissionOriginRef.Type),
			ID:   strings.TrimSpace(state.submissionOriginRef.ID),
		}
	}
	entryID := ""
	if state != nil && state.entry != nil {
		entryID = strings.TrimSpace(state.entry.ID)
	}
	return &OriginRef{Type: "assessment_entry", ID: entryID}
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
