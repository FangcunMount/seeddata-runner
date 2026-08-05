#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/macmini-common.sh"

: "${RUNNER_SSH_ALIAS:?RUNNER_SSH_ALIAS is required}"
: "${RUNNER_SSH_CONFIG:?RUNNER_SSH_CONFIG is required}"

REMOTE_APP_ROOT="/root/workspace/golang/src/github.com/fangcun-mount/seeddata-runner"
REMOTE_STATE="${REMOTE_APP_ROOT}/.seeddata-cache"
REMOTE_CONFIG="${REMOTE_APP_ROOT}/configs/seeddata.yaml"
REMOTE_ENV="/etc/default/seeddata-runner"
REMOTE_SERVICE="seeddata-runner.service"
run_token="${GITHUB_RUN_ID:-local}-$$"
case "$run_token" in *[!0-9A-Za-z._-]*) mac_fail "unsafe run token" ;; esac
REMOTE_STAGE="/tmp/seeddata-return-servera-${run_token}"
server_active=0
mac_removed=0
current_image=""
current_image_id=""

remote() {
  ssh -F "$RUNNER_SSH_CONFIG" "$RUNNER_SSH_ALIAS" "$@"
}

cleanup_return() {
  remote "rm -rf -- '$REMOTE_STAGE'" >/dev/null 2>&1 || true
  release_mac_lock
}

handle_return_error() {
  local rc=$?
  trap - ERR
  if remote "sudo -n /usr/bin/systemctl is-active --quiet '$REMOTE_SERVICE'" >/dev/null 2>&1; then
    server_active=1
  fi
  if [ "$server_active" -eq 0 ] && [ "$mac_removed" -eq 1 ]; then
    echo "ServerA did not start; restoring the prior Mac container" >&2
    if compose_seeddata "$current_image" up --detach --force-recreate --no-build; then
      wait_for_healthy_container "$current_image_id" || true
    fi
  elif [ "$server_active" -eq 1 ]; then
    echo "ServerA is active; Mac container remains absent to protect the single-instance contract" >&2
  fi
  trap - EXIT
  cleanup_return
  exit "$rc"
}

mac_init
acquire_mac_lock
trap cleanup_return EXIT
trap handle_return_error ERR
validate_runtime_files
[ -f "$RECEIPT_FILE" ] || mac_fail "deployment receipt is missing: $RECEIPT_FILE"
container_exists || mac_fail "seeddata container is missing on this Mac"

current_image=$(docker container inspect --format '{{.Config.Image}}' "$CONTAINER_NAME")
current_image_id=$(container_image_id)
[ "$(docker container inspect --format '{{.State.Status}}' "$CONTAINER_NAME")" = "running" ] || mac_fail "Mac container is not running"
wait_for_healthy_container "$current_image_id"

remote_hostname=$(remote "hostname -s")
[ "$(printf '%s' "$remote_hostname" | tr '[:upper:]' '[:lower:]')" = "servera" ] || mac_fail "SSH target is not serverA"
remote_uid=$(remote "id -u")
remote_gid=$(remote "id -g")
case "$remote_uid:$remote_gid" in *[!0-9:]*) mac_fail "remote UID or GID is invalid" ;; esac

remote "
  set -eu
  sudo -n /usr/bin/true
  sudo -n /usr/bin/test -f '$REMOTE_CONFIG'
  sudo -n /usr/bin/test -f '$REMOTE_ENV'
  sudo -n /usr/bin/test -d '$REMOTE_STATE'
  if sudo -n /usr/bin/systemctl is-active --quiet '$REMOTE_SERVICE'; then
    echo 'ServerA service must be inactive before state transfer' >&2
    exit 1
  fi
  umask 077
  mkdir -p '$REMOTE_STAGE/state'
"

compose_seeddata "$current_image" down --remove-orphans
mac_removed=1
backup_container_state "before-return-servera"

scp -F "$RUNNER_SSH_CONFIG" -r "$STATE_DIR/." \
  "${RUNNER_SSH_ALIAS}:${REMOTE_STAGE}/state"

remote "
  set -eu
  sudo -n /usr/bin/rsync --archive --checksum --delete '$REMOTE_STAGE/state/' '$REMOTE_STATE/'
  sudo -n /usr/bin/systemctl reset-failed '$REMOTE_SERVICE'
  sudo -n /usr/bin/systemctl enable '$REMOTE_SERVICE'
  sudo -n /usr/bin/systemctl start '$REMOTE_SERVICE'
  sudo -n /usr/bin/systemctl is-active --quiet '$REMOTE_SERVICE'
"
server_active=1
write_container_receipt "return-servera" "$current_image" "$current_image_id" "" "" "$STATE_BACKUP_RESULT"
rm -f "$CANDIDATE_MARKER"
echo "Production returned to ServerA with the latest Mac state; Mac container was removed"
echo "Pre-return state copy: ${STATE_BACKUP_RESULT}"
