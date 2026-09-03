# Burnaby RFP #177-08-26 — Requirement Gap Analysis

**Assessed:** 2026-09-02 · **Against:** CityConnect `main` (branch `mcp-connection`)
**Source:** `docs/Service_Request_Portal_Build_Brief.docx.md` (distilled from Appendix G — Functional Requirements)

Every requirement ID in the brief is rated against what is actually in the repository today, with the
evidence that supports the rating. Ratings are deliberately harsh: **Have** means a Burnaby evaluator
could be shown it working, not that a foundation exists.

| Rating | Count | Meaning |
|---|---|---|
| **Have** | 12 | Working today, demonstrable |
| **Partial** | 27 | Foundation exists, visible work needed |
| **Missing** | 28 | Nothing in the codebase |

*Updated 2026-09-02: G·1-035 moved Missing → Have (CIT-13).*

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
| G·1-010 Central configurable catalogue | **Partial** | `domain.ServiceType` carries code, name, category, description, department, routing default (`DefaultQueueID`), mapped form (`IntakeForm`), `Active`/`PublicVisible`. **Missing: synonyms, publish state (DRAFT/PUBLISHED/ARCHIVED), effective start/end dates.** |
| G·1-011 2+ level category hierarchy | **Missing** | `ServiceType.Category` is a flat `varchar(80)` string. No `service_category` table, no parent/child, no ordering. |
| G·1-012 Search with synonyms, typo tolerance, relevance | **Missing** | `catalog.ListServiceTypes` filters only; `portal.Catalog` returns the whole list. No search of any kind. |
| G·1-013 Type-ahead suggestions | **Missing** | — |
| G·1-014 Promoted/shortcut services | **Missing** | — |
| G·1-015 Contextual service detail before submission | **Partial** | Description is surfaced in the portal; no dedicated detail step with department, expected response time, or what-to-expect copy. |
| G·1-067 Related knowledge articles / FAQs | **Missing** | No knowledge surface. Brief recommends proxying the CRM knowledge base rather than owning a copy. |

## 2.3 Request Intake & Forms

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-016 Anonymous + guest, hardened against abuse | **Missing** | `mountPortal` puts `POST /portal/requests` inside `requireCitizen`. No anonymous path, no guest path, and no CAPTCHA/honeypot/proof-of-work anywhere in the tree. **The single largest gap.** |
| G·1-017 Authenticated path: registration, recovery, profile, history, prefs | **Partial → integrate C2** | OIDC+PKCE login, `/portal/me`, and request history all work (`internal/portal`, `internal/c2/oidc`). Self-registration and credential recovery are **C2's job, not ours** (`builder/authentication-profile.md`). Address management and preferences are absent. |
| G·1-018 Configurable forms per service | **Have** | `ServiceType.IntakeForm` → `domain.FormField` (key, label, type, required, options, help, pattern, min, max), rendered by `PortalField` and validated server-side via `catalog.ParseForm`. |
| G·1-019 Scoped launch set, engine scalable | **Have** | Adding a service adds no code. |
| G·1-020 Form opens without losing page context | **Partial** | `Report` is a route, not a dialog. No focus trap, no keyboard dismissal, no return-focus. |
| G·1-021 Configurable contact fields + PI collection notice | **Missing** | No personal-information collection notice anywhere in the intake flow. **This is a PIPEDA obligation, not just a Burnaby requirement.** |
| G·1-022 Client+server validation, accessible field errors | **Partial** | Server-side validation is real. Client-side errors exist but field-level `aria-describedby`/`aria-invalid` association is unverified. |
| G·1-023 Conditional fields/sections | **Missing** | `FormField` has no `conditionalOn` / show-when rule. |
| G·1-024 Max length + profanity filtering | **Partial** | `pattern`, `min`, `max` supported. No profanity filter. |
| G·1-025 Attachments incl. camera capture, mandatory malware scan | **Partial** | `internal/requests/attachments.go` has an `AttachmentStore` with type allow-list, size cap, checksum and a `ScanFunc` seam — **but the default scanner returns `"skipped"`, no scanner is wired in `cmd/server`, and the citizen portal cannot attach at all.** Honest seam, unmet requirement. |
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
| G·1-033 Gated submission, no duplicate submits, data preserved | **Partial** | `internal/httpapi/idempotency.go` + `domain.IdempotencyKey` give real server-side duplicate-submit protection. Client-side data preservation on failure unverified. |
| G·1-034 Configurable confirmation with case number, next steps, expected response | **Partial** | Reference number is returned and SLA targets are computable (`catalog.ComputeTargets`). The confirmation copy itself is not configurable. |
| G·1-035 Unique **non-sequential** reference number | **Have** *(CIT-13)* | `requests.NewReference` draws 8 symbols of Crockford base32 from `crypto/rand` — `BBY-7K4M-2QX9` — with the prefix configurable per deployment (`CC_REFERENCE_PREFIX`) and a redraw on the unique-index collision. Lookup folds O/I/L so a reference survives being read down a phone. `ccadm reissue-references` converts historical rows. |
| G·1-037 Structured, queryable submission data | **Have** | `Request.FormData` JSON + full reporting layer over it. |
| G·1-036 Template-driven confirmation (email, optional SMS) | **Have (authenticated) / Missing (guest)** | `internal/notifications` is a durable outbox with templates, retry/backoff, suppression and an admin retry/replay UI — but `NotificationOutbox.C2Sub` is `not null`: **every message goes through C2 and requires an active consented citizen.** A guest requester cannot be told anything. |
| G·1-065 Notification preferences by channel and message type | **Partial — C2 owns this** | `domain.ConsentPreference` (contact × purpose × channel) exists locally but is not wired to sending. Per `builder/notifications.md`, **C2 owns channel opt-in and the consent gate**; C2 does *not* model per-message-type preference. Genuine delta to disclose. |

