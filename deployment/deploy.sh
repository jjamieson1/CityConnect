#!/usr/bin/env bash
#
# Deploy CityConnect: cross-compile the API for linux/amd64, build the SPA for
# the configured base path, ship both, and restart the service.
#
# Usage:
#   deployment/deploy.sh [--host user@server] [--dry-run] [--skip-web] [--skip-api]
#
# Configuration comes from deployment/deploy.env when present, so credentials
# and host names stay out of the repository.

set -euo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

APP_NAME="${APP_NAME:-cityconnect}"
DEPLOY_HOST="${DEPLOY_HOST:-}"
REMOTE_APP_DIR="${REMOTE_APP_DIR:-/opt/${APP_NAME}}"
REMOTE_WEB_DIR="${REMOTE_WEB_DIR:-/var/www/${APP_NAME}}"
REMOTE_PORTAL_DIR="${REMOTE_PORTAL_DIR:-/var/www/${APP_NAME}-portal}"
SERVICE_NAME="${SERVICE_NAME:-${APP_NAME}-api}"
BASE_PATH="${BASE_PATH:-/${APP_NAME}}"
HEALTH_URL="${HEALTH_URL:-}"

# shellcheck source=/dev/null
[[ -f deployment/deploy.env ]] && source deployment/deploy.env

DRY_RUN=false
SKIP_WEB=false
SKIP_API=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host) DEPLOY_HOST="$2"; shift 2 ;;
    --dry-run) DRY_RUN=true; shift ;;
    --skip-web) SKIP_WEB=true; shift ;;
    --skip-api) SKIP_API=true; shift ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m warn\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31merror\033[0m %s\n' "$*" >&2; exit 1; }

run() {
  if $DRY_RUN; then
    printf '  [dry-run] %s\n' "$*"
  else
    "$@"
  fi
}

[[ -n "$DEPLOY_HOST" ]] || die "no deploy host. Pass --host user@server or set DEPLOY_HOST in deployment/deploy.env"

readonly BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT

VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

log "Deploying ${APP_NAME} ${VERSION} (${COMMIT}) to ${DEPLOY_HOST}"

# A dirty tree deploys code that exists nowhere else, which makes the next
# rollback guesswork. Warn loudly rather than refusing — hotfixes happen.
if git status --porcelain 2>/dev/null | grep -q .; then
  warn "working tree has uncommitted changes; this build will not match any commit"
fi

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

if ! $SKIP_API; then
  log "Running tests"
  run go test ./... >/dev/null || die "tests failed; not deploying"

  log "Cross-compiling the API for linux/amd64"
  run env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.builtAt=${BUILT_AT}" \
    -o "${BUILD_DIR}/${APP_NAME}-api" \
    ./cmd/server

  run env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath -ldflags "-s -w" \
    -o "${BUILD_DIR}/ccadm" \
    ./cmd/ccadm
fi

if ! $SKIP_WEB; then
  log "Building the staff console for base path ${BASE_PATH}/"
  run npm ci --silent
  ( cd web && run env CC_BASE_PATH="${BASE_PATH}/" npm run build )

  # The portal is a separate application on its own origin, so it builds at
  # the root of its host rather than under a path.
  log "Building the citizen portal"
  ( cd web-portal && run npm run build )
fi

# ---------------------------------------------------------------------------
# Ship
# ---------------------------------------------------------------------------

if ! $SKIP_API; then
  log "Uploading the API binary"
  run ssh "$DEPLOY_HOST" "sudo mkdir -p ${REMOTE_APP_DIR}/bin ${REMOTE_APP_DIR}/data"

  # Upload beside the live binary, then move into place: a partial transfer
  # must never become the file systemd tries to execute.
  run scp "${BUILD_DIR}/${APP_NAME}-api" "$DEPLOY_HOST:/tmp/${APP_NAME}-api.new"
  run scp "${BUILD_DIR}/ccadm" "$DEPLOY_HOST:/tmp/ccadm.new"

  run ssh "$DEPLOY_HOST" "
    set -e
    sudo cp ${REMOTE_APP_DIR}/bin/${APP_NAME}-api ${REMOTE_APP_DIR}/bin/${APP_NAME}-api.prev 2>/dev/null || true
    sudo mv /tmp/${APP_NAME}-api.new ${REMOTE_APP_DIR}/bin/${APP_NAME}-api
    sudo mv /tmp/ccadm.new ${REMOTE_APP_DIR}/bin/ccadm
    sudo chmod 0755 ${REMOTE_APP_DIR}/bin/${APP_NAME}-api ${REMOTE_APP_DIR}/bin/ccadm
  "
fi

if ! $SKIP_WEB; then
  log "Uploading the staff console"
  run ssh "$DEPLOY_HOST" "sudo mkdir -p ${REMOTE_WEB_DIR} ${REMOTE_PORTAL_DIR}"
  run rsync -az --delete \
    --rsync-path="sudo rsync" \
    web/dist/ "$DEPLOY_HOST:${REMOTE_WEB_DIR}/"

  log "Uploading the citizen portal"
  run rsync -az --delete \
    --rsync-path="sudo rsync" \
    web-portal/dist/ "$DEPLOY_HOST:${REMOTE_PORTAL_DIR}/"
fi

# ---------------------------------------------------------------------------
# Restart and verify
# ---------------------------------------------------------------------------

if ! $SKIP_API; then
  log "Restarting ${SERVICE_NAME}"
  run ssh "$DEPLOY_HOST" "sudo systemctl restart ${SERVICE_NAME}"

  log "Waiting for the service to become healthy"
  if ! $DRY_RUN; then
    healthy=false
    for attempt in $(seq 1 20); do
      sleep 1
      if ssh "$DEPLOY_HOST" "curl -fsS --max-time 3 http://127.0.0.1:4021/healthz" >/dev/null 2>&1; then
        healthy=true
        break
      fi
      printf '  attempt %s/20\n' "$attempt"
    done

    if ! $healthy; then
      warn "the service did not become healthy; rolling back"
      ssh "$DEPLOY_HOST" "
        sudo mv ${REMOTE_APP_DIR}/bin/${APP_NAME}-api.prev ${REMOTE_APP_DIR}/bin/${APP_NAME}-api 2>/dev/null || true
        sudo systemctl restart ${SERVICE_NAME}
      "
      ssh "$DEPLOY_HOST" "sudo journalctl -u ${SERVICE_NAME} -n 40 --no-pager" || true
      die "deployment rolled back"
    fi

    # Readiness is a separate question from liveness: the process can be up
    # while C2 is unreachable, and with SSO as the only staff login that means
    # nobody can sign in. Report it rather than failing the deploy.
    log "Checking readiness"
    if ! ssh "$DEPLOY_HOST" "curl -fsS --max-time 5 http://127.0.0.1:4021/readyz" >/dev/null 2>&1; then
      warn "the service is running but not ready — most often C2 is unreachable."
      warn "run '${REMOTE_APP_DIR}/bin/ccadm check-c2' on the server to see which endpoints it resolved."
    fi
  fi
fi

log "Deployed ${VERSION} to ${DEPLOY_HOST}"

if [[ -n "$HEALTH_URL" ]] && ! $DRY_RUN; then
  log "Public health check: ${HEALTH_URL}"
  curl -fsS --max-time 10 "$HEALTH_URL" >/dev/null \
    && log "reachable through the proxy" \
    || warn "not reachable through the proxy — check the Apache configuration"
fi
