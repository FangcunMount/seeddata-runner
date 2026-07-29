package dailysim

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	toolanswersheet "github.com/FangcunMount/seeddata-runner/internal/answersheet"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

const (
	historicalStateDBFilename = "historical-state-v2.db"
	historicalStateDBVersion  = 2
	historicalWriteBatchSize  = 64
	historicalWriteBatchWait  = 5 * time.Millisecond
)

var (
	historicalBucketMeta        = []byte("meta")
	historicalBucketTargets     = []byte("targets")
	historicalBucketPlans       = []byte("plans")
	historicalBucketScenarios   = []byte("scenarios")
	historicalBucketPlanTasks   = []byte("plan_tasks")
	historicalBucketDailyCounts = []byte("daily_counts")
	historicalBucketDayStates   = []byte("day_states")
	historicalBucketLocalStages = []byte("local_stages")
	historicalBucketSubmissions = []byte("submissions")
	historicalBucketDownstream  = []byte("downstream")
)

type historicalStateIdentity struct {
	Version int    `json:"version"`
	BatchID string `json:"batch_id"`
	OrgID   int64  `json:"org_id"`
	From    string `json:"from"`
	To      string `json:"to"`
}

type historicalStateMigration struct {
	CompletedAt     time.Time         `json:"completed_at"`
	LegacySHA256    map[string]string `json:"legacy_sha256,omitempty"`
	ScenarioCount   int               `json:"scenario_count"`
	PlanTaskCount   int               `json:"plan_task_count"`
	LocalStageCount int               `json:"local_stage_count"`
	SubmissionCount int               `json:"submission_count"`
}

type historicalStoredPlanTask struct {
	ParentScenarioID string                     `json:"parent_scenario_id"`
	Recovery         HistoricalPlanTaskRecovery `json:"recovery"`
}

type historicalStateWrite struct {
	apply func(*bolt.Tx) error
	done  chan error
}

type historicalStateStore struct {
	path      string
	db        *bolt.DB
	writes    chan historicalStateWrite
	closeOnce sync.Once
	wg        sync.WaitGroup
}

func historicalStateDBPath(stateDir, batchID string) string {
	checkpointPath, _ := historicalPaths(stateDir, batchID)
	return filepath.Join(filepath.Dir(checkpointPath), historicalStateDBFilename)
}

func openHistoricalStateStore(stateDir, batchID string, readOnly bool) (*historicalStateStore, error) {
	path := historicalStateDBPath(stateDir, batchID)
	if !readOnly {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create historical state directory: %w", err)
		}
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second, ReadOnly: readOnly})
	if err != nil {
		if errors.Is(err, berrors.ErrTimeout) {
			return nil, fmt.Errorf("historical batch state is locked by another writer: %w", err)
		}
		return nil, fmt.Errorf("open historical state %s: %w", path, err)
	}
	if !readOnly {
		if err := db.Update(createHistoricalBuckets); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("prepare historical state buckets: %w", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("protect historical state %s: %w", path, err)
		}
	}
	store := &historicalStateStore{path: path, db: db}
	if !readOnly {
		store.writes = make(chan historicalStateWrite, 256)
		store.wg.Add(1)
		go store.runWriter()
	}
	return store, nil
}

func (s *historicalStateStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		if s.writes != nil {
			close(s.writes)
			s.wg.Wait()
		}
		closeErr = s.db.Close()
	})
	return closeErr
}

