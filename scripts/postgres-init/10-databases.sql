-- Runs once, on first initialisation of the data directory.
--
-- Creates the integration-test database alongside the development one, and
-- gives the runtime role its password. In a deployed environment this file has
-- no equivalent: the role is created by 00001 and the password is set by the
-- platform's secret manager.
CREATE DATABASE expenses_test OWNER expense;

-- Matches the NOLOGIN role that migration 00001 creates. Doing it here means a
-- fresh container is usable without a manual step; doing it in the migration
-- would commit a password.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'expense_app') THEN
        CREATE ROLE expense_app LOGIN PASSWORD 'local_app_pw' NOBYPASSRLS;
    ELSE
        ALTER ROLE expense_app LOGIN PASSWORD 'local_app_pw' NOBYPASSRLS;
    END IF;
END;
$$;
