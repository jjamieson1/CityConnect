# Burnaby RFP #177-08-26 — Requirement Gap Analysis

**Assessed:** 2026-09-02 · **Re-rated:** 2026-09-08 · **Against:** CityConnect `main`
**Source:** `docs/Service_Request_Portal_Build_Brief.docx.md` (distilled from Appendix G — Functional Requirements)

Every requirement ID in the brief is rated against what is actually in the repository today, with the
evidence that supports the rating. Ratings are deliberately harsh: **Have** means a Burnaby evaluator
could be shown it working, not that a foundation exists.

| Rating | Count | Meaning |
|---|---|---|
| **Have** | 19 | Working today, demonstrable |
| **Partial** | 25 | Foundation exists, visible work needed |
| **Missing** | 22 | Nothing in the codebase |
| **Inverted** | 1 | G·1-049 — a design decision reverses the rating once a CRM adapter lands |

Counts are read off the tables below rather than kept by hand. The 2026-09-07 summary said 17 / 25 /
25 while its own rows said 14 / 25 / 27; a number in a bid document that disagrees with the evidence
under it is worse than no number.

**Re-rated 2026-09-08**, at the close of Sprint 1. Ten requirements have moved since the first
assessment, and the movement is concentrated in exactly the two areas this analysis called the
weakness.

The **public front door**: what was "a resident cannot report a pothole without a C2 account" is now
three working submission paths, a scanned photo upload, a versioned collection notice recorded per
submission, and a tracking loop the people it was built for can actually reach.

**Discovery**, which the 2026-09-07 pass still named as the thing an evaluator would find first: the
catalogue is now a two-level tree staff arrange, searchable by the words residents actually use
rather than the City's names for its own services, with publish states, seasonal dates and a
staff-ordered shortcut row on the landing page.

Moved to **Have**: G·1-010, G·1-011, G·1-012, G·1-014, G·1-015, G·1-016, G·1-025, G·1-033,
G·1-035, G·1-036, G·1-048. Moved to **Partial**: G·1-013, G·1-021, G·1-055.

What has *not* moved is the honest headline for the response. Three blocks of work stand between
here and a complete answer, and two of the three are what a demo is judged on:

- **The form builder** — §2.3, and G·1-052/053 behind it. An intake form is still edited as raw
  JSON, which is not a surface a business user can be shown.
- **Location services** — §2.4, G·1-027 to G·1-032. Nothing at all: no GIS, no map, no boundary
  check. The columns are ready and the capture is not.
- **Configuration governance** — §2.7, G·1-055 to G·1-057. CIT-18 built the publish *state*;
  the review-and-approve workflow that moves it, the version history and the promotion path
  between environments are all still to come.

The shape of that result is the strategy: CityConnect is strong exactly where a CRM is hard
(workload, SLA, audit, routing, notification durability) and absent exactly where a *public intake
portal* is judged (anonymous front door, discovery, location, governance). See
`burnaby-demo-plan.md`.

---

## 2.1 Portal & Navigation

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-001 Single citizen entry point | **Partial** | `web-portal/` SPA exists (`Portal.tsx`: Landing, Report, MyReports, ReportDetail) and covers discovery→submission→confirmation→history — but **only for a signed-in citizen**. No anonymous or guest path. |
| G·1-002 Consistent branded visual system | **Partial** | Tailwind + shared kit across both SPAs; no per-municipality branding configuration (logo, palette, wordmark). |
| G·1-003 Responsive across devices | **Partial** | Tailwind responsive utilities used; never audited against the core flows on tablet/mobile. |
| G·1-004 Configurable navigation | **Missing** | Navigation is hardcoded in `Portal.tsx`. |
| G·1-005 Persistent help/contact | **Missing** | No help link, no contextual contact block in the request flow. |
| G·1-006 Configurable titles, copy, banners, alerts | **Missing** | All portal copy is literal JSX. |
| G·1-007 Breadcrumbs / location orientation | **Missing** | — |
| G·1-008 Configurable primary/sub sections | **Missing** | — |
| G·1-009 Configurable default landing view | **Missing** | — |

