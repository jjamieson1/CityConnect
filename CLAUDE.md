# CityConnect

## Health reporting (security dashboard)

This app reports to C2's admin **Health** dashboard via `cmd/security-dashboard` +
`security/config.json` (copied from C2; the manifest format is the contract).

To **fix and rescan**:
1. `go run ./cmd/security-dashboard scan`, then read `security/runs/<newest>.json` and the raw
   reports in `security/runs/<id>/`.
2. Fix every non-`pass` gating check. Real fixes for real issues; for a **verified** false positive
   use a documented suppression (`#nosec <rule> -- <reason>` for gosec; `# gitleaks:allow` on a line
   confirmed to be a non-secret such as a dev stub) — never suppress something unverified. Bump
   `go.mod` to the patched Go toolchain for govulncheck stdlib CVEs.
3. After each change: `go build ./...` && `go test ./...`.
4. Re-run `scan` until gating checks pass, then `go run ./cmd/security-dashboard bundle > health.json`.
5. Upload `health.json` in the C2 admin → **Health → Add application report** (SYSTEM_ADMIN).

Scope: report CityConnect's own surface (build/test/lint, deps, SAST, secrets, access control). It is a
C2 **SSO client**, so it does **not** report identity-provider conformance (OIDC/SAML/federation).
