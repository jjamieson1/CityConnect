# CityConnect — Build Plan

> **Status: built, and since revised.** Every phase below is implemented and the
> suite is green (`go test ./...`, `go vet ./...`, `npm run build`).
>
> **The scope has since changed in one significant way: there is now a public
> citizen portal**, which reverses `Design.md`'s "no user interfaces" and
> supersedes decision Q3. It is a separate application on a separate origin.
> See §11 (Q3 revised) and §12.


> Derived from `Design.md`, `Architecure.md`, `c2-auth-integration.md`,
> `service-card-callout-integration.md`, `notification-integration.md`.
> Sections marked **[OPEN]** need a decision before the phase that depends on them.
>
> **Decisions locked (2026-08-10):** staff auth = **C2 SSO only** · web UI = **full agent
> console** · status bundle = **C2 Service Card callout only** · tenancy = **single
> municipality, departments as a soft boundary**. See §11.

---

## 1. What we are building

CityConnect ("CC") is a **municipal CRM / service-request system**. It is a downstream
relying party of **C2 (TrustIdentity)**, the citizen identity portal. Citizens never log
into CityConnect directly — they meet it through C2 Service Cards, through connected
line-of-business systems (e.g. a future permitting app), or through a staff agent.

Three actor classes:

| Actor | How they reach CC | Surface |
| --- | --- | --- |
| **Citizen** | The **citizen portal** on its own origin, a C2 Service Card, or an agent logging a call | Public web app, C2 SSO |
| **Staff / agent** | Full agent console SPA, logged in via **C2 SSO** | Web UI + API |
| **Connected system** | Machine credentials (PAT or client-credentials JWT) | REST API + webhooks |

Citizens gained a surface after the original build. The two web applications are
deliberately separate — separate builds, separate origins, separate session
tables — for reasons set out in §12.

Four integration edges with C2:

1. **Outbound OIDC login** — staff SSO (Authorization Code + PKCE), the *only* staff auth path.
2. **Inbound Service Card callout** — C2 GETs `…/citizens/{sub}/status`, we return a live
   personalized status bundle of that citizen's open tickets.
3. **Outbound notifications** — CC POSTs to `<c2-base>/partner/notifications` to reach a
   citizen's C2 inbox / email / SMS. Consent-gated by C2.
4. **Back-channel logout** — C2 POSTs a `logout_token`; we kill all local sessions for
   that `sub`.

### Doc reconciliation notes

- `Design.md` says "no user interfaces… backend service" but then describes an admin
  interface, and `Architecure.md` describes a full React SPA. **Original reading:** the
  *citizen* has no UI; staff do. **This was later reversed** — a citizen portal was
  added on request, so the statement in `Design.md` no longer holds. The portal is a
  distinct application, not a section of the console (§12).
- `Architecure.md` is clearly adapted from a sibling project — it names `internal/devlinks`,
  `internal/architectures`, and a `c2_pat_…` token prefix. Those are not CityConnect
  concerns. Kept: React/Vite/TS + chi + GORM + layered layout + systemd/Apache deploy.
  Changed: token prefix → `cc_pat_…` (a `c2_` prefix on *our* tokens would be actively
  confusing next to real C2 credentials); DB → **MariaDB** per `Design.md`.
- `Design.md`'s "callback service delivering status bundles of open tickets" maps to the
  **Service Card callout** (§5.4) and nothing else. Connected systems are kept informed by
  **event webhooks** (§4.6), not by status-bundle polling or pushes.
- **Departments** (Public Works, Bylaw, Water, …) are a soft boundary inside one
  municipality: they scope queues, service types, and default visibility, but all data
  lives in one tenant and a supervisor can see across departments. No tenant column.

---

## 2. Technology decisions

| Layer | Choice | Notes |
| --- | --- | --- |
| API | Go 1.25, `chi` v5 | Module `github.com/jjamieson1/CityConnect` |
| ORM | GORM + MariaDB driver | `AutoMigrate` in dev; versioned SQL migrations for prod (see §7) |
| DB | MariaDB 10.11+ | `utf8mb4`, InnoDB, `FULLTEXT` indexes for search |
| Auth (staff) | OIDC → HTTP-only session cookie | `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2` |
| Auth (machine) | `cc_pat_…` PATs (SHA-256 at rest) + inbound C2 JWT verify | JWKS via discovery, cached, refresh-on-unknown-`kid` |
| JWT | `github.com/go-jose/go-jose/v4` (or `lestrrat-go/jwx/v2`) | One library for both verify + sign |
| Jobs | In-process worker goroutines + DB-backed outbox/queue tables | No external broker in v1 |
| Logging | `log/slog` JSON, request-id + `sub` correlation | |
| Metrics | Prometheus `/metrics` | |
| Frontend | React 18 + TS + Vite, react-router-dom, TanStack Query, Tailwind + shadcn-style kit | Base path `/cityconnect/` |
| Deploy | Cross-compiled Go binary, systemd `cityconnect-api`, Apache reverse proxy | API on **:4021** |

---

## 3. Repository layout

