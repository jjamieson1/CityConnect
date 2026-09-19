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

# ---- dns ----------------------------------------------------------------
# resolve_addr <name> — the IPv4 address a name resolves to, or nothing.
#
# An ADDRESS, not whatever `dig +short` prints last. For a CNAME that ends
# nowhere — pointed at an apex with no A record, say — the last line is the
# target name, which is non-empty and reads as success.
# Always exits 0, printing nothing when there is no address. A trailing grep
# that matches nothing exits 1, and under `set -e` that turns a perfectly
# ordinary "this name does not resolve" into the caller dying with no message
# at all — which is exactly what it did the first time.
resolve_addr() {
  dig +short "$1" A 2>/dev/null | grep -Eo '^[0-9]+(\.[0-9]+){3}$' | tail -1 || true
}

# resolve_addr_authoritative <name> — the same, asked of the zone's own
# nameserver so no recursive cache is involved.
#
# This is the question that actually matters before issuing a certificate.
# A record can be correct at the authority and still be stale in a resolver
# for hours — Cloudflare held a wrong answer for cityconnect-admin for twelve
# — while Let's Encrypt resolves independently and would have succeeded
# throughout. Refusing to provision because the operator's laptop has an old
# answer cached is the check being wrong, not the DNS.
resolve_addr_authoritative() {
  local name="$1" zone="$1" ns_list="" ns ns_ip addr

  # Walk up the labels until one of them answers with a real NS record.
  #
  # The record TYPE has to be checked, not just that something came back.
  # `dig +short NS` on a name that is a CNAME prints the CNAME's target,
  # because every query type follows a CNAME — so asking for the NS of
  # cityconnect.dev-pro.app returns muni-demo.dev-pro.app, which is a web
  # server and answers no DNS at all.
  while [[ "$zone" == *.* ]]; do
    ns_list="$(dig +noall +answer NS "$zone" 2>/dev/null | awk '$4=="NS" {print $5}')"
    [[ -n "$ns_list" ]] && break
    zone="${zone#*.}"
  done
  [[ -n "$ns_list" ]] || return 1

  # Ask each nameserver by ADDRESS, and try all of them.
  #
  # "@ns1.example.com" makes dig resolve that name first, through the very
  # recursive resolver whose stale answer this function exists to bypass — and
  # when that lookup is slow or refused, dig prints nothing and the failure
  # looks like "no such record". Resolving the nameserver to an address first
  # removes that dependency; walking the whole list rides out one server being
  # rate-limited, which matters because they come back in random order.
  while read -r ns; do
    [[ -n "$ns" ]] || continue
    ns_ip="$(dig +short "$ns" A 2>/dev/null | grep -Eo '^[0-9]+(\.[0-9]+){3}$' | head -1)"
    [[ -n "$ns_ip" ]] || continue
    addr="$(dig +short +time=3 +tries=1 "@${ns_ip}" "$name" A 2>/dev/null \
            | grep -Eo '^[0-9]+(\.[0-9]+){3}$' | tail -1)"
    if [[ -n "$addr" ]]; then
      printf '%s\n' "$addr"
      return 0
    fi
  done <<< "$ns_list"
  return 1
}

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
