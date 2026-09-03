// Command migrate applies the database migrations.
//
// It exists instead of the goose CLI because that binary imports a driver for
// every database goose supports - mssql, mysql, vertica, ydb, turso - and
// building it would pull all of them into this module's dependency graph for
// no benefit. The goose library is already a dependency of the integration
// harness, and driving it directly costs forty lines and adds nothing.
//
// The migrations are embedded, so this binary and the schema it expects are a
// single artefact.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	appdb "github.com/mlkad/b2b-expense-tracker/db"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	timeout := flag.Duration("timeout", 5*time.Minute,
		"how long the whole migration run may take before it is abandoned")
	flag.Parse()

	command := flag.Arg(0)
	if command == "" {
		command = "up"
	}

	// MIGRATE_DATABASE_URL is read first and separately from DATABASE_URL on
	// purpose. Migrations must run as the schema owner, while the application
	// connects as expense_app - which holds DML grants only and would be
	// refused every statement here. Keeping them in different variables means
	// the two credentials cannot be confused by a copy-paste, and a deployment
	// that supplies only the application's DSN fails immediately rather than
	// part way through the first CREATE TABLE.
	dsn := os.Getenv("MIGRATE_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("GOOSE_DBSTRING")
	}
	if dsn == "" {
		return errors.New("MIGRATE_DATABASE_URL is required, and must be the schema owner's connection string, not the application's")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// One connection. Migrations are strictly sequential and goose takes an
	// advisory lock, so a pool would only make a failure harder to read.
	db.SetMaxOpenConns(1)

	goose.SetBaseFS(appdb.Migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	const dir = "migrations"

	switch command {
	case "up":
		return goose.UpContext(ctx, db, dir)
	case "up-by-one":
		return goose.UpByOneContext(ctx, db, dir)
	case "down":
		// Deliberately one step, and deliberately not "reset". A single
		// mistyped argument that drops every table is not a thing a deployment
		// image should be able to do.
		return goose.DownContext(ctx, db, dir)
	case "status":
		return goose.StatusContext(ctx, db, dir)
	case "version":
		return goose.VersionContext(ctx, db, dir)
	default:
		return fmt.Errorf("unknown command %q; one of: %s",
			command, strings.Join([]string{"up", "up-by-one", "down", "status", "version"}, ", "))
	}
}
