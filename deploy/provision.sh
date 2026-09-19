#!/usr/bin/env bash
#
# provision.sh — one-time setup of CityConnect on the muni-demo QA server.
#
# Creates the service account, the directory tree, the database and its
# least-privilege user, the environment file, the C2 client signing key, a TLS
# certificate for EACH of the two hostnames, both Apache vhosts and the systemd
# unit. Idempotent: re-running it is safe and changes nothing that already
# exists. Secrets it generates are written once and never regenerated, because
# rotating them silently would log every user out and break the C2 client.
#
# It does NOT ship the application. Run deploy/deploy.sh for that — provision
# installs and enables the unit, and starts it only once a binary is present.
#
# Usage:
#   CC_C2_CLIENT_ID=c2f_... CC_C2_CLIENT_SECRET=... CC_BOOTSTRAP_ADMIN_SUBS=... \
#     CC_SMTP_PASSWORD=re_... ./deploy/provision.sh
#   ./deploy/provision.sh --dry-run     # print the remote script, change nothing
#   ./deploy/provision.sh --skip-tls    # stop before certbot; print the commands
#
# Those three values are required on a FIRST run only. Once the env file exists
# on the server it is left strictly alone, so later runs need nothing.
#
# TWO HOSTNAMES, on purpose. The citizen portal and the staff console get
# separate origins so they get separate cookie jars: script on the public site
# then has no ambient authority over a staff session in the same browser. The
# portal hostname is also the only surface C2 talks to.
#
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "$HERE/lib/common.sh"

# ---- configuration (override via environment) ----
SERVER="${SERVER:-muni-demo}"           # ssh alias; connects as root
PORTAL_DOMAIN="${PORTAL_DOMAIN:-cityconnect.dev-pro.app}"
ADMIN_DOMAIN="${ADMIN_DOMAIN:-cityconnect-admin.dev-pro.app}"
APP_DIR="${APP_DIR:-/app/cityconnect}"
SERVICE="${SERVICE:-cityconnect}"
SVC_USER="${SVC_USER:-cityconnect}"
PORT="${PORT:-8095}"                    # 8090 audit, 8092 c2-api, 8093 parking, 8094 facility
DB_NAME="${DB_NAME:-cityconnect}"
DB_USER="${DB_USER:-cityconnect_app}"
C2_ORIGIN="${C2_ORIGIN:-https://muni-demo.dev-pro.app/c2}"

# Guest and anonymous notifications leave through Resend's SMTP interface, so
# both mail paths use the same provider and the same verified domain. The
# address must be on a domain verified in Resend, and verification is EXACT:
# dev-pro.app is verified, cityconnect.dev-pro.app is not.
MAIL_FROM="${MAIL_FROM:-jamie@dev-pro.app}"
SMTP_RELAY="${SMTP_RELAY:-smtp.resend.com}"

CERT_EMAIL="${CERT_EMAIL:-jamie@celestialtech.ca}"
ACME_WEBROOT="${ACME_WEBROOT:-/var/www/html}"

DRY_RUN=0
SKIP_TLS=0
for arg in "$@"; do
  case "$arg" in
    --dry-run)  DRY_RUN=1 ;;
    --skip-tls) SKIP_TLS=1 ;;
    -h|--help)  grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option: $arg" ;;
  esac
done

require_cmd ssh scp sed openssl

# ---------------------------------------------------------------------------
# 1. Recon — read-only, before anything is changed
# ---------------------------------------------------------------------------
# This is a SHARED host running C2, parking, facility-booking and the audit
# service. Every check here is one that, if skipped, could take down somebody
# else's service: binding a port that is in use, or issuing a certificate for a
# hostname that does not point at this machine and having Apache fail to
# reload.
log "Recon on $SERVER (read-only)"

remote "test -d /etc/apache2" || die "no /etc/apache2 on $SERVER — this script targets Ubuntu + apache2, not httpd/RHEL"

env_exists=0
if remote "test -f '$APP_DIR/$SERVICE.env'"; then
  env_exists=1
  step "env file already present — secrets will be preserved"
fi

