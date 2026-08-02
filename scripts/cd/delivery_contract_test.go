package cd

import (
	"os"
	"os/exec"
	"path/filepath"
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
		"RUNNER_SUDO_PASSWORD: ${{ secrets.SVRA_SUDO_PASSWORD }}",
	)
	if count := strings.Count(workflow, "RUNNER_SUDO_PASSWORD: ${{ secrets.SVRA_SUDO_PASSWORD }}"); count != 2 {
		t.Fatalf("deploy and rollback must both receive the sudo secret; got %d references", count)
	}
	for _, forbidden := range []string{"git pull", "git reset", "IAM_MOCK_CONSUMER_SHARED_SECRET:"} {
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

func TestRunnerPassesSudoPasswordThroughStandardInput(t *testing.T) {
	runner := readRepositoryFile(t, "scripts/cd/runner-deploy.sh")
	requireContains(t, runner,
		`SUDO_PASSWORD="$RUNNER_SUDO_PASSWORD"`,
		"unset RUNNER_SUDO_PASSWORD",
		"export -n SUDO_PASSWORD",
		`printf '%s\n' "$SUDO_PASSWORD" |`,
		`"sudo -S -k -p '' -- $remote_command"`,
		`run_remote_sudo "true"`,
		`"'$REMOTE_DIR/remote-deploy.sh' --package '$REMOTE_PACKAGE' --sha '$DEPLOY_SHA'"`,
		`"'$REMOTE_DIR/remote-rollback.sh' --backup '$ROLLBACK_BACKUP'"`,
	)
	for _, forbidden := range []string{"sudo -n", `echo "$SUDO_PASSWORD"`, `export SUDO_PASSWORD`} {
		if strings.Contains(runner, forbidden) {
			t.Fatalf("sudo credential handling must not contain %q", forbidden)
		}
	}
}

func TestRunnerRejectsMissingSudoPassword(t *testing.T) {
	root := repositoryRoot(t)
	cmd := exec.Command("bash", filepath.Join(root, "scripts/cd/runner-deploy.sh"))
	cmd.Dir = root
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"RUNNER_SSH_ALIAS=contract-test",
		"RUNNER_SSH_CONFIG=/dev/null",
		"OPERATION=rollback",
		"ROLLBACK_BACKUP=latest",
	}
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("missing sudo password was accepted")
	}
	if !strings.Contains(string(output), "RUNNER_SUDO_PASSWORD is required") {
		t.Fatalf("unexpected rejection output: %s", output)
	}
}

func TestRunnerDoesNotExposeSudoPasswordToCommandsOrChildEnvironment(t *testing.T) {
	root := repositoryRoot(t)
	fakeBin := t.TempDir()
	traceFile := filepath.Join(t.TempDir(), "trace")
	secret := "contract-secret-%-!-$"
	fakeCommand := `#!/bin/sh
printf '%s\n' "$*" >>"$SUDO_TRACE"
if [ "${RUNNER_SUDO_PASSWORD+x}" = x ] || [ "${SUDO_PASSWORD+x}" = x ]; then
  echo "sudo secret leaked into child environment" >&2
  exit 92
fi
case "$*" in
  *"sudo -S -k"*)
    IFS= read -r supplied
    [ "$supplied" = "$EXPECTED_TEST_PASSWORD" ] || exit 93
    ;;
esac
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
		"RUNNER_SUDO_PASSWORD=" + secret,
		"EXPECTED_TEST_PASSWORD=" + secret,
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
	if strings.Contains(string(output), secret) || strings.Contains(string(trace), secret) {
		t.Fatal("sudo password appeared in command output or arguments")
	}
	if count := strings.Count(string(trace), "sudo -S -k"); count != 2 {
		t.Fatalf("preflight and rollback must both authenticate with sudo; got %d calls\n%s", count, trace)
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
		"--check-config",
		`/proc/${pid}/exe`,
	)
	requireContains(t, deploy,
		"backup_current_binary",
		"install_binary_atomically",
		"systemctl restart",
		"rollback_immediate",
		"rollback_allowed=0",
	)
	requireContains(t, rollback,
		"resolve_rollback_backup",
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

func TestCDShellScriptsParse(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{
		"scripts/cd/build-package.sh",
		"scripts/cd/setup-runner-ssh.sh",
		"scripts/cd/runner-deploy.sh",
		"scripts/cd/remote-common.sh",
		"scripts/cd/remote-deploy.sh",
		"scripts/cd/remote-rollback.sh",
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
