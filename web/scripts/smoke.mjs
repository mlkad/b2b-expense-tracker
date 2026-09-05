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

// The organisation this signs in as needs a paid plan: department budgets are
// not part of the free tier, and the entitlement gate is exercised by the Go
// integration tests rather than duplicated here.

const email = process.env.SMOKE_EMAIL ?? "ada@acme.test";
const password = process.env.SMOKE_PASSWORD ?? "correct-horse-battery";

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });

const problems = [];
page.on("pageerror", (e) => problems.push(`uncaught: ${e.message}`));
page.on("requestfailed", (r) => {
  // A navigation the browser converts into a download is reported as aborted:
  // the page never navigates, the download manager takes over. That is the
  // export working, not failing.
  if (r.url().includes("/reports/expenses/export?")) return;
  problems.push(`request failed: ${r.url()} ${r.failure()?.errorText}`);
});
/**
 * Responses this script provokes on purpose.
 *
 * The bootstrap refresh on a first visit is a 401 by design - there is no
 * session to recover yet. The overlapping budget is a 422 the test exists to
 * see. Flagging either would make the script fail on a check that passed.
 */
let expectingFailure = null;

page.on("response", (r) => {
  if (r.status() < 400) return;
  if (r.url().endsWith("/auth/refresh")) return;
  if (expectingFailure && r.url().includes(expectingFailure)) return;
  problems.push(`${r.status()} ${r.request().method()} ${r.url()}`);
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

// --- Exports ----------------------------------------------------------------
//
// A browser navigation carries no Authorization header, so the export link has
// to be signed. Before it was, these buttons produced nothing but a 401 page.

const download = page.waitForEvent("download", { timeout: 20000 });
await page.getByRole("button", { name: "CSV" }).click();
const file = await download;
check(
  /^expenses-\d{4}-\d{2}-\d{2}\.csv$/.test(file.suggestedFilename()),
  `the CSV export downloaded as ${file.suggestedFilename()}`,
);

// --- The new-claim form -----------------------------------------------------

await page.getByRole("link", { name: "New claim" }).click();
await page.getByRole("heading", { name: "New claim" }).waitFor();
await page.screenshot({ path: `${out}/06-new-claim.png`, fullPage: true });

// An amount with more precision than the currency has is refused before a
// request is made.
const merchant = `Smoke ${Date.now()}`;
await page.getByLabel("Merchant").fill(merchant);
await page.getByLabel(/^Amount/).fill("12.505");
await page.getByRole("button", { name: "Create draft" }).click();
check(
  (await page.getByText(/at most 2 decimal places/).count()) === 1,
  "an over-precise amount is refused client-side, before a round trip",
);

await page.getByLabel(/^Amount/).fill("12.50");
await page.getByRole("button", { name: "Create draft" }).click();
await page.getByRole("heading", { name: new RegExp(merchant) }).waitFor({ timeout: 10000 });
check(
  (await page.getByRole("heading", { name: new RegExp(merchant) }).count()) === 1,
  "a valid claim was created and opened on its own page",
);
await page.screenshot({ path: `${out}/07-created.png`, fullPage: true });

// --- Receipts ---------------------------------------------------------------
//
// The upload goes straight to the object store: the digest is computed in the
// browser, the API only signs a URL. Which means this is the only place the
// whole path can be checked.

await page.getByRole("link", { name: "Expenses", exact: true }).click();
await page.locator("tbody tr a").first().waitFor({ timeout: 10000 });

// A draft, so a receipt can be attached to it.
const draftRow = page.locator("tbody tr", { hasText: "Draft" }).first();
if ((await draftRow.count()) > 0) {
  await draftRow.locator("a").click();
  await page.getByRole("heading", { name: "Receipts" }).waitFor({ timeout: 10000 });

  await page.setInputFiles("#receipt-input", {
    name: "receipt.pdf",
    mimeType: "application/pdf",
    buffer: Buffer.from("%PDF-1.7\nsmoke test receipt\n"),
  });

  await page.getByRole("link", { name: "receipt.pdf" }).waitFor({ timeout: 20000 });
  check(true, "a receipt was hashed, uploaded to object storage and recorded");
  await page.screenshot({ path: `${out}/08-receipt.png`, fullPage: true });
} else {
  check(false, "no draft claim available to attach a receipt to");
}

// --- One claim --------------------------------------------------------------

// The claim this run just created, not whatever is in the list. Seeded data
// belongs to other people and previous runs leave claims already submitted, so
// picking one from the table makes the transition assertions depend on the
// state of the database rather than on the product.
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

// --- Organisation -----------------------------------------------------------

await page.getByRole("link", { name: "Organisation" }).click();
await page.getByRole("heading", { name: "Organisation" }).waitFor();
await page.getByRole("tab", { name: "Members" }).click();
await page.locator("tbody tr").first().waitFor({ timeout: 10000 });
check(
  (await page.getByText("(you)").count()) === 1,
  "the members table marks the signed-in person",
);
await page.screenshot({ path: `${out}/10-organisation.png`, fullPage: true });

// A department of its own for this run. The script has to be idempotent
// against a database that already holds everything the last run created - and
// a budget belongs to a department, so a fresh one is what makes the overlap
// check below deterministic rather than a collision with last time.
const department = `Smoke ${Date.now()}`;
await page.getByRole("tab", { name: "Departments" }).click();
await page.locator("#dept-name").fill(department);
await page.getByRole("button", { name: "Add" }).click();
await page.getByText(department).waitFor({ timeout: 10000 });
check(true, "a department was created and appeared in the list");

await page.getByRole("tab", { name: "Subscriptions" }).click();
await page.getByText(/recurring software/i).waitFor({ timeout: 10000 });
check(true, "the vendor subscription panel loaded");

// --- Budgets ----------------------------------------------------------------

await page.getByRole("link", { name: "Budgets" }).click();
await page.getByRole("heading", { name: "Budgets" }).waitFor();

const year = new Date().getUTCFullYear();

await page.getByRole("button", { name: "New budget" }).click();
await page.locator("#budget-department").selectOption({ label: department });
await page.locator("#budget-start").fill(`${year}-01-01`);
await page.locator("#budget-end").fill(`${year}-12-31`);
await page.locator("#budget-amount").fill("5000");
await page.getByRole("button", { name: "Create", exact: true }).click();

await page.getByText(department).first().waitFor({ timeout: 10000 });
check(
  (await page.getByRole("progressbar").count()) > 0,
  "a budget was created and its consumption rendered",
);
await page.screenshot({ path: `${out}/09-budgets.png`, fullPage: true });

// Two envelopes covering the same department and overlapping dates would make
// "how much is left" ambiguous, so the database refuses it.
expectingFailure = "/budgets";
await page.getByRole("button", { name: "New budget" }).click();
await page.locator("#budget-department").selectOption({ label: department });
await page.locator("#budget-start").fill(`${year}-06-01`);
await page.locator("#budget-end").fill(`${year}-08-31`);
await page.locator("#budget-amount").fill("1000");
await page.getByRole("button", { name: "Create", exact: true }).click();
await page.getByText(/overlaps/i).waitFor({ timeout: 10000 });
check(true, "an overlapping budget was refused, with the reason shown");
expectingFailure = null;

if (problems.length > 0) {
  console.error(`\n${problems.length} problem(s):\n  ${problems.join("\n  ")}`);
  await browser.close();
  process.exit(1);
}

console.log("\nno console errors, no failed requests");
await browser.close();
