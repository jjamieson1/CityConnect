# Authenticating to C2 (TrustIdentity) — agent integration guide

**Audience:** an AI coding agent (or developer) implementing or fixing authentication
in a **downstream client application** that uses C2 (TrustIdentity) as its identity
provider. Follow these rules literally; they encode mistakes that have already cost
real debugging time.

C2 is a standard **OpenID Connect** provider. There is nothing exotic here — but
three things trip people up every time: using the wrong origin, forcing
re-authentication by accident, and hardcoding URLs instead of discovering them.

---

## 0. Golden rules (do these or nothing works)

1. **One origin.** Talk to C2 *only* through its **portal origin** — the single public
   host that serves the citizen portal and fronts C2's API. **Never** target C2's
   internal API host/port directly. The portal origin is also the token `iss` you must
   verify. (Dev: `http://localhost:5173`. The API on `:8088` is internal — do not use it.)
2. **Discover, don't hardcode.** Read every endpoint (authorize, token, JWKS, …) from
   `GET <issuer>/.well-known/openid-configuration`. Paths change; discovery is the contract.
3. **Authorization Code + PKCE (S256).** Public clients (SPA/mobile) MUST use PKCE.
   Confidential clients (server-side) use PKCE *and* the client secret.
4. **Do NOT force re-authentication unless you mean it.** Do **not** send
   `prompt=login` and do **not** send `max_age=0`. Either one makes C2 re-prompt the
   user for credentials on every visit even when they already have an active C2 session —
   defeating SSO. (This is the #1 "why am I asked to log in again?" cause.)
5. **Verify every token.** Validate signature (JWKS from discovery), `iss`, `aud`, `exp`,
   and `nonce`. Trust `sub` only after all checks pass.

---

## 1. What you need from the C2 administrator

Ask the C2 admin to register your application and give you:

| Item | Notes |
| --- | --- |
| **Portal origin** | e.g. `https://portal.example.gov/c2` (dev: `http://localhost:5173`). Everything derives from here. |
| **`client_id`** | Your OAuth client id. It is the `aud` you must expect in id_tokens and in inbound callout assertions. |
| **`client_secret`** | Confidential (server-side) clients only. Never ship it to a browser. |
| **Registered `redirect_uri`(s)** | Exact-match. C2 rejects any redirect_uri not pre-registered. |
| **Scopes** | Which of `openid profile email phone address offline_access` your app may request. |

**Do not** invent a `client_id`, reuse another app's, or point at a client that belongs to
a different application — C2 signs tokens with `aud` = *your* client, and it must match
what you verify. A mismatched `aud` is a silent `401`/`invalid token`.

---

## 2. Discover the endpoints

```
GET <portal-origin>/oidc/.well-known/openid-configuration
```

Cache the result. Use these fields:

- `issuer` — the exact string you must see in every token's `iss`.
- `authorization_endpoint`, `token_endpoint`, `userinfo_endpoint`, `end_session_endpoint`.
- `jwks_uri` — fetch the signing keys here; cache them and refresh on an unknown `kid`.

> DO NOT hardcode `jwks_uri` or any endpoint path. Read them from discovery every deploy.

---

## 3. Part A — Log a user in (Authorization Code + PKCE)

### 3.1 Build the authorization request

```
GET <authorization_endpoint>
  ?response_type=code
  &client_id=<your client_id>
  &redirect_uri=<one of your registered redirect_uris>
  &scope=openid%20profile%20email          # openid is required; add only what you need
  &state=<random, unguessable>             # CSRF binding — verify on return
  &nonce=<random, unguessable>             # replay binding — must equal id_token.nonce
  &code_challenge=<BASE64URL(SHA256(verifier))>
  &code_challenge_method=S256
```

Rules:
- **DO** generate a fresh `code_verifier` (43–128 chars), store it in the user's session,
  and derive `code_challenge = BASE64URL(SHA256(verifier))`.
- **DO** generate fresh `state` and `nonce` per request and store them for verification.
- **DO NOT** add `prompt=login`. Omit `prompt` entirely for normal SSO. (Use `prompt=none`
  only for a *silent* check — see §3.5.)
- **DO NOT** add `max_age=0` (or any tiny `max_age`) unless you deliberately want to force
  re-auth. Omit it, or set a sane value like `max_age=3600`.

### 3.2 Handle the redirect back

C2 redirects to your `redirect_uri` with `?code=…&state=…`.
- **Verify `state`** equals what you stored. Reject otherwise.
- If you get `?error=…` instead, handle it (see §3.5 for `prompt=none` errors).

### 3.3 Exchange the code for tokens

```
POST <token_endpoint>
Content-Type: application/x-www-form-urlencoded

grant_type=authorization_code
&code=<code from the redirect>
&redirect_uri=<same redirect_uri as in the auth request>
&client_id=<your client_id>
&code_verifier=<the verifier you stored>
# Confidential clients also authenticate the client, e.g.:
# Authorization: Basic base64(client_id:client_secret)
```

Response: `{ access_token, id_token, refresh_token?, token_type: "Bearer", expires_in }`.

### 3.4 Validate the `id_token` (do ALL of these)

1. Verify the RS256 signature with the JWK whose `kid` matches the token header, fetched
   from `jwks_uri`.
2. `iss` **exactly equals** the discovery `issuer`.
3. `aud` **equals your `client_id`**.
4. `exp` is in the future; `iat` is sane.
5. `nonce` **equals** the `nonce` you sent.

Only then map `sub` to your user record. `sub` is an **opaque, stable** identifier — not an
email or name. **This is the same `sub` you'll receive in callout assertions** (Part C), so
store it as your link to the C2 identity.

### 3.5 SSO behavior — what to expect

There is no `prompt` value that *means* "SSO" — SSO is the behavior when `prompt` is
**absent**. Quick reference:

| `prompt` | Behavior | Use for |
| --- | --- | --- |
| *(omitted)* | **Silent SSO if a session exists; login form if not** | ✅ your normal "Log in" |
| `none` | Never shows UI — completes silently, or returns `error=login_required` | background "still signed in?" checks |
| `login` | Forces a fresh login every time (breaks SSO) | step-up before a sensitive action |
| `consent` | Forces the consent screen | re-confirming consent |

`max_age=0` forces re-auth regardless of `prompt` — omit it too (or set a real TTL).

- With no `prompt`/`max_age`: if the user already has an active C2 session, C2 **silently
  re-asserts** and redirects straight back with a `code` — no login form. That is the SSO
  you want. If they have no session, C2 shows its login form, then returns.
- `prompt=none`: C2 **never** shows UI. If there's a usable session it completes silently;
  otherwise it redirects back with `?error=login_required` (or `consent_required` /
  `interaction_required`). Use this for background "is the user still signed in?" checks —
  handle the error by doing a normal (promptless) redirect.
- `prompt=login` / `max_age=0`: C2 **forces** a fresh login. Only use these when you have a
  specific reason (e.g. step-up before a sensitive action).

### 3.6 Requesting extra attributes (Trust Adapter claims)

Beyond the standard OIDC scopes (`profile`, `email`, `phone`, `address`), a C2
administrator can expose extra claims sourced **live from a Trust Adapter's data
bundle** — one bundle field per claim, released only with the citizen's consent.
This is how you receive a verified attribute a partner system holds (e.g. a
residency `status`) without ever seeing the rest of that record.

To receive one, **add the configured scope to your `scope` parameter** (ask your C2
administrator which scopes/claims exist for your app):

```
scope=openid residency
```

On consent, the mapped claim appears in the `id_token` **and** at UserInfo — e.g.:

```json
{ "sub": "…", "residency_status": "active" }
```

Rules for consuming these claims:

- **Absent means unknown, not false.** The claim is **omitted (never fabricated)**
  when the citizen hasn't onboarded with the source system, hasn't consented, or the
  field isn't present. Treat a missing claim as "unverified", not "no".
- **Point-in-time.** The value is fetched fresh from the source at each release and
  is not stored by C2. Don't cache it long — re-request (or call UserInfo) when the
  fact must be current (e.g. at a discount/eligibility decision).
- **You request a scope, not a claim name.** The scope (`residency`) is the unit of
  consent; the claim (`residency_status`) is what the scope releases. One scope may
  release several claims.

Example: a facility-booking app requests `scope=openid residency`; if the citizen
consents and is a Rivermont resident, the token carries `residency_status: "active"`,
which the app uses to apply resident pricing — and nothing else from the residency
record is disclosed.

> Setting these up is an operator task in the C2 console (consent scope on the
> policy, attribute mapping, data-bundle source, and the scope on the client's
> **Allowed scopes**). See the end-to-end runbook: `docs/service-card-setup-runbook.md`.

---

## 4. Part B — Call C2 APIs with the access token

Call C2's API through the **same portal origin**, under `/api`:

```
GET <portal-origin>/api/<resource>
Authorization: Bearer <access_token>
Accept: application/json
```

- The access token is opaque to you — do not try to parse it for claims; use the
  `id_token`/`userinfo` for identity.
- Need fresh user claims? Call `GET <userinfo_endpoint>` with the bearer access token.
- On `401`, the token is expired/invalid → refresh (§5) or re-run the login flow.
- **DO NOT** call the internal API host/port directly; go through the portal origin.

---

## 5. Refresh tokens (optional)

Request `offline_access` scope to receive a `refresh_token`. Refresh with:

```
POST <token_endpoint>
grant_type=refresh_token
&refresh_token=<the refresh token>
&client_id=<your client_id>
# + client auth for confidential clients
```

Store refresh tokens server-side only, encrypted. Never expose them to a browser.

---

## 6. Logout — both directions

Logout has two independent directions. Handle **both** — otherwise a user who signs out in
one place stays signed in in the other.

### 6.1 RP-initiated logout (your app → C2)

When the user logs out of *your* app and you also want to end their C2 session, redirect the
browser to the `end_session_endpoint` from discovery — ideally with the user's `id_token` as
`id_token_hint` and your registered `post_logout_redirect_uri`. Clearing only your local
session leaves the C2 session active, so the user stays SSO'd and can silently re-enter.

### 6.2 IdP-initiated logout (C2 → your app) — Back-Channel Logout

When the user **explicitly logs out** — either at another relying party (RP-initiated
`end_session`) or by signing out of the **C2 portal** — C2 proactively notifies your app so
you can drop the user's session too. C2 implements **OIDC Back-Channel Logout 1.0**.

> **Scope:** C2 fans out back-channel logout on *explicit* logout (RP `end_session` **and**
> C2 portal sign-out). It does **not** fire on a passive **session expiry** — an idle C2
> session timing out does not notify RPs, and that is intentional: a session ageing out is
> not a logout the user asked for. In that case let *your own* session govern the app (see
> the fallback guidance below); do **not** treat C2 simply idling out as a signal to end an
> actively-working user's session.

**Setup:** register a `backchannel_logout_uri` (a server endpoint you host) with the C2
administrator. C2 notifies every client the user currently has an active token with that has
one registered — best-effort and detached (a slow endpoint never blocks C2's logout).

This is a **server-to-server POST**, so the URI must be reachable by C2's backend over the
network — *not* a browser-mediated call. In production that means your app's **public HTTPS
URL**, even when the service itself is loopback-bound behind a reverse proxy: register the
proxied public path (e.g. `https://services.example.gov/api/c2/backchannel-logout`), **not**
the internal `127.0.0.1:PORT` address. (The loopback form only works in dev because C2 runs
on the same host.) Exposing the endpoint publicly is safe — it does nothing without a valid,
signed `logout_token`, so an unauthenticated or forged request just gets a `400`. There is no
need to invent a browser-brokered ("front-channel") logout: C2 doesn't implement one, and a
reverse-proxied back-channel endpoint reaches the same result.

**What C2 sends:**

```
POST <your backchannel_logout_uri>
Content-Type: application/x-www-form-urlencoded

logout_token=<signed JWT>
```

The `logout_token` is an RS256 JWT (`typ: logout+jwt`, `kid` from the **same JWKS** as
id_tokens):

| claim | value |
| --- | --- |
| `iss` | the C2 issuer |
| `aud` | **your `client_id`** |
| `sub` | the user (the **same** `sub` as their id_token) |
| `events` | contains `"http://schemas.openid.net/event/backchannel-logout": {}` |
| `iat`, `jti` | issued-at, unique token id |

C2's logout_token is **`sub`-based — there is no `sid`.** It identifies the *user*, not one
session.

**Your endpoint MUST:**

1. Verify the RS256 signature via `jwks_uri` (match `kid`).
2. Check `iss` == issuer and `aud` == your `client_id`.
3. Check the `events` claim contains the back-channel-logout member above.
4. Confirm a `sub` is present, and **reject the token if it contains a `nonce`** (the spec
   forbids `nonce` in a logout_token).
5. (Optional) dedupe on `jti`; check `iat` is recent.
6. **Terminate ALL local sessions for that `sub`** — you can't target a single one (no `sid`).
7. Respond **`200`** (or **`400`** on a validation failure) with `Cache-Control: no-store`.
   Do not redirect.

**Fallback / defense in depth.** Back-channel delivery is best-effort and only reaches
clients the user still holds an active C2 token with, so don't rely on it alone — but be
deliberate about *which* missed event you're compensating for. They are not the same:

- **A missed *explicit* logout** (your endpoint was briefly down): safe to catch with a
  silent `prompt=none` re-check at a natural boundary — app load or tab focus. If it returns
  `error=login_required`, end the local session.
- **Passive IdP idle expiry is NOT a logout — do not chase it with a poll.** C2's session
  idles out in ~15 minutes and activity in *your* app does **not** reset that timer. A
  periodic `prompt=none`-then-logout would therefore sign out a user who is doing real work
  in your app but hasn't touched C2 — destroying whatever they were in the middle of. Give
  your own session its own sliding idle window that reflects activity *in your app*, and let
  C2's silent expiry stay invisible while the user is active. Re-authentication happens
  naturally the next time SSO is actually needed.
- **Never let a logout of any kind destroy in-progress work.** Treat a `401` as "re-auth
  needed", not "discard everything": preserve the form or draft the user was editing (locally
  or server-side), then let them sign back in and resume. Back-channel logout is server-side,
  so a logout that lands mid-form *will* `401` the next request — handle that gracefully
  rather than dumping their input.

C2 does **not** implement front-channel logout or a session-management `check_session_iframe`
(both absent from discovery), so do not build on those.

---

## 7. Part C — Verify C2's inbound callout assertion (reverse direction)

If your app exposes a **Service Card callout** endpoint, C2 calls *you* server-to-server and
authenticates with a short-lived **RS256 JWT** (bearer). You verify it with the **same
JWKS** you already use for id_tokens.

**Assertion claims:** `iss` = the C2 issuer; `aud` = **your `client_id`**; `sub` = the
citizen (same `sub` as their id_token); `scope` = consented scopes; `exp` ≈ 60s after `iat`;
plus `jti`.

**Verify (all of them):** signature via `jwks_uri`, `iss` == issuer, `aud` == your
`client_id`, `exp` in the future. Only then trust `sub` and map it to your user. Fail closed
(4xx) on any check. Full contract, request/response shape, and an example:
`docs/service-card-callout-integration.md`.

---

## 8. Gotchas

### Quick symptom → fix

| Symptom | Cause | Fix |
| --- | --- | --- |
| Re-prompted for login though already signed in | Client sends `prompt=login` and/or `max_age=0` | Remove both from the authorize request (§0.4, §3.1) |
| `401` / invalid token from C2 (or your callout 401s) | `aud` mismatch — token signed for a different `client_id`, or you're verifying the wrong `client_id` | Ensure C2 signs with, and you expect, the **same** `client_id` |
| Signature/JWKS failure | Hardcoded a JWKS path, or pointed at the internal API host | Discover `jwks_uri`; use the portal origin only |
| `iss` mismatch on verify | Configured issuer as the API host/port | Issuer is the **portal origin** + `/oidc` — take it from discovery |
| Intermittent/blank behavior in dev | Talking to the internal API host directly, or a stale service worker from another app on the same dev port | Use the portal origin; clear site data if a prior app registered a service worker |

### Subtle behaviors that surprise people

- **`prompt=login` / `max_age=0` are added by default in some client libraries.** Inspect
  the authorize URL your library actually builds — a `maxAge: 0` option that "means unset"
  frequently serializes to `max_age=0` and silently kills SSO. This is the single most
  common "why am I logged in again?" cause.
- **The issuer is the portal origin, even when discovery is served from the API.** Fetching
  `.well-known` from the internal API host still returns `iss` = the *portal* origin, and
  every token carries that `iss`. Configure your client's issuer as the portal origin, or
  `iss` validation fails even though discovery "worked."
- **Cookies ignore the port; SameSite is per-*site*, not per-origin.** In dev,
  `localhost:5173` and `localhost:8088` are the same site and share the C2 session cookie —
  the port does not isolate it. **But `127.0.0.1` and `localhost` are different cookie
  hosts:** mixing them (issuer on one, your app on the other) drops the session and breaks
  SSO. Pick one hostname and use it everywhere.
- **A first-time or new-scope request bounces to a consent screen — that's expected**, even
  with an active session. It is not a bug. Under `prompt=none` you get
  `error=consent_required` back instead of any UI.
- **Treat both tokens as opaque except `id_token` claims.** Do not parse the access token
  for identity; use `id_token` / `userinfo`. Do not expect an email or name in `sub`.
- **Redirect URIs are exact-match.** A trailing slash, `http` vs `https`, or a port
  difference is a different URI and C2 rejects it. Register every variant you actually use.
- **One canonical OIDC client per application.** C2's server-to-server callout signs with a
  specific client's `client_id` as `aud`. Extra clients registered under the same app (e.g.
  from conformance/test tooling) can shift which `aud` is used and cause `401`s. Register
  test clients under a *separate* application.
