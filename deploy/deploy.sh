#!/usr/bin/env bash
#
# deploy.sh — build and ship CityConnect to the muni-demo QA server.
#
# Cross-compiles the API and ccadm, builds both React SPAs, pushes all four,
# refreshes the systemd unit and restarts the service. Run
# deploy/provision.sh once first; this script assumes the server is already
# set up and will say so if it is not.
#
# Usage:
#   ./deploy/deploy.sh                 # gate + build + ship + restart
#   ./deploy/deploy.sh --skip-build    # ship the last build again
#   ./deploy/deploy.sh --with-vhost    # also reinstall both Apache vhosts
#   ./deploy/deploy.sh --skip-tests    # ship past a failing gate (emergencies)
#
# THE GATE: go build, go vet, the full Go suite, and a typecheck plus
# production build of each SPA. CI runs the same things on a pull request, but
# this server exists to try branches that have not been through CI, so the gate
# runs here too rather than trusting that it happened somewhere.
#
# The Go suite needs no database: internal/storetest gives every test its own
# SQLite schema.
#
# Unlike a production deploy, ANY branch may ship here: that is what a QA
# server is for. A tree that is not clean still warns loudly and names the
# commit it does not match, because the binary on the server is then traceable
# to nothing reviewable.
#
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
# shellcheck source=lib/common.sh
source "$HERE/lib/common.sh"

# ---- configuration (override via environment) ----
SERVER="${SERVER:-muni-demo}"
PORTAL_DOMAIN="${PORTAL_DOMAIN:-cityconnect.dev-pro.app}"
ADMIN_DOMAIN="${ADMIN_DOMAIN:-cityconnect-admin.dev-pro.app}"
APP_DIR="${APP_DIR:-/app/cityconnect}"
SERVICE="${SERVICE:-cityconnect}"
SVC_USER="${SVC_USER:-cityconnect}"
PORT="${PORT:-8095}"
TARGET_ARCH="${TARGET_ARCH:-amd64}"
BUILD_DIR="$HERE/build"

SKIP_BUILD=0; WITH_VHOST=0; SKIP_TESTS=0
for arg in "$@"; do
  case "$arg" in
    --skip-build) SKIP_BUILD=1 ;;
    --with-vhost) WITH_VHOST=1 ;;
    --skip-tests) SKIP_TESTS=1 ;;
    -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option: $arg" ;;
  esac
done

require_cmd ssh scp rsync git go npm

# ---------------------------------------------------------------------------
# 1. Provenance — warn, do not refuse
# ---------------------------------------------------------------------------
if [[ "$SKIP_BUILD" -eq 0 ]]; then
  branch="$(git -C "$ROOT" rev-parse --abbrev-ref HEAD)"
  head_sha="$(git -C "$ROOT" rev-parse --short HEAD)"
  dirty_count="$(git -C "$ROOT" status --porcelain | grep -c . || true)"
  if [[ "$dirty_count" -gt 0 ]]; then
    warn "Shipping a DIRTY tree: $dirty_count uncommitted file(s) on '$branch' ($head_sha).
    What lands on $PORTAL_DOMAIN will match no commit. Fine for QA; never for production."
  else
    step "Shipping '$branch' at $head_sha (clean)"
  fi
fi

# ---------------------------------------------------------------------------
# 2. Gate
# ---------------------------------------------------------------------------
if [[ "$SKIP_TESTS" -eq 0 ]]; then
  log "Gate: go build"; ( cd "$ROOT" && go build ./... )
  log "Gate: go vet";   ( cd "$ROOT" && go vet ./... )
  log "Gate: go test";  ( cd "$ROOT" && go test ./... )
  # Each SPA gets its own step so a failure names the app that broke, rather
  # than "the frontend".
  log "Gate: typecheck console"; ( cd "$ROOT" && npm run lint --workspace web )
  log "Gate: typecheck portal";  ( cd "$ROOT" && npm run lint --workspace web-portal )
  log "Gate passed"
else
  warn "--skip-tests: shipping without running build, vet, the Go suite or the
    typechecks. Nothing has checked this code."
fi

