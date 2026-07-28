package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCLIOptionsDefaults(t *testing.T) {
	opts, err := parseCLIOptions(nil)
	if err != nil {
		t.Fatalf("parse default cli options: %v", err)
	}
	if opts.configPath != "./configs/seeddata.yaml" {
		t.Fatalf("unexpected default config path: %q", opts.configPath)
	}
	if opts.command != "daemon" {
		t.Fatalf("unexpected default command: %q", opts.command)
	}
	if opts.verbose {
		t.Fatalf("expected verbose=false by default")
	}
}

func TestParseHistoricalBackfillCLI(t *testing.T) {
	opts, err := parseCLIOptions([]string{
		"historical-backfill", "--config", "/tmp/seeddata.yaml", "--from", "2025-01-01", "--to", "2026-07-27",
		"--batch-id", "hist-20250101-20260727-v1", "--resume",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.command != "historical-backfill" || opts.from != "2025-01-01" || opts.to != "2026-07-27" || !opts.resume {
		t.Fatalf("unexpected historical options: %+v", opts)
	}
}

func TestParseHistoricalReadOnlyCommandsRequireBatch(t *testing.T) {
	for _, command := range []string{"historical-verify", "historical-manifest"} {
		if _, err := parseCLIOptions([]string{command}); err == nil {
			t.Fatalf("%s accepted without batch id", command)
		}
		if opts, err := parseCLIOptions([]string{command, "--batch-id", "batch"}); err != nil || opts.batchID != "batch" {
			t.Fatalf("%s parse failed: opts=%+v err=%v", command, opts, err)
		}
	}
}

func TestParseCLIOptionsOverrides(t *testing.T) {
	opts, err := parseCLIOptions([]string{"--config", "/tmp/seeddata.yaml", "--verbose"})
	if err != nil {
		t.Fatalf("parse cli options: %v", err)
	}
	if opts.configPath != "/tmp/seeddata.yaml" {
		t.Fatalf("unexpected config path: %q", opts.configPath)
	}
	if !opts.verbose {
		t.Fatalf("expected verbose=true")
	}
}

func TestRunSeeddataDaemonScriptUsesConfigOnly(t *testing.T) {
	scriptPath := filepath.Join("..", "..", "scripts", "run_seeddata_daemon.sh")
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read script %s: %v", scriptPath, err)
	}
	content := string(data)
	for _, expected := range []string{
		"LOG_FILE=\"${SEEDDATA_LOG_FILE:-$ROOT_DIR/logs/seeddata-daemon.log}\"",
		"exec \"$GO_BIN\" run ./cmd/seeddata --config \"$CONFIG_FILE\"",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("expected script to contain %q", expected)
		}
	}
	for _, unexpected := range []string{
		"SEEDDATA_PLAN_ID",
		"PLAN_ID=",
		"--plan-id",
	} {
		if strings.Contains(content, unexpected) {
			t.Fatalf("expected script not to contain %q", unexpected)
		}
	}
}
