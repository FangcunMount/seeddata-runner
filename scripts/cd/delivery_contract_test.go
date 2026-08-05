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
		"actions/upload-artifact@v7",
		"actions/download-artifact@v8",
		"vars.SEEDDATA_DEPLOY_TARGET != 'macmini'",
	)
	for _, forbidden := range []string{"git pull", "git reset", "IAM_MOCK_CONSUMER_SHARED_SECRET:", "SVRA_SUDO_PASSWORD"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("CD workflow must not contain %q", forbidden)
		}
	}
}

func TestContainerImageIsARM64AndRunsWithoutRoot(t *testing.T) {
	dockerfile := readRepositoryFile(t, "Dockerfile")
	requireContains(t, dockerfile,
		"ARG TARGETARCH=arm64",
		"CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH",
		"apk add --no-cache ca-certificates curl tzdata",
		"USER 10001:10001",
		`CMD ["--config", "/run/seeddata/config.yaml"]`,
		"org.opencontainers.image.revision",
	)
	for _, value := range []string{"IAM_MOCK_CONSUMER_SHARED_SECRET", "COPY configs/seeddata.yaml"} {
		if strings.Contains(dockerfile, value) {
			t.Fatalf("Dockerfile must not contain %q", value)
		}
	}
}

func TestMacComposeHasSingleHardenedPersistentService(t *testing.T) {
	compose := readRepositoryFile(t, "deploy/macmini/compose.yaml")
	requireContains(t, compose,
		"platform: linux/arm64",
		"pull_policy: never",
		"restart: unless-stopped",
		"read_only: true",
		"no-new-privileges:true",
		"TZ: Asia/Shanghai",
		"extra_hosts:",
		"qs.fangcunmount.cn=${SEEDDATA_SERVERA_TAILSCALE_IP",
		"collect.fangcunmount.cn=${SEEDDATA_SERVERA_TAILSCALE_IP",
		"iam.fangcunmount.cn=${SEEDDATA_SERVERA_TAILSCALE_IP",
		"target: /app/.seeddata-cache",
		"--check-config",
		"max-size: 20m",
	)
	for _, value := range []string{"ports:", "privileged:", "network_mode: host", "latest"} {
		if strings.Contains(compose, value) {
			t.Fatalf("Mac Compose contract must not contain %q", value)
		}
	}
	if count := strings.Count(compose, "  seeddata-runner:\n"); count != 1 {
		t.Fatalf("expected exactly one seeddata service, got %d", count)
	}
}

func TestContainerPackageIsImmutableAndSelfVerifying(t *testing.T) {
	build := readRepositoryFile(t, "scripts/cd/build-container-package.sh")
	requireContains(t, build,
		`: "${DEPLOY_SHA:?DEPLOY_SHA is required}"`,
		"--platform linux/arm64",
		`IMAGE="seeddata-runner:${DEPLOY_SHA}"`,
		"docker image save",
		"container-metadata.env",
		"container-SHA256SUMS",
		"seeddata-runner-linux-arm64-image.tar.gz",
		"org.opencontainers.image.revision",
	)
}

func TestMacWorkflowSeparatesPreflightFromProductionCutover(t *testing.T) {
	workflow := readRepositoryFile(t, ".github/workflows/macmini.yml")
	requireContains(t, workflow,
		`workflows: ["CI"]`,
		"group: seeddata-production-deploy",
		"cancel-in-progress: false",
		"host-preflight",
		"vars.SEEDDATA_DEPLOY_TARGET == 'macmini'",
		"labels: [self-hosted, macOS, ARM64, ops]",
		"docker/setup-qemu-action@v3",
		"docker/setup-buildx-action@v3",
		"actions/upload-artifact@v7",
		"actions/download-artifact@v8",
		"seeddata-runner-linux-arm64-${{ env.DEPLOY_SHA }}",
		"SEEDDATA_SERVERA_TAILSCALE_IP: ${{ vars.SVRA_TAILSCALE_IP }}",
		"macmini-cutover.sh",
		"macmini-rollback.sh",
		"macmini-return-servera.sh",
		"name: production",
	)
	for _, value := range []string{"IAM_MOCK_CONSUMER_SHARED_SECRET:", "SVRA_SUDO_PASSWORD", "seeddata-runner:latest", "actions/upload-artifact@v4", "actions/download-artifact@v4"} {
		if strings.Contains(workflow, value) {
			t.Fatalf("Mac workflow must not contain %q", value)
		}
	}
}

func TestMacPreflightPinsProductionDomainsToServerATailnet(t *testing.T) {
	common := readRepositoryFile(t, "scripts/cd/macmini-common.sh")
	hostPreflight := readRepositoryFile(t, "scripts/cd/macmini-host-preflight.sh")
	compose := readRepositoryFile(t, "deploy/macmini/compose.yaml")

	requireContains(t, common,
		"SEEDDATA_SERVERA_TAILSCALE_IP is required",
		"SEEDDATA_SERVERA_TAILSCALE_IP must be an IPv4 address in 100.64.0.0/10",
		`--add-host "qs.fangcunmount.cn:${SERVERA_TAILSCALE_IP}"`,
		`--add-host "collect.fangcunmount.cn:${SERVERA_TAILSCALE_IP}"`,
		`--add-host "iam.fangcunmount.cn:${SERVERA_TAILSCALE_IP}"`,
		`SEEDDATA_SERVERA_TAILSCALE_IP="$SERVERA_TAILSCALE_IP"`,
	)
	requireContains(t, hostPreflight,
		`--resolve "${host}:443:${SERVERA_TAILSCALE_IP}"`,
		`"${TAILSCALE_HOST_ARGS[@]}"`,
		"https://qs.fangcunmount.cn/healthz",
		"https://collect.fangcunmount.cn/readyz",
		"https://iam.fangcunmount.cn/healthz",
	)
	for _, body := range []string{common, hostPreflight, compose} {
		if strings.Contains(body, "47.94.204.124") {
			t.Fatal("Mac deployment must not hard-code the public ServerA address")
		}
	}
}

