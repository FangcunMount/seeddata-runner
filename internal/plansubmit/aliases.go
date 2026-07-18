package plansubmit

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	toolanswersheet "github.com/FangcunMount/seeddata-runner/internal/answersheet"
	"github.com/FangcunMount/seeddata-runner/internal/seedapi"
	"github.com/FangcunMount/seeddata-runner/internal/seedconfig"
	"github.com/FangcunMount/seeddata-runner/internal/seedruntime"
)

type Dependencies = seedruntime.Dependencies
type dependencies = Dependencies
type SeedConfig = seedconfig.Config
type GlobalConfig = seedconfig.GlobalConfig
type APIClient = seedapi.APIClient
type PublishedAssessmentModelResponse = seedapi.PublishedAssessmentModelResponse
type PlanResponse = seedapi.PlanResponse
type TaskResponse = seedapi.TaskResponse
type ApiserverTesteeResponse = seedapi.ApiserverTesteeResponse
type PlanTaskWindowResponse = seedapi.PlanTaskWindowResponse
type ListPlanTaskWindowRequest = seedapi.ListPlanTaskWindowRequest
type QuestionnaireDetailResponse = seedapi.QuestionnaireDetailResponse
type SubmitAnswerSheetRequest = seedapi.SubmitAnswerSheetRequest
type AdminSubmitAnswerSheetRequest = seedapi.AdminSubmitAnswerSheetRequest
type Answer = seedapi.Answer
type SubmitAnswerSheetResponse = seedapi.SubmitAnswerSheetResponse
type SubmissionLedger = toolanswersheet.SubmissionLedger
type QuestionResponse = seedapi.QuestionResponse
type OptionResponse = seedapi.OptionResponse
type Response = seedapi.Response

var NewAPIClient = seedapi.NewAPIClient
var NewSubmissionLedger = toolanswersheet.NewSubmissionLedger

func newSeeddataLogger(verbose bool) log.Logger {
	return seedruntime.NewLogger(verbose)
}

type Options struct {
	PlanIDs           []string
	Workers           int
	CompletionPercent int
	TesteeSource      string
	Verbose           bool
	Continuous        bool
	IdleInterval      time.Duration
	ActiveInterval    time.Duration
}

type planOpenTaskSubmitOptions = Options

func optionsFromConfig(cfg *seedconfig.Config) (Options, error) {
	idleInterval, err := seedruntime.ParseRelativeDuration(cfg.PlanSubmit.IdleInterval)
	if err != nil {
		return Options{}, err
	}
	activeInterval, err := seedruntime.ParseRelativeDuration(cfg.PlanSubmit.ActiveInterval)
	if err != nil {
		return Options{}, err
	}
	completionPercent := seedconfig.DefaultPlanSubmitCompletionPercent
	if cfg.PlanSubmit.CompletionPercent != nil {
		completionPercent = *cfg.PlanSubmit.CompletionPercent
	}
	return Options{
		PlanIDs:           normalizePlanIDs(cfg.PlanSubmit.PlanIDStrings()),
		Workers:           cfg.PlanSubmit.Workers,
		CompletionPercent: completionPercent,
		TesteeSource:      strings.TrimSpace(cfg.DailySimulation.TesteeSource),
		Continuous:        true,
		IdleInterval:      idleInterval,
		ActiveInterval:    activeInterval,
	}, nil
}

func RunDaemon(ctx context.Context, deps *Dependencies, verbose bool) (*planOpenTaskSubmitStats, error) {
	opts, err := optionsFromConfig(deps.Config)
	if err != nil {
		return nil, err
	}
	opts.Verbose = verbose
	return seedPlanSubmitOpenTasksDaemon(ctx, deps, opts)
}

func normalizePlanID(planID string) string {
	return strings.TrimSpace(planID)
}

func normalizePlanIDs(planIDs []string) []string {
	if len(planIDs) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(planIDs))
	seen := make(map[string]struct{}, len(planIDs))
	for _, planID := range planIDs {
		value := normalizePlanID(planID)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func parseID(raw string) uint64 {
	return seedruntime.ParseID(raw)
}

func normalizePlanWorkers(workers, taskCount int) int {
	return seedruntime.NormalizePlanWorkers(workers, taskCount)
}

func prewarmAPIToken(ctx context.Context, client *APIClient, orgID int64, logger interface{ Warnw(string, ...interface{}) }) {
	if client == nil || orgID <= 0 {
		return
	}
	_, err := client.ListTesteesByOrg(ctx, orgID, 1, 1)
	if err != nil {
		logger.Warnw("Prewarm API token failed", "error", err)
	}
}

func sortTasksBySeq(tasks []TaskResponse) {
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].Seq == tasks[j].Seq {
			return parseID(tasks[i].ID) < parseID(tasks[j].ID)
		}
		return tasks[i].Seq < tasks[j].Seq
	})
}
