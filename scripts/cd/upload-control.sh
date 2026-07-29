#!/usr/bin/env bash
set -Eeuo pipefail

: "${SEEDDATA_SSH_CONFIG:?SEEDDATA_SSH_CONFIG is required}"
: "${SEEDDATA_SSH_ALIAS:?SEEDDATA_SSH_ALIAS is required}"
: "${SEEDDATA_CONTROL_OPERATION:?SEEDDATA_CONTROL_OPERATION is required}"
: "${SEEDDATA_BATCH_ID:?SEEDDATA_BATCH_ID is required}"

ssh_cmd=(ssh -F "$SEEDDATA_SSH_CONFIG")
scp_cmd=(scp -F "$SEEDDATA_SSH_CONFIG")
remote_dir="/tmp/seeddata-historical-control-${GITHUB_RUN_ID:-$$}"
local_bootstrap="$(mktemp)"
cleanup() {
  rm -f "$local_bootstrap"
  "${ssh_cmd[@]}" "$SEEDDATA_SSH_ALIAS" "rm -rf '$remote_dir'" >/dev/null 2>&1 || true
}
trap cleanup EXIT

"${ssh_cmd[@]}" "$SEEDDATA_SSH_ALIAS" "mkdir -m 700 '$remote_dir'"
"${scp_cmd[@]}" scripts/cd/remote-control.sh "$SEEDDATA_SSH_ALIAS:$remote_dir/remote-control.sh"

emit_export() {
  printf 'export %s=%q\n' "$1" "$2" >> "$local_bootstrap"
}

{
  printf '%s\n' '#!/usr/bin/env bash' 'set -Eeuo pipefail'
  printf '%s\n' 'export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin${PATH:+:$PATH}"'
} > "$local_bootstrap"
emit_export SEEDDATA_CONTROL_OPERATION "$SEEDDATA_CONTROL_OPERATION"
emit_export SEEDDATA_CONTROL_CONFIRMATION "${SEEDDATA_CONTROL_CONFIRMATION:-}"
emit_export SEEDDATA_BATCH_ID "$SEEDDATA_BATCH_ID"
emit_export SEEDDATA_DEPLOY_ROOT "${SEEDDATA_DEPLOY_ROOT:-/opt/seeddata-runner-historical}"
emit_export SEEDDATA_LOG_DIR "${SEEDDATA_LOG_DIR:-/secure/path/seeddata-historical-logs}"
emit_export SEEDDATA_EXPECTED_HOSTNAME "${SEEDDATA_EXPECTED_HOSTNAME:-serverA}"
emit_export SEEDDATA_SUDO_PASSWORD "${SEEDDATA_SUDO_PASSWORD:-}"
cat >> "$local_bootstrap" <<BOOTSTRAP
trap 'rm -rf "$remote_dir"' EXIT
bash "$remote_dir/remote-control.sh"
BOOTSTRAP
chmod 600 "$local_bootstrap"
"${scp_cmd[@]}" "$local_bootstrap" "$SEEDDATA_SSH_ALIAS:$remote_dir/bootstrap.sh"
"${ssh_cmd[@]}" "$SEEDDATA_SSH_ALIAS" "chmod 600 '$remote_dir/bootstrap.sh' && bash '$remote_dir/bootstrap.sh'"
