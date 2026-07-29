package dailysim

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

type historicalDownstreamStatus string

const (
	historicalDownstreamSubmitted historicalDownstreamStatus = "submitted"
	historicalDownstreamPending   historicalDownstreamStatus = "downstream_pending"
	historicalDownstreamVerified  historicalDownstreamStatus = "verified"
)

type historicalDownstreamRecord struct {
	LogicalID          string                     `json:"logical_id"`
	ScenarioID         string                     `json:"scenario_id"`
	BusinessDate       string                     `json:"business_date"`
	TaskID             string                     `json:"task_id,omitempty"`
	TargetKey          string                     `json:"target_key"`
	AnswerSheetID      string                     `json:"answersheet_id"`
	TesteeID           uint64                     `json:"testee_id"`
	RequiresAssessment bool                       `json:"requires_assessment"`
	Status             historicalDownstreamStatus `json:"status"`
	AssessmentID       string                     `json:"assessment_id,omitempty"`
	OutcomeID          string                     `json:"outcome_id,omitempty"`
	ReportID           string                     `json:"report_id,omitempty"`
	PollCount          int                        `json:"poll_count"`
	LastError          string                     `json:"last_error,omitempty"`
	NextAttemptAt      time.Time                  `json:"next_attempt_at"`
	SubmittedAt        time.Time                  `json:"submitted_at"`
	VerifiedAt         *time.Time                 `json:"verified_at,omitempty"`
	UpdatedAt          time.Time                  `json:"updated_at"`
}

func (r historicalDownstreamRecord) validateIdentity() error {
	if strings.TrimSpace(r.LogicalID) == "" || strings.TrimSpace(r.ScenarioID) == "" ||
		strings.TrimSpace(r.BusinessDate) == "" || strings.TrimSpace(r.TargetKey) == "" ||
		strings.TrimSpace(r.AnswerSheetID) == "" || r.TesteeID == 0 {
		return fmt.Errorf("historical downstream identity is incomplete")
	}
	return nil
}

func (s *historicalStateStore) putDownstreamSubmitted(record historicalDownstreamRecord) error {
	if err := record.validateIdentity(); err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(historicalBucketDownstream)
		var existing historicalDownstreamRecord
		found, err := getJSON(bucket, []byte(record.LogicalID), &existing)
		if err != nil {
			return err
		}
		if found {
			if existing.ScenarioID != record.ScenarioID || existing.TaskID != record.TaskID ||
				existing.TargetKey != record.TargetKey || existing.AnswerSheetID != record.AnswerSheetID ||
				existing.TesteeID != record.TesteeID || existing.RequiresAssessment != record.RequiresAssessment {
				return fmt.Errorf("historical downstream identity conflict for %s", record.LogicalID)
			}
			return nil
		}
		record.Status = historicalDownstreamSubmitted
		record.SubmittedAt = now
		record.UpdatedAt = now
		record.NextAttemptAt = now
		return putJSON(bucket, []byte(record.LogicalID), record)
	})
}

func (s *historicalStateStore) listDueDownstream(now time.Time, limit int) ([]historicalDownstreamRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	result := make([]historicalDownstreamRecord, 0, limit)
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(historicalBucketDownstream)
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(_, value []byte) error {
			if len(result) >= limit {
				return nil
			}
			var record historicalDownstreamRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			if record.Status == historicalDownstreamVerified || record.NextAttemptAt.After(now) {
				return nil
			}
			result = append(result, record)
			return nil
		})
	})
	sort.Slice(result, func(i, j int) bool {
		if !result[i].NextAttemptAt.Equal(result[j].NextAttemptAt) {
			return result[i].NextAttemptAt.Before(result[j].NextAttemptAt)
		}
		return result[i].LogicalID < result[j].LogicalID
	})
	return result, err
}

func (s *historicalStateStore) markDownstreamPending(logicalID, lastError string, nextAttemptAt time.Time) error {
	return s.update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(historicalBucketDownstream)
		var record historicalDownstreamRecord
		found, err := getJSON(bucket, []byte(strings.TrimSpace(logicalID)), &record)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("historical downstream record %s not found", logicalID)
		}
		if record.Status == historicalDownstreamVerified {
			return nil
		}
		record.Status = historicalDownstreamPending
		record.PollCount++
		record.LastError = strings.TrimSpace(lastError)
		record.NextAttemptAt = nextAttemptAt.UTC()
		record.UpdatedAt = time.Now().UTC()
		return putJSON(bucket, []byte(record.LogicalID), record)
	})
}

func (s *historicalStateStore) markDownstreamVerified(
	logicalID, assessmentID, outcomeID, reportID string,
) error {
	return s.update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(historicalBucketDownstream)
		var record historicalDownstreamRecord
		found, err := getJSON(bucket, []byte(strings.TrimSpace(logicalID)), &record)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("historical downstream record %s not found", logicalID)
		}
		now := time.Now().UTC()
		record.Status = historicalDownstreamVerified
		record.AssessmentID = strings.TrimSpace(assessmentID)
		record.OutcomeID = strings.TrimSpace(outcomeID)
		record.ReportID = strings.TrimSpace(reportID)
		record.LastError = ""
		record.NextAttemptAt = time.Time{}
		record.VerifiedAt = &now
		record.UpdatedAt = now
		return putJSON(bucket, []byte(record.LogicalID), record)
	})
}

func (s *historicalStateStore) countOutstandingDownstream(day string) (int, error) {
	day = strings.TrimSpace(day)
	count := 0
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(historicalBucketDownstream)
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(_, value []byte) error {
			var record historicalDownstreamRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			if record.Status != historicalDownstreamVerified && (day == "" || record.BusinessDate == day) {
				count++
			}
			return nil
		})
	})
	return count, err
}

func (s *historicalStateStore) loadDownstream(logicalID string) (historicalDownstreamRecord, bool, error) {
	var record historicalDownstreamRecord
	var found bool
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		found, err = getJSON(tx.Bucket(historicalBucketDownstream), []byte(strings.TrimSpace(logicalID)), &record)
		return err
	})
	return record, found, err
}
