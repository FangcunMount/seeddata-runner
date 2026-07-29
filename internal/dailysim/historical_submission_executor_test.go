package dailysim

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
)

func TestHistoricalSubmissionExecutorBoundsGlobalConcurrency(t *testing.T) {
	const workers = 4
	executor := newHistoricalSubmissionExecutor(context.Background(), log.New(log.NewOptions()), "2025-01-01", 20, workers, 2, 8, time.Hour)
	var inFlight atomic.Int64
	var maximum atomic.Int64
	futures := make([]historicalSubmissionFuture, 0, 20)
	for index := 0; index < 20; index++ {
		future, err := executor.Submit(HistoricalSubmissionJob{ScenarioID: "scenario", TargetKey: "scale/M"}, func(context.Context) (historicalSubmissionJobResult, error) {
			current := inFlight.Add(1)
			for {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			inFlight.Add(-1)
			return historicalSubmissionJobResult{ReportGenerated: true}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		futures = append(futures, future)
	}
	for _, future := range futures {
		if _, err := future.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	executor.Close()
	if got := maximum.Load(); got > workers || got < 2 {
		t.Fatalf("maximum concurrency=%d workers=%d", got, workers)
	}
	if executor.completed.Load() != 20 || executor.reports.Load() != 20 || executor.failed.Load() != 0 {
		t.Fatalf("unexpected executor counters: completed=%d reports=%d failed=%d", executor.completed.Load(), executor.reports.Load(), executor.failed.Load())
	}
}

func TestHistoricalSubmissionExecutorDoesNotCancelQueuedJobsAfterFailure(t *testing.T) {
	executor := newHistoricalSubmissionExecutor(context.Background(), log.New(log.NewOptions()), "2025-01-01", 2, 1, 1, 2, time.Hour)
	started := make(chan struct{})
	release := make(chan struct{})
	failure := errors.New("invalid submission")

	failedFuture, err := executor.Submit(HistoricalSubmissionJob{ScenarioID: "scenario-failed", TargetKey: "scale/A"}, func(context.Context) (historicalSubmissionJobResult, error) {
		close(started)
		<-release
		return historicalSubmissionJobResult{}, failure
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	succeededFuture, err := executor.Submit(HistoricalSubmissionJob{ScenarioID: "scenario-succeeded", TargetKey: "scale/B"}, func(ctx context.Context) (historicalSubmissionJobResult, error) {
		if err := ctx.Err(); err != nil {
			return historicalSubmissionJobResult{}, err
		}
		return historicalSubmissionJobResult{ReportGenerated: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	close(release)

	if _, err := failedFuture.Wait(); !errors.Is(err, failure) {
		t.Fatalf("failed job error=%v, want %v", err, failure)
	}
	if _, err := succeededFuture.Wait(); err != nil {
		t.Fatalf("queued job was canceled after unrelated failure: %v", err)
	}
	executor.Close()

	if executor.failed.Load() != 1 || executor.completed.Load() != 1 || executor.reports.Load() != 1 {
		t.Fatalf("unexpected executor counters: completed=%d reports=%d failed=%d", executor.completed.Load(), executor.reports.Load(), executor.failed.Load())
	}
}

func TestHistoricalSubmissionExecutorReleasesSubmissionWorkerWhileReportWaits(t *testing.T) {
	executor := newHistoricalSubmissionExecutor(context.Background(), log.New(log.NewOptions()), "2025-01-01", 2, 1, 1, 2, time.Hour)
	reportStarted := make(chan struct{})
	releaseReport := make(chan struct{})
	secondSubmissionStarted := make(chan struct{})

	first, err := executor.Submit(HistoricalSubmissionJob{ScenarioID: "scenario-1", TargetKey: "scale/A"}, func(context.Context) (historicalSubmissionJobResult, error) {
		return historicalSubmissionJobResult{
			awaitReport: func(context.Context) (historicalSubmissionJobResult, error) {
				close(reportStarted)
				<-releaseReport
				return historicalSubmissionJobResult{ReportGenerated: true}, nil
			},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-reportStarted

	second, err := executor.Submit(HistoricalSubmissionJob{ScenarioID: "scenario-2", TargetKey: "scale/B"}, func(context.Context) (historicalSubmissionJobResult, error) {
		close(secondSubmissionStarted)
		return historicalSubmissionJobResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-secondSubmissionStarted:
	case <-time.After(time.Second):
		t.Fatal("submission worker remained blocked by report polling")
	}
	if _, err := second.Wait(); err != nil {
		t.Fatal(err)
	}
	close(releaseReport)
	if _, err := first.Wait(); err != nil {
		t.Fatal(err)
	}
	executor.Close()
}

func TestHistoricalSubmissionExecutorBoundsPendingReportQueue(t *testing.T) {
	executor := newHistoricalSubmissionExecutor(context.Background(), log.New(log.NewOptions()), "2025-01-01", 4, 1, 1, 1, time.Hour)
	releaseReports := make(chan struct{})
	fourthSubmissionStarted := make(chan struct{})
	var prepared atomic.Int64
	futures := make([]historicalSubmissionFuture, 0, 4)
	for index := 0; index < 3; index++ {
		future, err := executor.Submit(HistoricalSubmissionJob{ScenarioID: "scenario", TargetKey: "scale/M"}, func(context.Context) (historicalSubmissionJobResult, error) {
			prepared.Add(1)
			return historicalSubmissionJobResult{
				awaitReport: func(context.Context) (historicalSubmissionJobResult, error) {
					<-releaseReports
					return historicalSubmissionJobResult{ReportGenerated: true}, nil
				},
			}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		futures = append(futures, future)
	}

	deadline := time.Now().Add(time.Second)
	for prepared.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if prepared.Load() != 3 {
		t.Fatalf("prepared=%d, want 3", prepared.Load())
	}
	if got := len(executor.reportJobs); got != 1 {
		t.Fatalf("pending report queue length=%d, want capacity 1", got)
	}
	if got := executor.reportInFlight.Load(); got != 1 {
		t.Fatalf("report in flight=%d, want 1", got)
	}

	fourth, err := executor.Submit(HistoricalSubmissionJob{ScenarioID: "scenario-4", TargetKey: "scale/N"}, func(context.Context) (historicalSubmissionJobResult, error) {
		close(fourthSubmissionStarted)
		return historicalSubmissionJobResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	futures = append(futures, fourth)

	select {
	case <-fourthSubmissionStarted:
		t.Fatal("submission worker was not backpressured by the full report queue")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseReports)
	for _, future := range futures {
		if _, err := future.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	executor.Close()
}
