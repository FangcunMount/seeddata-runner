#!/usr/bin/env bash

MAC_ROOT="${SEEDDATA_MAC_ROOT:-${HOME}/.local/share/seeddata-runner}"
CONFIG_DIR="${MAC_ROOT}/config"
ENV_DIR="${MAC_ROOT}/env"
STATE_DIR="${MAC_ROOT}/state"
BACKUP_DIR="${MAC_ROOT}/backups"
COMPOSE_FILE="${MAC_ROOT}/compose.yaml"
CONFIG_FILE="${CONFIG_DIR}/seeddata.yaml"
ENV_FILE="${ENV_DIR}/seeddata.env"
RECEIPT_FILE="${MAC_ROOT}/current.env"
CANDIDATE_MARKER="${MAC_ROOT}/.candidate-started"
LOCK_DIR="${MAC_ROOT}/.deploy.lock"
CONTAINER_NAME="seeddata-runner"
TARGET_IMAGE=""
TARGET_IMAGE_ID=""
TARGET_GIT_SHA=""
STATE_BACKUP_RESULT=""
LOCK_HELD=0
SERVERA_TAILSCALE_IP=""
TAILSCALE_HOST_ARGS=()

mac_fail() {
  echo "seeddata Mac deploy: $*" >&2
  return 1
}

mac_require_command() {
  command -v "$1" >/dev/null 2>&1 || mac_fail "required command is missing: $1"
}

validate_git_sha() {
  case "$1" in
    *[!0-9a-f]*|'') return 1 ;;
  esac
  [ "${#1}" -eq 40 ]
}

validate_sha256() {
  case "$1" in
    *[!0-9a-f]*|'') return 1 ;;
  esac
  [ "${#1}" -eq 64 ]
}

validate_tailscale_ipv4() {
  printf '%s\n' "$1" | awk -F. '
    NF != 4 { exit 1 }
    {
      for (i = 1; i <= 4; i++) {
        if ($i !~ /^[0-9][0-9]*$/ || $i < 0 || $i > 255) exit 1
      }
      if ($1 != 100 || $2 < 64 || $2 > 127) exit 1
    }
  '
}

mac_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

metadata_value() {
  local key="$1" file="$2"
  awk -F= -v wanted="$key" '$1 == wanted {sub(/^[^=]*=/, ""); print; found=1} END {if (!found) exit 1}' "$file"
}

mac_init() {
  local expected_prefix
  for command_name in awk basename curl date ditto docker find grep gzip id install mkdir mv rsync sed shasum sleep stat tr wc; do
    mac_require_command "$command_name"
  done
  [ -n "${HOME:-}" ] && [ "$HOME" != "/" ] || mac_fail "HOME is unsafe"
  expected_prefix="${HOME}/"
  case "$MAC_ROOT" in
    "$expected_prefix"*) ;;
    *) mac_fail "SEEDDATA_MAC_ROOT must be a child of HOME" ;;
  esac
  [ "$MAC_ROOT" != "$HOME" ] || mac_fail "SEEDDATA_MAC_ROOT must not equal HOME"
  [ -n "${SEEDDATA_SERVERA_TAILSCALE_IP:-}" ] || mac_fail "SEEDDATA_SERVERA_TAILSCALE_IP is required"
  validate_tailscale_ipv4 "$SEEDDATA_SERVERA_TAILSCALE_IP" || \
    mac_fail "SEEDDATA_SERVERA_TAILSCALE_IP must be an IPv4 address in 100.64.0.0/10"
  SERVERA_TAILSCALE_IP="$SEEDDATA_SERVERA_TAILSCALE_IP"
  TAILSCALE_HOST_ARGS=(
    --add-host "qs.fangcunmount.cn:${SERVERA_TAILSCALE_IP}"
    --add-host "collect.fangcunmount.cn:${SERVERA_TAILSCALE_IP}"
    --add-host "iam.fangcunmount.cn:${SERVERA_TAILSCALE_IP}"
  )

  umask 077
  mkdir -p "$CONFIG_DIR" "$ENV_DIR" "$STATE_DIR" "$BACKUP_DIR"
  docker info >/dev/null 2>&1 || mac_fail "Docker daemon is not available"
  docker compose version >/dev/null 2>&1 || mac_fail "Docker Compose is not available"
}

acquire_mac_lock() {
  if [ "${SEEDDATA_LOCK_ALREADY_HELD:-0}" = "1" ]; then
    [ -d "$LOCK_DIR" ] || mac_fail "shared deployment lock is not present"
    return 0
  fi
  if ! mkdir "$LOCK_DIR" 2>/dev/null; then
    mac_fail "another seeddata deployment is running: $LOCK_DIR"
    return 1
  fi
  LOCK_HELD=1
}

