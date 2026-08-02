#!/usr/bin/env bash
set -Eeuo pipefail

: "${DEPLOY_SHA:?DEPLOY_SHA is required}"

case "$DEPLOY_SHA" in
  *[!0-9a-f]*|'')
    echo "DEPLOY_SHA must be a lowercase hexadecimal Git SHA" >&2
    exit 2
    ;;
esac
if [ "${#DEPLOY_SHA}" -ne 40 ]; then
  echo "DEPLOY_SHA must contain exactly 40 characters" >&2
  exit 2
fi

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
DIST_DIR="${DIST_DIR:-$REPO_ROOT/dist}"
PACKAGE_FILE="$DIST_DIR/seeddata-runner-linux-amd64.tar.gz"
STAGE_DIR=$(mktemp -d "${RUNNER_TEMP:-/tmp}/seeddata-package.XXXXXX")
trap 'rm -rf "$STAGE_DIR"' EXIT

mkdir -p "$DIST_DIR" "$STAGE_DIR/bin"
rm -f "$PACKAGE_FILE"

cd "$REPO_ROOT"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -o "$STAGE_DIR/bin/seeddata" ./cmd/seeddata

BUILD_INFO=$(go version -m "$STAGE_DIR/bin/seeddata")
if ! printf '%s\n' "$BUILD_INFO" | grep -Fq "vcs.revision=${DEPLOY_SHA}"; then
  echo "built binary does not contain vcs.revision=${DEPLOY_SHA}" >&2
  printf '%s\n' "$BUILD_INFO" >&2
  exit 1
fi
if ! printf '%s\n' "$BUILD_INFO" | grep -Fq 'vcs.modified=false'; then
  echo "refusing to package a binary built from a modified checkout" >&2
  printf '%s\n' "$BUILD_INFO" >&2
  exit 1
fi

(
  cd "$STAGE_DIR"
  sha256sum bin/seeddata >SHA256SUMS
)
BINARY_SHA=$(awk '$2 == "bin/seeddata" { print $1 }' "$STAGE_DIR/SHA256SUMS")
BUILD_TIME=${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
GO_VERSION=$(go env GOVERSION)
cat >"$STAGE_DIR/build-metadata.env" <<EOF
git_sha=${DEPLOY_SHA}
binary_sha256=${BINARY_SHA}
go_version=${GO_VERSION}
build_time=${BUILD_TIME}
EOF

tar -C "$STAGE_DIR" -czf "$PACKAGE_FILE" \
  bin/seeddata \
  SHA256SUMS \
  build-metadata.env

gzip -t "$PACKAGE_FILE"
echo "Built deployment package: $PACKAGE_FILE"
echo "Git SHA: $DEPLOY_SHA"
echo "Binary SHA-256: $BINARY_SHA"

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "package=$PACKAGE_FILE"
    echo "binary_sha256=$BINARY_SHA"
  } >>"$GITHUB_OUTPUT"
fi
