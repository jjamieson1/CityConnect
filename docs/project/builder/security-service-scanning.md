# Build a security pipeline for a C2-connected application

Give a client application the same security posture C2 holds itself to, shaped to
what that application actually is. The output is a **running pipeline plus a
shareable evidence page**, not a document.

## Before you start

Read, in this order:

- `security/README.md` — how the dashboard works, and the division of labour that
  the whole approach depends on.
- `docs/security-testing-manual.md` — C2's own playbook. **A model, not a
  template.** Roughly a fifth of it is specific to C2 being an identity provider.
- `docs/c2-auth-integration.md` and `docs/payment-broker-integration.md` — the
  integration points you will be writing checks for.

### The rule that matters most

`security-dashboard init` writes `checks[]` by detecting the stack. It
deliberately **never** writes `standards[]` or `asvs.requirements` evidence,
because a compliance table full of invented citations is worse than no table —
that section is exactly what a customer reads as an assertion.

So: **every evidence string you write must cite a file or test that exists.**
Open it and confirm. If a control is not actually covered, say
`coverage: "manual"` or leave it out. Never write "enforced by middleware" unless
you have read the middleware.

### Not to be confused with `build-security-dashboard`

This skill **adopts** C2's dashboard in a client application: copy the tool, run
`init`, author the evidence, wire CI, generate their playbook. Use
`build-security-dashboard` only when someone needs to *implement* the dashboard
itself — a native port for a team that cannot take a Go dependency.

### The client app is probably not Go

The dashboard is a Go program, but it only *reads* JSON, so a Node/Python/PHP app
uses it unchanged — it just needs a Go toolchain on the machine that runs the
scan (CI runner or a developer laptop). Confirm that is acceptable early. If it
is not, stop and discuss: a vendored prebuilt binary or a container step are the
options, and both are worse than installing Go.

## Phase 1 — Interview

Use `AskUserQuestion` for the structured choices, batched plain questions for the
specifics.

**1. The application**
- Name, one-line description, repository, team, security contact (these become
  `app` in `config.json`).
- Stack and layout: languages, package managers, which directories hold what.
- Release cadence — this sets what "every release" means for task scheduling.

**2. How it connects to C2** — this shapes the playbook's core section:
- **OIDC relying party?** Which flow, and where the `id_token` is validated.
- **Partner API?** Notifications, invoices, or both — and whether it authenticates
  with `private_key_jwt` or a client secret.
- **Payment callbacks?** Where the `status_token` receiver lives.
- **A Trust Node / adapter?** Then the SDK's inbound verifier is in scope.
- **Service-card callouts?** Then it verifies C2-signed callout assertions.

**3. Data and compliance**
- What personal or sensitive data it holds.
- Which regimes apply (PIPEDA / GDPR / HIPAA / PCI-DSS / a provincial standard).
  Their framework docs go in `security/compliance/` as markdown; `init` records
  them, and **you** author the controls they imply.
- ASVS level to target (L1 is the honest default for most municipal apps; L2 if
  they hold health or financial data).

**4. Pipeline logistics**
- CI provider. GitHub Actions gets C2's two workflows adapted; anything else,
  translate the same steps.
- Is there a staging URL for DAST, and a scanner credential? Without both, the
  ZAP job must be omitted rather than committed broken — C2's own is skipped
  because `STAGING_BASE_URL` is unset, which is easy to mistake for passing.
- Who owns manual tasks (the `owner` field), and are they willing to run them.

## Phase 2 — Generate

### a. The dashboard

Copy `cmd/security-dashboard/` into their repo. Do not copy `security/` — `init`
creates it:

```
go run ./cmd/security-dashboard init      # detect the stack, write config.json
go run ./cmd/security-dashboard scan      # run everything, render the page
```

Review what `init` proposed **with the user**. Decline a suggestion with
`"disabled": true` and a `"reason"` — never by deleting it, or the next `init`
resurrects it and the decision is lost.

### b. CI

Adapt C2's `.github/workflows/ci.yml` and `security-nightly.yml`:

- **Per-PR**: build, vet/lint, tests, dependency audit, secret scan, SAST.
- **Nightly**: deep SCA, CodeQL, and DAST only if a staging target exists.
- `security-dashboard init --check` as a gate, so the manifest cannot silently
  drift from the stack.