func TestReturnToServerAPreservesLatestStateAndRemovesMacInstance(t *testing.T) {
	returnScript := readRepositoryFile(t, "scripts/cd/macmini-return-servera.sh")
	requireContains(t, returnScript,
		`REMOTE_SERVICE="seeddata-runner.service"`,
		`wait_for_healthy_container "$current_image_id"`,
		`compose_seeddata "$current_image" down --remove-orphans`,
		`backup_container_state "before-return-servera"`,
		`scp -F "$RUNNER_SSH_CONFIG" -r "$STATE_DIR/."`,
		"sudo -n /usr/bin/rsync --archive --checksum --delete",
		"sudo -n /usr/bin/systemctl enable '$REMOTE_SERVICE'",
		"sudo -n /usr/bin/systemctl start '$REMOTE_SERVICE'",
		`write_container_receipt "return-servera"`,
		"restoring the prior Mac container",
	)
	down := strings.Index(returnScript, `compose_seeddata "$current_image" down --remove-orphans`)
	copyState := strings.Index(returnScript, "sudo -n /usr/bin/rsync --archive --checksum --delete")
	start := strings.Index(returnScript, "sudo -n /usr/bin/systemctl start '$REMOTE_SERVICE'")
	if down < 0 || copyState <= down || start <= copyState {
		t.Fatal("return must remove the Mac instance, copy current state, then start ServerA")
	}
}

func TestMacPreflightValidatesSecretWithoutPrintingIt(t *testing.T) {
	common := readRepositoryFile(t, "scripts/cd/macmini-common.sh")
	requireContains(t, common,
		"SEEDDATA_MAC_ROOT must be a child of HOME",
		"container archive checksum mismatch",
		"loaded image ID does not match metadata",
		"--check-config",
		".container-write-probe",
		"X-IAM-Seed-Secret: ${IAM_MOCK_CONSUMER_SHARED_SECRET}",
		`[ "$code" = "400" ]`,
		"container restarted during verification",
	)
	for _, value := range []string{`. "$ENV_FILE"`, `source "$ENV_FILE"`, "echo $IAM_MOCK_CONSUMER_SHARED_SECRET"} {
		if strings.Contains(common, value) {
			t.Fatalf("Mac deployment common code must not contain %q", value)
		}
	}
}

func TestInitialCutoverPreservesSingleInstanceBoundary(t *testing.T) {
	cutover := readRepositoryFile(t, "scripts/cd/macmini-cutover.sh")
	requireContains(t, cutover,
		`REMOTE_SERVICE="seeddata-runner.service"`,
		"candidate_runtime_preflight",
		"sudo -n /usr/bin/systemctl stop '$REMOTE_SERVICE'",
		"sudo -n /usr/bin/rsync --archive --checksum --delete",
		"server_stopped=1",
		"macmini-deploy.sh",
		"sudo -n /usr/bin/systemctl disable '$REMOTE_SERVICE'",
		"ServerA remains stopped to protect the single-instance state contract",
		"restarting ServerA",
	)
	preflight := strings.Index(cutover, `candidate_runtime_preflight "$TARGET_IMAGE"`)
	stop := strings.Index(cutover, "sudo -n /usr/bin/systemctl stop '$REMOTE_SERVICE'")
	stateCopy := strings.Index(cutover, "sudo -n /usr/bin/rsync --archive --checksum --delete")
	deploy := strings.Index(cutover, `"$SCRIPT_DIR/macmini-deploy.sh"`)
	disable := strings.Index(cutover, "sudo -n /usr/bin/systemctl disable '$REMOTE_SERVICE'")
	if preflight < 0 || stop <= preflight || stateCopy <= stop || deploy <= stateCopy || disable <= deploy {
		t.Fatal("cutover must preflight, stop, copy state, deploy, then disable ServerA in that order")
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
		"scripts/cd/macmini-cutover.sh",
		"scripts/cd/macmini-return-servera.sh",
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
		`"error":[[:space:]]*"context canceled"`,
	)
	requireContains(t, deploy,
		"backup_current_binary",
		"install_binary_atomically",
		"sudo_systemctl restart",
		"seeddata-runner-preflight.service",
		"rollback_immediate",
		"rollback_allowed=0",
		"journal_contains_runtime_failure",
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

func TestJournalFailureFilterAllowsOnlyGracefulSupervisorCancellation(t *testing.T) {
	common := filepath.Join(repositoryRoot(t), "scripts/cd/remote-common.sh")
	tests := []struct {
		name        string
		log         string
		wantFailure bool
	}{
		{name: "clean", log: "Seeddata configuration check passed\n"},
		{name: "graceful stop", log: `Seeddata supervisor exited with error {"error": "context canceled"}` + "\n"},
		{name: "supervisor failure", log: `Seeddata supervisor exited with error {"error": "connection refused"}` + "\n", wantFailure: true},
		{name: "dependency failure", log: "Initialize seeddata dependencies failed\n", wantFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logFile := filepath.Join(t.TempDir(), "journal.log")
			if err := os.WriteFile(logFile, []byte(tt.log), 0o600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("bash", "-c", `. "$1"; journal_contains_runtime_failure "$2"`, "journal", common, logFile)
			err := command.Run()
			if (err == nil) != tt.wantFailure {
				t.Fatalf("journal_contains_runtime_failure error=%v, wantFailure=%v", err, tt.wantFailure)
			}
		})
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
