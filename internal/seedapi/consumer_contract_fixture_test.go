package seedapi

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestQSConsumerContractFixtureDoesNotRestoreSubmitStatus(t *testing.T) {
	data, err := os.ReadFile("testdata/qs-server-consumer-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		OpenAPIBaseCommit string              `json:"openapi_base_commit"`
		ActivePaths       []string            `json:"active_paths"`
		RemovedPaths      []string            `json:"removed_paths"`
		RequiredFields    map[string][]string `json:"required_fields"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.OpenAPIBaseCommit) != 40 {
		t.Fatalf("fixture must identify the qs-server OpenAPI base commit: %q", fixture.OpenAPIBaseCommit)
	}
	if slices.Contains(fixture.ActivePaths, "GET /api/v1/answersheets/{id}/submit-status") {
		t.Fatal("removed submit-status path must not be active")
	}
	if !slices.Contains(fixture.RemovedPaths, "GET /api/v1/answersheets/{id}/submit-status") {
		t.Fatal("fixture must explicitly guard the removed submit-status path")
	}
	for name, field := range map[string]string{
		"admin_submit_request":       "idempotency_key",
		"collection_submit_request":  "idempotency_key",
		"collection_submit_accepted": "answersheet_id",
		"assessment_readiness":       "next_poll_after_ms",
	} {
		if !slices.Contains(fixture.RequiredFields[name], field) {
			t.Fatalf("fixture section %s missing %s", name, field)
		}
	}
}
