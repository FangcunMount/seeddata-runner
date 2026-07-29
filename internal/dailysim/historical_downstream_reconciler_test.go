package dailysim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	toolanswersheet "github.com/FangcunMount/seeddata-runner/internal/answersheet"
)

func TestHistoricalDownstreamReconcilerVerifiesPersistedSubmission(t *testing.T) {
	const scenarioID = "2025-01-01/1/submit_answer/M"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/internal/v1/historical-seed/batches/batch-v2/scenarios" || r.URL.Query().Get("scenario_id") != scenarioID {
			t.Fatalf("unexpected reconciliation request: %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"batch_id":"batch-v2","stages":[
			{"stage":"answersheet_submit","status":"completed","resource_id":"answer-1"},
			{"stage":"assessment_created","status":"completed","resource_id":"assessment-1"},
			{"stage":"assessment_submitted","status":"completed","resource_id":"assessment-1"},
			{"stage":"outcome_committed","status":"completed","resource_id":"outcome-1"},
			{"stage":"report_generated","status":"completed","resource_id":"report-1"}
		]}}`))
	}))
	defer server.Close()

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
	if _, err := store.MarkAcceptedPending(logicalID); err != nil {
		t.Fatal(err)
	}
	if err := store.putDownstreamSubmitted(historicalDownstreamRecord{
		LogicalID: logicalID, ScenarioID: scenarioID, BusinessDate: "2025-01-01",
		TargetKey: "scale/M", AnswerSheetID: "answer-1", TesteeID: 42, RequiresAssessment: true,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reconciler := newHistoricalDownstreamReconciler(
		ctx, log.New(log.NewOptions()), store, NewAPIClient(server.URL, "admin-token", log.New(log.NewOptions())),
		opts.BatchID, 1, 1, 10,
	)
	reconciler.Notify()
	deadline := time.Now().Add(3 * time.Second)
	for {
		record, ok, err := store.loadDownstream(logicalID)
		if err != nil {
			t.Fatal(err)
		}
		if ok && record.Status == historicalDownstreamVerified {
			if record.AssessmentID != "assessment-1" || record.OutcomeID != "outcome-1" || record.ReportID != "report-1" {
				t.Fatalf("verified downstream resources=%+v", record)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("downstream reconciliation did not finish: %+v", record)
		}
		time.Sleep(10 * time.Millisecond)
	}
	reconciler.Close()

	submission, ok, err := store.Get(logicalID)
	if err != nil || !ok || submission.Status != toolanswersheet.SubmissionStatusReady || submission.AssessmentID != "assessment-1" {
		t.Fatalf("reconciled submission=%+v ok=%v err=%v", submission, ok, err)
	}
}
