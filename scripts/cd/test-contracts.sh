#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

state_dir="$tmp_dir/state"
package_dir="$tmp_dir/package"
env_file="$tmp_dir/historical.env"
baseline="$tmp_dir/baseline.json"
image_archive="$tmp_dir/image.tar.gz"
legacy_file="$tmp_dir/legacy.json"

mkdir -p "$state_dir" "$package_dir/configs" "$package_dir/scripts"
install -m 0644 "$repo_root/configs/seeddata.yaml" "$package_dir/configs/seeddata.yaml"
install -m 0755 "$repo_root/scripts/run_historical_container.sh" "$package_dir/scripts/run_historical_container.sh"
printf '%s\n' 0123456789abcdef > "$package_dir/REVISION"
printf '%s\n' '{}' > "$baseline"
sha256sum "$baseline" > "$baseline.sha256"
printf '%s\n' '{}' > "$legacy_file"
printf '%s\n' 'image-placeholder' | gzip > "$image_archive"
cat > "$env_file" <<'EOF'
IAM_USERNAME=system@example.com
IAM_PASSWORD=test-only
IAM_MOCK_CONSUMER_SHARED_SECRET=test-only
QS_HISTORICAL_CONTEXT_SECRET=test-only
EOF
chmod 600 "$env_file"

common_env=(
  SEEDDATA_CD_VALIDATE_ONLY=1
  SEEDDATA_IMAGE_ARCHIVE="$image_archive"
  SEEDDATA_DEPLOY_SHA=0123456789abcdef
  SEEDDATA_IMAGE_REF=ghcr.io/fangcunmount/seeddata-runner-historical:0123456789abcdef
  SEEDDATA_BATCH_ID=hist-20250101-20260727-v1
  SEEDDATA_FROM=2025-01-01
  SEEDDATA_TO=2026-07-27
  SEEDDATA_RESUME=1
  SEEDDATA_STATE_DIR="$state_dir"
  SEEDDATA_SECRET_ENV_FILE="$env_file"
  SEEDDATA_LEGACY_SUBMISSION_FILE="$legacy_file"
  SEEDDATA_BASELINE_FILE="$baseline"
  SEEDDATA_DEPLOY_ROOT="$tmp_dir/deploy"
  SEEDDATA_LOG_DIR="$tmp_dir/logs"
  SEEDDATA_REMOTE_PACKAGE_DIR="$package_dir"
  SEEDDATA_EXPECTED_HOSTNAME=
)

env "${common_env[@]}" bash "$repo_root/scripts/cd/remote-deploy.sh" |
  grep -Fq 'historical deployment contract valid'

if env "${common_env[@]}" \
  SEEDDATA_BATCH_ID='unsafe/batch' \
  bash "$repo_root/scripts/cd/remote-deploy.sh" >/dev/null 2>&1; then
  echo "remote deploy accepted unsafe batch ID" >&2
  exit 1
fi

chmod 644 "$env_file"
if env "${common_env[@]}" \
  bash "$repo_root/scripts/cd/remote-deploy.sh" >/dev/null 2>&1; then
  echo "remote deploy accepted insecure secret file mode" >&2
  exit 1
fi
chmod 600 "$env_file"

env \
  SEEDDATA_CD_VALIDATE_ONLY=1 \
  SEEDDATA_CONTROL_OPERATION=status \
  SEEDDATA_BATCH_ID=hist-20250101-20260727-v1 \
  bash "$repo_root/scripts/cd/remote-control.sh" |
  grep -Fq 'historical status contract valid'

if env \
  SEEDDATA_CD_VALIDATE_ONLY=1 \
  SEEDDATA_CONTROL_OPERATION=stop \
  SEEDDATA_BATCH_ID=hist-20250101-20260727-v1 \
  bash "$repo_root/scripts/cd/remote-control.sh" >/dev/null 2>&1; then
  echo "remote control accepted stop without confirmation" >&2
  exit 1
fi

env \
  SEEDDATA_CD_VALIDATE_ONLY=1 \
  SEEDDATA_CONTROL_OPERATION=stop \
  SEEDDATA_CONTROL_CONFIRMATION=STOP_HISTORICAL_BACKFILL \
  SEEDDATA_BATCH_ID=hist-20250101-20260727-v1 \
  bash "$repo_root/scripts/cd/remote-control.sh" |
  grep -Fq 'historical stop contract valid'

if rg -n '\$\{\{ *secrets\.(IAM_PASSWORD|IAM_MOCK_CONSUMER_SHARED_SECRET|QS_HISTORICAL_CONTEXT_SECRET)' \
  "$repo_root/.github/workflows"; then
  echo "workflow must not transport historical business secrets" >&2
  exit 1
fi

grep -Fq 'workflow_dispatch:' "$repo_root/.github/workflows/historical-deploy.yml"
grep -Fq 'START_HISTORICAL_BACKFILL' "$repo_root/.github/workflows/historical-deploy.yml"
grep -Fq 'STOP_HISTORICAL_BACKFILL' "$repo_root/.github/workflows/historical-control.yml"

echo 'deployment contracts passed'
