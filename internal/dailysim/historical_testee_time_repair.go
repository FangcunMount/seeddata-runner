package dailysim

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const historicalTesteeTimeRepairConfirmation = "REPAIR_HISTORICAL_TESTEE_CREATED_AT"

var mysqlDatabaseNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

type HistoricalTesteeCreatedAtRepair struct {
	TesteeID  uint64
	CreatedAt time.Time
}

// BuildHistoricalTesteeCreatedAtRepairs derives exact, batch-owned Testee
// timestamps from the deterministic scenario identity recorded in manifest.
func BuildHistoricalTesteeCreatedAtRepairs(manifest HistoricalManifest) ([]HistoricalTesteeCreatedAtRepair, error) {
	if strings.TrimSpace(manifest.BatchID) == "" || manifest.OrgID <= 0 {
		return nil, fmt.Errorf("historical manifest batch_id and positive org_id are required")
	}
	locationName := strings.TrimSpace(manifest.Timezone)
	if locationName == "" {
		locationName = historicalTimezone
	}
	location, err := time.LoadLocation(locationName)
	if err != nil {
		return nil, fmt.Errorf("load historical manifest timezone: %w", err)
	}

	byTesteeID := make(map[uint64]time.Time)
	for _, scenario := range manifest.Scenarios {
		if !scenario.TesteeCreated || strings.TrimSpace(scenario.TesteeID) == "" {
			continue
		}
		testeeID, err := strconv.ParseUint(strings.TrimSpace(scenario.TesteeID), 10, 64)
		if err != nil || testeeID == 0 {
			return nil, fmt.Errorf("scenario %s has invalid testee_id %q", scenario.ScenarioID, scenario.TesteeID)
		}
		businessDate, index, err := historicalScenarioDateAndIndex(scenario, location)
		if err != nil {
			return nil, err
		}
		timeline := BuildHistoricalTimeline(manifest.BatchID, businessDate, index)
		if timeline.TesteeCreatedAt == nil || timeline.TesteeCreatedAt.IsZero() {
			return nil, fmt.Errorf("scenario %s has no deterministic testee_created_at", scenario.ScenarioID)
		}
		createdAt := timeline.TesteeCreatedAt.In(location)
		if existing, ok := byTesteeID[testeeID]; ok && !existing.Equal(createdAt) {
			return nil, fmt.Errorf("testee %d maps to conflicting historical timestamps %s and %s", testeeID, existing, createdAt)
		}
		byTesteeID[testeeID] = createdAt
	}
	if len(byTesteeID) == 0 {
		return nil, fmt.Errorf("historical manifest has no batch-owned Testee timestamps to repair")
	}

	repairs := make([]HistoricalTesteeCreatedAtRepair, 0, len(byTesteeID))
	for testeeID, createdAt := range byTesteeID {
		repairs = append(repairs, HistoricalTesteeCreatedAtRepair{TesteeID: testeeID, CreatedAt: createdAt})
	}
	sort.Slice(repairs, func(i, j int) bool { return repairs[i].TesteeID < repairs[j].TesteeID })
	return repairs, nil
}

func historicalScenarioDateAndIndex(scenario HistoricalScenarioManifest, location *time.Location) (time.Time, int, error) {
	parts := strings.SplitN(strings.TrimSpace(scenario.ScenarioID), "/", 4)
	if len(parts) < 4 {
		return time.Time{}, 0, fmt.Errorf("invalid historical scenario_id %q", scenario.ScenarioID)
	}
	businessDate := strings.TrimSpace(scenario.BusinessDate)
	if parts[0] != businessDate {
		return time.Time{}, 0, fmt.Errorf("scenario %s business_date %q does not match identity", scenario.ScenarioID, businessDate)
	}
	day, err := time.ParseInLocation("2006-01-02", businessDate, location)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("scenario %s has invalid business_date: %w", scenario.ScenarioID, err)
	}
	index, err := strconv.Atoi(parts[1])
	if err != nil || index <= 0 {
		return time.Time{}, 0, fmt.Errorf("scenario %s has invalid user index", scenario.ScenarioID)
	}
	return day, index, nil
}

