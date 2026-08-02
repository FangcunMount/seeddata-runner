package answersheet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const completedSubmissionRetention = 30 * 24 * time.Hour

var ErrSubmissionConflict = errors.New("submission payload conflicts with prepared identity")

type SubmissionStatus string

const (
	SubmissionStatusPrepared        SubmissionStatus = "prepared"
	SubmissionStatusAccepted        SubmissionStatus = "accepted"
	SubmissionStatusAcceptedPending SubmissionStatus = "accepted_pending"
	SubmissionStatusReady           SubmissionStatus = "ready"
	SubmissionStatusCompleted       SubmissionStatus = "completed"
	SubmissionStatusLegacy          SubmissionStatus = "legacy_reconciled"
	SubmissionStatusConflict        SubmissionStatus = "conflict"
)

type SubmissionRecord struct {
	LogicalID      string           `json:"logical_id"`
	IdempotencyKey string           `json:"idempotency_key"`
	Fingerprint    string           `json:"fingerprint"`
	RequestID      string           `json:"request_id,omitempty"`
	Status         SubmissionStatus `json:"status"`
	AnswerSheetID  string           `json:"answersheet_id,omitempty"`
	AssessmentID   string           `json:"assessment_id,omitempty"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

type PreparedSubmission struct {
	Record       SubmissionRecord
	ShouldSubmit bool
}

type submissionLedgerState struct {
	Records map[string]SubmissionRecord `json:"records"`
}

// SubmissionLedger persists submission identity before any HTTP side effect.
type SubmissionLedger struct {
	path   string
	mode   string
	mu     sync.Mutex
	now    func() time.Time
	rename func(string, string) error
}

func NewSubmissionLedger(path, mode string) (*SubmissionLedger, error) {
	path = strings.TrimSpace(path)
	mode = strings.TrimSpace(mode)
	if path == "" {
		return nil, fmt.Errorf("submission ledger path is required")
	}
	if mode != "daily" && mode != "plan" {
		return nil, fmt.Errorf("unsupported submission ledger mode %q", mode)
	}
	return &SubmissionLedger{path: path, mode: mode, now: time.Now, rename: os.Rename}, nil
}

func (l *SubmissionLedger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func (l *SubmissionLedger) Prepare(logicalID string, payload any) (PreparedSubmission, error) {
	if l == nil {
		return PreparedSubmission{}, fmt.Errorf("submission ledger is nil")
	}
	logicalID = strings.TrimSpace(logicalID)
	if logicalID == "" {
		return PreparedSubmission{}, fmt.Errorf("submission logical_id is required")
	}
	fingerprint, err := submissionFingerprint(payload)
	if err != nil {
		return PreparedSubmission{}, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	state, err := l.loadLocked()
	if err != nil {
		return PreparedSubmission{}, err
	}
	now := l.now().UTC()
	l.pruneLocked(state, now)
	record, exists := state.Records[logicalID]
	if exists && record.Status == SubmissionStatusConflict {
		return PreparedSubmission{Record: record}, fmt.Errorf("%w: logical_id=%s", ErrSubmissionConflict, logicalID)
	}
	if exists && record.Fingerprint != fingerprint {
		record.Status = SubmissionStatusConflict
		record.UpdatedAt = now
		state.Records[logicalID] = record
		if saveErr := l.saveLocked(state); saveErr != nil {
			return PreparedSubmission{}, saveErr
		}
		return PreparedSubmission{Record: record}, fmt.Errorf("%w: logical_id=%s", ErrSubmissionConflict, logicalID)
	}
	if exists && strings.TrimSpace(record.AnswerSheetID) != "" {
		return PreparedSubmission{Record: record, ShouldSubmit: false}, nil
	}
	if !exists {
		record = SubmissionRecord{
			LogicalID:      logicalID,
			IdempotencyKey: submissionIdempotencyKey(l.mode, logicalID),
			Fingerprint:    fingerprint,
		}
	}
	record.RequestID = uuid.NewString()
	record.Status = SubmissionStatusPrepared
	record.UpdatedAt = now
	state.Records[logicalID] = record
	if err := l.saveLocked(state); err != nil {
		return PreparedSubmission{}, err
	}
	return PreparedSubmission{Record: record, ShouldSubmit: true}, nil
}

func (l *SubmissionLedger) Get(logicalID string) (SubmissionRecord, bool, error) {
	if l == nil {
		return SubmissionRecord{}, false, fmt.Errorf("submission ledger is nil")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state, err := l.loadLocked()
	if err != nil {
		return SubmissionRecord{}, false, err
	}
	record, ok := state.Records[strings.TrimSpace(logicalID)]
	return record, ok, nil
}

func (l *SubmissionLedger) MarkAccepted(logicalID, answerSheetID string) (SubmissionRecord, error) {
	return l.update(logicalID, func(record *SubmissionRecord) error {
		answerSheetID = strings.TrimSpace(answerSheetID)
		if answerSheetID == "" {
			return fmt.Errorf("answersheet_id is required")
		}
		record.AnswerSheetID = answerSheetID
		record.Status = SubmissionStatusAccepted
		return nil
	})
}

func (l *SubmissionLedger) MarkAcceptedPending(logicalID string) (SubmissionRecord, error) {
	return l.update(logicalID, func(record *SubmissionRecord) error {
		if strings.TrimSpace(record.AnswerSheetID) == "" {
			return fmt.Errorf("cannot mark submission pending without answersheet_id")
		}
		record.Status = SubmissionStatusAcceptedPending
		return nil
	})
}

func (l *SubmissionLedger) MarkReady(logicalID, assessmentID string) (SubmissionRecord, error) {
	return l.update(logicalID, func(record *SubmissionRecord) error {
		assessmentID = strings.TrimSpace(assessmentID)
		if assessmentID == "" {
			return fmt.Errorf("assessment_id is required")
		}
		record.AssessmentID = assessmentID
		record.Status = SubmissionStatusReady
		return nil
	})
}

func (l *SubmissionLedger) MarkCompleted(logicalID, answerSheetID string) (SubmissionRecord, error) {
	return l.update(logicalID, func(record *SubmissionRecord) error {
		answerSheetID = strings.TrimSpace(answerSheetID)
		if answerSheetID == "" {
			return fmt.Errorf("answersheet_id is required")
		}
		record.AnswerSheetID = answerSheetID
		record.Status = SubmissionStatusCompleted
		return nil
	})
}

func (l *SubmissionLedger) MarkConflict(logicalID string) (SubmissionRecord, error) {
	return l.update(logicalID, func(record *SubmissionRecord) error {
		record.Status = SubmissionStatusConflict
		return nil
	})
}

func (l *SubmissionLedger) ReconcileLegacy(logicalID, answerSheetID string, payload any) (SubmissionRecord, error) {
	prepared, err := l.Prepare(logicalID, payload)
	if err != nil {
		return SubmissionRecord{}, err
	}
	return l.update(logicalID, func(record *SubmissionRecord) error {
		record.IdempotencyKey = prepared.Record.IdempotencyKey
		record.AnswerSheetID = strings.TrimSpace(answerSheetID)
		if record.AnswerSheetID == "" {
			return fmt.Errorf("answersheet_id is required")
		}
		record.Status = SubmissionStatusLegacy
		return nil
	})
}

func (l *SubmissionLedger) update(logicalID string, mutate func(*SubmissionRecord) error) (SubmissionRecord, error) {
	if l == nil {
		return SubmissionRecord{}, fmt.Errorf("submission ledger is nil")
	}
	logicalID = strings.TrimSpace(logicalID)
	l.mu.Lock()
	defer l.mu.Unlock()
	state, err := l.loadLocked()
	if err != nil {
		return SubmissionRecord{}, err
	}
	record, ok := state.Records[logicalID]
	if !ok {
		return SubmissionRecord{}, fmt.Errorf("submission record %s not found", logicalID)
	}
	if err := mutate(&record); err != nil {
		return SubmissionRecord{}, err
	}
	record.UpdatedAt = l.now().UTC()
	state.Records[logicalID] = record
	if err := l.saveLocked(state); err != nil {
		return SubmissionRecord{}, err
	}
	return record, nil
}

func submissionFingerprint(payload any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode submission fingerprint payload: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func submissionIdempotencyKey(mode, logicalID string) string {
	sum := sha256.Sum256([]byte(logicalID))
	return "seed." + mode + "." + hex.EncodeToString(sum[:])
}

func (l *SubmissionLedger) loadLocked() (*submissionLedgerState, error) {
	data, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &submissionLedgerState{Records: make(map[string]SubmissionRecord)}, nil
		}
		return nil, fmt.Errorf("read submission ledger %s: %w", l.path, err)
	}
	var state submissionLedgerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode submission ledger %s: %w", l.path, err)
	}
	if state.Records == nil {
		state.Records = make(map[string]SubmissionRecord)
	}
	return &state, nil
}

func (l *SubmissionLedger) saveLocked(state *submissionLedgerState) error {
	if state == nil {
		return fmt.Errorf("submission ledger state is nil")
	}
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create submission ledger directory %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode submission ledger %s: %w", l.path, err)
	}
	tmp, err := os.CreateTemp(dir, ".submission-ledger-*")
	if err != nil {
		return fmt.Errorf("create temporary submission ledger: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temporary submission ledger: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temporary submission ledger: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temporary submission ledger: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temporary submission ledger: %w", err)
	}
	if err := l.rename(tmpPath, l.path); err != nil {
		cleanup()
		return fmt.Errorf("replace submission ledger %s: %w", l.path, err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open submission ledger directory %s: %w", dir, err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return fmt.Errorf("sync submission ledger directory %s: %w", dir, err)
	}
	return directory.Close()
}

func (l *SubmissionLedger) pruneLocked(state *submissionLedgerState, now time.Time) {
	for key, record := range state.Records {
		if record.Status != SubmissionStatusReady && record.Status != SubmissionStatusCompleted && record.Status != SubmissionStatusLegacy {
			continue
		}
		if !record.UpdatedAt.IsZero() && now.Sub(record.UpdatedAt) > completedSubmissionRetention {
			delete(state.Records, key)
		}
	}
}
