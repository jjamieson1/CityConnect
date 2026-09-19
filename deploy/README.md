# Deploying CityConnect to muni-demo (QA)

Ships CityConnect to **two hostnames** on the `muni-demo` QA server, behind
Apache + Let's Encrypt, integrated with the C2 instance running on the same box.

| | |
|---|---|
| Citizen portal | `https://cityconnect.dev-pro.app/` |
| Staff console | `https://cityconnect-admin.dev-pro.app/` |

`provision.sh` sets the server up once. `deploy.sh` ships code, every time.

## Why two hostnames

Not decoration. A separate origin means a separate cookie jar, so script running
on the public portal has no ambient authority over a staff session in the same
browser. Serving the console at `/console` on the portal's host would keep the
bundles apart and leave that risk exactly where it was.

The portal hostname is also **the only surface C2 talks to**. C2 is an external
system: it cannot reach the API's port, and it has no business reaching the
console's host. Every inbound C2 endpoint is published on the portal vhost and
nowhere else.

Both vhosts proxy the same API on `127.0.0.1:8095`. The portal vhost
**default-denies `/api`** and re-allows exactly three groups — the portal's own
API, the service-card callout, and C2's logout and JWKS endpoints. That rule is
only meaningful because `CC_ADDR` binds to loopback: an API on a public
interface is reachable around Apache entirely. **If you change `CC_ADDR` you are
changing the security model.**

`deploy.sh` asserts the rule on every run — it expects `403` from
`https://cityconnect.dev-pro.app/api/requests` and warns loudly on anything else.

## The server

`muni-demo` is an ssh alias that connects as root. It is a **shared host**: C2,
Parking Pleasure, facility-booking and the audit service run here too, so
everything in this directory is additive and every Apache change runs
`configtest` before a reload — a syntax error would take those services down as
well, not just this one.

| Path | Purpose |
|---|---|
| `/app/cityconnect/cityconnect` | the Go API binary |
| `/app/cityconnect/ccadm` | the admin CLI (`migrate`, `seed`, `grant-role`, `check-c2`) |
| `/app/cityconnect/cityconnect.env` | configuration + secrets (0600, `cityconnect`) |
| `/app/cityconnect/portal/` | the built citizen SPA |
| `/app/cityconnect/console/` | the built staff SPA |
| `/app/cityconnect/data/attachments/` | citizen uploads, including `quarantine/` — the only writable path |
| `/app/cityconnect/keys/` | the C2 client signing key (0400, not yet enabled) |
| `/etc/systemd/system/cityconnect.service` | the unit |
| `/etc/apache2/sites-available/cityconnect-{portal,admin}.conf` | the vhosts |
| `/swapfile`, `/etc/clamav/clamd.conf` | host-level, owned by `host-setup.sh` and shared with every app on the box |

The API listens on **127.0.0.1:8095**. Neighbours: 8090 audit, 8092 c2-api,
8093 parking, 8094 facility-booking.

This follows facility-booking's layout rather than this repo's older
`deployment/` directory: the box is Ubuntu with apache2, not RHEL with httpd,
and the apps live under `/app` rather than `/var/www`. `deployment/` is still
the reference for a generic install and is what `docs/runbook.md` describes;
nothing here replaces it.

## A new host

Three scripts, in order. Each one is idempotent, so re-running any of them on a
host that is already right changes nothing.

```bash
./deploy/host-setup.sh      # once per HOST   — swap, clamd, apache modules
./deploy/provision.sh       # once per APP    — user, dirs, db, certs, vhosts
./deploy/deploy.sh          # every time      — build and ship
```

`host-setup.sh` is deliberately separate. Everything in it belongs to the
**machine**, not to CityConnect: clamd serves any application on the box that
wants file scanning, and swap belongs to the host. Folding it into
`provision.sh` would mean facility-booking needs its own copy of the same
steps, and two scripts editing `/etc/clamav/clamd.conf` is the shared-host
hazard the rest of this directory is careful to avoid.

`provision.sh` **re-checks** all of it and warns — by name, with what will
happen — rather than refusing. Skipping `host-setup.sh` does not fail; it
produces a host that works until the first signature update or the first
citizen attachment, and then does not.

### What host-setup.sh does, and why

All three were learned the hard way on muni-demo — a 2GB droplet already
running C2, MySQL, parking, facility-booking and the audit service.

| Step | Why |
|---|---|
| **2GB swapfile** (`SWAP_GB=` to change) | The box shipped with none. clamd holds ~960MB resident and MySQL ~490MB, which left **97MB available** with CityConnect not yet deployed. No swap means no margin: the first spike kills a process instead of paging. |
| **`ConcurrentDatabaseReload no`** | ClamAV defaults it to **yes**, loading a *second* copy of the signature database during an update while the first still serves. freshclam checks **24 times a day**. On a box with 97MB free that is an hourly invitation to the OOM killer — whose biggest targets after clamd are **MySQL and C2**, so the blast radius is every other service on the host. |
| **freshclam before clamd's first start** | A fresh install has no database, and `clamav-daemon` fails to start until one exists with an error that does not say so. Ordering it turns a puzzling failure into a wait. |
| **Apache modules** | `proxy proxy_http headers rewrite ssl deflate`. `provision.sh` refuses without them; enabling here means a new host does not fail at the first vhost install. |

