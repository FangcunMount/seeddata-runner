#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
image=${SEEDDATA_HISTORICAL_IMAGE:-seeddata-runner:historical}

docker build \
  --file "$repository_root/Dockerfile.historical" \
  --tag "$image" \
  "$repository_root"

printf 'built historical image: %s\n' "$image"
