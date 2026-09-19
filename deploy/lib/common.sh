#!/usr/bin/env bash
# common.sh — helpers shared by provision.sh and deploy.sh.
#
# Lifted from the facility-booking repo's deploy/lib/common.sh, deliberately
# unchanged. The two applications share the muni-demo box, and a helper that
# has drifted between them is a trap for whoever reads the second one expecting
# the first.
#
# Sourced, never executed. Everything here is deliberately dependency-free:
# these scripts run from a developer workstation with nothing installed but
# bash, ssh, rsync, go and node.

# ---- output -------------------------------------------------------------
log()  { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
step() { printf '    %s\n' "$*"; }
warn() { printf '\n\033[1;33m!!  %s\033[0m\n' "$*" >&2; }
die()  { printf '\n\033[1;31mxx  %s\033[0m\n' "$*" >&2; exit 1; }

require_cmd() {
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || die "required command not found: $c"
  done
}

# ---- templates ----------------------------------------------------------
# render_template <src> <dst> KEY=VALUE...
#
# Substitutes @@KEY@@ tokens. The same file therefore serves every
# environment, which is the point: a per-environment copy of a vhost drifts
# from the one in git, and the drift is only discovered when a deploy
# overwrites a hand-edit somebody made on the server months earlier.
#
# Values go through a sed-safe escape. They are hostnames, paths and ports —
# never secrets, which reach the server only through the env file.
render_template() {
  local src="$1" dst="$2"; shift 2
  [[ -f "$src" ]] || die "template not found: $src"
  local script='' pair key value
  for pair in "$@"; do
    key="${pair%%=*}"
    value="${pair#*=}"
    # Escape the sed replacement metacharacters: \ & and the delimiter |
    value="${value//\\/\\\\}"; value="${value//&/\\&}"; value="${value//|/\\|}"
    script+="s|@@${key}@@|${value}|g;"
  done
  sed "$script" "$src" > "$dst" || die "failed to render $src"

  # An unsubstituted token means a template gained a placeholder that the
  # caller does not pass. Catching it here beats Apache failing configtest
  # with a literal @@PORT@@ in a ProxyPass line.
  if grep -q '@@[A-Z0-9_]\+@@' "$dst"; then
    die "unsubstituted tokens in $dst: $(grep -o '@@[A-Z0-9_]\+@@' "$dst" | sort -u | tr '\n' ' ')"
  fi
}

# ---- remote -------------------------------------------------------------
# The server is addressed by an ssh alias (see deploy.md), so no user@host or
# key handling belongs in these scripts.
remote()      { ssh "$SERVER" "$@"; }
remote_sudo() { ssh "$SERVER" "$@"; }   # the alias already connects as root

# secret <bytes> — a URL-safe random string for generated credentials.
# openssl is present on macOS and every Linux we target; /dev/urandom is the
# fallback so this never silently produces a weak value.
secret() {
  local bytes="${1:-32}"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 "$((bytes * 2))" | tr -d '\n=+/' | cut -c1-"$((bytes * 2))"
  else
    LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom | head -c "$((bytes * 2))"
  fi
}
