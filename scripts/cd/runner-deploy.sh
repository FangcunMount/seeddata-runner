#!/usr/bin/env bash
set -Eeuo pipefail

: "${RUNNER_SSH_ALIAS:?RUNNER_SSH_ALIAS is required}"
: "${RUNNER_SSH_CONFIG:?RUNNER_SSH_CONFIG is required}"

OPERATION="${OPERATION:-deploy}"
DEPLOY_PACKAGE="${DEPLOY_PACKAGE:-}"
DEPLOY_SHA="${DEPLOY_SHA:-}"
ROLLBACK_BACKUP="${ROLLBACK_BACKUP:-latest}"
RUN_ID="${GITHUB_RUN_ID:-$$}"

case "$RUN_ID" in
  *[!0-9]*|'') echo "GITHUB_RUN_ID must be numeric" >&2; exit 2 ;;
esac
case "$OPERATION" in
  deploy)
    [ -f "$DEPLOY_PACKAGE" ] || { echo "deployment package is missing: $DEPLOY_PACKAGE" >&2; exit 2; }
    case "$DEPLOY_SHA" in *[!0-9a-f]*|'') echo "invalid DEPLOY_SHA" >&2; exit 2 ;; esac
    [ "${#DEPLOY_SHA}" -eq 40 ] || { echo "DEPLOY_SHA must contain 40 characters" >&2; exit 2; }
    ;;
  rollback)
    case "$ROLLBACK_BACKUP" in
      latest) ;;
      ''|*[!A-Za-z0-9._-]*) echo "invalid rollback backup selector" >&2; exit 2 ;;
    esac
    ;;
  *) echo "OPERATION must be deploy or rollback" >&2; exit 2 ;;
esac

SSH=(ssh -F "$RUNNER_SSH_CONFIG")
SCP=(scp -F "$RUNNER_SSH_CONFIG")
REMOTE_DIR="/tmp/seeddata-cd-${RUN_ID}"

cleanup_remote() {
  "${SSH[@]}" "$RUNNER_SSH_ALIAS" "rm -rf '$REMOTE_DIR'" >/dev/null 2>&1 || true
}
trap cleanup_remote EXIT

# The deploy user has a constrained NOPASSWD policy. Keep the orchestration
# unprivileged and let remote scripts elevate only fixed, allowlisted commands.
"${SSH[@]}" "$RUNNER_SSH_ALIAS" "sudo -n /usr/bin/true"
"${SSH[@]}" "$RUNNER_SSH_ALIAS" "rm -rf '$REMOTE_DIR' && umask 077 && mkdir -p '$REMOTE_DIR'"
"${SCP[@]}" \
  scripts/cd/remote-common.sh \
  scripts/cd/remote-deploy.sh \
  scripts/cd/remote-rollback.sh \
  scripts/cd/retire-removed-config.sh \
  scripts/cd/seeddata-runner-preflight.service \
  "${RUNNER_SSH_ALIAS}:${REMOTE_DIR}/"
"${SSH[@]}" "$RUNNER_SSH_ALIAS" "chmod 700 '$REMOTE_DIR/remote-deploy.sh' '$REMOTE_DIR/remote-rollback.sh' && chmod 600 '$REMOTE_DIR/remote-common.sh' '$REMOTE_DIR/retire-removed-config.sh' '$REMOTE_DIR/seeddata-runner-preflight.service'"

if [ "$OPERATION" = "deploy" ]; then
  gzip -t "$DEPLOY_PACKAGE"
  REMOTE_PACKAGE="$REMOTE_DIR/seeddata-runner-linux-amd64.tar.gz"
  "${SCP[@]}" "$DEPLOY_PACKAGE" "${RUNNER_SSH_ALIAS}:${REMOTE_PACKAGE}"
  "${SSH[@]}" "$RUNNER_SSH_ALIAS" "gzip -t '$REMOTE_PACKAGE'"
  "${SSH[@]}" "$RUNNER_SSH_ALIAS" \
    "'$REMOTE_DIR/remote-deploy.sh' --package '$REMOTE_PACKAGE' --sha '$DEPLOY_SHA'"
else
  "${SSH[@]}" "$RUNNER_SSH_ALIAS" \
    "'$REMOTE_DIR/remote-rollback.sh' --backup '$ROLLBACK_BACKUP'"
fi