port_owner="$(remote "ss -ltnpH 'sport = :$PORT' 2>/dev/null | head -1" || true)"
if [[ -n "$port_owner" ]]; then
  if [[ "$port_owner" == *"$SERVICE"* ]]; then
    step "port $PORT already held by $SERVICE (fine, it is ours)"
  else
    die "port $PORT is in use by something else:
    $port_owner
Pick a free port with PORT=<n>; CC_ADDR in the env file follows it."
  fi
else
  step "port $PORT is free"
fi

# deflate is in this list and not in facility-booking's: both vhosts compress
# their SPA with AddOutputFilterByType, and Apache fails configtest on an
# unknown directive rather than ignoring it.
for m in proxy_http headers rewrite ssl deflate; do
  remote "apache2ctl -M 2>/dev/null | grep -q ${m}_module" \
    || die "apache module ${m} is not enabled. Run ./deploy/host-setup.sh, which
enables every module these vhosts need, or by hand:
    ssh $SERVER 'a2enmod ${m} && apache2ctl configtest && systemctl reload apache2'"
done
step "apache modules present: proxy_http headers rewrite ssl deflate"

# ---------------------------------------------------------------------------
# Host preconditions — warn, do not refuse
# ---------------------------------------------------------------------------
# All of these are deploy/host-setup.sh's job. They are re-checked here because
# skipping that script does not fail: it produces a host that works until the
# first signature update or the first citizen attachment, and then does not.
# None of them is a reason to refuse to provision, so each one says what will
# happen rather than stopping.
host_warnings=0

if [[ -z "$(remote "swapon --show --noheadings 2>/dev/null" || true)" ]]; then
  warn "No swap on $SERVER.
    clamd holds ~960MB resident and MySQL another ~490MB; a host with no swap
    has no margin, and the first spike kills a process rather than paging.
    Fix: ./deploy/host-setup.sh"
  host_warnings=$((host_warnings + 1))
fi

avail_mb="$(remote "free -m | awk '/^Mem:/ {print \$7}'" 2>/dev/null || echo 0)"
if [[ "$avail_mb" =~ ^[0-9]+$ ]] && (( avail_mb < 300 )); then
  warn "Only ${avail_mb}MB available on $SERVER, and CityConnect is not running yet.
    This host is shared — the OOM killer's biggest targets are clamd, MySQL and
    C2, so the service that dies may not be ours."
  host_warnings=$((host_warnings + 1))
fi

if remote "command -v clamdscan >/dev/null 2>&1"; then
  if ! remote "clamdscan --ping 1 >/dev/null 2>&1"; then
    warn "clamd is installed on $SERVER but not answering.
    Every citizen attachment will stay quarantined and never be served, while
    the resident is told it arrived. Nothing errors at boot.
    Check: ssh $SERVER 'journalctl -u clamav-daemon -n 30'"
    host_warnings=$((host_warnings + 1))
  elif ! remote "grep -qiE '^[[:space:]]*ConcurrentDatabaseReload[[:space:]]+no' /etc/clamav/clamd.conf 2>/dev/null"; then
    warn "clamd answers, but ConcurrentDatabaseReload is not 'no' on $SERVER.
    The default loads a SECOND copy of the signature database on every update,
    and freshclam checks 24 times a day.
    Fix: ./deploy/host-setup.sh"
    host_warnings=$((host_warnings + 1))
  else
    step "clamd answering, ConcurrentDatabaseReload no"
  fi
else
  warn "No malware scanner on $SERVER.
    CC_SCANNER_ADDRESS points at clamd's socket; without it every citizen
    attachment stays quarantined and is never served.
    Fix: ./deploy/host-setup.sh"
  host_warnings=$((host_warnings + 1))
fi

if (( host_warnings > 0 )); then
  warn "$host_warnings host precondition(s) unmet. Provisioning continues — none of
    these stops CityConnect starting, and all of them are fixable afterwards."
fi

remote "command -v mysql >/dev/null"   || die "mysql client not found on $SERVER"
remote "command -v certbot >/dev/null" || die "certbot not found on $SERVER"

