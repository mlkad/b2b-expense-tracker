-- Vendor subscriptions: the customer's own recurring software spend, which is
-- what this product tracks. Not to be confused with tenant_subscriptions
-- below, which is our subscription and decides what they are entitled to.

-- name: CreateVendorSubscription :one
INSERT INTO vendor_subscriptions (
    tenant_id, vendor, plan_name, department_id, owner_id,
    amount_minor, currency, cadence, next_charge_on, auto_create_expense
) VALUES (
    @tenant_id, @vendor, @plan_name, @department_id, @owner_id,
    @amount_minor, @currency, @cadence, @next_charge_on, @auto_create_expense
)
RETURNING *;

-- name: ListVendorSubscriptions :many
SELECT vs.*, d.name AS department_name
  FROM vendor_subscriptions vs
  LEFT JOIN departments d ON d.id = vs.department_id AND d.tenant_id = vs.tenant_id
 WHERE vs.tenant_id = @tenant_id
   AND (sqlc.narg('status')::vendor_subscription_status IS NULL OR vs.status = sqlc.narg('status'))
 ORDER BY vs.next_charge_on ASC;

-- ClaimDueVendorSubscriptions is the recurring sweep's only read, and it runs
-- in a system transaction because it crosses tenants.
--
-- FOR UPDATE SKIP LOCKED turns the table into a work queue: two workers running
-- the sweep concurrently take disjoint sets of rows instead of blocking on each
-- other or double-charging. SKIP LOCKED rather than NOWAIT because a row
-- another worker already holds is not an error here - it is simply someone
-- else's work.
--
-- The batch limit bounds how long a single sweep transaction holds locks;
-- the caller loops until it gets a short page.
-- name: ClaimDueVendorSubscriptions :many
SELECT * FROM vendor_subscriptions
WHERE status = 'active'
  AND auto_create_expense
  AND next_charge_on <= @due_on
  AND (last_generated_on IS NULL OR last_generated_on < next_charge_on)
ORDER BY next_charge_on ASC
LIMIT @batch_size
FOR UPDATE SKIP LOCKED;

-- name: AdvanceVendorSubscription :one
UPDATE vendor_subscriptions
SET last_generated_on = next_charge_on,
    next_charge_on    = @next_charge_on
WHERE tenant_id = @tenant_id AND id = @id
RETURNING *;

-- name: UpdateVendorSubscription :one
UPDATE vendor_subscriptions
SET vendor              = @vendor,
    plan_name           = @plan_name,
    department_id       = @department_id,
    owner_id            = @owner_id,
    amount_minor        = @amount_minor,
    currency            = @currency,
    cadence             = @cadence,
    next_charge_on      = @next_charge_on,
    auto_create_expense = @auto_create_expense,
    status              = @status,
    cancelled_at        = @cancelled_at
WHERE tenant_id = @tenant_id AND id = @id
RETURNING *;

-- ---------------------------------------------------------------------------
-- Our subscription, projected from the payment gateway (project #1)
-- ---------------------------------------------------------------------------

-- GetTenantEntitlement is read by the feature-gate middleware on every request
-- to a gated endpoint, so it is answered from tenant_subscriptions_live_idx,
-- which INCLUDEs exactly these columns.
--
-- It returns a row whatever the status: "past_due" is a different answer from
-- "no subscription at all", and the entitlement matrix in
-- internal/domain/billing decides what each one grants. Filtering to live
-- statuses here would collapse the two.
-- name: GetTenantEntitlement :one
SELECT tenant_id, plan_code, status, seats, current_period_end, cancel_at_period_end, trial_end
  FROM tenant_subscriptions
 WHERE tenant_id = @tenant_id;

-- UpsertTenantSubscription applies a relayed event.
--
-- The WHERE clause on the DO UPDATE branch is the out-of-order guard, and it
-- is the reason this is one statement rather than a read followed by a write.
-- The gateway forwards Stripe's stream, which is unordered and at-least-once:
-- a redelivered `customer.subscription.updated` from an hour ago must not
-- overwrite the `canceled` that arrived since. Comparing last_event_at inside
-- the same statement makes the check atomic without a row lock.
--
-- An event that loses the comparison updates nothing and returns no row. The
-- caller treats that as success and marks the delivery 'skipped': it is the
-- expected outcome of an unordered stream, not a fault.
-- name: UpsertTenantSubscription :one
INSERT INTO tenant_subscriptions (
    tenant_id, gateway_subscription_id, gateway_customer_ref, plan_code, status, seats,
    current_period_start, current_period_end, cancel_at_period_end, trial_end,
    last_event_id, last_event_at
) VALUES (
    @tenant_id, @gateway_subscription_id, @gateway_customer_ref, @plan_code, @status, @seats,
    @current_period_start, @current_period_end, @cancel_at_period_end, @trial_end,
    @last_event_id, @last_event_at
)
ON CONFLICT (tenant_id) DO UPDATE
SET gateway_subscription_id = EXCLUDED.gateway_subscription_id,
    gateway_customer_ref    = EXCLUDED.gateway_customer_ref,
    plan_code               = EXCLUDED.plan_code,
    status                  = EXCLUDED.status,
    seats                   = EXCLUDED.seats,
    current_period_start    = EXCLUDED.current_period_start,
    current_period_end      = EXCLUDED.current_period_end,
    cancel_at_period_end    = EXCLUDED.cancel_at_period_end,
    trial_end               = EXCLUDED.trial_end,
    last_event_id           = EXCLUDED.last_event_id,
    last_event_at           = EXCLUDED.last_event_at
WHERE tenant_subscriptions.last_event_at IS NULL
   OR tenant_subscriptions.last_event_at <= EXCLUDED.last_event_at
RETURNING *;

-- ---------------------------------------------------------------------------
-- Relay delivery ledger
-- ---------------------------------------------------------------------------

-- ClaimBillingEvent is the idempotency gate, and the ordering around it is the
-- security property: the caller must verify the HMAC before calling this.
--
-- event_id comes from the request body and is unauthenticated until the
-- signature checks out. Claiming first lets anyone POST a guessed id, plant a
-- settled row, and have the genuine delivery discarded as a duplicate - the
-- relay would answer 200 and the subscription would never update.
--
-- ON CONFLICT DO NOTHING returns no row when the id is already present, which
-- is how a duplicate delivery is detected. A row stuck in 'processing' past
-- the handler deadline is reclaimed by the sweeper, not by this statement.
-- name: ClaimBillingEvent :one
INSERT INTO billing_events (event_id, tenant_id, event_type, payload)
VALUES (@event_id, @tenant_id, @event_type, @payload)
ON CONFLICT (event_id) DO NOTHING
RETURNING *;

-- name: SettleBillingEvent :execrows
UPDATE billing_events
SET status = @status, processed_at = now(), error_detail = @error_detail, tenant_id = coalesce(@tenant_id, tenant_id)
WHERE event_id = @event_id AND status = 'processing';

-- name: ReclaimStuckBillingEvents :many
UPDATE billing_events
SET attempts = attempts + 1, received_at = now()
WHERE event_id IN (
    SELECT event_id FROM billing_events
     WHERE status = 'processing' AND received_at < now() - @stale_after::interval
     ORDER BY received_at ASC
     LIMIT @batch_size
     FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: CountActiveVendorSubscriptions :one
SELECT count(*) FROM vendor_subscriptions
WHERE tenant_id = @tenant_id AND status <> 'cancelled';

-- name: GetVendorSubscription :one
SELECT * FROM vendor_subscriptions WHERE tenant_id = @tenant_id AND id = @id;