func (s *historicalStateStore) runWriter() {
	defer s.wg.Done()
	for first := range s.writes {
		batch := []historicalStateWrite{first}
		timer := time.NewTimer(historicalWriteBatchWait)
	collect:
		for len(batch) < historicalWriteBatchSize {
			select {
			case write, ok := <-s.writes:
				if !ok {
					if !timer.Stop() {
						<-timer.C
					}
					s.commitWrites(batch)
					return
				}
				batch = append(batch, write)
			case <-timer.C:
				break collect
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		s.commitWrites(batch)
	}
}

func (s *historicalStateStore) commitWrites(batch []historicalStateWrite) {
	err := s.db.Update(func(tx *bolt.Tx) error {
		for _, write := range batch {
			if applyErr := write.apply(tx); applyErr != nil {
				return applyErr
			}
		}
		return nil
	})
	if err == nil {
		for _, write := range batch {
			write.done <- nil
		}
		return
	}
	// One conflicting operation must not prevent unrelated queued updates from
	// committing. Retry individually after the rolled-back aggregate transaction.
	for _, write := range batch {
		write.done <- s.db.Update(write.apply)
	}
}

func (s *historicalStateStore) update(apply func(*bolt.Tx) error) error {
	if s == nil || s.writes == nil {
		return fmt.Errorf("historical state store is not writable")
	}
	done := make(chan error, 1)
	s.writes <- historicalStateWrite{apply: apply, done: done}
	return <-done
}

func createHistoricalBuckets(tx *bolt.Tx) error {
	for _, name := range [][]byte{
		historicalBucketMeta, historicalBucketTargets, historicalBucketPlans,
		historicalBucketScenarios, historicalBucketPlanTasks, historicalBucketDailyCounts,
		historicalBucketDayStates,
		historicalBucketLocalStages, historicalBucketSubmissions, historicalBucketDownstream,
	} {
		if _, err := tx.CreateBucketIfNotExists(name); err != nil {
			return err
		}
	}
	return nil
}

func putJSON(bucket *bolt.Bucket, key []byte, value any) error {
	if bucket == nil {
		return fmt.Errorf("historical state bucket is missing")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return bucket.Put(key, data)
}

func getJSON(bucket *bolt.Bucket, key []byte, target any) (bool, error) {
	if bucket == nil {
		return false, nil
	}
	data := bucket.Get(key)
	if data == nil {
		return false, nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return false, err
	}
	return true, nil
}

func historicalManifestHeader(manifest HistoricalManifest) HistoricalManifest {
	manifest.Targets = nil
	manifest.Plans = nil
	manifest.Scenarios = nil
	manifest.DailyCounts = nil
	return manifest
}

func (s *historicalStateStore) loadIdentity() (historicalStateIdentity, error) {
	var identity historicalStateIdentity
	err := s.db.View(func(tx *bolt.Tx) error {
		exists, err := getJSON(tx.Bucket(historicalBucketMeta), []byte("identity"), &identity)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("historical state identity is missing")
		}
		return nil
	})
	return identity, err
}

func (s *historicalStateStore) loadManifest() (HistoricalManifest, error) {
	manifest := HistoricalManifest{
		Targets: make(map[string]HistoricalTargetManifest), Plans: make(map[string]HistoricalPlanManifest),
		Scenarios: make(map[string]HistoricalScenarioManifest), DailyCounts: make(map[string]int),
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		exists, err := getJSON(tx.Bucket(historicalBucketMeta), []byte("manifest_header"), &manifest)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("historical manifest metadata is missing")
		}
		manifest.Targets = make(map[string]HistoricalTargetManifest)
		manifest.Plans = make(map[string]HistoricalPlanManifest)
		manifest.Scenarios = make(map[string]HistoricalScenarioManifest)
		manifest.DailyCounts = make(map[string]int)
		if err := loadBucketMap(tx.Bucket(historicalBucketTargets), manifest.Targets); err != nil {
			return err
		}
		if err := loadBucketMap(tx.Bucket(historicalBucketPlans), manifest.Plans); err != nil {
			return err
		}
		if err := loadBucketMap(tx.Bucket(historicalBucketScenarios), manifest.Scenarios); err != nil {
			return err
		}
		planTasks := make(map[string]historicalStoredPlanTask)
		if err := loadBucketMap(tx.Bucket(historicalBucketPlanTasks), planTasks); err != nil {
			return err
		}
		orderedPlanTasks := make([]historicalStoredPlanTask, 0, len(planTasks))
		for _, stored := range planTasks {
			orderedPlanTasks = append(orderedPlanTasks, stored)
		}
		sort.Slice(orderedPlanTasks, func(left, right int) bool {
			if orderedPlanTasks[left].ParentScenarioID != orderedPlanTasks[right].ParentScenarioID {
				return orderedPlanTasks[left].ParentScenarioID < orderedPlanTasks[right].ParentScenarioID
			}
			if orderedPlanTasks[left].Recovery.PlannedAt != orderedPlanTasks[right].Recovery.PlannedAt {
				return orderedPlanTasks[left].Recovery.PlannedAt < orderedPlanTasks[right].Recovery.PlannedAt
			}
			return orderedPlanTasks[left].Recovery.TaskID < orderedPlanTasks[right].Recovery.TaskID
		})
		for _, stored := range orderedPlanTasks {
			parent := manifest.Scenarios[stored.ParentScenarioID]
			if strings.TrimSpace(parent.ScenarioID) == "" {
				return fmt.Errorf("historical plan task %s has no parent scenario %s", stored.Recovery.TaskID, stored.ParentScenarioID)
			}
			found := false
			for _, existing := range parent.PlanTaskRecoveries {
				if existing.TaskID != stored.Recovery.TaskID {
					continue
				}
				if existing != stored.Recovery {
					return fmt.Errorf("historical plan task recovery conflict for %s", stored.Recovery.TaskID)
				}
				found = true
				break
			}
			if !found {
				parent.PlanTaskRecoveries = append(parent.PlanTaskRecoveries, stored.Recovery)
			}
			parent.ChildScenarioIDs = appendUniqueString(parent.ChildScenarioIDs, stored.Recovery.ScenarioID)
			parent.TaskIDs = appendUniqueString(parent.TaskIDs, stored.Recovery.TaskID)
			manifest.Scenarios[stored.ParentScenarioID] = parent
		}
		return loadBucketMap(tx.Bucket(historicalBucketDailyCounts), manifest.DailyCounts)
	})
	return manifest, err
}

