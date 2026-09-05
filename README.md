# Multi-Tenant B2B Expense & Team Budget Tracker

Expense claims, department budgets and recurring software spend for small
businesses and agencies. Go, PostgreSQL, one binary for the API and one for the
workers.

**Go 1.25 · PostgreSQL 16 · pgx/v5 · sqlc · chi · Redis + Asynq**

The hard part of a multi-tenant product is not writing `WHERE tenant_id = $1`.
It is that you have to write it in every query, forever, and one omission is a
data breach rather than a bug. So it is not written in the queries at all — it
is enforced by the database, and the Go types are shaped so that a query
without a tenant bound will not compile.

---

## Five things that would break, and don't

Each has a test. Each test was checked by breaking the code and watching it
fail.

### Take away the transaction, and one customer reads another's expenses

The tenant is bound with `set_config('app.tenant_id', $1, true)` — the third
argument is `is_local`, so the setting is reverted by `COMMIT`. On a pooled
connection that is the whole ballgame: a session-level `SET` leaves the last
tenant's id on the connection, and the next checkout — possibly a different
customer, microseconds later — inherits it.

```
--- FAIL: TestBindingDoesNotSurviveTheTransaction
    rls_test.go:281: round 0: a pooled connection came back bound to
        "8f2c...": the next request would inherit this tenant
```

The pool in that test holds four connections and the test runs twenty
transactions, so reuse is certain rather than hoped for.

### Connect as the migration owner, and RLS silently does nothing

PostgreSQL exempts a table's owner from its own policies unless
`FORCE ROW LEVEL SECURITY` is set, and exempts superusers unconditionally. A
deployment pointed at the wrong DSN works perfectly, passes every test, and
isolates nothing — `\d` still lists the policies.

Two things stop it. Every tenant table sets `FORCE`, and the pool refuses to
open at all if its role can see through RLS:

```
fatal: refusing to start: database role "expense" is a superuser and has
BYPASSRLS, so row-level security would not isolate tenants; connect as the
runtime role (expense_app), not the migration owner
```

### Make the isolation policy permissive, and the next feature widens it

Permissive policies are OR-ed together, so adding one for a new feature
*grants* access — and the grant looks like an ordinary feature commit in
review. The isolation policies are `RESTRICTIVE`, which is AND-ed with
everything else, so they can only be removed deliberately.

```
--- FAIL: TestIsolationPoliciesAreRestrictive
    tenant_isolation on expenses is PERMISSIVE; it must be RESTRICTIVE so no
    other policy can widen it
```

### Drop the row lock, and two approvers approve the same claim

`SELECT ... FOR UPDATE NOWAIT` guards the read-decide-write. The
compare-and-swap on `version` would catch a lost update on its own — but only
after the audit row had already been written, because the ledger insert and the
status update are two statements.

```
--- FAIL: TestConcurrentApprovalsProduceOneDecision
    round 0: 2 approvals succeeded, want exactly 1
    round 0: ledger holds 2 approvals, want 1
```

Eight rounds, two racing approvers each.

### Buffer the export, and one customer's download takes the service down

A four-year export is roughly 12 MB of spreadsheet. Buffered, that is 12 MB per
concurrent download and an endpoint whose memory cost is set by the customer's
data volume.

Nothing is buffered. XLSX is a ZIP of XML written straight onto the socket, PDF
holds exactly one page, CSV holds one row:

```
    heap in use: 824 KiB at 1k rows, 4152 KiB at 200k rows   (csv)
    heap in use: 1624 KiB at 1k rows, 4064 KiB at 200k rows  (xlsx)
    heap in use: 1656 KiB at 1k rows, 4312 KiB at 200k rows  (pdf)
```

Two hundred times the rows, the same memory.

---

## How tenancy actually works

Three layers, none of which is trusted to be the only one.

**The token.** The tenant travels in a signed JWT claim. There is no
`X-Tenant-Id` header and no tenant path segment, because either would be a
value the client picks.

**The transaction.** `WithTenantTx` opens a transaction, binds the session, and
hands back a `*TenantConn`. Its `pgx.Tx` is unexported and there is no
constructor, so a repository method taking a `*TenantConn` cannot be called
outside a bound transaction — not by convention, but because the program would
not compile.

```go
err := db.WithTenantTx(ctx, postgres.Binding{TenantID: subject.TenantID}, 
    func(ctx context.Context, tc *postgres.TenantConn) error {
        claim, err := expenses.GetForUpdate(ctx, tc, id)   // needs a *TenantConn
        ...
    })
```