- **Every module gets a step.** C2 shipped for months with its SDK untested in CI
  because a nested Go module is invisible to a root `./...`. Check for nested
  modules, workspaces, or a second package manifest, and give each its own step.

### c. The compliance mapping

Author `standards[]` and `asvs.requirements` yourself, citing real files and
tests, per the rule above. Link each framework doc with `"framework": "<doc id>"`.
Re-run `init --check` — the "no authored controls" warning should clear.

### d. `security/tasks.json` — the manual programme

Cadence is enforced, not decorative: `lastRun` plus `cadence` yields a due date
and a state, and completion is recorded with

```
go run ./cmd/security-dashboard task done <id> --owner <name> --note "<finding>"
```

which writes the stamp into `tasks.json`, so **git history is the audit trail**.
Set `status: "automated"` on anything a check in `checks[]` now performs, so it
leaves the human schedule instead of sitting permanently amber. Tell the user
`task list` shows the schedule in a terminal.

### e. Their playbook — generated, not copied

Write `docs/security-testing-manual.md` **for their app**, following C2's
structure but with the content re-derived:

- **§A Access control** — their resources and their tenancy model. Push toward
  automation: a test that walks the router and drives every user-scoped route as
  a non-owner beats a hand-maintained list, because the list is what goes stale.
- **§B C2 integration** — this replaces C2's federation section, and it is the
  highest-value part. Cover only what applies:
  - **`id_token` validation**: pin the algorithm rather than reading `alg` from
    the token; verify against C2's JWKS (discover `jwks_uri` from
    `{C2_ORIGIN}/oidc/.well-known/openid-configuration`; it resolves to
    `{C2_ORIGIN}/api/oauth2/jwks.json` — note the `/api`); check `iss`, `aud` ==
    their `client_id`, and `nonce`; and **require `exp` to be present**, because
    most JWT libraries reject only an already-expired token and accept one with
    no `exp` at all.
  - **PKCE and `state`**: `S256` challenge sent and `state` compared exactly.
  - **Client assertions** (partner API): `exp` is required and capped at **10
    minutes**; mint per call rather than caching.
  - **Payment `status_token`**: same verification discipline, plus `aud` == their
    **application id**, which is what stops another tenant's token being replayed
    at them. And the callback is delivered **once, with no retry** — so polling
    `GET /partner/invoices/{id}` is a requirement, and the handler must be
    idempotent.
  - **Consent**: a `403` before consent exists is normal, not a fault.
- **§C Exploratory** — Burp against an authenticated session; the automated DAST
  is the floor, not the ceiling.
- **§D Business logic** — their money and state transitions. Name the invariants:
  non-positive amounts, replayed callbacks, token reuse, privileged overrides
  being audited.
- **§E Infrastructure** — TLS and security headers **on the deployed edge**.
  Headers set only in a vhost file are invisible to a scan of the app, and to any
  deployment that does not use that file. Prefer application middleware, and
  verify against the running host rather than the config in git.
- **Cadence summary** plus the `task done` commands.

Where a check is automatable, automate it and mark the task `automated` — a
quarterly reminder to do something a test could do on every push is waste.

## Phase 3 — Verify

Evidence before assertions. Do not report a pipeline as working until:

1. `go run ./cmd/security-dashboard init --check` — no drift.
2. `go run ./cmd/security-dashboard scan` — every configured check either runs or
   is recorded as skipped with an install hint. **A check that silently passes
   because its tool is missing is worse than no check**; confirm each result is
   real by reading `security/runs/<newest>.json`.
3. Their own build, lint and test commands pass.
4. If you generated an authz/IDOR test, **mutation-test it**: break the guard,
   confirm the test fails, restore. A test that cannot fail is not evidence.
5. CI config parses (`actionlint`, or the provider's validator).
6. `security/dashboard.html` opens and shows the posture, the checks, the ASVS
   table and the task schedule.

## Finish

- Open `dashboard.html` with the user and walk the sections — this is the artifact
  they will send a customer.
- State plainly what is automated, what stays manual, and when each manual task is
  next due.
- Flag anything you could not evidence, rather than filling the gap with a
  plausible sentence. An honest "evidence pending" is what makes the rest
  credible.
- Remind them the badge (`security/badge.svg`) embeds in their README, and that a
  connected app can report into C2's admin Health dashboard — see
  `docs/health-report-guide.md`.
- Only commit if the user asks; if you do, end with the repo's Co-Authored-By
  trailer.
