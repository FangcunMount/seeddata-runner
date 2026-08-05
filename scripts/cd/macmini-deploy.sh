#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/macmini-common.sh"

ARCHIVE="${1:-}"
METADATA="${2:-}"
CHECKSUMS="${3:-}"
COMPOSE_SOURCE="${4:-}"
[ -n "$ARCHIVE" ] && [ -n "$METADATA" ] && [ -n "$CHECKSUMS" ] && [ -n "$COMPOSE_SOURCE" ] || {
  echo "usage: macmini-deploy.sh <image.tar.gz> <metadata.env> <checksums> <compose.yaml>" >&2
  exit 2
}

previous_image=""
previous_image_id=""
deployment_started=0
automatic_rollback=0

restore_previous_container() {
  [ -n "$previous_image" ] && [ -n "$previous_image_id" ] || return 1
  docker image inspect "$previous_image_id" >/dev/null 2>&1 || return 1
  echo "Candidate did not reach its first running verification; restoring ${previous_image}" >&2
  compose_seeddata "$TARGET_IMAGE" down --remove-orphans >/dev/null 2>&1 || true
  compose_seeddata "$previous_image" up --detach --force-recreate --no-build
  wait_for_healthy_container "$previous_image_id"
  write_container_receipt "automatic-rollback" "$previous_image" "$previous_image_id" "$TARGET_IMAGE" "$TARGET_IMAGE_ID" "$STATE_BACKUP_RESULT"
  rm -f "$CANDIDATE_MARKER"
}

handle_deploy_error() {
  local rc=$?
  trap - ERR
  if [ "$deployment_started" -eq 1 ] && [ "$automatic_rollback" -eq 1 ]; then
    if ! restore_previous_container; then
      echo "No verified previous Mac container could be restored; candidate marker was retained" >&2
    fi
  elif [ "$deployment_started" -eq 1 ]; then
    echo "Candidate had started; container and state were retained for operator review" >&2
  fi
  release_mac_lock
  exit "$rc"
}

mac_init
acquire_mac_lock
trap release_mac_lock EXIT
trap handle_deploy_error ERR

verify_and_load_container_package "$ARCHIVE" "$METADATA" "$CHECKSUMS"
install_compose_contract "$COMPOSE_SOURCE"
validate_runtime_files
candidate_runtime_preflight "$TARGET_IMAGE"

if container_exists; then
  previous_image=$(docker container inspect --format '{{.Config.Image}}' "$CONTAINER_NAME")
  previous_image_id=$(container_image_id)
  compose_seeddata "$previous_image" stop
fi
backup_container_state "before-${TARGET_GIT_SHA}"

printf 'git_sha=%s\nimage=%s\nstarted_at=%s\n' "$TARGET_GIT_SHA" "$TARGET_IMAGE" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$CANDIDATE_MARKER"
chmod 0600 "$CANDIDATE_MARKER"
deployment_started=1
automatic_rollback=1

compose_seeddata "$TARGET_IMAGE" up --detach --force-recreate --no-build --remove-orphans
for attempt in 1 2 3 4 5 6; do
  status=$(docker container inspect --format '{{.State.Status}}' "$CONTAINER_NAME" 2>/dev/null || true)
  if [ "$status" = "running" ]; then
    [ "$(container_image_id)" = "$TARGET_IMAGE_ID" ] || mac_fail "candidate started with the wrong image ID"
    [ "$(container_restart_count)" = "0" ] || mac_fail "candidate restarted before its first verification"
    break
  fi
  sleep 1
done
[ "${status:-}" = "running" ] || mac_fail "candidate did not reach running state"

# A live supervisor may begin durable work immediately. Keep its state intact
# after this first identity check instead of switching images automatically.
automatic_rollback=0

wait_for_healthy_container "$TARGET_IMAGE_ID"
sleep 5
wait_for_healthy_container "$TARGET_IMAGE_ID"
if container_logs_contain_failure; then
  mac_fail "candidate logs contain an immediate runtime or authentication failure"
fi

write_container_receipt "deploy" "$TARGET_IMAGE" "$TARGET_IMAGE_ID" "$previous_image" "$previous_image_id" "$STATE_BACKUP_RESULT"
rm -f "$CANDIDATE_MARKER"
echo "Mac container deployment completed: git_sha=${TARGET_GIT_SHA} image_id=${TARGET_IMAGE_ID}"
echo "Pre-deploy state copy: ${STATE_BACKUP_RESULT}"
