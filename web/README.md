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

## What is here

Sign-in, the authenticated shell, and the overview. The expense list, detail and
forms are the next branch; the API they need is complete and documented in
`../api/openapi.json`.
