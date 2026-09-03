/**
 * Drives the running dashboard with a real browser.
 *
 * The unit tests stub fetch, which proves the client's logic and proves
 * nothing about whether the thing works. This signs in against a live API,
 * checks what rendered, reloads to confirm the session survives, and fails on
 * any console error or failed request along the way.
 *
 * The reload is the assertion that matters. The access token lives only in
 * memory, so a session that does not come back from the HttpOnly refresh
 * cookie means the whole scheme is decorative - and that is exactly the bug
 * this script found the first time it ran.
 *
 *   node scripts/smoke.mjs http://localhost:5173 ./screenshots
 */
import { chromium } from "playwright";

const base = process.argv[2] ?? "http://localhost:5173";
const out = process.argv[3] ?? "./screenshots";

const email = process.env.SMOKE_EMAIL ?? "ada@acme.test";
const password = process.env.SMOKE_PASSWORD ?? "correct-horse-battery";

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });

const problems = [];
page.on("pageerror", (e) => problems.push(`uncaught: ${e.message}`));
page.on("requestfailed", (r) => problems.push(`request failed: ${r.url()} ${r.failure()?.errorText}`));
page.on("response", (r) => {
  // The bootstrap refresh on a first visit is a 401 by design - there is no
  // session to recover yet - so it is not a problem. Anything else is.
  if (r.status() >= 400 && !r.url().endsWith("/auth/refresh")) {
    problems.push(`${r.status()} ${r.request().method()} ${r.url()}`);
  }
});

function check(condition, message) {
  if (!condition) problems.push(message);
  console.log(`${condition ? "ok  " : "FAIL"}  ${message}`);
}

await page.goto(base, { waitUntil: "networkidle" });
await page.screenshot({ path: `${out}/01-sign-in.png` });
check(
  (await page.getByRole("button", { name: "Sign in" }).count()) === 1,
  "a signed-out visitor is offered the sign-in form and nothing else",
);

await page.getByLabel("Email").fill(email);
await page.getByLabel("Password").fill(password);
await page.getByRole("button", { name: "Sign in" }).click();

await page.getByRole("heading", { name: "Overview" }).waitFor({ timeout: 15000 });
await page.waitForLoadState("networkidle");
await page.screenshot({ path: `${out}/02-overview.png`, fullPage: true });

const nav = await page.locator("nav a").allInnerTexts();
check(nav.includes("Expenses"), `the navigation rendered: ${nav.join(", ")}`);

const totals = await page.locator("main p.text-xl").allInnerTexts();
check(
  totals.every((t) => /[\d,]+\.\d{2}|\d+/.test(t)),
  `amounts are formatted for the currency: ${totals.join(", ") || "(none)"}`,
);

await page.reload({ waitUntil: "networkidle" });
check(
  (await page.getByRole("heading", { name: "Overview" }).count()) === 1,
  "the session survives a reload, recovered from the HttpOnly cookie alone",
);

if (problems.length > 0) {
  console.error(`\n${problems.length} problem(s):\n  ${problems.join("\n  ")}`);
  await browser.close();
  process.exit(1);
}

console.log("\nno console errors, no failed requests");
await browser.close();
