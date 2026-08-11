# Service Card Callout — Integration Guide

For developers of a **client application** that TrustIdentity links to via a
Service Card. This explains the HTTP endpoint you implement so TrustIdentity can
show the citizen a live, personalized detailed view of your service.

---

## 1. What the callout is

When a citizen opens the detailed view of your Service Card, TrustIdentity makes
a **server-to-server GET request** to a "callout" URL you host, and renders the
JSON you return (your title, a personalized description, contact info, and task
links). You provide **per-citizen** content — e.g. *"You have 1 active
reservation, site B-14"* plus a link into your app.

Key properties:

- **Per-citizen.** Each request identifies one citizen (see §4). Return *that*
  citizen's data.
- **Consent-gated.** TrustIdentity only calls you while the citizen holds active
  consent for your application. If they revoke consent, the calls stop.
- **Always fresh.** TrustIdentity does **not** cache your response — it calls on
  each render and periodically while the card is on screen. Keep the endpoint
  fast; cache on your side if you need to.
- **Fails safe.** If your endpoint errors, times out, or returns non-JSON,
  TrustIdentity silently falls back to the static card content. A failure never
  breaks the citizen's page — but it also means you should return correct data or
  a clean error, never a partial page.

---

## 2. The request TrustIdentity sends

```
GET https://your-app.example/api/citizens/{sub}/status
Accept: application/json
Authorization: Bearer eyJhbGciOiJSUzI1Ni...-OR- X-App-Key / X-App-Secret (see §3)
```

- **Method:** `GET`. No request body.
- **URL:** whatever callout URL the TrustIdentity administrator configures for
  your card. It may contain the placeholder `{sub}` (and/or `{identityId}`),
  which TrustIdentity replaces with the citizen's subject identifier before
  calling. You choose the URL shape when you give it to the administrator.
- **Timeout:** ~5 seconds (configurable server-side). Exceeding it is treated as
  "unavailable" → fallback.
- **Response limit:** the body must be JSON and **≤ 1 MB**.

---

## 3. Authenticating the request

TrustIdentity authenticates every callout so you can trust it's really them and
which citizen it's about. Two modes — the administrator picks one per
application. **Signed JWT is strongly recommended.**

### 3a. `signed_jwt` (recommended)

TrustIdentity sends a short-lived **RS256 JWT** as a bearer token:

```
Authorization: Bearer <jwt>
```

You verify it against TrustIdentity's **JWKS** — the *same* public keys you
already use to validate OIDC `id_token`s at login, so there's no new secret to
manage.

**JWT header:**

| field | value |
|-------|-------|
| `alg` | `RS256` |
| `typ` | `JWT` |
| `kid` | key id — match it against the JWKS to pick the verifying key |

**JWT claims:**

| claim | meaning |
|-------|---------|
| `iss` | TrustIdentity's OIDC issuer (e.g. `https://portal.example.gov/c2/oidc`) |
| `aud` | **your** OAuth `client_id` — verify this equals your client id |
| `sub` | the citizen's subject identifier (see §4) |
| `scope` | space-delimited scopes the citizen consented to for your app (present when non-empty) |
| `iat` | issued-at (epoch seconds) |
| `exp` | expiry — about 60 seconds after `iat`; reject if past |
| `jti` | unique token id |

**How to verify (do all of these):**

1. Fetch the JWKS. **Discover it — never hard-code the path.**
   `GET <issuer>/.well-known/openid-configuration` → use the returned `jwks_uri`
   (it currently resolves to `<issuer>/keys`). Cache the keys and refresh on an
   unknown `kid` (keys rotate, and the path can change — the discovery document is
   the contract, not any fixed URL).
2. Verify the RS256 signature using the JWK whose `kid` matches the token header.
3. Check `iss` equals TrustIdentity's issuer.
4. Check `aud` equals your `client_id`.
5. Check `exp` is in the future (and optionally `iat`/clock skew).

Only after all checks pass, trust `sub`.

> **One origin.** Discovery, the JWKS, and the issuer all live on the **portal
> origin** — the single public host that serves the citizen portal and fronts
> TrustIdentity's API. Point your `issuer` at that origin and let discovery
> resolve `jwks_uri`; every request you make to TrustIdentity (metadata, JWKS)
> goes through that one origin. Don't target an internal API host or port
> directly — it isn't guaranteed to be reachable from your network, and only the
> portal origin matches the `iss` you must verify.

### 3b. `app_key` (legacy fallback)

TrustIdentity sends two static headers carrying credentials the administrator
configured on your application:

```
X-App-Key:    <your app key>
X-App-Secret: <your app secret>
```

Verify both match the values you registered. This is simpler but uses
long-lived shared secrets — prefer `signed_jwt` for new integrations, and serve
only over HTTPS.

---

## 4. Identifying the citizen

The citizen is identified by **`sub`** — in the JWT (`signed_jwt` mode) and/or
substituted into the `{sub}` URL placeholder.

**This is the same `sub` your app received in the OIDC `id_token` when the
citizen logged in.** So map it to your own user record exactly as you do at
login. Do not expect an email address or name in `sub` — it is an opaque,
stable identifier. (`{identityId}` currently resolves to the same value.)

If you don't recognize a `sub`, return an empty-but-valid payload or a 404 — do
not guess.

