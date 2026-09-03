-- =============================================================================
-- 00006_row_level_security
--
-- Tenant isolation, enforced by PostgreSQL rather than by every WHERE clause
-- being written correctly forever.
--
-- The shape of the model, and why it is this shape:
--
-- 1. Isolation is a RESTRICTIVE policy, not a permissive one. Permissive
--    policies are OR-ed together, so a future migration adding one for a new
--    feature would *widen* access, and the widening would look like an
--    ordinary feature commit in review. Restrictive policies are AND-ed with
--    everything else, so `tenant_id = app.current_tenant_id()` cannot be
--    relaxed by adding policies - only by deleting this one, which is a change
--    nobody merges by accident.
--
-- 2. Access itself is a separate permissive policy per table. It is where
--    business rules live (drafts are deletable, audit rows are not), and it is
--    always evaluated under the restrictive one.
--
-- 3. FORCE ROW LEVEL SECURITY is set everywhere. Without it PostgreSQL exempts
--    the table owner, and an application that is accidentally deployed with
--    the migration credentials has RLS switched on and no isolation - the
--    worst configuration, because `\d` shows the policies and they are inert.
--
--    Consequence for operations: because the policies below are granted TO
--    expense_app, the owner matches no policy and is denied DML on these
--    tables too. A backfill migration must either run as a BYPASSRLS role or
--    wrap itself in ALTER TABLE ... NO FORCE ROW LEVEL SECURITY / ... FORCE.
--    That friction is intentional; it is one line in a migration and it keeps
--    the guarantee unconditional the rest of the time.
--
-- 4. `OR app.is_system()` appears in every isolation clause. It is the only
--    widening in the model and it is inert unless a transaction opts in via
--    WithSystemTx. See 00001 for the argument.
--
-- What RLS does NOT cover, stated so nobody assumes otherwise:
--
--   * users and refresh_tokens are global. Login has to find a user by email
--     before any tenant is known, so there is no tenant to filter on. Nothing
--     returns a user row to a client except through a join on memberships,
--     which is protected.
--   * Foreign key checks run internally with policies bypassed. That is why
--     every child table references a composite (id, tenant_id) key rather
--     than a bare id: the tenant match is then part of the constraint itself.
--   * A non-LEAKPROOF operator in a query can be evaluated before the policy
--     and leak the existence of a filtered row through an error message. The
--     API accepts no user-supplied SQL fragments, which is what closes that.
-- =============================================================================

-- +goose Up

-- ---------------------------------------------------------------------------
-- Enable, force, and apply the restrictive isolation policy
-- ---------------------------------------------------------------------------

-- Applied in a loop rather than transcribed nine times. The list is the
-- authoritative set of tenant-scoped tables; a table absent from it and absent
-- from the "deliberately global" list below is a bug.
-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'memberships',
        'departments',
        'budgets',
        'expenses',
        'expense_attachments',
        'expense_events',
        'vendor_subscriptions',
        'tenant_subscriptions'
    ]
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE  ROW LEVEL SECURITY', t);

        EXECUTE format($p$
            CREATE POLICY tenant_isolation ON %I
                AS RESTRICTIVE
                FOR ALL
                TO expense_app
                USING      (tenant_id = app.current_tenant_id() OR app.is_system())
                WITH CHECK (tenant_id = app.current_tenant_id() OR app.is_system())
        $p$, t);
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- tenants is the one tenant-scoped table whose key column is `id`, so it gets
-- the same treatment written out.
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON tenants
    AS RESTRICTIVE
    FOR ALL
    TO expense_app
    USING      (id = app.current_tenant_id() OR app.is_system())
    WITH CHECK (id = app.current_tenant_id() OR app.is_system());

-- ---------------------------------------------------------------------------
-- Permissive access policies
--
-- Each one is evaluated AND-ed with tenant_isolation above, so none of them
-- needs to repeat the tenant predicate - and none of them can subvert it.
-- ---------------------------------------------------------------------------

-- A tenant reads and renames itself. Creation and deletion are lifecycle
-- operations that run in a system transaction, because at creation time there
-- is no tenant to be bound to yet.
CREATE POLICY tenant_self_service ON tenants
    FOR SELECT TO expense_app USING (true);
CREATE POLICY tenant_self_update ON tenants
    FOR UPDATE TO expense_app USING (true) WITH CHECK (true);
CREATE POLICY tenant_provisioning ON tenants
    FOR INSERT TO expense_app WITH CHECK (app.is_system());

-- Membership, department, budget and vendor subscription management is
-- ordinary CRUD once isolation holds; who may perform it is an RBAC question,
-- answered in Go where the answer can depend on the actor's role and the
-- transition being attempted. Encoding role checks here would mean a second,
-- silently diverging copy of the permission matrix.
CREATE POLICY memberships_rw ON memberships
    FOR ALL TO expense_app USING (true) WITH CHECK (true);

CREATE POLICY departments_rw ON departments
    FOR ALL TO expense_app USING (true) WITH CHECK (true);

CREATE POLICY budgets_rw ON budgets
    FOR ALL TO expense_app USING (true) WITH CHECK (true);

CREATE POLICY vendor_subscriptions_rw ON vendor_subscriptions
    FOR ALL TO expense_app USING (true) WITH CHECK (true);

