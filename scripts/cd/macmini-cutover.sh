#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/macmini-common.sh"

ARCHIVE="${1:-}"
METADATA="${2:-}"
CHECKSUMS="${3:-}"
COMPOSE_SOURCE="${4:-}"
[ -n "$ARCHIVE" ] && [ -n "$METADATA" ] && [ -n "$CHECKSUMS" ] && [ -n "$COMPOSE_SOURCE" ] || {
  echo "usage: macmini-cutover.sh <image.tar.gz> <metadata.env> <checksums> <compose.yaml>" >&2
  exit 2
}

: "${RUNNER_SSH_ALIAS:?RUNNER_SSH_ALIAS is required}"
: "${RUNNER_SSH_CONFIG:?RUNNER_SSH_CONFIG is required}"

REMOTE_APP_ROOT="/root/workspace/golang/src/github.com/fangcun-mount/seeddata-runner"
REMOTE_CONFIG="${REMOTE_APP_ROOT}/configs/seeddata.yaml"
REMOTE_ENV="/etc/default/seeddata-runner"
REMOTE_STATE="${REMOTE_APP_ROOT}/.seeddata-cache"
REMOTE_SERVICE="seeddata-runner.service"
run_token="${GITHUB_RUN_ID:-local}-$$"
case "$run_token" in *[!0-9A-Za-z._-]*) mac_fail "unsafe run token" ;; esac
REMOTE_STAGE="/tmp/seeddata-mac-cutover-${run_token}"
LOCAL_STAGE=$(mktemp -d "${TMPDIR:-/tmp}/seeddata-mac-cutover.XXXXXX")
server_stopped=0
cutover_complete=0
remote_stage_may_exist=0

remote() {
  ssh -F "$RUNNER_SSH_CONFIG" "$RUNNER_SSH_ALIAS" "$@"
}

cleanup_cutover() {
  if [ "$remote_stage_may_exist" -eq 1 ]; then
    remote "rm -rf -- '$REMOTE_STAGE'" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$LOCAL_STAGE"
  release_mac_lock
}

handle_cutover_error() {
  local rc=$?
  trap - ERR
  if [ "$server_stopped" -eq 1 ] && [ "$cutover_complete" -eq 0 ]; then
    if [ -f "$CANDIDATE_MARKER" ] || container_exists; then
      echo "A Mac candidate started; ServerA remains stopped to protect the single-instance state contract" >&2
    else
      echo "Mac candidate did not start; restarting ServerA" >&2
      remote "sudo -n /usr/bin/systemctl reset-failed '$REMOTE_SERVICE'; sudo -n /usr/bin/systemctl start '$REMOTE_SERVICE'; sudo -n /usr/bin/systemctl is-active --quiet '$REMOTE_SERVICE'"
    fi
  fi
  trap - EXIT
  cleanup_cutover
  exit "$rc"
}

trap cleanup_cutover EXIT
mac_init
acquire_mac_lock
SEEDDATA_LOCK_ALREADY_HELD=1 SEEDDATA_MAC_ROOT="$MAC_ROOT" \
  "$SCRIPT_DIR/macmini-host-preflight.sh" "$ARCHIVE" "$METADATA" "$CHECKSUMS"
trap handle_cutover_error ERR

container_exists && mac_fail "seeddata container already exists on this Mac"
if [ -f "$RECEIPT_FILE" ]; then
  [ "$(metadata_value operation "$RECEIPT_FILE")" = "return-servera" ] || mac_fail "existing Mac receipt does not allow a new cutover"
else
  [ -z "$(find "$STATE_DIR" -mindepth 1 -print -quit)" ] || mac_fail "Mac state directory must be empty before initial cutover"
fi
install_compose_contract "$COMPOSE_SOURCE"

