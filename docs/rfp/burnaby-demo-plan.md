# Burnaby #177-08-26 — Master Plan for a Winning Demo

**Written:** 2026-09-02 · **Corrected:** 2026-09-07 (§0) · **Companion:** `burnaby-gap-analysis.md` · **Tracking ticket:** CIT-1

---

## 0. Read this first — the bid is live

**The RFP is still open.** September 2 was the deadline for submitting questions to the City, not
for submitting a proposal.

That is a correction to what this document said when it was written on 2026-09-02, and the reason it
was wrong is worth keeping: §1.3.1 of the RFP labels September 2 as the "RFP Closing Date", and
that table is the only schedule the issued PDF contains. It also says, in the same clause, that the
dates are *estimated and subject to change at the sole discretion of the City*, and §2.1 and §6.8
make addenda published on bids&tenders the operative source. **The document in the repository is
not the current state of the procurement.** Anyone planning against it should check the portal.

### What this changes

Everything in this plan was written for the wrong half of the process — a demonstration after a
submitted proposal. It is now **pre-submission work with a live deadline**, which reorders what
matters:

- **The response matrices are deliverables, not internal artefacts.** Appendix G and H (CIT-47) and
  the Appendix I security questionnaire (CIT-44) are things the City will actually read and score.
  They move from "write as the features land" to "on the critical path".
- **The demo becomes supporting evidence rather than the main event.** It still matters — a working
  demo is what makes a matrix believable — but a proposal is what gets scored.
- **Scope discipline gets harder and more important.** Every Missing requirement is now something to
  answer honestly in a scored document rather than something to build before a meeting. See §6: an
  honest "Configuration" beside a working demo beats an "Out-of-the-box" that collapses in a
  reference call, and that is now a bid-losing rather than a reputational risk.

### The dates

| Milestone | Date | From 2026-09-07 |
|---|---|---|
| **RFP closing** | **2026-11-11** | 65 days · 9.3 weeks |
| Submission buffer — final read, appendices assembled, addenda acknowledged | 2026-11-06 | 60 days |
| Response pack complete | 2026-11-01 | 55 days |
| Demo rehearsed end to end | 2026-10-30 | 53 days |
| **Feature freeze for anything the matrix claims** | **2026-10-25** | 48 days |

Confirmed 2026-09-07. September 2 was the question deadline; the closing date moved to November 11.

**The feature freeze is the date that governs the build.** Nothing built after it can honestly be
claimed as Out-of-the-box in the Appendix G matrix, because there is no time left to demonstrate it.
Work after that date is for the demo's polish and for the next engagement, not for the response.

### One thing to check, not assume

**The closing date moved, which means an addendum was issued.** RFP §6.8.3 requires every addendum
to be acknowledged in the response, and an unacknowledged one is a disqualification risk that has
nothing to do with the quality of the bid. Pull the full list from bids&tenders and read each —
addenda amend requirements as well as dates, so one of them may have changed something this plan is
built on.

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

**Dated against the 2026-11-11 close.** Sequenced so a demo exists at the end of every sprint and
each is strictly better than the last, so stopping at any boundary still leaves something coherent.

### Sprint 1 — "The front door" · **complete**

Anonymous, guest and authenticated intake · non-sequential references · catalogue search with
synonyms and typo tolerance · a category hierarchy · public tracking with a second factor and
enumeration-resistant rate limiting · direct email for requesters C2 cannot reach · attachments
scanned before storage · abuse hardening · a versioned collection notice recorded per submission ·
CI running build, accessibility and security on every commit.

Acts 1 and 2 of the demo are real. One item outstanding: publish state and effective dates (CIT-18),
which Sprint 2 needs anyway.

### Sprint 2 — "The City runs it" · to **2026-10-25**, the feature freeze

Act 3, and the argument that wins municipal bids: *can your staff change it without calling the
vendor?* The visual form builder with conditional fields, draft → review → approve → publish
governance with versioned diffs, the three admin personas, configurable portal content.

This sprint carries the most value and the most risk. The form builder is the hard piece — the brief
says so and it is right — and it is the thing most likely to slip past the freeze.

### Sprint 3 — "Trust" · to **2026-11-01**, alongside the response pack

Act 4. The `CrmCaseAdapter` port with a stub and `crm_sync_log`, the WCAG 2.2 AA manual audit, the
PIPEDA obligations, and the Appendix I positions.

### What gets answered rather than built

Nine weeks does not fit everything, and pretending otherwise produces a matrix that fails a
reference call. **Location services (CIT-7) are the deliberate cut.** GIS type-ahead, a boundary
check and map-pin capture are a per-engagement integration against a City's own address data — they
are properly scoped at kick-off with Burnaby's dataset in hand, not guessed at now. Answered as
Development, with the seam described.

The same applies to environment promotion, non-production masking and consent-aware analytics.

**The test of every one of these is the same:** if it cannot be shown working by 2026-10-25, it is
Development or Future Release in the matrix, not Out-of-the-box. That is a decision to make
deliberately per requirement, in writing, rather than one to discover in the week of submission.

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

1. **The closing date is unknown (see §0).** The bid is live, which makes this the sharpest risk on
   the list rather than a resolved one: work is being sequenced against a deadline nobody has
   written down. Commercial, not engineering.
2. **Scope illusion.** Missing requirements will not all be built. The plan deliberately builds the
   ones that appear in the demo and answers the rest as Development/Future Release — and now that
   the bid is live, an over-claim in that matrix is a bid-losing risk, not a reputational one.
3. **The form builder is the hard piece.** The brief says so and it is right. It is the whole of
   Sprint 2's value and the most likely thing to slip.
4. ~~**No CI today.**~~ Fixed (CIT-41). Build, accessibility and security run on every commit, and
   the health bundle is a CI artefact rather than something produced by hand.
5. ~~**Sequential reference numbers ship today.**~~ Fixed (CIT-13).
