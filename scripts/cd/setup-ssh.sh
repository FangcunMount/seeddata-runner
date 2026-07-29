#!/usr/bin/env bash
set -Eeuo pipefail

: "${SEEDDATA_SSH_KEY:?SEEDDATA_SSH_KEY is required}"
: "${SEEDDATA_SSH_HOST:?SEEDDATA_SSH_HOST is required}"
: "${SEEDDATA_SSH_USER:?SEEDDATA_SSH_USER is required}"
: "${SEEDDATA_SSH_FINGERPRINT:?SEEDDATA_SSH_FINGERPRINT is required}"

ssh_port="${SEEDDATA_SSH_PORT:-22}"
ssh_alias="${SEEDDATA_SSH_ALIAS:-seeddata-servera}"
expected_hostname="${SEEDDATA_EXPECTED_HOSTNAME:-serverA}"
ssh_temp_dir="${RUNNER_TEMP:-/tmp}/seeddata-ssh-${GITHUB_RUN_ID:-$$}"
key_file="$ssh_temp_dir/id_deploy"
known_hosts="$ssh_temp_dir/known_hosts"
ssh_config="$ssh_temp_dir/config"

case "$ssh_port" in
  ''|*[!0-9]*)
    echo "SEEDDATA_SSH_PORT must be numeric" >&2
    exit 1
    ;;
esac

mkdir -p "$ssh_temp_dir"
chmod 700 "$ssh_temp_dir"
umask 077
printf '%s\n' "$SEEDDATA_SSH_KEY" | tr -d '\r' > "$key_file"
chmod 600 "$key_file"
ssh-keygen -lf "$key_file"

for attempt in 1 2 3; do
  if ssh-keyscan -T 15 -p "$ssh_port" "$SEEDDATA_SSH_HOST" > "$known_hosts" 2>/dev/null &&
     test -s "$known_hosts"; then
    break
  fi
  if [ "$attempt" -eq 3 ]; then
    echo "failed to scan ServerA SSH host key" >&2
    exit 1
  fi
  sleep 2
done
chmod 600 "$known_hosts"

if ! ssh-keygen -lf "$known_hosts" -E sha256 |
  awk '{print $2}' |
  grep -Fxq "$SEEDDATA_SSH_FINGERPRINT"; then
  echo "ServerA SSH fingerprint mismatch" >&2
  ssh-keygen -lf "$known_hosts" -E sha256 >&2
  exit 1
fi

cat > "$ssh_config" <<EOF
Host $ssh_alias
  HostName $SEEDDATA_SSH_HOST
  User $SEEDDATA_SSH_USER
  Port $ssh_port
  IdentityFile $key_file
  IdentitiesOnly yes
  BatchMode yes
  StrictHostKeyChecking yes
  UserKnownHostsFile $known_hosts
  ConnectTimeout 20
  ServerAliveInterval 30
  ServerAliveCountMax 3
EOF
chmod 600 "$ssh_config"

remote_hostname="$(ssh -F "$ssh_config" "$ssh_alias" 'hostname -s 2>/dev/null || hostname')"
remote_hostname_lc="$(printf '%s' "$remote_hostname" | tr '[:upper:]' '[:lower:]')"
expected_hostname_lc="$(printf '%s' "$expected_hostname" | tr '[:upper:]' '[:lower:]')"
if [ "$remote_hostname_lc" != "$expected_hostname_lc" ]; then
  echo "SSH reached $remote_hostname, expected $expected_hostname" >&2
  exit 1
fi

if [ -n "${GITHUB_ENV:-}" ]; then
  {
    printf 'SEEDDATA_SSH_CONFIG=%s\n' "$ssh_config"
    printf 'SEEDDATA_SSH_ALIAS=%s\n' "$ssh_alias"
    printf 'SEEDDATA_SSH_TEMP_DIR=%s\n' "$ssh_temp_dir"
  } >> "$GITHUB_ENV"
fi

printf 'ServerA SSH verified: host=%s alias=%s\n' "$remote_hostname" "$ssh_alias"
