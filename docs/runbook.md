# CityConnect operations runbook

Everything an administrator needs on day one, and the procedures for when
something goes wrong.

---

## 1. What to ask the C2 administrator

This has the longest lead time and blocks staff sign-in entirely. Send it first.

| Ask for | Why it matters |
| --- | --- |
| **Portal origin** | The single public host serving the citizen portal. Everything derives from it. |
| **`client_id`** | The `aud` on every token we verify. |
| **`client_secret`** | Server-side client authentication (unless using private-key JWT only). |
| **Registered `redirect_uri`s** | Exact-match. Register *every* environment variant, including local dev. A trailing slash is a different URI. **Two per environment**: one for the staff console's origin and one for the citizen portal's, because they are separate hosts. |
| **Registered `post_logout_redirect_uri`s** | Optional. Ask for two per environment — the console's base path and the portal's root — and set them *only once registered*. Unset, sign-out still works and ends on C2's own page. |
| **Allowed scopes** | We request `openid profile email`. |
| **Callout URL + auth mode** | Give them `https://services.<host>/api/citizens/{sub}/status` and ask for **`signed_jwt`**. |
| **`backchannel_logout_uri`** | Register `https://services.<host>/api/c2/backchannel-logout`. Not advertised in discovery — it must be arranged deliberately. |
| **Our JWKS URL** | `https://services.<host>/api/c2/jwks`, so C2 can verify our notification client assertions. |
| **Partner notifications base URL** | Mounted at `<base>/partner`, a sibling of the OIDC endpoints — **not** under `/api`. |
| **Confirmation that staff can hold C2 identities** | C2 is a *citizen* identity provider. With SSO as our only staff login, this is load-bearing. Ask how staff accounts are provisioned. |

**Every URL you give C2 is on the citizen origin.** C2 is an external system:
it cannot reach the API's listening port, and in most deployments it cannot
reach the staff console's host either. The public origin is the only surface it
talks to, and Apache proxies from there to the API on loopback. Handing C2 an
address ending `:4021` works on a developer's laptop and nowhere else.

**Register test and conformance clients under a separate application.** An extra
client under ours can change which `client_id` C2 signs callouts with, producing
401s that look like a code bug.

---

## 2. First boot

```sh
# 1. Database
mysql -e "CREATE DATABASE cityconnect CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
mysql -e "CREATE USER 'cityconnect'@'localhost' IDENTIFIED BY '<REPLACE_ME>';"
mysql -e "GRANT ALL ON cityconnect.* TO 'cityconnect'@'localhost';"

# 2. Service account and directories
sudo useradd --system --home /opt/cityconnect --shell /usr/sbin/nologin cityconnect
sudo mkdir -p /opt/cityconnect/{bin,data/attachments,keys}
sudo chown -R cityconnect:cityconnect /opt/cityconnect/data

# 3. Configuration
sudo cp deployment/cityconnect.env.example /opt/cityconnect/cityconnect.env
sudo chown root:cityconnect /opt/cityconnect/cityconnect.env
sudo chmod 0640 /opt/cityconnect/cityconnect.env
sudo -e /opt/cityconnect/cityconnect.env     # fill in every <REPLACE_ME>

# 4. Signing key for notification client assertions
sudo openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
  -out /opt/cityconnect/keys/client-signing.pem
sudo chown root:cityconnect /opt/cityconnect/keys/client-signing.pem
sudo chmod 0640 /opt/cityconnect/keys/client-signing.pem

# 5. Service and proxy
sudo cp deployment/cityconnect-api.service /etc/systemd/system/
sudo cp deployment/apache-cityconnect.conf /etc/apache2/conf-available/
sudo cp deployment/apache-cityconnect-portal.conf /etc/apache2/sites-available/
sudo a2enmod proxy proxy_http headers rewrite deflate expires
sudo a2enconf apache-cityconnect
sudo a2ensite apache-cityconnect-portal   # the citizen portal's own vhost
sudo systemctl daemon-reload && sudo systemctl enable --now cityconnect-api
sudo systemctl reload apache2

# 6. Schema and baseline configuration
sudo -u cityconnect /opt/cityconnect/bin/ccadm migrate
sudo -u cityconnect /opt/cityconnect/bin/ccadm seed
```

### The first administrator

