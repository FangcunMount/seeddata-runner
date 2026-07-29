package dailysim

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	toolanswersheet "github.com/FangcunMount/seeddata-runner/internal/answersheet"
	bolt "go.etcd.io/bbolt"
)

func historicalStateTestOptions(stateDir string, resume bool) HistoricalBackfillOptions {
	return HistoricalBackfillOptions{
		From: "2025-01-01", To: "2025-01-03", BatchID: "batch-v2", StateDir: stateDir, Resume: resume,
	}
}

func TestHistoricalSubmissionPayloadConflictIsDurable(t *testing.T) {
	stateDir := t.TempDir()
	opts := historicalStateTestOptions(stateDir, false)
	if err := PrepareHistoricalBackfillState(opts, 1, ""); err != nil {
		t.Fatal(err)
	}
	store, err := openHistoricalStateStore(stateDir, opts.BatchID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	logicalID := "daily|20250101|1|42|scale|M|1|Q|1"
	if _, err := store.Prepare(logicalID, struct{ Value string }{Value: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prepare(logicalID, struct{ Value string }{Value: "drift"}); !errors.Is(err, toolanswersheet.ErrSubmissionConflict) {
		t.Fatalf("expected payload conflict, got %v", err)
	}
	record, ok, err := store.Get(logicalID)
	if err != nil || !ok || record.Status != toolanswersheet.SubmissionStatusConflict {
		t.Fatalf("conflict was not persisted: record=%+v ok=%v err=%v", record, ok, err)
	}
}

func TestHistoricalDownstreamStateLifecycleAndHighWatermark(t *testing.T) {
	stateDir := t.TempDir()
	opts := historicalStateTestOptions(stateDir, false)
	if err := PrepareHistoricalBackfillState(opts, 1, ""); err != nil {
		t.Fatal(err)
	}
	store, err := openHistoricalStateStore(stateDir, opts.BatchID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	record := historicalDownstreamRecord{
		LogicalID: "daily|20250101|1|42|scale|M|1|Q|1", ScenarioID: "2025-01-01/1/submit_answer/M",
		BusinessDate: "2025-01-01", TargetKey: "scale/M", AnswerSheetID: "answer-1", TesteeID: 42,
		RequiresAssessment: true,
	}
	if err := store.putDownstreamSubmitted(record); err != nil {
		t.Fatal(err)
	}
	stored, ok, err := store.loadDownstream(record.LogicalID)
	if err != nil || !ok || stored.Status != historicalDownstreamSubmitted || stored.AnswerSheetID != "answer-1" {
		t.Fatalf("submitted downstream record=%+v ok=%v err=%v", stored, ok, err)
	}
	if pending, err := store.countOutstandingDownstream(""); err != nil || pending != 1 {
		t.Fatalf("outstanding=%d err=%v, want 1", pending, err)
	}
	if err := (&historicalDownstreamReconciler{store: store, highWatermark: 1}).AllowSubmission(); err == nil || !strings.Contains(err.Error(), "high watermark") {
		t.Fatalf("expected explicit high-watermark circuit breaker, got %v", err)
	}

	nextAttempt := time.Now().UTC().Add(time.Minute)
	if err := store.markDownstreamPending(record.LogicalID, "report pending", nextAttempt); err != nil {
		t.Fatal(err)
	}
	if due, err := store.listDueDownstream(time.Now().UTC(), 10); err != nil || len(due) != 0 {
		t.Fatalf("premature due records=%+v err=%v", due, err)
	}
	if due, err := store.listDueDownstream(nextAttempt.Add(time.Second), 10); err != nil || len(due) != 1 || due[0].Status != historicalDownstreamPending {
		t.Fatalf("due pending records=%+v err=%v", due, err)
	}
	if err := store.markDownstreamVerified(record.LogicalID, "assessment-1", "outcome-1", "report-1"); err != nil {
		t.Fatal(err)
	}
	stored, ok, err = store.loadDownstream(record.LogicalID)
	if err != nil || !ok || stored.Status != historicalDownstreamVerified || stored.ReportID != "report-1" || stored.VerifiedAt == nil {
		t.Fatalf("verified downstream record=%+v ok=%v err=%v", stored, ok, err)
	}
	if pending, err := store.countOutstandingDownstream(""); err != nil || pending != 0 {
		t.Fatalf("outstanding=%d err=%v, want 0", pending, err)
	}
}

func TestHistoricalCheckpointSplitsSubmissionAndVerificationProgress(t *testing.T) {
	checkpoint := normalizeHistoricalCheckpoint(HistoricalCheckpoint{
		Version: 2, BatchID: "batch", SubmittedThrough: "2025-01-03", VerifiedThrough: "2025-01-01",
	})
	if checkpoint.SubmittedThrough != "2025-01-03" || checkpoint.VerifiedThrough != "2025-01-01" || checkpoint.CompletedThrough != "2025-01-01" {
		t.Fatalf("split checkpoint was not preserved: %+v", checkpoint)
	}
	legacy := normalizeHistoricalCheckpoint(HistoricalCheckpoint{Version: 1, CompletedThrough: "2025-01-02"})
	if legacy.Version != 2 || legacy.SubmittedThrough != "2025-01-02" || legacy.VerifiedThrough != "2025-01-02" {
		t.Fatalf("legacy checkpoint was not upgraded safely: %+v", legacy)
	}
}

func TestHistoricalAcceptedPendingDoesNotDowngradeVerifiedSubmission(t *testing.T) {
	stateDir := t.TempDir()
	opts := historicalStateTestOptions(stateDir, false)
	if err := PrepareHistoricalBackfillState(opts, 1, ""); err != nil {
		t.Fatal(err)
	}
	store, err := openHistoricalStateStore(stateDir, opts.BatchID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	logicalID := "daily|20250101|1|42|scale|M|1|Q|1"
	if _, err := store.Prepare(logicalID, struct{ Value string }{Value: "stable"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkAccepted(logicalID, "answer-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkReady(logicalID, "assessment-1"); err != nil {
		t.Fatal(err)
	}
	record, err := store.MarkAcceptedPending(logicalID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != toolanswersheet.SubmissionStatusReady || record.AssessmentID != "assessment-1" {
		t.Fatalf("verified submission was downgraded: %+v", record)
	}
}

func TestOpenHistoricalStateStoreAddsDownstreamBucketToExistingV2State(t *testing.T) {
	stateDir := t.TempDir()
	opts := historicalStateTestOptions(stateDir, false)
	if err := PrepareHistoricalBackfillState(opts, 1, ""); err != nil {
		t.Fatal(err)
	}
	store, err := openHistoricalStateStore(stateDir, opts.BatchID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.update(func(tx *bolt.Tx) error {
		return tx.DeleteBucket(historicalBucketDownstream)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = openHistoricalStateStore(stateDir, opts.BatchID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if pending, err := store.countOutstandingDownstream(""); err != nil || pending != 0 {
		t.Fatalf("upgraded downstream bucket pending=%d err=%v", pending, err)
	}
}

func TestPrepareHistoricalBackfillStateCreatesProtectedV2Database(t *testing.T) {
	stateDir := t.TempDir()
	opts := historicalStateTestOptions(stateDir, false)
	if err := PrepareHistoricalBackfillState(opts, 1, filepath.Join(stateDir, "missing-submissions.json")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(historicalStateDBPath(stateDir, opts.BatchID))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("historical database mode=%o want=600", info.Mode().Perm())
	}
	store, err := openHistoricalStateStore(stateDir, opts.BatchID, false)
	if err != nil {
		t.Fatal(err)
	}
	payload := struct{ Value string }{Value: "stable"}
	prepared, err := store.Prepare("daily|20250101|1|42|scale|M|1|Q|1", payload)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.ShouldSubmit || prepared.Record.IdempotencyKey == "" || prepared.Record.RequestID == "" {
		t.Fatalf("unexpected prepared submission: %+v", prepared)
	}
	replayed, err := store.Prepare(prepared.Record.LogicalID, payload)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Record.RequestID != prepared.Record.RequestID || replayed.Record.IdempotencyKey != prepared.Record.IdempotencyKey {
		t.Fatalf("prepared replay changed identity: first=%+v replay=%+v", prepared.Record, replayed.Record)
	}
	if _, err := store.MarkAccepted(prepared.Record.LogicalID, "answer-1"); err != nil {
		t.Fatal(err)
	}
	parent := HistoricalScenarioManifest{
		ScenarioID: "2025-01-01/1/submit_answer/M", BusinessDate: "2025-01-01", TargetKey: "scale/M",
		Profile: HistoricalProfileManifest{
			Index: 1, RunDate: "2025-01-01", GuardianName: "吴军", GuardianPhone: "+8619905088001",
			GuardianEmail: "hist.0123456789abcdef.20250101.0001@fangcunmount.com",
			ChildName:     "吴小军", ChildDOB: "2017-05-06", ChildGender: 1,
		},
	}
	if err := store.putScenario(parent); err != nil {
		t.Fatal(err)
	}
	recovery := HistoricalPlanTaskRecovery{ScenarioID: "2025-01-02/1/submit_answer/task-1", TaskID: "task-1", PlannedAt: "2025-01-02T09:00:00+08:00", TargetKey: "scale/M"}
	if err := store.putPlanTask(parent.ScenarioID, recovery); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	readStore, err := openHistoricalStateStore(stateDir, opts.BatchID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readStore.Close() }()
	record, ok, err := readStore.Get(prepared.Record.LogicalID)
	if err != nil || !ok || record.AnswerSheetID != "answer-1" || record.IdempotencyKey != prepared.Record.IdempotencyKey {
		t.Fatalf("persisted record=%+v ok=%v err=%v", record, ok, err)
	}
	manifest, err := readStore.loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != historicalManifestVersion {
		t.Fatalf("historical manifest version=%d want=%d", manifest.Version, historicalManifestVersion)
	}
	restoredParent := manifest.Scenarios[parent.ScenarioID]
	if restoredParent.Profile != parent.Profile {
		t.Fatalf("frozen historical profile was not restored: got=%+v want=%+v", restoredParent.Profile, parent.Profile)
	}
	if len(restoredParent.PlanTaskRecoveries) != 1 || restoredParent.PlanTaskRecoveries[0] != recovery {
		t.Fatalf("independent plan task recovery was not restored: %+v", restoredParent)
	}
}

func TestPrepareHistoricalBackfillStateRejectsLegacyManifestWithoutRewritingFiles(t *testing.T) {
	stateDir := t.TempDir()
	opts := historicalStateTestOptions(stateDir, true)
	checkpointPath, manifestPath := historicalPaths(stateDir, opts.BatchID)
	manifest := HistoricalManifest{
		Version: 1, BatchID: opts.BatchID, OrgID: 1, From: opts.From, To: opts.To, Timezone: historicalTimezone,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Targets: map[string]HistoricalTargetManifest{}, Plans: map[string]HistoricalPlanManifest{}, DailyCounts: map[string]int{},
		Scenarios: map[string]HistoricalScenarioManifest{
			"2025-01-01/1/submit_answer/M": {ScenarioID: "2025-01-01/1/submit_answer/M", BusinessDate: "2025-01-01", TargetKey: "scale/M", TesteeID: "42"},
		},
	}
	checkpoint := HistoricalCheckpoint{Version: 1, BatchID: opts.BatchID, From: opts.From, To: opts.To}
	if err := saveSecureJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := saveSecureJSON(checkpointPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	manifestBefore, _, err := hashExistingFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	err = PrepareHistoricalBackfillState(opts, 1, "")
	if err == nil || !strings.Contains(err.Error(), "version 1 is not resumable") {
		t.Fatalf("expected legacy manifest version rejection, got %v", err)
	}
	manifestAfter, _, err := hashExistingFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifestBefore != manifestAfter {
		t.Fatal("legacy manifest was rewritten during rejection")
	}
	if _, err := os.Stat(historicalStateDBPath(stateDir, opts.BatchID)); !os.IsNotExist(err) {
		t.Fatalf("legacy manifest rejection created v2 state: %v", err)
	}
}

func TestPrepareHistoricalBackfillStateRejectsExistingDatabaseWithLegacyManifest(t *testing.T) {
	stateDir := t.TempDir()
	opts := historicalStateTestOptions(stateDir, false)
	if err := PrepareHistoricalBackfillState(opts, 1, ""); err != nil {
		t.Fatal(err)
	}
	store, err := openHistoricalStateStore(stateDir, opts.BatchID, false)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := store.loadManifest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.Version = 1
	if err := store.putManifestHeader(manifest); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	opts.Resume = true
	err = PrepareHistoricalBackfillState(opts, 1, "")
	if err == nil || !strings.Contains(err.Error(), "version 1 is not resumable") {
		t.Fatalf("expected existing database manifest version rejection, got %v", err)
	}
}

func TestHistoricalStateRejectsSecondWriter(t *testing.T) {
	stateDir := t.TempDir()
	opts := historicalStateTestOptions(stateDir, false)
	if err := PrepareHistoricalBackfillState(opts, 1, ""); err != nil {
		t.Fatal(err)
	}
	store, err := openHistoricalStateStore(stateDir, opts.BatchID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	started := time.Now()
	if _, err := openHistoricalStateStore(stateDir, opts.BatchID, false); err == nil {
		t.Fatal("expected second writer lock failure")
	}
	if time.Since(started) < 900*time.Millisecond {
		t.Fatal("writer lock did not honor the configured timeout")
	}
}

func BenchmarkHistoricalStateUpdateWith100KRecords(b *testing.B) {
	stateDir := b.TempDir()
	opts := historicalStateTestOptions(stateDir, false)
	if err := PrepareHistoricalBackfillState(opts, 1, ""); err != nil {
		b.Fatal(err)
	}
	store, err := openHistoricalStateStore(stateDir, opts.BatchID, false)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(historicalBucketSubmissions)
		for index := 0; index < 100_000; index++ {
			record := toolanswersheet.SubmissionRecord{LogicalID: fmt.Sprintf("seed-%d", index), Status: toolanswersheet.SubmissionStatusCompleted}
			if err := putJSON(bucket, []byte(record.LogicalID), record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	var sequence atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			index := sequence.Add(1)
			record := HistoricalLocalStageRecord{ScenarioID: fmt.Sprintf("scenario-%d", index), Stage: "answersheet_submit", PayloadHash: "stable", Status: "completed"}
			if err := store.putLocalStage(record); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