---

## 5. The response you return

Return `200` with `Content-Type: application/json` and this shape. **Every field
is optional** — omit what you don't have. TrustIdentity uses your values when
present and falls back to the admin-configured card content otherwise.

```json
{
  "title": "Your Campsite Booking",
  "description": "You have 1 active reservation at Riverbend Campground, site B-14, arriving Aug 2.",
  "CTA": "https://your-app.example/reservations",
  "contact": {
    "address1": "500 Riverbend Rd",
    "address2": "",
    "city": "Springfield",
    "state": "ON",
    "postalCode": "K1A 0B1",
    "email": "camping@parks.example",
    "phone": "+1 555 0100"
  },
  "tasks": [
    { "name": "View reservation B-14", "description": "See dates, site and rules", "url": "https://your-app.example/reservations/B-14" },
    { "name": "Add a night", "description": "Extend your stay", "url": "https://your-app.example/reservations/B-14/extend" }
  ]
}
```

| field | type | notes |
|-------|------|-------|
| `title` | string | Card heading. Overrides the static card title. |
| `description` | string | Personalized summary line. |
| `CTA` | string (URL) | **Note the capitalization: `CTA`.** A single primary link, used when you return no `tasks`. |
| `contact` | object | Any subset of `address1`, `address2`, `city`, `state`, `postalCode`, `email`, `phone`. Renders a "Contact us" block. |
| `tasks` | array | Zero or more actions. Each: `name` (label), optional `description`, optional `url` (opens in a new tab). Rendered as a list of buttons. |

Notes:
- Keep `url`s absolute (`https://…`). They open in a new browser tab.
- If you return `tasks`, they take priority over `CTA`.
- Return only data the citizen is entitled to see — TrustIdentity displays it
  as-is.

---

## 6. Behavior & guarantees

- **Consent is the gate.** TrustIdentity calls you only while the citizen's
  consent to your application is active. On revocation, calls stop immediately —
  you don't need to track consent yourself for this endpoint, but you must still
  honor your own data-access rules.
- **No caching by TrustIdentity.** Expect repeat calls (per render + periodic
  refresh). Make the endpoint idempotent and quick.
- **Fail safe.** On any error, timeout, non-2xx, or unparseable body,
  TrustIdentity shows the static fallback. Prefer returning a clean 4xx/5xx over
  a malformed 200.
- **HTTPS only.** Especially required for `app_key` mode.

---

## 7. Quick checklist

- [ ] Host a `GET` endpoint at the callout URL you give the administrator (use a
      `{sub}` placeholder if you want the id in the path).
- [ ] `signed_jwt`: verify the bearer JWT against TrustIdentity's JWKS —
      signature, `iss`, `aud` (= your `client_id`), `exp`.
- [ ] `app_key`: verify `X-App-Key` / `X-App-Secret`.
- [ ] Map `sub` to your user (same as your OIDC login).
- [ ] Return the JSON in §5 (all fields optional; `CTA` is capitalized).
- [ ] Respond within ~5s and keep the body ≤ 1 MB.
- [ ] Serve over HTTPS.

---

## 8. Example (Node.js / Express, `signed_jwt`)

Uses [`jose`](https://github.com/panva/jose) for JWKS-based verification.

```js
import express from "express";
import { createRemoteJWKSet, jwtVerify } from "jose";

const ISSUER = "https://portal.example.gov/c2/oidc";       // TrustIdentity OIDC issuer (the portal origin)
const CLIENT_ID = "your-client-id";                          // your registered client_id
// Discover jwks_uri from <ISSUER>/.well-known/openid-configuration; it currently resolves to <ISSUER>/keys:
const JWKS = createRemoteJWKSet(new URL("https://portal.example.gov/c2/oidc/keys"));

const app = express();

app.get("/api/citizens/:sub/status", async (req, res) => {
  // 1. Authenticate the callout.
  const auth = req.header("authorization") || "";
  const token = auth.startsWith("Bearer ") ? auth.slice(7) : null;
  if (!token) return res.sendStatus(401);

  let claims;
  try {
    ({ payload: claims } = await jwtVerify(token, JWKS, {
      issuer: ISSUER,
      audience: CLIENT_ID,
    })); // also enforces exp
  } catch {
    return res.sendStatus(401);
  }

  // 2. Identify the citizen. claims.sub === the id_token sub from login.
  const user = await findUserByTrustIdentitySub(claims.sub);
  if (!user) return res.json({}); // unknown → empty (still 200), or 404

  // 3. Return this citizen's data.
  const booking = await getActiveBooking(user);
  res.json({
    title: "Your Campsite Booking",
    description: booking
      ? `You have 1 active reservation at ${booking.park}, site ${booking.site}.`
      : "You have no active reservations.",
    CTA: "https://your-app.example/reservations",
    contact: { email: "camping@parks.example", phone: "+1 555 0100" },
    tasks: booking
      ? [{ name: `View reservation ${booking.site}`, url: `https://your-app.example/reservations/${booking.id}` }]
      : [],
  });
});
```

For `app_key` mode, replace step 1 with a comparison of the `X-App-Key` /
`X-App-Secret` headers against your registered credentials.

---

*Questions about issuer URLs, your `client_id`, or callout configuration go to
your TrustIdentity administrator.*
