package main

import (
	"encoding/hex"
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
	if opts.verbose {
		t.Fatalf("expected verbose=false by default")
	}
	if opts.checkConfig {
		t.Fatalf("expected check-config=false by default")
	}
}

func TestParseCLIOptionsRejectsRetiredCommandsFlagsAndPositionals(t *testing.T) {
	tests := [][]string{
		{decodeRetiredCLIInput(t, "686973746f726963616c2d6261636b66696c6c")},
		{decodeRetiredCLIInput(t, "686973746f726963616c2d766572696679")},
		{decodeRetiredCLIInput(t, "686973746f726963616c2d6d616e6966657374")},
		{decodeRetiredCLIInput(t, "686973746f726963616c2d7465737465652d74696d652d7265706169722d73716c")},
		{decodeRetiredCLIInput(t, "2d2d62617463682d6964")},
		{decodeRetiredCLIInput(t, "2d2d72756e2d64617465")},
		{decodeRetiredCLIInput(t, "2d2d73746174652d646972")},
		{decodeRetiredCLIInput(t, "2d2d66726f6d")},
		{decodeRetiredCLIInput(t, "2d2d746f")},
		{decodeRetiredCLIInput(t, "2d2d726573756d65")},
		{decodeRetiredCLIInput(t, "2d2d706172656e742d776f726b657273")},
		{decodeRetiredCLIInput(t, "2d2d7375626d697373696f6e2d776f726b657273")},
		{decodeRetiredCLIInput(t, "2d2d7265706f72742d776f726b657273")},
		{decodeRetiredCLIInput(t, "2d2d7265706f72742d71756575652d6361706163697479")},
		{decodeRetiredCLIInput(t, "2d2d70656e64696e672d686967682d77617465726d61726b")},
		{decodeRetiredCLIInput(t, "2d2d73746167652d726561642d776f726b657273")},
		{decodeRetiredCLIInput(t, "2d2d69616d2d776f726b657273")},
		{decodeRetiredCLIInput(t, "2d2d65787065637465642d6461746162617365")},
		{"unexpected"},
	}
	for _, args := range tests {
		if _, err := parseCLIOptions(args); err == nil {
			t.Fatalf("retired or unknown CLI input was accepted: %v", args)
		}
	}
}

func decodeRetiredCLIInput(t *testing.T, encoded string) string {
	t.Helper()
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return string(decoded)
}

func TestParseCLIOptionsOverrides(t *testing.T) {
	opts, err := parseCLIOptions([]string{"--config", "/tmp/seeddata.yaml", "--verbose", "--check-config"})
	if err != nil {
		t.Fatalf("parse cli options: %v", err)
	}
	if opts.configPath != "/tmp/seeddata.yaml" {
		t.Fatalf("unexpected config path: %q", opts.configPath)
	}
	if !opts.verbose {
		t.Fatalf("expected verbose=true")
	}
	if !opts.checkConfig {
		t.Fatalf("expected check-config=true")
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
