-- =============================================================================
-- 00003_departments_and_budgets
--
-- The organisational spine: departments in a hierarchy, and the budget
-- envelopes attached to them for a period.
--
-- The interesting constraint here is budgets_no_overlap. Two budgets covering
-- the same department and overlapping dates make "how much is left this
-- quarter" ambiguous, and every downstream number - the dashboard, the
-- threshold alerts, the approval guard - inherits that ambiguity. An exclusion
-- constraint removes the question rather than answering it in application code.
-- =============================================================================

-- +goose Up

-- Needed for `tenant_id WITH =` inside a GiST exclusion constraint; the default
-- GiST opclass set has no equality operator for uuid.
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE departments (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name       TEXT        NOT NULL,

    -- Self-reference for the org tree. The composite FK carries tenant_id so a
    -- department cannot be parented to one in another tenant - a check that
    -- RLS alone would not make, because foreign key validation runs as the
    -- table owner with policies bypassed.
    parent_id  UUID,

    -- Denormalised head of department. Approval routing reads it; it is not a
    -- membership FK because the head can be replaced while their membership
    -- stays, and vice versa.
    head_user_id UUID      REFERENCES users (id) ON DELETE SET NULL,

    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT departments_id_tenant_key UNIQUE (id, tenant_id),
    CONSTRAINT departments_parent_fk
        FOREIGN KEY (parent_id, tenant_id)
        REFERENCES departments (id, tenant_id) ON DELETE RESTRICT,
    CONSTRAINT departments_not_own_parent_chk CHECK (parent_id IS DISTINCT FROM id),
    CONSTRAINT departments_name_len_chk
        CHECK (char_length(btrim(name)) BETWEEN 1 AND 120)
);

CREATE UNIQUE INDEX departments_tenant_name_live_key
    ON departments (tenant_id, lower(name))
    WHERE archived_at IS NULL;

CREATE INDEX departments_tenant_parent_idx ON departments (tenant_id, parent_id);

CREATE TRIGGER departments_touch_updated_at
    BEFORE UPDATE ON departments
    FOR EACH ROW EXECUTE FUNCTION app.touch_updated_at();

-- Deferred from 00002, which could not reference a table that did not exist
-- yet. Composite again: a manager scoped to a department in another tenant is
-- the exact privilege escalation this forecloses.
ALTER TABLE memberships
    ADD CONSTRAINT memberships_department_fk
    FOREIGN KEY (department_id, tenant_id)
    REFERENCES departments (id, tenant_id) ON DELETE SET NULL;

CREATE INDEX memberships_tenant_department_idx
    ON memberships (tenant_id, department_id)
    WHERE department_id IS NOT NULL;

-- ---------------------------------------------------------------------------

CREATE TABLE budgets (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,

    -- NULL is a tenant-wide envelope. See budgets_no_overlap for how that
    -- interacts with the exclusion constraint.
    department_id UUID,

    -- Inclusive on both ends. Stored as DATE, not TIMESTAMPTZ: a fiscal
    -- quarter is a calendar fact, and giving it a time zone means the same
    -- quarter starts at different instants for different readers.
    period_start  DATE        NOT NULL,
    period_end    DATE        NOT NULL,

    amount_minor  BIGINT      NOT NULL,
    currency      CHAR(3)     NOT NULL,

    -- Fraction of the envelope at which the alerting worker raises a warning.
    -- Per budget, because a marketing spend budget and a payroll budget do not
    -- want the same threshold.
    alert_threshold_bps INTEGER NOT NULL DEFAULT 8000,   -- basis points: 8000 = 80%

    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT budgets_amount_chk    CHECK (amount_minor > 0),
    CONSTRAINT budgets_currency_chk  CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT budgets_period_chk    CHECK (period_end >= period_start),
    CONSTRAINT budgets_threshold_chk CHECK (alert_threshold_bps BETWEEN 1 AND 10000),
    CONSTRAINT budgets_department_fk
        FOREIGN KEY (department_id, tenant_id)
        REFERENCES departments (id, tenant_id) ON DELETE CASCADE,

    -- One envelope per department per stretch of time.
    --
    -- COALESCE collapses the tenant-wide budget (department_id IS NULL) onto a
    -- sentinel, because NULL <> NULL under `WITH =` and two overlapping
    -- tenant-wide budgets would otherwise both be accepted - the one case an
    -- ordinary unique index also misses.
    CONSTRAINT budgets_no_overlap EXCLUDE USING gist (
        tenant_id WITH =,
        (COALESCE(department_id, '00000000-0000-0000-0000-000000000000'::uuid)) WITH =,
        daterange(period_start, period_end, '[]') WITH &&
    )
);

-- Covers the dashboard's "budgets in effect on date D" lookup. The exclusion
-- constraint's GiST index could serve it, but only as a range scan over the
-- whole tenant; this one answers it from the index alone.
CREATE INDEX budgets_tenant_period_idx
    ON budgets (tenant_id, period_start DESC, period_end DESC);

CREATE TRIGGER budgets_touch_updated_at
    BEFORE UPDATE ON budgets
    FOR EACH ROW EXECUTE FUNCTION app.touch_updated_at();

-- +goose Down

DROP TABLE IF EXISTS budgets;
ALTER TABLE memberships DROP CONSTRAINT IF EXISTS memberships_department_fk;
DROP TABLE IF EXISTS departments;
