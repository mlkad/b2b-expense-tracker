# Dashboard

React 19, Vite, TypeScript, Tailwind 4.

```bash
npm install
npm run dev      # proxies /api to http://127.0.0.1:8080
npm run check    # typecheck, lint, unit tests
npm run smoke    # drives the running app with a real browser
```

## The session

The access token lives **in memory** — a field on an object held by the
provider, gone when the tab closes. Not `localStorage`, not a readable cookie.
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
load — go through the same in-flight promise. `scripts/smoke.mjs` found what
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

## Lint

`react/refs` is off in `.oxlintrc.json`. It flags any closure that reads
`ref.current` inside a component body, which is the documented way to give
non-React code — here, the API client — a stable accessor to a mutable value.
The alternative it suggests, holding the token in state, would re-create the
client on every rotation and re-run every effect that depends on it, so a
routine refresh would reload the whole dashboard.

## Pagination

Next and previous, with no page numbers and no jump-to-page. The server cannot
answer either without counting the whole filtered set on every request, and a
`COUNT` over a tenant's history costs more than the page itself. Offering
controls the data model cannot support is how a list ends up slow for everybody
so that a few people can jump to the end.

Going back is a **stack of the cursors already visited**. There is no way to
compute the previous page's cursor from the current one, and pretending
otherwise is how a "previous" button starts skipping rows. Changing a filter
discards the stack, because those cursors point into a result set that no longer
exists.

## Loading state is derived, not stored

`useResource` keeps the key its data was fetched for; anything else is stale by
definition. A `setLoading(true)` at the top of an effect schedules an extra
render on every mount and every query change, for a boolean that is already
computable — and the linter is right to flag it.

Previous results stay on screen while the next request is in flight. Blanking a
table on every filter change makes the page jump and costs the reader their
place.

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

Eighteen assertions against a live API, and it has now found three things the
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

It is also idempotent: it creates a department of its own each run, because a
script that only passes against a fresh database is a script nobody runs twice.