func loadBucketMap[T any](bucket *bolt.Bucket, target map[string]T) error {
	if bucket == nil {
		return nil
	}
	return bucket.ForEach(func(key, value []byte) error {
		var decoded T
		if err := json.Unmarshal(value, &decoded); err != nil {
			return fmt.Errorf("decode historical state key %q: %w", key, err)
		}
		target[string(key)] = decoded
		return nil
	})
}

func (s *historicalStateStore) loadCheckpoint() (HistoricalCheckpoint, error) {
	var checkpoint HistoricalCheckpoint
	err := s.db.View(func(tx *bolt.Tx) error {
		exists, err := getJSON(tx.Bucket(historicalBucketMeta), []byte("checkpoint"), &checkpoint)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("historical checkpoint is missing")
		}
		return nil
	})
	return normalizeHistoricalCheckpoint(checkpoint), err
}

func (s *historicalStateStore) putManifestHeader(manifest HistoricalManifest) error {
	return s.update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(historicalBucketMeta), []byte("manifest_header"), historicalManifestHeader(manifest))
	})
}

func (s *historicalStateStore) putTarget(key string, target HistoricalTargetManifest) error {
	return s.update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(historicalBucketTargets), []byte(key), target) })
}

func (s *historicalStateStore) putPlan(key string, plan HistoricalPlanManifest) error {
	return s.update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(historicalBucketPlans), []byte(key), plan) })
}

func (s *historicalStateStore) putScenario(scenario HistoricalScenarioManifest) error {
	if strings.TrimSpace(scenario.ScenarioID) == "" {
		return fmt.Errorf("historical scenario_id is required")
	}
	return s.update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(historicalBucketScenarios), []byte(scenario.ScenarioID), scenario)
	})
}

func (s *historicalStateStore) putPlanTask(parentScenarioID string, recovery HistoricalPlanTaskRecovery) error {
	parentScenarioID = strings.TrimSpace(parentScenarioID)
	if parentScenarioID == "" || strings.TrimSpace(recovery.TaskID) == "" || strings.TrimSpace(recovery.ScenarioID) == "" {
		return fmt.Errorf("historical plan task recovery identity is required")
	}
	key := []byte(parentScenarioID + "\x00" + recovery.TaskID)
	stored := historicalStoredPlanTask{ParentScenarioID: parentScenarioID, Recovery: recovery}
	return s.update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(historicalBucketPlanTasks)
		var existing historicalStoredPlanTask
		if found, err := getJSON(bucket, key, &existing); err != nil {
			return err
		} else if found && existing != stored {
			return fmt.Errorf("historical plan task recovery conflict for %s", recovery.TaskID)
		}
		return putJSON(bucket, key, stored)
	})
}

func (s *historicalStateStore) putDailyCount(day string, count int) error {
	return s.update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(historicalBucketDailyCounts), []byte(day), count) })
}

func (s *historicalStateStore) putDayState(day, status string) error {
	day, status = strings.TrimSpace(day), strings.TrimSpace(status)
	if day == "" || (status != "running" && status != "submitted" && status != "verified") {
		return fmt.Errorf("invalid historical day state %q=%q", day, status)
	}
	return s.update(func(tx *bolt.Tx) error {
		return tx.Bucket(historicalBucketDayStates).Put([]byte(day), []byte(status))
	})
}

func (s *historicalStateStore) putCheckpoint(checkpoint HistoricalCheckpoint) error {
	checkpoint = normalizeHistoricalCheckpoint(checkpoint)
	return s.update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(historicalBucketMeta), []byte("checkpoint"), checkpoint)
	})
}

func historicalLocalStageKey(scenarioID, stage string) []byte {
	return []byte(strings.TrimSpace(scenarioID) + "\x00" + strings.TrimSpace(stage))
}

func (s *historicalStateStore) putLocalStage(record HistoricalLocalStageRecord) error {
	if strings.TrimSpace(record.ScenarioID) == "" || strings.TrimSpace(record.Stage) == "" {
		return fmt.Errorf("historical local stage identity is required")
	}
	return s.update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(historicalBucketLocalStages)
		key := historicalLocalStageKey(record.ScenarioID, record.Stage)
		var existing HistoricalLocalStageRecord
		if found, err := getJSON(bucket, key, &existing); err != nil {
			return err
		} else if found && existing.PayloadHash != record.PayloadHash {
			return fmt.Errorf("historical local stage payload conflict: scenario=%s stage=%s", record.ScenarioID, record.Stage)
		}
		return putJSON(bucket, key, record)
	})
}