## 2.6 Tracking & Support

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-048 Track by reference + second factor, rate-limited, anti-enumeration | **Missing** | No public tracking endpoint. `requests.GetByReference` exists but is only reachable behind staff auth. The global per-IP limiter (`middleware.go`, 600/min) is not a per-endpoint tracking control. |
| G·1-049 Status from the system of record only | **Have (today)** | `portal.MyRequest` projects status/updates from the single source of truth. The rating inverts once a CRM adapter makes the back office the SoR. |
| G·1-050 Configurable "reference not found" help | **Missing** | — |
| G·1-051 Configurable contact channels | **Missing** | — |

## 2.7 Administration

| Req | Rating | Evidence / gap |
|---|---|---|
| G·1-052 Non-technical staff maintain the catalogue | **Partial** | `/api/service-types` CRUD + `web/src/pages/Admin.tsx`. The intake form is edited as raw JSON — that is not a non-technical surface. |
| G·1-053 Maintain forms, messages, routing, workflow settings | **Partial** | Strong here: `RuleEditor.tsx` + `/api/routing-rules/simulate`, notification templates, SLA policies, business calendars, macros. Form building is the weak link; workflow states are fixed in code. |
| G·1-054 Role-based admin permissions (admin / business user / IT support) | **Partial** | Roles are `readonly \| agent \| supervisor \| admin` (`domain/org.go`). Burnaby's three personas do not map cleanly; no IT-support persona. |
| G·1-055 Draft → review → approval → publish governance | **Missing** | Configuration saves take effect immediately. Only an `Active` boolean. |
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
| G·1-046 Monitoring/alerting across portal and integrations | **Partial** | `/healthz`, notification outbox `Stats` with stuck-queue detection, webhook delivery log, `cmd/security-dashboard`. No alerting, and **no CI at all — `.github/workflows/` does not exist.** |
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

1. **The front door is the gap.** Everything a Burnaby evaluator clicks first — anonymous submission,
   catalogue search, map pin, tracking a reference — is missing. Everything behind it is strong.
2. ~~**Sequential reference numbers are a live defect**~~ — **fixed** (CIT-13). References are now
   drawn at random, and `ccadm reissue-references` converts what was already in the table.
3. **No CI exists.** `builder/security-service-scanning.md` and the WCAG axe-in-CI commitment both
   assume a pipeline. This also affects what evidence we can put in the proposal.
4. **C2 covers more than expected on identity and messaging, and less than expected on preferences.**
   Registration, credential recovery, consent and channel fan-out are C2's. Per-message-type
   preference (G·1-065) is not something C2 models, and no C2 path exists for a guest with no `sub`.
5. **Stack.** The brief targets Angular + Spring Boot + PostgreSQL; CityConnect is React/Vite +
   Go/chi/GORM on MySQL. Recommendation in `burnaby-demo-plan.md`.
