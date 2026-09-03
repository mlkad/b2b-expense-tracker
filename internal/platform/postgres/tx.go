package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
)

// TenantConn is a database handle that is provably bound to one tenant.
//
// The pgx.Tx inside it is unexported and there is no exported constructor, so
// the only way to obtain one is from WithTenantTx, which sets the session
// variable before it hands the handle over. A repository method that takes a
// *TenantConn therefore cannot be called outside a bound transaction - not by
// convention, and not because a reviewer noticed, but because the program
// would not compile.
//
// This is the whole reason the type exists. A `Querier` interface satisfied by
// both the pool and a transaction would be more flexible and would lose the
// guarantee: any handler could reach for the pool, run a query with no tenant
// bound, and under a fail-closed policy get an empty result that looks like
// "this tenant has no expenses" rather than an error.
//
// Commit and Rollback are not exposed. The transaction's lifetime belongs to
// WithTenantTx; a repository that could commit early would release the
// binding while the service still believed it held one.
type TenantConn struct {
	tx       pgx.Tx
	tenantID uuid.UUID
	actorID  uuid.UUID
	readOnly bool
}

// TenantID reports the binding. Repositories use it to fill tenant_id columns
// on insert rather than taking it as an argument, which removes the one way a
// caller could write a row into the wrong tenant. RLS would refuse such a
// write anyway - this makes it not happen in the first place.
func (c *TenantConn) TenantID() uuid.UUID { return c.tenantID }

// ActorID reports the membership the transaction was opened for. Zero when the
// transaction is a system one.
func (c *TenantConn) ActorID() uuid.UUID { return c.actorID }

func (c *TenantConn) ReadOnly() bool { return c.readOnly }

// Exec, Query and QueryRow are the three methods sqlc's generated code needs.
// They are written out rather than obtained by embedding pgx.Tx, because
// embedding would also export Commit, Rollback, Begin and Conn.
func (c *TenantConn) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return c.tx.Exec(ctx, sql, args...)
}

func (c *TenantConn) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return c.tx.Query(ctx, sql, args...)
}

func (c *TenantConn) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return c.tx.QueryRow(ctx, sql, args...)
}

// CopyFrom is exposed for the bulk import path. It runs inside the same
// transaction and therefore under the same policies: COPY is not a way around
// RLS, and a row it tries to insert for another tenant fails the WITH CHECK
// clause exactly as an INSERT would.
func (c *TenantConn) CopyFrom(ctx context.Context, table pgx.Identifier, cols []string, src pgx.CopyFromSource) (int64, error) {
	return c.tx.CopyFrom(ctx, table, cols, src)
}

// Binding is what a transaction is opened for.
type Binding struct {
	// TenantID is required for WithTenantTx and must not be the nil UUID.
	TenantID uuid.UUID

	// ActorID is the acting membership. Optional: worker transactions act on
	// behalf of a tenant with no user behind them.
	ActorID uuid.UUID

	// ReadOnly opens the transaction READ ONLY, which lets PostgreSQL skip
	// assigning a transaction id and makes an accidental write fail rather
	// than succeed. Every GET endpoint uses it.
	ReadOnly bool

	// Isolation defaults to READ COMMITTED. The export path raises it to
	// REPEATABLE READ so a report that streams for ninety seconds describes
	// one instant rather than a smear across the run.
	Isolation pgx.TxIsoLevel
}

// MaxSerializationRetries bounds the retry loop for transactions that fail
// with a serialization or deadlock error.
//
// Retrying is only correct because the callback is required to be a pure
// function of the database state it reads. A callback that sends an email or
// charges a card must not be retried, and such work is queued through Asynq
// inside the transaction instead of performed in it.
const MaxSerializationRetries = 3