# Both certificates are issued over HTTP-01, so BOTH hostnames must already
# resolve to this machine. certbot's failure for this is opaque; this one is
# not — and the admin hostname is the one most likely to be missing, because
# nothing else on this box has ever needed it.
server_ip="$(remote "curl -fsS -4 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print \$1}'" || true)"
for d in "$PORTAL_DOMAIN" "$ADMIN_DOMAIN"; do
  resolved="$(resolve_addr "$d" || true)"

  if [[ -z "$resolved" ]]; then
    # Nothing from the recursive resolver. Before refusing, ask the zone's own
    # nameservers: the question that matters is whether LET'S ENCRYPT can
    # resolve this name, and it resolves independently of whatever this
    # machine has cached. A record fixed minutes ago is correct at the
    # authority and wrong in a cache for as long as the OLD record's TTL —
    # Cloudflare held a stale answer for cityconnect-admin for twelve hours.
    # Refusing to provision over that would be the check being wrong, not DNS.
    auth="$(resolve_addr_authoritative "$d" || true)"

    if [[ -n "$auth" ]]; then
      warn "$d is correct at its authoritative nameservers ($auth), but this
    machine's resolver still has a stale answer cached:
        $(dig +short "$d" 2>/dev/null | tr '\n' ' ')
    Let's Encrypt resolves independently and will most likely succeed.
    If certbot does fail on this name, wait for the old TTL to expire and run
    this again — provisioning is idempotent and picks up where it stopped."
      resolved="$auth"
    else
      chain="$(dig +short "$d" 2>/dev/null | tr '\n' ' ' || true)"
      if [[ -n "$chain" ]]; then
        die "$d has DNS records but resolves to no address, at its own
nameservers as well as here.
The chain is: $chain
That usually means a CNAME pointing somewhere with no A record of its own — an
apex is the common mistake. Point it at a name that HAS an address:
    $d  CNAME  muni-demo.dev-pro.app.
which is what $PORTAL_DOMAIN does."
      fi
      die "$d does not resolve at all.
Add a DNS record pointing it at $server_ip. A CNAME to muni-demo.dev-pro.app is
what the other apps on this box use."
    fi
  fi

  if [[ -n "$server_ip" && "$resolved" != "$server_ip" ]]; then
    warn "$d resolves to $resolved but the server reports $server_ip.
    certbot will be the judge, but check this is not pointing at another host."
  else
    step "$d resolves to $resolved"
  fi
done

# ---------------------------------------------------------------------------
# 2. Render the configuration locally
# ---------------------------------------------------------------------------
log "Rendering templates"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
chmod 700 "$STAGE"

for pair in "portal:$PORTAL_DOMAIN" "admin:$ADMIN_DOMAIN"; do
  role="${pair%%:*}"; domain="${pair#*:}"
  render_template "$HERE/apache/cityconnect-http.conf.tmpl" "$STAGE/$SERVICE-$role-http.conf" \
    DOMAIN="$domain" SERVICE="$SERVICE"
  render_template "$HERE/apache/cityconnect-$role.conf.tmpl" "$STAGE/$SERVICE-$role.conf" \
    DOMAIN="$domain" APP_DIR="$APP_DIR" PORT="$PORT" SERVICE="$SERVICE"
done
render_template "$HERE/systemd/cityconnect.service.tmpl" "$STAGE/$SERVICE.service" \
  APP_DIR="$APP_DIR" SERVICE="$SERVICE" USER="$SVC_USER"
step "apache (x2 hosts) + systemd rendered"

