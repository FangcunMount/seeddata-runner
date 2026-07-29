package dailysim

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestBuildHistoricalTesteeCreatedAtRepairsUsesManifestIdentity(t *testing.T) {
	manifest := HistoricalManifest{
		BatchID: "batch", OrgID: 1, Timezone: historicalTimezone,
		Scenarios: map[string]HistoricalScenarioManifest{
			"scenario": {
				ScenarioID: "2025-01-01/7/create_testee/scale", BusinessDate: "2025-01-01",
				TesteeID: "42", TesteeCreated: true,
			},
		},
	}
	repairs, err := BuildHistoricalTesteeCreatedAtRepairs(manifest)
	if err != nil {
		t.Fatal(err)
	}
	location, _ := time.LoadLocation(historicalTimezone)
	day := time.Date(2025, 1, 1, 0, 0, 0, 0, location)
	want := BuildHistoricalTimeline("batch", day, 7).TesteeCreatedAt
	if len(repairs) != 1 || repairs[0].TesteeID != 42 || want == nil || !repairs[0].CreatedAt.Equal(*want) {
		t.Fatalf("repairs=%+v want testee=42 created_at=%v", repairs, want)
	}
}

func TestBuildHistoricalTesteeCreatedAtRepairsRejectsIdentityConflict(t *testing.T) {
	manifest := HistoricalManifest{
		BatchID: "batch", OrgID: 1, Timezone: historicalTimezone,
		Scenarios: map[string]HistoricalScenarioManifest{
			"scenario": {ScenarioID: "2025-01-01/7/create_testee/scale", BusinessDate: "2025-01-02", TesteeID: "42", TesteeCreated: true},
		},
	}
	if _, err := BuildHistoricalTesteeCreatedAtRepairs(manifest); err == nil {
		t.Fatal("expected scenario identity conflict")
	}
}

func TestWriteHistoricalTesteeCreatedAtRepairSQLIsExactAndGuarded(t *testing.T) {
	manifest := HistoricalManifest{
		BatchID: "batch", OrgID: 9, Timezone: historicalTimezone,
		Scenarios: map[string]HistoricalScenarioManifest{
			"scenario": {ScenarioID: "2025-01-01/7/create_testee/scale", BusinessDate: "2025-01-01", TesteeID: "42", TesteeCreated: true},
		},
	}
	var output bytes.Buffer
	if err := WriteHistoricalTesteeCreatedAtRepairSQL(&output, manifest, "qs"); err != nil {
		t.Fatal(err)
	}
	sql := output.String()
	for _, required := range []string{
		"REPAIR_HISTORICAL_TESTEE_CREATED_AT", "IF DATABASE() <> 'qs'", "(42, '2025-01-01 ",
		"WHERE t.org_id = 9 AND t.deleted_at IS NULL", "t.updated_at = t.updated_at", "ROLLBACK;",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("generated SQL missing %q:\n%s", required, sql)
		}
	}
}

func TestWriteHistoricalTesteeCreatedAtRepairSQLRejectsUnsafeDatabaseName(t *testing.T) {
	manifest := HistoricalManifest{BatchID: "batch", OrgID: 1, Timezone: historicalTimezone, Scenarios: map[string]HistoricalScenarioManifest{
		"scenario": {ScenarioID: "2025-01-01/1/create_testee/scale", BusinessDate: "2025-01-01", TesteeID: "42", TesteeCreated: true},
	}}
	if err := WriteHistoricalTesteeCreatedAtRepairSQL(&bytes.Buffer{}, manifest, "qs; DROP DATABASE qs"); err == nil {
		t.Fatal("expected unsafe database name rejection")
	}
}