**The database.** Every tenant table carries a `RESTRICTIVE` policy comparing
`tenant_id` against the session variable, with `FORCE` set so the owner is
subject to it too. An unbound session yields `NULL`, `tenant_id = NULL` is not
`TRUE`, and the policy denies every row — unbound reads as *no rows*, never as
*all rows*.

The queries still say `WHERE tenant_id = $1` anyway, filled from
`tc.TenantID()`. That is redundant with RLS on purpose: it gives the planner a
constant on the leading index column, and it keeps the queries isolating if a
policy is ever disabled. Two mechanisms, neither assumed from the other.

The one widening is `WithSystemTx`, for the billing relay and the cross-tenant
sweeps. It is logged at `warn` on every call, and it widens the policies rather
than replacing them.

---

## The approval state machine

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

Three absences are as deliberate as the edges:

- **`rejected` → `pending_approval` does not exist.** A rejected claim goes
  back through `revise`, which bumps the revision counter. Without that step a
  submitter could re-present an identical claim until an approver clicked the
  wrong button, and the ledger would show two decisions with nothing to tell
  them apart.
- **`approved` → `rejected` does not exist.** Reversing a decision rewrites
  history; a mistaken approval is corrected by a compensating claim, and both
  facts stay in the ledger.
- **`paid` has no outgoing edges.** The money is gone.

Separation of duties is enforced per claim, not just per role. Nobody approves
their own claim, and the approver of a claim cannot also settle it — an owner
holds both permissions, so a role check alone would not stop them.

The transition table is data, not a switch statement, so the tests read it
directly: every state is reachable, every non-terminal state has an exit, and
every `(state, action)` pair is either an edge or an explicit refusal.

---

## Streaming exports

`GET /api/v1/reports/expenses/export?format=xlsx&from=2026-01-01&to=2026-03-31`

CSV, XLSX and PDF, none of them buffered.

XLSX gets the interesting constraint. The conventional layout interns every
string in `sharedStrings.xml` and references them by index — smaller, and it
requires holding every distinct string until the last row is known. This writer
uses `t="inlineStr"` instead and omits `<dimension>`, so the whole document is
append-only and `archive/zip` can push it onto the socket as it is produced.
Amounts are typed numbers with a currency format and dates are real serial
dates, so the file sums and sorts rather than being a grid of text.

PDF cannot stream a page — the content stream's `/Length` precedes its bytes —
so it buffers exactly one page and writes each out as it is finished. The
cross-reference table is built from a byte counter as the file is written,
which is possible because PDF puts `startxref` at the end by design.

Verified against readers that are not this codebase: `openpyxl` with warnings
as errors, and macOS CoreGraphics (the engine behind Preview) for the PDF.

Non-Latin-1 text in the PDF renders as `?`. The base-14 fonts are single-byte
WinAnsi and embedding a font subset is a bigger change than this format
justifies — CSV and XLSX carry the full range.

---

## Billing