- **A stale service worker from another app on the same dev port will impersonate the
  portal.** If a different PWA once ran on the portal's dev port, its service worker can
  keep serving its cached shell on that origin — you'll see crashes or blank pages that look
  like C2 bugs but aren't. Clear site data (unregister service workers + delete caches) for
  that origin.
- **`token_endpoint` is not `/oidc/token`.** In this deployment it's `/oidc/oauth/token` —
  one more reason to read every endpoint from discovery instead of guessing.
- **Back-channel logout works but is NOT advertised in discovery.** `backchannel_logout_supported`
  is absent from `.well-known`, so you can't feature-detect it — coordinate with the admin,
  register your `backchannel_logout_uri`, and implement §6.2. The `logout_token` is `sub`-based
  (no `sid`), so end *all* of that user's local sessions. There is no front-channel logout or
  session-management iframe.

---

## 9. Verification checklist

- [ ] All C2 traffic goes through the **portal origin**; the internal API host/port is never referenced.
- [ ] Endpoints + `jwks_uri` come from `.well-known/openid-configuration`, not hardcoded.
- [ ] Authorization Code flow with **PKCE (S256)**; `state` and `nonce` generated and verified.
- [ ] Authorize request does **not** contain `prompt=login` or `max_age=0`.
- [ ] `id_token` validated: signature, `iss`, `aud`==client_id, `exp`, `nonce`.
- [ ] `sub` stored as the stable link to the C2 identity (opaque; == callout `sub`).
- [ ] API calls use `Authorization: Bearer <access_token>` against `<portal-origin>/api`.
- [ ] (If applicable) callout endpoint verifies the inbound assertion: sig, `iss`, `aud`==client_id, `exp`.
- [ ] Refresh tokens (if used) are server-side + encrypted; RP-initiated logout uses `end_session_endpoint`.
- [ ] IdP-initiated logout handled: a `backchannel_logout_uri` endpoint validates the logout_token (signature, `iss`, `aud`, `events`, no `nonce`) and ends **all** of that `sub`'s local sessions, returning `200`. Register its **public** proxied URL (not `127.0.0.1`). Do **not** propagate passive IdP idle expiry to an active session, and preserve in-progress work on any logout.

---

## Appendix — dev reference values (example only; always discover)

Portal origin (dev): `http://localhost:5173` — the SPA origin that proxies `/oidc` + `/api`
to the internal API (`:8088`, do not call directly).

```
issuer:                 http://localhost:5173/oidc
authorization_endpoint: http://localhost:5173/oidc/authorize
token_endpoint:         http://localhost:5173/oidc/oauth/token
userinfo_endpoint:      http://localhost:5173/oidc/userinfo
jwks_uri:               http://localhost:5173/oidc/keys
end_session_endpoint:   http://localhost:5173/oidc/end_session
introspection_endpoint: http://localhost:5173/oidc/oauth/introspect
scopes_supported:       openid profile email phone address offline_access
code_challenge_methods: S256
```

*Questions about your `client_id`, redirect URIs, issuer URLs, or scopes go to your C2
administrator.*
