# Burnaby #177-08-26 — Master Plan for a Winning Demo

**Written:** 2026-09-02 · **Companion:** `burnaby-gap-analysis.md` · **Tracking ticket:** CIT-1

---

## 0. Read this first — the date

The RFP's own timetable (§1.3.1) is:

| Milestone | Estimated date |
|---|---|
| RFP Issued | August 12, 2026 |
| **RFP Closing Date** | **September 2, 2026** |
| Negotiation and Contracting | September 2026 |
| Kick-Off Meeting | September/October 2026 |

**The closing date is today.** This plan is therefore written for the *demonstration and negotiation*
window that follows a submitted proposal — not for a pre-submission build. Three readings are
possible and they need different things:

- **(a) The proposal went in.** Then this plan is right as written: a demo-ready build for the
  shortlist/negotiation stage in September, with kick-off in Sept/Oct.
- **(b) The proposal did not go in.** Then Burnaby is a *reference* opportunity — the same plan
  builds the product for the next municipal RFP of this shape, and the demo becomes a sales asset.
  Nothing below is wasted; only the deadline pressure changes.
- **(c) A closing extension or addendum applies.** Check bidsandtenders for addenda (§2.1 requires
  proponents to monitor the site).

**This is the one question that must be answered by a human before the schedule below is committed.**
Everything else in this plan proceeds regardless.

---

## 1. Strategy in three sentences

CityConnect is strong exactly where a service-request CRM is hard — durable notification delivery,
SLA maths against a business calendar, hash-chained audit, routing rules with a simulator, reversible
contact merge — and absent exactly where a *public intake portal* is judged: the anonymous front
door, catalogue search, location capture, and configuration governance.

Burnaby's evaluators will click the front door first. So the plan spends its effort on the
**thin, visible layer** in front of a back end that is already deeper than most of the field, and
frames the depth as the reason to trust the front.

The differentiator we lead with is not a feature. It is that **the portal core knows nothing about
the back office** — CityConnect already proves this pattern in production against C2 (identity,
notifications, service-card callout are all behind seams), and the same discipline is what lets a
`CrmCaseAdapter` swap Dynamics 365 for the next municipality's system without touching the portal.

---

## 2. Decision: keep the Go/React stack. Do not port to Angular/Spring Boot.

The brief targets Angular + Spring Boot BFF + PostgreSQL. That is the **hub's internal standard**,
not Burnaby's requirement.

**Evidence:** Appendix H (Non-Functional Requirements) is technology-neutral throughout. It asks the
proponent to declare **On Premise (external) or Cloud Hosting**, and scores every requirement on
*Out-of-the-box / Configuration / Development / Future Release / Not Available*. It asks about
platforms supported, environment segregation, interoperability, uptime measurement and accessibility
("WCAG 2.x") — never about a language, framework or database engine. Appendix I asks about
encryption, residency, tenancy, portability and identity standards — again, no stack mandate.

**Therefore:** porting spends the entire runway rewriting working software into a lower "ability to
meet" score. Every week spent on a port is a week not spent on the anonymous front door, which is
what actually loses marks. Respond with CityConnect as it is, and treat the hub's Angular/Spring
standard as the pattern language we conform to conceptually — same seams, same phasing, same
adapter contract — not as a literal target.

**Consequences to state openly in the response:** MySQL rather than PostgreSQL (Appendix I asks about
encryption at rest and residency, not engine); Go rather than Java (answer the "supported platforms"
question with the deployment shape, not the language).

---

## 3. Build vs. integrate — what C2 gives us free

From `docs/project/builder/`. Where a C2 service exists we integrate; reimplementing it is both
slower and a worse answer, because platform capability is more credible than bespoke code.

