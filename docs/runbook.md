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
| **Registered `redirect_uri`s** | Exact-match. Register *every* environment variant, including local dev. A trailing slash is a different URI. |
| **Allowed scopes** | We request `openid profile email`. |
| **Callout URL + auth mode** | Give them `https://<host>/cityconnect/api/citizens/{sub}/status` and ask for **`signed_jwt`**. |
| **`backchannel_logout_uri`** | Register `https://<host>/cityconnect/api/c2/backchannel-logout`. Not advertised in discovery — it must be arranged deliberately. |
| **Our JWKS URL** | `https://<host>/cityconnect/api/c2/jwks`, so C2 can verify our notification client assertions. |
| **Partner notifications base URL** | Mounted at `<base>/partner`, a sibling of the OIDC endpoints — **not** under `/api`. |
| **Confirmation that staff can hold C2 identities** | C2 is a *citizen* identity provider. With SSO as our only staff login, this is load-bearing. Ask how staff accounts are provisioned. |

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
sudo a2enmod proxy proxy_http headers rewrite deflate expires
sudo a2enconf apache-cityconnect
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
  https://<host>/cityconnect/api/citizens/<sub>/status
```

- `401` — the assertion failed verification. Almost always an `aud` mismatch:
  C2 is signing for a different `client_id` than the one configured here.
- `200 {}` — CityConnect does not know that subject. Correct behaviour for a
  citizen with no record; if it is wrong, their contact is missing its C2 identity.
- Slow — C2's budget is about five seconds and it calls on every render.
  `CC_C2_CALLOUT_CACHE_TTL` is what keeps that cheap.

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
