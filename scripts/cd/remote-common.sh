#!/usr/bin/env bash

SERVICE="seeddata-runner.service"
PREFLIGHT_SERVICE="seeddata-runner-preflight.service"
APP_ROOT="/root/workspace/golang/src/github.com/fangcun-mount/seeddata-runner"
BINARY="${APP_ROOT}/bin/seeddata"
PREFLIGHT_BINARY="${APP_ROOT}/bin/seeddata.preflight"
CONFIG_FILE="${APP_ROOT}/configs/seeddata.yaml"
ENV_FILE="/etc/default/seeddata-runner"
STATE_DIR="${APP_ROOT}/.seeddata-cache"
UNIT_FILE="/etc/systemd/system/seeddata-runner.service"
PREFLIGHT_UNIT_FILE="/etc/systemd/system/${PREFLIGHT_SERVICE}"
BACKUP_DIR="/opt/backups/seeddata-runner/deployments"
RECEIPT_FILE="/opt/backups/seeddata-runner/current.env"
LOCK_FILE="/tmp/seeddata-runner-deploy.lock"
EXPECTED_HOSTNAME="serverA"
BACKUP_RESULT=""
ROLLBACK_RESULT=""

fail() {
  echo "seeddata CD: $*" >&2
  return 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is missing: $1"
}

sudo_systemctl() {
  sudo -n /usr/bin/systemctl "$@"
}

sudo_journalctl() {
  sudo -n /usr/bin/journalctl "$@"
}

sudo_grep() {
  sudo -n /usr/bin/grep "$@"
}

sudo_install() {
  sudo -n /usr/bin/install "$@"
}

sudo_rsync() {
  sudo -n /usr/bin/rsync "$@"
}

sudo_sha256sum() {
  sudo -n /usr/bin/sha256sum "$@"
}

sudo_test() {
  sudo -n /usr/bin/test "$@"
}

sudo_ls() {
  sudo -n /usr/bin/ls "$@"
}

validate_sha256() {
  case "$1" in
    *[!0-9a-f]*|'') return 1 ;;
  esac
  [ "${#1}" -eq 64 ]
}

validate_git_sha() {
  case "$1" in
    *[!0-9a-f]*|'') return 1 ;;
  esac
  [ "${#1}" -eq 40 ]
}

seeddata_cd_init() {
  local command_name command_path actual_hostname expected_lower actual_lower
  for command_name in sudo flock gzip tar awk grep sort sed; do
    require_command "$command_name"
  done
  sudo -n /usr/bin/true
  for command_path in \
    /usr/bin/install \
    /usr/bin/grep \
    /usr/bin/journalctl \
    /usr/bin/ls \
    /usr/bin/rsync \
    /usr/bin/sha256sum \
    /usr/bin/systemctl \
    /usr/bin/test; do
    sudo_test -x "$command_path" || fail "required privileged command is missing: $command_path"
  done

  actual_hostname=$(hostname -s 2>/dev/null || hostname)
  expected_lower=$(printf '%s' "$EXPECTED_HOSTNAME" | tr '[:upper:]' '[:lower:]')
  actual_lower=$(printf '%s' "$actual_hostname" | tr '[:upper:]' '[:lower:]')
  if [ "$actual_lower" != "$expected_lower" ]; then
    fail "deployment reached ${actual_hostname}, expected ${EXPECTED_HOSTNAME}"
  fi

  umask 077
  exec 9>"$LOCK_FILE"
  flock -n 9 || fail "another seeddata deployment is already running"

  validate_runtime_contract
  sudo_install -d -m 0700 "$(dirname "$BACKUP_DIR")" "$BACKUP_DIR"
}