remote_hostname=$(remote "hostname -s")
[ "$(printf '%s' "$remote_hostname" | tr '[:upper:]' '[:lower:]')" = "servera" ] || mac_fail "SSH target is not serverA"
remote_uid=$(remote "id -u")
remote_gid=$(remote "id -g")
case "$remote_uid:$remote_gid" in *[!0-9:]*) mac_fail "remote UID or GID is invalid" ;; esac

remote_stage_may_exist=1
remote "
  set -eu
  sudo -n /usr/bin/true
  sudo -n /usr/bin/test -f '$REMOTE_CONFIG'
  sudo -n /usr/bin/test -f '$REMOTE_ENV'
  sudo -n /usr/bin/test -d '$REMOTE_STATE'
  sudo -n /usr/bin/systemctl is-active --quiet '$REMOTE_SERVICE'
  umask 077
  mkdir -p '$REMOTE_STAGE/config' '$REMOTE_STAGE/env' '$REMOTE_STAGE/state'
  sudo -n /usr/bin/install -m 0600 -o '$remote_uid' -g '$remote_gid' '$REMOTE_CONFIG' '$REMOTE_STAGE/config/seeddata.yaml'
  sudo -n /usr/bin/install -m 0600 -o '$remote_uid' -g '$remote_gid' '$REMOTE_ENV' '$REMOTE_STAGE/env/seeddata.env'
"

scp -F "$RUNNER_SSH_CONFIG" \
  "${RUNNER_SSH_ALIAS}:${REMOTE_STAGE}/config/seeddata.yaml" "$LOCAL_STAGE/seeddata.yaml"
scp -F "$RUNNER_SSH_CONFIG" \
  "${RUNNER_SSH_ALIAS}:${REMOTE_STAGE}/env/seeddata.env" "$LOCAL_STAGE/seeddata.env"
install -m 0600 "$LOCAL_STAGE/seeddata.yaml" "$CONFIG_FILE"
install -m 0600 "$LOCAL_STAGE/seeddata.env" "$ENV_FILE"

verify_and_load_container_package "$ARCHIVE" "$METADATA" "$CHECKSUMS"
candidate_runtime_preflight "$TARGET_IMAGE"

remote "
  set -eu
  sudo -n /usr/bin/systemctl stop '$REMOTE_SERVICE'
  if sudo -n /usr/bin/systemctl is-active --quiet '$REMOTE_SERVICE'; then
    echo 'ServerA service remained active after stop' >&2
    exit 1
  fi
"
server_stopped=1

remote "
  set -eu
  sudo -n /usr/bin/rsync --archive --checksum --delete --chown='$remote_uid:$remote_gid' '$REMOTE_STATE/' '$REMOTE_STAGE/state/'
"

mkdir -p "$LOCAL_STAGE/state"
scp -F "$RUNNER_SSH_CONFIG" -r \
  "${RUNNER_SSH_ALIAS}:${REMOTE_STAGE}/state/." "$LOCAL_STAGE/state"
[ -d "$LOCAL_STAGE/state" ] || mac_fail "ServerA state copy is missing"
backup_container_state "before-cutover"
rsync --archive --delete "$LOCAL_STAGE/state/" "$STATE_DIR/"

SEEDDATA_LOCK_ALREADY_HELD=1 SEEDDATA_MAC_ROOT="$MAC_ROOT" \
  "$SCRIPT_DIR/macmini-deploy.sh" "$ARCHIVE" "$METADATA" "$CHECKSUMS" "$COMPOSE_SOURCE"

remote "
  set -eu
  sudo -n /usr/bin/systemctl disable '$REMOTE_SERVICE'
  if sudo -n /usr/bin/systemctl is-enabled --quiet '$REMOTE_SERVICE'; then
    echo 'ServerA service remained enabled' >&2
    exit 1
  fi
  if sudo -n /usr/bin/systemctl is-active --quiet '$REMOTE_SERVICE'; then
    echo 'ServerA service unexpectedly active after Mac verification' >&2
    exit 1
  fi
"

cutover_complete=1
echo "Production cutover completed: Mac container is healthy and ServerA service is disabled"