// WithTenantTx runs fn inside a transaction bound to a tenant.
//
// The binding is one extra round trip at the start of the transaction:
//
//	SELECT set_config('app.tenant_id', $1, true), set_config('app.actor_id', $2, true)
//
// Three details in that statement are load-bearing.
//
// First, set_config rather than SET LOCAL. `SET LOCAL app.tenant_id = $1` is
// not valid: SET does not accept bind parameters, so using it would mean
// interpolating the tenant id into SQL text. The id comes from a verified JWT
// and is parsed as a UUID before it gets here, so it is not attacker-shaped
// today - but "we build SQL by concatenation, and it is safe because of a
// check two layers away" is exactly the pattern that becomes an injection the
// day someone adds a second, less careful caller. set_config takes the value
// as a parameter and the question never arises.
//
// Second, the third argument is is_local = true. That scopes the setting to
// this transaction, so COMMIT or ROLLBACK reverts it. This is what makes the
// design safe on a pooled connection: the connection returns to the pool with
// no tenant bound, and the next checkout - which may be a different customer's
// request microseconds later - starts from nothing. A session-level
// set_config, or a `SET` outside a transaction, would leave the previous
// tenant's id on the connection, and the next request would silently inherit
// it. That is the single most dangerous mistake available in this design, and
// the entire reason every tenant query is wrapped in a transaction even when
// it is a single SELECT.
//
// Third, both settings go in one statement. Two round trips would double the
// per-request latency floor for no benefit.
//
// The callback must not retain the *TenantConn: it is invalid the moment
// WithTenantTx returns.
func (db *DB) WithTenantTx(ctx context.Context, b Binding, fn func(context.Context, *TenantConn) error) error {
	if b.TenantID == uuid.Nil {
		// A programming error, not a client one. Proceeding would open a
		// transaction with no binding, and under the fail-closed policies in
		// 00006 every query in it would return zero rows - which reads as "this
		// tenant has no data" and is the worst possible way to fail.
		return fmt.Errorf("%w: WithTenantTx called with the nil tenant id", shared.ErrNoTenantContext)
	}
	return db.runTx(ctx, b, false, fn)
}

// WithSystemTx runs fn with app.system = 'on', which widens the RESTRICTIVE
// isolation policies to every tenant.
//
// This is the only privilege escalation in the persistence layer and it exists
// for exactly two callers:
//
//   - the billing relay receiver, which must write a subscription row for a
//     tenant it identifies from a signed payload rather than from a session;
//   - worker sweeps that iterate across tenants, such as the recurring-charge
//     materialiser, which has no single tenant to bind to.
//
// A Binding.TenantID may still be supplied and is still set, so a system
// transaction that also knows its tenant gets the tenant-scoped policy as
// well; that is the preferred shape and the relay uses it once the tenant is
// resolved.
//
// Every call is logged at warn. A system transaction opened by anything other
// than those two paths is an incident, and it should be visible without
// anybody having to go looking.
func (db *DB) WithSystemTx(ctx context.Context, b Binding, reason string, fn func(context.Context, *TenantConn) error) error {
	db.log.LogAttrs(ctx, slog.LevelWarn, "system transaction opened",
		slog.String("reason", reason),
		slog.String("tenant_id", nullableUUID(b.TenantID)))
	return db.runTx(ctx, b, true, fn)
}

func (db *DB) runTx(ctx context.Context, b Binding, system bool, fn func(context.Context, *TenantConn) error) error {
	if b.Isolation == "" {
		b.Isolation = pgx.ReadCommitted
	}
	access := pgx.ReadWrite
	if b.ReadOnly {
		access = pgx.ReadOnly
	}

	var lastErr error
	for attempt := 1; attempt <= MaxSerializationRetries; attempt++ {
		err := db.attemptTx(ctx, b, system, access, fn)
		if err == nil {
			return nil
		}
		if !isRetryable(err) {
			return err
		}
		lastErr = err

		// The caller's context governs: a request that has already been
		// abandoned must not keep retrying and holding a pool slot.
		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}

		// Linear backoff with a small floor. Serialization failures under
		// READ COMMITTED are rare and short-lived; the point of the pause is to
		// let the winning transaction commit, not to wait out a queue.
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(time.Duration(attempt) * 10 * time.Millisecond):
		}

		db.log.LogAttrs(ctx, slog.LevelInfo, "retrying serialization failure",
			slog.Int("attempt", attempt),
			slog.String("error", err.Error()))
	}
	return fmt.Errorf("transaction failed after %d attempts: %w", MaxSerializationRetries, lastErr)
}