validate_runtime_contract() {
  local value exec_start
  sudo_test -f "$UNIT_FILE" || fail "unit file is missing: $UNIT_FILE"
  sudo_test -x "$BINARY" || fail "current production binary is missing: $BINARY"
  sudo_test -f "$CONFIG_FILE" || fail "production config is missing: $CONFIG_FILE"
  sudo_test -f "$ENV_FILE" || fail "production EnvironmentFile is missing: $ENV_FILE"
  sudo_test -d "$STATE_DIR" || fail "production state directory is missing: $STATE_DIR"

  value=$(sudo_systemctl show "$SERVICE" --property=FragmentPath --value --no-pager)
  [ "$value" = "$UNIT_FILE" ] || fail "FragmentPath=${value}, expected ${UNIT_FILE}"
  value=$(sudo_systemctl show "$SERVICE" --property=WorkingDirectory --value --no-pager)
  [ "$value" = "$APP_ROOT" ] || fail "WorkingDirectory=${value}, expected ${APP_ROOT}"
  value=$(sudo_systemctl show "$SERVICE" --property=EnvironmentFiles --value --no-pager)
  [ "$value" = "$ENV_FILE (ignore_errors=no)" ] || fail "EnvironmentFiles contract changed"
  value=$(sudo_systemctl show "$SERVICE" --property=User --value --no-pager)
  [ "$value" = "root" ] || fail "service User=${value}, expected root"
  value=$(sudo_systemctl show "$SERVICE" --property=Restart --value --no-pager)
  [ "$value" = "always" ] || fail "service Restart=${value}, expected always"
  exec_start=$(sudo_systemctl show "$SERVICE" --property=ExecStart --value --no-pager --full)
  printf '%s\n' "$exec_start" | grep -Fq "path=$BINARY ;" || fail "ExecStart binary contract changed"
  printf '%s\n' "$exec_start" | grep -Fq "argv[]=$BINARY --config $CONFIG_FILE ;" || fail "ExecStart config contract changed"
}

run_config_preflight() {
  local candidate="$1" unit_source="$2" result exit_status
  [ -x "$candidate" ] || fail "preflight candidate is not executable: $candidate"
  [ -f "$unit_source" ] || fail "preflight unit source is missing: $unit_source"

  sudo_install -m 0755 "$candidate" "$PREFLIGHT_BINARY"
  sudo_install -m 0644 "$unit_source" "$PREFLIGHT_UNIT_FILE"
  sudo_systemctl daemon-reload
  sudo_systemctl reset-failed "$PREFLIGHT_SERVICE" >/dev/null 2>&1 || true
  if ! sudo_systemctl start "$PREFLIGHT_SERVICE"; then
    sudo_systemctl status "$PREFLIGHT_SERVICE" --no-pager || true
    sudo_journalctl -u "$PREFLIGHT_SERVICE" -n 100 --no-pager || true
    fail "candidate configuration preflight failed"
    return 1
  fi

  result=$(sudo_systemctl show "$PREFLIGHT_SERVICE" --property=Result --value --no-pager)
  exit_status=$(sudo_systemctl show "$PREFLIGHT_SERVICE" --property=ExecMainStatus --value --no-pager)
  [ "$result" = "success" ] && [ "$exit_status" = "0" ] || {
    fail "candidate configuration preflight result=${result} exit_status=${exit_status}"
    return 1
  }
}

binary_sha256() {
  sudo_sha256sum "$1" | awk '{print $1}'
}

stored_binary_sha256() {
  local checksum_file="$1" stored_sha
  stored_sha=$(sudo_grep -E '^[0-9a-f]{64}$' "$checksum_file") || return 1
  validate_sha256 "$stored_sha" || return 1
  printf '%s\n' "$stored_sha"
}

backup_current_binary() {
  local reason="$1" timestamp current_sha backup_path checksum_tmp metadata_tmp
  BACKUP_RESULT=""
  timestamp=$(date -u +%Y%m%dT%H%M%SZ) || return 1
  current_sha=$(binary_sha256 "$BINARY") || return 1
  validate_sha256 "$current_sha" || { fail "current production binary checksum is invalid"; return 1; }
  backup_path="$BACKUP_DIR/${timestamp}-${current_sha%${current_sha#????????????????}}-${reason}"
  checksum_tmp=$(mktemp "/tmp/seeddata-backup-sha.XXXXXX") || return 1
  metadata_tmp=$(mktemp "/tmp/seeddata-backup-metadata.XXXXXX") || {
    rm -f "$checksum_tmp"
    return 1
  }
  printf '%s\n' "$current_sha" >"$checksum_tmp"
  printf 'created_at=%s\nreason=%s\nbinary_sha256=%s\n' \
    "$timestamp" "$reason" "$current_sha" >"$metadata_tmp"
  sudo_install -d -m 0700 "$backup_path"
  sudo_install -m 0755 "$BINARY" "$backup_path/seeddata"
  sudo_install -m 0600 "$checksum_tmp" "$backup_path/binary.sha256"
  sudo_install -m 0600 "$metadata_tmp" "$backup_path/metadata.env"
  rm -f "$checksum_tmp" "$metadata_tmp"
  BACKUP_RESULT="$backup_path"
  echo "Backed up current binary to $backup_path" >&2
}

