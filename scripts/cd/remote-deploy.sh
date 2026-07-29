#!/usr/bin/env bash
set -Eeuo pipefail

export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin${PATH:+:$PATH}"

: "${SEEDDATA_IMAGE_ARCHIVE:?SEEDDATA_IMAGE_ARCHIVE is required}"
: "${SEEDDATA_DEPLOY_SHA:?SEEDDATA_DEPLOY_SHA is required}"
: "${SEEDDATA_IMAGE_REF:?SEEDDATA_IMAGE_REF is required}"
: "${SEEDDATA_BATCH_ID:?SEEDDATA_BATCH_ID is required}"
: "${SEEDDATA_FROM:?SEEDDATA_FROM is required}"
: "${SEEDDATA_TO:?SEEDDATA_TO is required}"
: "${SEEDDATA_STATE_DIR:?SEEDDATA_STATE_DIR is required}"
: "${SEEDDATA_SECRET_ENV_FILE:?SEEDDATA_SECRET_ENV_FILE is required}"
: "${SEEDDATA_BASELINE_FILE:?SEEDDATA_BASELINE_FILE is required}"
: "${SEEDDATA_DEPLOY_ROOT:?SEEDDATA_DEPLOY_ROOT is required}"
: "${SEEDDATA_LOG_DIR:?SEEDDATA_LOG_DIR is required}"
: "${SEEDDATA_REMOTE_PACKAGE_DIR:?SEEDDATA_REMOTE_PACKAGE_DIR is required}"

unit_name="seeddata-historical-backfill.service"
systemd_env_file="/etc/seeddata-runner-historical.env"
systemd_unit_file="/etc/systemd/system/$unit_name"
resume="${SEEDDATA_RESUME:-1}"
expected_hostname="${SEEDDATA_EXPECTED_HOSTNAME:-serverA}"

validate_token() {
  local name="$1" value="$2"
  case "$value" in
    ''|*[!A-Za-z0-9_.:-]*)
      echo "$name contains unsupported characters" >&2
      exit 1
      ;;
  esac
}

validate_image_ref() {
  local value="$1"
  case "$value" in
    ''|*[!A-Za-z0-9_./:-]*)
      echo "SEEDDATA_IMAGE_REF contains unsupported characters" >&2
      exit 1
      ;;
  esac
}