| Burnaby requirement | C2 service | Status |
|---|---|---|
| G·1-017 authenticated path, self-registration, credential recovery | `authentication-profile.md` — OIDC + PKCE, back-channel logout | **Already integrated** (`internal/c2/oidc`). Registration and recovery are C2's; we should say so rather than build them. |
| G·1-036 confirmation notification; G·1-042/049 status to the citizen | `notifications.md` — consent gate, in-app + email/SMS fan-out | **Already integrated** (`internal/notifications` → `internal/c2/notify`), durable outbox with retry. |
| G·1-049 status in the citizen's own portal | `application-status.md` — service-card callout | **Already integrated** (`internal/c2/callout`, `GET /api/citizens/{sub}/status`, `ServiceType.CitizenSummaryTemplate`). A live, per-citizen, consent-gated status card is a demo asset most competitors will not have. |
| G·1-046 monitoring; Appendix I security evidence | `security-service-scanning.md` | **Partially integrated** — `cmd/security-dashboard` exists and reports to C2 Health, but **there is no CI pipeline** (`.github/workflows/` is absent). |
| Fee-bearing services | `payments.md` | **Out of scope** for Appendix G. Mention as available platform capability; do not build. |

**Three places C2 does *not* cover Burnaby, and we must own the answer:**

1. **Guests cannot be notified.** `NotificationOutbox.C2Sub` is `not null` and C2 returns 403 without
   active citizen consent. A guest requester (no C2 account) can receive nothing today. G·1-016's
   guest path and G·1-034/036's confirmation require a **direct email channel alongside C2**.
2. **Per-message-type preferences (G·1-065).** C2 models channel opt-in per service card, not
   preference per message type (STATUS_UPDATE / REMINDER / FOLLOW_UP / SURVEY). `ConsentPreference`
   already has the right shape locally; it needs wiring as a *pre-send filter* in front of C2.
3. **SMS never carries content.** Per `notifications.md`, C2's SMS says only that a message arrived.
   If Burnaby expects the case number by text, that is a disclosed constraint, not a defect.

---

## 4. The demo storyboard

Twelve minutes, one continuous story, no slides until the end. Every beat below is a build target.

**Act 1 — the resident who won't sign in (3 min).** Land on the portal. Type "pot hole" (misspelled)
into search; the catalogue returns *Pothole Repair* on a synonym and a fuzzy match. Open the service:
department, what happens next, expected response time. Fill the form — a conditional field appears
because the answer was "on a road", not "on a sidewalk". Drop a pin on the map; the boundary check
confirms it's in Burnaby. Attach a photo from a phone. Submit **without an account**. Get back a
reference like `BBY-7K4M-2QX9` and a confirmation email.

*This act alone closes G·1-011, 012, 013, 016, 023, 025, 027, 031, 034, 035, 036.*

**Act 2 — coming back (2 min).** Return, enter the reference plus the email address used, and see the
status and history. Show the rate limit refusing a scripted enumeration attempt. Then sign in with
C2 and show the same request in the citizen's account, alongside the **live service-card callout** in
their C2 portal — the same request, surfaced by the platform, consent-gated.

*Closes G·1-048, 049, 001, 017; demonstrates C2 as a platform, not a login button.*

**Act 3 — the City runs it themselves (4 min).** As a business user: add a service to the catalogue,
build its form in a visual builder with a conditional rule, and hit **Submit for approval** — it does
not go live. As an administrator: review the diff against the current version, approve, publish.
Refresh the portal; the new service is there. Then show the routing rule simulator sending it to the
right queue, and the SLA clock running against the business calendar.

*Closes G·1-052, 053, 054, 055, 056, 063 — and this is the act that wins it, because "can your staff
change it without the vendor" is the question every municipality has been burned on.*

**Act 4 — the part nobody else shows (3 min).** The audit chain: open the request timeline, then run
`/api/audit/verify` and show the hash chain intact. Open the integration console: the
`CrmCaseAdapter` running as a stub, the sync log, a deliberately failed transaction, and the retry
that clears it. State plainly: Dynamics 365 is a phase-2 swap behind this one interface; nothing in
front of it changes. Close on the security dashboard and the compliance mapping.

*Closes G·1-038…047, 061, 045, 046 — as an architecture argument rather than a feature claim.*

---

## 5. Build order

Sequenced so that **a demo exists at the end of every sprint** and each sprint's demo is strictly
better than the last. If the runway is cut, we stop at a sprint boundary and still have something to
show.

### Sprint 1 — "The front door" (the demo minimum)
Act 1 + Act 2. Anonymous/guest intake, non-sequential references, catalogue search and hierarchy,
public tracking with a second factor and rate limiting, guest email notification, a working malware
scanner. Without this sprint there is no demo.

