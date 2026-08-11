#!/usr/bin/env bash
#
# Manage the CityConnect development environment.
#
#   scripts/dev.sh start|stop|restart|status|logs|doctor [service...]
#
# Services: stub, api, web  (default: all three, in dependency order)
#
# MariaDB is treated as an external dependency: this script connects to it and
# reports clearly when it cannot, but never starts, stops or installs it. Your
# database is yours.
#
# State lives in .dev/ — one pid file and one log file per service, both
# gitignored. Nothing here touches a deployed environment.

set -euo pipefail

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

readonly DEV_DIR="${ROOT}/.dev"
readonly SERVICES=(stub api web)

# ---------------------------------------------------------------------------
# Configuration — override any of these in scripts/dev.env (gitignored)
# ---------------------------------------------------------------------------

# Sourced before the defaults below, so a CC_DEV_* value set there actually
# wins. Sourcing afterwards would silently ignore every override.
# shellcheck source=/dev/null
[[ -f "${ROOT}/scripts/dev.env" ]] && source "${ROOT}/scripts/dev.env"

DB_HOST="${CC_DEV_DB_HOST:-127.0.0.1}"
DB_PORT="${CC_DEV_DB_PORT:-3306}"
DB_USER="${CC_DEV_DB_USER:-cityconnect}"
DB_PASSWORD="${CC_DEV_DB_PASSWORD:-cityconnect}"
DB_NAME="${CC_DEV_DB_NAME:-cityconnect}"

# STUB_PORT defaults to 5273, not 5173. http://localhost:5173 is C2's
# documented dev origin, and if a real C2 is running there the collision is
# genuinely confusing: the stub appears to start, but `localhost` resolves to
# the real C2 first and requests silently go there instead. `doctor` checks.
STUB_PORT="${CC_DEV_STUB_PORT:-5273}"
API_PORT="${CC_DEV_API_PORT:-4021}"
WEB_PORT="${CC_DEV_WEB_PORT:-5174}"

CLIENT_ID="${CC_DEV_CLIENT_ID:-cityconnect-dev}"
CLIENT_SECRET="${CC_DEV_CLIENT_SECRET:-dev-secret}"
ADMIN_SUB="${CC_DEV_ADMIN_SUB:-}"
# An email is what an operator actually knows before anyone has signed in — a
# C2 subject identifier is opaque and only observable after a first login.
ADMIN_EMAIL="${CC_DEV_ADMIN_EMAIL:-}"

# Set CC_DEV_C2_ORIGIN to point at a real C2 instead of the stub. The stub is
# then simply not started.
C2_ORIGIN_OVERRIDE="${CC_DEV_C2_ORIGIN:-}"

# 127.0.0.1 by default: where something else holds the IPv6 loopback for a
# port, the two names reach different processes.
#
# It is overridable because C2 and CityConnect must agree on a hostname.
# 127.0.0.1 and localhost are different cookie hosts, and the C2 auth guide
# warns that mixing them — issuer on one, your app on the other — drops the
# session. If C2 is reached as localhost, set CC_DEV_HOST=localhost too.
readonly HOST="${CC_DEV_HOST:-127.0.0.1}"
readonly STUB_ORIGIN="http://${HOST}:${STUB_PORT}"
readonly C2_ORIGIN="${C2_ORIGIN_OVERRIDE:-$STUB_ORIGIN}"
readonly API_URL="http://${HOST}:${API_PORT}"
readonly WEB_URL="http://${HOST}:${WEB_PORT}"
readonly DB_DSN="${CC_DEV_DB_DSN:-${DB_USER}:${DB_PASSWORD}@tcp(${DB_HOST}:${DB_PORT})/${DB_NAME}?charset=utf8mb4&parseTime=True&loc=Local}"

USING_REAL_C2=false
[[ -n "$C2_ORIGIN_OVERRIDE" ]] && USING_REAL_C2=true

# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------

if [[ -t 1 ]]; then
  C_RESET=$'\033[0m'; C_DIM=$'\033[2m'; C_BOLD=$'\033[1m'
  C_BLUE=$'\033[34m'; C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_RED=$'\033[31m'
else
  C_RESET=""; C_DIM=""; C_BOLD=""; C_BLUE=""; C_GREEN=""; C_YELLOW=""; C_RED=""
fi

