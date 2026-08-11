# CityConnect

A municipal CRM and service-request system. Citizens report problems, staff work
them through to resolution, and connected line-of-business systems participate as
first-class agents.

CityConnect is a downstream relying party of **C2 (TrustIdentity)**, the citizen
identity portal. Citizens never sign into CityConnect directly — they meet it
through a C2 Service Card, through a connected system, or by talking to an agent.

---

## What it does

- **Service requests** with a real status machine, business-hours SLA clocks,
  routing rules, queues, comments, attachments and duplicate linking.
- **Contacts** with external identity links, communication preferences, duplicate
  detection and a reversible merge.
- **Interactions** — calls, emails, counter visits — on one timeline with request
  activity.
- **Four C2 integration edges**: staff SSO, the inbound Service Card callout,
  outbound citizen notifications, and back-channel logout.
- **Connected systems** that own requests and receive signed event webhooks with
  retry and a dead-letter queue.
- **Reporting** on volume, service levels, workload and geography, with CSV export.
- **A hash-chained audit log** that can be replayed to prove it has not been edited.

## Stack

Go 1.25 · chi · GORM · MariaDB · React 18 · TypeScript · Vite · TanStack Query ·
Tailwind. The API listens on **:4021**; Apache serves the SPA and reverse-proxies
`/cityconnect/api` to it.

---

## Running it locally

One prerequisite: a MariaDB the app can reach. Create the database and user
once (this needs an administrative connection, hence `sudo`):

```sh
brew services start mariadb        # or however you run it
sudo mysql -e "CREATE DATABASE IF NOT EXISTS cityconnect CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
sudo mysql -e "CREATE USER IF NOT EXISTS 'cityconnect'@'localhost' IDENTIFIED BY 'cityconnect';"
sudo mysql -e "GRANT ALL ON cityconnect.* TO 'cityconnect'@'localhost'; FLUSH PRIVILEGES;"
```

Then everything else is one command:

```sh
./scripts/dev.sh start        # C2 stub, API and console
```

| Command | |
| --- | --- |
| `./scripts/dev.sh start [svc]` | Start all, or one of `stub` `api` `web` |
| `./scripts/dev.sh stop` | Stop, in reverse dependency order |
| `./scripts/dev.sh restart api` | After a Go change |
| `./scripts/dev.sh status` | What is running — and who holds a port if we cannot |
| `./scripts/dev.sh logs -f` | Follow every log |
| `./scripts/dev.sh doctor` | Diagnose the environment |
| `./scripts/dev.sh demo` | Add a sample contact and request |
| `./scripts/dev.sh reset` | Drop and recreate the database (confirms first) |

The console is on **:5174**, the API on **:4021**, the C2 stub on **:5273**. Sign
in as `staff-boss` — the bootstrap grant makes it an administrator. Logs and pid
files live in `.dev/`; override any port or credential in `scripts/dev.env`.

If something is wrong, `doctor` is the first stop: it checks the tooling, the
database credentials, every port, and the `:5173` collision described below.

### Already running a real C2?

`localhost:5173` is C2's documented dev origin. If a real C2 is running there,
**do not point the stub at it** — the stub appears to start while `localhost`
resolves to the real C2 first, and requests silently go there instead. The dev
environment uses 5273 to sidestep this, and `doctor` warns when it sees 5173 in
use.

To develop against a real C2, register a client for CityConnect with redirect
URI `http://127.0.0.1:5174/api/auth/callback`, then in `scripts/dev.env`:

```sh
CC_DEV_C2_ORIGIN=http://localhost:5173
CC_DEV_CLIENT_ID=<the registered client id>
CC_DEV_CLIENT_SECRET=<its secret>
```

The stub is then simply not started.

### Why the stub exists

C2 SSO is the **only** staff login — there are no local accounts. Without
something answering OIDC discovery, nothing in CityConnect is reachable, so
`devtools/c2stub` is what makes the project developable offline and testable in
CI. It deliberately reproduces C2's real quirks: the token endpoint is
`/oidc/oauth/token` rather than the `/oidc/token` everyone assumes, and back-channel
logout works but is absent from the discovery document.

Drive scenarios against it:

```sh
curl 'localhost:5173/stub/login?sub=citizen-001'                 # give a citizen a session
curl 'localhost:5173/stub/consent?sub=citizen-001&granted=false' # revoke consent → 403 on notify
curl 'localhost:5173/stub/logout?sub=staff-you'                  # fire a back-channel logout
curl 'localhost:5173/stub/callout?sub=citizen-001&url=http://localhost:4021/api/citizens/{sub}/status'
curl  localhost:5173/stub/notifications                          # what C2 received
```

---

## Tests

```sh
go test ./...            # unit and integration, no database or network needed
cd web && npm run build  # type-check and bundle
```

Service-layer tests run against in-memory SQLite, and the HTTP integration suite
drives a fully wired server plus a C2 stub over real HTTP: the authorization-code
flow, the request lifecycle, the Service Card callout, the notification outbox,
and the audit chain.

---

## Layout

```
cmd/server          the API
cmd/ccadm           operator CLI — bootstrap, tokens, audit verification, break-glass
cmd/c2stub          the local C2 stand-in
internal/domain     GORM models
internal/c2/…       oidc (discovery, JWKS, token verification), callout, notify
internal/…          service packages: agents, contacts, requests, routing, catalog, …
internal/httpapi    router, middleware, handlers
web/                the console
deployment/         deploy.sh, systemd unit, Apache config, env example
docs/               OpenAPI spec and the operations runbook
build_docs/         the source integration guides and the build plan
```

Each service package owns its own `Err*` sentinels; `internal/httpapi/respond.go`
is the single place that turns them into status codes.

---

## Deploying

```sh
cp deployment/cityconnect.env.example deployment/deploy.env   # edit the host settings
deployment/deploy.sh --host deploy@server
```

The script runs the tests, cross-compiles for linux/amd64, builds the SPA for the
configured base path, ships both, restarts the systemd unit, waits for `/healthz`,
and **rolls back to the previous binary if it does not come up**. It then checks
`/readyz` separately and warns without failing: the process can be healthy while
C2 is unreachable, and that means nobody can sign in.

See `docs/runbook.md` for first-boot setup, the C2 administrator request, and
recovery procedures.

---

## Two things worth knowing before you change anything

**The C2 issuer is the portal origin.** Not C2's internal API host, even though
discovery is served from there and appears to work. Configure it wrongly and every
token fails validation with an error that points nowhere useful. `ccadm check-c2`
prints exactly what was resolved.

**Never send `prompt=login` or `max_age=0`** on the authorization request. Either
makes C2 re-prompt for credentials on every visit even with an active session,
silently destroying SSO — and several OIDC client libraries add them by default,
serialising a "means unset" zero straight into `max_age=0`. The authorize URL is
built explicitly for this reason, and `TestAuthorizeOmitsReauthParams` fails if
anyone reintroduces them.
