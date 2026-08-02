package cd

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepositoryFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repositoryRoot(t), path))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func requireContains(t *testing.T, body string, values ...string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(body, value) {
			t.Fatalf("expected contract to contain %q", value)
		}
	}
}

func TestCDWorkflowDeploysTheSuccessfulCISHA(t *testing.T) {
	workflow := readRepositoryFile(t, ".github/workflows/cd.yml")
	requireContains(t, workflow,
		`workflows: ["CI"]`,
		"branches: [main]",
		"github.event.workflow_run.conclusion == 'success'",
		"github.event.workflow_run.head_sha",
		"group: seeddata-production-deploy",
		"cancel-in-progress: false",
		"name: production",
		"group: qlume",
		"labels: [self-hosted, macOS, ARM64, ops]",
		"actions/upload-artifact@v4",
		"actions/download-artifact@v4",
	)
	for _, forbidden := range []string{"git pull", "git reset", "IAM_MOCK_CONSUMER_SHARED_SECRET:", "SVRA_SUDO_PASSWORD"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("CD workflow must not contain %q", forbidden)
		}
	}
}

func TestBuildPackageIsImmutableAndSelfVerifying(t *testing.T) {
	build := readRepositoryFile(t, "scripts/cd/build-package.sh")
	requireContains(t, build,
		`: "${DEPLOY_SHA:?DEPLOY_SHA is required}"`,
		"CGO_ENABLED=0 GOOS=linux GOARCH=amd64",
		"go build -trimpath",
		"go version -m",
		"vcs.revision=${DEPLOY_SHA}",
		"sha256sum",
		"build-metadata.env",
		"seeddata-runner-linux-amd64.tar.gz",
	)
}

func TestSSHSetupPinsTheProductionHostKey(t *testing.T) {
	setup := readRepositoryFile(t, "scripts/cd/setup-runner-ssh.sh")
	requireContains(t, setup,
		"RUNNER_SSH_FINGERPRINT",
		"ssh-keyscan",
		"ssh-keygen -lf",
		"StrictHostKeyChecking yes",
		"UserKnownHostsFile",
		"EXPECTED_HOSTNAME",
	)
	for _, forbidden := range []string{"StrictHostKeyChecking accept-new", "StrictHostKeyChecking no"} {
		if strings.Contains(setup, forbidden) {
			t.Fatalf("SSH setup must not contain %q", forbidden)
		}
	}
}

func TestRunnerUsesExistingNopasswdPolicyWithoutElevatingScripts(t *testing.T) {
	runner := readRepositoryFile(t, "scripts/cd/runner-deploy.sh")
	requireContains(t, runner,
		`"sudo -n /usr/bin/true"`,
		`"'$REMOTE_DIR/remote-deploy.sh' --package '$REMOTE_PACKAGE' --sha '$DEPLOY_SHA'"`,
		`"'$REMOTE_DIR/remote-rollback.sh' --backup '$ROLLBACK_BACKUP'"`,
		"scripts/cd/seeddata-runner-preflight.service",
		"scripts/cd/retire-removed-config.sh",
	)
	for _, forbidden := range []string{"sudo -S", "SUDO_PASSWORD", "RUNNER_SUDO_PASSWORD", "SVRA_SUDO_PASSWORD"} {
		if strings.Contains(runner, forbidden) {
			t.Fatalf("runner must not contain %q", forbidden)
		}
	}
}