# ---------------------------------------------------------------------------
# 3. Build
# ---------------------------------------------------------------------------
if [[ "$SKIP_BUILD" -eq 0 ]]; then
  log "Building API and ccadm (linux/$TARGET_ARCH, cgo-free)"
  mkdir -p "$BUILD_DIR"
  # CGO_ENABLED=0: the MySQL driver is pure Go, so these are fully static
  # binaries that need nothing from the server's libc.
  #
  # ccadm goes too. It is how an operator runs migrate, seed, grant-role,
  # check-c2 and reissue-references, and an API with no admin tool beside it
  # means the first locked-out administrator has no way back in.
  for cmd in server:$SERVICE ccadm:ccadm; do
    src="${cmd%%:*}"; out="${cmd#*:}"
    ( cd "$ROOT" && GOOS=linux GOARCH="$TARGET_ARCH" CGO_ENABLED=0 \
        go build -trimpath -ldflags "-s -w" -o "$BUILD_DIR/$out" "./cmd/$src" )
    step "$out: $(cd "$BUILD_DIR" && ls -lh "$out" | awk '{print $5}')"
  done

  # Both SPAs are served at the ROOT of their own hostname, so neither needs a
  # base path. That is the whole reason for two hostnames — see the vhosts.
  log "Building console SPA"
  ( cd "$ROOT" && npm ci --silent )
  ( cd "$ROOT/web" && CC_BASE_PATH=/ npm run build )

  log "Building portal SPA"
  ( cd "$ROOT/web-portal" && CC_PORTAL_BASE_PATH=/ npm run build )
else
  log "Skipping build (--skip-build)"
  [[ -x "$BUILD_DIR/$SERVICE" ]] || die "no prior build at $BUILD_DIR/$SERVICE"
  [[ -d "$ROOT/web/dist" && -d "$ROOT/web-portal/dist" ]] || die "no prior SPA build"
fi

# ---------------------------------------------------------------------------
# 4. Preconditions on the server
# ---------------------------------------------------------------------------
log "Checking $SERVER"
remote "test -f '$APP_DIR/$SERVICE.env'" \
  || die "no $APP_DIR/$SERVICE.env on $SERVER — run ./deploy/provision.sh first.
This script will not start a service with no configuration."

# The service refuses to boot without a DSN. Catch that here rather than
# restarting a working service into a crash loop. Only the KEY is ever
# grepped — the line holds the database password.
remote "grep -qE '^[[:space:]]*CC_DB_DSN=' '$APP_DIR/$SERVICE.env'" \
  || die "$APP_DIR/$SERVICE.env has no CC_DB_DSN; the service would fail to start."

# A public API on this shared box would put the staff surface on the open
# internet regardless of what the portal vhost's Require rules say.
if ! remote "grep -qE '^[[:space:]]*CC_ADDR=127\.0\.0\.1:' '$APP_DIR/$SERVICE.env'"; then
  warn "CC_ADDR is not bound to 127.0.0.1 in $APP_DIR/$SERVICE.env.
    The portal vhost default-denies most of /api, and every one of those rules
    is decoration if the API answers on a public interface."
fi
step "server configuration looks sane"

# ---------------------------------------------------------------------------
# 5. Ship the SPAs
# ---------------------------------------------------------------------------
# --delete keeps each docroot clean, but the ACME challenge directory must
# survive or certificate renewal silently starts failing 60 days from now.
log "Syncing console SPA to $APP_DIR/console"
rsync -az --delete --exclude '.well-known/' "$ROOT/web/dist/" "$SERVER:$APP_DIR/console/"
log "Syncing portal SPA to $APP_DIR/portal"
rsync -az --delete --exclude '.well-known/' "$ROOT/web-portal/dist/" "$SERVER:$APP_DIR/portal/"

# ---------------------------------------------------------------------------
# 6. Ship the binaries
# ---------------------------------------------------------------------------
log "Uploading binaries"
# Upload beside the target and rename: writing in place gives "text file busy"
# against the running process, and a half-copied binary would be started by the
# restart below.
scp -q "$BUILD_DIR/$SERVICE" "$SERVER:$APP_DIR/$SERVICE.new"
scp -q "$BUILD_DIR/ccadm"    "$SERVER:$APP_DIR/ccadm.new"
remote "cp -f '$APP_DIR/$SERVICE' '$APP_DIR/$SERVICE.prev' 2>/dev/null || true
        mv -f '$APP_DIR/$SERVICE.new' '$APP_DIR/$SERVICE'
        mv -f '$APP_DIR/ccadm.new'    '$APP_DIR/ccadm'
        chown $SVC_USER:$SVC_USER '$APP_DIR/$SERVICE' '$APP_DIR/ccadm'
        chmod 0755 '$APP_DIR/$SERVICE' '$APP_DIR/ccadm'"
step "previous API binary kept as $SERVICE.prev"

# ---------------------------------------------------------------------------
# 7. Unit, vhosts, restart
# ---------------------------------------------------------------------------
log "Refreshing systemd unit"
STAGE="$(mktemp -d)"; trap 'rm -rf "$STAGE"' EXIT
render_template "$HERE/systemd/cityconnect.service.tmpl" "$STAGE/$SERVICE.service" \
  APP_DIR="$APP_DIR" SERVICE="$SERVICE" USER="$SVC_USER"
