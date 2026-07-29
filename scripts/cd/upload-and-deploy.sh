#!/usr/bin/env bash
set -Eeuo pipefail

: "${SEEDDATA_SSH_CONFIG:?SEEDDATA_SSH_CONFIG is required}"
: "${SEEDDATA_SSH_ALIAS:?SEEDDATA_SSH_ALIAS is required}"
: "${SEEDDATA_IMAGE_ARCHIVE:?SEEDDATA_IMAGE_ARCHIVE is required}"
: "${SEEDDATA_PACKAGE_ARCHIVE:?SEEDDATA_PACKAGE_ARCHIVE is required}"
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

for file in "$SEEDDATA_IMAGE_ARCHIVE" "$SEEDDATA_PACKAGE_ARCHIVE"; do
  test -f "$file" || {
    echo "missing deployment artifact: $file" >&2
    exit 1
  }
  gzip -t "$file"
done

ssh_cmd=(ssh -F "$SEEDDATA_SSH_CONFIG")
scp_cmd=(scp -F "$SEEDDATA_SSH_CONFIG")
remote_dir="/tmp/seeddata-historical-deploy-${GITHUB_RUN_ID:-$$}"
local_bootstrap="$(mktemp)"
cleanup() {
  rm -f "$local_bootstrap"
  "${ssh_cmd[@]}" "$SEEDDATA_SSH_ALIAS" "rm -rf '$remote_dir'" >/dev/null 2>&1 || true
}
trap cleanup EXIT

"${ssh_cmd[@]}" "$SEEDDATA_SSH_ALIAS" "mkdir -m 700 '$remote_dir'"

upload_verified() {
  local source="$1" destination="$2" attempt
  for attempt in 1 2 3; do
    if "${scp_cmd[@]}" "$source" "$SEEDDATA_SSH_ALIAS:$destination" &&
       "${ssh_cmd[@]}" "$SEEDDATA_SSH_ALIAS" "gzip -t '$destination'"; then
      return 0
    fi
    if [ "$attempt" -eq 3 ]; then
      echo "failed to upload $source" >&2
      return 1
    fi
    sleep 3
  done
}

remote_image="$remote_dir/image.tar.gz"
remote_package="$remote_dir/package.tar.gz"
upload_verified "$SEEDDATA_IMAGE_ARCHIVE" "$remote_image"
upload_verified "$SEEDDATA_PACKAGE_ARCHIVE" "$remote_package"

emit_export() {
  printf 'export %s=%q\n' "$1" "$2" >> "$local_bootstrap"
}

{
  printf '%s\n' '#!/usr/bin/env bash' 'set -Eeuo pipefail'
  printf '%s\n' 'export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin${PATH:+:$PATH}"'
} > "$local_bootstrap"

emit_export SEEDDATA_IMAGE_ARCHIVE "$remote_image"
emit_export SEEDDATA_PACKAGE_ARCHIVE "$remote_package"
emit_export SEEDDATA_DEPLOY_SHA "$SEEDDATA_DEPLOY_SHA"
emit_export SEEDDATA_IMAGE_REF "$SEEDDATA_IMAGE_REF"
emit_export SEEDDATA_BATCH_ID "$SEEDDATA_BATCH_ID"
emit_export SEEDDATA_FROM "$SEEDDATA_FROM"
emit_export SEEDDATA_TO "$SEEDDATA_TO"
emit_export SEEDDATA_RESUME "${SEEDDATA_RESUME:-1}"
emit_export SEEDDATA_STATE_DIR "$SEEDDATA_STATE_DIR"
emit_export SEEDDATA_SECRET_ENV_FILE "$SEEDDATA_SECRET_ENV_FILE"
emit_export SEEDDATA_LEGACY_SUBMISSION_FILE "${SEEDDATA_LEGACY_SUBMISSION_FILE:-}"
emit_export SEEDDATA_BASELINE_FILE "$SEEDDATA_BASELINE_FILE"
emit_export SEEDDATA_DEPLOY_ROOT "$SEEDDATA_DEPLOY_ROOT"
emit_export SEEDDATA_LOG_DIR "$SEEDDATA_LOG_DIR"
emit_export SEEDDATA_EXPECTED_HOSTNAME "${SEEDDATA_EXPECTED_HOSTNAME:-serverA}"
emit_export SEEDDATA_SUDO_PASSWORD "${SEEDDATA_SUDO_PASSWORD:-}"

cat >> "$local_bootstrap" <<'BOOTSTRAP'

remote_dir=$(dirname "$SEEDDATA_PACKAGE_ARCHIVE")
package_dir="$remote_dir/package"
cleanup() {
  rm -rf "$remote_dir"
}
trap cleanup EXIT
mkdir -m 700 "$package_dir"
tar -xzf "$SEEDDATA_PACKAGE_ARCHIVE" -C "$package_dir"
export SEEDDATA_REMOTE_PACKAGE_DIR="$package_dir"
bash "$package_dir/scripts/cd/remote-deploy.sh"
BOOTSTRAP

chmod 600 "$local_bootstrap"
remote_bootstrap="$remote_dir/bootstrap.sh"
"${scp_cmd[@]}" "$local_bootstrap" "$SEEDDATA_SSH_ALIAS:$remote_bootstrap"
"${ssh_cmd[@]}" "$SEEDDATA_SSH_ALIAS" "chmod 600 '$remote_bootstrap' && bash '$remote_bootstrap'"
