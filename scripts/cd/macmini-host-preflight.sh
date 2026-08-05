#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/macmini-common.sh"

ARCHIVE="${1:-}"
METADATA="${2:-}"
CHECKSUMS="${3:-}"
[ -n "$ARCHIVE" ] && [ -n "$METADATA" ] && [ -n "$CHECKSUMS" ] || {
  echo "usage: macmini-host-preflight.sh <image.tar.gz> <metadata.env> <checksums>" >&2
  exit 2
}

mac_init
acquire_mac_lock
trap release_mac_lock EXIT
verify_and_load_container_package "$ARCHIVE" "$METADATA" "$CHECKSUMS"

for url in \
  "${SEEDDATA_QS_HEALTH_URL:-https://qs.fangcunmount.cn/healthz}" \
  "${SEEDDATA_COLLECTION_HEALTH_URL:-https://collect.fangcunmount.cn/readyz}" \
  "${SEEDDATA_IAM_HEALTH_URL:-https://iam.fangcunmount.cn/healthz}"; do
  host=${url#https://}
  host=${host%%/*}
  curl --fail --silent --show-error --connect-timeout 5 --max-time 15 \
    --resolve "${host}:443:${SERVERA_TAILSCALE_IP}" --output /dev/null "$url"
done

docker run --rm --platform linux/arm64 \
  "${TAILSCALE_HOST_ARGS[@]}" \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  --entrypoint /bin/sh "$TARGET_IMAGE" -ec '
    test -x /usr/local/bin/seeddata
    test -x /usr/bin/curl
    test -f /usr/share/zoneinfo/Asia/Shanghai
    for url in \
      https://qs.fangcunmount.cn/healthz \
      https://collect.fangcunmount.cn/readyz \
      https://iam.fangcunmount.cn/healthz
    do
      curl --fail --silent --show-error --connect-timeout 5 --max-time 15 \
        --output /dev/null "$url"
    done
  '

echo "Mac host preflight passed: image=${TARGET_IMAGE} id=${TARGET_IMAGE_ID} servera_tailnet=${SERVERA_TAILSCALE_IP}"
