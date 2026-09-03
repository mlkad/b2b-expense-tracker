// Package postgres owns the connection pool and the transaction boundary.
//
// Every tenant-scoped query in this service runs inside a transaction opened
// by this package, because that is the only place the PostgreSQL session
// variable that Row-Level Security reads is set. The types here are shaped so
// that a repository method cannot be called any other way: see tx.go.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config describes the pool. Zero-valued fields fall back to withDefaults, so
// a caller may set only DSN.
type Config struct {
	// DSN is a postgres:// URL or a libpq keyword/value string. It must name
	// the runtime role, not the migration owner: PostgreSQL exempts a table's
	// owner from its policies unless FORCE ROW LEVEL SECURITY is set, and
	// exempts superusers unconditionally. Migration 00006 does set FORCE, so a
	// misconfigured deployment fails closed rather than silently sharing data
	// between tenants - but the role is still the first line, and
	// RefuseIfBypassRLS below checks it at startup.
	DSN string

	MaxConns int32
	MinConns int32

	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
	ConnectTimeout    time.Duration

	// StatementTimeout is applied server-side to every session. Without it one
	// pathological query holds a pool slot until the client gives up, and a
	// handful of them take the service down by exhaustion rather than by
	// error. It is set as a connection runtime parameter, so it survives
	// pgx reconnecting a broken connection - a session-level SET after
	// connect would not.
	StatementTimeout time.Duration

	// IdleInTxTimeout kills sessions left inside an open transaction. Those
	// hold locks and pin the oldest xmin, which stops VACUUM from reclaiming
	// anything newer across the whole database.
	IdleInTxTimeout time.Duration

	ApplicationName string

	// SlowQueryThreshold enables the query tracer. Zero disables tracing.
	SlowQueryThreshold time.Duration

	// VerifyTenantResetOnCommit turns on a second round trip after every
	// tenant transaction, asserting that the session variable really did go
	// out of scope. It is the direct test of the property this whole design
	// rests on, and it is off in production because the property is structural
	// rather than probabilistic - set_config(..., true) is reverted by the
	// transaction, not by us remembering to revert it. Integration tests and
	// staging run with it on.
	VerifyTenantResetOnCommit bool
}

func (c Config) withDefaults() Config {
	if c.MaxConns <= 0 {
		c.MaxConns = 25
	}
	if c.MinConns < 0 {
		c.MinConns = 0
	}
	if c.MaxConnLifetime <= 0 {
		c.MaxConnLifetime = time.Hour
	}
	if c.MaxConnIdleTime <= 0 {
		c.MaxConnIdleTime = 30 * time.Minute
	}
	if c.HealthCheckPeriod <= 0 {
		c.HealthCheckPeriod = time.Minute
	}
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = 10 * time.Second
	}
	if c.StatementTimeout <= 0 {
		c.StatementTimeout = 15 * time.Second
	}
	if c.IdleInTxTimeout <= 0 {
		c.IdleInTxTimeout = 30 * time.Second
	}
	if c.ApplicationName == "" {
		c.ApplicationName = "b2b-expense-tracker"
	}
	return c
}

// DB is the handle the rest of the service holds.
//
// It exposes no way to run a query directly. Everything goes through
// WithTenantTx or WithSystemTx, which is what makes "no query runs without a
// tenant bound" a property of the type system rather than of code review.
type DB struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	cfg  Config
}

