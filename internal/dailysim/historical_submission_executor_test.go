package dailysim

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
)

func TestHistoricalSubmissionExecutorBoundsGlobalConcurrency(t *testing.T) {
	const workers = 4
	executor := newHistoricalSubmissionExecutor(context.Background(), log.New(log.NewOptions()), "2025-01-01", 20, workers, time.Hour)
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