**This is the day-one blocker.** C2 SSO is the only staff login, so there is
nobody who can grant the first role through the console — a fresh deployment is
permanently locked out unless an administrator is created out of band.

Either set `CC_BOOTSTRAP_ADMIN_SUBS` in the environment file (applied on every
boot, idempotent), or:

```sh
sudo -u cityconnect /opt/cityconnect/bin/ccadm grant-role \
  --sub '<the C2 subject identifier>' \
  --email 'you@city.example' --name 'Your Name' --role admin
```

The subject identifier is opaque and comes from C2 — it is not an email address.
Ask the C2 administrator, or read it from `sub` in your own id_token.

### Verify before handing over

```sh
sudo -u cityconnect /opt/cityconnect/bin/ccadm check-c2   # resolves and prints every endpoint
curl -fsS localhost:4021/readyz | jq                       # database + C2 reachability
sudo -u cityconnect /opt/cityconnect/bin/ccadm list-users
```

**Confirm the API is not reachable except through Apache.** From another
machine:

```sh
curl --max-time 5 http://<server>:4021/healthz     # must fail to connect
curl -fsS https://services.<host>/api/portal/catalog | jq   # must succeed
```

The first must fail. `CC_ADDR` defaults to `127.0.0.1:4021` in production for
this reason, and the boot log carries a warning if the API is listening on all
interfaces. The console and the portal are separated by what each Apache vhost
is allowed to proxy; an API that answers on a public interface is reachable
around both, which puts the staff surface on the open network no matter what
the vhosts say.

---

## 3. Recovery

### Nobody can sign in

Staff sign-in has exactly one dependency, so work through it in order:

```sh
curl -fsS localhost:4021/readyz | jq '.c2'
sudo -u cityconnect /opt/cityconnect/bin/ccadm check-c2
```

| What you see | What it means |
| --- | --- |
| `discovery issuer … does not match configured issuer` | `CC_C2_ISSUER` is wrong. It is the **portal origin** plus `/oidc`, never C2's internal API host — discovery still resolves from there, which is what makes this confusing. |
| Connection refused or timeout | C2 is down or unreachable from this host. Sessions already open keep working for their remaining TTL; nobody new can sign in. There is no local fallback by design. |
| Discovery fine, but users get "no CityConnect access" | Their C2 identity has no CityConnect user. Deny-by-default is intentional: C2 authenticates *citizens*, so auto-provisioning would open the console to the public. Invite them by email, or `ccadm grant-role`. |
| Users are asked for their password every visit | Something is sending `prompt=login` or `max_age=0`. CityConnect never does — check whether a proxy or a C2-side client setting is adding it. |
| `502` from Apache, nothing in the API log | Apache cannot reach `127.0.0.1:4021`. Either the service is down (`systemctl status cityconnect-api`) or `CC_ADDR` was set to an address Apache is not proxying to. |

### `post_logout_redirect_uri invalid` on sign-out

C2 exact-matches this against its registration list, so it rejects any value
that was not registered — including a correct-looking one. **Unset the variable
and sign-out starts working immediately**, ending on C2's own signed-out page:

```sh
# in /opt/cityconnect/cityconnect.env
# CC_C2_POST_LOGOUT_REDIRECT_URL=...
# CC_C2_PORTAL_POST_LOGOUT_REDIRECT_URL=...
```

To land the browser back on CityConnect instead, have the C2 administrator
register these exact strings and then set the matching variable:

| Surface | Register and set |
| --- | --- |
| Staff console | `https://city.<host>/cityconnect/` → `CC_C2_POST_LOGOUT_REDIRECT_URL` |
| Citizen portal | `https://services.<host>/` → `CC_C2_PORTAL_POST_LOGOUT_REDIRECT_URL` |

**One per origin.** A citizen returned to the console gets a staff sign-in page
that refuses them, and C2 rejects the mismatched URI before that anyway. This
is why sign-out can break on a deployment where sign-in has worked for months:
the two are registered separately, and nothing exercises sign-out until
somebody uses it.

Either way the local session is revoked before the C2 hop, so a user whose
sign-out errors at C2 is still signed out of CityConnect.

### Locked out of the last administrator account

```sh
sudo -u cityconnect /opt/cityconnect/bin/ccadm grant-role --sub '<subject>' --role admin
sudo -u cityconnect /opt/cityconnect/bin/ccadm unlock --email 'someone@city.example'
```

