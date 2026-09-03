# OWASP ASVS

## Scope

The Application Security Verification Standard is a graded list of security requirements for web apps and APIs. It is voluntary, but it is the cheapest way to answer "how do you know the app is secure?" with something other than a scan report. Version 4.0.3 remains widely referenced and 5.0 landed in 2025; pick one version and pin it, because the requirement numbering differs.

## What it demands

Three levels: **L1** is what any application should do and is testable from outside; **L2** is the level most SaaS handling personal or business data should target; **L3** is for systems where failure is severe. Requirements are grouped by chapter — architecture, authentication, session management, access control, validation and encoding, cryptography, error handling and logging, data protection, communications, malicious code, business logic, files, API, and configuration.

The chapters that matter most in practice:

- **Access control.** Every request authorizes server-side against the authenticated identity. Object-level authorization is the single most common real breach in a multi-tenant app: never trust an id in a path or body.
- **Authentication and session management.** Credential storage with a memory-hard hash, rate limiting on login, secure session invalidation, MFA where the risk warrants.
- **Data protection.** TLS everywhere, secrets never in source or logs, minimal retention.
- **Logging.** Security-relevant events logged with enough context to reconstruct an incident, and without logging credentials or tokens.

## How it lands in this app

Adopt a level explicitly and record it. Treat the relevant chapter as a review checklist per feature rather than an annual event, and keep a short deviations list where a requirement genuinely does not apply. Pair it with dependency scanning and a secrets scan in CI, which ASVS assumes but does not itself perform.

## Evidence to keep

The chosen level and version, a completed checklist with per-requirement status, the deviations list with rationale, and the date of the last penetration test with its findings and remediation state.