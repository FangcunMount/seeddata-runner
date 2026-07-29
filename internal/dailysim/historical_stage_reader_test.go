package dailysim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
)

func TestHistoricalStageReadersBoundConcurrencyAndPreserveIdentity(t *testing.T) {
	var inFlight atomic.Int64
	var maximum atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": HistoricalStageBatchResponse{BatchID: r.URL.Query().Get("scenario_id")},
		})
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "token", log.New(log.NewOptions()))
	ids := make([]string, 20)
	for index := range ids {
		ids[index] = "scenario-" + strconv.Itoa(index)
	}
	responses, err := loadHistoricalScenarioStageResponses(context.Background(), client, "batch", ids, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != len(ids) {
		t.Fatalf("responses=%d want=%d", len(responses), len(ids))
	}
	for _, id := range ids {
		if responses[id].BatchID != id {
			t.Fatalf("scenario response mismatch for %s: %+v", id, responses[id])
		}
	}
	if got := maximum.Load(); got > 4 || got < 2 {
		t.Fatalf("maximum stage read concurrency=%d", got)
	}
}
