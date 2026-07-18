package answersheet

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSubmissionLedgerPersistsStableIdentityAcrossRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	ledger, err := NewSubmissionLedger(path, "daily")
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"questionnaire_code": "Q", "questionnaire_version": "1", "testee_id": 42}
	first, err := ledger.Prepare("daily|20260718|1|42|Q|1", payload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ledger.Prepare("daily|20260718|1|42|Q|1", payload)
	if err != nil {
		t.Fatal(err)
	}
	if first.Record.IdempotencyKey != second.Record.IdempotencyKey || first.Record.Fingerprint != second.Record.Fingerprint {
		t.Fatalf("submission identity changed: first=%+v second=%+v", first.Record, second.Record)
	}
	if first.Record.RequestID == second.Record.RequestID {
		t.Fatal("each HTTP attempt must receive a fresh request ID")
	}
	if !first.ShouldSubmit || !second.ShouldSubmit {
		t.Fatal("prepared records without an answer sheet must be retryable")
	}

	reopened, err := NewSubmissionLedger(path, "daily")
	if err != nil {
		t.Fatal(err)
	}
	record, ok, err := reopened.Get(first.Record.LogicalID)
	if err != nil || !ok {
		t.Fatalf("reopen ledger: ok=%v err=%v", ok, err)
	}
	if record.IdempotencyKey != first.Record.IdempotencyKey {
		t.Fatalf("reopened identity mismatch: %+v", record)
	}
}

func TestSubmissionLedgerRejectsPayloadDrift(t *testing.T) {
	ledger, err := NewSubmissionLedger(filepath.Join(t.TempDir(), "ledger.json"), "plan")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Prepare("plan|1|2|3", map[string]any{"answer": "A"}); err != nil {
		t.Fatal(err)
	}
	prepared, err := ledger.Prepare("plan|1|2|3", map[string]any{"answer": "B"})
	if !errors.Is(err, ErrSubmissionConflict) {
		t.Fatalf("expected conflict, got prepared=%+v err=%v", prepared, err)
	}
	if prepared.Record.Status != SubmissionStatusConflict {
		t.Fatalf("unexpected conflict status: %+v", prepared.Record)
	}
	prepared, err = ledger.Prepare("plan|1|2|3", map[string]any{"answer": "A"})
	if !errors.Is(err, ErrSubmissionConflict) || prepared.Record.Status != SubmissionStatusConflict {
		t.Fatalf("conflict must remain sticky: prepared=%+v err=%v", prepared, err)
	}
}

func TestSubmissionLedgerAcceptedRecordSkipsResubmit(t *testing.T) {
	ledger, err := NewSubmissionLedger(filepath.Join(t.TempDir(), "ledger.json"), "plan")
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"answer": "A"}
	prepared, err := ledger.Prepare("plan|1|2|3", payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.MarkCompleted(prepared.Record.LogicalID, "9001"); err != nil {
		t.Fatal(err)
	}
	again, err := ledger.Prepare(prepared.Record.LogicalID, payload)
	if err != nil {
		t.Fatal(err)
	}
	if again.ShouldSubmit || again.Record.AnswerSheetID != "9001" {
		t.Fatalf("completed submission should be reused: %+v", again)
	}
	info, err := os.Stat(ledger.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("ledger permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestSubmissionLedgerFailedAtomicReplacePreservesPreviousFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	ledger, err := NewSubmissionLedger(path, "daily")
	if err != nil {
		t.Fatal(err)
	}
	first, err := ledger.Prepare("daily|1", map[string]any{"answer": "A"})
	if err != nil {
		t.Fatal(err)
	}
	ledger.rename = func(string, string) error { return errors.New("injected rename failure") }
	if _, err := ledger.Prepare("daily|1", map[string]any{"answer": "A"}); err == nil {
		t.Fatal("expected atomic replace failure")
	}
	reopened, err := NewSubmissionLedger(path, "daily")
	if err != nil {
		t.Fatal(err)
	}
	record, ok, err := reopened.Get("daily|1")
	if err != nil || !ok {
		t.Fatalf("read preserved ledger: ok=%v err=%v", ok, err)
	}
	if record.RequestID != first.Record.RequestID {
		t.Fatalf("failed replace changed durable request ID: got=%q want=%q", record.RequestID, first.Record.RequestID)
	}
}