func (s *historicalStateStore) loadLocalStages(scenarioID string) (map[string]HistoricalLocalStageRecord, error) {
	result := make(map[string]HistoricalLocalStageRecord)
	prefix := []byte(strings.TrimSpace(scenarioID) + "\x00")
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(historicalBucketLocalStages)
		if bucket == nil {
			return nil
		}
		cursor := bucket.Cursor()
		for key, value := cursor.Seek(prefix); key != nil && strings.HasPrefix(string(key), string(prefix)); key, value = cursor.Next() {
			var record HistoricalLocalStageRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			result[record.Stage] = record
		}
		return nil
	})
	return result, err
}

func (s *historicalStateStore) loadAllLocalStages() (map[string]HistoricalLocalStageRecord, error) {
	result := make(map[string]HistoricalLocalStageRecord)
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(historicalBucketLocalStages)
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(key, value []byte) error {
			var record HistoricalLocalStageRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			result[string(key)] = record
			return nil
		})
	})
	return result, err
}

func (s *historicalStateStore) exportJSON(stateDir, batchID string) error {
	manifest, err := s.loadManifest()
	if err != nil {
		return err
	}
	checkpoint, err := s.loadCheckpoint()
	if err != nil {
		return err
	}
	checkpointPath, manifestPath := historicalPaths(stateDir, batchID)
	if err := saveSecureJSON(manifestPath, &manifest); err != nil {
		return err
	}
	return saveSecureJSON(checkpointPath, &checkpoint)
}

func persistHistoricalFrozenConfiguration(store *historicalStateStore, manifest HistoricalManifest) error {
	for key, target := range manifest.Targets {
		if err := store.putTarget(key, target); err != nil {
			return err
		}
	}
	for key, plan := range manifest.Plans {
		if err := store.putPlan(key, plan); err != nil {
			return err
		}
	}
	return store.putManifestHeader(manifest)
}

func persistHistoricalDay(store *historicalStateStore, manifest HistoricalManifest, day string) error {
	for _, scenario := range manifest.Scenarios {
		if scenario.BusinessDate != day && scenarioDateFromID(scenario.ScenarioID) != day {
			continue
		}
		if err := store.putScenario(scenario); err != nil {
			return err
		}
		for _, recovery := range scenario.PlanTaskRecoveries {
			if err := store.putPlanTask(scenario.ScenarioID, recovery); err != nil {
				return err
			}
		}
	}
	if count, ok := manifest.DailyCounts[day]; ok {
		if err := store.putDailyCount(day, count); err != nil {
			return err
		}
	}
	return store.putManifestHeader(manifest)
}

func persistHistoricalManifest(store *historicalStateStore, manifest HistoricalManifest) error {
	if err := persistHistoricalFrozenConfiguration(store, manifest); err != nil {
		return err
	}
	for _, scenarioID := range sortedScenarioIDs(manifest) {
		scenario := manifest.Scenarios[scenarioID]
		if err := store.putScenario(scenario); err != nil {
			return err
		}
		for _, recovery := range scenario.PlanTaskRecoveries {
			if err := store.putPlanTask(scenarioID, recovery); err != nil {
				return err
			}
		}
	}
	for day, count := range manifest.DailyCounts {
		if err := store.putDailyCount(day, count); err != nil {
			return err
		}
	}
	return nil
}

func (s *historicalStateStore) Get(logicalID string) (toolanswersheet.SubmissionRecord, bool, error) {
	var record toolanswersheet.SubmissionRecord
	var found bool
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		found, err = getJSON(tx.Bucket(historicalBucketSubmissions), []byte(strings.TrimSpace(logicalID)), &record)
		return err
	})
	return record, found, err
}