### Sprint 2 — "The City runs it" (the differentiator)
Act 3. Visual form builder with conditional fields, config governance (draft → review → approve →
publish) with versioned diffs, the three admin personas, configurable portal content and navigation.

### Sprint 3 — "Trust" (the close)
Act 4 + the response pack. `CrmCaseAdapter` port with a stub implementation and `crm_sync_log`,
location services completed (GIS type-ahead, boundary check, map pin with non-map fallback),
WCAG 2.2 AA remediation with axe in CI, the security pipeline, and the Appendix G/H/I response
matrices.

**Parallel throughout:** the response pack (§6) is written as the features land, not afterwards.

---

## 6. The response pack — a first-class deliverable

Appendix H scores every requirement as **Out-of-the-box / Configuration / Development / Future
Release / Not Available**. Our gap analysis already produces exactly this, in a different vocabulary.
Restating it in Burnaby's own scale is cheap and materially changes how the submission reads:

| Our rating | Burnaby's scale |
|---|---|
| Have | Out-of-the-box |
| Have, needs setup | Configuration |
| Partial | Configuration or Development, per item |
| Missing, on the roadmap | Development / Future Release |
| Missing, not proposed | Not Available |

**Never claim Out-of-the-box for something a demo cannot show.** An honest "Configuration" beside a
working demo beats an "Out-of-the-box" that collapses in the reference call.

Appendix I (Security Risk Assessment) needs a real answer per question, and several are strengths:
data portability and machine-readable export; **data residency restricted to Canada** (a PIPEDA
obligation for us regardless); tenancy and segregation; encryption in transit and at rest; BYOK/CMK;
breach-notification timelines (which PIPEDA's RROSH rules and the 24-month incident register already
force us to have); and **enterprise identity integration with Microsoft Entra ID via SAML 2.0 or
OIDC** — note this is about *staff* identity, and our staff auth currently runs through C2, so the
answer is either C2 federation to Entra or a direct OIDC path. One question asks whether customer
data trains AI/ML models; the answer must be explicit and it must be "no".

---

## 7. Open decisions — recommendations, not a survey

From the brief's §9, each with a recommendation so the plan is not blocked:

| Decision | Recommendation |
|---|---|
| Geocoding source | Live geocoding for the demo; per-engagement address extract with a refresh job for production. Ask Burnaby for their authoritative address dataset at kick-off. |
| Mapping platform | A library with an open tile source for the demo so no licensing decision blocks the build. Price the client's preferred platform per engagement. |
| Malware scanning | Self-hosted ClamAV behind the existing `ScanFunc` seam. The seam already exists; only the implementation is missing, and self-hosting avoids a per-engagement vendor dependency. |
| SMS provider | None of ours. C2 owns SMS delivery, and its SMS never carries content — disclose rather than solve. |
| Latency targets | Burnaby left these as "[X]". Propose the numbers ourselves: synchronous reference number under 2s at p95; status sync under 5 minutes. Proposing a number reads as experience. |
| Catalogue mastering | Portal masters the catalogue. It is the configurable surface City staff maintain (G·1-052), and CRM-mastered catalogues make the governance workflow impossible. |
| Hosting region | Canadian region, non-negotiable. Appendix I asks it directly and PIPEDA makes it an obligation. |
| Routing: ours or the CRM's? | **Ours, with an override.** Our routing engine and simulator are a selling point; G·1-043 asks for routing in the back office. Propose portal-side routing that *supplies* routing metadata and defers to the back office when configured to. Say this explicitly rather than scoring ourselves down. |

---

## 8. Risks

1. **Timeline (see §0).** Unresolved commercial question, not an engineering one.
2. **Scope illusion.** 29 Missing requirements will not all be built. The plan deliberately builds the
   ~15 that appear in the demo and answers the rest as Development/Future Release.
3. **The form builder is the hard piece.** The brief says so and it is right. It is the whole of
   Sprint 2's value and the most likely thing to slip.
4. **No CI today.** Everything we want to claim about quality and security assumes a pipeline that
   does not exist. Cheap to fix, embarrassing if asked about.
5. **Sequential reference numbers ship today.** Fix regardless of the outcome of this bid.