```
cmd/
  server/main.go            # config → db → services → http → worker
  worker/main.go            # [optional] run schedulers out-of-process
  ccadm/main.go             # CLI: seed, issue PAT, backfill, retention run
internal/
  config/                   # env → typed config, validation at boot
  domain/                   # GORM models + AllModels()
  store/                    # repository helpers, pagination, tx, soft-delete scopes
  contacts/                 # contacts, identities, groups, merge/dedupe
  interactions/             # call/email/meeting logs, unified timeline
  requests/                 # service requests: lifecycle, comments, attachments, links
  catalog/                  # service types, forms, SLA policies, business calendars
  routing/                  # queues, rules engine, assignment strategies
  agents/                   # staff users, roles, connected-system agents
  c2/
    oidc/                   # discovery, JWKS cache, login, PKCE, logout
    callout/                # inbound assertion verify + status-bundle builder
    notify/                 # partner notification client (basic + private-key JWT)
  notifications/            # templates, outbox, dispatcher, delivery log
  webhooks/                 # outbound events to connected systems, HMAC, retries
  reports/                  # rollups, saved views, CSV export
  search/                   # FULLTEXT query builder
  audit/                    # hash-chained audit log
  httpapi/                  # chi router, middleware, handlers, DTOs, error mapping
  jobs/                     # scheduler: SLA escalation, outbox drain, rollups, retention
  portal/                   # citizen-facing service layer, scoped to one contact
web/                        # staff console (Vite SPA)
web-portal/                 # citizen portal (Vite SPA, separate origin)
shared/ui/                  # design tokens + primitives used by both apps
scripts/
  dev.sh                    # start/stop/restart/logs/doctor for the dev environment
deployment/
  deploy.sh  cityconnect-api.service
  apache-cityconnect.conf   # staff vhost
  apache-cityconnect-portal.conf  # citizen vhost, stricter CSP
devtools/
  c2stub/                   # local fake C2: discovery, JWKS, callout caller, notify sink
docs/                       # OpenAPI spec, ADRs, runbooks
build_docs/                 # source-of-truth integration docs (this file)
```

---

## 4. Domain model

All models embed `Base { ID uuid, CreatedAt, UpdatedAt, DeletedAt }`. Mutable aggregates
(`Request`, `Contact`) also carry `Version uint` for optimistic concurrency.

### 4.1 People & identity

- **Contact** — the citizen. `display_name`, `given_name`, `family_name`, `preferred_language`,
  `primary_email`, `primary_phone`, `do_not_contact`, `status`, `tags`, `custom_fields JSON`.
  Deliberately **not** keyed on `sub`: a walk-in with no C2 account is still a contact.
- **ContactIdentity** — `(contact_id, provider, external_id)` unique. `provider ∈ {c2, permitting, …}`.
  The C2 `sub` lands here. Lets one contact carry several system identities and lets us
  merge duplicates without losing links.
- **ContactChannel** — email/phone/address rows with `verified`, `is_primary`, `label`.
- **ContactGroup** + **ContactGroupMember** — many-to-many, e.g. "Ward 3", "Snow route A".
- **ConsentPreference** — per contact × channel × purpose, with source + timestamp. CC's own
  contact preferences; distinct from C2 consent, which C2 owns and enforces.
- **MergeRecord** — audit of contact merges (survivor, merged, field-level choices), reversible.

### 4.2 Staff & machine actors

- **User** — staff. `c2_sub` (**unique, required** — C2 SSO is the only staff login),
  `email`, `name`, `status ∈ {invited, active, suspended}`, `role`, `department_id`, `queues[]`.
  A user row may exist before first login (invited by an admin, matched on email at the
  first successful C2 callback, then pinned to `sub` forever after).
- **Department** — `name`, `code`, `parent_id` (optional hierarchy), `default_queue_id`,
  `contact_block` (address/email/phone surfaced in callout responses). Soft boundary:
  scopes queues, service types, and default list filters; supervisors and admins can be
  granted cross-department visibility.
- **Role / Permission** — `admin`, `supervisor`, `agent`, `readonly`, plus scoped grants
  (`department:*` vs `department:{id}`). Enforced in the service layer, not just middleware.
- **Session** — server-side session records keyed by `sub` so back-channel logout can kill
  *all* of a user's sessions (the logout_token has no `sid`).
- **ApiToken** — `cc_pat_…`, stored as SHA-256; `scopes`, `read_only`, `owner`, `expires_at`,
  `last_used_at`.
- **ConnectedSystem** — a non-human agent (the future permitting app). `name`, `base_url`,
  `webhook_url`, `webhook_secret`, `auth_mode`, `queues[]`, `active`. Can be assigned tickets.

### 4.3 Service catalog

- **ServiceType** — `code` (e.g. `POTHOLE`), `name`, `category`, `description`,
  `intake_form JSON` (field schema), `department_id`, `default_queue_id`, `sla_policy_id`,
  `c2_service_card_id`, `public_visible`, `active`.
- **SlaPolicy** — `first_response_minutes`, `resolution_minutes`, per-priority overrides,
  `calendar_id`, `pause_on_statuses[]` (clock stops while waiting on citizen).
