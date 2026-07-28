package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"golang.org/x/sync/errgroup"

	"github.com/FangcunMount/seeddata-runner/internal/dailysim"
	"github.com/FangcunMount/seeddata-runner/internal/seedconfig"
	"github.com/FangcunMount/seeddata-runner/internal/seedruntime"
)

type cliOptions struct {
	command    string
	configPath string
	verbose    bool
	from       string
	to         string
	batchID    string
	resume     bool
	stateDir   string
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

	cfg, err := seedconfig.Load(opts.configPath)
	if err != nil {
		logger.Errorw("Load seeddata config failed", "config", opts.configPath, "error", err.Error())
		os.Exit(1)
	}

	ctx, cancel := seedruntime.NewSignalContext()
	defer cancel()

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

	if opts.command == "historical-backfill" {
		err = dailysim.RunHistoricalBackfill(ctx, deps, dailysim.HistoricalBackfillOptions{
			From: opts.from, To: opts.to, BatchID: opts.batchID, Resume: opts.resume, StateDir: opts.stateDir,
			CountMin: cfg.DailySimulation.CountMin, CountMax: cfg.DailySimulation.CountMax, Workers: cfg.DailySimulation.Workers,
		})
	} else {
		err = runSeedSupervisor(ctx, deps, opts.verbose)
	}
	if err != nil {
		logger.Errorw("Seeddata supervisor exited with error", "error", err.Error())
		os.Exit(1)
	}
}

func parseCLIOptions(args []string) (cliOptions, error) {
	opts := cliOptions{command: "daemon", stateDir: ".seeddata-cache/historical"}
	if len(args) > 0 {
		switch args[0] {
		case "historical-backfill", "historical-verify", "historical-manifest":
			opts.command = args[0]
			args = args[1:]
		}
	}
	fs := flag.NewFlagSet("seeddata "+opts.command, flag.ContinueOnError)
	fs.StringVar(&opts.configPath, "config", "./configs/seeddata.yaml", "path to seeddata config yaml")
	fs.BoolVar(&opts.verbose, "verbose", false, "enable verbose logging")
	fs.StringVar(&opts.stateDir, "state-dir", opts.stateDir, "historical checkpoint and manifest directory")
	if opts.command == "historical-backfill" {
		fs.StringVar(&opts.from, "from", "", "first historical business date (YYYY-MM-DD)")
		fs.StringVar(&opts.to, "to", "", "last historical business date (YYYY-MM-DD)")
		fs.StringVar(&opts.batchID, "batch-id", "", "stable historical batch identity")
		fs.BoolVar(&opts.resume, "resume", false, "resume from the first incomplete day")
	} else if opts.command == "historical-verify" || opts.command == "historical-manifest" {
		fs.StringVar(&opts.batchID, "batch-id", "", "historical batch identity")
	}
	if err := fs.Parse(args); err != nil {
		return cliOptions{}, err
	}
	if opts.command == "historical-backfill" && (opts.from == "" || opts.to == "" || opts.batchID == "") {
		return cliOptions{}, fmt.Errorf("historical-backfill requires --from, --to and --batch-id")
	}
	if (opts.command == "historical-verify" || opts.command == "historical-manifest") && opts.batchID == "" {
		return cliOptions{}, fmt.Errorf("%s requires --batch-id", opts.command)
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
