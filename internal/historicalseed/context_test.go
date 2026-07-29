package historicalseed

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestHeadersForMatchesHistoricalSeedV1Wire(t *testing.T) {
	at := time.Date(2025, 1, 1, 8, 5, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	historical := Context{
		BatchID: "hist-20250101-20260727-v1", ScenarioID: "2025-01-01/7/resolve_entry/model-a",
		OrgID: 1, Version: Version1, Timeline: Timeline{TesteeCreatedAt: &at, EntryResolvedAt: &at},
	}
	requestedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	headers, err := HeadersFor("POST", "/api/v1/assessment-entries/token/resolve?x=1", []byte(`{"a":1}`), historical, requestedAt, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(headers[HeaderContext])
	if err != nil {
		t.Fatal(err)
	}
	var got Context
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.BatchID != historical.BatchID || got.Timeline.TesteeCreatedAt == nil || !got.Timeline.TesteeCreatedAt.Equal(at) || got.Timeline.EntryResolvedAt == nil || !got.Timeline.EntryResolvedAt.Equal(at) {
		t.Fatalf("wire context mismatch: %+v", got)
	}
	want := Sign("POST", "/api/v1/assessment-entries/token/resolve?x=1", []byte(`{"a":1}`), headers[HeaderRequestedAt], headers[HeaderContext], []byte("secret"))
	if headers[HeaderSignature] != want {
		t.Fatalf("signature mismatch: got=%s want=%s", headers[HeaderSignature], want)
	}
}
