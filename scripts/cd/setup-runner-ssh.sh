#!/usr/bin/env bash
set -Eeuo pipefail

: "${RUNNER_SSH_KEY:?RUNNER_SSH_KEY is required}"
: "${RUNNER_SSH_HOST:?RUNNER_SSH_HOST is required}"
: "${RUNNER_SSH_USER:?RUNNER_SSH_USER is required}"
: "${RUNNER_SSH_FINGERPRINT:?RUNNER_SSH_FINGERPRINT is required}"

RUNNER_SSH_PORT="${RUNNER_SSH_PORT:-22}"
RUNNER_SSH_ALIAS="${RUNNER_SSH_ALIAS:-seeddata-production}"
EXPECTED_HOSTNAME="${EXPECTED_HOSTNAME:-serverA}"
SSH_HOME="${RUNNER_TEMP:-/tmp}/seeddata-ssh-${GITHUB_RUN_ID:-$$}"
KEY_FILE="$SSH_HOME/deploy-key"
KNOWN_HOSTS="$SSH_HOME/known_hosts"
CONFIG="$SSH_HOME/config"
setup_complete=0

cleanup_failed_setup() {
  if [ "$setup_complete" -eq 0 ]; then
    case "$SSH_HOME" in
      "${RUNNER_TEMP:-/tmp}"/seeddata-ssh-*) rm -rf -- "$SSH_HOME" ;;
      *) echo "refusing to remove unexpected SSH directory: $SSH_HOME" >&2 ;;
    esac
  fi
}
trap cleanup_failed_setup EXIT

mkdir -p "$SSH_HOME"
chmod 700 "$SSH_HOME"
umask 077
printf '%s\n' "$RUNNER_SSH_KEY" | tr -d '\r' >"$KEY_FILE"
chmod 600 "$KEY_FILE"
ssh-keygen -lf "$KEY_FILE"

SCAN_FILE="$SSH_HOME/keyscan"
if ! ssh-keyscan -T 15 -p "$RUNNER_SSH_PORT" "$RUNNER_SSH_HOST" >"$SCAN_FILE" 2>/dev/null; then
  echo "ssh-keyscan failed for ${RUNNER_SSH_HOST}:${RUNNER_SSH_PORT}" >&2
  exit 1
fi

: >"$KNOWN_HOSTS"
while IFS= read -r host_key; do
  [ -n "$host_key" ] || continue
  case "$host_key" in
    \#*) continue ;;
  esac
  candidate="$SSH_HOME/candidate-host-key"
  printf '%s\n' "$host_key" >"$candidate"
  actual_fingerprint=$(ssh-keygen -lf "$candidate" -E sha256 | awk '{print $2}')
  if [ "$actual_fingerprint" = "$RUNNER_SSH_FINGERPRINT" ]; then
    printf '%s\n' "$host_key" >>"$KNOWN_HOSTS"
  fi
done <"$SCAN_FILE"

if [ ! -s "$KNOWN_HOSTS" ]; then
  echo "no scanned SSH host key matched the configured fingerprint" >&2
  exit 1
fi
chmod 600 "$KNOWN_HOSTS"

cat >"$CONFIG" <<EOF
Host ${RUNNER_SSH_ALIAS}
  HostName ${RUNNER_SSH_HOST}
  User ${RUNNER_SSH_USER}
  Port ${RUNNER_SSH_PORT}
  IdentityFile ${KEY_FILE}
  IdentitiesOnly yes
  BatchMode yes
  StrictHostKeyChecking yes
  UserKnownHostsFile ${KNOWN_HOSTS}
EOF
chmod 600 "$CONFIG"

remote_hostname=$(ssh -F "$CONFIG" -o ConnectTimeout=20 "$RUNNER_SSH_ALIAS" 'hostname -s 2>/dev/null || hostname')
expected_lower=$(printf '%s' "$EXPECTED_HOSTNAME" | tr '[:upper:]' '[:lower:]')
actual_lower=$(printf '%s' "$remote_hostname" | tr '[:upper:]' '[:lower:]')
if [ "$actual_lower" != "$expected_lower" ]; then
  echo "SSH reached ${remote_hostname}, expected ${EXPECTED_HOSTNAME}" >&2
  exit 1
fi

if [ -n "${GITHUB_ENV:-}" ]; then
  {
    echo "RUNNER_SSH_CONFIG=$CONFIG"
    echo "RUNNER_SSH_DIR=$SSH_HOME"
  } >>"$GITHUB_ENV"
fi

setup_complete=1
echo "SSH deployment target verified: ${RUNNER_SSH_USER}@${RUNNER_SSH_HOST}:${RUNNER_SSH_PORT} (${remote_hostname})"
