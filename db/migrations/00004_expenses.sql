-- =============================================================================
-- 00004_expenses
--
-- The expense claim, its attachments, and the append-only ledger of every
-- state change made to it.
--
-- Two things are enforced here rather than in Go:
--
--   * expenses_status_timestamps_chk keeps the timestamp columns consistent
--     with the status. The state machine in internal/domain/expense is the
--     authority on which transitions exist; this is the authority on what a
--     row in each state must look like. A bug in the former shows up as a
--     constraint violation instead of a row that says `paid` with no paid_at.
--
--   * expense_events refuses UPDATE and DELETE outright. An audit trail that
--     can be edited answers the wrong question.
-- =============================================================================

-- +goose Up

-- Declaration order is not significant for this type - nothing compares
-- expense_status with an inequality - but it is written in lifecycle order so
-- \dT output reads like the state diagram.
CREATE TYPE expense_status AS ENUM (
    'draft',
    'pending_approval',
    'approved',
    'rejected',
    'paid'
);

CREATE TYPE expense_category AS ENUM (
    'travel',
    'meals',
    'accommodation',
    'software',
    'hardware',
    'marketing',
    'training',
    'office',
    'contractor',
    'other'
);

CREATE TABLE expenses (
    id            UUID           PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID           NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,

    -- Membership, not user: authority is a property of someone's place in this
    -- tenant, and the composite FK makes a claim filed by a member of another
    -- tenant unrepresentable rather than merely unreachable.
    submitter_id  UUID           NOT NULL,
    department_id UUID,

    status        expense_status NOT NULL DEFAULT 'draft',
    category      expense_category NOT NULL DEFAULT 'other',

    -- Minor units of `currency`. Never a float: 0.1 + 0.2 is a support ticket.
    amount_minor  BIGINT         NOT NULL,
    currency      CHAR(3)        NOT NULL,

    merchant      TEXT           NOT NULL,
    description   TEXT,

    -- When the money was spent, as distinct from when the claim was filed.
    -- Reporting periods key on this.
    spent_at      DATE           NOT NULL,

    submitted_at  TIMESTAMPTZ,
    decided_at    TIMESTAMPTZ,
    decided_by    UUID,
    decision_note TEXT,
    paid_at       TIMESTAMPTZ,
    payment_ref   TEXT,

    -- Which draft iteration this is. A rejected claim returns to draft and
    -- comes back as revision 2; the pair (id, revision) is what an approver is
    -- actually being asked about, and it is why "rejected -> pending" is not a
    -- transition. See internal/domain/expense/statemachine.go.
    revision      INTEGER        NOT NULL DEFAULT 1,

    -- Optimistic concurrency token, bumped by every state transition. The
    -- repository's compare-and-swap uses it so two approvers clicking at once
    -- produce one approval and one 409, not two audit rows.
    version       INTEGER        NOT NULL DEFAULT 1,

    -- Set when this claim was generated from a recurring vendor subscription
    -- (00005) rather than filed by a person.
    source_subscription_id UUID,

    created_at    TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ    NOT NULL DEFAULT now(),

    CONSTRAINT expenses_id_tenant_key UNIQUE (id, tenant_id),

    CONSTRAINT expenses_submitter_fk
        FOREIGN KEY (submitter_id, tenant_id)
        REFERENCES memberships (id, tenant_id) ON DELETE RESTRICT,
    CONSTRAINT expenses_decided_by_fk
        FOREIGN KEY (decided_by, tenant_id)
        REFERENCES memberships (id, tenant_id) ON DELETE RESTRICT,
    CONSTRAINT expenses_department_fk
        FOREIGN KEY (department_id, tenant_id)
        REFERENCES departments (id, tenant_id) ON DELETE RESTRICT,

    CONSTRAINT expenses_amount_chk   CHECK (amount_minor > 0),
    CONSTRAINT expenses_currency_chk CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT expenses_merchant_chk CHECK (char_length(btrim(merchant)) BETWEEN 1 AND 200),
    CONSTRAINT expenses_revision_chk CHECK (revision >= 1),
    CONSTRAINT expenses_version_chk  CHECK (version >= 1),

    -- A future-dated expense is either a typo or a claim for money not yet
    -- spent. One day of slack absorbs client clock skew across time zones.
    CONSTRAINT expenses_spent_at_chk CHECK (spent_at <= (now() AT TIME ZONE 'UTC')::date + 1),

    -- The row shape implied by each state. Written as a single CHECK so the
    -- error names the invariant rather than whichever column happened to be
    -- inspected first.
    CONSTRAINT expenses_status_timestamps_chk CHECK (
        CASE status
            WHEN 'draft' THEN
                submitted_at IS NULL AND decided_at IS NULL
                AND decided_by IS NULL AND paid_at IS NULL
            WHEN 'pending_approval' THEN
                submitted_at IS NOT NULL AND decided_at IS NULL
                AND decided_by IS NULL AND paid_at IS NULL
            WHEN 'approved' THEN
                submitted_at IS NOT NULL AND decided_at IS NOT NULL
                AND decided_by IS NOT NULL AND paid_at IS NULL
            WHEN 'rejected' THEN
                submitted_at IS NOT NULL AND decided_at IS NOT NULL
                AND decided_by IS NOT NULL AND paid_at IS NULL
            WHEN 'paid' THEN
                submitted_at IS NOT NULL AND decided_at IS NOT NULL
                AND decided_by IS NOT NULL AND paid_at IS NOT NULL
        END
    ),

    CONSTRAINT expenses_decision_order_chk
        CHECK (decided_at IS NULL OR submitted_at IS NULL OR decided_at >= submitted_at),
    CONSTRAINT expenses_payment_order_chk
        CHECK (paid_at IS NULL OR decided_at IS NULL OR paid_at >= decided_at)
);

