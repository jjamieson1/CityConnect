import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

/**
 * WCAG 2.2 AA checks on the citizen portal's public flows.
 *
 * 2.2, not 2.1: the build brief cites 2.1, but the project's compliance profile
 * commits to 2.2 and the RFP asks openly for "WCAG 2.x". We answer to the
 * higher bar. See docs/project/compliance/wcag-2.2-aa.md.
 *
 * These are Acts 1 and 2 of the demo — find a service, start a report, come
 * back and check on it — which is exactly the path an evaluator clicks first
 * and the one a resident with a screen reader has to complete unaided.
 */

const TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];

/** A small, realistic catalogue. Two categories, so the grouping renders. */
const CATALOG = {
  items: [
    {
      id: "svc-pothole",
      code: "POTHOLE",
      name: "Pothole repair",
      category: "Roads & transport",
      description: "Report a pothole or damaged road surface.",
      department: "Public Works",
      requiresLocation: true,
      fields: [
        { key: "size", label: "How big is it?", type: "select", required: true,
          options: ["Small", "Medium", "Large (over 1m)"] },
        { key: "hazard", label: "Is it a hazard to traffic?", type: "checkbox" },
        { key: "detail", label: "Anything else we should know?", type: "textarea" },
      ],
    },
    {
      id: "svc-graffiti",
      code: "GRAFFITI",
      name: "Graffiti removal",
      category: "Parks & public space",
      description: "Report graffiti on City property.",
      department: "Parks",
      requiresLocation: true,
      fields: [],
    },
  ],
};

/**
 * Stub the portal's API.
 *
 * Signed out by default — the public paths are what this suite is about, and a
 * resident who never made an account is the one most likely to be using
 * assistive technology on a page nobody tested.
 *
 * The intake form is the exception: it still requires a session today, and
 * signed out it bounces to C2 for silent SSO rather than rendering. Once CIT-11
 * and CIT-12 land the anonymous and guest paths, that test should drop back to
 * the signed-out default — which is the state it most needs checking in.
 */
async function stubApi(page: Page, { signedIn = false } = {}) {
  // Order matters, and not the way it reads. Playwright tries the most recently
  // registered matching route first, so the catch-all goes down FIRST and the
  // specific routes after it — the intuitive order silently shadows every stub
  // with a 404 and leaves the suite scanning empty pages that still pass.
  await page.route("**/api/portal/**", (route) =>
    route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({ title: "Not found", detail: "Not found." }),
    }),
  );

  await page.route("**/api/portal/catalog", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(CATALOG),
    }),
  );

  await page.route("**/api/portal/me", (route) =>
    signedIn
      ? route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ name: "Alex Citizen", email: "alex@example.gov", openRequests: 0 }),
        })
      : route.fulfill({
          status: 401,
          contentType: "application/json",
          body: JSON.stringify({ title: "Unauthenticated", detail: "Sign in to continue." }),
        }),
  );
}

async function scan(page: Page) {
  return new AxeBuilder({ page }).withTags(TAGS).analyze();
}

/** Reports violations by rule and the nodes involved, so a failure is actionable. */
function describe(violations: Awaited<ReturnType<typeof scan>>["violations"]) {
  return violations
    .map((v) => {
      const where = v.nodes.map((n) => `      ${n.target.join(" ")}`).join("\n");
      return `  [${v.impact ?? "unknown"}] ${v.id} — ${v.help}\n    ${v.helpUrl}\n${where}`;
    })
    .join("\n\n");
}

test("landing page — finding a service", async ({ page }) => {
  await stubApi(page);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: /what would you like to report/i })).toBeVisible();
  // The catalogue must actually be on the page: an empty one passes any scan.
  await expect(page.getByRole("link", { name: /pothole repair/i })).toBeVisible();
  await expect(page.getByRole("heading", { name: /roads & transport/i })).toBeVisible();

  const results = await scan(page);
  expect(describe(results.violations)).toBe("");
});

test("intake form — reporting a pothole", async ({ page }) => {
  await stubApi(page, { signedIn: true });
  await page.goto("/new/POTHOLE");
  // The form is built from the stubbed field definitions, so wait for a field
  // rather than the heading: an empty form would pass a scan meaninglessly.
  await expect(page.getByLabel(/how big is it/i)).toBeVisible();

  const results = await scan(page);
  expect(describe(results.violations)).toBe("");
});

test("tracking form — coming back without an account", async ({ page }) => {
  await stubApi(page);
  await page.goto("/track");
  await expect(page.getByRole("heading", { name: /check on a report/i })).toBeVisible();

  const results = await scan(page);
  expect(describe(results.violations)).toBe("");
});

/**
 * The error state, which is where accessibility is usually forgotten.
 *
 * A failed lookup must announce itself — a message that only appears visually
 * leaves a screen-reader user believing the button did nothing. The portal
 * marks it role="alert" and this pins that down.
 */
test("tracking failure is announced, not just displayed", async ({ page }) => {
  await stubApi(page);
  await page.route("**/api/portal/requests/track", (route) =>
    route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({
        title: "Not found",
        detail: "We could not find a request matching that reference and contact detail.",
        code: "not_found",
      }),
    }),
  );

  await page.goto("/track");
  await page.getByLabel(/reference number/i).fill("SR-7K4M-2QX9");
  await page.getByLabel(/email or phone/i).fill("someone@example.gov");
  await page.getByRole("button", { name: /check my report/i }).click();

  await expect(page.getByRole("alert")).toContainText(/could not find/i);

  const results = await scan(page);
  expect(describe(results.violations)).toBe("");
});

/**
 * Keyboard reachability, which axe cannot judge.
 *
 * "Check a report" is offered to signed-out residents on purpose. If it is not
 * reachable by tabbing, the people most likely to need it cannot get to it.
 */
test("the tracking link is reachable by keyboard from the landing page", async ({ page }) => {
  await stubApi(page);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: /what would you like to report/i })).toBeVisible();

  const trackLink = page.getByRole("link", { name: /check a report/i });
  await expect(trackLink).toBeVisible();

  for (let i = 0; i < 15; i++) {
    await page.keyboard.press("Tab");
    if (await trackLink.evaluate((el) => el === document.activeElement)) {
      await page.keyboard.press("Enter");
      await expect(page.getByRole("heading", { name: /check on a report/i })).toBeVisible();
      return;
    }
  }
  throw new Error("the tracking link was not reachable within 15 tab stops");
});
