#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck source=remote-common.sh
. "$SCRIPT_DIR/remote-common.sh"

PACKAGE_FILE=""
DEPLOY_SHA=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --package) PACKAGE_FILE="${2:-}"; shift 2 ;;
    --sha) DEPLOY_SHA="${2:-}"; shift 2 ;;
    *) fail "unknown remote deploy argument: $1"; exit 2 ;;
  esac
done

[ -f "$PACKAGE_FILE" ] || fail "deployment package is missing: $PACKAGE_FILE"
validate_git_sha "$DEPLOY_SHA" || fail "--sha must be a 40-character lowercase Git SHA"

seeddata_cd_init

STAGE_DIR=$(mktemp -d "/tmp/seeddata-deploy-${DEPLOY_SHA%${DEPLOY_SHA#????????????}}.XXXXXX")
BACKUP_PATH=""
EXPECTED_BINARY_SHA=""
deployed=0
rollback_allowed=0

cleanup() {
  rm -rf "$STAGE_DIR"
}

rollback_immediate() {
  local backup_sha actual_backup_sha
  [ -n "$BACKUP_PATH" ] || return 1
  backup_sha=$(stored_binary_sha256 "$BACKUP_PATH/binary.sha256")
  validate_sha256 "$backup_sha" || return 1
  actual_backup_sha=$(binary_sha256 "$BACKUP_PATH/seeddata")
  [ "$actual_backup_sha" = "$backup_sha" ] || return 1
  echo "New binary failed before startup verification; restoring $BACKUP_PATH" >&2
  sudo_systemctl stop "$SERVICE" || true
  install_binary_atomically "$BACKUP_PATH/seeddata" "$backup_sha"
  sudo_systemctl reset-failed "$SERVICE" >/dev/null 2>&1 || true
  sudo_systemctl start "$SERVICE"
  wait_for_active_service
  verify_running_binary "$backup_sha"
  write_deployment_receipt "automatic-rollback" "unknown" "$backup_sha" "$BACKUP_PATH"
}

handle_error() {
  local rc=$?
  trap - ERR
  if [ "$deployed" -eq 1 ] && [ "$rollback_allowed" -eq 1 ]; then
    if ! rollback_immediate; then
      echo "Automatic binary rollback also failed; service requires immediate operator attention" >&2
    fi
  elif [ "$deployed" -eq 1 ]; then
    echo "Deployment failed after startup verification; binary and state were left in place for operator review" >&2
  fi
  exit "$rc"
}

trap cleanup EXIT
trap handle_error ERR

gzip -t "$PACKAGE_FILE"
while IFS= read -r entry; do
  case "$entry" in
    /*|../*|*/../*|*/..) fail "unsafe archive entry: $entry" ;;
  esac
done < <(tar -tzf "$PACKAGE_FILE")
tar -xzf "$PACKAGE_FILE" -C "$STAGE_DIR"

for required in bin/seeddata SHA256SUMS build-metadata.env; do
  [ -f "$STAGE_DIR/$required" ] || fail "deployment package is missing $required"
done
grep -Fxq "git_sha=$DEPLOY_SHA" "$STAGE_DIR/build-metadata.env" || fail "deployment metadata Git SHA mismatch"
EXPECTED_BINARY_SHA=$(awk '$2 == "bin/seeddata" {print $1}' "$STAGE_DIR/SHA256SUMS")
validate_sha256 "$EXPECTED_BINARY_SHA" || fail "deployment package contains an invalid binary checksum"
[ "$(wc -l <"$STAGE_DIR/SHA256SUMS" | tr -d '[:space:]')" = "1" ] || fail "deployment package checksum list must contain one entry"
printf '%s  bin/seeddata\n' "$EXPECTED_BINARY_SHA" | (cd "$STAGE_DIR" && sha256sum -c -)
chmod 0755 "$STAGE_DIR/bin/seeddata"

run_config_preflight "$STAGE_DIR/bin/seeddata" "$SCRIPT_DIR/seeddata-runner-preflight.service"

deploy_started_epoch=$(date +%s)
backup_current_binary "deploy"
BACKUP_PATH="$BACKUP_RESULT"
install_binary_atomically "$STAGE_DIR/bin/seeddata" "$EXPECTED_BINARY_SHA"
deployed=1
rollback_allowed=1

sudo_systemctl reset-failed "$SERVICE"
sudo_systemctl restart "$SERVICE"
wait_for_active_service
verify_running_binary "$EXPECTED_BINARY_SHA"
restart_count_after_start=$(service_restart_count)
if [ "$restart_count_after_start" -ne 0 ]; then
  fail "service restarted automatically during initial verification: NRestarts=${restart_count_after_start}"
fi

# The supervisor is now live and may start business work immediately. From this
# point on preserve the binary and durable state for diagnosis instead of
# attempting an automatic rollback.
rollback_allowed=0

sleep 5
wait_for_active_service
verify_running_binary "$EXPECTED_BINARY_SHA"

restart_count_after=$(service_restart_count)
if [ "$restart_count_after" -ne 0 ]; then
  fail "service restart loop detected after startup verification: NRestarts=${restart_count_after}"
fi

RECENT_LOG=$(mktemp "/tmp/seeddata-deploy-log.XXXXXX")
trap 'rm -f "$RECENT_LOG"; cleanup' EXIT
sudo_journalctl -u "$SERVICE" --since "@${deploy_started_epoch}" --no-pager -o cat >"$RECENT_LOG"
if journal_contains_runtime_failure "$RECENT_LOG"; then
  fail "post-deploy journal contains a startup or immediate runtime failure"
fi

write_deployment_receipt "deploy" "$DEPLOY_SHA" "$EXPECTED_BINARY_SHA" "$BACKUP_PATH"
echo "Seeddata deployment completed: git_sha=${DEPLOY_SHA} binary_sha256=${EXPECTED_BINARY_SHA}"
echo "Previous binary backup: $BACKUP_PATH"
echo "Production config, EnvironmentFile, and $STATE_DIR were preserved"