# The env file and the signing key are created only when the server has none.
# Regenerating the form-token secret would refuse submissions mid-flight;
# regenerating the database password would lock the running service out of its
# own data.
DB_PASSWORD=""
CLIENT_KID=""
if [[ "$env_exists" -eq 0 ]]; then
  : "${CC_C2_CLIENT_ID:?set CC_C2_CLIENT_ID (the c2f_... OIDC client id) for a first provision}"
  : "${CC_C2_CLIENT_SECRET:?set CC_C2_CLIENT_SECRET (shown once when the client was created)}"
  : "${CC_BOOTSTRAP_ADMIN_SUBS:?set CC_BOOTSTRAP_ADMIN_SUBS (your C2 subject id) or nobody can sign in as an administrator}"

  # The Resend API key is optional. Without it CC_SMTP_HOST renders empty,
  # which is the app's documented inert state — messages queue in the outbox
  # rather than erroring. The alternative, a host with no credentials, would
  # have every send rejected at AUTH instead, which is worse: it looks like a
  # broken relay rather than one that was never configured.
  smtp_host=""
  if [[ -n "${CC_SMTP_PASSWORD:-}" ]]; then
    smtp_host="$SMTP_RELAY"
  else
    warn "CC_SMTP_PASSWORD is not set, so guest email is left switched off.
    A guest or anonymous reporter who leaves an address will never be told
    their report was received, and nothing will say so on screen. Set it to a
    Resend API key and re-run, or edit the env file on the server afterwards."
  fi

  DB_PASSWORD="$(secret 24)"
  CLIENT_KID="cityconnect-$(date +%Y-%m)"
  render_template "$HERE/cityconnect.env.tmpl" "$STAGE/$SERVICE.env" \
    APP_DIR="$APP_DIR" PORT="$PORT" \
    PORTAL_DOMAIN="$PORTAL_DOMAIN" ADMIN_DOMAIN="$ADMIN_DOMAIN" \
    DB_NAME="$DB_NAME" DB_USER="$DB_USER" DB_PASSWORD="$DB_PASSWORD" \
    C2_ORIGIN="$C2_ORIGIN" \
    C2_CLIENT_ID="$CC_C2_CLIENT_ID" C2_CLIENT_SECRET="$CC_C2_CLIENT_SECRET" \
    CLIENT_KID="$CLIENT_KID" \
    SMTP_HOST="$smtp_host" SMTP_PASSWORD="${CC_SMTP_PASSWORD:-}" \
    MAIL_FROM="$MAIL_FROM" \
    FORM_TOKEN_SECRET="$(secret 32)" \
    BOOTSTRAP_ADMIN_SUBS="$CC_BOOTSTRAP_ADMIN_SUBS"
  chmod 600 "$STAGE/$SERVICE.env"
  step "env file rendered with generated secrets"

  # Generated now and left unreferenced. /api/c2/jwks serves an empty key set
  # until the env file points at this, and pointing at it switches messaging
  # from HTTP Basic to signed assertions — which C2 rejects until an
  # administrator registers our JWKS URI. See the note in the env file.
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
    -out "$STAGE/client-signing.pem" 2>/dev/null
  chmod 600 "$STAGE/client-signing.pem"
  step "C2 client signing key generated (kid $CLIENT_KID, not yet enabled)"
fi

# ---------------------------------------------------------------------------
# 3. Build the remote script
# ---------------------------------------------------------------------------
# Everything that changes the server happens in one script, run once. Doing it
# as twenty separate ssh calls makes a partial failure much harder to reason
# about, and each call re-authenticates.
cat > "$STAGE/remote-provision.sh" <<REMOTE
#!/usr/bin/env bash
set -euo pipefail

APP_DIR='$APP_DIR'
SERVICE='$SERVICE'
SVC_USER='$SVC_USER'
PORTAL_DOMAIN='$PORTAL_DOMAIN'
ADMIN_DOMAIN='$ADMIN_DOMAIN'
PORT='$PORT'
DB_NAME='$DB_NAME'
DB_USER='$DB_USER'
DB_PASSWORD='$DB_PASSWORD'
CERT_EMAIL='$CERT_EMAIL'
ACME_WEBROOT='$ACME_WEBROOT'
SKIP_TLS='$SKIP_TLS'
ENV_EXISTS='$env_exists'
STAGE_DIR="\$(dirname "\$0")"

say() { printf '    %s\n' "\$*"; }

# ---- service account ----
if id -u "\$SVC_USER" >/dev/null 2>&1; then
  say "user \$SVC_USER exists"
else
  useradd --system --no-create-home --shell /usr/sbin/nologin "\$SVC_USER"
  say "created system user \$SVC_USER"
fi

