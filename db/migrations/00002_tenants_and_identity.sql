-- =============================================================================
-- 00002_tenants_and_identity
--
-- Tenants, the global user identity, and the membership that joins them.
--
-- Users are global, not tenant-scoped: a consultant working for three agencies
-- has one login and three memberships. That choice is what makes `memberships`
-- - not `users` - the table the authorisation model reads on every request.
-- =============================================================================

-- +goose Up

CREATE TYPE tenant_status AS ENUM ('active', 'suspended', 'cancelled');

CREATE TABLE tenants (
    id                  UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    slug                CITEXT        NOT NULL,
    name                TEXT          NOT NULL,
    status              tenant_status NOT NULL DEFAULT 'active',

    -- ISO 4217, uppercase. Every expense in a tenant is compared against
    -- budgets in this currency; multi-currency claims are converted at capture
    -- time rather than stored mixed, because summing mixed currencies in SQL
    -- silently produces a meaningless number.
    default_currency    CHAR(3)       NOT NULL DEFAULT 'USD',

    -- Identifier of this tenant in the Stripe Payment & Subscription Gateway
    -- (project #1). NULL until the tenant starts a checkout. It is that
    -- service's user id, not a Stripe customer id: the gateway owns the
    -- mapping to Stripe and this service never learns cus_*.
    billing_customer_ref TEXT,

    created_at          TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ   NOT NULL DEFAULT now(),

    CONSTRAINT tenants_slug_key UNIQUE (slug),
    CONSTRAINT tenants_slug_format_chk
        CHECK (slug ~ '^[a-z0-9](?:[a-z0-9-]{1,38}[a-z0-9])$'),
    CONSTRAINT tenants_name_len_chk
        CHECK (char_length(btrim(name)) BETWEEN 1 AND 200),
    CONSTRAINT tenants_currency_chk
        CHECK (default_currency ~ '^[A-Z]{3}$')
);

CREATE TRIGGER tenants_touch_updated_at
    BEFORE UPDATE ON tenants
    FOR EACH ROW EXECUTE FUNCTION app.touch_updated_at();

-- Partial unique index rather than a plain one on billing_customer_ref: the
-- column is NULL for every tenant that has not reached checkout, and a plain
-- unique index would index all of those NULLs for nothing.
CREATE UNIQUE INDEX tenants_billing_customer_ref_key
    ON tenants (billing_customer_ref)
    WHERE billing_customer_ref IS NOT NULL;

-- ---------------------------------------------------------------------------

CREATE TABLE users (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    email         CITEXT      NOT NULL,

    -- NULL for a user created by invitation who has not yet set a password.
    -- Authentication treats NULL as "no password credential", which is not the
    -- same as "wrong password" and must not be reported differently.
    password_hash TEXT,

    full_name     TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ,

    CONSTRAINT users_email_format_chk
        CHECK (email ~ '^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$'),
    CONSTRAINT users_email_len_chk
        CHECK (char_length(email::text) BETWEEN 3 AND 320)
);

-- Soft delete frees the address for re-registration, so uniqueness applies to
-- live rows only.
CREATE UNIQUE INDEX users_email_live_key
    ON users (email)
    WHERE deleted_at IS NULL;

CREATE TRIGGER users_touch_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION app.touch_updated_at();

-- ---------------------------------------------------------------------------

-- Ordered from most to least privileged. The ordering is load-bearing:
-- membership_role is compared with >= in the RBAC layer, and PostgreSQL orders
-- enum values by their declaration position, so inserting a value in the middle
-- later requires ALTER TYPE ... ADD VALUE ... BEFORE and a matching change in
-- internal/domain/tenant. Appending to the end is always wrong for this type.
CREATE TYPE membership_role AS ENUM (
    'owner',    -- billing, tenant lifecycle, everything below
    'admin',    -- full operational control, no billing
    'finance',  -- settles approved claims, exports, sees every department
    'manager',  -- approves within their department and their approval limit
    'member',   -- files and submits their own claims
    'viewer'    -- read-only
);

CREATE TYPE membership_status AS ENUM ('invited', 'active', 'suspended');

CREATE TABLE memberships (
    id                  UUID              PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID              NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    user_id             UUID              NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    role                membership_role   NOT NULL DEFAULT 'member',
    status              membership_status NOT NULL DEFAULT 'invited',

    -- Per-member ceiling on what this manager may approve, in minor units.
    -- NULL means "the role's default", resolved in Go rather than here so the
    -- default can change without a data migration.
    approval_limit_minor BIGINT,

    -- Set for managers whose authority is scoped to one department. NULL means
    -- tenant-wide. The FK is added in 00003, after departments exists.
    department_id       UUID,

    invited_by          UUID              REFERENCES users (id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ       NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ       NOT NULL DEFAULT now(),

    CONSTRAINT memberships_tenant_user_key UNIQUE (tenant_id, user_id),
    CONSTRAINT memberships_approval_limit_chk
        CHECK (approval_limit_minor IS NULL OR approval_limit_minor > 0),

    -- The composite the child tables' foreign keys point at. It makes
    -- "this membership belongs to this tenant" checkable by the database
    -- instead of by the query that happens to be running.
    CONSTRAINT memberships_id_tenant_key UNIQUE (id, tenant_id)
);

-- Leading column is tenant_id on every index over a tenant table. RLS appends
-- `tenant_id = app.current_tenant_id()` to each query as an implicit
-- predicate, so an index that does not start with tenant_id cannot satisfy it
-- and the planner falls back to a filter after the fetch.
CREATE INDEX memberships_tenant_role_idx ON memberships (tenant_id, role);
CREATE INDEX memberships_user_idx        ON memberships (user_id) WHERE status = 'active';

CREATE TRIGGER memberships_touch_updated_at
    BEFORE UPDATE ON memberships
    FOR EACH ROW EXECUTE FUNCTION app.touch_updated_at();

-- Exactly one owner per tenant. A partial unique index is the cheapest way to
-- say it, and it makes "transfer ownership" a single UPDATE inside a
-- transaction that has to demote the incumbent first - which is the behaviour
-- we want, rather than two owners existing for a moment.
CREATE UNIQUE INDEX memberships_single_owner_key
    ON memberships (tenant_id)
    WHERE role = 'owner';

-- ---------------------------------------------------------------------------

-- Refresh tokens are per user and cross-tenant, so this table is deliberately
-- outside the tenancy model: a session is not scoped to a tenant, the access
-- token it mints is.
CREATE TABLE refresh_tokens (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    -- SHA-256 of the presented token. Storing the token itself would make a
    -- database read equivalent to a stolen session for every live user.
    token_hash  BYTEA       NOT NULL,

    -- Rotation family. A reused token revokes the whole family, which is the
    -- only signal available that a token was copied rather than rotated.
    family_id   UUID        NOT NULL,

    issued_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    revoked_at  TIMESTAMPTZ,

    user_agent  TEXT,
    client_ip   INET,

    CONSTRAINT refresh_tokens_token_hash_key UNIQUE (token_hash),
    CONSTRAINT refresh_tokens_hash_len_chk   CHECK (octet_length(token_hash) = 32),
    CONSTRAINT refresh_tokens_expiry_chk     CHECK (expires_at > issued_at)
);

CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family_id);
CREATE INDEX refresh_tokens_sweep_idx  ON refresh_tokens (expires_at) WHERE revoked_at IS NULL;

-- +goose Down

DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS memberships;
DROP TYPE  IF EXISTS membership_status;
DROP TYPE  IF EXISTS membership_role;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS tenants;
DROP TYPE  IF EXISTS tenant_status;