The console refuses to remove the last active admin, so this should only be
needed if the account's C2 identity itself became unusable.

### Notifications are not arriving

Open **Admin → Delivery log**. The state says what happened:

| State | Meaning | Action |
| --- | --- | --- |
| `sent` | C2 accepted it. | Delivery beyond the in-app inbox depends on the citizen's own channel preferences. Nothing to fix here. |
| `suppressed` / no consent | The citizen has no active consent for CityConnect. | **Expected, not a fault.** Only they can restore it, in their C2 portal. Retrying is refused deliberately. |
| `suppressed` / unknown to C2 | C2 does not recognise the linked subject. | The identity link is stale. Check the contact's identities. |
| `suppressed` / no C2 account | The contact has never linked to C2. | Reach them by phone or post. |
| `failed` | A transport or credential problem. | Check `lastError`, fix, then retry from the log. |
| `pending` and `overdue` rising | The dispatcher is stuck or C2 is unreachable. | Check **Admin → Operations**, then `journalctl -u cityconnect-api`. |

### The Service Card shows nothing

C2 fails safe: on any error it silently renders the static card, so the citizen
sees no failure and neither do you. Reproduce it directly:

```sh
curl -i -H "Authorization: Bearer <assertion>" \
  https://services.<host>/api/citizens/<sub>/status
```

- `401` — the assertion failed verification. Almost always an `aud` mismatch:
  C2 is signing for a different `client_id` than the one configured here.
- Only "Browse all city services" appears, with no named shortcuts — every code
  in `CC_C2_CALLOUT_QUICK_LINKS` was skipped. They are checked against the live
  catalogue, so this means unknown, retired, or not publicly visible. The API
  log names each one; Admin → Catalogue shows what is available.
- `200 {}` — CityConnect does not know that subject. Correct behaviour for a
  citizen with no record; if it is wrong, their contact is missing its C2 identity.
- Slow — C2's budget is about five seconds and it calls on every render.
  `CC_C2_CALLOUT_CACHE_TTL` is what keeps that cheap.
- `400` mentioning *"sign-in response was missing its code or state"* — the
  callout URL has been set to `/api/auth/callback`. That is the redirect URI,
  a different field: it receives a browser after sign-in and knows nothing
  about status bundles.
- Nothing in the log at all — C2 cannot reach the host. Confirm the callout URL
  is on the public citizen origin and not the API port.

The card can also render correctly and still be broken: **check where its links
go.** Every link in a bundle — the card's own action and each task — must be on
the citizen origin, `https://services.<host>/requests/<reference>`. A citizen
has no staff console account, so a link to the console host ends at a sign-in
page that refuses them, on a surface the City does not control. Set
`CC_PORTAL_PUBLIC_URL`; it is what those links are built from.

### Suspected tampering

```sh
sudo -u cityconnect /opt/cityconnect/bin/ccadm verify-audit
```

Replays the hash chain and reports the first entry whose contents or link do not
match. A break means a row was edited or deleted directly in the database — the
API cannot produce one.

---

## 4. Routine operations

**Before activating a routing rule, simulate it.** Admin → Routing → the rule →
*Simulate* replays the last 200 requests and shows what would move. A rule that
matches more broadly than intended silently redirects a queue's whole workload,
and the symptom — a citizen chasing a request three weeks later — is a long way
from the cause.

**Retention is off by default.** Admin sets the schedules; destroying municipal
records is a deliberate decision against a real retention policy, never something
that starts happening because a default was left on. Anonymisation keeps the
operational record (counts, cycle times, service mix) and removes what identifies
a person.

**Run exactly one instance with `CC_JOBS_ENABLED=true`.** Two schedulers against
one database will double-send citizen notifications.

**Watch these four numbers:** overdue outbox messages, dead-lettered webhooks,
open-and-breached requests, and the audit verification result. All four are on
Admin → Operations and Admin → Delivery log.

---

## 5. Backups

```sh
mysqldump --single-transaction --routines cityconnect | zstd > cityconnect-$(date +%F).sql.zst
tar -C /opt/cityconnect/data -cf - attachments | zstd > attachments-$(date +%F).tar.zst
```

Attachments live on disk and are **not** in the database dump — a database-only
backup restores requests whose evidence photographs are gone. Back up both, and
test the restore.
