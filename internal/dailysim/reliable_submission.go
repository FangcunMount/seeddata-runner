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

var errDailySimulationAssessmentPending = errors.New("assessment remains pending")

var (
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
	logicalID := dailySimulationSubmissionLogicalID(state, req.TesteeID)

	_, exists, err := ledger.Get(logicalID)
	if err != nil {
		return err
	}
	if !exists && strings.TrimSpace(state.guardianUserID) != "" {
		legacy, err := findDailySimulationLegacyAnswerSheet(
			ctx, state.deps.APIClient, req.QuestionnaireCode, state.guardianUserID,
		)
		if err != nil {
			return err
		}
		if legacy != nil {
			record, err := ledger.ReconcileLegacy(logicalID, legacy.ID, req)
			if err != nil {
				return err
			}
			state.outcome.AnswerSheetID = record.AnswerSheetID
			state.outcome.SkippedSubmission = true
			return finishDailySimulationReadiness(ctx, state, ledger, logicalID, req.TesteeID, record.AnswerSheetID)
		}
	}

	prepared, err := ledger.Prepare(logicalID, req)
	if err != nil {
		return err
	}
	req.IdempotencyKey = prepared.Record.IdempotencyKey
	answerSheetID := strings.TrimSpace(prepared.Record.AnswerSheetID)
	if prepared.ShouldSubmit {
		accepted, err := client.AcceptCollectionAnswerSheet(ctx, req, prepared.Record.RequestID)
		if err != nil {
			return err
		}
		record, err := ledger.MarkAccepted(logicalID, accepted.AnswerSheetID)
		if err != nil {
			return err
		}
		answerSheetID = record.AnswerSheetID
	} else {
		state.outcome.SkippedSubmission = true
	}
	state.outcome.AnswerSheetID = answerSheetID
	return finishDailySimulationReadiness(ctx, state, ledger, logicalID, req.TesteeID, answerSheetID)
}

func finishDailySimulationReadiness(
	ctx context.Context,
	state *dailySimulationJourneyState,
	ledger *toolanswersheet.SubmissionLedger,
	logicalID string,
	testeeID uint64,
	answerSheetID string,
) error {
	if !state.target.RequiresAssessment {
		record, ok, err := ledger.Get(logicalID)
		if err != nil {
			return err
		}
		if ok && record.Status == toolanswersheet.SubmissionStatusLegacy {
			return nil
		}
		_, err = ledger.MarkCompleted(logicalID, answerSheetID)
		return err
	}
	assessmentID, err := waitForDailySimulationReadiness(ctx, state.collectionClient, answerSheetID, testeeID)
	if errors.Is(err, errDailySimulationAssessmentPending) {
		_, markErr := ledger.MarkAcceptedPending(logicalID)
		return markErr
	}
	if err != nil {
		return err
	}
	record, err := ledger.MarkReady(logicalID, assessmentID)
	if err != nil {
		return err
	}
	state.outcome.AssessmentID = record.AssessmentID
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

func dailySimulationSubmissionLogicalID(state *dailySimulationJourneyState, testeeID uint64) string {
	return strings.Join([]string{
		"daily",
		state.profile.RunDate.Format("20060102"),
		strconv.Itoa(state.profile.Index),
		strconv.FormatUint(testeeID, 10),
		strings.TrimSpace(state.target.TargetType),
		strings.TrimSpace(state.target.TargetCode),
		strings.TrimSpace(state.target.TargetVersion),
		strings.TrimSpace(state.target.QuestionnaireCode),
		strings.TrimSpace(state.target.QuestionnaireVersion),
	}, "|")
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
