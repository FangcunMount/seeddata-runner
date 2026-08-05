#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/macmini-common.sh"

mac_init
acquire_mac_lock
trap release_mac_lock EXIT
validate_runtime_files
[ -f "$RECEIPT_FILE" ] || mac_fail "deployment receipt is missing: $RECEIPT_FILE"

current_image=$(metadata_value image "$RECEIPT_FILE")
current_image_id=$(metadata_value image_id "$RECEIPT_FILE")
previous_image=$(metadata_value previous_image "$RECEIPT_FILE")
previous_image_id=$(metadata_value previous_image_id "$RECEIPT_FILE")
[ -n "$previous_image" ] && [ -n "$previous_image_id" ] || mac_fail "receipt has no previous Mac image"
docker image inspect "$previous_image_id" >/dev/null 2>&1 || mac_fail "previous image is not present on this Mac"
[ "$(docker image inspect --format '{{.Id}}' "$previous_image")" = "$previous_image_id" ] || mac_fail "previous image tag no longer resolves to its recorded image ID"

candidate_runtime_preflight "$previous_image"
compose_seeddata "$current_image" stop
backup_container_state "before-rollback"

printf 'image=%s\nimage_id=%s\nstarted_at=%s\n' "$previous_image" "$previous_image_id" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$CANDIDATE_MARKER"
chmod 0600 "$CANDIDATE_MARKER"
compose_seeddata "$previous_image" up --detach --force-recreate --no-build --remove-orphans
wait_for_healthy_container "$previous_image_id"
sleep 5
wait_for_healthy_container "$previous_image_id"
if container_logs_contain_failure; then
  mac_fail "rolled-back image logs contain an immediate runtime or authentication failure"
fi

write_container_receipt "rollback" "$previous_image" "$previous_image_id" "$current_image" "$current_image_id" "$STATE_BACKUP_RESULT"
rm -f "$CANDIDATE_MARKER"
echo "Mac container rollback completed: image=${previous_image} image_id=${previous_image_id}"
echo "Pre-rollback state copy: ${STATE_BACKUP_RESULT}"