- **BusinessCalendar** — weekly hours + holiday exceptions + timezone.

### 4.4 Service requests (tickets)

- **Request** — `reference` (`SR-2026-000123`, human-quotable), `contact_id`, `service_type_id`,
  `source ∈ {c2_card, api, agent, email, import}`, `origin_system`, `status`, `priority`,
  `queue_id`, `assignee_user_id | assignee_system_id`, `subject`, `description`,
  `location {address, lat, lng, ward, parcel_id}`, `form_data JSON`,
  `first_response_at`, `sla_due_at`, `sla_breached`, `opened_at`, `closed_at`,
  `resolution_code`, `csat_score`, `version`.
- **Status machine** (fixed core, configurable labels):
  `new → triaged → assigned → in_progress → {waiting_citizen | waiting_third_party} → resolved → closed`
  with `cancelled` and `reopened` as side transitions. Transitions validated server-side;
  illegal transitions are a `409`, not a silent write.
- **RequestComment** — `visibility ∈ {internal, citizen}`, markdown body, author.
  Citizen-visible comments are what feed the callout's "descriptions of actions".
- **RequestEvent** — append-only timeline: created, status_changed, assigned, commented,
  attachment_added, notification_sent, callout_served, sla_warning, sla_breached, merged.
- **RequestLink** — `duplicate_of`, `related_to`, `child_of`. 311 workloads generate many
  reports of one pothole; merging duplicates while keeping every reporter notified is
  table stakes.
- **Attachment** — local disk (v1) with content-type allowlist, size cap, checksum, and a
  pluggable scan hook.

### 4.5 Routing

- **Queue** — `name`, `department_id`, `type ∈ {human, system}`,
  `assignment_strategy ∈ {manual, round_robin, least_loaded}`,
  `members[]`, `calendar_id`, `escalation_queue_id`. Cross-department transfer is a
  first-class action (it is the most common real-world routing correction) and is recorded
  on the request timeline.
- **RoutingRule** — ordered by `priority`; `conditions JSON` (service type, keyword match,
  ward/geofence, priority, source, custom field predicates) → `actions JSON` (set queue,
  set assignee, set priority, add tag, set SLA, notify). First-match-wins with an explicit
  `continue` flag. Evaluated on create and on re-triage.
- **RuleSimulation** — dry-run a rule set against historical requests before activating it.
  Cheap to build, prevents a bad rule silently black-holing a queue.

### 4.6 Interactions & communications

- **Interaction** — `type ∈ {call, email, meeting, sms, portal, note, walk_in}`, `direction`,
  `occurred_at`, `duration_seconds`, `summary`, `contact_id`, `user_id`, optional `request_id`.
- **NotificationTemplate** — keyed by event + language; Go `text/template` over a documented
  context; renders `subject`, `body`, `shortBody`.
- **NotificationOutbox** — durable row per send: payload, attempts, `next_attempt_at`,
  `state ∈ {pending, sent, failed, suppressed}`, C2 `notificationId`, `channels[]`, last error.
- **WebhookDelivery** — same shape for outbound webhooks to connected systems, with replay
  and a dead-letter view.

### 4.7 Cross-cutting

- **AuditLog** — actor, action, target, before/after diff, IP, request-id, and a `prev_hash`
  chain. C2 keeps a tamper-evident log of every notification it accepts from us; matching
  that on our side makes reconciliation possible.
- **IdempotencyKey** — `(client, key)` → stored response, 24h TTL, on all inbound POSTs.
- **RetentionPolicy** — per entity: retain N years, then anonymize or purge. Records-retention
  schedules are a real municipal obligation; retrofitting them is painful.

---

## 5. C2 integration specifics

These are the parts most likely to be got wrong, so they get explicit treatment.

### 5.1 Discovery & JWKS (shared foundation — build first)

- One `c2/oidc.Provider` owns: discovery fetch from `<C2_PORTAL_ORIGIN>/oidc/.well-known/openid-configuration`,
  cached with TTL; JWKS cache keyed by `kid` with refresh-on-miss and a rate limit.
- **Nothing hardcodes an endpoint path.** `token_endpoint` in this deployment is
  `/oidc/oauth/token`, not `/oidc/token` — a standing reminder that discovery is the contract.
- Configured issuer = **portal origin + `/oidc`**, never the internal `:8088` API host.
- A boot-time self-check logs the resolved issuer/endpoints; a `/readyz` probe fails if
  discovery is unreachable.

### 5.2 Staff login (Authorization Code + PKCE)

- Fresh `code_verifier` (43–128), `state`, `nonce` per attempt, stored server-side.
- **Never send `prompt=login`; never send `max_age=0`.** A lint test asserts the built
  authorize URL contains neither — this is the single most common SSO-killer and a unit
  test is cheaper than the debugging session.
- `id_token` validation: signature via JWKS `kid`, `iss` exact, `aud == client_id`, `exp`,
  `nonce`. Only then map `sub` → `User`.
- `prompt=none` silent re-check before privileged actions and on a timer; `login_required`
  → local logout.
