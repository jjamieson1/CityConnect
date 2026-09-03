**CUSTOM APPLICATION HUB · PRODUCT & ENGINEERING BRIEF**

**Service Request Portal**

*Feature list, domain model, and build blueprint for the hub's citizen service-request feature.*

**Source RFP:**  City of Burnaby \#177-08-26 — Customer Service Centre Web Portal (issued August 12, 2026\)

**Requirements drawn from:**  Appendix G — Functional Requirements

**Target stack:**  Angular · Spring Boot BFF · PostgreSQL · REST /api/v1

**Prepared:**  September 2, 2026

# **Contents**

*Right-click above and choose “Update Field” (or press Ctrl+A, then F9) to populate this table of contents.*

# **1\. Overview**

A self-service portal where residents, businesses, and visitors discover a City service, fill out a configurable form for it, get a reference number back, and can track status later — anonymously or signed in. City staff maintain the service catalogue and forms themselves; the case itself lives and moves through a back-office CRM, not the portal.

The one design decision everything else follows from: **the portal core knows nothing about Dynamics 365, or any other CRM.** It talks to exactly one interface — CrmCaseAdapter — and that is the only thing that changes when the hub sells this feature into a municipality running a different back office. Build the core against a stub adapter first; wire in the real one in phase 2\.

# **2\. Feature List**

Every feature below traces back to a numbered requirement in Appendix G of the source RFP, so scope decisions can be checked against the original document. 58 features are back-office-agnostic and belong in the hub's reusable core; 9 are specific to whichever CRM a municipality runs and are grouped separately as an adapter contract.

## **2.1  Portal & Navigation**

*The shell every other capability sits inside.*

| Req \# | Priority | Feature |
| :---- | :---- | :---- |
| G·1-001 | **High** | Single secure, citizen-facing entry point covering discovery, submission, confirmation, and status tracking — anonymous and authenticated alike. |
| G·1-002 | **High** | Consistent branded visual system and interaction patterns across every page and workflow. |
| G·1-003 | **High** | Responsive across desktop, tablet, and mobile for every core flow. |
| G·1-004 | **High** | Configurable navigation — menus, utility links, labels, destinations — editable by non-technical staff. |
| G·1-005 | **Medium** | Persistent help/contact access from every page: header link plus contextual contact section in the request flow. |
| G·1-006 | **High** | Configurable page titles, instructional copy, banners, and alert/notice messages. |
| G·1-007 | **High** | Location-orientation aids (breadcrumbs or equivalent). |
| G·1-008 | **Medium** | Configurable primary sections (create, track, contact) with configurable sub-sections. |
| G·1-009 | **Medium** | Configurable default landing view or workflow step. |

## **2.2  Service Catalogue & Discovery**

*How a resident finds the right service before opening a form.*

| Req \# | Priority | Feature |
| :---- | :---- | :---- |
| G·1-010 | **High** | Central configurable service catalogue — names, synonyms, description, category, owning department, routing destination, mapped form, publish state, effective dates. |
| G·1-011 | **High** | Browse by a configurable category hierarchy (2+ levels), grouped and ordered. |
| G·1-012 | **High** | Search across catalogue and help content — synonyms, typo tolerance, relevance tuning, no-results fallback. |
| G·1-013 | **Medium** | Type-ahead service suggestions as the user types. |
| G·1-014 | **Medium** | Configurable promoted/shortcut services. |
| G·1-015 | **Medium** | Contextual service detail shown before submission begins. |
| G·1-067 | **Medium** | Related knowledge articles/FAQs by service, sourced from one canonical knowledge base. |

## **2.3  Request Intake & Forms**

*The configurable form engine — where staff will spend the most time.*

| Req \# | Priority | Feature |
| :---- | :---- | :---- |
| G·1-016 | **High** | Anonymous submission (no confirmation/status) and a separate guest path; both hardened against automated abuse. |
| G·1-017 | **Medium** | Authenticated path: self-registration, credential recovery, profile/address management, history, preferences. |
| G·1-018 | **High** | Configurable forms — fields, sections, labels, help text, validation, per-service requirements. |
| G·1-019 | **High** | Launch scoped to an initial set of forms, engine scalable to more without re-architecture. |
| G·1-020 | **Medium** | Forms open in-portal (modal/panel/embedded) without losing page context; focus management and keyboard dismissal. |
| G·1-021 | **High** | Configurable requester/contact fields per service, plus a personal-information collection notice. |
| G·1-022 | **High** | Required/optional/validated fields enforced client- and server-side, with field-level accessible error messaging. |
| G·1-023 | **High** | Conditional fields, lists, or sections driven by service type or prior answers. |
| G·1-024 | **High** | Configurable free-text fields with max length and profanity filtering. |
| G·1-025 | **High** | File attachments (documents, images, video, mobile camera capture) with mandatory malware scanning before storage. |
| G·1-026 | **Medium** | Cancel or exit a form without an unintended submission. |

