// Package service holds the application logic: it opens transactions, resolves
// who is asking, calls the domain, and persists what the domain decided.
//
// It makes no authorisation decisions of its own. Every "may this actor do
// this" question is answered by internal/domain/tenant or by the expense state
// machine, so there is exactly one copy of the permission matrix and it is the
// one covered by the domain tests.
package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mlkad/b2b-expense-tracker/internal/auth"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
)

// Scope turns a verified token into a bound transaction and a resolved actor.
//
// Every tenant-scoped service method in this package goes through one of its
// three methods, and none of them takes a tenant id as an argument. The tenant
// comes from the token, is bound to the session by WithTenantTx, and is read
// back off the connection by the repositories - so there is no point at which
// a caller could supply one.
type Scope struct {
	db      *postgres.DB
	tenancy *repo.TenancyRepository
}

func NewScope(db *postgres.DB, tenancy *repo.TenancyRepository) *Scope {
	return &Scope{db: db, tenancy: tenancy}
}

// DB exposes the pool for the services that need to reach identity tables
// outside any tenant - registration and login.
func (s *Scope) DB() *postgres.DB { return s.db }

// Work is the body of a scoped operation.
type Work func(ctx context.Context, tc *postgres.TenantConn, actor tenant.Actor) error

// Read runs work in a read-only transaction.
//
// READ ONLY is not just documentation: PostgreSQL refuses writes in such a
// transaction, so a GET handler that acquires a write path by mistake fails
// loudly instead of quietly mutating data. It also lets the server skip
// assigning a transaction id.
func (s *Scope) Read(ctx context.Context, subject auth.Subject, work Work) error {
	return s.run(ctx, subject, postgres.Binding{
		TenantID: subject.TenantID,
		ReadOnly: true,
	}, work)
}

// Write runs work in a read-write transaction at READ COMMITTED, which is the
// right level for the compare-and-swap pattern the repositories use: the row
// lock, not the isolation level, is what serialises a decision.
func (s *Scope) Write(ctx context.Context, subject auth.Subject, work Work) error {
	return s.run(ctx, subject, postgres.Binding{
		TenantID: subject.TenantID,
		ReadOnly: false,
	}, work)
}

// Snapshot runs work in a REPEATABLE READ read-only transaction.
//
// The export uses it. A report that streams for a minute against a busy tenant
// would otherwise see rows committed part way through the scan, and the
// totals on the last page would not agree with the rows on the first.
func (s *Scope) Snapshot(ctx context.Context, subject auth.Subject, work Work) error {
	return s.run(ctx, subject, postgres.Binding{
		TenantID:  subject.TenantID,
		ReadOnly:  true,
		Isolation: pgx.RepeatableRead,
	}, work)
}

// run binds the session, resolves the actor and hands both to the work.
//
// The actor is resolved inside the transaction, as its first statement. That
// ordering matters: the role that authorises the work and the rows the work
// touches then come from a single snapshot, so a role change committed
// mid-request applies to both or to neither.
func (s *Scope) run(ctx context.Context, subject auth.Subject, b postgres.Binding, work Work) error {
	if subject.TenantID == uuid.Nil {
		return fmt.Errorf("%w: the token carried no tenant", shared.ErrNoTenantContext)
	}

	return s.db.WithTenantTx(ctx, b, func(ctx context.Context, tc *postgres.TenantConn) error {
		actor, err := s.tenancy.ResolveActor(ctx, tc, subject.UserID)
		if err != nil {
			// A valid token whose membership has been deleted. Forbidden, not
			// not-found: the caller exists, they are simply no longer a member
			// here, and 404 on every endpoint would look like an outage.
			if err == shared.ErrNotFound {
				return fmt.Errorf("%w: no membership in this organisation", shared.ErrForbidden)
			}
			return err
		}

		if !actor.Active() {
			return fmt.Errorf("%w: %s", shared.ErrForbidden, inactiveReason(actor))
		}

		// The binding is set from the token's tenant claim, and ResolveActor
		// only returns a row when a membership exists for that tenant - so
		// this cannot fail. It is checked anyway, because the cost is a
		// comparison and the cost of being wrong is a cross-tenant write.
		if actor.TenantID != tc.TenantID() {
			return fmt.Errorf("%w: resolved actor belongs to a different tenant than the session binding",
				shared.ErrTenantMismatch)
		}

		return work(ctx, tc, actor)
	})
}

func inactiveReason(a tenant.Actor) string {
	switch {
	case a.TenantStatus == tenant.StatusSuspended:
		return "this organisation is suspended; an active subscription is required"
	case a.TenantStatus == tenant.StatusCancelled:
		return "this organisation has been closed"
	case a.Status == tenant.MembershipInvited:
		return "your invitation has not been accepted yet"
	case a.Status == tenant.MembershipSuspended:
		return "your membership has been suspended"
	default:
		return "your membership is not active"
	}
}

// Require is the route-level permission check, used by handlers before they
// open a transaction and by services as a guard on operations that are not
// about one specific claim.
func Require(actor tenant.Actor, p tenant.Permission) error {
	if !actor.Can(p) {
		return fmt.Errorf("%w: role %s does not carry %s", shared.ErrForbidden, actor.Role, p)
	}
	return nil
}