// Open builds the pool and verifies the connection is usable and correctly
// privileged.
func Open(ctx context.Context, cfg Config, log *slog.Logger) (*DB, error) {
	cfg = cfg.withDefaults()

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.HealthCheckPeriod = cfg.HealthCheckPeriod
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	// Runtime parameters travel in the startup packet, so they apply to every
	// connection the pool ever opens - including the replacements it creates
	// after a failover - without an AfterConnect hook that could be skipped.
	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	poolCfg.ConnConfig.RuntimeParams["application_name"] = cfg.ApplicationName
	poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = msString(cfg.StatementTimeout)
	poolCfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = msString(cfg.IdleInTxTimeout)

	// row_security is on by default; naming it makes the intent explicit and
	// makes an accidental `row_security = off` in a DSN visible in review.
	poolCfg.ConnConfig.RuntimeParams["row_security"] = "on"

	if cfg.SlowQueryThreshold > 0 {
		poolCfg.ConnConfig.Tracer = &slowQueryTracer{threshold: cfg.SlowQueryThreshold, log: log}
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	db := &DB{pool: pool, log: log, cfg: cfg}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := db.verifyRuntimeRole(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}

// verifyRuntimeRole refuses to start if the credentials can see through RLS.
//
// This is the check that would have caught the failure mode this design is
// most exposed to: a deploy pointed at the migration owner's DSN. Everything
// works, every test passes, and every tenant sees every other tenant's data.
// It is cheap, it runs once, and there is no legitimate configuration in which
// the API connects as a superuser or a BYPASSRLS role.
func (db *DB) verifyRuntimeRole(ctx context.Context) error {
	var (
		role      string
		superuser bool
		bypassRLS bool
		rowSecOn  bool
	)
	err := db.pool.QueryRow(ctx, `
		SELECT current_user,
		       rolsuper,
		       rolbypassrls,
		       current_setting('row_security') = 'on'
		  FROM pg_roles
		 WHERE rolname = current_user`,
	).Scan(&role, &superuser, &bypassRLS, &rowSecOn)
	if err != nil {
		return fmt.Errorf("verify runtime role: %w", err)
	}

	var problems []string
	if superuser {
		problems = append(problems, "is a superuser")
	}
	if bypassRLS {
		problems = append(problems, "has BYPASSRLS")
	}
	if !rowSecOn {
		problems = append(problems, "has row_security disabled")
	}
	if len(problems) > 0 {
		return fmt.Errorf(
			"refusing to start: database role %q %s, so row-level security would not isolate tenants; "+
				"connect as the runtime role (expense_app), not the migration owner",
			role, strings.Join(problems, " and "))
	}

	db.log.Info("database role verified", slog.String("role", role))
	return nil
}

// Pool exposes the underlying pool for the health check and for the migration
// runner in tests. It is deliberately not used by any repository: a query run
// here carries no tenant binding, and under FORCE RLS it would return nothing.
func (db *DB) Pool() *pgxpool.Pool { return db.pool }

func (db *DB) Close() { db.pool.Close() }

// Ping is the readiness probe's query. It takes a connection out of the pool
// rather than checking a cached flag, so a pool that is full of broken
// connections reports unready.
func (db *DB) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return db.pool.Ping(ctx)
}

// Stat reports pool utilisation for the metrics endpoint. Saturation here is
// the first symptom of nearly every database-side incident.
func (db *DB) Stat() *pgxpool.Stat { return db.pool.Stat() }

func msString(d time.Duration) string {
	return fmt.Sprintf("%d", d.Milliseconds())
}

// slowQueryTracer logs statements that exceed a threshold.
//
// It logs the SQL and the duration but never the arguments: those are expense
// amounts, merchant names and email addresses, and a slow-query log is copied
// into ticketing systems and pasted into chat.
type slowQueryTracer struct {
	threshold time.Duration
	log       *slog.Logger
}

type traceKey struct{}

type traceState struct {
	start time.Time
	sql   string
}

func (t *slowQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, traceKey{}, &traceState{start: time.Now(), sql: data.SQL})
}

func (t *slowQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	state, ok := ctx.Value(traceKey{}).(*traceState)
	if !ok {
		return
	}
	elapsed := time.Since(state.start)
	if elapsed < t.threshold && data.Err == nil {
		return
	}

	attrs := []slog.Attr{
		slog.Duration("elapsed", elapsed),
		slog.String("sql", collapse(state.sql)),
	}
	if data.Err != nil && !errors.Is(data.Err, context.Canceled) {
		attrs = append(attrs, slog.String("error", data.Err.Error()))
		t.log.LogAttrs(ctx, slog.LevelWarn, "query failed", attrs...)
		return
	}
	t.log.LogAttrs(ctx, slog.LevelWarn, "slow query", attrs...)
}

// collapse folds a multi-line query onto one line and truncates it, so a log
// aggregator keeps it as a single record.
func collapse(sql string) string {
	s := strings.Join(strings.Fields(sql), " ")
	const max = 300
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