release_mac_lock() {
  if [ "$LOCK_HELD" -eq 1 ]; then
    rmdir "$LOCK_DIR" 2>/dev/null || true
    LOCK_HELD=0
  fi
}

verify_and_load_container_package() {
  local archive="$1" metadata="$2" checksums="$3" expected_sha archive_sha actual_sha loaded
  [ -f "$archive" ] || mac_fail "container archive is missing: $archive"
  [ -f "$metadata" ] || mac_fail "container metadata is missing: $metadata"
  [ -f "$checksums" ] || mac_fail "container checksum file is missing: $checksums"

  expected_sha=$(awk -v name="$(basename "$archive")" '$2 == name {print $1}' "$checksums")
  validate_sha256 "$expected_sha" || mac_fail "container checksum file is invalid"
  [ "$(wc -l <"$checksums" | tr -d '[:space:]')" = "1" ] || mac_fail "container checksum file must contain one entry"
  actual_sha=$(mac_sha256 "$archive")
  [ "$actual_sha" = "$expected_sha" ] || mac_fail "container archive checksum mismatch"

  TARGET_GIT_SHA=$(metadata_value git_sha "$metadata")
  TARGET_IMAGE=$(metadata_value image "$metadata")
  TARGET_IMAGE_ID=$(metadata_value image_id "$metadata")
  validate_git_sha "$TARGET_GIT_SHA" || mac_fail "container metadata Git SHA is invalid"
  [ "$TARGET_IMAGE" = "seeddata-runner:${TARGET_GIT_SHA}" ] || mac_fail "container image tag is not immutable"
  case "$TARGET_IMAGE_ID" in sha256:*) ;; *) mac_fail "container image ID is invalid" ;; esac
  [ "$(metadata_value platform "$metadata")" = "linux/arm64" ] || mac_fail "container platform metadata is invalid"

  gzip -t "$archive"
  loaded=$(gzip -dc "$archive" | docker image load)
  printf '%s\n' "$loaded"
  [ "$(docker image inspect --format '{{.Id}}' "$TARGET_IMAGE")" = "$TARGET_IMAGE_ID" ] || mac_fail "loaded image ID does not match metadata"
  [ "$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$TARGET_IMAGE")" = "linux/arm64" ] || mac_fail "loaded image is not linux/arm64"
  [ "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$TARGET_IMAGE")" = "$TARGET_GIT_SHA" ] || mac_fail "loaded image revision label mismatch"
  archive_sha="$actual_sha"
  echo "Verified container package: git_sha=${TARGET_GIT_SHA} image_id=${TARGET_IMAGE_ID} archive_sha256=${archive_sha}"
}

install_compose_contract() {
  local source_file="$1" temporary
  [ -f "$source_file" ] || mac_fail "Compose source is missing: $source_file"
  temporary="${COMPOSE_FILE}.incoming"
  install -m 0600 "$source_file" "$temporary"
  mv -f "$temporary" "$COMPOSE_FILE"
}

validate_runtime_files() {
  local mode
  [ -f "$COMPOSE_FILE" ] || mac_fail "Compose file is missing: $COMPOSE_FILE"
  [ -f "$CONFIG_FILE" ] || mac_fail "production config is missing: $CONFIG_FILE"
  [ -f "$ENV_FILE" ] || mac_fail "production env file is missing: $ENV_FILE"
  [ -d "$STATE_DIR" ] || mac_fail "production state directory is missing: $STATE_DIR"
  grep -Eq '^[[:space:]]*IAM_MOCK_CONSUMER_SHARED_SECRET=' "$ENV_FILE" || mac_fail "IAM shared secret is absent from the env file"
  if mode=$(stat -f '%Lp' "$ENV_FILE" 2>/dev/null); then
    :
  else
    mode=$(stat -c '%a' "$ENV_FILE")
  fi
  [ "$mode" = "600" ] || mac_fail "env file mode must be 600, got ${mode}"
}

compose_seeddata() {
  SEEDDATA_IMAGE="$1" \
  SEEDDATA_SERVERA_TAILSCALE_IP="$SERVERA_TAILSCALE_IP" \
  SEEDDATA_HOST_UID="$(id -u)" \
  SEEDDATA_HOST_GID="$(id -g)" \
  SEEDDATA_CONFIG_FILE="$CONFIG_FILE" \
  SEEDDATA_ENV_FILE="$ENV_FILE" \
  SEEDDATA_STATE_DIR="$STATE_DIR" \
    docker compose --project-name seeddata-runner --file "$COMPOSE_FILE" "${@:2}"
}

