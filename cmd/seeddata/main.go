package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/FangcunMount/seeddata-runner/internal/dailysim"
	"github.com/FangcunMount/seeddata-runner/internal/seedconfig"
	"github.com/FangcunMount/seeddata-runner/internal/seedruntime"
)

type cliOptions struct {
	command           string
	configPath        string
	verbose           bool
	from              string
	to                string
	batchID           string
	resume            bool
	stateDir          string
	expectedDB        string
	parentWorkers     int
	submissionWorkers int
	stageReadWorkers  int
	iamWorkers        int
}

func main() {
	opts, err := parseCLIOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(2)
	}
	logger := seedruntime.NewLogger(opts.verbose)
	if opts.command == "historical-manifest" {
		manifest, err := dailysim.LoadHistoricalManifest(opts.stateDir, opts.batchID)
		if err != nil {
			logger.Errorw("Load historical manifest failed", "error", err.Error())
			os.Exit(1)
		}
		writeCLIJSON(manifest)
		return
	}
	if opts.command == "historical-testee-time-repair-sql" {
		manifest, err := dailysim.LoadHistoricalManifest(opts.stateDir, opts.batchID)
		if err != nil {
			logger.Errorw("Load historical manifest failed", "error", err.Error())
			os.Exit(1)
		}
		if manifest.BatchID != opts.batchID {
			logger.Errorw("Historical manifest batch identity mismatch", "expected", opts.batchID, "actual", manifest.BatchID)
			os.Exit(1)
		}
		if err := dailysim.WriteHistoricalTesteeCreatedAtRepairSQL(os.Stdout, manifest, opts.expectedDB); err != nil {
			logger.Errorw("Generate historical Testee time repair SQL failed", "error", err.Error())
			os.Exit(1)
		}
		return
	}

	cfg, err := seedconfig.Load(opts.configPath)
	if err != nil {
		logger.Errorw("Load seeddata config failed", "config", opts.configPath, "error", err.Error())
		os.Exit(1)
	}

	ctx, cancel := seedruntime.NewSignalContext()
	defer cancel()
	var historicalOpts dailysim.HistoricalBackfillOptions
	switch opts.command {
	case "historical-backfill":
		historicalConfig := cfg.ResolveHistoricalBackfill()
		if opts.parentWorkers > 0 {
			historicalConfig.ParentWorkers = opts.parentWorkers
		}
		if opts.submissionWorkers > 0 {
			historicalConfig.SubmissionWorkers = opts.submissionWorkers
		}
		if opts.stageReadWorkers > 0 {
			historicalConfig.StageReadWorkers = opts.stageReadWorkers
		}
		if opts.iamWorkers > 0 {
			historicalConfig.IAMWorkers = opts.iamWorkers
		}
		historicalOpts = dailysim.HistoricalBackfillOptions{
			From: opts.from, To: opts.to, BatchID: opts.batchID, Resume: opts.resume, StateDir: opts.stateDir,
			CountMin: cfg.DailySimulation.CountMin, CountMax: cfg.DailySimulation.CountMax,
			Workers: historicalConfig.ParentWorkers, SubmissionWorkers: historicalConfig.SubmissionWorkers,
			StageReadWorkers: historicalConfig.StageReadWorkers, IAMWorkers: historicalConfig.IAMWorkers,
			ProgressInterval: historicalConfig.ProgressInterval,
		}
		legacySubmissionPath := resolveHistoricalLegacySubmissionPath(cfg)
		if err := dailysim.PrepareHistoricalBackfillState(historicalOpts, cfg.Global.OrgID, legacySubmissionPath); err != nil {
			logger.Errorw("Prepare historical state failed", "error", err.Error())
			os.Exit(1)
		}
	}

	deps, err := seedruntime.LoadDependencies(ctx, cfg, logger)
	if err != nil {
		logger.Errorw("Initialize seeddata dependencies failed", "error", err.Error())
		os.Exit(1)
	}
	if opts.command == "historical-verify" {
		result, err := dailysim.VerifyHistoricalBackfillWithServer(ctx, deps, opts.stateDir, opts.batchID)
		if err != nil {
			logger.Errorw("Verify historical backfill failed", "error", err.Error())
			os.Exit(1)
		}
		writeCLIJSON(result)
		if !result.Complete {
			os.Exit(1)
		}
		return
	}

	switch opts.command {
	case "historical-backfill":
		err = dailysim.RunHistoricalBackfill(ctx, deps, historicalOpts)
	default:
		err = runSeedSupervisor(ctx, deps, opts.verbose)
	}
	if err != nil {
		logger.Errorw("Seeddata supervisor exited with error", "error", err.Error())
		os.Exit(1)
	}
}

