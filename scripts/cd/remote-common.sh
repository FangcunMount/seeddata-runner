#!/usr/bin/env bash

SERVICE="seeddata-runner.service"
APP_ROOT="/root/workspace/golang/src/github.com/fangcun-mount/seeddata-runner"
BINARY="${APP_ROOT}/bin/seeddata"
CONFIG_FILE="${APP_ROOT}/configs/seeddata.yaml"
ENV_FILE="/etc/default/seeddata-runner"
STATE_DIR="${APP_ROOT}/.seeddata-cache"
UNIT_FILE="/etc/systemd/system/seeddata-runner.service"
BACKUP_DIR="/opt/backups/seeddata-runner/deployments"
RECEIPT_FILE="/opt/backups/seeddata-runner/current.env"
LOCK_FILE="/run/lock/seeddata-runner-deploy.lock"
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
  local command_name actual_hostname expected_lower actual_lower
  for command_name in systemctl systemd-run journalctl flock sha256sum install tar gzip awk grep find sort sed; do
    require_command "$command_name"
  done
  if [ "$(id -u)" -ne 0 ]; then
    fail "deployment must run as root because the locked production paths are under /root"
  fi

  actual_hostname=$(hostname -s 2>/dev/null || hostname)
  expected_lower=$(printf '%s' "$EXPECTED_HOSTNAME" | tr '[:upper:]' '[:lower:]')
  actual_lower=$(printf '%s' "$actual_hostname" | tr '[:upper:]' '[:lower:]')
  if [ "$actual_lower" != "$expected_lower" ]; then
    fail "deployment reached ${actual_hostname}, expected ${EXPECTED_HOSTNAME}"
  fi

  mkdir -p "$(dirname "$LOCK_FILE")"
  exec 9>"$LOCK_FILE"
  flock -n 9 || fail "another seeddata deployment is already running"

  validate_runtime_contract
  mkdir -p "$BACKUP_DIR"
  chmod 700 "$(dirname "$BACKUP_DIR")" "$BACKUP_DIR"
}

validate_runtime_contract() {
  local value exec_start
  [ -f "$UNIT_FILE" ] || fail "unit file is missing: $UNIT_FILE"
  [ -x "$BINARY" ] || fail "current production binary is missing: $BINARY"
  [ -f "$CONFIG_FILE" ] || fail "production config is missing: $CONFIG_FILE"
  [ -f "$ENV_FILE" ] || fail "production EnvironmentFile is missing: $ENV_FILE"
  [ -d "$STATE_DIR" ] || fail "production state directory is missing: $STATE_DIR"

  value=$(systemctl show "$SERVICE" --property=FragmentPath --value --no-pager)
  [ "$value" = "$UNIT_FILE" ] || fail "FragmentPath=${value}, expected ${UNIT_FILE}"
  value=$(systemctl show "$SERVICE" --property=WorkingDirectory --value --no-pager)
  [ "$value" = "$APP_ROOT" ] || fail "WorkingDirectory=${value}, expected ${APP_ROOT}"
  value=$(systemctl show "$SERVICE" --property=EnvironmentFiles --value --no-pager)
  [ "$value" = "$ENV_FILE (ignore_errors=no)" ] || fail "EnvironmentFiles contract changed"
  value=$(systemctl show "$SERVICE" --property=User --value --no-pager)
  [ "$value" = "root" ] || fail "service User=${value}, expected root"
  value=$(systemctl show "$SERVICE" --property=Restart --value --no-pager)
  [ "$value" = "always" ] || fail "service Restart=${value}, expected always"
  exec_start=$(systemctl show "$SERVICE" --property=ExecStart --value --no-pager --full)
  printf '%s\n' "$exec_start" | grep -Fq "path=$BINARY ;" || fail "ExecStart binary contract changed"
  printf '%s\n' "$exec_start" | grep -Fq "argv[]=$BINARY --config $CONFIG_FILE ;" || fail "ExecStart config contract changed"
}

run_config_preflight() {
  local candidate="$1" deploy_sha="$2" unit_name
  [ -x "$candidate" ] || fail "preflight candidate is not executable: $candidate"
  validate_git_sha "$deploy_sha" || fail "invalid deploy SHA for preflight"
  unit_name="seeddata-runner-preflight-${deploy_sha%${deploy_sha#????????????}}-$$"
  systemd-run \
    --quiet \
    --wait \
    --pipe \
    --collect \
    --unit "$unit_name" \
    --property "Type=exec" \
    --property "User=root" \
    --property "WorkingDirectory=$APP_ROOT" \
    --property "EnvironmentFile=$ENV_FILE" \
    "$candidate" --config "$CONFIG_FILE" --check-config
}

