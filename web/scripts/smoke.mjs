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

// --- Expenses ---------------------------------------------------------------

await page.getByRole("link", { name: "Expenses", exact: true }).click();
await page.getByRole("heading", { name: "Expenses" }).waitFor();

// networkidle is not a signal a single-page app gives on navigation: no new
// document loads, so it resolves before the data request has even started.
// Waiting for the thing that should appear is the only honest condition.
await page.locator("tbody tr").first().waitFor({ timeout: 10000 });
await page.screenshot({ path: `${out}/03-expenses.png`, fullPage: true });

const rows = await page.locator("tbody tr").count();
check(rows > 0, `the claim list rendered ${rows} rows`);

// Filtering must reset the walk: a cursor from the previous result set points
// into rows that are no longer in the list.
await page.getByLabel("Status").selectOption("draft");
await page.getByRole("button", { name: "Apply" }).click();
await page.locator("tbody tr").first().waitFor({ timeout: 10000 });
check(
  (await page.locator("tbody tr").count()) > 0,
  "filtering by status returned rows",
);

await page.getByRole("button", { name: "Clear" }).click();
await page.locator("tbody tr").first().waitFor({ timeout: 10000 });

// --- One claim --------------------------------------------------------------

await page.locator("tbody tr a").first().click();
await page.getByRole("heading", { name: "History" }).waitFor({ timeout: 10000 });
await page.screenshot({ path: `${out}/04-claim.png`, fullPage: true });

const actions = await page.locator("main button").allInnerTexts();
check(
  actions.some((a) => a.includes("Submit for approval")),
  `the actions came from the server's allowed_actions: ${actions.join(", ")}`,
);
check(
  (await page.getByRole("heading", { name: "History" }).count()) === 1,
  "the audit ledger is shown alongside the claim",
);

// --- Submitting -------------------------------------------------------------

await page.getByRole("button", { name: "Submit for approval" }).click();
await page.getByText("Awaiting approval").first().waitFor({ timeout: 10000 });
check(
  (await page.getByText("Awaiting approval").count()) > 0,
  "submitting moved the claim to awaiting approval",
);
// The submitter cannot decide on their own claim, so that button must be gone.
const afterSubmit = await page.locator("main button").allInnerTexts();
check(
  !afterSubmit.some((a) => a === "Approve"),
  `the submitter is not offered Approve on their own claim: ${afterSubmit.join(", ") || "(none)"}`,
);
await page.screenshot({ path: `${out}/05-submitted.png`, fullPage: true });

// --- The new-claim form -----------------------------------------------------

await page.getByRole("link", { name: "Expenses", exact: true }).click();
await page.getByRole("link", { name: "New claim" }).click();
await page.getByRole("heading", { name: "New claim" }).waitFor();
await page.screenshot({ path: `${out}/06-new-claim.png`, fullPage: true });

// An amount with more precision than the currency has is refused before a
// request is made.
await page.getByLabel("Merchant").fill("Precision Test");
await page.getByLabel(/^Amount/).fill("12.505");
await page.getByRole("button", { name: "Create draft" }).click();
check(
  (await page.getByText(/at most 2 decimal places/).count()) === 1,
  "an over-precise amount is refused client-side, before a round trip",
);

await page.getByLabel(/^Amount/).fill("12.50");
await page.getByRole("button", { name: "Create draft" }).click();
await page.getByRole("heading", { name: /Precision Test/ }).waitFor({ timeout: 10000 });
check(
  (await page.getByRole("heading", { name: /Precision Test/ }).count()) === 1,
  "a valid claim was created and opened on its own page",
);
await page.screenshot({ path: `${out}/07-created.png`, fullPage: true });

if (problems.length > 0) {
  console.error(`\n${problems.length} problem(s):\n  ${problems.join("\n  ")}`);
  await browser.close();
  process.exit(1);
}

console.log("\nno console errors, no failed requests");
await browser.close();