- RP-initiated logout hits `end_session_endpoint` with `id_token_hint` +
  `post_logout_redirect_uri`.

**Consequences of C2-SSO-only (accept these deliberately):**

- Every staff member needs a C2 identity. Confirm with the C2 administrator that staff
  accounts are supported in the same directory as citizens, and how they are provisioned.
- **Bootstrap:** the first admin cannot be created through the UI. `ccadm grant-role
  --sub <c2-sub> --role admin` (or a boot-time `CC_BOOTSTRAP_ADMIN_SUBS` allowlist) seeds
  it. Document this in the deploy runbook — it is the classic day-one blocker.
- **Unknown-`sub` policy on login:** default **deny** — a valid C2 identity with no `User`
  row gets a clean "no CityConnect access" page, not an auto-provisioned account. Admins
  invite by email; the invite binds to `sub` on first login.
- **If C2 is down, nobody can log in.** No local fallback by choice. Mitigations: long-ish
  session TTL (8h) with silent `prompt=none` refresh, a documented break-glass procedure
  via `ccadm`, and `/readyz` surfacing C2 discovery health so the outage is diagnosable in
  seconds rather than mistaken for a CityConnect fault.
- Back-channel logout now affects *staff* sessions too — a citizen-portal sign-out ends the
  agent's console session. Expected, but worth calling out in staff training.

### 5.3 Back-channel logout — `POST /api/c2/backchannel-logout`

Validate: RS256 signature via the *same* JWKS; `iss` == issuer; `aud` == our `client_id`;
`events` contains `http://schemas.openid.net/event/backchannel-logout`; `sub` present;
**reject if `nonce` present**; dedupe on `jti`; `iat` recent. Then delete **every** session
for that `sub` (no `sid` exists). Respond `200` + `Cache-Control: no-store`, `400` on
validation failure, never a redirect. Not advertised in discovery — coordinate registration
with the C2 admin.

### 5.4 Service Card callout — `GET /api/citizens/{sub}/status`

Inbound from C2, server-to-server, ~5s budget, ≤1 MB, no caching by C2.

- Auth: `signed_jwt` mode primary — verify signature/`iss`/`aud == client_id`/`exp` against
  the same JWKS. `app_key` mode (`X-App-Key`/`X-App-Secret`) supported behind a config flag
  as a fallback. Fail closed with 4xx.
- Resolve `sub` → `ContactIdentity(provider=c2)` → `Contact`. **Unknown `sub` → `200 {}`**
  (valid-but-empty), never a guess.
- Build the status bundle from that contact's open requests:

```json
{
  "title": "Your service requests",
  "description": "You have 2 open requests. Most recent update: SR-2026-000123 moved to In Progress on Aug 8.",
  "CTA": "https://…/cityconnect/requests",
  "contact": { "email": "311@city.example", "phone": "+1 555 0100" },
  "tasks": [
    { "name": "SR-2026-000123 — Pothole, 12 Oak St", "description": "In Progress · updated Aug 8 · Crew scheduled for Aug 12", "url": "https://…/requests/SR-2026-000123" }
  ]
}
```

  Satisfies `Design.md`'s "ticket ID, status, last-updated timestamp, descriptions of actions".
  Note the capitalized **`CTA`**. `tasks` win over `CTA` when both present. Cap `tasks` at
  ~10 most-recently-updated and summarize the remainder in `description`.
- Performance: single indexed query + a short in-process cache (C2 calls on every render
  *and* on a refresh timer). Target p99 < 300 ms. Emit a `callout_served` event but
  rate-limit that write so a card left on screen doesn't flood the timeline.

### 5.5 Outbound notifications — `POST <c2-base>/partner/notifications`

- Auth: **private-key JWT via `X-Client-Assertion`** as the primary path (we host a JWKS
  URL and register it with the C2 admin); HTTP Basic with `client_secret` as fallback.
  Assertion: `iss == sub == client_id`, `aud` = deployment issuer, short `exp`, unique `jti`,
  `kid` in header.
- Body: `sub`, `subject`, `body`, optional `shortBody` (one SMS segment), `category`.
- Response handling — this is where a naive client burns itself:
  - `202` → record `notificationId` + `channels`, mark sent.
  - `403` (no active consent) → mark **suppressed, do not retry**. Flag the contact as
    "not reachable via C2" in the UI so agents fall back to phone/mail.
  - `404` (unknown `sub`) → suppress + flag a stale identity link for review.
  - `429` → back off with jitter, respect any `Retry-After`.
  - `5xx`/network → exponential backoff, cap at N attempts, then dead-letter.
- One recipient per call, no bulk endpoint — the outbox worker paces sends and honours the
  per-IP rate limit rather than fanning out.
- Every send is recorded on the request timeline and in the audit log.

### 5.6 Local development

`devtools/c2stub` — a small Go server that serves a discovery document, a JWKS, mints
id_tokens and callout assertions, fires back-channel logouts, and accepts partner
notifications (with a togglable 403 to exercise the consent path). Building this in Phase 0
is what makes phases 4–7 testable without a live C2, and lets CI run the integration suite.