candidate_runtime_preflight() {
  local image="$1" probe_url
  validate_runtime_files
  probe_url="${SEEDDATA_IAM_PREFLIGHT_URL:-https://iam.fangcunmount.cn/api/v2/internal/authn/mock-consumers/ensure}"

  docker run --rm --platform linux/arm64 \
    "${TAILSCALE_HOST_ARGS[@]}" \
    --user "$(id -u):$(id -g)" --read-only --cap-drop ALL \
    --security-opt no-new-privileges --env-file "$ENV_FILE" -e TZ=Asia/Shanghai \
    --volume "${CONFIG_FILE}:/run/seeddata/config.yaml:ro" \
    --volume "${STATE_DIR}:/app/.seeddata-cache" --workdir /app \
    "$image" --config /run/seeddata/config.yaml --check-config

  docker run --rm --platform linux/arm64 \
    "${TAILSCALE_HOST_ARGS[@]}" \
    --user "$(id -u):$(id -g)" --read-only --cap-drop ALL \
    --security-opt no-new-privileges --entrypoint /bin/sh \
    --volume "${STATE_DIR}:/app/.seeddata-cache" --workdir /app \
    "$image" -ec 'probe=.seeddata-cache/.container-write-probe; : >"$probe"; rm -f "$probe"'

  docker run --rm --platform linux/arm64 \
    "${TAILSCALE_HOST_ARGS[@]}" \
    --read-only --cap-drop ALL --security-opt no-new-privileges \
    --env-file "$ENV_FILE" -e "SEEDDATA_IAM_PREFLIGHT_URL=${probe_url}" \
    --entrypoint /bin/sh "$image" -ec '
      [ -n "${IAM_MOCK_CONSUMER_SHARED_SECRET:-}" ]
      code=$(curl --silent --show-error --output /dev/null --write-out "%{http_code}" \
        --connect-timeout 5 --max-time 15 \
        --header "Content-Type: application/json" \
        --header "X-IAM-Seed-Secret: ${IAM_MOCK_CONSUMER_SHARED_SECRET}" \
        --data "{}" "$SEEDDATA_IAM_PREFLIGHT_URL")
      [ "$code" = "400" ] || { echo "IAM shared-secret preflight expected HTTP 400, got $code" >&2; exit 1; }
    '
  echo "Candidate config, state write, and IAM shared-secret preflights passed"
}

container_exists() {
  docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1
}

container_image_id() {
  docker container inspect --format '{{.Image}}' "$CONTAINER_NAME"
}

container_restart_count() {
  docker container inspect --format '{{.RestartCount}}' "$CONTAINER_NAME"
}

wait_for_healthy_container() {
  local expected_id="$1" attempt status health
  for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
    status=$(docker container inspect --format '{{.State.Status}}' "$CONTAINER_NAME" 2>/dev/null || true)
    health=$(docker container inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$CONTAINER_NAME" 2>/dev/null || true)
    if [ "$status" = "running" ] && [ "$health" = "healthy" ]; then
      [ "$(container_image_id)" = "$expected_id" ] || mac_fail "running container image ID mismatch"
      [ "$(container_restart_count)" = "0" ] || mac_fail "container restarted during verification"
      return 0
    fi
    sleep 5
  done
  docker container inspect "$CONTAINER_NAME" 2>/dev/null || true
  docker logs --tail 100 "$CONTAINER_NAME" 2>/dev/null || true
  mac_fail "container did not become healthy"
}

container_logs_contain_failure() {
  docker logs --since 2m "$CONTAINER_NAME" 2>&1 | grep -E \
    'Load seeddata config failed|Initialize seeddata dependencies failed|Daily simulation daemon (run|after-hours catchup) failed|seed mock secret invalid|authentication failed \(401\)'
}

backup_container_state() {
  local reason="$1" timestamp destination
  STATE_BACKUP_RESULT=""
  timestamp=$(date -u +%Y%m%dT%H%M%SZ)
  destination="${BACKUP_DIR}/${timestamp}-${reason}"
  mkdir -p "$destination"
  if [ -n "$(find "$STATE_DIR" -mindepth 1 -print -quit)" ]; then
    ditto "$STATE_DIR" "${destination}/state"
  else
    mkdir -p "${destination}/state"
  fi
  STATE_BACKUP_RESULT="$destination"
  echo "State copied to $destination"
}

write_container_receipt() {
  local operation="$1" image="$2" image_id="$3" previous_image="$4" previous_image_id="$5" state_backup="$6" temporary
  temporary="${RECEIPT_FILE}.incoming"
  printf 'operation=%s\ntarget_git_sha=%s\nimage=%s\nimage_id=%s\nprevious_image=%s\nprevious_image_id=%s\nstate_backup=%s\ncompleted_at=%s\n' \
    "$operation" "${image#seeddata-runner:}" "$image" "$image_id" "$previous_image" "$previous_image_id" "$state_backup" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$temporary"
  chmod 0600 "$temporary"
  mv -f "$temporary" "$RECEIPT_FILE"
}
