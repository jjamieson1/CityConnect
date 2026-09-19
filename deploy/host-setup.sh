#!/usr/bin/env bash
#
# host-setup.sh — prepare a host to run CityConnect (and its neighbours).
#
# Everything here is HOST-level, not application-level: swap, the malware
# scanner, and the Apache modules. None of it is CityConnect's alone — clamd
# serves any application on the box that wants file scanning, and swap belongs
# to the machine. That is why it is a separate script from provision.sh, which
# owns only what lives under /app/cityconnect.
#
# Run it once per new host, before deploy/provision.sh. It is idempotent: every
# step checks first and re-running changes nothing that is already right, so it
# is also the way to bring an existing host up to standard.
#
# Usage:
#   ./deploy/host-setup.sh
#   ./deploy/host-setup.sh --dry-run       # print the remote script, change nothing
#   SWAP_GB=4 ./deploy/host-setup.sh       # larger swapfile
#   ./deploy/host-setup.sh --skip-scanner  # leave clamd alone
#
# WHY EACH STEP EXISTS — all three were learned on muni-demo, which is a 2GB
# droplet shared by C2, MySQL, parking, facility-booking and the audit service:
#
#   Swap. The box shipped with none. clamd holds its whole signature database
#   resident (~960MB), MySQL another ~490MB, and that left 97MB available with
#   CityConnect not yet deployed. A box with no swap has no margin at all: the
#   first spike kills a process rather than paging.
#
#   ConcurrentDatabaseReload. ClamAV defaults this to YES, which loads a SECOND
#   copy of the signature database during an update while the first is still
#   serving. freshclam checks 24 times a day. On a box with 97MB free that is
#   an hourly invitation to the OOM killer — and its biggest targets after
#   clamd are MySQL and C2, so the blast radius is every other service on the
#   host, not the one that caused it.
#
#   freshclam before clamd. A fresh install has no signature database, and
#   clamav-daemon fails to start until one exists, with an error that does not
#   say so. Ordering the first run explicitly turns a puzzling failure into a
#   wait.
#
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "$HERE/lib/common.sh"

SERVER="${SERVER:-muni-demo}"
SWAP_GB="${SWAP_GB:-2}"
SWAPFILE="${SWAPFILE:-/swapfile}"

DRY_RUN=0
SKIP_SCANNER=0
for arg in "$@"; do
  case "$arg" in
    --dry-run)       DRY_RUN=1 ;;
    --skip-scanner)  SKIP_SCANNER=1 ;;
    -h|--help)       grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option: $arg" ;;
  esac
done

require_cmd ssh

# ---------------------------------------------------------------------------
# Recon — read-only
# ---------------------------------------------------------------------------
log "Recon on $SERVER (read-only)"
remote "test -d /etc/apache2" || die "no /etc/apache2 on $SERVER — this script targets Ubuntu + apache2"
remote "command -v apt-get >/dev/null" || die "no apt-get on $SERVER"
step "$(remote "free -m | awk '/^Mem:/ {printf \"%dMB total, %dMB available\", \$2, \$7}'")"
step "$(remote "swapon --show=SIZE --noheadings 2>/dev/null | tr '\n' ' '" || true)swap configured"

# ---------------------------------------------------------------------------
# The remote script
# ---------------------------------------------------------------------------
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

cat > "$STAGE/remote-host-setup.sh" <<REMOTE
#!/usr/bin/env bash
set -euo pipefail

SWAP_GB='$SWAP_GB'
SWAPFILE='$SWAPFILE'
SKIP_SCANNER='$SKIP_SCANNER'

say()  { printf '    %s\n' "\$*"; }
warn() { printf '!!  %s\n' "\$*" >&2; }

# ---------------------------------------------------------------------------
# 1. Swap
# ---------------------------------------------------------------------------
if [ -n "\$(swapon --show --noheadings 2>/dev/null)" ]; then
  say "swap already active: \$(swapon --show=NAME,SIZE --noheadings | tr '\n' ' ')"
else
  if [ ! -e "\$SWAPFILE" ]; then
    # fallocate is instant on ext4. dd is the fallback for a filesystem where a
    # fallocated file cannot be used as swap (btrfs, some overlay setups) —
    # slower, but it always produces a usable file.
    if ! fallocate -l "\${SWAP_GB}G" "\$SWAPFILE" 2>/dev/null; then
      say "fallocate unavailable here; writing \${SWAP_GB}G with dd (slower)"
      dd if=/dev/zero of="\$SWAPFILE" bs=1M count="\$((SWAP_GB * 1024))" status=none
    fi
    chmod 600 "\$SWAPFILE"
    mkswap "\$SWAPFILE" >/dev/null
    say "created \$SWAPFILE (\${SWAP_GB}G)"
  else
    chmod 600 "\$SWAPFILE"
    say "\$SWAPFILE already exists; reusing it"
  fi

  swapon "\$SWAPFILE"
  say "swap on"
fi

# Separately from activating it: survive a reboot. Checked on its own so a host
# where somebody ran swapon by hand still gets the fstab line.
if grep -qE "^[[:space:]]*\$SWAPFILE[[:space:]]" /etc/fstab; then
  say "fstab entry already present"
