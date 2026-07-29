#!/bin/sh
set -eu

image=${SEEDDATA_HISTORICAL_IMAGE:-seeddata-runner:historical}
network=${SEEDDATA_HISTORICAL_NETWORK:-infra-network}
config=${SEEDDATA_HISTORICAL_CONFIG:?SEEDDATA_HISTORICAL_CONFIG is required}
state_dir=${SEEDDATA_HISTORICAL_STATE_DIR:?SEEDDATA_HISTORICAL_STATE_DIR is required}
env_file=${SEEDDATA_HISTORICAL_ENV_FILE:?SEEDDATA_HISTORICAL_ENV_FILE is required}
from=${SEEDDATA_HISTORICAL_FROM:-2025-01-01}
to=${SEEDDATA_HISTORICAL_TO:-2026-07-27}
batch_id=${SEEDDATA_HISTORICAL_BATCH_ID:-hist-20250101-20260727-v2}
resume=${SEEDDATA_HISTORICAL_RESUME:-1}

docker network inspect "$network" >/dev/null
test -r "$config"
test -r "$env_file"
test -d "$state_dir"
test -w "$state_dir"

config=$(cd "$(dirname "$config")" && pwd)/$(basename "$config")
state_dir=$(cd "$state_dir" && pwd)
env_file=$(cd "$(dirname "$env_file")" && pwd)/$(basename "$env_file")
legacy_submission_file=
batch_hash=$(printf '%s' "$batch_id" | sha256sum | awk '{print substr($1, 1, 16)}')
if [ "$resume" = "1" ] && \
  [ ! -f "$state_dir/$batch_hash/historical-state-v2.db" ] && \
  [ -n "${SEEDDATA_HISTORICAL_LEGACY_SUBMISSION_FILE:-}" ]; then
  legacy_submission_file=$SEEDDATA_HISTORICAL_LEGACY_SUBMISSION_FILE
  test -r "$legacy_submission_file"
  legacy_submission_file=$(cd "$(dirname "$legacy_submission_file")" && pwd)/$(basename "$legacy_submission_file")
fi

docker run --rm \
  --network "$network" \
  --read-only \
  --cap-drop=ALL \
  --security-opt no-new-privileges:true \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m \
  --entrypoint /bin/sh \
  --mount "type=bind,src=$config,dst=/run/seeddata/config.yaml,readonly" \
  --mount "type=bind,src=$state_dir,dst=/state" \
  "$image" \
  -ec '
    test -r /run/seeddata/config.yaml
    test -w /state
    preflight_file=/state/.seeddata-container-preflight
    : > "$preflight_file"
    rm -f "$preflight_file"
    getent hosts qs-apiserver >/dev/null
    getent hosts qs-collection-server >/dev/null
    getent hosts iam-apiserver >/dev/null
    wget -q --spider http://qs-apiserver:8080/healthz
    wget -q --spider http://qs-collection-server:8080/readyz
    wget -q --spider http://iam-apiserver:9080/healthz
  '

set -- historical-backfill \
  --config /run/seeddata/config.yaml \
  --state-dir /state \
  --from "$from" \
  --to "$to" \
  --batch-id "$batch_id"
if [ "$resume" = "1" ]; then
  set -- "$@" --resume
fi

if [ -n "$legacy_submission_file" ]; then
  exec docker run --rm \
    --name "seeddata-historical-${batch_id}" \
    --network "$network" \
    --read-only \
    --cap-drop=ALL \
    --security-opt no-new-privileges:true \
    --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m \
    --env-file "$env_file" \
    --env SEEDDATA_API_BASE_URL=http://qs-apiserver:8080 \
    --env SEEDDATA_COLLECTION_BASE_URL=http://qs-collection-server:8080 \
    --env SEEDDATA_IAM_BASE_URL=http://iam-apiserver:9080 \
    --env SEEDDATA_IAM_LOGIN_URL=http://iam-apiserver:9080/api/v2/authn/login \
    --env SEEDDATA_DAILY_SUBMISSION_STATE_FILE=/run/seeddata/legacy-daily-submissions.json \
    --mount "type=bind,src=$config,dst=/run/seeddata/config.yaml,readonly" \
    --mount "type=bind,src=$legacy_submission_file,dst=/run/seeddata/legacy-daily-submissions.json,readonly" \
    --mount "type=bind,src=$state_dir,dst=/state" \
    "$image" "$@"
fi

exec docker run --rm \
  --name "seeddata-historical-${batch_id}" \
  --network "$network" \
  --read-only \
  --cap-drop=ALL \
  --security-opt no-new-privileges:true \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m \
  --env-file "$env_file" \
  --env SEEDDATA_API_BASE_URL=http://qs-apiserver:8080 \
  --env SEEDDATA_COLLECTION_BASE_URL=http://qs-collection-server:8080 \
  --env SEEDDATA_IAM_BASE_URL=http://iam-apiserver:9080 \
  --env SEEDDATA_IAM_LOGIN_URL=http://iam-apiserver:9080/api/v2/authn/login \
  --mount "type=bind,src=$config,dst=/run/seeddata/config.yaml,readonly" \
  --mount "type=bind,src=$state_dir,dst=/state" \
  "$image" "$@"
