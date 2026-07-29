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
mock_bin="$tmp_dir/mock-bin"
sudo_log="$tmp_dir/sudo.log"

mkdir -p "$state_dir" "$package_dir/configs" "$package_dir/scripts" "$mock_bin"
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

cat > "$mock_bin/id" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
test "${1:-}" = -u
printf '%s\n' 1000
EOF
cat > "$mock_bin/sudo" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
: "${SEEDDATA_SUDO_TEST_MODE:?SEEDDATA_SUDO_TEST_MODE is required}"
: "${SEEDDATA_SUDO_TEST_LOG:?SEEDDATA_SUDO_TEST_LOG is required}"
case "$SEEDDATA_SUDO_TEST_MODE" in
  password)
    test "${1:-}" = -S
    shift
    test "${1:-}" = -p
    shift
    test "${1+x}" = x && test "$1" = ''
    shift
    test "${1:-}" = --
    shift
    read -r supplied_password
    test "$supplied_password" = "${SEEDDATA_SUDO_TEST_PASSWORD:?}"
    printf 'password:%s\n' "$*" >> "$SEEDDATA_SUDO_TEST_LOG"
    ;;
  nopasswd)
    test "${1:-}" = -n
    shift
    test "${1:-}" = --
    shift
    printf 'nopasswd:%s\n' "$*" >> "$SEEDDATA_SUDO_TEST_LOG"
    ;;
  *)
    echo "unexpected sudo test mode: $SEEDDATA_SUDO_TEST_MODE" >&2
    exit 1
    ;;
esac
EOF
chmod 0755 "$mock_bin/id" "$mock_bin/sudo"

for remote_script in remote-deploy.sh remote-control.sh; do
  function_file="$tmp_dir/${remote_script%.sh}-as-root.sh"
  sed -n '/^as_root() {$/,/^}$/p' \
    "$repo_root/scripts/cd/$remote_script" > "$function_file"
  test -s "$function_file"

  : > "$sudo_log"
  (
    export PATH="$mock_bin:$PATH"
    export SEEDDATA_CD_VALIDATE_ONLY=0
    export SEEDDATA_SUDO_PASSWORD='test sudo password'
    export SEEDDATA_SUDO_TEST_MODE=password
    export SEEDDATA_SUDO_TEST_LOG="$sudo_log"
    export SEEDDATA_SUDO_TEST_PASSWORD="$SEEDDATA_SUDO_PASSWORD"
    # shellcheck disable=SC1090
    source "$function_file"
    as_root /usr/bin/true password-mode
  )
  grep -Fxq 'password:/usr/bin/true password-mode' "$sudo_log"

  : > "$sudo_log"
  (
    export PATH="$mock_bin:$PATH"
    export SEEDDATA_CD_VALIDATE_ONLY=0
    unset SEEDDATA_SUDO_PASSWORD
    export SEEDDATA_SUDO_TEST_MODE=nopasswd
    export SEEDDATA_SUDO_TEST_LOG="$sudo_log"
    # shellcheck disable=SC1090
    source "$function_file"
    as_root /usr/bin/true nopasswd-mode
  )
  grep -Fxq 'nopasswd:/usr/bin/true nopasswd-mode' "$sudo_log"
done

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
  SEEDDATA_STARTUP_STABILITY_SECONDS=15
  SEEDDATA_REMOTE_PACKAGE_DIR="$package_dir"
  SEEDDATA_EXPECTED_HOSTNAME=
)

env "${common_env[@]}" bash "$repo_root/scripts/cd/remote-deploy.sh" |
  grep -Fq 'historical deployment contract valid'

if env "${common_env[@]}" \
  SEEDDATA_STARTUP_STABILITY_SECONDS=invalid \
  bash "$repo_root/scripts/cd/remote-deploy.sh" >/dev/null 2>&1; then
  echo "remote deploy accepted invalid startup stability duration" >&2
  exit 1
fi

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
grep -Fq -- '--- last 100 runner log lines:' "$repo_root/scripts/cd/remote-deploy.sh"

echo 'deployment contracts passed'