func (s *historicalStateStore) Prepare(logicalID string, payload any) (toolanswersheet.PreparedSubmission, error) {
	logicalID = strings.TrimSpace(logicalID)
	if logicalID == "" {
		return toolanswersheet.PreparedSubmission{}, fmt.Errorf("submission logical_id is required")
	}
	fingerprint, err := toolanswersheet.SubmissionFingerprint(payload)
	if err != nil {
		return toolanswersheet.PreparedSubmission{}, err
	}
	var prepared toolanswersheet.PreparedSubmission
	var operationErr error
	err = s.update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(historicalBucketSubmissions)
		var record toolanswersheet.SubmissionRecord
		exists, err := getJSON(bucket, []byte(logicalID), &record)
		if err != nil {
			return err
		}
		if exists && record.Status == toolanswersheet.SubmissionStatusConflict {
			prepared.Record = record
			operationErr = fmt.Errorf("%w: logical_id=%s", toolanswersheet.ErrSubmissionConflict, logicalID)
			return nil
		}
		if exists && record.Fingerprint != fingerprint {
			record.Status = toolanswersheet.SubmissionStatusConflict
			record.UpdatedAt = time.Now().UTC()
			prepared.Record = record
			if err := putJSON(bucket, []byte(logicalID), record); err != nil {
				return err
			}
			operationErr = fmt.Errorf("%w: logical_id=%s", toolanswersheet.ErrSubmissionConflict, logicalID)
			return nil
		}
		if exists && strings.TrimSpace(record.AnswerSheetID) != "" {
			prepared = toolanswersheet.PreparedSubmission{Record: record, ShouldSubmit: false}
			return nil
		}
		if !exists {
			record = toolanswersheet.SubmissionRecord{
				LogicalID: logicalID, IdempotencyKey: toolanswersheet.SubmissionIdempotencyKey("daily", logicalID), Fingerprint: fingerprint,
			}
			record.RequestID = uuid.NewString()
		}
		if strings.TrimSpace(record.RequestID) == "" {
			record.RequestID = uuid.NewString()
		}
		record.Status = toolanswersheet.SubmissionStatusPrepared
		record.UpdatedAt = time.Now().UTC()
		if err := putJSON(bucket, []byte(logicalID), record); err != nil {
			return err
		}
		prepared = toolanswersheet.PreparedSubmission{Record: record, ShouldSubmit: true}
		return nil
	})
	if err != nil {
		return prepared, err
	}
	return prepared, operationErr
}

func (s *historicalStateStore) updateSubmission(logicalID string, mutate func(*toolanswersheet.SubmissionRecord) error) (toolanswersheet.SubmissionRecord, error) {
	logicalID = strings.TrimSpace(logicalID)
	var result toolanswersheet.SubmissionRecord
	err := s.update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(historicalBucketSubmissions)
		var record toolanswersheet.SubmissionRecord
		found, err := getJSON(bucket, []byte(logicalID), &record)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("submission record %s not found", logicalID)
		}
		if err := mutate(&record); err != nil {
			return err
		}
		record.UpdatedAt = time.Now().UTC()
		result = record
		return putJSON(bucket, []byte(logicalID), record)
	})
	return result, err
}

func (s *historicalStateStore) MarkAccepted(logicalID, answerSheetID string) (toolanswersheet.SubmissionRecord, error) {
	return s.updateSubmission(logicalID, func(record *toolanswersheet.SubmissionRecord) error {
		if answerSheetID = strings.TrimSpace(answerSheetID); answerSheetID == "" {
			return fmt.Errorf("answersheet_id is required")
		}
		record.AnswerSheetID, record.Status = answerSheetID, toolanswersheet.SubmissionStatusAccepted
		return nil
	})
}

func (s *historicalStateStore) MarkAcceptedPending(logicalID string) (toolanswersheet.SubmissionRecord, error) {
	return s.updateSubmission(logicalID, func(record *toolanswersheet.SubmissionRecord) error {
		if strings.TrimSpace(record.AnswerSheetID) == "" {
			return fmt.Errorf("cannot mark submission pending without answersheet_id")
		}
		// A resumed submit path may rediscover a durable downstream record after
		// the reconciler has already completed it. Never move terminal submission
		// state backwards merely because the parent scenario is being restored.
		if record.Status == toolanswersheet.SubmissionStatusReady ||
			record.Status == toolanswersheet.SubmissionStatusCompleted ||
			record.Status == toolanswersheet.SubmissionStatusLegacy {
			return nil
		}
		record.Status = toolanswersheet.SubmissionStatusAcceptedPending
		return nil
	})
}

func (s *historicalStateStore) MarkReady(logicalID, assessmentID string) (toolanswersheet.SubmissionRecord, error) {
	return s.updateSubmission(logicalID, func(record *toolanswersheet.SubmissionRecord) error {
		if assessmentID = strings.TrimSpace(assessmentID); assessmentID == "" {
			return fmt.Errorf("assessment_id is required")
		}
		record.AssessmentID, record.Status = assessmentID, toolanswersheet.SubmissionStatusReady
		return nil
	})
}

func (s *historicalStateStore) MarkCompleted(logicalID, answerSheetID string) (toolanswersheet.SubmissionRecord, error) {
	return s.updateSubmission(logicalID, func(record *toolanswersheet.SubmissionRecord) error {
		if answerSheetID = strings.TrimSpace(answerSheetID); answerSheetID == "" {
			return fmt.Errorf("answersheet_id is required")
		}
		record.AnswerSheetID, record.Status = answerSheetID, toolanswersheet.SubmissionStatusCompleted
		return nil
	})
}

func (s *historicalStateStore) MarkConflict(logicalID string) (toolanswersheet.SubmissionRecord, error) {
	return s.updateSubmission(logicalID, func(record *toolanswersheet.SubmissionRecord) error {
		record.Status = toolanswersheet.SubmissionStatusConflict
		return nil
	})
}