step() { printf '%s==>%s %s\n' "$C_BLUE" "$C_RESET" "$*"; }
ok()   { printf '  %s✓%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn() { printf '  %s!%s %s\n' "$C_YELLOW" "$C_RESET" "$*"; }
bad()  { printf '  %s✗%s %s\n' "$C_RED" "$C_RESET" "$*"; }
die()  { printf '%serror%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; exit 1; }
hint() { printf '    %s%s%s\n' "$C_DIM" "$*" "$C_RESET"; }

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

pidfile() { echo "${DEV_DIR}/$1.pid"; }
logfile() { echo "${DEV_DIR}/$1.log"; }

port_for() {
  case "$1" in stub) echo "$STUB_PORT" ;; api) echo "$API_PORT" ;; web) echo "$WEB_PORT" ;; esac
}

running() {
  local pf; pf="$(pidfile "$1")"
  [[ -f "$pf" ]] || return 1
  local pid; pid="$(cat "$pf" 2>/dev/null || true)"
  [[ -n "$pid" ]] || return 1
  if kill -0 "$pid" 2>/dev/null; then return 0; fi
  rm -f "$pf"   # stale: the process died without cleaning up
  return 1
}

pid_of() { cat "$(pidfile "$1")" 2>/dev/null || true; }

port_in_use() { lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; }
port_owner()  { lsof -nP -iTCP:"$1" -sTCP:LISTEN 2>/dev/null | awk 'NR==2 {print $1" (pid "$2")"}'; }

wait_for_http() {
  local url="$1" label="$2" tries="${3:-60}"
  for ((i = 1; i <= tries; i++)); do
    curl -fsS --max-time 2 "$url" >/dev/null 2>&1 && return 0
    sleep 0.5
  done
  bad "${label} did not come up within $((tries / 2))s"
  return 1
}

# DB_ERROR holds the last probe's stderr, so the hint can name the actual cause
# instead of listing every possibility.
DB_ERROR=""

# db_probe checks that the configured credentials actually work, which is the
# one dependency failure that otherwise surfaces as an unhelpful error deep in
# the API log.
db_probe() {
  command -v mysql >/dev/null 2>&1 || return 2   # cannot check; not a failure
  DB_ERROR="$(mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" ${DB_PASSWORD:+-p"$DB_PASSWORD"} \
    --connect-timeout=5 -e "SELECT 1" "$DB_NAME" 2>&1 >/dev/null)"
  [[ -z "$(printf '%s' "$DB_ERROR" | grep -i '^ERROR' || true)" ]]
}

# db_setup_hint branches on the failure. MariaDB's error codes distinguish
# these precisely, and the fixes are completely different — advising "create
# the user" when the user already exists with socket auth sends people in
# circles.
db_setup_hint() {
  hint "CityConnect cannot reach '${DB_NAME}' at ${DB_HOST}:${DB_PORT} as '${DB_USER}'."
  hint ""

  case "$DB_ERROR" in
    *"ERROR 1698"*)
      hint "MariaDB says 1698: the user '${DB_USER}' exists but authenticates via"
      hint "unix_socket. That ignores passwords and only accepts a connection from"
      hint "an OS user of the same name — so it can never work over TCP, which is"
      hint "how the application connects."
      hint ""
      hint "Switch it to password authentication:"
      hint "  sudo mysql -e \"ALTER USER '${DB_USER}'@'localhost' IDENTIFIED VIA mysql_native_password USING PASSWORD('${DB_PASSWORD}');\""
      hint "  sudo mysql -e \"GRANT ALL ON ${DB_NAME}.* TO '${DB_USER}'@'localhost'; FLUSH PRIVILEGES;\""
      ;;
    *"ERROR 1045"*)
      hint "MariaDB says 1045: wrong password, or no such user."
      hint ""
      hint "Create it:"
      hint "  sudo mysql -e \"CREATE USER '${DB_USER}'@'localhost' IDENTIFIED BY '${DB_PASSWORD}';\""
      hint "  sudo mysql -e \"GRANT ALL ON ${DB_NAME}.* TO '${DB_USER}'@'localhost'; FLUSH PRIVILEGES;\""
      ;;
    *"ERROR 1049"*)
      hint "MariaDB says 1049: the database '${DB_NAME}' does not exist."
      hint ""
      hint "  sudo mysql -e \"CREATE DATABASE ${DB_NAME} CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\""
      hint "  sudo mysql -e \"GRANT ALL ON ${DB_NAME}.* TO '${DB_USER}'@'localhost'; FLUSH PRIVILEGES;\""
      ;;
    *"Can't connect"*|*"connect-timeout"*|*"Connection refused"*)
      hint "Nothing answered on ${DB_HOST}:${DB_PORT}. Is MariaDB running?"
      hint "  brew services start mariadb"
      ;;
    *)
      [[ -n "$DB_ERROR" ]] && hint "MariaDB said: ${DB_ERROR}"
      hint ""
      hint "  sudo mysql -e \"CREATE DATABASE IF NOT EXISTS ${DB_NAME} CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\""
      hint "  sudo mysql -e \"CREATE USER IF NOT EXISTS '${DB_USER}'@'localhost' IDENTIFIED BY '${DB_PASSWORD}';\""
      hint "  sudo mysql -e \"GRANT ALL ON ${DB_NAME}.* TO '${DB_USER}'@'localhost'; FLUSH PRIVILEGES;\""
      ;;
  esac

  hint ""
  hint "Using a different database or user? Put it in scripts/dev.env:"
  hint "  CC_DEV_DB_USER=…  CC_DEV_DB_PASSWORD=…  CC_DEV_DB_NAME=…"
}

