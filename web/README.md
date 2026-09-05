# Dashboard

React 19, Vite, TypeScript, Tailwind 4, TanStack Query, zod, zustand, nuqs.

```bash
npm install
npm run dev      # proxies /api to http://127.0.0.1:8080
npm run check    # typecheck, lint, layer boundaries, unit tests
npm run layers   # the Feature-Sliced import rules on their own
npm run smoke    # drives the running app with a real browser
```

## Layout

Feature-Sliced. Six layers, and an import may only go **downwards**:

```
app/        providers, router, query client, styles
pages/      one folder per screen; composes the layers below
widgets/    composite blocks - the shell, the claim table, the receipt panel
features/   things a person does - sign in, filter, decide, upload, export
entities/   the business objects - expense, budget, member, session, billing
shared/     the API client, the UI kit, formatting, config
```

Two rules, checked by `scripts/layers.mjs` in CI:

1. **A layer imports only from layers below it.** Otherwise `shared` ends up
   importing a page and nothing can be read or moved on its own again.
2. **A slice is entered through its `index.ts`.** Reaching into
   `entities/expense/model/queries` makes every file public and every rename a
   breaking change.

A convention nothing checks is a convention for about a month. The check earned
its place immediately — it caught three violations the moment it was written,
including two entities importing each other. Roles moved down into
`shared/config` (the session and the member list both speak that vocabulary and
neither owns it), the spend summary moved into `entities/expense` where it
belongs, and the export menu now takes the query to sign rather than a
dependency on the filter feature.

## Responses are parsed, not asserted

Every response goes through a zod schema before anything reads it. `as Expense`
is a claim about the server that nothing verifies; `decode(expenseSchema, …)`
is a check that fails **at the endpoint that changed**, naming the field:

```
GET /expenses did not match the expected shape (items.0.amount.amount_minor: expected int)
```

Unknown keys are stripped rather than rejected, so a server adding a field never
breaks a deployed dashboard. A missing or mistyped one does throw, because the
screen cannot render correctly from it and saying so beats drawing it wrong.

This found a real mismatch on the first run: every collection this API returns
is wrapped in `{ "items": [...] }`, and four of the rewritten queries had been
written against a bare array.

## Server state is TanStack Query; client state is zustand and the URL

Three kinds of state, kept apart:

- **Server state** — claims, budgets, members. Cached by query key, invalidated
  by the mutation that changed it. A decision invalidates `["expenses"]`, which
  is a prefix of the list, the detail and the approver queue, so all three
  refresh together instead of one screen showing a claim that was approved a
  moment ago on another.
- **Session** — a zustand store. A context re-renders every consumer whenever
  any part of its value changes; components here select the one field they read.
- **Filters and the open tab** — in the address bar, via nuqs. A filtered view
  can be sent to a colleague, kept as a bookmark, and survives a reload, and
  back steps through the filters somebody actually applied.

Retries are for a network that failed, not a server that answered. A 403 retried
three times is three 403s and a slower error message; a 409 retried is a
decision the user has not been told about. Only 5xx and a request that never
arrived are tried again — and a response that failed its schema will fail it
again.

From the repository root, `make seed` fills the database with an organisation
that has claims in every state — an empty dashboard shows nothing about whether
any of this works.

The smoke script creates its own claim rather than picking one from the table.
Seeded claims belong to other people, and previous runs leave claims already
submitted, so acting on whatever is in the list makes the assertions depend on
the state of the database rather than on the product.

## The session

The access token lives **in memory** — a module-scoped value in
`shared/api/token.ts`, gone when the tab closes. Not `localStorage`, not a
readable cookie.
Both are reachable by any script the page ever runs, so one XSS bug in one
dependency becomes a stolen credential, and a stored one outlives the tab so the
theft outlives the session.

The refresh token is an `HttpOnly` cookie scoped to `/api/v1/auth`, which script
cannot read at all. A reload has no access token, asks for one, and gets it if
the browser still holds the cookie.

That is also why the dev server **proxies** `/api` instead of calling
`localhost:8080` directly. Cross-origin would make the cookie `SameSite=None`,
which requires `Secure`, which means it would not be sent over plain HTTP — and
sessions would silently never survive a reload in development while working
fine in production.

### Refreshing is single-flight, and that is not an optimisation

Refresh tokens rotate, and the server treats a reused one as theft: it revokes
the whole rotation family. So two concurrent refreshes sign the user out.

Both paths that can trigger one — a 401 on any request, and the bootstrap on
load — go through the same in-flight promise, and the bootstrap is additionally
memoised at module scope. `scripts/smoke.mjs` found what
happens when they do not:

```
POST /api/v1/auth/refresh 401  (00:41:15.466)
POST /api/v1/auth/refresh 401  (00:41:15.466)
session survived a reload: false
```

React StrictMode invokes a mount effect twice, so two refreshes left in the same
millisecond, the second looked like a replay, and the session died on every
reload. StrictMode only made it reproducible — two tabs opening together would
do the same in production.

## Money

Amounts are integer minor units plus a currency, everywhere. Nothing parses the
server's `formatted` string back into a number: JavaScript has one numeric type
and it is a double, so a round trip through a decimal string is where a total
quietly stops matching the receipts.