validate_absolute_path() {
  local name="$1" value="$2"
  case "$value" in
    /*) ;;
    *)
      echo "$name must be an absolute path" >&2
      exit 1
      ;;
  esac
  case "$value" in
    *[!A-Za-z0-9_./-]*)
      echo "$name contains unsupported characters" >&2
      exit 1
      ;;
  esac
}

validate_token SEEDDATA_DEPLOY_SHA "$SEEDDATA_DEPLOY_SHA"
validate_token SEEDDATA_BATCH_ID "$SEEDDATA_BATCH_ID"
validate_image_ref "$SEEDDATA_IMAGE_REF"
printf '%s\n' "$SEEDDATA_FROM" "$SEEDDATA_TO" |
  grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'
case "$resume" in 0|1) ;; *) echo "SEEDDATA_RESUME must be 0 or 1" >&2; exit 1 ;; esac

for path_name in \
  SEEDDATA_IMAGE_ARCHIVE \
  SEEDDATA_STATE_DIR \
  SEEDDATA_SECRET_ENV_FILE \
  SEEDDATA_BASELINE_FILE \
  SEEDDATA_DEPLOY_ROOT \
  SEEDDATA_LOG_DIR \
  SEEDDATA_REMOTE_PACKAGE_DIR; do
  validate_absolute_path "$path_name" "${!path_name}"
done
if [ -n "${SEEDDATA_LEGACY_SUBMISSION_FILE:-}" ]; then
  validate_absolute_path SEEDDATA_LEGACY_SUBMISSION_FILE "$SEEDDATA_LEGACY_SUBMISSION_FILE"
fi

if [ "$SEEDDATA_BATCH_ID" = hist-20250101-20260727-v1 ]; then
  test "$SEEDDATA_FROM" = 2025-01-01
  test "$SEEDDATA_TO" = 2026-07-27
  test "$resume" = 1
fi

if [ "${SEEDDATA_CD_VALIDATE_ONLY:-0}" != 1 ] && [ -n "$expected_hostname" ]; then
  actual_hostname="$(hostname -s 2>/dev/null || hostname)"
  actual_hostname_lc="$(printf '%s' "$actual_hostname" | tr '[:upper:]' '[:lower:]')"
  expected_hostname_lc="$(printf '%s' "$expected_hostname" | tr '[:upper:]' '[:lower:]')"
  if [ "$actual_hostname_lc" != "$expected_hostname_lc" ]; then
    echo "deploy reached $actual_hostname, expected $expected_hostname" >&2
    exit 1
  fi
fi

as_root() {
  if [ "${SEEDDATA_CD_VALIDATE_ONLY:-0}" = 1 ]; then
    "$@"
    return
  fi
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
    return
  fi
  if sudo -n true 2>/dev/null; then
    sudo "$@"
    return
  fi
  if [ -z "${SEEDDATA_SUDO_PASSWORD:-}" ]; then
    echo "sudo requires SEEDDATA_SUDO_PASSWORD or NOPASSWD" >&2
    return 1
  fi
  printf '%s\n' "$SEEDDATA_SUDO_PASSWORD" | sudo -S -p '' "$@"
}

test -f "$SEEDDATA_IMAGE_ARCHIVE"
gzip -t "$SEEDDATA_IMAGE_ARCHIVE"
test -f "$SEEDDATA_REMOTE_PACKAGE_DIR/REVISION"
package_revision="$(tr -d '\r\n' < "$SEEDDATA_REMOTE_PACKAGE_DIR/REVISION")"
if [ "$package_revision" != "$SEEDDATA_DEPLOY_SHA" ]; then
  echo "deployment package revision mismatch" >&2
  exit 1
fi
test -f "$SEEDDATA_REMOTE_PACKAGE_DIR/configs/seeddata.yaml"
test -x "$SEEDDATA_REMOTE_PACKAGE_DIR/scripts/run_historical_container.sh"
as_root test -d "$SEEDDATA_STATE_DIR"
as_root test -r "$SEEDDATA_SECRET_ENV_FILE"
as_root test -r "$SEEDDATA_BASELINE_FILE"
as_root test -r "${SEEDDATA_BASELINE_FILE}.sha256"

if secret_mode="$(as_root stat -c '%a' "$SEEDDATA_SECRET_ENV_FILE" 2>/dev/null)"; then
  :
else
  secret_mode="$(as_root stat -f '%Lp' "$SEEDDATA_SECRET_ENV_FILE")"
fi
case "$secret_mode" in
  400|600) ;;
  *)
    echo "historical secret env file must have mode 0400 or 0600; got $secret_mode" >&2
    exit 1
    ;;
esac

for required_key in \
  IAM_USERNAME \
  IAM_PASSWORD \
  IAM_MOCK_CONSUMER_SHARED_SECRET \
  QS_HISTORICAL_CONTEXT_SECRET; do
  as_root grep -Eq "^${required_key}=.+" "$SEEDDATA_SECRET_ENV_FILE" || {
    echo "historical secret env file is missing $required_key" >&2
    exit 1
  }
done

as_root sha256sum -c "${SEEDDATA_BASELINE_FILE}.sha256"

batch_hash="$(printf '%s' "$SEEDDATA_BATCH_ID" | sha256sum | awk '{print substr($1, 1, 16)}')"
v2_db="$SEEDDATA_STATE_DIR/$batch_hash/historical-state-v2.db"
if [ "$resume" = 1 ] && ! as_root test -f "$v2_db"; then
  test -n "${SEEDDATA_LEGACY_SUBMISSION_FILE:-}" || {
    echo "legacy submission file is required for first v2 resume" >&2
    exit 1
  }
  as_root test -s "$SEEDDATA_LEGACY_SUBMISSION_FILE"
fi

if [ "${SEEDDATA_CD_VALIDATE_ONLY:-0}" = 1 ]; then
  echo "historical deployment contract valid"
  exit 0
fi

if as_root systemctl is-active --quiet "$unit_name"; then
  echo "$unit_name is already active; use the status workflow" >&2
  exit 1
fi
if as_root systemctl is-active --quiet seeddata-runner.service; then
  echo "ordinary seeddata-runner.service is active; stop it before historical backfill" >&2
  exit 1
fi
if as_root docker ps --format '{{.Names}}' |
  grep -Fxq "seeddata-historical-${SEEDDATA_BATCH_ID}"; then
  echo "historical container is already running" >&2
  exit 1
fi

as_root docker network inspect infra-network >/dev/null

load_output="$(as_root docker load -i "$SEEDDATA_IMAGE_ARCHIVE")"
printf '%s\n' "$load_output"
as_root docker image inspect "$SEEDDATA_IMAGE_REF" >/dev/null

release_dir="$SEEDDATA_DEPLOY_ROOT/releases/$SEEDDATA_DEPLOY_SHA"
as_root mkdir -p "$release_dir/configs" "$release_dir/scripts" "$SEEDDATA_LOG_DIR"
as_root install -m 0644 \
  "$SEEDDATA_REMOTE_PACKAGE_DIR/configs/seeddata.yaml" \
  "$release_dir/configs/seeddata.yaml"
as_root install -m 0755 \
  "$SEEDDATA_REMOTE_PACKAGE_DIR/scripts/run_historical_container.sh" \
  "$release_dir/scripts/run_historical_container.sh"
as_root ln -sfn "$release_dir" "$SEEDDATA_DEPLOY_ROOT/current"

as_root docker run --rm \
  --network infra-network \
  --read-only \
  --cap-drop=ALL \
  --security-opt no-new-privileges:true \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m \
  --entrypoint /bin/sh \
  --mount "type=bind,src=$release_dir/configs/seeddata.yaml,dst=/run/seeddata/config.yaml,readonly" \
  --mount "type=bind,src=$SEEDDATA_STATE_DIR,dst=/state" \
  "$SEEDDATA_IMAGE_REF" \
  -ec '
    test -r /run/seeddata/config.yaml
    test -w /state
    preflight_file=/state/.seeddata-cd-preflight
    : > "$preflight_file"
    rm -f "$preflight_file"
    getent hosts qs-apiserver >/dev/null
    getent hosts qs-collection-server >/dev/null
    getent hosts iam-apiserver >/dev/null
    wget -q --spider http://qs-apiserver:8080/healthz
    wget -q --spider http://qs-collection-server:8080/readyz
    wget -q --spider http://iam-apiserver:9080/healthz
  '

runtime_env="$(mktemp)"
unit_file="$(mktemp)"
trap 'rm -f "$runtime_env" "$unit_file"' EXIT
chmod 600 "$runtime_env" "$unit_file"

cat > "$runtime_env" <<EOF
SEEDDATA_HISTORICAL_IMAGE=$SEEDDATA_IMAGE_REF
SEEDDATA_HISTORICAL_NETWORK=infra-network
SEEDDATA_HISTORICAL_CONFIG=$SEEDDATA_DEPLOY_ROOT/current/configs/seeddata.yaml
SEEDDATA_HISTORICAL_STATE_DIR=$SEEDDATA_STATE_DIR
SEEDDATA_HISTORICAL_ENV_FILE=$SEEDDATA_SECRET_ENV_FILE
SEEDDATA_HISTORICAL_FROM=$SEEDDATA_FROM
SEEDDATA_HISTORICAL_TO=$SEEDDATA_TO
SEEDDATA_HISTORICAL_BATCH_ID=$SEEDDATA_BATCH_ID
SEEDDATA_HISTORICAL_RESUME=$resume
SEEDDATA_HISTORICAL_LEGACY_SUBMISSION_FILE=${SEEDDATA_LEGACY_SUBMISSION_FILE:-}
EOF

log_file="$SEEDDATA_LOG_DIR/$SEEDDATA_BATCH_ID.log"
cat > "$unit_file" <<EOF
[Unit]
Description=Seeddata historical backfill $SEEDDATA_BATCH_ID
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$systemd_env_file
WorkingDirectory=$SEEDDATA_DEPLOY_ROOT/current
ExecStart=$SEEDDATA_DEPLOY_ROOT/current/scripts/run_historical_container.sh
Restart=no
KillMode=control-group
TimeoutStopSec=150
StandardOutput=append:$log_file
StandardError=append:$log_file

[Install]
WantedBy=multi-user.target
EOF

as_root install -m 0600 "$runtime_env" "$systemd_env_file"
as_root install -m 0644 "$unit_file" "$systemd_unit_file"

receipt_file="$SEEDDATA_DEPLOY_ROOT/deployment-$SEEDDATA_BATCH_ID.txt"
receipt="$(mktemp)"
cat > "$receipt" <<EOF
revision=$SEEDDATA_DEPLOY_SHA
image=$SEEDDATA_IMAGE_REF
batch_id=$SEEDDATA_BATCH_ID
from=$SEEDDATA_FROM
to=$SEEDDATA_TO
resume=$resume
state_dir=$SEEDDATA_STATE_DIR
deployed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
as_root install -m 0600 "$receipt" "$receipt_file"
rm -f "$receipt"

as_root systemctl daemon-reload
as_root systemctl reset-failed "$unit_name" >/dev/null 2>&1 || true
as_root systemctl start "$unit_name"

for attempt in $(seq 1 30); do
  if as_root systemctl is-active --quiet "$unit_name"; then
    if as_root docker ps --format '{{.Names}}' |
      grep -Fxq "seeddata-historical-${SEEDDATA_BATCH_ID}"; then
      echo "historical backfill started: unit=$unit_name batch=$SEEDDATA_BATCH_ID image=$SEEDDATA_IMAGE_REF"
      as_root systemctl show "$unit_name" \
        --property=ActiveState,SubState,Result,ExecMainPID --no-pager
      exit 0
    fi
  elif as_root systemctl is-failed --quiet "$unit_name"; then
    as_root journalctl -u "$unit_name" -n 100 --no-pager
    exit 1
  elif [ "$(as_root systemctl show "$unit_name" --property=ActiveState --value)" = inactive ] &&
       [ "$(as_root systemctl show "$unit_name" --property=Result --value)" = success ] &&
       [ "$(as_root systemctl show "$unit_name" --property=ExecMainStatus --value)" = 0 ]; then
    echo "historical backfill completed during deployment startup"
    exit 0
  fi
  sleep 2
done

echo "historical service did not become ready within 60 seconds" >&2
as_root systemctl status "$unit_name" --no-pager || true
as_root journalctl -u "$unit_name" -n 100 --no-pager || true
exit 1
