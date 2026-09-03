-- =============================================================================
-- 00001_bootstrap
--
-- Schema-level plumbing that every later migration depends on: the `app`
-- schema, the two session accessors that Row-Level Security is written
-- against, and the runtime role that the API and the worker connect as.
--
-- Nothing here is tenant data. The one thing worth reading twice is
-- app.current_tenant_id(): every RLS policy in 00006 is a comparison against
-- its return value, so its failure mode is the failure mode of the whole
-- isolation model.
-- =============================================================================

-- +goose Up

CREATE SCHEMA IF NOT EXISTS app;

CREATE EXTENSION IF NOT EXISTS pgcrypto;  -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS citext;    -- case-insensitive emails and slugs

-- ---------------------------------------------------------------------------
-- Session accessors
-- ---------------------------------------------------------------------------

-- current_tenant_id returns the tenant bound to this transaction, or NULL.
--
-- The second argument to current_setting is missing_ok. Without it an unset
-- GUC raises 42704 and every query on a tenant table fails with an error that
-- reads like a bug in the query rather than a missing session binding. With it,
-- an unbound session yields NULL, and `tenant_id = NULL` is NULL, which is not
-- TRUE, which means the policy denies every row. Unbound therefore reads as
-- "no rows", never "all rows" - the direction a mistake has to fail in.
--
-- NULLIF(...,'') matters because set_config writes the empty string, not NULL,
-- when handed one: '' :: uuid would raise 22P02 instead of denying.
--
-- STABLE, not VOLATILE: the value cannot change inside a statement, and STABLE
-- is what lets the planner evaluate it once and push the resulting constant
-- into an index scan. Marking it VOLATILE turns every RLS-protected lookup
-- into a per-row function call.
CREATE FUNCTION app.current_tenant_id() RETURNS uuid
    LANGUAGE sql
    STABLE
    PARALLEL SAFE
    -- No SECURITY DEFINER: this reads a session variable and touches no table,
    -- so it needs no privileges beyond the caller's.
AS $$
    SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$;

COMMENT ON FUNCTION app.current_tenant_id() IS
    'Tenant bound to the current transaction by set_config(...,true). NULL when unbound, which every RLS policy treats as "deny".';

-- current_actor_id is the authenticated user behind the request. RLS does not
-- key on it - isolation is per tenant, not per user - but triggers use it to
-- stamp audit rows, which keeps "who did this" out of reach of the caller
-- supplying it in a payload.
CREATE FUNCTION app.current_actor_id() RETURNS uuid
    LANGUAGE sql
    STABLE
    PARALLEL SAFE
AS $$
    SELECT NULLIF(current_setting('app.actor_id', true), '')::uuid
$$;

-- is_system opens a deliberate, narrow hole in the tenancy model.
--
-- Two paths legitimately write rows for a tenant without a tenant-scoped HTTP
-- request behind them: the billing relay receiver, which learns the tenant
-- from a signed payload, and worker jobs that sweep across tenants. Both are
-- machine paths with no user context to derive a tenant from.
--
-- The flag is set only by WithSystemTx in internal/platform/postgres, is
-- transaction-local like every other binding here, and widens the policies in
-- 00006 rather than replacing them - a system transaction still cannot reach a
-- table that has no system clause. Grep for WithSystemTx to enumerate every
-- caller; if that list ever grows past the billing and worker entry points,
-- the model has been eroded.
CREATE FUNCTION app.is_system() RETURNS boolean
    LANGUAGE sql
    STABLE
    PARALLEL SAFE
AS $$
    SELECT COALESCE(current_setting('app.system', true), '') = 'on'
$$;

-- ---------------------------------------------------------------------------
-- Shared triggers
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION app.touch_updated_at() RETURNS trigger
    LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Append-only enforcement for audit tables.
--
-- RLS can withhold UPDATE and DELETE from the application role, but a future
-- migration adding a permissive policy would slip past that. This refuses at
-- the table, so the guarantee holds even if the policies are edited.
--
-- The two operations are treated differently, and the asymmetry is deliberate:
--
--   * UPDATE is refused for everyone, including the owner and including a
--     superuser session. An audit row that can be edited answers the wrong
--     question, and there is no legitimate reason to change one.
--
--   * DELETE is refused for everyone except the table's owner. Erasure has to
--     remain possible: closing an account cascades from `tenants` through
--     every child table, and a retention policy has to be able to purge old
--     rows. Without this exception a tenant could never be deleted at all -
--     the cascade would hit this trigger and abort.
--
-- The application connects as expense_app, which is not the owner, so nothing
-- reachable from an HTTP request can delete an audit row. It also holds no
-- DELETE grant and matches no DELETE policy: three independent mechanisms,
-- and the one relaxed here is the one that has to bend for erasure.
-- +goose StatementBegin
CREATE FUNCTION app.refuse_mutation() RETURNS trigger
    LANGUAGE plpgsql
AS $$
DECLARE
    owner name;
BEGIN
    IF TG_OP = 'DELETE' THEN
        SELECT tableowner INTO owner
          FROM pg_tables
         WHERE schemaname = TG_TABLE_SCHEMA AND tablename = TG_TABLE_NAME;

        -- pg_has_role rather than an equality test, so a DBA acting through a
        -- role granted the owner role is also permitted.
        IF owner IS NOT NULL AND pg_has_role(current_user, owner, 'USAGE') THEN
            RETURN OLD;
        END IF;
    END IF;

    RAISE EXCEPTION 'table %.% is append-only', TG_TABLE_SCHEMA, TG_TABLE_NAME
        USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

-- ---------------------------------------------------------------------------
-- Runtime role
-- ---------------------------------------------------------------------------

-- The API and worker connect as expense_app, which is NOT the owner of any
-- table here. That separation is the point: PostgreSQL exempts a table's owner
-- from its policies unless FORCE ROW LEVEL SECURITY is set, and exempts
-- superusers and BYPASSRLS roles unconditionally. An application connecting as
-- the migration owner has RLS enabled and no isolation.
--
-- 00006 sets FORCE on every tenant table as well, so the model holds even if a
-- future deploy gets the connection string wrong.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'expense_app') THEN
        -- NOLOGIN here; the deploy grants LOGIN and a password out of band so
        -- no credential is ever committed to a migration.
        CREATE ROLE expense_app NOLOGIN NOBYPASSRLS;
    END IF;
END;
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA app, public TO expense_app;
GRANT EXECUTE ON FUNCTION app.current_tenant_id(), app.current_actor_id(), app.is_system() TO expense_app;

-- Tables created by later migrations are granted explicitly in 00006. Default
-- privileges are deliberately not used: a table that nobody remembered to
-- grant should be unreadable, not silently readable.

-- +goose Down

DROP FUNCTION IF EXISTS app.refuse_mutation();
DROP FUNCTION IF EXISTS app.touch_updated_at();
DROP FUNCTION IF EXISTS app.is_system();
DROP FUNCTION IF EXISTS app.current_actor_id();
DROP FUNCTION IF EXISTS app.current_tenant_id();
DROP SCHEMA IF EXISTS app;
