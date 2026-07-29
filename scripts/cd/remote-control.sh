#!/usr/bin/env bash
set -Eeuo pipefail

export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin${PATH:+:$PATH}"

: "${SEEDDATA_CONTROL_OPERATION:?SEEDDATA_CONTROL_OPERATION is required}"
: "${SEEDDATA_BATCH_ID:?SEEDDATA_BATCH_ID is required}"

unit_name="seeddata-historical-backfill.service"
container_name="seeddata-historical-$SEEDDATA_BATCH_ID"
expected_hostname="${SEEDDATA_EXPECTED_HOSTNAME:-serverA}"
deploy_root="${SEEDDATA_DEPLOY_ROOT:-/opt/seeddata-runner-historical}"
log_dir="${SEEDDATA_LOG_DIR:-/secure/path/seeddata-historical-logs}"
log_file="$log_dir/$SEEDDATA_BATCH_ID.log"

case "$SEEDDATA_BATCH_ID" in
  ''|*[!A-Za-z0-9_.-]*)
    echo "invalid batch ID" >&2
    exit 1
    ;;
esac
case "$SEEDDATA_CONTROL_OPERATION" in status|stop) ;; *) echo "operation must be status or stop" >&2; exit 1 ;; esac

if [ "${SEEDDATA_CD_VALIDATE_ONLY:-0}" != 1 ]; then
  actual_hostname="$(hostname -s 2>/dev/null || hostname)"
  actual_hostname_lc="$(printf '%s' "$actual_hostname" | tr '[:upper:]' '[:lower:]')"
  expected_hostname_lc="$(printf '%s' "$expected_hostname" | tr '[:upper:]' '[:lower:]')"
  if [ "$actual_hostname_lc" != "$expected_hostname_lc" ]; then
    echo "control reached $actual_hostname, expected $expected_hostname" >&2
    exit 1
  fi
fi

as_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
    return
  fi
  if [ -n "${SEEDDATA_SUDO_PASSWORD:-}" ]; then
    printf '%s\n' "$SEEDDATA_SUDO_PASSWORD" | sudo -S -p '' -- "$@"
    return
  fi
  sudo -n -- "$@"
}

show_status() {
  as_root systemctl show "$unit_name" \
    --property=LoadState,ActiveState,SubState,Result,ExecMainPID,ExecMainStatus \
    --no-pager || true
  as_root docker ps -a \
    --filter "name=^/${container_name}$" \
    --format 'container={{.Names}} status={{.Status}} image={{.Image}}' || true
  as_root journalctl -u "$unit_name" -n 100 --no-pager || true
  if as_root test -r "$log_file"; then
    echo "--- last 100 runner log lines: $log_file ---"
    as_root tail -n 100 "$log_file"
  fi
  receipt_file="$deploy_root/deployment-$SEEDDATA_BATCH_ID.txt"
  if as_root test -r "$receipt_file"; then
    echo "--- deployment receipt ---"
    as_root grep -E '^(revision|image|batch_id|from|to|resume|state_dir|deployed_at)=' "$receipt_file"
  fi
}

if [ "$SEEDDATA_CONTROL_OPERATION" = status ]; then
  if [ "${SEEDDATA_CD_VALIDATE_ONLY:-0}" = 1 ]; then
    echo "historical status contract valid"
    exit 0
  fi
  show_status
  exit 0
fi

if [ "${SEEDDATA_CONTROL_CONFIRMATION:-}" != STOP_HISTORICAL_BACKFILL ]; then
  echo "stop requires STOP_HISTORICAL_BACKFILL" >&2
  exit 1
fi

if [ "${SEEDDATA_CD_VALIDATE_ONLY:-0}" = 1 ]; then
  echo "historical stop contract valid"
  exit 0
fi

as_root systemctl stop "$unit_name" || true
if as_root docker ps --format '{{.Names}}' | grep -Fxq "$container_name"; then
  as_root docker stop --time 120 "$container_name"
fi
echo "historical backfill stopped without deleting state or business data"
show_status