## 2.2 Service Catalogue & Discovery

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-010 Central configurable catalogue | **Have** *(CIT-17, CIT-18)* | `domain.ServiceType` carries code, name, category, description, department, routing default, mapped form, `Synonyms`, and `PublishState` (draft · published · archived) with `EffectiveStart`/`EffectiveEnd`. `catalog.ListServiceTypes` applies publish state and the date window in one query, so a seasonal service cannot be live on one surface and not another. |
| G·1-011 2+ level category hierarchy | **Have** *(CIT-16)* | `domain.ServiceCategory` is a parented, ordered tree (`catalog/categories.go`, depth capped at 3). Existing flat category names are adopted on boot rather than discarded (`seed.adoptFlatCategories`), and the portal groups by the top of the path. |
| G·1-012 Search with synonyms, typo tolerance, relevance | **Have** *(CIT-17)* | `catalog.Search` ranks on name, staff-editable synonyms, category and description, with whole-word and prefix boosts and optimal-string-alignment fuzzy matching. The no-results state offers a way back and a way to reach a person rather than a dead end. |
| G·1-013 Type-ahead suggestions | **Partial** *(CIT-17)* | Results narrow as the resident types, debounced, with the count announced to assistive technology. There is no suggestion dropdown — the ranked list *is* the suggestion. Worth deciding deliberately rather than building: a combobox is a materially harder accessibility surface than a list. |
| G·1-014 Promoted/shortcut services | **Have** *(CIT-18)* | An ordered, staff-editable shortcut row on the landing view. `catalog.SetPromoted` replaces the whole list in one transaction, refuses anything not published and publicly visible, and archiving a service takes it off the front page in the same operation. |
| G·1-015 Contextual service detail before submission | **Have** *(CIT-19)* | `/service/:code` sits between finding a service and filling in its form: category path, description, owning department by its **public** name, and a numbered "what happens next". The expected response time is **computed** from the SLA policy and business calendar on every view (`portal.Expectation`), not typed into a content field, so it stays true when staff change the policy — and it accounts for working hours, which is why the same service reads "within 8 hours" on a Tuesday morning and "within 3 days" at five on a Friday. |
| G·1-067 Related knowledge articles / FAQs | **Missing** *(slot built, CIT-19)* | Still no knowledge surface, and deliberately so: the brief is explicit that we should proxy the CRM's articles read-through rather than keep a copy that drifts. `CatalogEntry.RelatedArticles` and the section that renders it exist and are absent-when-empty, so the adapter epic is a wiring job rather than a redesign. Rated Missing because nothing fills it — a slot is not a feature. |

## 2.3 Request Intake & Forms

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-016 Anonymous + guest, hardened against abuse | **Have** *(CIT-11, CIT-12, CIT-14)* | Both paths ship. Anonymous files with no contact and is explicitly not trackable; guest gives an email and gets a confirmation and tracking. Hardened by a single-use signed form token, a honeypot hidden from sight, keyboard and assistive technology alike, per-endpoint rate limits and idempotent submit. The bot control is shaped by WCAG 2.2 SC 3.3.8 — no puzzle CAPTCHA. |
| G·1-017 Authenticated path: registration, recovery, profile, history, prefs | **Partial → integrate C2** | OIDC+PKCE login, `/portal/me`, and request history all work (`internal/portal`, `internal/c2/oidc`). Self-registration and credential recovery are **C2's job, not ours** (`builder/authentication-profile.md`). Address management and preferences are absent. |
| G·1-018 Configurable forms per service | **Have** | `ServiceType.IntakeForm` → `domain.FormField` (key, label, type, required, options, help, pattern, min, max), rendered by `PortalField` and validated server-side via `catalog.ParseForm`. |
| G·1-019 Scoped launch set, engine scalable | **Have** | Adding a service adds no code. |
| G·1-020 Form opens without losing page context | **Partial** | `Report` is a route, not a dialog. No focus trap, no keyboard dismissal, no return-focus. |
| G·1-021 Configurable contact fields + PI collection notice | **Partial** *(CIT-12, CIT-15)* | The notice is now configuration a municipality owns, versioned rather than edited, with the exact wording shown recorded against each submission (`domain.CollectionNotice`, `domain.RequestNotice`). The **contact fields** are still fixed — that half arrives with the form builder (CIT-25). |
| G·1-022 Client+server validation, accessible field errors | **Partial** | Server-side validation is real. Client-side errors exist but field-level `aria-describedby`/`aria-invalid` association is unverified. |
| G·1-023 Conditional fields/sections | **Missing** | `FormField` has no `conditionalOn` / show-when rule. |
| G·1-024 Max length + profanity filtering | **Partial** | `pattern`, `min`, `max` supported. No profanity filter. |
| G·1-025 Attachments incl. camera capture, mandatory malware scan | **Have** *(CIT-26)* | Scanning happens **before** storage: uploads land in quarantine, stream to clamd, and are promoted into the served tree only on a clean verdict. Infected files are deleted; unscanned ones stay quarantined and are never served, while the request is still accepted. Portal upload with `capture="environment"` so a phone opens the camera. Nothing can record `"skipped"` any more. |
| G·1-026 Cancel/exit without unintended submission | **Partial** | Navigable away; no explicit confirm-discard. |

