# City Connect (CIT)

A CRM application

These are the documents captured when this project was created. Read them before planning or implementing: they describe how this project is built and what it must comply with, and they take precedence over general defaults where the two differ.

## Architecture

- `architecture.md` — React/Vite/Typescript/Golang

## Compliance

This project is being built to the following standards.

- `compliance/wcag-2.2-aa.md` — **WCAG 2.2 Level AA**: The accessibility bar nearly every other accessibility law points at.
- `compliance/owasp-asvs.md` — **OWASP ASVS**: A checklist of what application security actually has to do; Level 2 is the usual target.
- `compliance/cis-benchmarks.md` — **CIS Benchmarks**: Hardening baselines for the OS, container runtime, database and web tier.
- `compliance/pipeda.md` — **PIPEDA (Canada federal)**: Meaningful consent, ten fair information principles, RROSH breach reporting to the OPC.

## App Builder

This project builds on the following DevPro App Builder services. Integrate against them rather than reimplementing what they provide.

- `builder/authentication-profile.md` — **Authentication and profile**: Sign users in through C2 (OIDC + PKCE) and read their profile, instead of building your own identity.
- `builder/notifications.md` — **Notification Services**: Send notifications to a citizen through C2, which handles the consent gate and delivery channels.
- `builder/payments.md` — **Payment Service**: C2 brokers the payment. No integration guide published yet — confirm scope with the C2 team first.
- `builder/application-status.md` — **Application Status**: Answer C2's service card callout so a citizen sees your application's status in their portal.
- `builder/security-service-scanning.md` — **Security Pipeline**
