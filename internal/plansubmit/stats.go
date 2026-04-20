package plansubmit

type planOpenTaskSubmitStats struct {
	OpenedCount          int
	SubmittedCount       int
	SkippedCount         int
	FailedTaskListLoads  int
	FailedTaskExecutions int
}

func newPlanOpenTaskSubmitStats() *planOpenTaskSubmitStats {
	return &planOpenTaskSubmitStats{}
}

func normalizePlanOpenTaskSubmitStats(stats *planOpenTaskSubmitStats) *planOpenTaskSubmitStats {
	if stats == nil {
		return newPlanOpenTaskSubmitStats()
	}
	return stats
}

func (s *planOpenTaskSubmitStats) HasActivity() bool {
	return s != nil && (s.OpenedCount > 0 || s.SubmittedCount > 0)
}

func (s *planOpenTaskSubmitStats) RecordOpenedCount(count int) {
	if s == nil {
		return
	}
	s.OpenedCount = count
}

func (s *planOpenTaskSubmitStats) RecordListLoadFailure() {
	if s == nil {
		return
	}
	s.FailedTaskListLoads++
}

func (s *planOpenTaskSubmitStats) RecordExecutionCounts(submitted, skipped, failedExecutions int) {
	if s == nil {
		return
	}
	s.SubmittedCount = submitted
	s.SkippedCount = skipped
	s.FailedTaskExecutions = failedExecutions
}

func (s *planOpenTaskSubmitStats) Merge(other *planOpenTaskSubmitStats) {
	if s == nil || other == nil {
		return
	}
	s.OpenedCount += other.OpenedCount
	s.SubmittedCount += other.SubmittedCount
	s.SkippedCount += other.SkippedCount
	s.FailedTaskListLoads += other.FailedTaskListLoads
	s.FailedTaskExecutions += other.FailedTaskExecutions
}
