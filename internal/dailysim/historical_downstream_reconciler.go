package dailysim

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	"github.com/FangcunMount/seeddata-runner/internal/historicalseed"
)

const (
	historicalDownstreamPollInterval      = 3 * time.Second
	historicalDownstreamDispatchInterval  = 250 * time.Millisecond
	historicalDefaultPendingHighWatermark = 4096
)

type historicalDownstreamReconcilerKey struct{}

type historicalDownstreamReconciler struct {
	ctx           context.Context
	cancel        context.CancelFunc
	logger        log.Logger
	store         *historicalStateStore
	client        *APIClient
	batchID       string
	highWatermark int
	jobs          chan historicalDownstreamRecord
	wake          chan struct{}
	wg            sync.WaitGroup
	inFlightMu    sync.Mutex
	inFlight      map[string]struct{}
	verified      atomic.Int64
	failedPolls   atomic.Int64
}

func newHistoricalDownstreamReconciler(
	ctx context.Context,
	logger log.Logger,
	store *historicalStateStore,
	client *APIClient,
	batchID string,
	workers, queueCapacity, highWatermark int,
) *historicalDownstreamReconciler {
	if workers <= 0 {
		workers = 1
	}
	if queueCapacity <= 0 {
		queueCapacity = historicalSubmissionQueueCapacity
	}
	if highWatermark <= 0 {
		highWatermark = historicalDefaultPendingHighWatermark
	}
	reconcileCtx, cancel := context.WithCancel(ctx)
	r := &historicalDownstreamReconciler{
		ctx: reconcileCtx, cancel: cancel, logger: logger, store: store, client: client,
		batchID: strings.TrimSpace(batchID), highWatermark: highWatermark,
		jobs: make(chan historicalDownstreamRecord, queueCapacity), wake: make(chan struct{}, 1),
		inFlight: make(map[string]struct{}),
	}
	r.wg.Add(1)
	go r.runDispatcher()
	for worker := 0; worker < workers; worker++ {
		r.wg.Add(1)
		go r.runWorker()
	}
	return r
}

func withHistoricalDownstreamReconciler(ctx context.Context, reconciler *historicalDownstreamReconciler) context.Context {
	return context.WithValue(ctx, historicalDownstreamReconcilerKey{}, reconciler)
}

func historicalDownstreamReconcilerFromContext(ctx context.Context) *historicalDownstreamReconciler {
	reconciler, _ := ctx.Value(historicalDownstreamReconcilerKey{}).(*historicalDownstreamReconciler)
	return reconciler
}

func (r *historicalDownstreamReconciler) AllowSubmission() error {
	if r == nil || r.store == nil {
		return nil
	}
	pending, err := r.store.countOutstandingDownstream("")
	if err != nil {
		return err
	}
	if pending >= r.highWatermark {
		return fmt.Errorf("historical downstream pending high watermark reached: pending=%d limit=%d", pending, r.highWatermark)
	}
	return nil
}

func (r *historicalDownstreamReconciler) Notify() {
	if r == nil {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *historicalDownstreamReconciler) runDispatcher() {
	defer r.wg.Done()
	ticker := time.NewTicker(historicalDownstreamDispatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.wake:
			r.dispatchDue()
		case <-ticker.C:
			r.dispatchDue()
		}
	}
}

func (r *historicalDownstreamReconciler) dispatchDue() {
	available := cap(r.jobs) - len(r.jobs)
	if available <= 0 || r.store == nil {
		return
	}
	records, err := r.store.listDueDownstream(time.Now().UTC(), available)
	if err != nil {
		r.logger.Warnw("Historical downstream scan failed", "error", err.Error())
		return
	}
	for _, record := range records {
		if !r.claimInFlight(record.LogicalID) {
			continue
		}
		select {
		case <-r.ctx.Done():
			r.releaseInFlight(record.LogicalID)
			return
		case r.jobs <- record:
		default:
			r.releaseInFlight(record.LogicalID)
			return
		}
	}
}

