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
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	// Registers the "pgx" driver for database/sql, which goose and the
	// fixture helpers use. The application itself never goes through
	// database/sql - it uses pgx natively.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/mlkad/b2b-expense-tracker/internal/logger"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/storage"
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

	// objectStore points at a real MinIO. The presigned-URL signing is written
	// by hand rather than taken from the AWS SDK, so it is verified against a
	// server that actually checks signatures - a unit test asserting the
	// signature equals what this code produces would prove nothing at all.
	objectStore storage.Store
)

func TestMain(m *testing.M) {
	code, err := runSuite(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration: %v\n", err)
		os.Exit(1)
	}
	os.Exit(code)
}

// startObjectStore brings up MinIO and creates the bucket.
func startObjectStore(ctx context.Context) error {
	const (
		accessKey = "integration-key"
		secretKey = "integration-secret"
		bucket    = "receipts"
	)

	container, err := tcminio.Run(ctx, "minio/minio:RELEASE.2024-08-17T01-24-54Z",
		tcminio.WithUsername(accessKey),
		tcminio.WithPassword(secretKey),
	)
	if err != nil {
		return fmt.Errorf("start minio: %w", err)
	}
	cleanups = append(cleanups, func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintf(os.Stderr, "integration: terminate minio: %v\n", err)
		}
	})

	endpoint, err := container.ConnectionString(ctx)
	if err != nil {
		return fmt.Errorf("minio endpoint: %w", err)
	}

	store, err := storage.NewS3Store(storage.S3Config{
		Endpoint:  "http://" + endpoint,
		Region:    "us-east-1",
		Bucket:    bucket,
		AccessKey: accessKey,
		SecretKey: secretKey,
		PathStyle: true,
	})
	if err != nil {
		return fmt.Errorf("build store: %w", err)
	}

	// The bucket is created with the same signing path everything else uses,
	// which means a broken signature fails here rather than in the first test.
	if err := createBucket(ctx, "http://"+endpoint, bucket, accessKey, secretKey); err != nil {
		return err
	}

	objectStore = store
	return nil
}

// createBucket issues a signed PUT against the bucket itself.
func createBucket(ctx context.Context, endpoint, bucket, accessKey, secretKey string) error {
	// A store whose "bucket" is empty and whose "key" is the bucket name
	// produces exactly the canonical path a bucket creation needs, /bucket,
	// without a second signing implementation for one call.
	signer, err := storage.NewS3Store(storage.S3Config{
		Endpoint: endpoint, Region: "us-east-1", Bucket: bucket,
		AccessKey: accessKey, SecretKey: secretKey, PathStyle: true,
	})
	if err != nil {
		return err
	}

	url, err := signer.PresignBucketCreate(time.Minute)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("create bucket: %w", err)
	}
	defer resp.Body.Close()

	// 409 BucketAlreadyOwnedByYou is success for our purposes.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return fmt.Errorf("create bucket returned %d: %s", resp.StatusCode, body)
	}
	return nil
}

// cleanups run in reverse order once the suite finishes.
var cleanups []func()

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
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
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

	if err := startObjectStore(ctx); err != nil {
		return 1, err
	}

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