# spawn starts a background process and records its pid.
spawn() {
  local svc="$1"; shift
  local log; log="$(logfile "$svc")"
  : > "$log"
  "$@" >>"$log" 2>&1 &
  echo $! > "$(pidfile "$svc")"
}

# build compiles a binary into .dev/bin.
#
# The Go services are built and executed directly rather than run through
# `go run`, which spawns the compiled program as a *child*: the recorded pid
# would be the wrapper, and stopping it would leave the real server holding
# the port. Building also turns a compile error into a clear message here
# instead of a service that never appears.
build() {
  local svc="$1" pkg="$2"
  mkdir -p "${DEV_DIR}/bin"
  step "Building ${svc}"
  if ! go build -o "${DEV_DIR}/bin/${svc}" "$pkg" 2>"${DEV_DIR}/${svc}.build.log"; then
    bad "${svc} did not compile"
    sed 's/^/    /' "${DEV_DIR}/${svc}.build.log"
    return 1
  fi
  rm -f "${DEV_DIR}/${svc}.build.log"
}

# kill_tree signals a process and everything beneath it. npm spawns vite as a
# child, so signalling only the parent leaves the port held.
kill_tree() {
  local pid="$1" sig="${2:-TERM}" child
  for child in $(pgrep -P "$pid" 2>/dev/null || true); do
    kill_tree "$child" "$sig"
  done
  kill -"$sig" "$pid" 2>/dev/null || true
}

# reap_port clears a leftover listener after a stop, but only when the process
# holding it is one of ours — never anything else that happens to be there.
reap_port() {
  local port="$1" expect="$2"
  local pid; pid="$(lsof -nP -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null | head -1)"
  [[ -n "$pid" ]] || return 0

  local cmd; cmd="$(ps -o comm= -p "$pid" 2>/dev/null || true)"
  if [[ "$cmd" == *"$expect"* ]]; then
    warn "reaping leftover ${expect} (pid ${pid}) still holding ${port}"
    kill_tree "$pid" TERM
    sleep 0.5
    kill_tree "$pid" KILL 2>/dev/null || true
  else
    warn "port ${port} is held by ${cmd:-something else} (pid ${pid}) — leaving it alone"
  fi
}

api_env() {
  export CC_ENV=dev
  export CC_ADDR=":${API_PORT}"
  export CC_DB_DSN="$DB_DSN"
  # AutoMigrate on, so a schema change lands on the next restart without a
  # separate step. Production forbids it; development is where it pays off.
  export CC_DB_AUTOMIGRATE=true
  export CC_C2_PORTAL_ORIGIN="$C2_ORIGIN"
  export CC_C2_CLIENT_ID="$CLIENT_ID"
  export CC_C2_CLIENT_SECRET="$CLIENT_SECRET"
  export CC_PUBLIC_URL="$WEB_URL"
  # Empty on purpose: in development the SPA is served at the root by Vite,
  # not under /cityconnect as in production.
  export CC_BASE_PATH=""
  export CC_BOOTSTRAP_ADMIN_SUBS="$ADMIN_SUB"
  export CC_BOOTSTRAP_ADMIN_EMAILS="$ADMIN_EMAIL"
  export CC_COOKIE_SECURE=false
  export CC_LOG_LEVEL="${CC_LOG_LEVEL:-info}"
}

require() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }

# ---------------------------------------------------------------------------
# Start
# ---------------------------------------------------------------------------

start_stub() {
  if $USING_REAL_C2; then
    step "Skipping the stub — configured against ${C2_ORIGIN}"
    return
  fi
  if running stub; then ok "C2 stub already running on ${STUB_PORT}"; return; fi
  if port_in_use "$STUB_PORT"; then
    die "port ${STUB_PORT} is held by $(port_owner "$STUB_PORT"). Set CC_DEV_STUB_PORT in scripts/dev.env."
  fi

  build stub ./cmd/c2stub || return 1

  step "Starting the C2 stub on ${STUB_PORT}"
  spawn stub "${DEV_DIR}/bin/stub" \
    --addr "${HOST}:${STUB_PORT}" \
    --client-id "$CLIENT_ID" \
    --client-secret "$CLIENT_SECRET" \
    --backchannel "${API_URL}/api/c2/backchannel-logout"

  wait_for_http "${STUB_ORIGIN}/oidc/.well-known/openid-configuration" "the C2 stub" || {
    tail -15 "$(logfile stub)" | sed 's/^/    /'; return 1
  }
  ok "C2 stub ready — issuer ${STUB_ORIGIN}/oidc"
}

start_api() {
  if running api; then ok "API already running on ${API_PORT}"; return; fi
  if port_in_use "$API_PORT"; then
    die "port ${API_PORT} is held by $(port_owner "$API_PORT"). Set CC_DEV_API_PORT in scripts/dev.env."
  fi

  # Check the database before starting, so a credential problem is reported
  # here in plain terms rather than as a stack trace in the API log.
  step "Checking the database"
  if db_probe; then
    ok "connected to ${DB_NAME} at ${DB_HOST}:${DB_PORT}"
  else
    case $? in
      2) warn "the mysql client is not installed, so credentials were not pre-checked" ;;
      *) bad "cannot connect to the database"; db_setup_hint; return 1 ;;
    esac
  fi

  build api ./cmd/server || return 1

  step "Starting the API on ${API_PORT}"
  ( api_env; spawn api "${DEV_DIR}/bin/api" )

  wait_for_http "${API_URL}/healthz" "the API" || {
    hint "last lines of $(logfile api):"
    tail -20 "$(logfile api)" | sed 's/^/    /'
    return 1
  }
  ok "API ready"

  # Liveness and readiness answer different questions: the process can be up
  # while C2 is unreachable, and with SSO as the only staff login that means
  # nobody can sign in.
  if curl -fsS --max-time 5 "${API_URL}/readyz" >/dev/null 2>&1; then
    ok "readiness ok — database and C2 both reachable"
  else
    warn "running, but not ready:"
    curl -s "${API_URL}/readyz" 2>/dev/null | sed 's/^/    /' || true
  fi
}

start_web() {
  if running web; then ok "console already running on ${WEB_PORT}"; return; fi
  if port_in_use "$WEB_PORT"; then
    die "port ${WEB_PORT} is held by $(port_owner "$WEB_PORT"). Set CC_DEV_WEB_PORT in scripts/dev.env."
  fi
  require npm

  if [[ ! -d web/node_modules ]]; then
    step "Installing frontend dependencies (first run only)"
    ( cd web && npm install ) || return 1
  fi

  step "Starting the console on ${WEB_PORT}"
  # Vite's base must match the API's base path, or the SPA is served at
  # /cityconnect/ while the API redirects to the root and sign-in lands on a
  # 404. In development both are the root; production uses /cityconnect/.
  # --host pins IPv4: Vite otherwise binds `localhost`, which on macOS resolves
  # to ::1 first, so the API's redirect target and the health check here would
  # reach a different socket than the one Vite is listening on.
  spawn web env CC_BASE_PATH=/ npm --prefix web run dev -- \
    --port "$WEB_PORT" --strictPort --host "$HOST"

  wait_for_http "$WEB_URL" "the console" || {
    tail -15 "$(logfile web)" | sed 's/^/    /'; return 1
  }
  ok "console ready"
}

# ---------------------------------------------------------------------------
# Stop
# ---------------------------------------------------------------------------