-- Expenses: read, create and update freely; delete only while the claim is
-- still a draft.
--
-- This is the one place a business rule is stated in a policy rather than in
-- the service layer, and it earns its place: "a submitted claim is a record"
-- is an auditor's requirement, not a product decision, and the cost of getting
-- it wrong is destroyed evidence. The state machine refuses the same thing one
-- layer up; this is what holds if a future endpoint forgets to ask it.
CREATE POLICY expenses_read ON expenses
    FOR SELECT TO expense_app USING (true);
CREATE POLICY expenses_insert ON expenses
    FOR INSERT TO expense_app WITH CHECK (true);
CREATE POLICY expenses_update ON expenses
    FOR UPDATE TO expense_app USING (true) WITH CHECK (true);
CREATE POLICY expenses_delete_drafts_only ON expenses
    FOR DELETE TO expense_app USING (status = 'draft');

CREATE POLICY expense_attachments_read ON expense_attachments
    FOR SELECT TO expense_app USING (true);
CREATE POLICY expense_attachments_insert ON expense_attachments
    FOR INSERT TO expense_app WITH CHECK (true);
-- Attachments follow their claim: removable while it is a draft, evidence
-- afterwards. The subquery is evaluated under the caller's policies, so it can
-- only see expenses in the same tenant.
CREATE POLICY expense_attachments_delete_drafts_only ON expense_attachments
    FOR DELETE TO expense_app
    USING (EXISTS (
        SELECT 1 FROM expenses e
        WHERE e.id = expense_attachments.expense_id
          AND e.status = 'draft'
    ));

-- The audit ledger. SELECT and INSERT have policies; UPDATE and DELETE have
-- none, and a command with no permissive policy is denied. The BEFORE trigger
-- from 00004 says the same thing again for any role that is not expense_app.
CREATE POLICY expense_events_read ON expense_events
    FOR SELECT TO expense_app USING (true);
CREATE POLICY expense_events_append ON expense_events
    FOR INSERT TO expense_app WITH CHECK (true);

-- Our own subscription state is readable by the tenant and writable only by
-- the relay, which runs as a system transaction. A tenant that could write
-- this row could grant itself the enterprise plan.
CREATE POLICY tenant_subscriptions_read ON tenant_subscriptions
    FOR SELECT TO expense_app USING (true);
CREATE POLICY tenant_subscriptions_system_write ON tenant_subscriptions
    FOR ALL TO expense_app
    USING (app.is_system()) WITH CHECK (app.is_system());

-- ---------------------------------------------------------------------------
-- Privileges
--
-- RLS filters rows; it grants nothing. A table the role has no GRANT on is
-- unreadable regardless of policy, and a table with a GRANT and no RLS is
-- readable across every tenant. Both halves are needed and they are listed
-- separately so neither can be assumed from the other.
-- ---------------------------------------------------------------------------

REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC;

GRANT SELECT, INSERT, UPDATE, DELETE ON
    memberships, departments, budgets, expenses, expense_attachments,
    vendor_subscriptions
TO expense_app;

GRANT SELECT, UPDATE ON tenants TO expense_app;
GRANT INSERT ON tenants TO expense_app;   -- gated to system transactions by policy

-- Append-only at the privilege level as well as the policy level. Two
-- independent mechanisms, because this is the table that proves what happened.
GRANT SELECT, INSERT ON expense_events TO expense_app;
GRANT USAGE, SELECT ON SEQUENCE expense_events_id_seq TO expense_app;

GRANT SELECT, INSERT, UPDATE ON tenant_subscriptions TO expense_app;

-- Global tables: no RLS, ordinary grants. Isolation for these is the service
-- layer's responsibility and is documented at the top of this file.
GRANT SELECT, INSERT, UPDATE ON users TO expense_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON refresh_tokens TO expense_app;
GRANT SELECT, INSERT, UPDATE ON billing_events TO expense_app;

-- +goose Down

DROP POLICY IF EXISTS tenant_subscriptions_system_write ON tenant_subscriptions;
DROP POLICY IF EXISTS tenant_subscriptions_read ON tenant_subscriptions;
DROP POLICY IF EXISTS expense_events_append ON expense_events;
DROP POLICY IF EXISTS expense_events_read ON expense_events;
DROP POLICY IF EXISTS expense_attachments_delete_drafts_only ON expense_attachments;
DROP POLICY IF EXISTS expense_attachments_insert ON expense_attachments;
DROP POLICY IF EXISTS expense_attachments_read ON expense_attachments;
DROP POLICY IF EXISTS expenses_delete_drafts_only ON expenses;
DROP POLICY IF EXISTS expenses_update ON expenses;
DROP POLICY IF EXISTS expenses_insert ON expenses;
DROP POLICY IF EXISTS expenses_read ON expenses;
DROP POLICY IF EXISTS vendor_subscriptions_rw ON vendor_subscriptions;
DROP POLICY IF EXISTS budgets_rw ON budgets;
DROP POLICY IF EXISTS departments_rw ON departments;
DROP POLICY IF EXISTS memberships_rw ON memberships;
DROP POLICY IF EXISTS tenant_provisioning ON tenants;
DROP POLICY IF EXISTS tenant_self_update ON tenants;
DROP POLICY IF EXISTS tenant_self_service ON tenants;
DROP POLICY IF EXISTS tenant_isolation ON tenants;

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'memberships', 'departments', 'budgets', 'expenses',
        'expense_attachments', 'expense_events', 'vendor_subscriptions',
        'tenant_subscriptions'
    ]
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
        EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
    END LOOP;
END;
$$;
-- +goose StatementEnd

ALTER TABLE tenants NO FORCE ROW LEVEL SECURITY;
ALTER TABLE tenants DISABLE ROW LEVEL SECURITY;
