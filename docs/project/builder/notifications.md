# Notifications — Integration Guide

For developers of a **client application** that needs to notify a citizen through
TrustIdentity. This is the inbound counterpart to the [Service Card Callout
Guide](./service-card-callout-integration.md): the callout is TrustIdentity
*pulling* display data from you; this is you *pushing* a notification to a
citizen's TrustIdentity inbox (and, if they've opted in, their email / SMS).

---

## 1. What it does

Your application makes a **server-to-server** call to TrustIdentity to send one
citizen a notification. TrustIdentity:

1. **Authenticates** your application with its OAuth client credentials.
2. **Checks consent** — the citizen must currently hold active consent for your
   application (established when they accepted your service's terms). No active
   consent → the call is refused and nothing is sent.
3. **Creates an in-app notification** in that citizen's TrustIdentity inbox.
4. **Delivers** the notification over the channels the citizen has **opted into
   for your service** — email and/or SMS. In-app is always created; email/SMS
   only happen on the citizen's opt-in.

This is a machine-to-machine action affecting a citizen, so it is **consent-gated
by design** — the same rule as the callout.

---

## 2. Endpoint

```
POST https://portal.example.gov/c2/partner/notifications
Authorization: Basic base64(<client_id>:<client_secret>)
Content-Type: application/json
```

- Mounted at `<base>/partner` — a sibling of the OIDC endpoints, **not** under
  `/api` (that surface is for citizen sessions). Ask your TrustIdentity
  administrator for the exact base URL.
- **HTTPS only.** You are sending credentials and citizen-facing content.
- Rate-limited per source IP.

---

## 3. Authenticating

Authenticate as the **same confidential OAuth client** you use for OIDC login
(bound to your Application in TrustIdentity, and active). Two methods — pick one.
**Private-key JWT is recommended** (no shared secret on the wire).

### 3a. Private-key JWT — `X-Client-Assertion` (recommended)

Sign a short-lived JWT ("client assertion", RFC 7523) with your private key and
send it in the `X-Client-Assertion` header. TrustIdentity verifies it against
**your registered JWKS** — you host a JWKS URL and register it with your
administrator; TrustIdentity fetches your public keys from it.

```
X-Client-Assertion: <jwt>
```

Assertion claims:

| claim | value |
|-------|-------|
| `iss` | your `client_id` |
| `sub` | your `client_id` (must equal `iss`) |
| `aud` | this deployment's issuer, e.g. `https://portal.example.gov/c2` (ask your administrator) |
| `exp` | short — a minute or two out; TrustIdentity allows ~1 min leeway |
| `iat` | issued-at |
| `jti` | unique id per assertion |

The JWT header must carry the signing key's `kid` (matching a key in your JWKS)
and `alg: RS256`. Because `aud` is checked, an assertion can't be replayed
against another audience. This reuses the exact trust model the callout uses in
reverse — there you verify *our* JWTs via *our* JWKS; here we verify *yours* via
*yours*.

### 3b. HTTP Basic — `client_secret`

Standard OAuth client-secret auth:

```
Authorization: Basic base64("client_id:client_secret")
```

Your `client_secret` is stored only as a hash by TrustIdentity; keep your copy
secret and rotate it via your administrator if exposed.

Either way, bad/missing credentials or an inactive client → `401 Unauthorized`.

---

## 4. Request body

```json
{
  "sub": "b3f1c2a4-…",
  "subject": "Your permit is ready",
  "body": "Permit #4471 for 12 Oak St has been approved. Print it any time from your account.",
  "shortBody": "Permit #4471 approved.",
  "category": "BUSINESS"
}
```

| field | required | notes |
|-------|----------|-------|
| `sub` | yes | The citizen's subject identifier — **the same `sub` your app received in the OIDC `id_token` at login.** This is how TrustIdentity knows which citizen; you never send an email address or name. |
| `subject` | yes | Notification title (shown in the inbox and as the email subject). |
| `body` | yes | Full notification text (inbox body / email body). |
| `shortBody` | no | Short form used for SMS (falls back to `body` if omitted). Keep it under one SMS segment. |
| `category` | no | `BUSINESS` (default) or `PROMOTIONAL`. |

---

## 4b. Sending from a template (recommended)

Rather than composing copy yourself, send a **template key** and let an
administrator own the wording:

```json
{
  "sub": "b3f1c2a4-…",
  "templateKey": "permit-ready",
  "message": "Permit #4471 for 12 Oak St has been approved.",
  "category": "BUSINESS"
}
```

| field | required | notes |
|-------|----------|-------|
| `templateKey` | yes (this form) | The template's key, agreed with your TrustIdentity administrator. Mutually exclusive with `subject`/`body`. |
| `message` | no | Free text interpolated into the template wherever it uses `${message}`. |

**Why prefer this.** The administrator can rewrite the message — tone, wording,
accessibility, translation — without you redeploying. You supply the facts; the
Platform supplies the phrasing.

**A template key belongs to your application.** Keys are not a global
namespace — another application's key is not visible to you and returns
`400`, so guessing one cannot send their message.

### Template parameters

A template may personalise with the citizen's profile using `${…}` placeholders:

| placeholder | scope required |
|-------------|----------------|
| `${firstName}`, `${lastName}`, `${name}`, `${salutation}`, `${birthdate}` | `profile` |
| `${email}` | `email` |
| `${phone}` | `phone` |
| `${address}`, `${address.city}`, `${address.line1}`, … | `address` |
| `${message}`, `${serviceName}` | none — you supply the first, we supply the second |

**Each profile placeholder is gated by the consent your application already
holds** — the same scopes as your OIDC claims. There is no separate permission to
request. A template asking for `${address.city}` when your application has no
`address` consent still sends; that placeholder simply renders empty. The message
goes out with less personalisation rather than failing, and the omission is
recorded for the administrator.

---

## 5. Consent gate

TrustIdentity sends the notification **only if the citizen currently holds active
consent for your application**. Consent is established when the citizen accepts
your service's terms in their portal, and it is withdrawn when they unlink the
service or revoke consent.

- No active consent (or never consented) → `403 Forbidden`, nothing created or
  sent. This is expected; handle it by not retrying until the citizen re-consents.
- You do not manage consent through this API — it's driven entirely by the
  citizen in their TrustIdentity portal.

---

## 6. Delivery channels

- **In-app** is always created (the citizen sees it in their TrustIdentity inbox).
- **Email / SMS** are sent only on the channels the citizen has **opted into for
  your service card(s)**, and only when they have a **verified** primary email /
  phone on file. Push is not currently supported.
- **SMS never carries the message.** It says only that a message arrived and from
  whom — *"You have a new message from Permit Portal. Sign in to view it."* — and
  the citizen signs in to read it. A text lands unencrypted on a lock screen, and
  often on a shared or lost device, so this holds even for a message that would
  have fitted in one segment. Do not rely on SMS to convey content.
- **Email** uses the full template, rendered from its authored format (Markdown,
  HTML or plain text) with both an HTML and a plain-text part.
- Delivery is **best-effort**: a transient email/SMS failure does not fail your
  API call (the in-app notification still exists, and email/SMS ride a durable
  retry queue). The response tells you which channels were dispatched.

You don't choose the channels — the citizen does, through their notification
settings. Send one clear message; TrustIdentity fans it out per their prefs.

---

## 6b. Delivery providers (administrators)

Email and SMS each go out through one **adapter**, chosen and configured in the
admin console under **Communication → Providers**:

| channel | adapters |
|---------|----------|
| Email | **Log only** (writes to the server log, sends nothing) · **Resend** |
| SMS | **Log only** · **Twilio** |

Templates live alongside them under **Communication → Templates**, where each
carries a **key** (what your application sends) and a **body format** —
Markdown, HTML or plain text.

- An **enabled** provider configured in the console overrides the environment
  (`C2_MAIL_PROVIDER`, `C2_SMS_PROVIDER`, …). With none configured the
  environment is used unchanged, so an existing deployment keeps sending after
  upgrade and local development stays zero-config.
- A **disabled** row is configuration parked for later, not an instruction to
  stop sending. To stop delivering on a channel, enable its **Log only** adapter.
- Credentials (Resend API key, Twilio auth token) are **encrypted at rest** under
  the same key that protects the IdP signing keys, are never returned by the API,
  and are left untouched when an unrelated setting is saved.
- A provider that authenticates cannot be enabled without a credential — the
  console would otherwise show it enabled while nothing was delivered.
- **Send a test message** from the provider screen to exercise the saved
  configuration immediately. It sends through the configuration as saved, so a
  wrong credential is reported there and then — with the provider's own error
  message — rather than surfacing later as a citizen's notification quietly not
  arriving. It goes to **the signed-in operator's own address** — there is no way
  to direct it elsewhere, so a configuration check cannot be used to send mail
  or texts to an arbitrary recipient. Every test send is recorded in the audit
  log, including who asked for it and where it went.

This is transparent to your application: you send the same request either way.

---

## 7. Responses

| status | meaning |
|--------|---------|
| `202 Accepted` | Notification created. Body: `{ "notificationId": "…", "channels": ["EMAIL","SMS"] }` — `channels` lists what was dispatched beyond in-app. |
| `400 Bad Request` | Missing `sub`; neither `templateKey` nor `subject`+`body`; **both** `templateKey` and `subject`/`body`; an unknown `templateKey`; or invalid JSON. |
| `401 Unauthorized` | Missing/invalid client credentials, or inactive client. |
| `403 Forbidden` | The citizen has no active consent for your application (or your client isn't bound to an application). |
| `404 Not Found` | No citizen matches `sub`. |
| `429 Too Many Requests` | Per-IP rate limit exceeded — back off and retry. |

---

## 8. Quick checklist

- [ ] Use your confidential OIDC client's `client_id` / `client_secret` via HTTP
      Basic, over HTTPS.
- [ ] Send `sub` = the citizen's OIDC `id_token` subject.
- [ ] Provide either a `templateKey` (preferred) **or** `subject` + `body` — not
      both. A template's wording stays under the administrator's control.
- [ ] Do not rely on SMS to carry content: it only says a message arrived.
- [ ] Expect `403` when the citizen hasn't consented / has unlinked — don't retry
      blindly.
- [ ] Treat `202` as success; the `channels` array is informational.

---

## 9. Examples

### curl

```sh
curl -sS -X POST https://portal.example.gov/c2/partner/notifications \
  -u "$CLIENT_ID:$CLIENT_SECRET" \
  -H "Content-Type: application/json" \
  -d '{
    "sub": "b3f1c2a4-...",
    "subject": "Your permit is ready",
    "body": "Permit #4471 for 12 Oak St has been approved.",
    "shortBody": "Permit #4471 approved.",
    "category": "BUSINESS"
  }'
# -> 202 {"notificationId":"...","channels":["EMAIL"]}
```

### Node.js

```js
async function notifyCitizen(sub, subject, body, shortBody) {
  const auth = Buffer.from(`${process.env.CLIENT_ID}:${process.env.CLIENT_SECRET}`).toString("base64");
  const res = await fetch("https://portal.example.gov/c2/partner/notifications", {
    method: "POST",
    headers: { Authorization: `Basic ${auth}`, "Content-Type": "application/json" },
    body: JSON.stringify({ sub, subject, body, shortBody, category: "BUSINESS" }),
  });
  if (res.status === 403) return; // citizen hasn't consented / unlinked — skip
  if (!res.ok) throw new Error(`notify failed: ${res.status}`);
  return res.json(); // { notificationId, channels }
}
```

### Node.js — private-key JWT (recommended)

Uses [`jose`](https://github.com/panva/jose) to sign the assertion with your
private key; TrustIdentity verifies it against your published JWKS.

```js
import { SignJWT, importPKCS8 } from "jose";

async function clientAssertion() {
  const key = await importPKCS8(process.env.CLIENT_PRIVATE_KEY_PEM, "RS256");
  return new SignJWT({})
    .setProtectedHeader({ alg: "RS256", kid: process.env.CLIENT_KID })
    .setIssuer(process.env.CLIENT_ID)
    .setSubject(process.env.CLIENT_ID)
    .setAudience("https://portal.example.gov/c2") // this deployment's issuer
    .setIssuedAt()
    .setExpirationTime("2m")
    .setJti(crypto.randomUUID())
    .sign(key);
}

async function notifyCitizen(sub, subject, body, shortBody) {
  const res = await fetch("https://portal.example.gov/c2/partner/notifications", {
    method: "POST",
    headers: {
      "X-Client-Assertion": await clientAssertion(),
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ sub, subject, body, shortBody }),
  });
  if (res.status === 403) return;
  if (!res.ok) throw new Error(`notify failed: ${res.status}`);
  return res.json();
}
```

---

## Notes & current limitations

- **`sub` is the OIDC subject**, identical to the one used by the callout — map
  it to your user exactly as you do at login.
- **Every call is recorded** in TrustIdentity's tamper-evident audit log — both
  successful sends and consent-denied attempts (with your `client_id` and the
  target citizen). Sends are accountable.
- **No push channel** — email and SMS only.
- **One recipient per call.** There is no bulk endpoint yet.
- Notification content is shown to the citizen as-is; send only what they're
  entitled to see.

*Get your `client_id`, `client_secret`, and the base URL from your TrustIdentity
administrator.*
