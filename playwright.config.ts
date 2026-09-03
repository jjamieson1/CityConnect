import { defineConfig, devices } from "@playwright/test";

/**
 * Accessibility checks for the citizen portal.
 *
 * The suite runs against the built portal served by `vite preview`, with every
 * API call stubbed in the test. That keeps it a front-end check: no Go server,
 * no MySQL, nothing to seed — so it runs on a bare CI runner in seconds and
 * fails for accessibility reasons rather than infrastructure ones.
 *
 * It is a floor, not the check. Our compliance profile is explicit that
 * automation catches roughly a third of real WCAG failures, so this exists to
 * stop regressions between the manual keyboard and screen-reader passes that
 * CIT-42 owns — not to replace them.
 */
const PORT = 4180;

export default defineConfig({
  testDir: "./a11y",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : [["list"]],

  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: "retain-on-failure",
  },

  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],

  webServer: {
    // Build then preview, so the checks run against the same bundle a resident
    // would be served rather than the dev server's instrumented output.
    // --host 127.0.0.1 matters: vite preview otherwise binds "localhost", which
    // resolves to ::1 first on macOS, and the readiness probe below never
    // connects. The symptom is a webServer timeout that looks like a slow build.
    command: `npm run build --workspace web-portal && npm run preview --workspace web-portal -- --port ${PORT} --strictPort --host 127.0.0.1`,
    url: `http://127.0.0.1:${PORT}`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