func (s *historicalStateStore) ReconcileLegacy(logicalID, answerSheetID string, payload any) (toolanswersheet.SubmissionRecord, error) {
	prepared, err := s.Prepare(logicalID, payload)
	if err != nil {
		return toolanswersheet.SubmissionRecord{}, err
	}
	return s.updateSubmission(logicalID, func(record *toolanswersheet.SubmissionRecord) error {
		record.IdempotencyKey = prepared.Record.IdempotencyKey
		if record.AnswerSheetID = strings.TrimSpace(answerSheetID); record.AnswerSheetID == "" {
			return fmt.Errorf("answersheet_id is required")
		}
		record.Status = toolanswersheet.SubmissionStatusLegacy
		return nil
	})
}

func hashExistingFile(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true, nil
}

func sortedLegacyStateHashes(paths []string) (map[string]string, error) {
	result := make(map[string]string)
	for _, path := range paths {
		hash, exists, err := hashExistingFile(path)
		if err != nil {
			return nil, err
		}
		if exists {
			result[path] = hash
		}
	}
	return result, nil
}

func legacyHistoricalStatePaths(stateDir, batchID, submissionPath string) ([]string, error) {
	checkpointPath, manifestPath := historicalPaths(stateDir, batchID)
	paths := []string{checkpointPath, manifestPath}
	if strings.TrimSpace(submissionPath) != "" {
		paths = append(paths, submissionPath)
	}
	stageRoot := filepath.Join(filepath.Dir(checkpointPath), "stages")
	err := filepath.WalkDir(stageRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

// PrepareHistoricalBackfillState creates or migrates the v2 state before API
// dependencies are initialized. This guarantees migration conflicts cannot be
// followed by an IAM login or business HTTP side effect.
func PrepareHistoricalBackfillState(opts HistoricalBackfillOptions, orgID int64, legacySubmissionPath string) error {
	location, err := time.LoadLocation(historicalTimezone)
	if err != nil {
		return err
	}
	from, to, err := parseHistoricalDateRange(opts.From, opts.To, location)
	if err != nil {
		return err
	}
	opts.From, opts.To, opts.BatchID = from.Format("2006-01-02"), to.Format("2006-01-02"), strings.TrimSpace(opts.BatchID)
	if opts.BatchID == "" || orgID <= 0 {
		return fmt.Errorf("historical batch_id and positive org_id are required")
	}
	path := historicalStateDBPath(opts.StateDir, opts.BatchID)
	if _, err := os.Stat(path); err == nil {
		store, openErr := openHistoricalStateStore(opts.StateDir, opts.BatchID, false)
		if openErr != nil {
			return openErr
		}
		defer func() { _ = store.Close() }()
		identity, loadErr := store.loadIdentity()
		if loadErr != nil {
			return loadErr
		}
		expected := historicalStateIdentity{Version: historicalStateDBVersion, BatchID: opts.BatchID, OrgID: orgID, From: opts.From, To: opts.To}
		if identity != expected {
			return fmt.Errorf("historical state identity conflict: stored=%+v requested=%+v", identity, expected)
		}
		manifest, loadErr := store.loadManifest()
		if loadErr != nil {
			return loadErr
		}
		if err := validateHistoricalManifestVersion(manifest); err != nil {
			return err
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	checkpointPath, manifestPath := historicalPaths(opts.StateDir, opts.BatchID)
	legacyPaths, err := legacyHistoricalStatePaths(opts.StateDir, opts.BatchID, legacySubmissionPath)
	if err != nil {
		return err
	}
	beforeHashes, err := sortedLegacyStateHashes(legacyPaths)
	if err != nil {
		return fmt.Errorf("hash legacy historical state: %w", err)
	}

	manifest := HistoricalManifest{}
	manifestExists, err := loadSecureJSON(manifestPath, &manifest)
	if err != nil {
		return err
	}
	checkpoint := HistoricalCheckpoint{}
	checkpointExists, err := loadSecureJSON(checkpointPath, &checkpoint)
	if err != nil {
		return err
	}
	if manifestExists && (manifest.BatchID != opts.BatchID || manifest.OrgID != orgID || manifest.From != opts.From || manifest.To != opts.To) {
		return fmt.Errorf("historical manifest identity conflicts with requested batch/range/org")
	}
	if manifestExists {
		if err := validateHistoricalManifestVersion(manifest); err != nil {
			return err
		}
	}
	if checkpointExists && (checkpoint.BatchID != opts.BatchID || checkpoint.From != opts.From || checkpoint.To != opts.To) {
		return fmt.Errorf("historical checkpoint identity conflicts with requested batch/range")
	}
	if !opts.Resume && (manifestExists || checkpointExists) {
		return fmt.Errorf("legacy historical state exists for batch %s; use --resume to migrate it", opts.BatchID)
	}
	now := time.Now().UTC()
	if !manifestExists {
		manifest = HistoricalManifest{Version: historicalManifestVersion, BatchID: opts.BatchID, OrgID: orgID, From: opts.From, To: opts.To, Timezone: historicalTimezone, CreatedAt: now, UpdatedAt: now}
	}
	if manifest.Targets == nil {
		manifest.Targets = make(map[string]HistoricalTargetManifest)
	}
	if manifest.Plans == nil {
		manifest.Plans = make(map[string]HistoricalPlanManifest)
	}
	if manifest.Scenarios == nil {
		manifest.Scenarios = make(map[string]HistoricalScenarioManifest)
	}
	if manifest.DailyCounts == nil {
		manifest.DailyCounts = make(map[string]int)
	}
	if !checkpointExists {
		checkpoint = HistoricalCheckpoint{Version: 2, BatchID: opts.BatchID, From: opts.From, To: opts.To}
	}
	checkpoint = normalizeHistoricalCheckpoint(checkpoint)
	localStages, err := loadAllHistoricalLocalStages(opts.StateDir, opts.BatchID)
	if err != nil {
		return err
	}

	legacySubmissions := make(map[string]toolanswersheet.SubmissionRecord)
	if strings.TrimSpace(legacySubmissionPath) != "" {
		ledger, ledgerErr := toolanswersheet.NewSubmissionLedger(legacySubmissionPath, "daily")
		if ledgerErr != nil {
			return ledgerErr
		}
		all, ledgerErr := ledger.ExportRecords()
		if ledgerErr != nil {
			return ledgerErr
		}
		testeeIDs := make(map[string]struct{})
		for _, scenario := range manifest.Scenarios {
			if id := strings.TrimSpace(scenario.TesteeID); id != "" {
				testeeIDs[id] = struct{}{}
			}
		}
		for _, stage := range localStages {
			if id := strings.TrimSpace(stage.TesteeID); id != "" {
				testeeIDs[id] = struct{}{}
			}
		}
		for logicalID, record := range all {
			parts := strings.Split(logicalID, "|")
			if len(parts) < 4 || parts[0] != "daily" {
				continue
			}
			if _, belongs := testeeIDs[parts[3]]; belongs {
				legacySubmissions[logicalID] = record
				continue
			}
			if record.Status == toolanswersheet.SubmissionStatusAcceptedPending && legacySubmissionMatchesHistoricalScenario(parts, manifest) {
				return fmt.Errorf("accepted_pending submission %s cannot be attributed to a historical Testee", logicalID)
			}
		}
	}
	if err := validateLegacyHistoricalMigration(manifest, localStages, legacySubmissions, from, to); err != nil {
		return err
	}

	afterHashes, err := sortedLegacyStateHashes(legacyPaths)
	if err != nil {
		return err
	}
	if !sameStringMap(beforeHashes, afterHashes) {
		return fmt.Errorf("legacy historical state changed during migration")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmpPath := path + ".migrating-" + uuid.NewString()
	db, err := bolt.Open(tmpPath, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return err
	}
	identity := historicalStateIdentity{Version: historicalStateDBVersion, BatchID: opts.BatchID, OrgID: orgID, From: opts.From, To: opts.To}
	planTaskCount := 0
	for _, scenario := range manifest.Scenarios {
		planTaskCount += len(scenario.PlanTaskRecoveries)
	}
	migration := historicalStateMigration{CompletedAt: now, LegacySHA256: beforeHashes, ScenarioCount: len(manifest.Scenarios), PlanTaskCount: planTaskCount, LocalStageCount: len(localStages), SubmissionCount: len(legacySubmissions)}
	err = db.Update(func(tx *bolt.Tx) error {
		if err := createHistoricalBuckets(tx); err != nil {
			return err
		}
		meta := tx.Bucket(historicalBucketMeta)
		if err := putJSON(meta, []byte("identity"), identity); err != nil {
			return err
		}
		if err := putJSON(meta, []byte("manifest_header"), historicalManifestHeader(manifest)); err != nil {
			return err
		}
		if err := putJSON(meta, []byte("checkpoint"), checkpoint); err != nil {
			return err
		}
		if err := putJSON(meta, []byte("migration"), migration); err != nil {
			return err
		}
		for key, value := range manifest.Targets {
			if err := putJSON(tx.Bucket(historicalBucketTargets), []byte(key), value); err != nil {
				return err
			}
		}
		for key, value := range manifest.Plans {
			if err := putJSON(tx.Bucket(historicalBucketPlans), []byte(key), value); err != nil {
				return err
			}
		}
		for key, value := range manifest.Scenarios {
			if err := putJSON(tx.Bucket(historicalBucketScenarios), []byte(key), value); err != nil {
				return err
			}
			for _, recovery := range value.PlanTaskRecoveries {
				stored := historicalStoredPlanTask{ParentScenarioID: key, Recovery: recovery}
				if err := putJSON(tx.Bucket(historicalBucketPlanTasks), []byte(key+"\x00"+recovery.TaskID), stored); err != nil {
					return err
				}
			}
		}
		for key, value := range manifest.DailyCounts {
			if err := putJSON(tx.Bucket(historicalBucketDailyCounts), []byte(key), value); err != nil {
				return err
			}
		}
		for key, value := range localStages {
			if err := putJSON(tx.Bucket(historicalBucketLocalStages), []byte(key), value); err != nil {
				return err
			}
		}
		for key, value := range legacySubmissions {
			if err := putJSON(tx.Bucket(historicalBucketSubmissions), []byte(key), value); err != nil {
				return err
			}
		}
		return nil
	})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write migrated historical state: %w", err)
	}
	finalHashes, err := sortedLegacyStateHashes(legacyPaths)
	if err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if !sameStringMap(beforeHashes, finalHashes) {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("legacy historical state changed before migration activation")
	}
	if err := validateMigratedHistoricalState(tmpPath, identity, migration); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("activate migrated historical state: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func validateLegacyHistoricalMigration(
	manifest HistoricalManifest,
	localStages map[string]HistoricalLocalStageRecord,
	submissions map[string]toolanswersheet.SubmissionRecord,
	from, to time.Time,
) error {
	for key, scenario := range manifest.Scenarios {
		if strings.TrimSpace(key) == "" || scenario.ScenarioID != key {
			return fmt.Errorf("legacy historical scenario identity conflict for key %q", key)
		}
		date, err := time.ParseInLocation("2006-01-02", scenarioDateFromID(key), from.Location())
		if err != nil || date.Before(from) || date.After(to) {
			return fmt.Errorf("legacy historical scenario %s is outside the requested date range", key)
		}
		if strings.TrimSpace(scenario.BusinessDate) == "" || strings.TrimSpace(scenario.TargetKey) == "" {
			return fmt.Errorf("legacy historical scenario %s is missing business identity", key)
		}
	}
	for key, stage := range localStages {
		expectedKey := stage.ScenarioID + "\x00" + stage.Stage
		if key != expectedKey || stage.Status != "completed" || strings.TrimSpace(stage.PayloadHash) == "" {
			return fmt.Errorf("legacy historical local stage %q is invalid", key)
		}
	}
	for key, record := range submissions {
		if record.LogicalID != key || strings.TrimSpace(record.Fingerprint) == "" || strings.TrimSpace(record.IdempotencyKey) == "" || strings.TrimSpace(record.RequestID) == "" {
			return fmt.Errorf("legacy historical submission %q is missing durable identity", key)
		}
	}
	return nil
}

func legacySubmissionMatchesHistoricalScenario(parts []string, manifest HistoricalManifest) bool {
	if len(parts) < 3 {
		return false
	}
	date, err := time.Parse("20060102", parts[1])
	if err != nil {
		return false
	}
	prefix := date.Format("2006-01-02") + "/" + parts[2] + "/"
	for scenarioID := range manifest.Scenarios {
		if strings.HasPrefix(scenarioID, prefix) {
			return true
		}
	}
	return false
}

func validateMigratedHistoricalState(path string, identity historicalStateIdentity, migration historicalStateMigration) error {
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return db.View(func(tx *bolt.Tx) error {
		var actual historicalStateIdentity
		found, err := getJSON(tx.Bucket(historicalBucketMeta), []byte("identity"), &actual)
		if err != nil {
			return fmt.Errorf("validate migrated historical identity: %w", err)
		}
		if !found {
			return fmt.Errorf("validate migrated historical identity: missing identity")
		}
		if actual != identity {
			return fmt.Errorf("migrated historical identity mismatch")
		}
		checks := []struct {
			bucket []byte
			want   int
		}{
			{historicalBucketScenarios, migration.ScenarioCount},
			{historicalBucketPlanTasks, migration.PlanTaskCount},
			{historicalBucketLocalStages, migration.LocalStageCount},
			{historicalBucketSubmissions, migration.SubmissionCount},
		}
		for _, check := range checks {
			bucket := tx.Bucket(check.bucket)
			if bucket == nil {
				return fmt.Errorf("migrated historical state bucket %s is missing", check.bucket)
			}
			if bucket.Stats().KeyN != check.want {
				return fmt.Errorf("migrated historical state count mismatch for %s: got=%d want=%d", check.bucket, bucket.Stats().KeyN, check.want)
			}
		}
		return nil
	})
}

func sortedScenarioIDs(manifest HistoricalManifest) []string {
	ids := make([]string, 0, len(manifest.Scenarios))
	for id := range manifest.Scenarios {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