func TestRunnerInvokesRollbackDirectlyUsingMockedTransport(t *testing.T) {
	root := repositoryRoot(t)
	fakeBin := t.TempDir()
	traceFile := filepath.Join(t.TempDir(), "trace")
	fakeCommand := `#!/bin/sh
printf '%s\n' "$*" >>"$SUDO_TRACE"
`
	for _, name := range []string{"ssh", "scp"} {
		path := filepath.Join(fakeBin, name)
		if err := os.WriteFile(path, []byte(fakeCommand), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command("bash", filepath.Join(root, "scripts/cd/runner-deploy.sh"))
	cmd.Dir = root
	cmd.Env = []string{
		"PATH=" + fakeBin + ":/usr/bin:/bin",
		"RUNNER_SSH_ALIAS=contract-test",
		"RUNNER_SSH_CONFIG=/dev/null",
		"SUDO_TRACE=" + traceFile,
		"OPERATION=rollback",
		"ROLLBACK_BACKUP=latest",
		"GITHUB_RUN_ID=12345",
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runner failed with mocked transport: %v\n%s", err, output)
	}
	trace, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatal(err)
	}
	traceText := string(trace)
	if !strings.Contains(traceText, "sudo -n /usr/bin/true") {
		t.Fatalf("runner did not verify NOPASSWD access\n%s", trace)
	}
	if !strings.Contains(traceText, "/remote-rollback.sh' --backup 'latest'") {
		t.Fatalf("runner did not invoke rollback script directly\n%s", trace)
	}
	if strings.Contains(traceText, "sudo -S") || strings.Contains(traceText, "sudo -n '/tmp/") {
		t.Fatalf("runner elevated an uploaded script or requested a password\n%s", trace)
	}
}

func TestRemoteScriptsOnlyElevateAllowlistedCommands(t *testing.T) {
	allowed := map[string]bool{
		"/usr/bin/grep":       true,
		"/usr/bin/install":    true,
		"/usr/bin/journalctl": true,
		"/usr/bin/ls":         true,
		"/usr/bin/rsync":      true,
		"/usr/bin/sha256sum":  true,
		"/usr/bin/stat":       true,
		"/usr/bin/systemctl":  true,
		"/usr/bin/test":       true,
		"/usr/bin/true":       true,
	}
	sudoCommand := regexp.MustCompile(`sudo -n (/usr/bin/[a-z0-9-]+)`)
	var all strings.Builder
	for _, path := range []string{
		"scripts/cd/runner-deploy.sh",
		"scripts/cd/remote-common.sh",
		"scripts/cd/remote-deploy.sh",
		"scripts/cd/remote-rollback.sh",
		"scripts/cd/retire-removed-config.sh",
	} {
		body := readRepositoryFile(t, path)
		all.WriteString(body)
		matches := sudoCommand.FindAllStringSubmatch(body, -1)
		if count := strings.Count(body, "sudo -n "); count != len(matches) {
			t.Fatalf("%s contains a sudo -n invocation without a literal /usr/bin command", path)
		}
		for _, match := range matches {
			if !allowed[match[1]] {
				t.Fatalf("%s elevates command outside the serverA allowlist: %s", path, match[1])
			}
		}
	}
	for _, forbidden := range []string{
		"sudo -S",
		"sudo -n /bin/bash",
		"sudo -n /usr/bin/env",
		"sudo -n /bin/sh",
		"systemd-run",
		"SVRA_SUDO_PASSWORD",
	} {
		if strings.Contains(all.String(), forbidden) {
			t.Fatalf("deployment scripts must not contain %q", forbidden)
		}
	}
}

func TestRemoteDeployPreservesProductionStateAndSupportsImmediateRollback(t *testing.T) {
	common := readRepositoryFile(t, "scripts/cd/remote-common.sh")
	deploy := readRepositoryFile(t, "scripts/cd/remote-deploy.sh")
	rollback := readRepositoryFile(t, "scripts/cd/remote-rollback.sh")

	requireContains(t, common,
		`SERVICE="seeddata-runner.service"`,
		`APP_ROOT="/root/workspace/golang/src/github.com/fangcun-mount/seeddata-runner"`,
		`CONFIG_FILE="${APP_ROOT}/configs/seeddata.yaml"`,
		`ENV_FILE="/etc/default/seeddata-runner"`,
		`STATE_DIR="${APP_ROOT}/.seeddata-cache"`,
		`BACKUP_DIR="/opt/backups/seeddata-runner/deployments"`,
		"flock -n",
		"EnvironmentFiles",
		"WorkingDirectory",
		"sudo_rsync --archive --checksum",
		`/proc/${pid}/exe`,
	)
	requireContains(t, deploy,
		"backup_current_binary",
		"install_binary_atomically",
		"sudo_systemctl restart",
		"seeddata-runner-preflight.service",
		"rollback_immediate",
		"rollback_allowed=0",
	)
	requireContains(t, rollback,
		"resolve_rollback_backup",
		"stored_binary_sha256",
		"backup_current_binary",
		"install_binary_atomically",
		"verify_running_binary",
	)
	firstRunningVerification := strings.Index(deploy, `verify_running_binary "$EXPECTED_BINARY_SHA"`)
	disableAutomaticRollback := strings.LastIndex(deploy, "rollback_allowed=0")
	postStartupWait := strings.Index(deploy, "sleep 5")
	if firstRunningVerification < 0 || disableAutomaticRollback <= firstRunningVerification || postStartupWait <= disableAutomaticRollback {
		t.Fatal("automatic rollback must be disabled immediately after the first running-binary verification")
	}

	all := common + deploy + rollback
	for _, forbidden := range []string{
		`rm -rf "${STATE_DIR}"`,
		`rm -f "${CONFIG_FILE}"`,
		`cp -f "${CONFIG_FILE}"`,
		`source "${ENV_FILE}"`,
		`. "${ENV_FILE}"`,
		"git pull",
	} {
		if strings.Contains(all, forbidden) {
			t.Fatalf("deployment scripts must not contain %q", forbidden)
		}
	}
}

func TestPreflightUnitUsesTheProductionRuntimeContract(t *testing.T) {
	unit := readRepositoryFile(t, "scripts/cd/seeddata-runner-preflight.service")
	requireContains(t, unit,
		"Type=oneshot",
		"User=root",
		"WorkingDirectory=/root/workspace/golang/src/github.com/fangcun-mount/seeddata-runner",
		"EnvironmentFile=/etc/default/seeddata-runner",
		"ExecStart=/root/workspace/golang/src/github.com/fangcun-mount/seeddata-runner/bin/seeddata.preflight --config /root/workspace/golang/src/github.com/fangcun-mount/seeddata-runner/configs/seeddata.yaml --check-config",
	)
}

func TestRemovedConfigMigrationOnlyDeletesTheSelectedTopLevelBlock(t *testing.T) {
	root := repositoryRoot(t)
	script := filepath.Join(root, "scripts/cd/retire-removed-config.sh")
	keyCommand := exec.Command("bash", "-c", `. "$1"; printf '%s' "$RETIRED_CONFIG_KEY"`, "key", script)
	keyOutput, err := keyCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("read retired key: %v\n%s", err, keyOutput)
	}
	key := string(keyOutput)
	if key == "" {
		t.Fatal("retired key is empty")
	}

	tempDir := t.TempDir()
	source := filepath.Join(tempDir, "source.yaml")
	destination := filepath.Join(tempDir, "destination.yaml")
	fixture := "daily:\n  enabled: true\n\n" + key + ":\n  workers: 8\n  nested:\n    value: keep-out\n\nplan:\n  enabled: true\n"
	if err := os.WriteFile(source, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", "-c", `. "$1"; strip_removed_config_block "$2" "$3"`, "strip", script, source, destination)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("strip removed config block: %v\n%s", err, output)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	want := "daily:\n  enabled: true\n\nplan:\n  enabled: true\n"
	if string(got) != want {
		t.Fatalf("unexpected migrated config:\n%s", got)
	}

	liveConfig := filepath.Join(tempDir, "live.yaml")
	backupDir := filepath.Join(tempDir, "backups")
	workDir := filepath.Join(tempDir, "work")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(liveConfig, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(root, "scripts/cd/remote-common.sh")
	migrate := exec.Command("bash", "-c", `
. "$1"
. "$2"
CONFIG_FILE="$3"
CONFIG_RETIREMENT_BACKUP_DIR="$4"
sudo_grep() { grep "$@"; }
sudo_stat() { printf '600 %s %s\n' "$(id -u)" "$(id -g)"; }
sudo_install() { install "$@"; }
binary_sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
retire_removed_config_block "$5"
printf '%s\n%s\n' "$CONFIG_RETIREMENT_CHANGED" "$CONFIG_RETIREMENT_BACKUP"
`, "migrate", common, script, liveConfig, backupDir, workDir)
	migrationOutput, err := migrate.CombinedOutput()
	if err != nil {
		t.Fatalf("migrate production config: %v\n%s", err, migrationOutput)
	}
	lines := strings.Split(strings.TrimSpace(string(migrationOutput)), "\n")
	if len(lines) < 3 || lines[len(lines)-2] != "1" {
		t.Fatalf("migration did not report a changed config:\n%s", migrationOutput)
	}
	backupPath := lines[len(lines)-1]
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read config backup: %v", err)
	}
	if string(backup) != fixture {
		t.Fatal("config backup did not preserve the original bytes")
	}
	migrated, err := os.ReadFile(liveConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(migrated) != want {
		t.Fatalf("live config was not migrated:\n%s", migrated)
	}
	restore := exec.Command("bash", "-c", `
. "$1"
. "$2"
CONFIG_FILE="$3"
CONFIG_RETIREMENT_CHANGED=1
CONFIG_RETIREMENT_BACKUP="$4"
CONFIG_RETIREMENT_MODE=600
CONFIG_RETIREMENT_UID="$(id -u)"
CONFIG_RETIREMENT_GID="$(id -g)"
sudo_install() { install "$@"; }
restore_removed_config_block
`, "restore", common, script, liveConfig, backupPath)
	if output, err := restore.CombinedOutput(); err != nil {
		t.Fatalf("restore production config: %v\n%s", err, output)
	}
	restored, err := os.ReadFile(liveConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != fixture {
		t.Fatal("config restore did not reproduce the original bytes")
	}
}

func TestCDShellScriptsParse(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{
		"scripts/cd/build-package.sh",
		"scripts/cd/setup-runner-ssh.sh",
		"scripts/cd/runner-deploy.sh",
		"scripts/cd/remote-common.sh",
		"scripts/cd/remote-deploy.sh",
		"scripts/cd/remote-rollback.sh",
		"scripts/cd/retire-removed-config.sh",
	} {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			cmd := exec.Command("bash", "-n", filepath.Join(root, path))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bash -n %s: %v\n%s", path, err, output)
			}
		})
	}
}

