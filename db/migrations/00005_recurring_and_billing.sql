-- =============================================================================
-- 00005_recurring_and_billing
--
-- Two things that both look like "subscriptions" and must never be confused:
--
--   * vendor_subscriptions - the customer's own recurring software spend,
--     which this product exists to track. Tenant data, under RLS.
--
--   * tenant_subscriptions - our subscription, projected from the Stripe
--     Payment & Subscription Gateway (project #1), which decides what this
--     tenant is entitled to. Written only by the billing relay.
--
-- The naming is deliberately asymmetric so that a grep for either one never
-- returns the other.
-- =============================================================================

-- +goose Up

CREATE TYPE billing_cadence AS ENUM ('weekly', 'monthly', 'quarterly', 'annual');
CREATE TYPE vendor_subscription_status AS ENUM ('active', 'paused', 'cancelled');

CREATE TABLE vendor_subscriptions (
    id            UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID    NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,

    vendor        TEXT    NOT NULL,          -- "Figma", "AWS", "Notion"
    plan_name     TEXT,
    department_id UUID,
    owner_id      UUID,                      -- membership that owns the renewal

    amount_minor  BIGINT  NOT NULL,
    currency      CHAR(3) NOT NULL,
    cadence       billing_cadence NOT NULL,

    status        vendor_subscription_status NOT NULL DEFAULT 'active',

    -- Next date a charge is expected. The worker materialises a draft expense
    -- on or after this date and advances it by one cadence.
    next_charge_on DATE   NOT NULL,

    -- Last date a draft was generated for. Compared against next_charge_on to
    -- make the sweep idempotent even if the advance and the insert are ever
    -- split across transactions.
    last_generated_on DATE,

    -- FALSE means "track the cost, but do not file a claim" - a subscription
    -- paid by corporate card that reconciles elsewhere.
    auto_create_expense BOOLEAN NOT NULL DEFAULT TRUE,

    cancelled_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT vendor_subscriptions_id_tenant_key UNIQUE (id, tenant_id),
    CONSTRAINT vendor_subscriptions_amount_chk    CHECK (amount_minor > 0),
    CONSTRAINT vendor_subscriptions_currency_chk  CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT vendor_subscriptions_vendor_chk
        CHECK (char_length(btrim(vendor)) BETWEEN 1 AND 200),
    CONSTRAINT vendor_subscriptions_cancelled_chk
        CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL)),
    CONSTRAINT vendor_subscriptions_department_fk
        FOREIGN KEY (department_id, tenant_id)
        REFERENCES departments (id, tenant_id) ON DELETE SET NULL,
    CONSTRAINT vendor_subscriptions_owner_fk
        FOREIGN KEY (owner_id, tenant_id)
        REFERENCES memberships (id, tenant_id) ON DELETE SET NULL
);

-- The sweep's only query: everything due today, across all tenants. It runs in
-- a system transaction, so the index has to be useful without a tenant
-- predicate leading it - the one index in this schema that starts with
-- something other than tenant_id, and it is why the sweep is the only reader.
CREATE INDEX vendor_subscriptions_due_idx
    ON vendor_subscriptions (next_charge_on, tenant_id)
    WHERE status = 'active' AND auto_create_expense;

CREATE INDEX vendor_subscriptions_tenant_idx
    ON vendor_subscriptions (tenant_id, status, next_charge_on);

CREATE TRIGGER vendor_subscriptions_touch_updated_at
    BEFORE UPDATE ON vendor_subscriptions
    FOR EACH ROW EXECUTE FUNCTION app.touch_updated_at();

ALTER TABLE expenses
    ADD CONSTRAINT expenses_source_subscription_fk
    FOREIGN KEY (source_subscription_id, tenant_id)
    REFERENCES vendor_subscriptions (id, tenant_id) ON DELETE SET NULL;

-- Idempotency for the recurring sweep, enforced by the database rather than by
-- the worker being careful. A retried job, a duplicate delivery or two workers
-- racing all collide here and the loser sees a unique violation it can treat
-- as success.
CREATE UNIQUE INDEX expenses_recurring_once_per_charge_key
    ON expenses (tenant_id, source_subscription_id, spent_at)
    WHERE source_subscription_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Our own billing, projected from project #1
-- ---------------------------------------------------------------------------