-- The approver's queue: everything awaiting a decision in this tenant, oldest
-- first. Partial on the status so the index holds only the rows the queue can
-- ever return - a tenant with two years of paid claims and nine pending ones
-- has a nine-row index here.
CREATE INDEX expenses_pending_queue_idx
    ON expenses (tenant_id, department_id, submitted_at)
    WHERE status = 'pending_approval';

-- The list endpoint's default ordering, and the export's scan order. spent_at
-- descending with id as the tiebreak is exactly the keyset pagination cursor,
-- so `WHERE (spent_at, id) < ($1, $2)` walks this index backwards without a
-- sort. Dropping id from the index breaks that: duplicate spent_at values make
-- the cursor skip or repeat rows.
CREATE INDEX expenses_tenant_spent_at_idx
    ON expenses (tenant_id, spent_at DESC, id DESC);

CREATE INDEX expenses_tenant_submitter_idx
    ON expenses (tenant_id, submitter_id, spent_at DESC);

-- Budget consumption sums approved and paid claims per department per period.
-- INCLUDE carries the amount so the aggregate is answered index-only.
CREATE INDEX expenses_budget_rollup_idx
    ON expenses (tenant_id, department_id, spent_at)
    INCLUDE (amount_minor)
    WHERE status IN ('approved', 'paid');

CREATE TRIGGER expenses_touch_updated_at
    BEFORE UPDATE ON expenses
    FOR EACH ROW EXECUTE FUNCTION app.touch_updated_at();

-- ---------------------------------------------------------------------------

CREATE TABLE expense_attachments (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    expense_id   UUID        NOT NULL,

    -- Object storage key. The bytes never enter this database: receipts are
    -- large, immutable and served by signed URL, and putting them in a row
    -- makes every backup and every logical replication slot carry them.
    object_key   TEXT        NOT NULL,
    filename     TEXT        NOT NULL,
    content_type TEXT        NOT NULL,
    size_bytes   BIGINT      NOT NULL,

    -- SHA-256 of the object, for deduplication and for proving the receipt
    -- backing an approved claim has not been swapped since.
    checksum     BYTEA       NOT NULL,

    uploaded_by  UUID        NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT expense_attachments_expense_fk
        FOREIGN KEY (expense_id, tenant_id)
        REFERENCES expenses (id, tenant_id) ON DELETE CASCADE,
    CONSTRAINT expense_attachments_uploader_fk
        FOREIGN KEY (uploaded_by, tenant_id)
        REFERENCES memberships (id, tenant_id) ON DELETE RESTRICT,
    CONSTRAINT expense_attachments_object_key_key UNIQUE (object_key),
    CONSTRAINT expense_attachments_size_chk     CHECK (size_bytes BETWEEN 1 AND 26214400), -- 25 MiB
    CONSTRAINT expense_attachments_checksum_chk CHECK (octet_length(checksum) = 32)
);

CREATE INDEX expense_attachments_expense_idx
    ON expense_attachments (tenant_id, expense_id);

-- ---------------------------------------------------------------------------

CREATE TYPE expense_action AS ENUM (
    'created',
    'updated',
    'submitted',
    'approved',
    'rejected',
    'withdrawn',   -- submitter pulls a pending claim back to draft
    'revised',     -- rejected claim reopened as the next draft revision
    'paid'
);

-- Append-only. Every transition the state machine performs writes exactly one
-- row here inside the same transaction as the UPDATE it describes, so the
-- ledger cannot disagree with the claim.
CREATE TABLE expense_events (
    id          BIGSERIAL      PRIMARY KEY,
    tenant_id   UUID           NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    expense_id  UUID           NOT NULL,

    action      expense_action NOT NULL,
    from_status expense_status,               -- NULL for 'created'
    to_status   expense_status NOT NULL,

    -- The membership that acted. NULL only for actions taken by the system
    -- itself, such as a recurring charge materialising a draft.
    actor_id    UUID,

    reason      TEXT,

    -- Amount as of this event. Denormalised on purpose: an audit row that says
    -- "approved" without saying what was approved is useless once the claim is
    -- revised, and joining back to expenses would report today's amount.
    amount_minor BIGINT        NOT NULL,
    currency     CHAR(3)       NOT NULL,
    revision     INTEGER       NOT NULL,

    metadata    JSONB          NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ    NOT NULL DEFAULT now(),

    CONSTRAINT expense_events_expense_fk
        FOREIGN KEY (expense_id, tenant_id)
        REFERENCES expenses (id, tenant_id) ON DELETE CASCADE,
    CONSTRAINT expense_events_actor_fk
        FOREIGN KEY (actor_id, tenant_id)
        REFERENCES memberships (id, tenant_id) ON DELETE RESTRICT,
    CONSTRAINT expense_events_transition_chk
        CHECK (from_status IS DISTINCT FROM to_status)
);

CREATE INDEX expense_events_expense_idx
    ON expense_events (tenant_id, expense_id, occurred_at DESC, id DESC);

-- The compliance query: every decision an approver made in a window.
CREATE INDEX expense_events_actor_idx
    ON expense_events (tenant_id, actor_id, occurred_at DESC)
    WHERE action IN ('approved', 'rejected');

CREATE TRIGGER expense_events_append_only
    BEFORE UPDATE OR DELETE ON expense_events
    FOR EACH ROW EXECUTE FUNCTION app.refuse_mutation();

-- +goose Down

DROP TABLE IF EXISTS expense_events;
DROP TYPE  IF EXISTS expense_action;
DROP TABLE IF EXISTS expense_attachments;
DROP TABLE IF EXISTS expenses;
DROP TYPE  IF EXISTS expense_category;
DROP TYPE  IF EXISTS expense_status;
