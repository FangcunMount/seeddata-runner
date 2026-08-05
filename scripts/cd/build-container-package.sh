#!/usr/bin/env bash
set -Eeuo pipefail

: "${DEPLOY_SHA:?DEPLOY_SHA is required}"
: "${BUILD_TIME:?BUILD_TIME is required}"

case "$DEPLOY_SHA" in
  *[!0-9a-f]*|'') echo "DEPLOY_SHA must be a lowercase Git SHA" >&2; exit 2 ;;
esac
[ "${#DEPLOY_SHA}" -eq 40 ] || { echo "DEPLOY_SHA must contain 40 characters" >&2; exit 2; }

IMAGE="seeddata-runner:${DEPLOY_SHA}"
DIST_DIR="${DIST_DIR:-dist}"
ARCHIVE="${DIST_DIR}/seeddata-runner-linux-arm64-image.tar.gz"
METADATA="${DIST_DIR}/container-metadata.env"
CHECKSUMS="${DIST_DIR}/container-SHA256SUMS"

mkdir -p "$DIST_DIR"
docker buildx build \
  --platform linux/arm64 \
  --build-arg "VCS_REF=${DEPLOY_SHA}" \
  --build-arg "BUILD_TIME=${BUILD_TIME}" \
  --tag "$IMAGE" \
  --load \
  .

image_id=$(docker image inspect --format '{{.Id}}' "$IMAGE")
image_os=$(docker image inspect --format '{{.Os}}' "$IMAGE")
image_arch=$(docker image inspect --format '{{.Architecture}}' "$IMAGE")
image_revision=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$IMAGE")

[ "$image_os/$image_arch" = "linux/arm64" ] || { echo "unexpected image platform: ${image_os}/${image_arch}" >&2; exit 1; }
[ "$image_revision" = "$DEPLOY_SHA" ] || { echo "image revision label does not match DEPLOY_SHA" >&2; exit 1; }

docker image save "$IMAGE" | gzip -9 >"$ARCHIVE"
printf 'git_sha=%s\nimage=%s\nimage_id=%s\nplatform=linux/arm64\nbuild_time=%s\n' \
  "$DEPLOY_SHA" "$IMAGE" "$image_id" "$BUILD_TIME" >"$METADATA"

if command -v sha256sum >/dev/null 2>&1; then
  archive_sha=$(sha256sum "$ARCHIVE" | awk '{print $1}')
else
  archive_sha=$(shasum -a 256 "$ARCHIVE" | awk '{print $1}')
fi
printf '%s  %s\n' "$archive_sha" "$(basename "$ARCHIVE")" >"$CHECKSUMS"

echo "Container package built: image=${IMAGE} id=${image_id} archive=${ARCHIVE}"
