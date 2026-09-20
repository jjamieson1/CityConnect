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
#   ./deploy/host-setup.sh --minimal-signatures   # see below
#   ./deploy/host-setup.sh --full-signatures      # undo it
#
# SIGNATURE SET. clamd holds its whole database resident: ~960MB for the full
# set, measured on muni-demo, on a host with 1.9GB. --minimal-signatures
# replaces it with a database containing one signature — EICAR, the standard
# antivirus test file — which costs about 22MB.
#
# THAT IS NOT MALWARE PROTECTION. It detects exactly one thing, and that thing
# is a test file. What it preserves is the PIPELINE: an upload still lands in
# quarantine, is still streamed to clamd, is still promoted only on a clean
# verdict, and an EICAR upload is still rejected. On a demo box that is the
# trade — the machinery is real and demonstrable, the signature set is not.
# Never do this anywhere a real citizen uploads a real file.
#
# --full-signatures puts it back.
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
SIG_MODE=""
for arg in "$@"; do
  case "$arg" in
    --dry-run)             DRY_RUN=1 ;;
    --skip-scanner)        SKIP_SCANNER=1 ;;
    --minimal-signatures)  SIG_MODE=minimal ;;
    --full-signatures)     SIG_MODE=full ;;
    -h|--help)             grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option: $arg" ;;
  esac
done

if [[ -n "$SIG_MODE" && "$SKIP_SCANNER" -eq 1 ]]; then
  die "--skip-scanner and --${SIG_MODE}-signatures contradict each other:
one says leave clamd alone, the other says change which database it loads."
fi

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
SIG_MODE='$SIG_MODE'
# EICAR, the standard antivirus test file, as a body signature matching
# anywhere in a file of any type. 68 bytes:
#   X5O!P%@AP[4\\PZX54(P^)7CC)7}\$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!\$H+H*
EICAR_HEX='58354f2150254041505b345c505a58353428505e2937434329377d2445494341522d5354414e444152442d414e544956495255532d544553542d46494c452124482b482a'

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
# 2b. Signature set
# ---------------------------------------------------------------------------
# Only when asked. Left alone, clamd keeps whatever database it already has.
#
# The database stays at /var/lib/clamav and the big files are moved OUT of it,
# rather than pointing clamd at a new directory. AppArmor confines clamd to
# exactly this path:
#
#     /var/lib/clamav/   r,
#     /var/lib/clamav/** krw,
#
# so a DatabaseDirectory anywhere else is denied at open() — clamd logs
# "Can't open directory", fails to start, and systemd gives up after five
# tries. The directory's own ownership and mode look perfectly correct while
# this happens, which makes it a genuinely confusing half hour.
if [ -n "\$SIG_MODE" ]; then
  db=/var/lib/clamav
  stash=/var/lib/clamav-full

  # Ubuntu's unit carries two of these:
  #     ConditionPathExistsGlob=/var/lib/clamav/main.{c[vl]d,inc}
  #     ConditionPathExistsGlob=/var/lib/clamav/daily.{c[vl]d,inc}
  # Move either file and systemd SKIPS the service — not a failure, a skip,
  # logged as "unmet condition check", leaving systemctl is-active saying
  # inactive with nothing that looks like an error. A drop-in resets them and
  # requires our own database instead.
  dropin=/etc/systemd/system/clamav-daemon.service.d
  if [ "\$SIG_MODE" = "minimal" ]; then
    mkdir -p "\$dropin"
    printf '%s\\n' \\
      '# Written by deploy/host-setup.sh --minimal-signatures.' \\
      '# The stock unit refuses to start without main.* and daily.*, which is' \\
      '# exactly what this mode removes. An empty assignment resets the list.' \\
      '[Unit]' \\
      'ConditionPathExistsGlob=' \\
      'ConditionPathExistsGlob=/var/lib/clamav/eicar.ndb' \\
      > "\$dropin/minimal-signatures.conf"
    systemctl daemon-reload
    say "drop-in installed: unit now requires eicar.ndb instead of main/daily"

    mkdir -p "\$stash"
    moved=0
    for f in "\$db"/*.cvd "\$db"/*.cld; do
      [ -e "\$f" ] || continue
      mv "\$f" "\$stash"/
      moved=\$((moved + 1))
    done
    printf 'Eicar-Test-Signature:0:*:%s\\n' "\$EICAR_HEX" > "\$db/eicar.ndb"
    chown clamav:clamav "\$db/eicar.ndb"
    chmod 644 "\$db/eicar.ndb"
    say "moved \$moved signature file(s) to \$stash and wrote a one-signature database"

    # freshclam downloads into \$db. Left running it would put the full set
    # back within the hour and quietly undo this.
    systemctl disable --now clamav-freshclam >/dev/null 2>&1 || true
    say "freshclam stopped: it would re-download what we just moved"
  else
    rm -f "\$dropin/minimal-signatures.conf"
    rmdir "\$dropin" 2>/dev/null || true
    systemctl daemon-reload
    say "drop-in removed: the stock start conditions are back"

    rm -f "\$db/eicar.ndb"
    restored=0
    if [ -d "\$stash" ]; then
      for f in "\$stash"/*.cvd "\$stash"/*.cld; do
        [ -e "\$f" ] || continue
        mv "\$f" "\$db"/
        restored=\$((restored + 1))
      done
      rmdir "\$stash" 2>/dev/null || true
    fi
    chown -R clamav:clamav "\$db"
    say "restored \$restored signature file(s) to \$db"

    if [ ! -s "\$db/main.cvd" ] && [ ! -s "\$db/main.cld" ]; then
      say "nothing to restore — downloading the full set (several minutes)"
      systemctl stop clamav-freshclam >/dev/null 2>&1 || true
      freshclam --quiet || warn "freshclam failed; clamd will not start"
    fi
    systemctl enable --now clamav-freshclam >/dev/null 2>&1 || true
    say "freshclam started"
  fi

  # Set explicitly to the one path AppArmor allows, in case an earlier run or
  # a hand edit pointed it somewhere clamd cannot reach.
  conf=/etc/clamav/clamd.conf
  if grep -qE '^[[:space:]]*DatabaseDirectory' "\$conf"; then
    sed -i "s|^[[:space:]]*DatabaseDirectory.*|DatabaseDirectory \$db|" "\$conf"
  else
    printf '\\nDatabaseDirectory %s\\n' "\$db" >> "\$conf"
  fi

  systemctl restart clamav-daemon

  ok=0
  for i in 1 2 3 4 5 6 7 8 9 10; do
    if clamdscan --ping 1 >/dev/null 2>&1; then ok=1; break; fi
    sleep 3
  done
  if [ "\$ok" != "1" ]; then
    echo "clamd did not come back: journalctl -u clamav-daemon -n 30" >&2
    echo "Restore with: ./deploy/host-setup.sh --full-signatures" >&2
    exit 1
  fi

  # Prove the VERDICTS, not just that the process answers. A database that
  # failed to load would leave clamd running and clearing every file, which is
  # the most dangerous way this could go wrong.
  #
  # Output is captured and then matched, never piped into grep: clamdscan
  # exits 1 when it FINDS something, and under "set -o pipefail" that makes
  # a "clamdscan | grep -q FOUND" pipeline fail on exactly the case it is
  # testing for. That reported a working scanner as broken.
  tmp="\$(mktemp -d)"
  printf '%s' "\$EICAR_HEX" | xxd -r -p > "\$tmp/eicar"
  printf 'an ordinary file\\n' > "\$tmp/clean"

  eicar_out="\$(clamdscan --fdpass --no-summary "\$tmp/eicar" 2>&1 || true)"
  clean_out="\$(clamdscan --fdpass --no-summary "\$tmp/clean" 2>&1 || true)"
  rm -rf "\$tmp"

  case "\$eicar_out" in
    *FOUND*) : ;;
    *) echo "clamd is running but did NOT detect EICAR — the database did not load." >&2
       echo "  clamdscan said: \$eicar_out" >&2
       echo "Every upload would be cleared. Restore with:" >&2
       echo "    ./deploy/host-setup.sh --full-signatures" >&2
       exit 1 ;;
  esac
  case "\$clean_out" in
    *FOUND*) echo "clamd flagged a clean file — the database is wrong: \$clean_out" >&2
             exit 1 ;;
  esac

  say "verified: EICAR detected, clean file passed"
  rss_kb="\$(ps -o rss= -C clamd 2>/dev/null | head -1 | tr -d ' ')"
  [ -n "\$rss_kb" ] && say "clamd resident: \$((rss_kb / 1024)) MB"

  if [ "\$SIG_MODE" = "minimal" ]; then
    warn "This host now detects EICAR AND NOTHING ELSE. The quarantine pipeline
    is real and demonstrable; the signature set is not protection. Do not point
    real citizens at it. Undo with --full-signatures."
  fi
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