scp -q "$STAGE/$SERVICE.service" "$SERVER:/etc/systemd/system/$SERVICE.service"
remote "systemctl daemon-reload"

if [[ "$WITH_VHOST" -eq 1 ]]; then
  log "Reinstalling Apache vhosts"
  for pair in "portal:$PORTAL_DOMAIN" "admin:$ADMIN_DOMAIN"; do
    role="${pair%%:*}"; domain="${pair#*:}"
    render_template "$HERE/apache/cityconnect-$role.conf.tmpl" "$STAGE/$SERVICE-$role.conf" \
      DOMAIN="$domain" APP_DIR="$APP_DIR" PORT="$PORT" SERVICE="$SERVICE"
    scp -q "$STAGE/$SERVICE-$role.conf" "$SERVER:/etc/apache2/sites-available/$SERVICE-$role.conf"
  done
  # One configtest after both are in place, then one reload. A syntax error
  # here would take down C2, parking and facility-booking too, not just this
  # service — so the reload only happens if the whole config still parses.
  remote "apache2ctl configtest && systemctl reload apache2" \
    || die "apache configtest failed — apache NOT reloaded, so nothing is broken.
Fix the template and re-run with --with-vhost. The bad files are on the server
at /etc/apache2/sites-available/$SERVICE-*.conf."
fi

# ---------------------------------------------------------------------------
# 7b. Schema
# ---------------------------------------------------------------------------
# Before the restart, so a migration that fails leaves the OLD binary running
# against the schema it was built for, rather than a new one against a schema
# half-applied.
#
# ccadm rather than CC_DB_AUTOMIGRATE: config validation refuses AutoMigrate on
# anything that is not CC_ENV=dev or test, and CC_ENV=prod is what turns on the
# C2 credential checks and the guard against binding the API publicly. An
# explicit step here is better anyway — it fails where somebody is watching,
# not at 3am inside a restart.
log "Applying the schema"
remote "set -a; . '$APP_DIR/$SERVICE.env'; set +a; '$APP_DIR/ccadm' migrate" \
  || die "ccadm migrate failed — the service has NOT been restarted, so the
previous binary is still serving against the schema it was built for.
Inspect: ssh $SERVER '$APP_DIR/ccadm migrate'"

log "Restarting $SERVICE"
remote "systemctl restart '$SERVICE'"

log "Health check"
# Probe on loopback: the direct, reliable check that the service itself is up.
if remote "sleep 2; systemctl is-active --quiet '$SERVICE' && curl -fsS 'http://127.0.0.1:$PORT/healthz' >/dev/null"; then
  step "OK — $SERVICE is active and healthy on loopback."
else
  warn "Health check FAILED. Inspect:
    ssh $SERVER 'systemctl status $SERVICE --no-pager; journalctl -u $SERVICE -n 50 --no-pager'
  Roll back:
    ssh $SERVER 'mv $APP_DIR/$SERVICE.prev $APP_DIR/$SERVICE && systemctl restart $SERVICE'"
  exit 1
fi

# The public paths exercise Apache, TLS and both vhosts. The loopback check
# above passes even when a vhost is broken, so this is the one that catches a
# certificate that did not renew or a docroot that did not sync.
log "Public checks"
failures=0
for url in "https://$PORTAL_DOMAIN/healthz" "https://$PORTAL_DOMAIN/" "https://$ADMIN_DOMAIN/"; do
  code="$(curl -s -o /dev/null -w '%{http_code}' "$url" || true)"
  if [[ "$code" == "200" ]]; then
    step "$url -> 200"
  else
    warn "$url -> $code"
    failures=$((failures + 1))
  fi
done

# The portal vhost default-denies the staff surface. If that rule ever stops
# working, this is the cheapest place to find out — a 403 is the pass.
code="$(curl -s -o /dev/null -w '%{http_code}' "https://$PORTAL_DOMAIN/api/requests" || true)"
if [[ "$code" == "403" ]]; then
  step "https://$PORTAL_DOMAIN/api/requests -> 403 (staff surface refused on the public origin)"
else
  warn "https://$PORTAL_DOMAIN/api/requests -> $code, expected 403.
    The public origin should not be able to reach the staff API at all."
  failures=$((failures + 1))
fi

[[ "$failures" -eq 0 ]] || warn "$failures public check(s) failed; the service is healthy on loopback, so look at the vhosts."

log "Deployed — portal https://$PORTAL_DOMAIN/ · console https://$ADMIN_DOMAIN/"