---

## 6. HTTP API surface (v1)

Base path `/api` (Apache strips `/cityconnect`). Cursor pagination, `?include=` expansion,
RFC 7807 problem+json errors.

```
GET  /healthz  /readyz  /metrics

# staff auth
GET  /api/auth/login            → 302 to C2 authorize (PKCE)
GET  /api/auth/callback         → sets session cookie
POST /api/auth/logout           → local + RP-initiated
GET  /api/auth/me
POST /api/c2/backchannel-logout # C2 → us

# C2 inbound
GET  /api/citizens/{sub}/status # Service Card callout

# contacts
GET|POST        /api/contacts
GET|PATCH|DEL   /api/contacts/{id}
GET             /api/contacts/{id}/timeline
POST            /api/contacts/{id}/merge
GET|POST|DEL    /api/contacts/{id}/identities
GET|POST        /api/contacts/{id}/channels
GET|POST        /api/contact-groups …

# interactions
GET|POST /api/interactions        GET|PATCH|DEL /api/interactions/{id}

# requests
GET|POST        /api/requests
GET|PATCH       /api/requests/{id}
POST            /api/requests/{id}/transition     # status machine
POST            /api/requests/{id}/assign
POST            /api/requests/{id}/comments
POST            /api/requests/{id}/attachments
POST            /api/requests/{id}/links
POST            /api/requests/bulk                # bulk assign / transition / tag
GET             /api/requests/{id}/events

# catalog & routing
GET|POST /api/departments        GET|POST /api/service-types
GET|POST /api/sla-policies       GET|POST /api/business-calendars
GET|POST /api/queues             POST /api/queues/{id}/members
GET|POST /api/routing-rules      POST /api/routing-rules/simulate

# agents & clients
GET|POST /api/users              POST /api/users/invite
GET|POST /api/connected-systems  POST /api/connected-systems/{id}/rotate-secret
GET|POST /api/tokens             DELETE /api/tokens/{id}

# comms
GET|POST /api/notification-templates
POST     /api/notifications/send        # ad-hoc, agent-initiated
GET      /api/notifications             # delivery log
POST     /api/notifications/{id}/retry

# reporting
GET /api/reports/volume  /sla  /agent-performance  /csat  /geo
GET /api/reports/{name}/export.csv
GET|POST /api/saved-views

GET /api/search?q=…&type=contact|request
```

**Partner intake API** (what the future permitting app calls) — same routes, authenticated
by `cc_pat_…` or client-credentials JWT, scoped to that system's service types, with
mandatory `Idempotency-Key` on POST.

---

## 7. Delivery phases

Each phase ends green: `go test ./...`, `golangci-lint`, `npm run build`, and a demo path.

| # | Phase | Contents | Depends on |
| --- | --- | --- | --- |
| **0** | **Foundation** | Module init, config, MariaDB + GORM + migration tooling, chi router, slog/request-id/recover/CORS middleware, `/healthz` `/readyz` `/metrics`, error mapping, test harness (testcontainers or dockerized MariaDB), CI, `devtools/c2stub`, `deployment/` skeleton on :4021 | — |
| **1** | **C2 auth core + access control** | OIDC discovery/JWKS provider, PKCE login, sessions keyed on `sub`, back-channel logout, departments, users, roles, PATs, `RequireAuth`/`RequirePermission`, audit log, `ccadm` (incl. admin bootstrap) | 0 |
| **2** | **Contacts CRM** | Contact, identities, channels, groups, consent prefs, dedupe + merge, FULLTEXT search | 1 |
| **3** | **Interactions & timeline** | Interaction CRUD, unified contact timeline | 2 |
| **4** | **Catalog & requests** | Departments→service types, intake forms, SLA policies, calendars, Request + status machine + comments + attachments + events + links, reference-number generator | 2 |
| **5** | **Routing & agents** | Queues, rules engine + simulator, assignment strategies, cross-department transfer, connected systems, outbound webhooks with retry/DLQ | 4 |
| **6** | **C2 citizen edges** | Callout endpoint + status-bundle builder, notification client (private-key JWT) + outbox + templates + dispatcher, reachability flagging | 1, 4 |
| **7** | **Automation & SLA** | Scheduler: SLA warn/breach/escalate, auto-close stale, CSAT survey, outbox drain, rollup jobs, retention jobs | 5, 6 |
| **8a** | **SPA shell** | Vite scaffold, C2 login flow, app shell, nav, department switcher, permissions-aware routing, `api.ts` client, UI kit. Lands right after phase 1 | 1 |
| **8b** | **Agent console** | Queue board, request detail (timeline, comments, attachments, transitions, assignment, links), contact record + timeline, global search, saved views, bulk actions, macros | 8a, 3, 4, 5 |
| **8c** | **Admin console** | Departments, service types + form builder, SLA policies, calendars, queues, routing rules + simulator, users/invites, connected systems, tokens, notification templates, delivery log + webhook DLQ viewer | 8a, 5, 6 |
| **9** | **Reporting** | Daily rollup tables, report endpoints, charts, CSV export, geo/ward clustering, per-department dashboards | 4, 7 |
| **10** | **Hardening & launch** | OpenAPI spec + generated TS client, rate limits, load test on the callout path, retention/PII review, deploy runbook, seed + demo data, break-glass procedure | all |