install_binary_atomically() {
  local candidate="$1" expected_sha="$2" incoming actual_sha installed_sha
  validate_sha256 "$expected_sha" || fail "invalid expected binary SHA-256"
  actual_sha=$(binary_sha256 "$candidate")
  [ "$actual_sha" = "$expected_sha" ] || fail "candidate checksum changed before install"

  # install creates a root-owned staging file. Local rsync writes through a
  # temporary sibling and renames it over the live binary at completion.
  incoming="${BINARY}.incoming"
  sudo_install -m 0755 "$candidate" "$incoming"
  sudo_rsync --archive --checksum "$incoming" "$BINARY"
  installed_sha=$(binary_sha256 "$BINARY")
  [ "$installed_sha" = "$expected_sha" ] || fail "installed binary checksum does not match candidate"
}

wait_for_active_service() {
  local attempt pid
  for attempt in 1 2 3 4 5 6; do
    if sudo_systemctl is-active --quiet "$SERVICE"; then
      pid=$(sudo_systemctl show "$SERVICE" --property=MainPID --value --no-pager)
      if [ -n "$pid" ] && [ "$pid" != "0" ] && sudo_test -r "/proc/${pid}/exe"; then
        return 0
      fi
    fi
    sleep 3
  done
  sudo_systemctl status "$SERVICE" --no-pager || true
  fail "service did not become active with a readable MainPID"
}

verify_running_binary() {
  local expected_sha="$1" pid running_sha
  validate_sha256 "$expected_sha" || fail "invalid expected running SHA-256"
  pid=$(sudo_systemctl show "$SERVICE" --property=MainPID --value --no-pager)
  [ -n "$pid" ] && [ "$pid" != "0" ] || fail "service MainPID is empty"
  running_sha=$(binary_sha256 "/proc/${pid}/exe")
  [ "$running_sha" = "$expected_sha" ] || fail "running binary SHA-256 ${running_sha} does not match ${expected_sha}"
  echo "Running binary verified: pid=${pid} sha256=${running_sha}"
}

service_restart_count() {
  local value
  value=$(sudo_systemctl show "$SERVICE" --property=NRestarts --value --no-pager)
  case "$value" in
    *[!0-9]*|'') fail "service NRestarts is not numeric: $value"; return 1 ;;
  esac
  printf '%s\n' "$value"
}

write_deployment_receipt() {
  local operation="$1" target_sha="$2" target_binary_sha="$3" backup_path="$4" receipt_tmp
  receipt_tmp=$(mktemp "/tmp/seeddata-deployment-receipt.XXXXXX")
  cat >"$receipt_tmp" <<EOF
operation=${operation}
target_git_sha=${target_sha}
target_binary_sha256=${target_binary_sha}
previous_binary_backup=${backup_path}
completed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
  sudo_install -d -m 0700 "$(dirname "$RECEIPT_FILE")"
  sudo_install -m 0600 "$receipt_tmp" "$RECEIPT_FILE"
  rm -f "$receipt_tmp"
}

resolve_rollback_backup() {
  local selector="$1" resolved_name resolved
  ROLLBACK_RESULT=""
  case "$selector" in
    latest)
      resolved_name=$(
        sudo_ls -1 "$BACKUP_DIR" |
          while IFS= read -r entry; do
            case "$entry" in
              20*-deploy|20*-pre-rollback) printf '%s\n' "$entry" ;;
            esac
          done |
          LC_ALL=C sort -r |
          sed -n '1p'
      ) || return 1
      [ -n "$resolved_name" ] || { fail "no rollback backup is available"; return 1; }
      resolved="$BACKUP_DIR/$resolved_name"
      ;;
    *[!A-Za-z0-9._-]*|'')
      fail "rollback backup must be 'latest' or a backup directory basename"
      return 1
      ;;
    *)
      resolved="$BACKUP_DIR/$selector"
      ;;
  esac
  sudo_test -d "$resolved" || { fail "rollback backup was not found: $selector"; return 1; }
  sudo_test -x "$resolved/seeddata" || { fail "rollback backup has no executable seeddata binary"; return 1; }
  sudo_test -f "$resolved/binary.sha256" || { fail "rollback backup has no checksum"; return 1; }
  ROLLBACK_RESULT="$resolved"
}