## 2.4 Location Services

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-027 Manual entry + boundary check + referral messaging | **Partial** | `Request` carries address1/2, city, state, postalCode, ward, parcelId, lat/long. No jurisdiction boundary check. |
| G·1-028 Address type-ahead from authoritative GIS | **Missing** | No GIS integration of any kind. |
| G·1-029 Validate against approved address data | **Missing** | — |
| G·1-030 Device geolocation with denial handling | **Missing** | — |
| G·1-031 Map pin selection + non-map fallback | **Missing** | No mapping library in either `package.json`. |
| G·1-032 Confirmed location populates the request | **Partial** | Columns exist and `reports.Geo` already aggregates by them — the destination is ready, the capture is not. |

## 2.5 Submission & Notifications

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-033 Gated submission, no duplicate submits, data preserved | **Have** *(CIT-14)* | Server-side validation gates submission; the portal sends an `Idempotency-Key` stable for the life of one form, so a double click replays the first result rather than dispatching a second crew. A failed submission leaves the form filled in. |
| G·1-034 Configurable confirmation with case number, next steps, expected response | **Partial** *(CIT-19)* | Reference number is returned, and the expected response is now surfaced to residents pre-submission from the real policy. The **confirmation copy itself is still not configurable** — that is CIT-23, and it is the half this rating turns on. |
| G·1-035 Unique **non-sequential** reference number | **Have** *(CIT-13)* | `requests.NewReference` draws 8 symbols of Crockford base32 from `crypto/rand` — `BBY-7K4M-2QX9` — with the prefix configurable per deployment (`CC_REFERENCE_PREFIX`) and a redraw on the unique-index collision. Lookup folds O/I/L so a reference survives being read down a phone. `ccadm reissue-references` converts historical rows. |
| G·1-037 Structured, queryable submission data | **Have** | `Request.FormData` JSON + full reporting layer over it. |
| G·1-036 Template-driven confirmation (email, optional SMS) | **Have** *(CIT-21)* | One durable outbox, two transports. C2 for a consented citizen — in-app inbox, consent gate, their own channel preferences — and direct SMTP for a requester C2 cannot reach. Both share the same retry, backoff, duplicate collapsing and operator view. A hard bounce suppresses as `bounced` rather than being retried. SMS remains C2's, and C2's SMS carries no content by design. |
| G·1-065 Notification preferences by channel and message type | **Partial — C2 owns this** | `domain.ConsentPreference` (contact × purpose × channel) exists locally but is not wired to sending. Per `builder/notifications.md`, **C2 owns channel opt-in and the consent gate**; C2 does *not* model per-message-type preference. Genuine delta to disclose. |

## 2.6 Tracking & Support

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-048 Track by reference + second factor, rate-limited, anti-enumeration | **Have** *(CIT-20, CIT-12)* | `POST /api/portal/requests/track` — a POST so the verification value never reaches a URL, browser history or an access log. Constant-time comparison over fixed-length digests. Every failure is byte-identical, so it cannot be used to discover which references exist. Two rate-limit buckets, per address and per reference, both keyed on the *normalised* reference so the O/0 fold cannot multiply attempts. Reachable in practice since CIT-12 gave guests a contact detail to verify against. |
| G·1-049 Status from the system of record only | **Have (today)** | `portal.MyRequest` projects status/updates from the single source of truth. The rating inverts once a CRM adapter makes the back office the SoR. |
| G·1-050 Configurable "reference not found" help | **Missing** | — |
| G·1-051 Configurable contact channels | **Missing** | — |

## 2.7 Administration

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-052 Non-technical staff maintain the catalogue | **Partial** | `/api/service-types` CRUD + `web/src/pages/Admin.tsx`. The intake form is edited as raw JSON — that is not a non-technical surface. |
| G·1-053 Maintain forms, messages, routing, workflow settings | **Partial** | Strong here: `RuleEditor.tsx` + `/api/routing-rules/simulate`, notification templates, SLA policies, business calendars, macros. Form building is the weak link; workflow states are fixed in code. |
| G·1-054 Role-based admin permissions (admin / business user / IT support) | **Partial** | Roles are `readonly \| agent \| supervisor \| admin` (`domain/org.go`). Burnaby's three personas do not map cleanly; no IT-support persona. |
| G·1-055 Draft → review → approval → publish governance | **Partial** *(CIT-18)* | The catalogue now has real publish states — draft, published, archived, plus effective dates — and a new service is created as a **draft** rather than live. What is missing is the workflow that moves between them: no review step, no approver, no separation between the person who edits and the person who publishes. The state field exists so that workflow has something to move. |
| G·1-056 Versioning, approval history, audit trail | **Partial** | The **audit trail is a genuine strength** — `internal/audit` is a hash-chained, append-only log with `/api/audit/verify`. But there is no config versioning, no previous-value snapshot for rollback, no approval history. |
| G·1-057 Controlled promotion across environments | **Missing** | — |