Phase 1 moved ahead of the CRM work because **every** surface now depends on C2 SSO —
there is no local-account path to develop against, so the OIDC core plus `c2stub` must
exist before anything else is demoable. Phases 2–3 and 4–5 can then run in parallel, and
8b/8c track the API phases they consume. The C2 client-registration request has the longest
lead time and must go out during phase 0 (§10).

---

## 8. Added features (beyond the source docs)

Recommended from experience with CRM/311 systems; each is cheap now and expensive later.

1. **Contact dedupe + merge** with reversible history — inevitable once multiple systems feed contacts.
2. **Request linking / duplicate merge** that keeps every reporter on the notification list.
3. **Rule simulation (dry-run)** before activating a routing rule.
4. **Saved views + bulk actions** — the difference between a usable queue console and a toy.
5. **Macros / canned responses** with template variables.
6. **Optimistic concurrency (`version`)** on requests — two agents editing one ticket is routine.
7. **Idempotency keys** on all partner POSTs — connected systems retry.
8. **Hash-chained audit log**, mirroring C2's tamper-evident log for reconciliation.
9. **Data-retention & anonymization jobs** — municipal records schedules.
10. **CSAT survey** on closure, delivered via the C2 notification channel.
11. **Geo/ward clustering** on requests, for both routing and reporting.
12. **`c2stub` dev server** — the highest-leverage item in the whole plan.
13. **Webhook replay + dead-letter UI** for connected-system outages.
14. **Business-hours-aware SLA clock** with pause-on-waiting.
15. **"Reachability" flag** driven by C2 `403`/`404` responses, so agents know when a citizen
    can't be reached digitally and fall back to phone or mail.

---

## 9. Risks

| Risk | Mitigation |
| --- | --- |
| `aud` / `iss` mismatch against C2 → silent 401s | One canonical OIDC client; issuer = portal origin + `/oidc`; boot-time self-check; integration tests against `c2stub` |
| A client library injects `max_age=0` / `prompt=login` | Unit test asserting the built authorize URL; construct params explicitly rather than trusting defaults |
| Callout latency under load (called on every render + timer) | Single indexed query, short cache, p99 budget + load test in phase 10 |
| Notification `403` retry storms | Suppress-not-retry on 403/404; backoff + `Retry-After` on 429 |
| **C2 outage locks out every staff member** (no local fallback, by decision) | 8h sessions with silent `prompt=none` refresh; `/readyz` surfaces C2 discovery health; documented `ccadm` break-glass; agreed C2 availability expectations with the administrator |
| Staff provisioning depends on citizen-IdP accounts | Confirm staff-account support with the C2 admin in phase 0; invite-by-email flow that binds to `sub` on first login; deny-by-default for unknown `sub` |
| Departments harden into a de-facto tenant boundary | Keep it a *filter*, not an isolation guarantee; no department column on `Contact` (citizens belong to the city, not a department); cross-department transfer as a first-class action |
| Rules engine becomes an unmaintainable DSL | Keep conditions to a small typed predicate set; simulator; no user-supplied code |
| `AutoMigrate` drift in production | AutoMigrate in dev only; versioned SQL migrations (goose/atlas) for staging + prod |
| PII sprawl | Field-level classification from day one; retention jobs in phase 7, not bolted on |

---

## 10. Immediate next actions

1. **Send the C2 administrator request now** — longest lead time, and phase 1 is blocked on
   it. Ask for: portal origin; `client_id` + `client_secret`; registered `redirect_uri`s
   (every environment variant, exact-match, including local dev); allowed scopes; the
   callout URL + auth mode (`signed_jwt`); our `backchannel_logout_uri`; our published JWKS
   URL for notification client-assertions; the partner-notifications base URL; **and
   confirmation that city staff can hold C2 identities, plus how those are provisioned.**
   Register any test/conformance clients under a *separate* application — a second client
   under ours shifts the `aud` on callouts and produces silent 401s.
2. Stand up phase 0 + `c2stub`.
3. Answer §11 Q5 (email intake) before phase 4 freezes the `source` enum.

---

## 11. Decisions & open questions

### Resolved (2026-08-10)

| # | Question | Decision | Effect |
| --- | --- | --- | --- |
| Q1 | Staff authentication | **C2 SSO only** | OIDC core promoted to phase 1; no local accounts; `ccadm` bootstrap + break-glass required; C2 outage = staff lockout, accepted |
| Q2 | "Callback service" semantics | **C2 Service Card callout only** | No status-bundle push/poll for connected systems; they get event webhooks instead |
| Q3 | Web UI scope | **Full agent console** — *superseded, see below* | Phase 8 split into shell / agent console / admin console; frontend is a major workstream, not a thin veneer |
| Q4 | Tenancy | **Single municipality, departments inside** | No tenant column; `Department` scopes queues, service types, users, and default filters; cross-department transfer is first-class; `Contact` is city-wide |