else
  printf '%s none swap sw 0 0\n' "\$SWAPFILE" >> /etc/fstab
  say "added \$SWAPFILE to /etc/fstab"
fi

# ---------------------------------------------------------------------------
# 2. Malware scanner
# ---------------------------------------------------------------------------
if [ "\$SKIP_SCANNER" = "1" ]; then
  say "--skip-scanner: leaving clamd alone"
else
  if ! command -v clamd >/dev/null 2>&1 && ! dpkg -s clamav-daemon >/dev/null 2>&1; then
    say "installing clamav-daemon (this pulls ~100MB of signatures)"
    DEBIAN_FRONTEND=noninteractive apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq clamav-daemon clamav-freshclam
    installed=1
  else
    say "clamav-daemon already installed"
    installed=0
  fi

  # ConcurrentDatabaseReload. Written idempotently: appending blindly would
  # leave two directives in the file on a second run, and clamd's behaviour
  # with a duplicate is not something to find out during a demo.
  conf=/etc/clamav/clamd.conf
  changed=0
  if grep -qE '^[[:space:]]*ConcurrentDatabaseReload' "\$conf"; then
    if ! grep -qiE '^[[:space:]]*ConcurrentDatabaseReload[[:space:]]+no[[:space:]]*\$' "\$conf"; then
      sed -i 's/^[[:space:]]*ConcurrentDatabaseReload.*/ConcurrentDatabaseReload no/' "\$conf"
      changed=1
    fi
  else
    printf '\n# Set by deploy/host-setup.sh: the default (yes) loads a second copy of the\n# signature database on every update, which this host does not have room for.\nConcurrentDatabaseReload no\n' >> "\$conf"
    changed=1
  fi
  [ "\$changed" = "1" ] && say "set ConcurrentDatabaseReload no" || say "ConcurrentDatabaseReload already no"

  # First signature download, before clamd is asked to start. A fresh install
  # has no database and the daemon's failure for that does not say so.
  if [ ! -s /var/lib/clamav/main.cvd ] && [ ! -s /var/lib/clamav/main.cld ]; then
    say "no signature database yet — running freshclam (several minutes)"
    systemctl stop clamav-freshclam 2>/dev/null || true
    freshclam --quiet || warn "freshclam failed; clamd will not start until it succeeds"
    systemctl start clamav-freshclam 2>/dev/null || true
  fi

  systemctl enable clamav-daemon >/dev/null 2>&1 || true
  if [ "\$changed" = "1" ] || [ "\$installed" = "1" ] || ! systemctl is-active --quiet clamav-daemon; then
    systemctl restart clamav-daemon
    say "clamav-daemon restarted"
  fi

  # Prove it answers rather than assuming a started unit is a working one.
  for i in 1 2 3 4 5 6 7 8 9 10; do
    if clamdscan --ping 1 >/dev/null 2>&1; then
      say "clamd answers PING on /var/run/clamav/clamd.ctl"
      break
    fi
    [ "\$i" = "10" ] && warn "clamd is not answering: journalctl -u clamav-daemon -n 30"
    sleep 3
  done
fi

# ---------------------------------------------------------------------------
# 3. Apache modules
# ---------------------------------------------------------------------------
# provision.sh refuses to run without these. Enabling them here means a new
# host does not fail at the first vhost install.
need_reload=0
for m in proxy proxy_http headers rewrite ssl deflate; do
  if apache2ctl -M 2>/dev/null | grep -q "\${m}_module"; then
    continue
  fi
  a2enmod "\$m" >/dev/null
  need_reload=1
  say "enabled apache module \$m"
done
if [ "\$need_reload" = "1" ]; then
  # configtest first: this host runs C2, parking and facility-booking, and a
  # reload on a broken config takes all of them down.
  apache2ctl configtest
  systemctl reload apache2
  say "apache reloaded"
else
  say "apache modules already enabled"
fi

# ---------------------------------------------------------------------------
# 4. What it looks like now
# ---------------------------------------------------------------------------
printf '\n'
free -h | head -3
printf '\n'
ps -eo rss,comm --sort=-rss | sed -n '2,6p' | awk '{printf "    %8.1f MB  %s\n", \$1/1024, \$2}'
REMOTE
chmod 700 "$STAGE/remote-host-setup.sh"

if [[ "$DRY_RUN" -eq 1 ]]; then
  log "--dry-run: the remote script below would run on $SERVER. Nothing was changed."
  sed 's/^/    /' "$STAGE/remote-host-setup.sh"
  exit 0
fi

log "Preparing $SERVER"
REMOTE_STAGE="/root/.host-setup.$$"
remote "mkdir -p '$REMOTE_STAGE' && chmod 700 '$REMOTE_STAGE'"
scp -q "$STAGE/remote-host-setup.sh" "$SERVER:$REMOTE_STAGE/"

set +e
remote "bash '$REMOTE_STAGE/remote-host-setup.sh'"
rc=$?
set -e
remote "rm -rf '$REMOTE_STAGE'"
[[ $rc -eq 0 ]] || die "host setup failed (exit $rc)"

log "Host ready. Next: ./deploy/provision.sh"