## 2.8 Reporting, Audit & Environments

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-058 Reporting/dashboards | **Have** | `internal/reports` + `Reports.tsx`: volume, SLA, CSAT, agents, geo, trends, rollups. |
| G·1-059 Operational metrics (volume, completion, first response, closure) | **Have** | All four exist, measured against a business calendar with pause-status handling. |
| G·1-060 Data export | **Partial** | `/api/reports/requests/export.csv` and saved-view exports. No configuration or audit export. |
| G·1-064 Consent-aware web analytics | **Missing** | No analytics. Form start/completion/abandonment is not instrumented. |
| G·1-061 Auditability of submissions, status changes, admin activity | **Have (differentiator)** | Hash-chained `AuditLog` + append-only `RequestEvent` timeline + verification endpoint. Stronger than most municipal portals will show. |
| G·1-062 Non-production refresh, masking, access control, monitoring | **Missing** | No masking or refresh tooling. |

## 2.9 Operations

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-046 Monitoring/alerting across portal and integrations | **Partial** *(CIT-41)* | `/healthz`, notification outbox `Stats` with stuck-queue detection, webhook delivery log, `cmd/security-dashboard`. CI now runs build, vet, test, lint and a production build for both SPAs, an axe pass over the citizen portal, and the pinned security scanners, failing on any gating check that did not pass — including one recorded as skipped because its tool was absent. Still no runtime **alerting**: a scan tells us about the build, not about production at 3am. |
| G·1-066 Outage/maintenance messaging with alternate channels | **Missing** | — |

## 2.10 Future Expansion

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-063 New services/forms/workflows/integrations by configuration | **Partial** | Services, forms, routing rules, SLA policies, templates and calendars are all configuration. **Workflow states are fixed in code** (`allowedTransitions` in `domain/requests.go`) and new integrations need code. |

## 2.11 CRM / Case-Management Adapter

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-038 Case creation, no rekeying | **Missing** | No adapter. `internal/webhooks` pushes events outward; nothing creates a case and returns its identifier. |
| G·1-039 Documented field-level mapping | **Missing** | — |
| G·1-040 Contact/account matching or creation | **Partial** | `internal/contacts` has real dedupe and reversible merge (`MergeRecord`) plus `ContactIdentity` — the matching logic exists, aimed at the wrong target. |
| G·1-041 Per-workflow mapping to CRM case type | **Missing** | `ServiceType.C2ServiceCardID` is the exact precedent for the field that is needed. |
| G·1-042 Back office is SoR for status within a latency budget | **Missing** | Status is ours. `Request.ExternalRef` / `OriginSystem` are the hooks. |
| G·1-043 Routing/queue logic lives in the back office | **Inverted — design decision** | Our routing engine (`internal/routing` + rule simulator) is a selling point. Burnaby asks for routing to live in Dynamics. Must be answered deliberately, not silently. |
| G·1-044 Approved, supported components — not scripts | **Partial** | `internal/webhooks` (HMAC signing, exponential backoff, delivery log, replay) is exactly the "supported component" shape asked for. |
| G·1-045 Failed/delayed transactions logged, retried, surfaced | **Have (reusable)** | `WebhookDelivery` + `NotificationOutbox` both do durable retry with an operator-visible log and `/{id}/retry` + `/{id}/replay`. This pattern transfers directly to `crm_sync_log`. |
| G·1-047 Optional decoupled API layer | **Partial** | A REST API with PAT auth (`c2_pat_…`, SHA-256 stored) already exists for external channels. |

---

## Cross-cutting findings

1. ~~**The front door is the gap.**~~ Half closed. Anonymous and guest submission, scanned photo
   upload and reference tracking all work (Sprint 1). **Catalogue search and the map pin do not** —
   and search is the first thing an evaluator touches, so it is now the single most visible hole.
2. ~~**Sequential reference numbers are a live defect**~~ — **fixed** (CIT-13). References are now
   drawn at random, and `ccadm reissue-references` converts what was already in the table.
3. ~~**No CI exists.**~~ Fixed (CIT-41). Build, test, lint, axe against WCAG 2.2 AA and the full
   security scan run on every commit, and `health.json` is a CI artefact rather than something
   produced by hand — so the evidence in the proposal is a copy of what CI observed. The scan also
   now covers `web-portal`, which it did not: the citizen-facing app was going unscanned.
4. **C2 covers more than expected on identity and messaging, and less than expected on preferences.**
   Registration, credential recovery, consent and channel fan-out are C2's. Per-message-type
   preference (G·1-065) is not something C2 models, and no C2 path exists for a guest with no `sub`.
5. **Stack.** The brief targets Angular + Spring Boot + PostgreSQL; CityConnect is React/Vite +
   Go/chi/GORM on MySQL. Recommendation in `burnaby-demo-plan.md`.