## **2.4  Location Services**

*Address search, boundary checks, and map-pin capture.*

| Req \# | Priority | Feature |
| :---- | :---- | :---- |
| G·1-027 | **High** | Manual location entry with a jurisdiction-boundary check and configurable out-of-boundary referral messaging. |
| G·1-028 | **High** | Address search / type-ahead sourced from authoritative GIS data. |
| G·1-029 | **Medium** | Validate submitted location against approved address/location data. |
| G·1-030 | **Medium** | Device geolocation, with graceful handling when permission is denied. |
| G·1-031 | **High** | Map-based pin selection with a non-map fallback always available. |
| G·1-032 | **High** | Confirmed location populates the request accurately. |

## **2.5  Submission & Notifications**

*What happens the moment a request goes in, and how the requester hears back.*

| Req \# | Priority | Feature |
| :---- | :---- | :---- |
| G·1-033 | **High** | Submission gated on every configured validation; blocks duplicate submits; preserves data on failure. |
| G·1-034 | **High** | Configurable confirmation message with case number, summary, next steps, expected response time. |
| G·1-035 | **High** | One unique, non-sequential reference number per request wherever tracking is public. |
| G·1-037 | **High** | Submission data captured in a structured, queryable form for processing, traceability, and reporting. |
| G·1-036 | **High** | Automated, template-driven confirmation notification (email, optional SMS) on submission. |
| G·1-065 | **High** | Notification preferences by channel and message type, stored against the contact record. |

## **2.6  Tracking & Support**

*The “where's my request” loop.*

| Req \# | Priority | Feature |
| :---- | :---- | :---- |
| G·1-048 | **High** | Track by reference number plus a second verification factor; rate-limited and hardened against enumeration. |
| G·1-049 | **High** | Status/progress display sourced only from the back-office system of record. |
| G·1-050 | **Medium** | Configurable help/contact guidance for a reference number that isn't found. |
| G·1-051 | **High** | Configurable contact channels surfaced consistently across the portal. |

## **2.7  Administration**

*What City staff can change themselves.*

| Req \# | Priority | Feature |
| :---- | :---- | :---- |
| G·1-052 | **High** | Non-technical staff maintain the service catalogue. |
| G·1-053 | **High** | Non-technical staff maintain forms, messages, routing rules, and workflow settings. |
| G·1-054 | **High** | Role-based admin permissions across administrator, business-user, and IT-support personas. |
| G·1-055 | **Medium** | Draft → review → approval → publish governance for configuration changes. |
| G·1-056 | **Medium** | Versioning, approval history, and audit trail for configuration changes. |
| G·1-057 | **Medium** | Controlled promotion of configuration across environments. |

## **2.8  Reporting, Audit & Environments**

*Operational visibility and non-production guardrails.*

| Req \# | Priority | Feature |
| :---- | :---- | :---- |
| G·1-058 | **High** | Reporting/dashboards for service-centre operations and performance monitoring. |
| G·1-059 | **Medium** | Operational metrics — volume, completion rates, time-to-first-response, time-to-closure. |
| G·1-060 | **Medium** | Data export for service-request, reporting, audit, and configuration data. |
| G·1-064 | **Medium** | Web analytics on visits, searches, form starts/completions/abandonment (privacy/consent-aware). |
| G·1-061 | **High** | Auditability of submissions, status changes, and admin activity — every change stored. |
| G·1-062 | **Medium** | Non-production environments support refresh, data masking, access controls, monitoring. |

## **2.9  Operations**

*Keeping the lights on, and telling people honestly when they're not.*

| Req \# | Priority | Feature |
| :---- | :---- | :---- |
| G·1-046 | **High** | Monitoring/alerting across the portal and integration layers. |
| G·1-066 | **High** | Customer-facing outage/maintenance messaging with alternate-channel guidance. |

