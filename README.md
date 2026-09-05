# Multi-Tenant B2B Expense & Team Budget Tracker

Expense claims, department budgets and recurring software spend for small
businesses and agencies. Go, PostgreSQL, one binary for the API and one for the
background workers, a React dashboard on top.

**Go 1.25 · PostgreSQL 16 · pgx/v5 · sqlc · chi · Redis + Asynq · React 19**

---

## The dashboard

<table>
<tr>
<td width="50%">

**Overview** — claims by status, committed spend by department
<img src="docs/screenshots/overview.png" alt="Overview screen">

</td>
<td width="50%">

**Expenses** — filters, search, export, per-row actions
<img src="docs/screenshots/expenses.png" alt="Expenses screen">

</td>
</tr>
<tr>
<td width="50%">

**Approvals** — oldest first, decisions scoped by role
<img src="docs/screenshots/approvals.png" alt="Approvals screen">

</td>
<td width="50%">

**Budgets** — consumption against ceiling, alert threshold
<img src="docs/screenshots/budgets.png" alt="Budgets screen">

</td>
</tr>
<tr>
<td width="50%">

**Organisation → Members** — roles, departments, approval limits
<img src="docs/screenshots/organisation-members.png" alt="Organisation members screen">

</td>
<td width="50%">

**Organisation → Subscriptions** — recurring vendor spend, annualised
<img src="docs/screenshots/organisation-subscriptions.png" alt="Organisation subscriptions screen">

</td>
</tr>
</table>

---

## Running it

```bash
cp .env.example .env
make up            # postgres, redis, minio, mailpit
make migrate-up
make seed          # a demo organisation with claims in every state

make run           # API on :8080
make run-worker    # background jobs, in another shell
make web           # dashboard on :5173, in a third shell
```

Sign in at <http://localhost:5173> as any of the seeded people — each shows a
different slice of the product:

| | | |
|---|---|---|
| `ada@acme.test` | owner | everything, including billing |
| `grace@acme.test` | manager | the approver queue, scoped to Engineering |
| `katherine@acme.test` | finance | settles approved claims, cannot approve |
| `margaret@acme.test` | member | files claims, sees only her own |

Password for all of them: `correct-horse-battery`.

Or containerised, no local Go or Node toolchain needed:

```bash
docker compose -f docker-compose.yml -f docker-compose.stack.yml up --build
```

---

## What it does

A claim moves through a state machine — draft, pending approval, approved or
rejected, paid — with every transition permission-checked, and the caller's
`allowed_actions` computed by the server so the client never re-implements the
rules. Department budgets track committed spend (approved and paid claims,
not pending ones) and raise an alert past a configurable threshold. Vendor
subscriptions turn into draft claims automatically on their charge date, so a
renewal never surprises finance at reconciliation time. Every decision is
exported as a streaming CSV, XLSX or PDF, and every state change is written to
an append-only ledger nothing can edit.