func (db *DB) attemptTx(
	ctx context.Context,
	b Binding,
	system bool,
	access pgx.TxAccessMode,
	fn func(context.Context, *TenantConn) error,
) (err error) {
	conn, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: b.Isolation, AccessMode: access})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	// Rollback on any path that does not reach the explicit Commit, including
	// a panic. pgx makes Rollback after Commit a no-op returning
	// pgx.ErrTxClosed, so this is safe to arm unconditionally.
	committed := false
	defer func() {
		if committed {
			return
		}
		// A fresh context: the request's may already be cancelled, and a
		// rollback that cannot be sent leaves the transaction open on the
		// server until idle_in_transaction_session_timeout fires.
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rbErr := tx.Rollback(rbCtx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			db.log.LogAttrs(ctx, slog.LevelError, "rollback failed", slog.String("error", rbErr.Error()))
		}
	}()

	if err := bindSession(ctx, tx, b, system); err != nil {
		return err
	}

	tc := &TenantConn{tx: tx, tenantID: b.TenantID, actorID: b.ActorID, readOnly: b.ReadOnly}
	if err := fn(ctx, tc); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true

	if db.cfg.VerifyTenantResetOnCommit {
		return db.assertBindingCleared(ctx, conn.Conn())
	}
	return nil
}

// bindSession installs the transaction-local settings the policies read.
//
// The UUIDs are passed as text, not as uuid parameters: set_config's signature
// is (text, text, boolean), and handing pgx a uuid.UUID for the second
// argument makes it infer the wrong parameter type and fail at bind time.
func bindSession(ctx context.Context, tx pgx.Tx, b Binding, system bool) error {
	tenant := ""
	if b.TenantID != uuid.Nil {
		tenant = b.TenantID.String()
	}
	actor := ""
	if b.ActorID != uuid.Nil {
		actor = b.ActorID.String()
	}
	systemFlag := "off"
	if system {
		systemFlag = "on"
	}

	_, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id', $1, true),
		        set_config('app.actor_id',  $2, true),
		        set_config('app.system',    $3, true)`,
		tenant, actor, systemFlag)
	if err != nil {
		return fmt.Errorf("bind tenant session: %w", err)
	}
	return nil
}

// assertBindingCleared re-reads the session variable on the same physical
// connection after the transaction has ended.
//
// It runs only when VerifyTenantResetOnCommit is set. What it proves is the
// property everything else assumes: that a connection going back into the pool
// carries no trace of the tenant that just used it. If this ever fails, every
// isolation guarantee in the service is void, so it is an error rather than a
// warning and the connection is not reused.
func (db *DB) assertBindingCleared(ctx context.Context, conn *pgx.Conn) error {
	var bound string
	if err := conn.QueryRow(ctx, `SELECT coalesce(current_setting('app.tenant_id', true), '')`).Scan(&bound); err != nil {
		return fmt.Errorf("verify tenant reset: %w", err)
	}
	if bound != "" {
		// Poisoning the connection is the conservative move: it is removed
		// from the pool rather than handed to the next request.
		_ = conn.Close(ctx)
		return fmt.Errorf(
			"tenant binding %q survived the transaction; a pooled connection would leak it to the next request", bound)
	}
	return nil
}

// isRetryable reports whether an error is a transient concurrency failure.
//
// 40001 serialization_failure and 40P01 deadlock_detected are the two the
// database asks the client to retry. Nothing else is retried: a constraint
// violation or a policy refusal will fail identically every time, and retrying
// it just multiplies the load during an incident.
func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40001" || pgErr.Code == "40P01"
}

func nullableUUID(id uuid.UUID) string {
	if id == uuid.Nil {
		return "<none>"
	}
	return id.String()
}
