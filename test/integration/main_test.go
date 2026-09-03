//go:build integration

// Package integration exercises the persistence layer against a real
// PostgreSQL 16 with row-level security enabled.
//
// These tests assert behaviour that cannot be observed without a database and
// cannot be faked convincingly: policy enforcement, constraint translation,
// and the two concurrency guarantees the service depends on - the row lock
// around a decision and the compare-and-swap behind it.
//
// The container is created once for the whole package. Two connections are
// opened to it, and the difference between them is the point of the suite:
//
//   - `owner` is the superuser the container starts with. Migrations run as
//     it, and so does test fixture setup, because a superuser bypasses RLS
//     unconditionally and can therefore write rows for any tenant.
//   - `app` connects as expense_app: NOLOGIN-derived, NOBYPASSRLS, granted
//     only DML. It is what the service uses in production and it is what
//     every assertion about isolation is made through.
//
// A suite that ran everything as the owner would pass with every policy
// deleted.
//
//	make test-integration
package integration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	// Registers the "pgx" driver for database/sql, which goose and the
	// fixture helpers use. The application itself never goes through
	// database/sql - it uses pgx natively.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/mlkad/b2b-expense-tracker/internal/logger"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
)

const (
	ownerUser     = "expense"
	ownerPassword = "test_owner_pw"
	appUser       = "expense_app"
	appPassword   = "test_app_pw"
	databaseName  = "expenses_test"
)

var (
	// app is the handle every isolation assertion goes through: the runtime
	// role, subject to every policy.
	app *postgres.DB

	// ownerDSN is used for fixtures and for the assertions that need to see
	// past RLS to prove something was or was not written.
	ownerDSN string
)

func TestMain(m *testing.M) {
	code, err := runSuite(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runSuite(m *testing.M) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, "postgres:16.4-alpine",
		tcpostgres.WithDatabase(databaseName),
		tcpostgres.WithUsername(ownerUser),
		tcpostgres.WithPassword(ownerPassword),
		testcontainers.WithWaitStrategy(
			// Two occurrences: the entrypoint starts the server once to run
			// initialisation and again for real. Waiting for the first would
			// race the restart and produce connection refused.
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		return 1, fmt.Errorf("start postgres container: %w", err)
	}
	defer func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintf(os.Stderr, "integration: terminate container: %v\n", err)
		}
	}()

	ownerDSN, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 1, fmt.Errorf("owner dsn: %w", err)
	}

	if err := migrate(ownerDSN); err != nil {
		return 1, err
	}
	if err := grantAppLogin(ctx, ownerDSN); err != nil {
		return 1, err
	}

	appDSN, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return 1, fmt.Errorf("app dsn: %w", err)
	}
	appDSN = swapCredentials(appDSN, appUser, appPassword)

	log := logger.New(logger.ParseLevel("error"), logger.FormatText, "integration", "test")
	app, err = postgres.Open(ctx, postgres.Config{
		DSN: appDSN,
		// A deliberately small pool. Several of these tests run concurrent
		// transactions for different tenants, and a pool of four guarantees
		// connections are reused between them - which is the only way to
		// observe a binding leaking from one tenant's transaction into the
		// next tenant's.
		MaxConns: 4,
		MinConns: 1,
		// On for the whole suite. It costs a round trip per transaction and it
		// asserts, every single time, the property the entire tenancy model
		// rests on.
		VerifyTenantResetOnCommit: true,
		ApplicationName:           "integration-test",
	}, log)
	if err != nil {
		return 1, fmt.Errorf("open app pool: %w", err)
	}
	defer app.Close()

	return m.Run(), nil
}

// migrate applies the goose migrations as the owner.
//
// The real migrations, not a hand-written test schema. A test schema is a
// second definition of the tables that drifts from the first, and the things
// this suite checks - policies, constraint names, index predicates - are
// exactly the things that would drift.
func migrate(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open for migrations: %w", err)
	}
	defer db.Close()

	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	if err := goose.Up(db, "../../db/migrations"); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// grantAppLogin gives the role created by migration 00001 a password.
//
// The migration creates it NOLOGIN and sets no password on purpose - a
// credential in a migration is a credential in version control. In a
// deployment the platform's secret manager does this; here the test does.
func grantAppLogin(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	_, err = db.ExecContext(ctx,
		fmt.Sprintf("ALTER ROLE %s LOGIN PASSWORD '%s' NOBYPASSRLS", appUser, appPassword))
	if err != nil {
		return fmt.Errorf("grant app login: %w", err)
	}
	return nil
}

func swapCredentials(dsn, user, password string) string {
	return replaceOnce(dsn, ownerUser+":"+ownerPassword, user+":"+password)
}

func replaceOnce(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}