The subscription lives in the [Stripe Payment & Subscription
Gateway](https://github.com/mlkad/stripe-payment-service) (project #1). That
service owns the Stripe key, receives Stripe's webhooks and resolves their
ordering; this one keeps a local projection and answers every entitlement check
from it.

Which means the gateway is never called on the request path. A billing outage
cannot lock a customer out of their own expense records, and a customer whose
card is being retried keeps working — `past_due` still grants the plan, because
revoking access during dunning is how a recoverable payment problem becomes a
cancellation.

The relay receiver applies the lesson project #1 learned against Stripe:
**verify the signature before claiming the event id.** The id arrives in the
body and is unauthenticated until then, so claiming first lets anyone POST a
guessed id, plant a settled row, and have the genuine delivery discarded as a
duplicate — answered `200`, never processed.

Full contract, including the two additions it asks of project #1:
[docs/BILLING_INTEGRATION.md](docs/BILLING_INTEGRATION.md).

---

## Receipts

`POST /expenses/{id}/attachments/presign` → client PUTs to the object store →
`POST /expenses/{id}/attachments`

Two steps rather than one multipart upload, because a receipt is up to 25 MiB
and an API that proxied it would hold that much per concurrent upload, spend its
request deadline on the client's connection speed, and put a user-chosen file
through the process that also holds database credentials. The bytes never come
through this service; its whole involvement is one HMAC and one row.

The signature binds the upload to the declared content type and SHA-256, so the
object store — the only party that sees the content — is what verifies it:

```
--- upload with content that does not match the signed checksum
    HTTP 400 XAmzContentChecksumMismatch
```

What a presigned PUT *cannot* carry is a length limit; only the browser POST
policy form can. So the declared size is checked before signing and the stored
size is checked again on confirm, which is the honest version of that
guarantee — a client can waste bucket space once and cannot get a row for it.

Confirm stat-s the object before recording it. Without that the attachment list
would be a set of assertions rather than a set of files: a caller could register
a receipt it never uploaded, or point a row at another tenant's object — and the
object store has no row-level security to stop them reading it afterwards.

Content types are an allowlist of PDF and images. `image/svg+xml` is the one
people forget: an image to a human, a scriptable document to a browser, and a
stored XSS against whoever opens the receipt.

SigV4 presigning is written out in `internal/storage` rather than taken from the
AWS SDK — this service needs one thing from S3 and the SDK is a large surface to
carry for a hundred lines of HMAC. The trade is that a mistake is silent until a
request is rejected, so it is verified against a real MinIO in the integration
suite rather than against my own idea of the algorithm.

---

## The API contract

[`api/openapi.json`](api/openapi.json) — OpenAPI 3.1, 47 operations.

It is **checked against the router**, not maintained alongside it. An API
document nobody verifies is a document describing last quarter's API, so
`TestOpenAPIMatchesTheRouter` walks the real chi tree and compares it with the
spec in both directions:

```
--- FAIL: TestOpenAPIMatchesTheRouter
    these routes exist but are not in api/openapi.json:
      post /api/v1/auth/logout
```

That is the test finding a real omission on its first run — the route is
declared with `r.With(...)` rather than `r.Post(...)`, so it had been missed.

A second test refuses operations with no summary, no tag, no `operationId`, a
duplicate `operationId`, or no documented failure response. An operation that
documents only its happy path tells a client nothing about what to handle.

---

## Layout

```
cmd/api                     HTTP server
cmd/worker                  Asynq workers and scheduler
cmd/migrate                 Migration runner, migrations embedded

internal/domain             Entities, the state machine, the permission matrix.
                            No I/O, no imports from the rest of the project.
internal/platform/postgres  Pool and the tenant transaction boundary
internal/repository/postgres sqlc-generated queries and the mapping to domain
internal/service            Transaction scope, orchestration
internal/transport/http     Router, middleware, handlers
internal/export             Streaming CSV, XLSX and PDF encoders
internal/gateway            Client and relay for project #1
internal/storage            S3-compatible object store, SigV4 presigning
internal/worker             Background jobs

db/migrations               goose migrations, RLS policies in 00006
db/queries                  Hand-written SQL, compiled by sqlc
test/integration            Real PostgreSQL via testcontainers
```

---

## Running it

```bash
cp .env.example .env
make tools          # goose and sqlc
make up             # postgres and redis
make migrate-up
make run            # api on :8080
make run-worker     # in another shell
```

```bash
make test               # unit, -race
make test-integration   # spins up its own postgres
make cover              # one coverage number, unit and integration together
make check              # fmt, vet, test
make openapi-validate   # the spec against the OpenAPI 3.1 schema
```

Combined coverage over unit and integration tests: **60%**. The persistence and
tenancy layers cannot be meaningfully exercised without a database, so a figure
measured without the integration tag would describe a different program than the
one that ships.

### Or containerised

```bash
docker compose -f docker-compose.yml -f docker-compose.stack.yml up --build
```

Two images from one Dockerfile, chosen by build stage rather than by an
environment variable — there is no shell in either to read one:

```
docker build --target api     -t expense-api .
docker build --target worker  -t expense-worker .
docker build --target migrate -t expense-migrate .
```

`api` and `worker` are `FROM scratch`, 38 MB, running as uid 65532. No shell,
no package manager, no libc: nothing for an attacker who reaches code execution
to pivot with, and nothing that needs patching between releases of this service.
The cost is that `docker exec` is impossible, which is the intended trade —
debugging goes through logs, metrics and `/readyz`.

That also rules out the usual `curl -f localhost/readyz` health check, so the
binary probes itself: `api -healthcheck` reads the same `HTTP_ADDR` the server
binds and exits non-zero if readiness fails.

The migration image is the exception, and is `alpine` with `psql` in it — a
migration that failed half way through is exactly when someone needs a prompt
inside the same network namespace. It embeds the migrations with `go:embed`, so
the binary and the schema it expects are one artefact.

The integration suite creates a throwaway PostgreSQL container, applies the
real migrations, and connects as `expense_app` — the non-superuser runtime
role. Running it as the owner would make every isolation assertion pass with
the policies deleted.
