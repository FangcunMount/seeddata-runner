package plansubmit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/FangcunMount/seeddata-runner/internal/scheduler"
)

func submitPlanTaskAnswerSheet(
	ctx context.Context,
	client adminAnswerSheetSubmitClient,
	ledger *SubmissionLedger,
	planID string,
	req SubmitAnswerSheetRequest,
) (attempts int, reused bool, err error) {
	if client == nil {
		return 0, false, fmt.Errorf("admin answersheet submit client is nil")
	}
	if ledger == nil {
		return 0, false, fmt.Errorf("plan submission ledger is not initialized")
	}
	logicalID := strings.Join([]string{
		"plan",
		strings.TrimSpace(planID),
		strings.TrimSpace(req.TaskID),
		fmt.Sprintf("%d", req.TesteeID),
	}, "|")
	fingerprintPayload := req
	fingerprintPayload.IdempotencyKey = ""

	for attempt := 0; attempt < planOpenTaskSubmitMaxAttempts; attempt++ {
		prepared, err := ledger.Prepare(logicalID, fingerprintPayload)
		if err != nil {
			return attempts, false, err
		}
		if !prepared.ShouldSubmit {
			return attempts, true, nil
		}
		req.IdempotencyKey = prepared.Record.IdempotencyKey
		attempts++
		response, err := client.SubmitAnswerSheetAdminAttempt(
			ctx,
			buildAdminSubmitAnswerSheetRequest(req),
			prepared.Record.RequestID,
			planOpenTaskSubmitRequestTimeout,
		)
		if err == nil {
			if response == nil || strings.TrimSpace(response.ID) == "" {
				return attempts, false, fmt.Errorf("admin submit returned empty answersheet id")
			}
			if _, err := ledger.MarkCompleted(logicalID, response.ID); err != nil {
				return attempts, false, err
			}
			return attempts, false, nil
		}
		if isSeedPlanHTTPStatus(err, 409) {
			if _, markErr := ledger.MarkConflict(logicalID); markErr != nil {
				return attempts, false, fmt.Errorf("record admin-submit conflict: %w", markErr)
			}
			return attempts, false, err
		}
		if attempt+1 >= planOpenTaskSubmitMaxAttempts || !isSeedPlanRecoverableError(err) {
			return attempts, false, err
		}
		if err := scheduler.Wait(ctx, planOpenTaskSubmitRetryBackoff*time.Duration(attempt+1)); err != nil {
			return attempts, false, err
		}
	}
	return attempts, false, fmt.Errorf("plan submission exhausted without a terminal result")
}

func isSeedPlanHTTPStatus(err error, status int) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, fmt.Sprintf("http_status=%d", status)) ||
		strings.Contains(message, fmt.Sprintf("status=%d", status))
}