// WriteHistoricalTesteeCreatedAtRepairSQL emits an idempotent, guarded MySQL
// script. It never connects to the database; operators retain the apply gate.
func WriteHistoricalTesteeCreatedAtRepairSQL(output io.Writer, manifest HistoricalManifest, expectedDatabase string) error {
	expectedDatabase = strings.TrimSpace(expectedDatabase)
	if !mysqlDatabaseNamePattern.MatchString(expectedDatabase) {
		return fmt.Errorf("expected database must contain only letters, digits, or underscore")
	}
	repairs, err := BuildHistoricalTesteeCreatedAtRepairs(manifest)
	if err != nil {
		return err
	}
	commentBatchID := strings.NewReplacer("\r", " ", "\n", " ").Replace(manifest.BatchID)

	_, err = fmt.Fprintf(output, `-- Generated from seeddata manifest for batch %s.
-- Apply only after setting:
--   SET @qs_testee_time_repair_confirm='%s';
DROP TEMPORARY TABLE IF EXISTS tmp_seeddata_testee_created_at_repair;
CREATE TEMPORARY TABLE tmp_seeddata_testee_created_at_repair (
  testee_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
  historical_created_at DATETIME(6) NOT NULL
);
INSERT INTO tmp_seeddata_testee_created_at_repair (testee_id, historical_created_at) VALUES
`, commentBatchID, historicalTesteeTimeRepairConfirmation)
	if err != nil {
		return err
	}
	for index, repair := range repairs {
		suffix := ",\n"
		if index == len(repairs)-1 {
			suffix = ";\n"
		}
		if _, err := fmt.Fprintf(output, "  (%d, '%s')%s", repair.TesteeID, repair.CreatedAt.Format("2006-01-02 15:04:05.000000"), suffix); err != nil {
			return err
		}
	}

	_, err = fmt.Fprintf(output, `DROP PROCEDURE IF EXISTS _oneoff_repair_historical_testee_created_at;
DELIMITER //
CREATE PROCEDURE _oneoff_repair_historical_testee_created_at()
BEGIN
  DECLARE scoped_count BIGINT DEFAULT 0;
  DECLARE mismatch_count BIGINT DEFAULT 0;

  IF COALESCE(@qs_testee_time_repair_confirm, '') <> '%s' THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'refusing Testee time repair: confirmation is missing';
  END IF;
  IF DATABASE() <> '%s' THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'refusing Testee time repair: unexpected database';
  END IF;

  SELECT COUNT(*) INTO scoped_count
  FROM testee t
  JOIN tmp_seeddata_testee_created_at_repair r ON r.testee_id = t.id
  WHERE t.org_id = %d AND t.deleted_at IS NULL;
  IF scoped_count <> %d THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'refusing Testee time repair: manifest IDs are missing or outside the expected organization';
  END IF;

  START TRANSACTION;
  UPDATE testee t
  JOIN tmp_seeddata_testee_created_at_repair r ON r.testee_id = t.id
  SET t.created_at = r.historical_created_at,
      t.updated_at = t.updated_at
  WHERE t.org_id = %d AND t.deleted_at IS NULL;

  SELECT COUNT(*) INTO mismatch_count
  FROM testee t
  JOIN tmp_seeddata_testee_created_at_repair r ON r.testee_id = t.id
  WHERE t.org_id = %d AND t.created_at <> r.historical_created_at;
  IF mismatch_count <> 0 THEN
    ROLLBACK;
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Testee time repair postcheck failed';
  END IF;
  COMMIT;
END//
DELIMITER ;
CALL _oneoff_repair_historical_testee_created_at();
DROP PROCEDURE _oneoff_repair_historical_testee_created_at;
SELECT COUNT(*) AS repaired_testee_count,
       MIN(t.created_at) AS earliest_created_at,
       MAX(t.created_at) AS latest_created_at
FROM testee t
JOIN tmp_seeddata_testee_created_at_repair r ON r.testee_id = t.id
WHERE t.org_id = %d;
DROP TEMPORARY TABLE tmp_seeddata_testee_created_at_repair;
`, historicalTesteeTimeRepairConfirmation, expectedDatabase, manifest.OrgID, len(repairs), manifest.OrgID, manifest.OrgID, manifest.OrgID)
	return err
}