It ends by printing memory and the top processes, because on a host this size
that is the number worth looking at before going further.

`--dry-run` prints the remote script without touching anything.
`--skip-scanner` leaves clamd alone.

**muni-demo is already in this state** — swap active and in `/etc/fstab`,
`ConcurrentDatabaseReload no`, clamd answering PING on
`/var/run/clamav/clamd.ctl`. Running the script there is a no-op.

## First time

**DNS first.** `cityconnect.dev-pro.app` already resolves to this box.
`cityconnect-admin.dev-pro.app` does not — add a CNAME to
`muni-demo.dev-pro.app` and let it propagate. `provision.sh` refuses to start
without it rather than letting certbot fail opaquely half way through.

```bash
CC_C2_CLIENT_ID=c2f_f7c68360c266452a1ec18f02bd8fafdc \
CC_C2_CLIENT_SECRET=... \
CC_BOOTSTRAP_ADMIN_SUBS=<your C2 subject id> \
  ./deploy/provision.sh
./deploy/deploy.sh
```

`provision.sh` creates the `cityconnect` system user, the directory tree, the
database and a least-privilege user, the env file with generated secrets, the
C2 signing key, a certificate for **each** hostname (via a throwaway HTTP vhost
that serves only the ACME challenge), both vhosts, and the systemd unit —
enabled, and started as soon as a binary exists. It is idempotent; `--dry-run`
prints the remote script without touching anything.

The three values are needed on the **first** run only. After that the env file
exists and is left alone.

`CC_BOOTSTRAP_ADMIN_SUBS` is the **day-one blocker**: C2 SSO is the only staff
login, so nobody can grant the first role through the console. It is a C2
subject identifier, not an email address — read it from `sub` in your own
id_token, or run `ccadm grant-role` on the server afterwards.

## Every deploy

```bash
./deploy/deploy.sh                 # gate, build, ship, restart, health-check
./deploy/deploy.sh --with-vhost    # also reinstall both Apache vhosts
./deploy/deploy.sh --skip-build    # re-ship the last build
./deploy/deploy.sh --skip-tests    # emergencies only; announces itself
```

The gate is `go build`, `go vet`, the full Go suite, and a typecheck of each
SPA. CI runs the same checks on a pull request — but this server exists to try
branches that have not been through CI, so the gate runs here too rather than
trusting that it happened somewhere. The Go suite needs no database;
`internal/storetest` gives every test its own SQLite schema.

**Any branch may ship to QA.** A dirty or non-main tree warns loudly and names
the commit it does not match, rather than refusing: trying a branch is what this
server is for, but the binary then corresponds to nothing reviewable and you
should know that. Production would refuse.

## C2 registration

Done through the `dev-app-builder` MCP against the C2 on this box, in the
existing **City of Dev-Pro** organization.

| Object | Id |
|---|---|
| Consent policy | `da98d27e-c2e0-4def-a015-8585b2ddcc73` |
| Application | `94be9ee0-179c-443d-a4dc-c54d252fb621` |
| OIDC client | `c2f_f7c68360c266452a1ec18f02bd8fafdc` (UUID `805b5a0c-4a21-4899-aba0-fceb8179aa10`) |
| Service card | `cdd030b1-9b0f-451b-9b64-f7b5be548975` — "Report a problem" |

Two redirect URIs, one per origin, both on the same client. PKCE is required.
The client is confidential because the same credentials authenticate the partner
notifications API.

The **application** id — not the OIDC client id — is the `service_provider_id`
for anything billed through C2's payment broker.

Service card callout:
`https://cityconnect.dev-pro.app/api/citizens/{sub}/status`, `signed_jwt`.
Unlike when facility-booking was set up, the MCP now sets this itself
(`create_service_card` takes `calloutUrl`, and `set_service_card_callout`
changes it later) — no C2 admin needed.

### One thing that does not match, and is not yet explained

`get_application` reports **`hasActivePolicy: false`** for CityConnect, while
Parking Pleasure and Rivermont Spaces both report `true` — with policies created
the same way, through the same MCP, in the same organization. It had not changed
several minutes after creation, so it is not simply lag.

Against that, `preview_service_card_consent` resolves the policy chain correctly
and reports revision 1, and that preview *is* the citizen-facing consent path —
the one that gates both the service card and the partner notifications API. So
the likelihood is that the flag means something other than "a citizen can
consent to this".

**It is cheap to settle and worth settling before a rehearsal:** connect the app
as a test citizen on the demo box. If the consent screen appears and the card
fills with that citizen's requests, the flag is a red herring. If consent is
refused, or notifications come back `403`, this is why — and the next thing to
try is an APPLICATION-scoped binding via `attach_rule_to_policy`.

