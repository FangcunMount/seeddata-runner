#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck source=remote-common.sh
. "$SCRIPT_DIR/remote-common.sh"

ROLLBACK_BACKUP="latest"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --backup) ROLLBACK_BACKUP="${2:-}"; shift 2 ;;
    *) fail "unknown remote rollback argument: $1"; exit 2 ;;
  esac
done

seeddata_cd_init
resolve_rollback_backup "$ROLLBACK_BACKUP"
TARGET_BACKUP="$ROLLBACK_RESULT"
TARGET_SHA=$(stored_binary_sha256 "$TARGET_BACKUP/binary.sha256")
validate_sha256 "$TARGET_SHA" || fail "rollback backup checksum is invalid"
[ "$(binary_sha256 "$TARGET_BACKUP/seeddata")" = "$TARGET_SHA" ] || fail "rollback backup checksum does not match"

SOURCE_BACKUP=""
rollback_allowed=0

restore_source_binary() {
  local source_sha actual_source_sha
  [ -n "$SOURCE_BACKUP" ] || return 1
  source_sha=$(stored_binary_sha256 "$SOURCE_BACKUP/binary.sha256")
  validate_sha256 "$source_sha" || return 1
  actual_source_sha=$(binary_sha256 "$SOURCE_BACKUP/seeddata")
  [ "$actual_source_sha" = "$source_sha" ] || return 1
  echo "Requested rollback failed; restoring pre-rollback binary $SOURCE_BACKUP" >&2
  sudo_systemctl stop "$SERVICE" || true
  install_binary_atomically "$SOURCE_BACKUP/seeddata" "$source_sha"
  sudo_systemctl reset-failed "$SERVICE" >/dev/null 2>&1 || true
  sudo_systemctl start "$SERVICE"
  wait_for_active_service
  verify_running_binary "$source_sha"
}

handle_error() {
  local rc=$?
  trap - ERR
  if [ "$rollback_allowed" -eq 1 ] && ! restore_source_binary; then
    echo "Restoring the pre-rollback binary also failed; service requires immediate operator attention" >&2
  fi
  exit "$rc"
}
trap handle_error ERR

backup_current_binary "pre-rollback"
SOURCE_BACKUP="$BACKUP_RESULT"
rollback_allowed=1
install_binary_atomically "$TARGET_BACKUP/seeddata" "$TARGET_SHA"
sudo_systemctl reset-failed "$SERVICE"
sudo_systemctl restart "$SERVICE"
wait_for_active_service
verify_running_binary "$TARGET_SHA"
if [ "$(service_restart_count)" -ne 0 ]; then
  fail "rolled-back binary restarted automatically during initial verification"
fi
sleep 5
wait_for_active_service
verify_running_binary "$TARGET_SHA"
if [ "$(service_restart_count)" -ne 0 ]; then
  fail "rolled-back binary entered a restart loop"
fi
rollback_allowed=0

write_deployment_receipt "manual-rollback" "unknown" "$TARGET_SHA" "$SOURCE_BACKUP"
echo "Seeddata binary rollback completed: target=${TARGET_BACKUP} sha256=${TARGET_SHA}"
echo "Configuration, EnvironmentFile, and $STATE_DIR were not rolled back"