func resolveHistoricalLegacySubmissionPath(cfg *seedconfig.Config) string {
	if strings.TrimSpace(os.Getenv("SEEDDATA_DAILY_SUBMISSION_STATE_FILE")) == "" {
		return ""
	}
	return strings.TrimSpace(cfg.DailySimulation.SubmissionStateFile)
}

func parseCLIOptions(args []string) (cliOptions, error) {
	opts := cliOptions{command: "daemon", stateDir: ".seeddata-cache/historical"}
	if len(args) > 0 {
		switch args[0] {
		case "historical-backfill", "historical-verify", "historical-manifest", "historical-testee-time-repair-sql":
			opts.command = args[0]
			args = args[1:]
		}
	}
	fs := flag.NewFlagSet("seeddata "+opts.command, flag.ContinueOnError)
	fs.StringVar(&opts.configPath, "config", "./configs/seeddata.yaml", "path to seeddata config yaml")
	fs.BoolVar(&opts.verbose, "verbose", false, "enable verbose logging")
	fs.StringVar(&opts.stateDir, "state-dir", opts.stateDir, "historical checkpoint and manifest directory")
	switch opts.command {
	case "historical-backfill":
		fs.StringVar(&opts.from, "from", "", "first historical business date (YYYY-MM-DD)")
		fs.StringVar(&opts.to, "to", "", "last historical business date (YYYY-MM-DD)")
		fs.StringVar(&opts.batchID, "batch-id", "", "stable historical batch identity")
		fs.BoolVar(&opts.resume, "resume", false, "resume from the first incomplete day")
		fs.IntVar(&opts.parentWorkers, "parent-workers", 0, "override historical parent worker count")
		fs.IntVar(&opts.submissionWorkers, "submission-workers", 0, "override historical submission worker count")
		fs.IntVar(&opts.stageReadWorkers, "stage-read-workers", 0, "override historical stage reader count")
		fs.IntVar(&opts.iamWorkers, "iam-workers", 0, "override historical IAM worker count")
	case "historical-verify", "historical-manifest", "historical-testee-time-repair-sql":
		fs.StringVar(&opts.batchID, "batch-id", "", "historical batch identity")
		if opts.command == "historical-testee-time-repair-sql" {
			fs.StringVar(&opts.expectedDB, "expected-database", "", "exact QS MySQL database name")
		}
	}
	if err := fs.Parse(args); err != nil {
		return cliOptions{}, err
	}
	if opts.command == "historical-backfill" && (opts.from == "" || opts.to == "" || opts.batchID == "") {
		return cliOptions{}, fmt.Errorf("historical-backfill requires --from, --to and --batch-id")
	}
	for name, value := range map[string]int{
		"parent-workers": opts.parentWorkers, "submission-workers": opts.submissionWorkers,
		"stage-read-workers": opts.stageReadWorkers, "iam-workers": opts.iamWorkers,
	} {
		if value < 0 {
			return cliOptions{}, fmt.Errorf("--%s must be positive when set", name)
		}
	}
	if (opts.command == "historical-verify" || opts.command == "historical-manifest" || opts.command == "historical-testee-time-repair-sql") && opts.batchID == "" {
		return cliOptions{}, fmt.Errorf("%s requires --batch-id", opts.command)
	}
	if opts.command == "historical-testee-time-repair-sql" && opts.expectedDB == "" {
		return cliOptions{}, fmt.Errorf("historical-testee-time-repair-sql requires --expected-database")
	}
	return opts, nil
}

func writeCLIJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
	}
}

func runSeedSupervisor(ctx context.Context, deps *seedruntime.Dependencies, verbose bool) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		return seedDailySimulationDaemon(groupCtx, deps)
	})
	group.Go(func() error {
		return seedPlanSubmitOpenTasksDaemon(groupCtx, deps, verbose)
	})
	return group.Wait()
}