# ---- directories ----
# portal/ and console/ are the two document roots, each replaced wholesale on
# a deploy. data/ holds citizen attachments, including the quarantine an
# upload sits in until clamd has judged it, and is the only writable path the
# unit allows. keys/ holds the C2 client signing key and is read-only to the
# service.
mkdir -p "\$APP_DIR/portal" "\$APP_DIR/console" "\$APP_DIR/data/attachments" "\$APP_DIR/keys"
chown -R "\$SVC_USER:\$SVC_USER" "\$APP_DIR/data"
chmod 750 "\$APP_DIR/data"
chmod 700 "\$APP_DIR/keys"
say "directories ready under \$APP_DIR"

# ---- database ----
# Least privilege: enough for GORM's AutoMigrate to manage its own schema, and
# nothing else. No global grants.
if [ "\$ENV_EXISTS" = "0" ]; then
  mysql <<SQL
CREATE DATABASE IF NOT EXISTS \\\`\$DB_NAME\\\`
  CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS '\$DB_USER'@'127.0.0.1' IDENTIFIED BY '\$DB_PASSWORD';
ALTER USER '\$DB_USER'@'127.0.0.1' IDENTIFIED BY '\$DB_PASSWORD';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX, REFERENCES, DROP
  ON \\\`\$DB_NAME\\\`.* TO '\$DB_USER'@'127.0.0.1';
FLUSH PRIVILEGES;
SQL
  say "database \$DB_NAME and user \$DB_USER ready"
else
  say "env file exists — leaving database credentials untouched"
fi

# ---- environment file and signing key ----
# Config is server state. A deploy ships code; it must never overwrite this.
if [ "\$ENV_EXISTS" = "0" ]; then
  install -o "\$SVC_USER" -g "\$SVC_USER" -m 600 "\$STAGE_DIR/\$SERVICE.env" "\$APP_DIR/\$SERVICE.env"
  install -o "\$SVC_USER" -g "\$SVC_USER" -m 400 "\$STAGE_DIR/client-signing.pem" "\$APP_DIR/keys/client-signing.pem"
  say "wrote \$APP_DIR/\$SERVICE.env (0600) and keys/client-signing.pem (0400)"
else
  say "kept existing \$APP_DIR/\$SERVICE.env"
fi

# ---- TLS certificates, one per hostname ----
for pair in "portal:\$PORTAL_DOMAIN" "admin:\$ADMIN_DOMAIN"; do
  role="\${pair%%:*}"; d="\${pair#*:}"

  if [ -d "/etc/letsencrypt/live/\$d" ]; then
    say "certificate for \$d already exists"
    continue
  fi
  if [ "\$SKIP_TLS" = "1" ]; then
    say "--skip-tls: certificate for \$d NOT issued. Run:"
    say "    certbot certonly --webroot -w \$ACME_WEBROOT -d \$d"
    continue
  fi

  # The real vhost cannot load without a certificate, and certbot cannot get a
  # certificate without a vhost answering on :80. The bootstrap vhost breaks
  # that circle: it serves the ACME challenge and 404s everything else, so the
  # hostname never briefly serves another site's content.
  install -m 644 "\$STAGE_DIR/\$SERVICE-\$role-http.conf" "/etc/apache2/sites-available/\$SERVICE-\$role-http.conf"
  a2ensite "\$SERVICE-\$role-http" >/dev/null
  apache2ctl configtest || { a2dissite "\$SERVICE-\$role-http" >/dev/null; exit 1; }
  systemctl reload apache2
  say "bootstrap HTTP vhost enabled for \$d"

  certbot certonly --webroot -w "\$ACME_WEBROOT" -d "\$d" \
    --non-interactive --agree-tos -m "\$CERT_EMAIL"
  say "certificate issued for \$d"

  a2dissite "\$SERVICE-\$role-http" >/dev/null
  rm -f "/etc/apache2/sites-available/\$SERVICE-\$role-http.conf"
done

# ---- apache vhosts ----
for pair in "portal:\$PORTAL_DOMAIN" "admin:\$ADMIN_DOMAIN"; do
  role="\${pair%%:*}"; d="\${pair#*:}"
  conf="/etc/apache2/sites-available/\$SERVICE-\$role.conf"

  if [ ! -d "/etc/letsencrypt/live/\$d" ]; then
    say "no certificate for \$d — TLS vhost not installed"
    continue
  fi

  # Back up whatever is there so a bad configtest can be put back exactly.
  if [ -f "\$conf" ]; then
    cp "\$conf" "\$conf.bak"
  fi
  install -m 644 "\$STAGE_DIR/\$SERVICE-\$role.conf" "\$conf"
  a2ensite "\$SERVICE-\$role" >/dev/null

  if apache2ctl configtest; then
    systemctl reload apache2
    say "\$role vhost installed for \$d and apache reloaded"
    rm -f "\$conf.bak"
  else
    # Leave the RUNNING config untouched: apache has not been reloaded, so C2,
    # parking and facility-booking are unaffected either way.
    if [ -f "\$conf.bak" ]; then
      mv "\$conf.bak" "\$conf"
    else
      a2dissite "\$SERVICE-\$role" >/dev/null
      rm -f "\$conf"
    fi
    echo "apache configtest FAILED for \$role — config rolled back, apache not reloaded" >&2
    exit 1
  fi
done

# ---- systemd unit ----
install -m 644 "\$STAGE_DIR/\$SERVICE.service" "/etc/systemd/system/\$SERVICE.service"
systemctl daemon-reload
systemctl enable "\$SERVICE" >/dev/null 2>&1 || true
say "systemd unit installed and enabled"

# Start only if a binary is actually there. On a first provision it is not, and
# starting would produce a crash loop that looks like a configuration fault.
if [ -x "\$APP_DIR/\$SERVICE" ]; then
  systemctl restart "\$SERVICE"
  sleep 2
  if systemctl is-active --quiet "\$SERVICE" && curl -fsS "http://127.0.0.1:\$PORT/healthz" >/dev/null; then
    say "service is active and healthy on 127.0.0.1:\$PORT"
  else
    echo "service did not come up healthy — journalctl -u \$SERVICE -n 50" >&2
    exit 1
  fi
else
  say "no binary at \$APP_DIR/\$SERVICE yet — run deploy/deploy.sh to ship one"
fi
REMOTE
chmod 700 "$STAGE/remote-provision.sh"

if [[ "$DRY_RUN" -eq 1 ]]; then
  log "--dry-run: the remote script below would run on $SERVER. Nothing was changed."
  sed 's/^/    /' "$STAGE/remote-provision.sh"
  exit 0
fi

# ---------------------------------------------------------------------------
# 4. Ship and run
# ---------------------------------------------------------------------------
# /root is mode 700, so the staged env file and private key are never
# world-readable even for the moment they sit there.
log "Provisioning $SERVER"
REMOTE_STAGE="/root/.provision-$SERVICE.$$"
remote "mkdir -p '$REMOTE_STAGE' && chmod 700 '$REMOTE_STAGE'"
# shellcheck disable=SC2086
scp -q "$STAGE"/* "$SERVER:$REMOTE_STAGE/"
remote "chmod 600 '$REMOTE_STAGE/$SERVICE.env' '$REMOTE_STAGE/client-signing.pem' 2>/dev/null || true"

set +e
remote "bash '$REMOTE_STAGE/remote-provision.sh'"
rc=$?
set -e
remote "rm -rf '$REMOTE_STAGE'"
[[ $rc -eq 0 ]] || die "provisioning failed (exit $rc)"

log "Provisioned https://$PORTAL_DOMAIN/ and https://$ADMIN_DOMAIN/"
cat <<EOF

Next:
  1. ./deploy/deploy.sh          ship the API, ccadm and both SPAs
  2. If any host precondition warned above, run ./deploy/host-setup.sh — it is
     idempotent and fixes swap, clamd and the Apache modules in one pass.
  3. After the first guest submission, check nothing was silently suppressed —
     an unverified Resend sender is a permanent 5xx, which suppresses the
     address rather than retrying it:
       ssh $SERVER 'journalctl -u $SERVICE | grep -i "bounce\|suppress"'

Mail leaves as $MAIL_FROM. C2 delivers to consented citizens itself; this
relay is only for guests and anonymous reporters, whom C2 cannot address.

Config lives at $APP_DIR/$SERVICE.env and is never touched by a deploy.
EOF