### Revised after the build

**Q3 — Web UI scope. Revised 2026-08-11: a citizen portal was added.**

The original answer was a staff console only, matching `Design.md`. That changed
when the Service Card's task links turned out to point at the staff console —
a link no citizen can follow, since they have no staff account. The fix needed
somewhere for a citizen to land, and the natural scope of that is the portal:
browse a service catalogue, report a problem, follow it, reply, withdraw, rate.

It is a **separate application on a separate origin**, not a route inside the
console. The reasoning is in §12; the short version is that a shared origin
means a shared cookie jar, and the console has no business being downloadable
by the public.

### Still open

**Q5 — Intake channels for v1.** Beyond C2 Service Cards, agent entry, and the partner API —
is inbound *email* intake (mailbox → ticket, with reply-threading back onto the request) in
scope for v1? Needed before phase 4 freezes the `Request.source` enum and the comment model.

### Raised by the locked decisions

**Q6 — Staff identities in C2.** Does C2 support staff/employee accounts, and are they
provisioned by the C2 administrator, self-registered, or federated from the city directory?
Blocks phase 1 acceptance.

**Q7 — Department list.** Which departments exist at launch, and does the hierarchy need
more than one level? Drives seed data and the routing rule set.

**Q8 — Cross-department visibility default.** Can any agent read another department's
requests (transparent, simpler), or is read access department-scoped with supervisors
granted cross-department rights? Recommend transparent-read + scoped-write for v1.

---

## 12. What changed during the build

Six decisions differ from the plan as written. Each was forced by something the
plan could not have known in advance.

**Foreign key constraints are not created.** Roughly twenty relationships are
optional and modelled as empty-string ids rather than nullable pointers, which
keeps the models and their JSON readable. A database FK cannot express that
(`""` is not `NULL`), so `DisableForeignKeyConstraintWhenMigrating` is set and
referential integrity is enforced in the service layer instead. Indexes are
still created. The alternative — `*string` on every optional foreign key —
makes every read site noisier for a guarantee the write paths already provide.

**The audit sequence number is assigned by the application, not the database.**
`autoIncrement` on a non-primary-key column is not portable; SQLite silently
leaves it zero, which made the chain unverifiable under test. The audit service
now assigns `Seq` under the same lock that serialises appends, and the sequence
is part of the hash. A gap is therefore detectable as well as an edit.

**Pagination is offset-based, not keyset.** Municipal volumes are tens of
thousands of requests a year, and the console wants exact totals and page
numbers — which keyset paging cannot provide without a second query anyway.

**Two dependency cycles are broken with setters rather than package merges.**
`requests` needs to emit notifications and webhooks; both of those need
`catalog` and `contacts`, which need `requests`. `SetNotifier` and `SetWebhooks`
wire them after construction, keeping each package's boundary intact.

**`internal/storetest` is a separate package.** The test-database helper lives
outside `internal/store` so the production binary never links `testing`.

**Reporting percentiles use nearest-rank.** For the small samples a single
department produces in a week, interpolating toward the median understates the
tail; a supervisor asking "how bad does this get?" is better served by the
actual worst case in the band.

**Idempotency keys, added later.** Documented in the OpenAPI spec from the
start but not implemented until asked for. `Idempotency-Key` is optional on any
mutating request; a retry replays the original response, a key reused with a
different body is a 422 rather than a silent discard, keys are scoped per
caller, a *failed* attempt releases the key, and concurrent deliveries are
settled by a unique-index insert rather than read-then-write. Building it
surfaced a trap worth remembering: `IdempotencyKey` embeds `Base`, which has
**soft delete**, so releasing a key left the row in place — the unique index
still held it while the lookup, which hides deleted rows, could not find it.
Any future table combining a unique index with soft delete has the same problem.

**Empty collections are `[]`, never `null`.** A nil Go slice marshals to
`null`, and the dashboard called `.map()` on it — so the console crashed to a
blank page on a brand-new deployment, the one state no test covered. Fixed at
the source across the reports and all 25 listing endpoints, with a structural
guard (§12, *Beyond the plan*).

### Beyond the plan

- **`ccadm check-c2`** prints the configured *and* discovered endpoints side by
  side. Every C2 misconfiguration in the integration guides produces a
  confusing symptom far from its cause; this makes the answer a single command.
- **`/readyz` reports C2 reachability as a first-class condition**, with the
  issuer hint in the payload. With SSO as the only staff login, C2 being
  unreachable means nobody can sign in, and that must not look like a
  CityConnect fault.
- **The deploy script rolls back automatically** if the new binary fails its
  health check, and reports readiness separately without failing the deploy.
- **The rule simulator warns when a candidate rule reroutes more than half of
  recent requests**, since that is almost always broader than intended.
- **An empty-state guard** sweeps 26 collection endpoints against an unseeded
  database and fails on any `null`, naming the endpoint and JSON path. Verified
  to fail by reintroducing the bug. Its limitation is honest: the endpoint list
  is hand-maintained, so it catches regressions rather than omissions.