-- Mirrors the gateway's subscription_status enum. Kept as TEXT with a CHECK
-- rather than an ENUM on purpose: the gateway owns this vocabulary, and a
-- value it adds must not require a migration here before deliveries can be
-- accepted. The CHECK is a tripwire, and the relay's failure mode when it
-- fires is a rejected delivery it will redeliver - not a lost event.
CREATE TABLE tenant_subscriptions (
    tenant_id               UUID        PRIMARY KEY REFERENCES tenants (id) ON DELETE CASCADE,

    gateway_subscription_id TEXT        NOT NULL,
    gateway_customer_ref    TEXT        NOT NULL,

    -- Maps to the entitlement matrix in internal/domain/billing.
    plan_code               TEXT        NOT NULL,
    status                  TEXT        NOT NULL,
    seats                   INTEGER     NOT NULL DEFAULT 1,

    current_period_start    TIMESTAMPTZ NOT NULL,
    current_period_end      TIMESTAMPTZ NOT NULL,
    cancel_at_period_end    BOOLEAN     NOT NULL DEFAULT FALSE,
    trial_end               TIMESTAMPTZ,

    -- Out-of-order guard, the same one project #1 applies against Stripe. The
    -- relay inherits the unordered delivery of the stream it forwards, so a
    -- redelivered older event must not overwrite newer state.
    last_event_id           TEXT,
    last_event_at           TIMESTAMPTZ,

    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT tenant_subscriptions_gateway_id_key UNIQUE (gateway_subscription_id),
    CONSTRAINT tenant_subscriptions_seats_chk      CHECK (seats > 0),
    CONSTRAINT tenant_subscriptions_period_chk     CHECK (current_period_end > current_period_start),
    CONSTRAINT tenant_subscriptions_status_chk     CHECK (status IN (
        'incomplete', 'incomplete_expired', 'trialing', 'active',
        'past_due', 'canceled', 'unpaid', 'paused'
    ))
);

CREATE TRIGGER tenant_subscriptions_touch_updated_at
    BEFORE UPDATE ON tenant_subscriptions
    FOR EACH ROW EXECUTE FUNCTION app.touch_updated_at();

-- Entitlement middleware reads this on every gated request, so it is a covering
-- index over exactly the columns the check needs.
CREATE INDEX tenant_subscriptions_live_idx
    ON tenant_subscriptions (tenant_id)
    INCLUDE (plan_code, seats, current_period_end)
    WHERE status IN ('trialing', 'active', 'past_due');

-- ---------------------------------------------------------------------------

CREATE TYPE billing_event_status AS ENUM ('processing', 'succeeded', 'failed', 'skipped');

-- Idempotency ledger for the relay. A system table: it holds deliveries whose
-- tenant is not yet known - the whole point of resolving them is to find out -
-- so it is never exposed through a tenant-scoped route and carries no RLS
-- policy. 00006 grants it to expense_app and nothing else.
--
-- The ordering rule that makes this safe is in the receiver, not here: the row
-- is claimed only after the HMAC verifies. Claiming first lets anyone POST a
-- guessed event_id, plant a settled row, and have the genuine delivery
-- discarded as a duplicate.
CREATE TABLE billing_events (
    event_id     TEXT                 PRIMARY KEY,
    tenant_id    UUID                 REFERENCES tenants (id) ON DELETE SET NULL,
    event_type   TEXT                 NOT NULL,
    status       billing_event_status NOT NULL DEFAULT 'processing',
    attempts     INTEGER              NOT NULL DEFAULT 1,

    payload      JSONB                NOT NULL,
    error_detail TEXT,

    received_at  TIMESTAMPTZ          NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ,

    CONSTRAINT billing_events_attempts_chk CHECK (attempts >= 1),
    CONSTRAINT billing_events_settled_chk
        CHECK ((status = 'processing') = (processed_at IS NULL))
);

-- Feeds the stuck-delivery sweeper: anything still 'processing' after the
-- handler's deadline was abandoned mid-flight and has to be reclaimed.
CREATE INDEX billing_events_processing_idx
    ON billing_events (received_at)
    WHERE status = 'processing';

CREATE INDEX billing_events_tenant_idx
    ON billing_events (tenant_id, received_at DESC)
    WHERE tenant_id IS NOT NULL;

-- +goose Down

DROP TABLE IF EXISTS billing_events;
DROP TYPE  IF EXISTS billing_event_status;
DROP TABLE IF EXISTS tenant_subscriptions;
DROP INDEX IF EXISTS expenses_recurring_once_per_charge_key;
ALTER TABLE expenses DROP CONSTRAINT IF EXISTS expenses_source_subscription_fk;
DROP TABLE IF EXISTS vendor_subscriptions;
DROP TYPE  IF EXISTS vendor_subscription_status;
DROP TYPE  IF EXISTS billing_cadence;