stop_service() {
  local svc="$1"

  if ! running "$svc"; then
    printf '  %s-%s %s not running\n' "$C_DIM" "$C_RESET" "$svc"
    rm -f "$(pidfile "$svc")"
    return
  fi

  local pid; pid="$(pid_of "$svc")"
  step "Stopping ${svc} (pid ${pid})"

  kill_tree "$pid" TERM

  for ((i = 0; i < 24; i++)); do
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.25
  done

  if kill -0 "$pid" 2>/dev/null; then
    warn "${svc} ignored SIGTERM; forcing"
    kill_tree "$pid" KILL
  fi

  rm -f "$(pidfile "$svc")"

  # A leftover listener means something escaped the tree; clear it if it is
  # ours, so the next start does not fail on a confusing port conflict.
  local port; port="$(port_for "$svc")"
  if port_in_use "$port"; then
    case "$svc" in
      web) reap_port "$port" node ;;
      *)   reap_port "$port" "$svc" ;;
    esac
  fi
  ok "${svc} stopped"
}

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------

cmd_start() {
  mkdir -p "$DEV_DIR"
  local targets=("$@")
  [[ ${#targets[@]} -eq 0 ]] && targets=("${SERVICES[@]}")

  # One service failing does not stop the others: a database problem should
  # still leave you with a running console to work against once you fix it.
  local failed=()
  for svc in "${targets[@]}"; do
    case "$svc" in
      stub) start_stub || failed+=(stub) ;;
      api)  start_api  || failed+=(api) ;;
      web)  start_web  || failed+=(web) ;;
      *) die "unknown service: ${svc} (expected: ${SERVICES[*]})" ;;
    esac
  done

  if [[ ${#failed[@]} -gt 0 ]]; then
    echo
    printf '%sStarted with problems.%s %s did not come up.\n' \
      "$C_YELLOW" "$C_RESET" "$(IFS=', '; echo "${failed[*]}")"
    hint "fix the cause above, then: scripts/dev.sh start ${failed[*]}"
    return 1
  fi

  echo
  printf '%sReady.%s\n' "$C_BOLD" "$C_RESET"
  printf '  Console   %s\n' "$WEB_URL"
  printf '  API       %s\n' "$API_URL"
  if $USING_REAL_C2; then
    printf '  C2        %s %s(real)%s\n' "$C2_ORIGIN" "$C_YELLOW" "$C_RESET"
  else
    printf '  C2 stub   %s\n' "$STUB_ORIGIN"
    echo
    printf '  Sign in as %s%s%s — the bootstrap grant makes it an administrator.\n' \
      "$C_BOLD" "${ADMIN_SUB:-$ADMIN_EMAIL}" "$C_RESET"
  fi
  echo
  hint "scripts/dev.sh logs -f      follow every log"
  hint "scripts/dev.sh restart api  after a Go change"
  hint "scripts/dev.sh demo         add a sample contact and request"
}

cmd_stop() {
  local targets=("$@")
  # Reverse dependency order: dependants go down before what they depend on.
  [[ ${#targets[@]} -eq 0 ]] && targets=(web api stub)
  for svc in "${targets[@]}"; do
    case "$svc" in
      stub|api|web) stop_service "$svc" ;;
      *) die "unknown service: ${svc}" ;;
    esac
  done
}

cmd_restart() {
  local targets=("$@")
  [[ ${#targets[@]} -eq 0 ]] && targets=("${SERVICES[@]}")

  local reversed=()
  for ((i = ${#targets[@]} - 1; i >= 0; i--)); do reversed+=("${targets[i]}"); done

  cmd_stop "${reversed[@]}"
  echo
  cmd_start "${targets[@]}"
}

# status_row pads the state on its plain text, then wraps it in colour.
# Padding a string that already contains escape sequences counts them toward
# the width and leaves the columns ragged.
status_row() {
  local name="$1" color="$2" state="$3" port="$4" detail="$5"
  printf '%-7s %s%-11s%s %-7s %s\n' "$name" "$color" "$state" "$C_RESET" "$port" "$detail"
}

cmd_status() {
  printf '%-7s %-11s %-7s %s\n' "SERVICE" "STATE" "PORT" "DETAIL"

  for svc in "${SERVICES[@]}"; do
    local port; port="$(port_for "$svc")"

    if [[ "$svc" == stub ]] && $USING_REAL_C2; then
      status_row "$svc" "$C_DIM" "not used" "-" "using ${C2_ORIGIN}"
      continue
    fi

    if running "$svc"; then
      local detail="pid $(pid_of "$svc")"
      if [[ "$svc" == api ]]; then
        if curl -fsS --max-time 2 "${API_URL}/readyz" >/dev/null 2>&1; then
          detail="ready · ${detail}"
        else
          detail="not ready — check the database and C2"
        fi
      fi
      status_row "$svc" "$C_GREEN" "running" "$port" "$detail"
    elif port_in_use "$port"; then
      # Something else holds our port. Naming it saves a confusing session.
      status_row "$svc" "$C_RED" "foreign" "$port" "held by $(port_owner "$port")"
    else
      status_row "$svc" "$C_DIM" "stopped" "$port" ""
    fi
  done

  echo
  if db_probe; then
    status_row "db" "$C_GREEN" "reachable" "$DB_PORT" "${DB_NAME} as ${DB_USER}"
  elif [[ $? -eq 2 ]]; then
    status_row "db" "$C_DIM" "unknown" "$DB_PORT" "mysql client not installed"
  else
    status_row "db" "$C_RED" "unreachable" "$DB_PORT" "${DB_NAME} as ${DB_USER}"
  fi
}

cmd_logs() {
  local follow=false files=() targets=()
  for arg in "$@"; do
    case "$arg" in
      -f|--follow) follow=true ;;
      *) targets+=("$arg") ;;
    esac
  done
  [[ ${#targets[@]} -eq 0 ]] && targets=("${SERVICES[@]}")

  for svc in "${targets[@]}"; do
    [[ -f "$(logfile "$svc")" ]] && files+=("$(logfile "$svc")")
  done
  [[ ${#files[@]} -eq 0 ]] && { warn "no logs yet — is anything running?"; return; }

  if $follow; then
    tail -n 20 -f "${files[@]}"
  else
    tail -n 60 "${files[@]}"
  fi
}

cmd_doctor() {
  step "Tooling"
  for tool in go npm curl lsof; do
    command -v "$tool" >/dev/null 2>&1 && ok "$tool" || bad "$tool is missing"
  done
  command -v mysql >/dev/null 2>&1 && ok "mysql client" || warn "mysql client missing (credentials cannot be pre-checked)"

  step "Database"
  if db_probe; then
    local tables
    tables="$(mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" ${DB_PASSWORD:+-p"$DB_PASSWORD"} \
      -N -B -e "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='${DB_NAME}';" 2>/dev/null || echo '?')"
    ok "${DB_NAME} reachable as ${DB_USER} — ${tables} table(s)"
    [[ "$tables" == "0" ]] && hint "empty; the API applies the schema on start (AutoMigrate is on in dev)"
  else
    case $? in
      2) warn "cannot check without the mysql client; the API will report it on start" ;;
      *) bad "cannot connect"; db_setup_hint ;;
    esac
  fi

  step "Ports"
  for svc in "${SERVICES[@]}"; do
    local port; port="$(port_for "$svc")"
    if running "$svc"; then ok "${port} — ours (${svc})"
    elif port_in_use "$port"; then bad "${port} — held by $(port_owner "$port"), wanted for ${svc}"
    else ok "${port} — free (${svc})"; fi
  done

  step "C2"
  if $USING_REAL_C2; then
    if curl -fsS --max-time 3 "${C2_ORIGIN}/oidc/.well-known/openid-configuration" >/dev/null 2>&1; then
      ok "real C2 at ${C2_ORIGIN} answers discovery"
      hint "client_id ${CLIENT_ID} must be registered there, with redirect_uri:"
      hint "  ${API_URL}/api/auth/callback"
    else
      bad "no discovery document at ${C2_ORIGIN}"
    fi
  else
    ok "using the stub on ${STUB_PORT}"
  fi

  # The trap worth naming explicitly, because it wastes an afternoon.
  if port_in_use 5173; then
    warn "something is listening on 5173: $(port_owner 5173)"
    if curl -fsS --max-time 2 http://localhost:5173/oidc/.well-known/openid-configuration >/dev/null 2>&1; then
      warn "it answers OIDC discovery — that is a real C2"
      hint "5173 is C2's documented dev origin. Never bind the stub there: it would"
      hint "appear to start while requests silently reach the real C2 instead."
      hint "To develop against it, put this in scripts/dev.env:"
      hint "  CC_DEV_C2_ORIGIN=http://localhost:5173"
      hint "  CC_DEV_CLIENT_ID=<a client registered in that C2>"
      hint "  CC_DEV_CLIENT_SECRET=<its secret>"
    fi
  fi

  step "API readiness"
  if running api; then
    curl -s "${API_URL}/readyz" 2>/dev/null | sed 's/^/    /'
  else
    warn "not running"
  fi
}

cmd_seed() { ( api_env; go run ./cmd/ccadm seed ); }
cmd_demo() { ( api_env; go run ./cmd/ccadm demo ); }

cmd_shell() {
  db_probe || { bad "cannot connect to the database"; db_setup_hint; exit 1; }
  mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" ${DB_PASSWORD:+-p"$DB_PASSWORD"} "$DB_NAME"
}

# reset drops every table. Named explicitly and confirmed, because it is the
# one command here that destroys work.
cmd_reset() {
  printf '%sThis drops every table in %s.%s\n' "$C_YELLOW" "$DB_NAME" "$C_RESET"
  read -r -p "Type 'reset' to confirm: " reply
  [[ "$reply" == "reset" ]] || { echo "Cancelled."; return; }

  db_probe || { bad "cannot connect to the database"; db_setup_hint; exit 1; }
  cmd_stop api

  step "Dropping and recreating ${DB_NAME}"
  mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" ${DB_PASSWORD:+-p"$DB_PASSWORD"} \
    -e "DROP DATABASE IF EXISTS \`${DB_NAME}\`; CREATE DATABASE \`${DB_NAME}\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
  ok "database recreated — start the API to reapply the schema"
}

cmd_env() {
  ( api_env
    for v in CC_ENV CC_ADDR CC_DB_DSN CC_DB_AUTOMIGRATE CC_C2_PORTAL_ORIGIN \
             CC_C2_CLIENT_ID CC_C2_CLIENT_SECRET CC_PUBLIC_URL CC_BASE_PATH \
             CC_BOOTSTRAP_ADMIN_SUBS CC_BOOTSTRAP_ADMIN_EMAILS CC_COOKIE_SECURE; do
      printf 'export %s=%q\n' "$v" "${!v}"
    done )
}

usage() {
  cat <<EOF
${C_BOLD}CityConnect development environment${C_RESET}

  scripts/dev.sh <command> [service...]

${C_BOLD}Commands${C_RESET}
  start [svc...]      Start services (default: all, in dependency order)
  stop [svc...]       Stop services (default: all, in reverse order)
  restart [svc...]    Stop then start
  status              What is running, and who holds a port if we cannot
  logs [-f] [svc...]  Show logs; -f follows
  doctor              Diagnose the environment, including the :5173 C2 trap
  seed                Re-apply the baseline configuration
  demo                Add a sample contact and request
  shell               Open a database shell
  reset               Drop and recreate the database (confirms first)
  env                 Print the env vars, for running ccadm by hand

${C_BOLD}Services${C_RESET}
  stub  The C2 stand-in on ${STUB_PORT}
  api   The Go API on ${API_PORT}
  web   The console on ${WEB_PORT}

MariaDB is not managed here — it is expected on ${DB_HOST}:${DB_PORT}.

${C_BOLD}Examples${C_RESET}
  scripts/dev.sh start                 everything
  scripts/dev.sh restart api           after a Go change
  scripts/dev.sh logs -f api           follow the API log
  eval "\$(scripts/dev.sh env)"         then run ccadm by hand

Override any port or credential in ${C_DIM}scripts/dev.env${C_RESET} (gitignored).
EOF
}

main() {
  local cmd="${1:-help}"
  [[ $# -gt 0 ]] && shift

  case "$cmd" in
    start)     cmd_start "$@" ;;
    stop)      cmd_stop "$@" ;;
    restart)   cmd_restart "$@" ;;
    status|ps) cmd_status ;;
    logs|log)  cmd_logs "$@" ;;
    doctor)    cmd_doctor ;;
    seed)      cmd_seed ;;
    demo)      cmd_demo ;;
    shell|db)  cmd_shell ;;
    reset)     cmd_reset ;;
    env)       cmd_env ;;
    help|-h|--help) usage ;;
    *) printf '%sunknown command: %s%s\n\n' "$C_RED" "$cmd" "$C_RESET" >&2; usage; exit 2 ;;
  esac
}

main "$@"
