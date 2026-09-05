# Billing integration with the Stripe Payment & Subscription Gateway

This service does not talk to Stripe. Project #1 —
[stripe-payment-service](https://github.com/mlkad/stripe-payment-service) —
holds the API key, receives Stripe's webhooks, resolves their ordering and
idempotency, and owns the authoritative subscription record.

## Division of responsibility

| | Gateway (project #1) | Expense tracker (this service) |
|---|---|---|
| Stripe API key | holds it | never sees it |
| Stripe webhooks | receives, verifies, orders | never receives |
| `cus_*` / `sub_*` ids | authoritative | never stored |
| Subscription record | source of truth | local projection |
| Entitlement decisions | none | all of them |

The projection is `tenant_subscriptions`, one row per tenant, written only by
the relay. `internal/domain/billing` turns it into an answer.

## Why the gateway is never called on the request path

Entitlement is checked on every gated request. Making that a network call to
another service would mean:

- a billing outage locks every customer out of their own expense records;
- p99 latency on ordinary endpoints inherits the gateway's p99;
- a database transaction is held open across a network call to a service whose
  latency this one does not control, which under load exhausts the pool and
  takes down endpoints with no billing involvement at all.

So the check is a local indexed read of `tenant_subscriptions`, and the gateway
is called for exactly three things: starting a checkout, opening the customer
portal, and the nightly reconciliation sweep.

This mirrors the argument project #1 makes for keeping its own projection of
Stripe rather than calling `api.stripe.com` on its request path.

## Event flow

```
  Stripe ──webhook──> Gateway ──relay──> Expense tracker
                         │                    │
              (owns ordering,          (projects into
               idempotency,            tenant_subscriptions,
               dunning state)           answers entitlement)
```

### The relay contract

`POST /internal/billing/relay`

```
X-Billing-Signature: t=<unix seconds>,v1=<hex hmac-sha256>
Content-Type: application/json
```

The signature covers `"<timestamp>." + <raw body bytes>`, keyed with
`BILLING_RELAY_SECRET`. Several `v1=` values may appear so a secret can be
rotated without a flag day; any one matching is accepted.

```json
{
  "id": "evt_1PxyzAbCdEf",
  "type": "subscription.updated",
  "created_at": "2026-03-14T09:12:44Z",
  "tenant_ref": "0b8f2c1e-....",
  "subscription": {
    "id": "sub_1Pxyz",
    "customer_ref": "0b8f2c1e-....",
    "plan_code": "growth",
    "status": "active",
    "quantity": 25,
    "current_period_start": "2026-03-01T00:00:00Z",
    "current_period_end": "2026-04-01T00:00:00Z",
    "cancel_at_period_end": false,
    "trial_end": null
  }
}
```

`tenant_ref` is the tenant's id in this service, which is also what is sent to
the gateway as its customer reference. Deriving the mapping rather than storing
it twice means a relayed event is still resolvable if the link write was ever
lost.

### Ordering of operations in the receiver

This is the part worth reviewing carefully.

1. Read the **raw body**. Not a decoded struct — the signature covers the exact
   bytes received, and re-encoding a parsed document verifies something the
   sender never signed.
2. Verify the timestamp is within tolerance (5 minutes), then the HMAC.
3. **Only then** claim `event_id` in `billing_events`.

Swapping 2 and 3 is the bug. `event_id` arrives in the body and is
unauthenticated until the signature checks out. Claim first, and anyone can
POST a guessed id, plant a settled row, and have the genuine delivery arrive to
find itself a duplicate — answered `200`, never processed, and the subscription
silently never updates. Project #1 has the same test against Stripe; this is
the same failure one hop downstream.

### Status codes, and what they make the gateway do

A 2xx stops redelivery; anything else schedules another attempt.

| Situation | Status | Why |
|---|---|---|
| Applied | 200 | done |
| Duplicate `event_id` | 200 | already done |
| Unknown event type | 200 | this build does not handle it; retrying forever helps nobody |
| Event older than applied state | 200 | expected of an unordered stream |
| Unknown `tenant_ref` | 200 | the gateway may serve more than one product |
| Bad or missing signature | 400 | will never verify |
| Timestamp outside tolerance | 408 | might succeed if the clocks are what is wrong |
| Processing failed | 500 | retry |

### Out-of-order deliveries

The gateway forwards a stream that is unordered and at-least-once, because
Stripe's is. `UpsertTenantSubscription` compares `last_event_at` inside the
`ON CONFLICT DO UPDATE` clause, so a redelivered `subscription.updated` from an
hour ago cannot overwrite the `canceled` that arrived since. Losing that
comparison updates nothing, returns no row, and is recorded as `skipped`.

Covered by `TestOutOfOrderEventsDoNotRegress`.

### Reconciliation

The relay is at-least-once but not guaranteed: a delivery during a deployment
window, or one that failed every retry, leaves the projection behind. Nothing
on the request path notices — the local read looks perfectly healthy — so a
nightly job asks the gateway directly for each tenant with a billing reference
and applies whatever it says. Reconciled state is stamped with `now()`, so it
wins the out-of-order comparison against anything already applied.

## What this asks of project #1

Two additions, both small and both additive:

**1. An outbound relay.** After the gateway settles a Stripe event, POST the
normalised shape above to each subscriber, signed as described, with retries
and a dead-letter queue. It already has the delivery ledger and sweeper
machinery this needs — `processed_webhooks` and the webhook sweeper — pointed
inbound rather than outbound.

**2. Service-token authentication.** The gateway's routes authenticate an end
user's bearer token. A server-to-server caller has no end user, so this service
presents a JWT signed with a shared secret:

```
iss: b2b-expense-tracker
aud: stripe-payment-service
sub: <tenant's customer reference>
exp: iat + 60s
jti: <uuid>
```

Accepted on the service audience only, and only for the three routes above.
The short lifetime is why it is minted per call rather than cached.

Until both exist, this service runs with `BILLING_GATEWAY_URL` unset: every
tenant resolves to the free plan, and the API logs a warning at startup saying
so rather than appearing to have working billing.

## Degradation

A lapsed subscription falls back to the free tier, not to nothing. Customers
keep their history, keep read access, and keep the ability to export it; they
lose the seats and features they stopped paying for. Locking a company out of
records they are legally required to retain, because a card expired, is not a
lever this product pulls.

`past_due` still grants the plan for the same reason — Stripe is still retrying,
and revoking access before dunning finishes churns customers who would have
paid.