Subscription billing for the organisation itself is delegated to a companion
service, the [Stripe Payment & Subscription
Gateway](https://github.com/mlkad/stripe-payment-service) — see
[docs/BILLING_INTEGRATION.md](docs/BILLING_INTEGRATION.md) for the contract
between the two.

---

## Why it's built this way

### Tenant isolation lives in the database, not in application code

The usual approach to multi-tenancy is `WHERE tenant_id = $1` on every query.
That works until the one query somebody forgets — and a forgotten filter here
is not a bug, it is one customer reading another's expense reports.

So isolation is enforced three ways at once, and none of them is trusted alone:

- **The token.** The tenant id travels in a signed JWT claim. There is no
  `X-Tenant-Id` header and no tenant path segment — either would be a value
  the client gets to pick.
- **The transaction.** `WithTenantTx` binds the session and hands back a
  `*TenantConn` whose underlying `pgx.Tx` is unexported, with no public
  constructor. A repository method that takes a `*TenantConn` cannot be
  called outside a bound transaction — not by convention, by the type system.
- **The database.** Every tenant table carries a `RESTRICTIVE` row-level
  security policy comparing `tenant_id` against a session variable, with
  `FORCE ROW LEVEL SECURITY` so even the table owner is subject to it. An
  unbound session reads as *no rows*, never as *all rows*.

The queries still say `WHERE tenant_id = $1` on top of that — redundant with
RLS on purpose, so the planner gets a constant on the leading index column and
the queries stay correct even if a policy is ever disabled by mistake.

Each of five failure modes here has its own test, verified by breaking the
code first and watching the test catch it: a session-level `SET` leaking a
tenant id across a pooled connection, connecting as a role RLS doesn't apply
to, a permissive policy accidentally widening access, a race between two
approvers on the same claim, and a streamed export holding the whole file in
memory.

### The approval flow is a table, not a switch statement

```
                    ┌──────────────── withdraw ────────────────┐
                    v                                          │
  (new) ──────> draft ────── submit ──────> pending_approval ──┤
                  ^                              │             │
                  │                     approve  │  reject     │
                  │                         ┌────┴────┐        │
               revise                       v         v        │
                  └───────────────────── rejected   approved ──┘
                                                       │
                                                      pay
                                                       v
                                                     paid  (terminal)
```

Transitions are rows in a table — `from`, `action`, `to`, the permission and
guards required — so a test can assert directly that every state is reachable,
every non-terminal state has an exit, and no transition skips its checks. A
rejected claim can only re-enter as a revision, which bumps a revision counter,
so the ledger always shows which version of a claim an approver decided on. An
approved claim cannot be un-approved — history isn't rewritten, a mistake is
corrected with a compensating claim — and separation of duties is enforced per
claim: nobody decides on their own submission, and the person who approved a
claim is not the one who settles it.

### Exports stream, so file size never becomes memory pressure

A four-year export is on the order of 12 MB of spreadsheet. Buffering it means
12 MB held per concurrent download — an endpoint whose memory footprint is set
by the customer's data volume. Nothing here is buffered: XLSX is written as a
ZIP of XML directly onto the response socket, PDF holds exactly one page at a
time with the cross-reference table built from a running byte count, and CSV
holds one row. Heap usage measured at 1,000 rows and at 200,000 rows comes out
the same.

### Billing never sits on the request path

Subscription state for the organisation is a local read-only projection kept
in sync by a signed relay from the billing service, not a live call made
during a request. A billing outage can't lock a tenant out of their own
records, and a card being retried keeps the tenant on their plan rather than
downgrading them mid-dunning.

### Object storage does the work an API server shouldn't

Receipts go straight from the browser to S3-compatible storage: the API signs
a URL, the browser uploads directly, and confirmation checks the object that
actually landed rather than trusting what the client claims it sent. A 25 MB
file never passes through the API process holding a database connection.
Exports the browser downloads work the same way — a short-lived signed link
bound to the exact query, so widening it in the address bar doesn't widen what
it returns.

### The API contract is checked, not just written

[`api/openapi.json`](api/openapi.json) — 47 operations, OpenAPI 3.1 — is
verified against the live chi router in both directions on every test run, and
a second check refuses any operation with no summary, no tag, or no documented
error response. The spec describes the API that's actually running, because
nothing else would catch it drifting.

---

## Layout

```
cmd/api                      HTTP server
cmd/worker                   Asynq workers and scheduler
cmd/migrate                  Migration runner, migrations embedded

internal/domain              Entities, the state machine, the permission matrix.
                              No I/O, no imports from the rest of the project.
internal/platform/postgres   Pool and the tenant transaction boundary
internal/repository/postgres sqlc-generated queries and the mapping to domain
internal/service             Transaction scope, orchestration
internal/transport/http      Router, middleware, handlers
internal/export              Streaming CSV, XLSX and PDF encoders
internal/gateway             Client and relay for the billing service
internal/storage             S3-compatible object store, SigV4 presigning
internal/worker              Background jobs

db/migrations                goose migrations, row-level security policies
db/queries                   Hand-written SQL, compiled by sqlc
test/integration             Real PostgreSQL, MinIO and Mailpit via testcontainers
web                           React dashboard — see web/README.md
```

---

## Development

```bash
make tools               # goose and sqlc
make test                # unit tests, -race
make test-integration    # spins up its own PostgreSQL, MinIO and Mailpit
make cover               # combined unit + integration coverage
make check               # fmt, vet, test
make openapi-validate    # api/openapi.json against the OpenAPI 3.1 schema

make web-check            # dashboard: typecheck, lint, unit tests
make web-smoke             # drives the running dashboard with a real browser
```

Two container images build from one Dockerfile by build stage: `api` and
`worker` are `FROM scratch`, running as a non-root user with no shell and no
package manager. The migration image is the one exception — `alpine` with
`psql`, for the moment a migration needs a human at a prompt.