## **2.10  Future Expansion**

*The scale test every prospective client will eventually ask about.*

| Req \# | Priority | Feature |
| :---- | :---- | :---- |
| G·1-063 | **High** | New services, forms, workflows, and integrations addable through configuration, not source changes. |

## **2.11  CRM / Case-Management Adapter (pluggable per engagement)**

*The only nine requirements written against a specific back office (Dynamics 365 in Burnaby's case). Build as an adapter contract, not core logic.*

| Req \# | Priority | Feature |
| :---- | :---- | :---- |
| G·1-038 | **High** | Case creation with no duplicate data entry or manual rekeying. |
| G·1-039 | **High** | Documented field-level mapping into case/contact/account fields. |
| G·1-040 | **High** | Configurable contact/account matching-or-creation logic per submission. |
| G·1-041 | **High** | Per-workflow mapping to the back office's own form/case-type/category. |
| G·1-042 | **High** | Back office is system of record for status; portal reflects changes within a defined latency budget. |
| G·1-043 | **High** | Routing/queue logic lives in the back office; portal supplies only metadata. |
| G·1-044 | **High** | Integration built from approved, supported components — not one-off scripts. |
| G·1-045 | **High** | Failed/delayed integration transactions logged, retried, and surfaced for remediation. |
| G·1-047 | **Low** | Optional decoupled API layer in front of the adapter for future channels. |

# **3\. Architecture**

Standard hub BFF pattern: Angular served as static assets, Spring Boot fronting everything under /api/v1, PostgreSQL owned entirely by the portal. The CRM is external and reached only through the adapter.

![][image1]

*The core only ever talks to one seam. Swap Dynamics 365 for a different case-management system on a future engagement, and only CrmCaseAdapter and its target change — the SPA, the BFF's catalog/form/request logic, and the Postgres schema stay identical.*

# **4\. Domain Model**

PostgreSQL, per the hub's database standard: snake\_case tables, varchar(36) UUID primary keys, every table carries created\_by / created\_timestamp / updated\_by / updated\_timestamp / version (omitted below for space — assume it on every table). Booleans are stored and serialized as the string TRUE/FALSE; codes are SCREAMING\_SNAKE\_CASE.

## **service\_category  —  catalog hierarchy**

| Column | Type | Notes |
| :---- | :---- | :---- |
| service\_category\_id | varchar(36) | PK |
| name | varchar(200) |  |
| parent\_category\_id | varchar(36) | FK → self, nullable (2+ level hierarchy) |
| display\_order | int |  |

## **service  —  the catalogue**

| Column | Type | Notes |
| :---- | :---- | :---- |
| service\_id | varchar(36) | PK |
| public\_name | varchar(200) | indexed for search |
| internal\_name | varchar(200) |  |
| description | text |  |
| synonyms | text | search keywords/aliases |
| service\_category\_id | varchar(36) | FK, indexed |
| owning\_department | varchar(200) |  |
| routing\_destination\_code | varchar(100) | handed to the adapter |
| mapped\_form\_id | varchar(36) | FK → service\_form |
| publish\_state | varchar(20) | DRAFT · PUBLISHED · ARCHIVED |
| effective\_start\_date / end\_date | date |  |

## **service\_form  —  a form version**

| Column | Type | Notes |
| :---- | :---- | :---- |
| service\_form\_id | varchar(36) | PK |
| service\_id | varchar(36) | FK, indexed |
| version\_number | int | one row per published version |
| config\_status | varchar(20) | DRAFT · IN\_REVIEW · PUBLISHED · ARCHIVED |
| crm\_case\_type\_code | varchar(100) | adapter mapping target |
| published\_timestamp | timestamptz |  |

## **form\_field  —  field definitions**

| Column | Type | Notes |
| :---- | :---- | :---- |
| form\_field\_id | varchar(36) | PK |
| service\_form\_id | varchar(36) | FK, indexed |
| field\_key | varchar(100) | machine name |
| label | varchar(300) |  |
| field\_type | varchar(30) | TEXT · TEXTAREA · SELECT · DATE · FILE · LOCATION |
| is\_required | varchar(5) | TRUE / FALSE |
| validation\_rule | text | JSON |
| conditional\_on\_field\_id | varchar(36) | FK → self, nullable |
| conditional\_rule | text | JSON show-when condition |
| display\_order | int |  |

## **contact  —  requester identity**

| Column | Type | Notes |
| :---- | :---- | :---- |
| contact\_id | varchar(36) | PK |
| email | varchar(320) | indexed |
| phone\_number | varchar(20) | E.164 |
| first\_name / last\_name | varchar(150) |  |
| crm\_contact\_external\_reference | varchar(100) | nullable, set by the adapter |

## **service\_request  —  the submission**

| Column | Type | Notes |
| :---- | :---- | :---- |
| service\_request\_id | varchar(36) | PK |
| reference\_number | varchar(50) | unique index — non-sequential |
| service\_id / service\_form\_id | varchar(36) | FK, indexed |
| contact\_id | varchar(36) | FK, nullable (anonymous) |
| submission\_channel | varchar(20) | ANONYMOUS · GUEST · AUTHENTICATED |
| form\_data | text | JSON — submitted field values |
| crm\_case\_external\_reference | varchar(100) | nullable until synced |
| portal\_status\_code | varchar(30) | public-facing status, indexed |
| sync\_status | varchar(20) | PENDING · SYNCED · FAILED · RETRYING |
| submitted\_timestamp | timestamptz |  |

## **request\_location  —  where**

| Column | Type | Notes |
| :---- | :---- | :---- |
| request\_location\_id | varchar(36) | PK |
| service\_request\_id | varchar(36) | FK, indexed |
| civic\_address | varchar(300) |  |
| latitude / longitude | numeric |  |
| capture\_method | varchar(20) | MANUAL · SEARCH · GEOLOCATION · MAP\_PIN |
| within\_jurisdiction | varchar(5) | TRUE / FALSE |

## **request\_attachment  —  uploads**

| Column | Type | Notes |
| :---- | :---- | :---- |
| request\_attachment\_id | varchar(36) | PK |
| service\_request\_id | varchar(36) | FK, indexed |
| file\_name / content\_type | varchar |  |
| size\_bytes | bigint |  |
| malware\_scan\_status | varchar(20) | PENDING · CLEAN · INFECTED · FAILED |
| storage\_reference | varchar(500) | object-store key, not the file |

## **notification\_preference  —  opt-in/out**

| Column | Type | Notes |
| :---- | :---- | :---- |
| notification\_preference\_id | varchar(36) | PK |
| contact\_id | varchar(36) | FK, indexed |
| message\_type\_code | varchar(30) | STATUS\_UPDATE · REMINDER · FOLLOW\_UP · SURVEY |
| channel\_code | varchar(20) | EMAIL · SMS |
| opted\_in | varchar(5) | TRUE / FALSE |

## **notification\_log  —  what went out**

| Column | Type | Notes |
| :---- | :---- | :---- |
| notification\_log\_id | varchar(36) | PK |
| service\_request\_id | varchar(36) | FK, indexed |
| channel\_code / message\_type\_code | varchar |  |
| sent\_timestamp | timestamptz |  |
| delivery\_status | varchar(20) |  |

## **config\_change\_log  —  governance trail**

| Column | Type | Notes |
| :---- | :---- | :---- |
| config\_change\_log\_id | varchar(36) | PK |
| entity\_type | varchar(50) | SERVICE · SERVICE\_FORM · FORM\_FIELD |
| entity\_id | varchar(36) | indexed with entity\_type |
| change\_type | varchar(20) | CREATE · UPDATE · PUBLISH · ARCHIVE |
| previous\_value | text | JSON snapshot, for rollback/diff |
| approval\_status | varchar(20) | DRAFT · PENDING\_APPROVAL · APPROVED · REJECTED |
| approved\_by | varchar(150) | nullable |

## **crm\_sync\_log  —  integration audit**

| Column | Type | Notes |
| :---- | :---- | :---- |
| crm\_sync\_log\_id | varchar(36) | PK |
| service\_request\_id | varchar(36) | FK, indexed |
| operation\_code | varchar(30) | CREATE\_CASE · STATUS\_POLL · CONTACT\_MATCH |
| attempt\_number | int |  |
| result\_status | varchar(20) | SUCCESS · FAILED · RETRYING |
| error\_message | text | nullable — never log PII |

*Knowledge articles (req. G·1-067) are deliberately not a table here — proxy and cache them from the CRM's knowledge base at read time rather than owning a copy that can drift out of sync.*

# **5\. API Surface**

Kebab-case paths, plural collections, lowerCamelCase DTO fields. Tracking is deliberately a POST action rather than a GET — the verification value shouldn't sit in a URL or a log line.

| Verb | Path (/api/v1…) | Purpose |
| :---- | :---- | :---- |
| **GET** | /service-categories | Category tree for browse navigation. |
| **GET** | /services?q=\&categoryId= | Search / browse the catalogue. Always 200 with an array, even empty. |
| **GET** | /services/{serviceId} | Service detail — description, mapped form, department. |
| **POST** | /services | Admin: create a catalogue entry. 201\. |
| **PUT** | /services/{serviceId} | Admin: replace a catalogue entry. |
| **POST** | /services/{serviceId}/publish | Action — DRAFT → PUBLISHED through governance. |
| **POST** | /service-forms | Admin: create a new form version for a service. |
| **GET** | /service-forms/{serviceFormId} | Field definitions for the dynamic form renderer. |
| **POST** | /service-forms/{serviceFormId}/publish | Action — governance publish step. |
| **GET** | /locations?q= | Address type-ahead against the City's GIS data. |
| **POST** | /locations/validate | Action — boundary check \+ validation against approved address data. |
| **POST** | /service-requests | Submit. 201 with reference number \+ confirmation payload. |
| **POST** | /service-requests/{id}/attachments | Upload — held at PENDING until the malware scan clears. |
| **POST** | /service-requests/track | Action — body: referenceNumber \+ verificationValue. Rate-limited. |
| **GET** | /service-requests/{id} | Staff/authenticated detail view (not the public tracking path). |
| **GET** | /contacts/{contactId}/notification-preferences | Read preferences for the profile screen. |
| **PUT** | /contacts/{contactId}/notification-preferences | Replace preferences. |
| **GET** | /knowledge-articles?serviceId= | Proxied read-through to the CRM knowledge base. |
| **GET** | /admin/config-changes?entityType=\&entityId= | Version/audit history for a catalogue or form entity. |
| **POST** | /admin/config-changes/{id}/approve | Action — governance approval step. |
| **GET** | /reports/service-request-metrics?from=\&to= | Volume, completion rate, response and closure time. |

*Standard reminders that already apply here: never expose updatedTimestamp-style audit columns in a response DTO; validation errors return 400 with application/x.fcc.validation+json mapping field → message; an empty query result is 200 \[\], never 404\.*

# **6\. Frontend — Angular**

Standalone components, signals for local state, resource() for data fetching, Tailwind for styling, luxon for every date. The form engine is the one genuinely hard piece: it builds a reactive form dynamically from the FormField\[\] the API returns, rather than from a hand-coded template per service.

app/  
  components/                            \# flat, standalone, kebab-case  
    service-catalog-browse/  
    service-search-bar/  
    service-detail-panel/  
    service-request-form/                \# dynamic form host  
    dynamic-field/                       \# renders one field by field\_type  
    location-picker/                     \# manual / search / geolocation / map tabs  
    attachment-uploader/  
    submission-confirmation/  
    request-tracker/  
    notification-preferences/  
    admin-catalog-editor/  
    admin-form-builder/  
    admin-config-history/  
    admin-service-request-dashboard/  
  services/  
    catalog.service.ts  service-request.service.ts  location.service.ts  
    attachment.service.ts  notification-preference.service.ts  
    config-admin.service.ts  reporting.service.ts  
  model/  
    service.ts  service-category.ts  service-form.ts  form-field.ts  
    service-request.ts  request-location.ts  notification-preference.ts  
  guard/  
    role.guard.ts                        \# gates /admin/\* on authorizedRoles

Requirement G·1-020 (a form opens without losing page context) maps directly onto the hub's existing custom DialogService — service-request-form opens as a dialog from service-detail-panel rather than a route change, so DialogRef already gives focus management and keyboard dismissal for free.

# **7\. Backend — Spring Boot**

Domain-driven packages, controller → service (assembler, validator) → repository (entity), one Liquibase changeset per table. Build from the database up: schema first, then entities/repositories, then services, then controllers.

ca.devpro.csrportal/  
  configuration/  
  controller/  
    ServiceController  ServiceFormController  ServiceRequestController  
    LocationController  NotificationPreferenceController  
    AdminConfigController  ReportingController  
  core/  
    catalog/       \# Service, ServiceCategory \+ assembler/validator/repository/service  
    form/          \# ServiceForm, FormField, DynamicFormValidator  
    request/       \# ServiceRequest, RequestLocation, RequestAttachment  
    contact/       \# Contact, NotificationPreference  
    notification/  \# NotificationLog, NotificationDispatchService  
    integration/  
      crm/         \# CrmCaseAdapter port \+ Dynamics365CrmCaseAdapter \+ CrmSyncLog  
    admin/         \# ConfigChangeLog, ConfigGovernanceService  
    exception/  
    utils/  
  resources/db.changelog/

The adapter port — this is the interface everything in Section 3 hinges on. Phase 1 ships a NoOpCrmCaseAdapter that just persists locally and logs; phase 2 replaces it with Dynamics365CrmCaseAdapter without touching a controller or a table.

public interface CrmCaseAdapter {  
   
    CrmCaseReference createCase(ServiceRequestSnapshot request);  
   
    void matchOrCreateContact(ContactSnapshot contact);  
   
    CrmCaseStatus getCaseStatus(CrmCaseReference reference);  
   
    void submitRoutingMetadata(RoutingMetadata metadata);  
}

# **8\. Build Phases**

Sequenced by the “Level of Need” ratings already in the requirements — High items first, CRM integration deliberately deferred behind a stub.

## **Phase 1 — Core Portal (MVP)**

Everything a resident touches, running against a stub adapter that only writes locally.

* Catalogue browse \+ search

* Dynamic form engine \+ validation

* Manual \+ search location capture

* File upload \+ malware scan

* Reference number \+ confirmation

* Tracking \+ rate limiting

* Basic admin catalogue/form CRUD

## **Phase 2 — CRM Adapter**

Swap in the real back office. Nothing built in phase 1 should need to change.

* Dynamics365CrmCaseAdapter

* Contact/account matching

* Routing metadata handoff

* Status sync (poll or webhook)

* Retry / error handling

* Integration monitoring

## **Phase 3 — Governance & Resilience**

What makes City staff comfortable running this without us on speed-dial.

* Draft/review/approve/publish workflow

* Config versioning \+ audit UI

* Environment promotion tooling

* Outage/maintenance banner

* WCAG AA remediation \+ axe gate

## **Phase 4 — Reporting & Expansion**

The features that make the next sale easier, not the ones that make this one work.

* Operational dashboards

* Data export

* Consent-aware web analytics

* Knowledge article surfacing

* Notification-preference self-service

* Decoupled public API layer

# **9\. Open Decisions**

Answer these per engagement, before or during phase 1 — they change real architecture, not just configuration.

* **Geocoding source:** a live geocoding service, or a periodically refreshed address extract — the second needs a refresh job and a staleness story.

* **Mapping platform** for pin-drop selection, and who bears its licensing or per-transaction cost on a given engagement.

* **Malware-scanning service** for attachments — self-hosted (e.g. ClamAV) vs. a managed scanning API.

* **SMS provider,** if a client enables SMS notifications — not every municipality will.

* **Latency targets:** the seconds budget for synchronous case-number return, and the minutes budget for status-sync latency — both left as “\[X\]” in Burnaby's own RFP.

* **Catalogue mastering:** portal or CRM — decides which direction the catalogue sync runs, if any.

* **Hosting region** for Canadian data-residency compliance — a per-engagement requirement, not just Burnaby's.

# **10\. Non-Functional Requirements Checklist**

Pulled forward from the requirements reference, mapped to what already exists in the hub's standards.

* **Accessibility —** WCAG 2.1 AA, with axe checks in CI per the Angular standard, not a manual pass at the end.

* **Audit trail —** every table already carries audit columns by standard; config\_change\_log covers governance-level history on top.

* **No PII in logs —** Slf4j with identifiers only — crm\_sync\_log.error\_message needs review to keep it that way.

* **Migrations —** Liquibase, one small idempotent changeset per table, tested against a clean database.

* **Abuse hardening —** rate limits on submission and on /service-requests/track; non-sequential reference numbers throughout.

* **Data residency —** confirm hosting region per engagement before go-live, not after.

*Derived from the Citizen Service Request Portal hub reference doc, itself distilled from City of Burnaby RFP \#177-08-26 (Appendix G — Functional Requirements). Built to the hub's existing Angular, Java/Spring Boot, database, and REST API standards — nothing here introduces a new architectural pattern.*

[image1]: <data:image/png;base64,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>