- **A React error boundary.** Previously any render error unmounted the whole
  console — a blank page with no message. The shell now survives and shows what
  broke.
- **`scripts/dev.sh`** manages the development environment (start/stop/restart/
  status/logs/doctor). It encodes the traps this project actually hit: the
  `:5173` collision with a real C2, MariaDB's `unix_socket` auth returning 1698,
  and `go run` orphaning its child on stop.
- **`ccadm invite`** and `CC_BOOTSTRAP_ADMIN_EMAILS` create admin invitations by
  email. Provisioning previously required a C2 subject identifier — which nobody
  has before their first sign-in, which is precisely what was blocked.
- **Sign-in denials log the subject and email**, with the exact `ccadm` command
  to grant access. The person being refused cannot see their own subject, so
  without this an administrator has nothing to act on.

---

## 13. The citizen portal (added 2026-08-11)

### Why it exists

The Service Card callout emitted task links pointing at
`/requests/{reference}` on the **staff console**. Broken twice over: that route
expects a UUID, not a reference, and a citizen following it has no staff
account, so they were bounced to sign-in and then refused. Giving the link
somewhere to land meant giving citizens a surface.

### What a citizen can do

Browse a service catalogue grouped by category, report a problem through the
same admin-configured intake form the console uses, then track it: status in
plain words, what the crew recorded, a reply box, withdraw while it is still
new, and rate it once resolved.

The rating closes a real gap. The scheduler could already *send* a satisfaction
survey, but nothing could record an answer — so `csat_score` was never written
and the CSAT report could only ever read zero.

### The trust models are opposite, and the code says so

This is the part worth understanding before changing anything here.

- **Staff access is deny-by-default.** C2 authenticates *citizens*, so an
  unknown subject must never become an agent.
- **Citizen access is open by design.** Any resident may report a pothole, so a
  first sign-in provisions a contact.

The second is only safe because every read is scoped to that one contact's own
records. The scoping therefore lives in the service layer, not the handlers: no
method in `internal/portal` accepts a caller-supplied contact or owner.

### How the surfaces are isolated

| Control | Why it is structural rather than conditional |
| --- | --- |
| **A separate `citizen_sessions` table** | No code path can turn a portal session into a staff principal, because the staff lookup queries a table citizens never appear in. A boolean on a shared row would put that guarantee one missing `WHERE` clause away. |
| **A separate cookie** | An agent can be signed into the console and view their own reports as a resident without one session evicting the other. |
| **A separate origin** | The decisive one. A shared origin is a shared cookie jar: script on the public site could call staff endpoints with a staff member's ambient authority. Cookie `Path` scoping does **not** fix this — `HttpOnly` stops script reading the cookie, but the browser still attaches it to any same-origin request the script makes. |
| **A separate build** | The public bundle contained `routing-rules/simulate`, `audit/verify`, `webhookSecret`, `user:manage` and more — free reconnaissance, and 332 KB to file one form. Now 228 KB and none of it. |
| **404, never 403** | Another citizen's reference is indistinguishable from one that does not exist. A 403 would confirm which references are real. |
| **Internal comments excluded at the query** | Not filtered after loading. A projection that has already fetched private notes is one refactor away from leaking them. |

Eight tests cover this, including a portal session refused on ten staff
endpoints, a staff session refused on the portal, and a comment marked `SECRET`
staying invisible to the person who filed the request.

### Deployment consequences

The API is served on **both** origins. That is what keeps each app's calls
same-origin and both cookies `SameSite=Lax` — serving the API only on the staff
host would make the portal's calls cross-site and force `SameSite=None`,
trading a cookie-jar problem for a CSRF one.

Two vhosts (`deployment/apache-cityconnect{,-portal}.conf`). The citizen one
carries a stricter CSP and denies `/api/*` by default, allowing exactly three
groups: the portal's own API, the sign-in callback, and the endpoints C2 calls
server-to-server.

**Everything C2 calls is published on the citizen origin**, never the API's
listening port. C2 is external: it cannot reach `:4021`, and in most
deployments it cannot reach the staff host either. The first draft of this
vhost denied the callout — it worked in development, where a Vite proxy
forwards everything, and would have failed on the first deploy. Dev and
production must publish the same paths or the divergence surfaces at the worst
possible moment.

**C2 needs a second `redirect_uri` registered** — one per origin, since C2
matches them exactly. `LoginFlow` records which surface and which redirect URI
started a flow, so one shared callback can still dispatch correctly in a
single-origin deployment.

---

### Still open

**Q5 — inbound email intake.** Not built. The `Request.source` enum already
carries `email`, and `Interaction` covers the logging side, so adding a
mailbox poller is additive rather than a schema change. Nothing else in the
system assumes it is absent.

**Versioned migrations.** Schema is still AutoMigrate via `ccadm migrate`, so
there is no schema rollback. Unchanged from the original build.

**FULLTEXT indexes are created but unused.** Search is `LIKE`-based; the
`internal/search` package in §3 was never built. Fine at municipal scale,
degrades at a few hundred thousand requests.