No rule has been created. Attaching one *gates* consent, so guessing at it could
turn a question into an outage.

Still true from facility-booking's notes:

- **`link_service_card_applications` returns an MCP protocol error** — the
  server replies with an empty content block — but the write lands. Verify with
  `preview_service_card_consent` rather than trusting the error.
- **Consent scopes are empty** on this C2, so `set_application_scopes` cannot be
  called and the card previews with none.

### Messaging authenticates with HTTP Basic, deliberately

`internal/c2/notify` uses signed `private_key_jwt` assertions when a key is
configured and falls back to HTTP Basic when one is not. The env file leaves it
unset, so messaging works the moment the app starts.

Switching to assertions is a **two-part change and doing half of it breaks
messaging**, because `/api/c2/jwks` serves an empty key set until the app is
pointed at a key:

1. Uncomment `CC_C2_CLIENT_PRIVATE_KEY_FILE` and `CC_C2_CLIENT_KID`, restart.
   The public key is now published.
2. Have the C2 administrator register the JWKS URI
   `https://cityconnect.dev-pro.app/api/c2/jwks` on the client above.

Between those two steps every notification is rejected. Do them together. The
key is already generated and waiting at `/app/cityconnect/keys/client-signing.pem`.

### Mail: two paths, one provider

C2's messaging API is addressed by **subject id** — `404` on an unknown one,
`403` without active consent. You never hand it an email address. So it cannot
carry a guest, and CityConnect's two transports are not interchangeable:

| Recipient | Transport | Delivery |
|---|---|---|
| C2 account + active consent | `TransportC2` → partner API | C2's own Resend integration |
| Guest / anonymous who left an address | `TransportEmail` → SMTP | Resend, direct from the app |

The second row is most of the demo, since the whole Sprint 1 front door is
built for people who never make an account. Pointing it at Resend's own SMTP
interface means both paths leave the same provider on the same verified domain.

```
CC_SMTP_HOST=smtp.resend.com
CC_SMTP_PORT=587
CC_SMTP_STARTTLS=true
CC_SMTP_USERNAME=resend        # the literal string
CC_SMTP_PASSWORD=re_…          # a Resend API key, passed as CC_SMTP_PASSWORD to provision.sh
CC_SMTP_FROM=jamie@dev-pro.app
```

**Resend verification is exact, and getting it wrong is silent.** `dev-pro.app`
is verified; `cityconnect.dev-pro.app` is **not**, so the sender is deliberately
not derived from the portal hostname. Resend refuses an unverified sender with a
5xx, `internal/mailer` classifies that as permanent, and the recipient is
suppressed as `bounced` and never retried — one bad value quietly poisons every
guest address it touches instead of erroring once.

After the first guest submission:

```bash
ssh muni-demo 'journalctl -u cityconnect | grep -i "bounce\|suppress"'
```

`CC_SMTP_PASSWORD` is optional at provision time. Without it `CC_SMTP_HOST`
renders empty, which is the app's documented inert state — messages queue in
the outbox. A host with no credentials would instead fail at AUTH on every
send, which looks like a broken relay rather than one nobody configured.

Replies to `jamie@dev-pro.app` go to a mailbox, not onto the request:
CityConnect does not ingest inbound mail. Residents reply through the portal.

### Payments

The application is **registered** as billing-capable — that is all three of the
payment broker's prerequisites met (a federation client bound to the
application, client authentication, and a consent policy). **Nothing in
CityConnect calls it.** There is no invoice model and no settlement callback;
see CIT-51. The consent policy deliberately says nothing about payments, because
a notice describing collection that does not happen is worse than no notice.

## Notes

- **Config is server state.** The env file is never overwritten by a deploy or a
  re-provision. Change it on the server and `systemctl restart cityconnect`.
- **Guest email needs `CC_SMTP_PASSWORD` at provision time.** Without it the
  relay is left off and messages queue in the outbox for ever, with nothing on
  screen to say so. `docs/runbook.md` has the full table of settings that fail
  this quietly.
- **The scanner is a unix socket**, `/var/run/clamav/clamd.ctl`, mode 0666 — so
  the service account needs no group membership and `ProtectSystem=strict` does
  not get in the way. `host-setup.sh` puts it there; `provision.sh` checks it
  answers.
- **Rollback:** each deploy keeps the previous API binary as
  `cityconnect.prev`. `mv cityconnect.prev cityconnect && systemctl restart cityconnect`.
- **Logs:** `journalctl -u cityconnect -f`.
- **Certificate renewal** is certbot's own timer, webroot `/var/www/html`. Both
  SPA syncs exclude `.well-known/` so a deploy cannot break it.
- **Database:** MySQL 8 shared with C2, parking and facility-booking.
  `CC_DB_AUTOMIGRATE=true` here so the schema follows whatever branch is
  deployed, which is the point of this box. Production runs versioned
  migrations with it off.
- **Reset the demo data:** `ccadm` on the server; `seed` is idempotent and
  repopulates baseline configuration without touching an operator's edits.