`parseAmount` is string arithmetic rather than `Math.round(parseFloat(x) * 100)`
— that rounds `1.005` down, because the nearest double is below it. A cent, in
the customer's disadvantage, on a figure they typed exactly.

The currency's exponent decides the decimal places: two for USD, **none for
JPY**, three for KWD. Assuming two is not an error, it is an invoice off by a
factor of a hundred.

## Permissions

`/me` returns the caller's permission list, and the navigation hides what they
cannot use. That is a **convenience, never an enforcement point** — the client
is free to lie about what it hid, and every endpoint checks for itself.

Likewise, each claim carries `allowed_actions` computed by the server's state
machine. The dashboard renders one button per entry and does not decide for
itself: a second copy of the transition rules in TypeScript would drift, and the
symptom would be a button that 403s.

## Dates

`spent_at` is a **calendar date** and is rendered in UTC. Rendering it in the
browser's zone shows the previous day to anybody west of Greenwich, which gets
reported as "the export is wrong". Timestamps like "approved at 14:02" are
instants and *are* rendered in the reader's zone — the opposite choice, for the
opposite reason.

## Pagination

Next and previous, with no page numbers and no jump-to-page. The server cannot
answer either without counting the whole filtered set on every request, and a
`COUNT` over a tenant's history costs more than the page itself. Offering
controls the data model cannot support is how a list ends up slow for everybody
so that a few people can jump to the end.

There is no way to compute the previous page's cursor from the current one, and
pretending otherwise is how a "previous" button starts skipping rows. What makes
going back work is that `useInfiniteQuery` keeps every page it has fetched, so
stepping back is a read from the cache and costs no request at all. Changing a
filter makes a different query key, and the page index resets with it.

Previous results stay on screen while the next request is in flight
(`keepPreviousData`). Blanking a table on every filter change makes the page
jump and costs the reader their place — a unit test asserts this, because it is
the kind of thing a refactor removes silently.

## Forms validate twice, on purpose

A zod schema runs before anything is sent, and whatever the server rejects is
kept beside the field it names. Neither replaces the other: the schema catches
an empty field without a round trip, and the server catches what only it can
know — that an address is already a member, that an amount is over an approval
limit.

The claim schema is **built per currency**, because how many decimal places are
valid is a property of the currency and not of the form. One fixed "two decimal
places" rule would reject a correct yen amount and quietly accept a dinar one
that loses a digit.

## Receipts

The bytes never pass through the API. The browser computes the file's SHA-256,
asks the API to sign a URL, `PUT`s straight to the object store, then tells the
API to record it.

The digest is computed here because **the store verifies against it** — a
mismatch is refused with `XAmzContentChecksumMismatch`, which is a stronger
guarantee than an API that never sees the file could make. `crypto.subtle` needs
a secure context, so over plain HTTP the upload fails with an explicit message
rather than silently falling back to something weaker.

The response body of the upload is read even though nothing needs it. Leaving a
stream unconsumed keeps the connection open until collection, and the browser
reports the finished request as aborted — which shows up in a network log as a
failed upload that plainly succeeded.

## Exports are a signed link, not a plain one

A browser navigating to a download **cannot set an Authorization header**. An
earlier version of this used a plain `<a href>` to the export route, and the
buttons did nothing but produce a 401 page — the refresh cookie is path-scoped
to `/api/v1/auth` and never reaches `/reports`, so the request arrived with no
credential at all.

Fetching the report with a header instead and turning it into a Blob would hold
tens of megabytes in the tab, which is the cost the streaming export exists to
avoid. So the click asks the API for a link signed for that exact query, and
navigates to it — the same shape as a presigned receipt URL.

The token lives one minute and is bound to the query string, because a URL is
written down by every access log it passes through, and because the export
reads its filters from the URL: anything the signature did not cover would be a
parameter the holder could widen in the address bar.

## What is here

All of it: sign-in, the shell, the overview, the expense list with filters and
export, the claim detail with its audit ledger and receipts, the create and edit
forms, the approver queue, budgets with consumption, and the organisation
sections for members, departments and vendor subscriptions.

## The smoke script is not decoration

Nineteen assertions against a live API, and it has now found five things the
unit tests could not:

1. **The session did not survive a reload** — two refreshes in the same
   millisecond, the second read as a replayed token.
2. **`waitForLoadState("networkidle")` is meaningless after a SPA navigation** —
   no document loads, so it resolves before the data request starts.
3. **The API mixed `snake_case` and `PascalCase` in one object** — handlers were
   returning repository structs whose embedded fields carried JSON tags while
   the added ones did not. Fixed with DTOs on the server.
4. **The export buttons were entirely broken** — a navigation carries no
   Authorization header, so every download returned 401. Fixed with signed,
   query-bound download links.
5. **A rewritten claim form left the detail page with no buttons** — `POST
   /expenses` answers with the claim alone, because only the read path computes
   `allowed_actions`; priming the detail cache with that smaller response hid
   every transition until the entry went stale.

It is also idempotent: it creates a department of its own each run, because a
script that only passes against a fresh database is a script nobody runs twice.