binary_sha256() {
  sha256sum "$1" | awk '{print $1}'
}

backup_current_binary() {
  local reason="$1" timestamp current_sha backup_path
  BACKUP_RESULT=""
  timestamp=$(date -u +%Y%m%dT%H%M%SZ) || return 1
  current_sha=$(binary_sha256 "$BINARY") || return 1
  validate_sha256 "$current_sha" || { fail "current production binary checksum is invalid"; return 1; }
  backup_path="$BACKUP_DIR/${timestamp}-${current_sha%${current_sha#????????????????}}-${reason}"
  mkdir -m 700 "$backup_path" || return 1
  install -m 0755 "$BINARY" "$backup_path/seeddata" || return 1
  printf '%s\n' "$current_sha" >"$backup_path/binary.sha256" || return 1
  printf 'created_at=%s\nreason=%s\nbinary_sha256=%s\n' \
    "$timestamp" "$reason" "$current_sha" >"$backup_path/metadata.env" || return 1
  BACKUP_RESULT="$backup_path"
  echo "Backed up current binary to $backup_path" >&2
}

install_binary_atomically() {
  local candidate="$1" expected_sha="$2" incoming actual_sha
  validate_sha256 "$expected_sha" || fail "invalid expected binary SHA-256"
  actual_sha=$(binary_sha256 "$candidate")
  [ "$actual_sha" = "$expected_sha" ] || fail "candidate checksum changed before install"
  incoming="${BINARY}.incoming-$$"
  install -m 0755 "$candidate" "$incoming"
  mv -f "$incoming" "$BINARY"
}

wait_for_active_service() {
  local attempt pid
  for attempt in 1 2 3 4 5 6; do
    if systemctl is-active --quiet "$SERVICE"; then
      pid=$(systemctl show "$SERVICE" --property=MainPID --value --no-pager)
      if [ -n "$pid" ] && [ "$pid" != "0" ] && [ -r "/proc/${pid}/exe" ]; then
        return 0
      fi
    fi
    sleep 3
  done
  systemctl status "$SERVICE" --no-pager || true
  fail "service did not become active with a readable MainPID"
}

verify_running_binary() {
  local expected_sha="$1" pid running_sha
  validate_sha256 "$expected_sha" || fail "invalid expected running SHA-256"
  pid=$(systemctl show "$SERVICE" --property=MainPID --value --no-pager)
  [ -n "$pid" ] && [ "$pid" != "0" ] || fail "service MainPID is empty"
  running_sha=$(binary_sha256 "/proc/${pid}/exe")
  [ "$running_sha" = "$expected_sha" ] || fail "running binary SHA-256 ${running_sha} does not match ${expected_sha}"
  echo "Running binary verified: pid=${pid} sha256=${running_sha}"
}

service_restart_count() {
  local value
  value=$(systemctl show "$SERVICE" --property=NRestarts --value --no-pager)
  case "$value" in
    *[!0-9]*|'') fail "service NRestarts is not numeric: $value"; return 1 ;;
  esac
  printf '%s\n' "$value"
}

write_deployment_receipt() {
  local operation="$1" target_sha="$2" target_binary_sha="$3" backup_path="$4" receipt_tmp
  receipt_tmp="${RECEIPT_FILE}.tmp-$$"
  mkdir -p "$(dirname "$RECEIPT_FILE")"
  umask 077
  cat >"$receipt_tmp" <<EOF
operation=${operation}
target_git_sha=${target_sha}
target_binary_sha256=${target_binary_sha}
previous_binary_backup=${backup_path}
completed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
  mv -f "$receipt_tmp" "$RECEIPT_FILE"
}

resolve_rollback_backup() {
  local selector="$1" resolved
  ROLLBACK_RESULT=""
  case "$selector" in
    latest)
      resolved=$(find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -name '20*' -print | sort -r | sed -n '1p') || return 1
      ;;
    *[!A-Za-z0-9._-]*|'')
      fail "rollback backup must be 'latest' or a backup directory basename"
      return 1
      ;;
    *)
      resolved="$BACKUP_DIR/$selector"
      ;;
  esac
  [ -n "$resolved" ] && [ -d "$resolved" ] || { fail "rollback backup was not found: $selector"; return 1; }
  [ -x "$resolved/seeddata" ] || { fail "rollback backup has no executable seeddata binary"; return 1; }
  [ -f "$resolved/binary.sha256" ] || { fail "rollback backup has no checksum"; return 1; }
  ROLLBACK_RESULT="$resolved"
}
