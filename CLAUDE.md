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

## CI

`.github/workflows/ci.yml` runs on every push to `main` and every pull request:

- **verify** — `go build`/`vet`/`test`, then `npm ci` and lint + production build for **each** SPA
  (`web` and `web-portal` get their own steps, so a failure names the app that broke).
- **accessibility** — `npm run test:a11y`: Playwright drives Chromium against the built citizen
  portal with the API stubbed in the test, and axe checks each page against WCAG 2.2 AA. No Go
  service and no database are involved. Covers the demo flows — catalogue, intake form, tracking
  form and its error state — plus a keyboard-reachability check axe cannot make.
- **security** — installs the pinned scanners, runs
  `security-dashboard scan --trigger ci --fail-on-gating`, and uploads `health.json`,
  `dashboard.html` and the run manifests as a build artifact.

`--fail-on-gating` exits non-zero when a gating check **did not pass** — which includes one recorded
as `skipped` because its tool is missing. `Totals.GatingFail` counts only `fail`/`error`, so gating on
that alone goes green when govulncheck is simply absent from the runner. A gate that cannot tell
"clean" from "never ran" is not a gate.

Scanner versions in the workflow are **pinned, not `@latest`**: an unpinned tool turns an unrelated
commit red, and it is the supply-chain question we would rather answer than be asked. The gitleaks
step needs full history, so the security job checks out with `fetch-depth: 0` — the default shallow
clone would hand it one commit and a meaningless pass.

Step 4 above is still the path for an **ad-hoc** bundle. In the normal case take `health.json` from
the CI artifact instead, so what reaches C2 is a copy of what CI observed rather than a second run.

Axe is a **floor, not the check** — our compliance profile puts automated coverage at roughly a
third of real WCAG failures. Keyboard-only and screen-reader passes are CIT-42's, and this suite
exists to stop regressions between them.

Two traps worth knowing if you extend `a11y/`:

- Playwright tries the **most recently registered** matching route first, so `stubApi` registers its
  catch-all *before* the specific routes. The intuitive order shadows every stub with a 404, and the
  tests still pass — against empty pages. That is why the landing test asserts a catalogue item is
  visible before it scans.
- `vite preview` binds `localhost`, which resolves to `::1` first on macOS. The config passes
  `--host 127.0.0.1`; without it the readiness probe never connects and the failure looks like a slow
  build timing out.
