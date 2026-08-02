#!/usr/bin/env bash

# This file is a one-release production migration. Remove it after the retired
# block has been deleted from ServerA and the normal deployment has succeeded.
RETIRED_CONFIG_KEY="historicalBackfill"
CONFIG_RETIREMENT_CHANGED=0
CONFIG_RETIREMENT_BACKUP=""
CONFIG_RETIREMENT_MODE=""
CONFIG_RETIREMENT_UID=""
CONFIG_RETIREMENT_GID=""
CONFIG_RETIREMENT_BACKUP_DIR="/opt/backups/seeddata-runner/config-retirements"

strip_removed_config_block() {
  local source_file="$1" destination_file="$2"
  awk -v retired_key="$RETIRED_CONFIG_KEY" '
    BEGIN { skipping = 0; removed = 0 }
    $0 ~ ("^" retired_key ":[[:space:]]*($|#)") {
      removed++
      skipping = 1
      next
    }
    skipping && $0 ~ /^[^[:space:]#][^:]*:[[:space:]]*/ {
      skipping = 0
    }
    !skipping { print }
    END {
      if (removed != 1) {
        exit 42
      }
    }
  ' "$source_file" >"$destination_file"
}

retire_removed_config_block() {
  local work_dir="$1" metadata source_file migrated_file timestamp config_sha backup_dir
  CONFIG_RETIREMENT_CHANGED=0
  CONFIG_RETIREMENT_BACKUP=""

  if ! sudo_grep -Eq "^${RETIRED_CONFIG_KEY}:[[:space:]]*($|#)" "$CONFIG_FILE"; then
    return 0
  fi

  metadata=$(sudo_stat --format='%a %u %g' "$CONFIG_FILE")
  read -r CONFIG_RETIREMENT_MODE CONFIG_RETIREMENT_UID CONFIG_RETIREMENT_GID <<<"$metadata"
  case "$CONFIG_RETIREMENT_MODE" in
    *[!0-7]*|'') fail "production config mode is invalid"; return 1 ;;
  esac
  if [ "${#CONFIG_RETIREMENT_MODE}" -ne 3 ] && [ "${#CONFIG_RETIREMENT_MODE}" -ne 4 ]; then
    fail "production config mode has an unexpected length"
    return 1
  fi
  case "$CONFIG_RETIREMENT_UID:$CONFIG_RETIREMENT_GID" in
    *[!0-9:]*|:|:*|*:) fail "production config owner is invalid"; return 1 ;;
  esac

  source_file="$work_dir/seeddata-config.original"
  migrated_file="$work_dir/seeddata-config.migrated"
  sudo_grep -E '^' "$CONFIG_FILE" >"$source_file"
  strip_removed_config_block "$source_file" "$migrated_file" || {
    fail "production config did not contain exactly one removable top-level block"
    return 1
  }

  timestamp=$(date -u +%Y%m%dT%H%M%SZ)
  config_sha=$(binary_sha256 "$CONFIG_FILE")
  validate_sha256 "$config_sha" || { fail "production config checksum is invalid"; return 1; }
  backup_dir="$CONFIG_RETIREMENT_BACKUP_DIR"
  CONFIG_RETIREMENT_BACKUP="$backup_dir/${timestamp}-${config_sha%${config_sha#????????????????}}.yaml"
  sudo_install -d -m 0700 "$backup_dir"
  sudo_install -m 0600 "$CONFIG_FILE" "$CONFIG_RETIREMENT_BACKUP"

  CONFIG_RETIREMENT_CHANGED=1
  sudo_install \
    -o "$CONFIG_RETIREMENT_UID" \
    -g "$CONFIG_RETIREMENT_GID" \
    -m "$CONFIG_RETIREMENT_MODE" \
    "$migrated_file" "$CONFIG_FILE"
  if sudo_grep -Eq "^${RETIRED_CONFIG_KEY}:[[:space:]]*($|#)" "$CONFIG_FILE"; then
    fail "retired configuration block is still present after migration"
    return 1
  fi
  echo "Retired configuration block removed; backup: $CONFIG_RETIREMENT_BACKUP"
}

restore_removed_config_block() {
  [ "$CONFIG_RETIREMENT_CHANGED" -eq 1 ] || return 0
  [ -n "$CONFIG_RETIREMENT_BACKUP" ] || return 1
  echo "Candidate preflight failed; restoring production config from $CONFIG_RETIREMENT_BACKUP" >&2
  sudo_install \
    -o "$CONFIG_RETIREMENT_UID" \
    -g "$CONFIG_RETIREMENT_GID" \
    -m "$CONFIG_RETIREMENT_MODE" \
    "$CONFIG_RETIREMENT_BACKUP" "$CONFIG_FILE"
  CONFIG_RETIREMENT_CHANGED=0
}