func TestRemoteChecksumValidators(t *testing.T) {
	common := filepath.Join(repositoryRoot(t), "scripts/cd/remote-common.sh")
	tests := []struct {
		name      string
		validator string
		value     string
		wantOK    bool
	}{
		{name: "git sha", validator: "validate_git_sha", value: strings.Repeat("a", 40), wantOK: true},
		{name: "short git sha", validator: "validate_git_sha", value: strings.Repeat("a", 39)},
		{name: "uppercase git sha", validator: "validate_git_sha", value: strings.Repeat("A", 40)},
		{name: "binary sha", validator: "validate_sha256", value: strings.Repeat("b", 64), wantOK: true},
		{name: "long binary sha", validator: "validate_sha256", value: strings.Repeat("b", 65)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", `. "$1"; "$2" "$3"`, "validator", common, tt.validator, tt.value)
			err := cmd.Run()
			if (err == nil) != tt.wantOK {
				t.Fatalf("%s(%q) error=%v", tt.validator, tt.value, err)
			}
		})
	}
}

func TestRunnerDeployRejectsUnsafeRollbackSelector(t *testing.T) {
	root := repositoryRoot(t)
	cmd := exec.Command("bash", filepath.Join(root, "scripts/cd/runner-deploy.sh"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"RUNNER_SSH_ALIAS=contract-test",
		"RUNNER_SSH_CONFIG=/dev/null",
		"OPERATION=rollback",
		"ROLLBACK_BACKUP=../outside",
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("unsafe rollback selector was accepted")
	}
	if !strings.Contains(string(output), "invalid rollback backup selector") {
		t.Fatalf("unexpected rejection output: %s", output)
	}
}
