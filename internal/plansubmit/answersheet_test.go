package plansubmit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type adminAnswerSheetSubmitClientStub struct {
	withPolicyCalls int
	lastPolicyReq   AdminSubmitAnswerSheetRequest
	lastRequestID   string
	lastTimeout     time.Duration
	policyErrs      []error
	idempotencyKeys []string
	requestIDs      []string
}

func (s *adminAnswerSheetSubmitClientStub) SubmitAnswerSheetAdminAttempt(ctx context.Context, req AdminSubmitAnswerSheetRequest, requestID string, timeout time.Duration) (*SubmitAnswerSheetResponse, error) {
	s.withPolicyCalls++
	s.lastPolicyReq = req
	s.lastRequestID = requestID
	s.lastTimeout = timeout
	s.idempotencyKeys = append(s.idempotencyKeys, req.IdempotencyKey)
	s.requestIDs = append(s.requestIDs, requestID)
	if len(s.policyErrs) == 0 {
		return &SubmitAnswerSheetResponse{ID: "9001"}, nil
	}
	err := s.policyErrs[0]
	s.policyErrs = s.policyErrs[1:]
	if err == nil {
		return &SubmitAnswerSheetResponse{ID: "9001"}, nil
	}
	return nil, err
}

func TestSubmitPlanTaskAnswerSheetRetriesWithStableBusinessIdentity(t *testing.T) {
	originalBackoff := planOpenTaskSubmitRetryBackoff
	planOpenTaskSubmitRetryBackoff = 0
	t.Cleanup(func() { planOpenTaskSubmitRetryBackoff = originalBackoff })

	ledger, err := NewSubmissionLedger(filepath.Join(t.TempDir(), "plan-submissions.json"), "plan")
	if err != nil {
		t.Fatal(err)
	}
	client := &adminAnswerSheetSubmitClientStub{policyErrs: []error{errors.New("http_status=503"), nil}}
	req := SubmitAnswerSheetRequest{
		QuestionnaireCode: "Q", QuestionnaireVersion: "1", TesteeID: 42, TaskID: "task-1",
		Answers: []Answer{{QuestionCode: "q1", QuestionType: "Radio", Value: "A"}},
	}
	attempts, reused, err := submitPlanTaskAnswerSheet(context.Background(), client, ledger, "plan-1", req)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || reused {
		t.Fatalf("attempts=%d reused=%v", attempts, reused)
	}
	if len(client.idempotencyKeys) != 2 || client.idempotencyKeys[0] == "" || client.idempotencyKeys[0] != client.idempotencyKeys[1] {
		t.Fatalf("idempotency keys changed: %#v", client.idempotencyKeys)
	}
	if len(client.requestIDs) != 2 || client.requestIDs[0] == client.requestIDs[1] {
		t.Fatalf("request IDs must change per attempt: %#v", client.requestIDs)
	}
}

func TestSubmitPlanTaskAnswerSheetRecords409AsStickyConflict(t *testing.T) {
	ledger, err := NewSubmissionLedger(filepath.Join(t.TempDir(), "plan-submissions.json"), "plan")
	if err != nil {
		t.Fatal(err)
	}
	client := &adminAnswerSheetSubmitClientStub{policyErrs: []error{errors.New("http_status=409")}}
	req := SubmitAnswerSheetRequest{
		QuestionnaireCode: "Q", QuestionnaireVersion: "1", TesteeID: 42, TaskID: "task-1",
		Answers: []Answer{{QuestionCode: "q1", QuestionType: "Radio", Value: "A"}},
	}
	if attempts, _, err := submitPlanTaskAnswerSheet(context.Background(), client, ledger, "plan-1", req); err == nil || attempts != 1 {
		t.Fatalf("first conflict: attempts=%d err=%v", attempts, err)
	}
	if attempts, _, err := submitPlanTaskAnswerSheet(context.Background(), client, ledger, "plan-1", req); err == nil || attempts != 0 {
		t.Fatalf("sticky conflict should stop before HTTP: attempts=%d err=%v", attempts, err)
	}
	if client.withPolicyCalls != 1 {
		t.Fatalf("409 must not retry or resubmit, calls=%d", client.withPolicyCalls)
	}
}

func TestSubmitPlanTaskAnswerSheetDoesNotSendWhenLedgerCannotPersist(t *testing.T) {
	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	ledger, err := NewSubmissionLedger(filepath.Join(parentFile, "ledger.json"), "plan")
	if err != nil {
		t.Fatal(err)
	}
	client := &adminAnswerSheetSubmitClientStub{}
	req := SubmitAnswerSheetRequest{
		QuestionnaireCode: "Q", QuestionnaireVersion: "1", TesteeID: 42, TaskID: "task-1",
		Answers: []Answer{{QuestionCode: "q1", QuestionType: "Radio", Value: "A"}},
	}
	if attempts, _, err := submitPlanTaskAnswerSheet(context.Background(), client, ledger, "plan-1", req); err == nil || attempts != 0 {
		t.Fatalf("ledger persistence failure: attempts=%d err=%v", attempts, err)
	}
	if client.withPolicyCalls != 0 {
		t.Fatalf("HTTP submission occurred before durable prepare: calls=%d", client.withPolicyCalls)
	}
}

func TestBuildPlanTaskSubmitRequestIsDeterministic(t *testing.T) {
	detail := &QuestionnaireDetailResponse{
		Code: "Q", Version: "1",
		Questions: []QuestionResponse{{
			Code: "q1", Type: questionTypeRadio,
			Options: []OptionResponse{{Code: "A"}, {Code: "B"}, {Code: "C"}},
		}},
	}
	task := TaskResponse{ID: "task-1", TesteeID: "42"}
	first, err := buildPlanTaskSubmitRequest("plan-1", detail, "1", task, false, newSeeddataLogger(false))
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildPlanTaskSubmitRequest("plan-1", detail, "1", task, false, newSeeddataLogger(false))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("deterministic requests differ: first=%+v second=%+v", first, second)
	}
}