func (r *historicalDownstreamReconciler) claimInFlight(logicalID string) bool {
	r.inFlightMu.Lock()
	defer r.inFlightMu.Unlock()
	if _, exists := r.inFlight[logicalID]; exists {
		return false
	}
	r.inFlight[logicalID] = struct{}{}
	return true
}

func (r *historicalDownstreamReconciler) releaseInFlight(logicalID string) {
	r.inFlightMu.Lock()
	delete(r.inFlight, logicalID)
	r.inFlightMu.Unlock()
}

func (r *historicalDownstreamReconciler) runWorker() {
	defer r.wg.Done()
	for {
		select {
		case <-r.ctx.Done():
			return
		case record := <-r.jobs:
			r.reconcile(record)
			r.releaseInFlight(record.LogicalID)
		}
	}
}

func (r *historicalDownstreamReconciler) reconcile(record historicalDownstreamRecord) {
	result := dailySimulationSubmissionResult{AnswerSheetID: record.AnswerSheetID, AssessmentID: record.AssessmentID}
	historical := historicalseed.Context{BatchID: r.batchID, ScenarioID: record.ScenarioID}
	completed, err := verifyHistoricalSubmissionStages(
		r.ctx, r.client, historical, record.TaskID, record.RequiresAssessment, result,
	)
	if err != nil {
		nextAttempt := time.Now().UTC().Add(historicalDownstreamPollInterval)
		lastError := ""
		if !errors.Is(err, errHistoricalSubmissionPending) {
			lastError = err.Error()
			r.failedPolls.Add(1)
			r.logger.Warnw("Historical downstream reconciliation failed",
				"scenario_id", record.ScenarioID, "answersheet_id", record.AnswerSheetID, "error", err.Error())
		}
		if markErr := r.store.markDownstreamPending(record.LogicalID, lastError, nextAttempt); markErr != nil {
			r.logger.Warnw("Historical downstream pending state update failed", "logical_id", record.LogicalID, "error", markErr.Error())
		}
		return
	}
	if record.RequiresAssessment {
		if _, err := r.store.MarkReady(record.LogicalID, completed.AssessmentID); err != nil {
			r.logger.Warnw("Historical downstream submission ready update failed", "logical_id", record.LogicalID, "error", err.Error())
			_ = r.store.markDownstreamPending(record.LogicalID, err.Error(), time.Now().UTC().Add(historicalDownstreamPollInterval))
			return
		}
	} else if _, err := r.store.MarkCompleted(record.LogicalID, completed.AnswerSheetID); err != nil {
		r.logger.Warnw("Historical downstream submission completion update failed", "logical_id", record.LogicalID, "error", err.Error())
		_ = r.store.markDownstreamPending(record.LogicalID, err.Error(), time.Now().UTC().Add(historicalDownstreamPollInterval))
		return
	}
	if err := r.store.markDownstreamVerified(
		record.LogicalID, completed.AssessmentID, completed.OutcomeID, completed.ReportID,
	); err != nil {
		r.logger.Warnw("Historical downstream verified state update failed", "logical_id", record.LogicalID, "error", err.Error())
		return
	}
	r.verified.Add(1)
	r.logger.Infow("Historical downstream verified",
		"scenario_id", record.ScenarioID, "answersheet_id", completed.AnswerSheetID,
		"assessment_id", completed.AssessmentID, "outcome_id", completed.OutcomeID, "report_id", completed.ReportID)
}

func (r *historicalDownstreamReconciler) Drain(ctx context.Context) error {
	if r == nil || r.store == nil {
		return nil
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		pending, err := r.store.countOutstandingDownstream("")
		if err != nil {
			return err
		}
		if pending == 0 {
			return nil
		}
		r.Notify()
		select {
		case <-ctx.Done():
			return fmt.Errorf("historical downstream reconciliation timed out with %d pending: %w", pending, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (r *historicalDownstreamReconciler) Close() {
	if r == nil {
		return
	}
	r.cancel()
	r.wg.Wait()
}
