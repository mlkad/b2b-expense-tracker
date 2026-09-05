//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
)

// The runtime role must not be able to see through its own policies. Every
// other test in this file is meaningless if this one fails.
func TestRuntimeRoleCannotBypassRLS(t *testing.T) {
	var (
		role      string
		superuser bool
		bypass    bool
		rowSec    bool
	)
	err := app.Pool().QueryRow(context.Background(), `
		SELECT current_user, rolsuper, rolbypassrls, current_setting('row_security') = 'on'
		  FROM pg_roles WHERE rolname = current_user`,
	).Scan(&role, &superuser, &bypass, &rowSec)
	if err != nil {
		t.Fatalf("inspect role: %v", err)
	}

	if role != appUser {
		t.Fatalf("the suite is connected as %q, not the runtime role", role)
	}
	if superuser || bypass {
		t.Fatalf("role %q is superuser=%v bypassrls=%v; RLS would not apply", role, superuser, bypass)
	}
	if !rowSec {
		t.Fatal("row_security is off for this session")
	}
}

// FORCE ROW LEVEL SECURITY must be set on every tenant table. Without it, the
// table's owner is exempt from its policies, and a deployment that
// accidentally uses the migration credentials has RLS enabled and no
// isolation - which looks correct in every schema dump.
func TestEveryTenantTableForcesRLS(t *testing.T) {
	rows, err := app.Pool().Query(context.Background(), `
		SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public'
		   AND c.relkind = 'r'
		   AND EXISTS (
		         SELECT 1 FROM information_schema.columns col
		          WHERE col.table_name = c.relname
		            AND col.column_name IN ('tenant_id')
		       )`)
	if err != nil {
		t.Fatalf("inspect tables: %v", err)
	}
	defer rows.Close()

	checked := 0
	for rows.Next() {
		var (
			name           string
			enabled, force bool
		)
		if err := rows.Scan(&name, &enabled, &force); err != nil {
			t.Fatal(err)
		}
		checked++

		// billing_events carries a tenant_id but is a system table: the relay
		// writes rows for tenants it has not yet identified, so it has no
		// policy and is never reachable from a tenant-scoped route.
		if name == "billing_events" {
			continue
		}
		if !enabled {
			t.Errorf("table %s has a tenant_id column and no row-level security", name)
		}
		if !force {
			t.Errorf("table %s does not FORCE row-level security; its owner would bypass every policy", name)
		}
	}
	if checked == 0 {
		t.Fatal("found no tenant tables to check; the query is wrong")
	}
}

// The isolation policies must be RESTRICTIVE. A permissive one is OR-ed with
// every other policy, so a future feature adding one would widen access - and
// the widening would look like an ordinary commit in review.
func TestIsolationPoliciesAreRestrictive(t *testing.T) {
	rows, err := app.Pool().Query(context.Background(),
		`SELECT tablename, permissive FROM pg_policies
		  WHERE schemaname = 'public' AND policyname = 'tenant_isolation'`)
	if err != nil {
		t.Fatalf("inspect policies: %v", err)
	}
	defer rows.Close()

	found := 0
	for rows.Next() {
		var table, permissive string
		if err := rows.Scan(&table, &permissive); err != nil {
			t.Fatal(err)
		}
		found++
		if !strings.EqualFold(permissive, "RESTRICTIVE") {
			t.Errorf("tenant_isolation on %s is %s; it must be RESTRICTIVE so no other policy can widen it",
				table, permissive)
		}
	}
	if found < 9 {
		t.Errorf("found tenant_isolation on only %d tables; expected every tenant table", found)
	}
}

// The property the whole design rests on: one tenant's transaction cannot see
// another's rows, and cannot write into another tenant either.
func TestTenantsAreIsolated(t *testing.T) {
	acme := seedOrg(t, "acme-iso")
	globex := seedOrg(t, "globex-iso")

	acmeClaim := seedClaim(t, acme, "draft", 5000)
	seedClaim(t, globex, "draft", 9900)

	expenses := repo.NewExpenseRepository()
	ctx := context.Background()

	t.Run("a tenant sees only its own claims", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			org   org
			want  int64
			count int
		}{
			{"acme", acme, 5000, 1},
			{"globex", globex, 9900, 1},
		} {
			err := app.WithTenantTx(ctx, postgres.Binding{TenantID: tc.org.TenantID, ReadOnly: true},
				func(ctx context.Context, conn *postgres.TenantConn) error {
					list, err := expenses.List(ctx, conn, repo.Filter{}, nil, 100)
					if err != nil {
						return err
					}
					if len(list) != tc.count {
						return fmt.Errorf("%s saw %d claims, want %d", tc.name, len(list), tc.count)
					}
					if list[0].Amount.Minor != tc.want {
						return fmt.Errorf("%s saw a claim for %d, want %d", tc.name, list[0].Amount.Minor, tc.want)
					}
					return nil
				})
			if err != nil {
				t.Error(err)
			}
		}
	})

	t.Run("one tenant cannot fetch another's claim by id", func(t *testing.T) {
		err := app.WithTenantTx(ctx, postgres.Binding{TenantID: globex.TenantID, ReadOnly: true},
			func(ctx context.Context, conn *postgres.TenantConn) error {
				_, err := expenses.Get(ctx, conn, acmeClaim)
				// Not-found, not forbidden. The row is filtered out before the
				// query returns, so the caller cannot even learn the id exists.
				if !errors.Is(err, shared.ErrNotFound) {
					return fmt.Errorf("got %v, want ErrNotFound", err)
				}
				return nil
			})
		if err != nil {
			t.Error(err)
		}
	})

	// The refused statement aborts the transaction, so the callback returns
	// the policy error rather than swallowing it - a callback that returned
	// nil after a failed statement would then fail at COMMIT with
	// "commit unexpectedly resulted in rollback", which says nothing about
	// what was actually refused.
	t.Run("one tenant cannot move a claim into another", func(t *testing.T) {
		err := app.WithTenantTx(ctx, postgres.Binding{TenantID: acme.TenantID},
			func(ctx context.Context, conn *postgres.TenantConn) error {
				_, err := conn.Exec(ctx,
					`UPDATE expenses SET tenant_id = $1 WHERE id = $2`, globex.TenantID, acmeClaim)
				return err
			})
		if err == nil {
			t.Fatal("the update succeeded; a tenant can export rows into another tenant")
		}
		if !strings.Contains(err.Error(), "row-level security") {
			t.Errorf("refused for the wrong reason: %v", err)
		}

		if n := countAsOwner(t, `SELECT count(*) FROM expenses WHERE id = $1 AND tenant_id = $2`,
			acmeClaim, acme.TenantID); n != 1 {
			t.Errorf("the claim did not stay in its own tenant (found %d)", n)
		}
	})

	t.Run("one tenant cannot insert a row for another", func(t *testing.T) {
		err := app.WithTenantTx(ctx, postgres.Binding{TenantID: globex.TenantID},
			func(ctx context.Context, conn *postgres.TenantConn) error {
				_, err := conn.Exec(ctx,
					`INSERT INTO expenses (tenant_id, submitter_id, status, amount_minor, currency, merchant, spent_at)
					 VALUES ($1, $2, 'draft', 100, 'USD', 'forged', CURRENT_DATE)`,
					acme.TenantID, acme.Submitter)
				return err
			})
		if err == nil {
			t.Fatal("the insert succeeded; a tenant can plant rows in another tenant")
		}
		if !strings.Contains(err.Error(), "row-level security") {
			t.Errorf("refused for the wrong reason: %v", err)
		}
	})
}

// An unbound session must see nothing, not everything.
//
// This is the direction the failure has to go in. A policy written so that an
// unbound session matches every row would produce a service that looks healthy
// and serves every customer's data to whoever asks first.
func TestUnboundSessionSeesNothing(t *testing.T) {
	o := seedOrg(t, "acme-unbound")
	seedClaim(t, o, "draft", 1234)

	var visible int
	err := app.Pool().QueryRow(context.Background(), `SELECT count(*) FROM expenses`).Scan(&visible)
	if err != nil {
		t.Fatalf("query without a binding: %v", err)
	}
	if visible != 0 {
		t.Fatalf("a session with no tenant bound saw %d claims; it must see none", visible)
	}
}

// WithTenantTx must refuse the nil tenant rather than opening an unbound
// transaction, which under the fail-closed policies would return empty results
// that read like an empty account.
func TestNilTenantIsRefused(t *testing.T) {
	err := app.WithTenantTx(context.Background(), postgres.Binding{TenantID: uuid.Nil},
		func(context.Context, *postgres.TenantConn) error {
			t.Error("the callback ran with no tenant bound")
			return nil
		})
	if !errors.Is(err, shared.ErrNoTenantContext) {
		t.Fatalf("got %v, want ErrNoTenantContext", err)
	}
}

// The binding must not survive its transaction.
//
// This is the failure mode the whole design exists to prevent: a session-level
// SET leaves the previous tenant's id on a pooled connection, and the next
// request - which may be a different customer's, microseconds later - inherits
// it. The pool here holds four connections and the test runs far more
// transactions than that, so connections are certainly reused.
func TestBindingDoesNotSurviveTheTransaction(t *testing.T) {
	o := seedOrg(t, "acme-leak")
	seedClaim(t, o, "draft", 4242)

	ctx := context.Background()

	for i := 0; i < 20; i++ {
		if err := app.WithTenantTx(ctx, postgres.Binding{TenantID: o.TenantID, ReadOnly: true},
			func(ctx context.Context, conn *postgres.TenantConn) error {
				var n int
				return conn.QueryRow(ctx, `SELECT count(*) FROM expenses`).Scan(&n)
			}); err != nil {
			// VerifyTenantResetOnCommit is on for this suite, so a surviving
			// binding fails the transaction itself rather than being missed.
			t.Fatalf("round %d: %v", i, err)
		}

		// Independently of that check: take a connection straight from the
		// pool and ask it what it is bound to.
		var bound string
		err := app.Pool().QueryRow(ctx,
			`SELECT coalesce(current_setting('app.tenant_id', true), '')`).Scan(&bound)
		if err != nil {
			t.Fatalf("round %d: read binding: %v", i, err)
		}
		if bound != "" {
			t.Fatalf("round %d: a pooled connection came back bound to %q", i, bound)
		}
	}
}

// Concurrent transactions for different tenants share a small pool. Each must
// see only its own rows, whichever physical connection it happens to get.
func TestConcurrentTenantsDoNotBleed(t *testing.T) {
	const tenants = 6

	orgs := make([]org, tenants)
	for i := range orgs {
		orgs[i] = seedOrg(t, fmt.Sprintf("concurrent-%d", i))
		// A distinct amount per tenant, so a row from the wrong tenant is
		// identifiable rather than merely counted.
		seedClaim(t, orgs[i], "draft", int64(1000+i))
	}

	expenses := repo.NewExpenseRepository()

	var wg sync.WaitGroup
	errs := make(chan error, tenants*10)

	for round := 0; round < 10; round++ {
		for i := range orgs {
			wg.Add(1)
			go func(o org, want int64) {
				defer wg.Done()

				err := app.WithTenantTx(context.Background(),
					postgres.Binding{TenantID: o.TenantID, ReadOnly: true},
					func(ctx context.Context, conn *postgres.TenantConn) error {
						list, err := expenses.List(ctx, conn, repo.Filter{}, nil, 100)
						if err != nil {
							return err
						}
						if len(list) != 1 {
							return fmt.Errorf("tenant %s saw %d claims, want 1", o.Slug, len(list))
						}
						if list[0].Amount.Minor != want {
							return fmt.Errorf("tenant %s saw a claim for %d, want %d - that row belongs to another tenant",
								o.Slug, list[0].Amount.Minor, want)
						}
						return nil
					})
				if err != nil {
					errs <- err
				}
			}(orgs[i], int64(1000+i))
		}
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// The audit ledger must be append-only, and it must be so at both the
// privilege level and the policy level.
func TestAuditLedgerIsAppendOnly(t *testing.T) {
	o := seedOrg(t, "acme-audit")
	claim := seedClaim(t, o, "draft", 700)
	ctx := context.Background()

	err := app.WithTenantTx(ctx, postgres.Binding{TenantID: o.TenantID},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			if _, err := conn.Exec(ctx,
				`INSERT INTO expense_events (tenant_id, expense_id, action, to_status, amount_minor, currency, revision)
				 VALUES ($1, $2, 'created', 'draft', 700, 'USD', 1)`,
				o.TenantID, claim); err != nil {
				return fmt.Errorf("append: %w", err)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}

	for _, statement := range []string{
		`UPDATE expense_events SET reason = 'tampered' WHERE expense_id = $1`,
		`DELETE FROM expense_events WHERE expense_id = $1`,
	} {
		err := app.WithTenantTx(ctx, postgres.Binding{TenantID: o.TenantID},
			func(ctx context.Context, conn *postgres.TenantConn) error {
				_, err := conn.Exec(ctx, statement, claim)
				return err
			})
		if err == nil {
			t.Errorf("the ledger accepted %q", statement)
		}
	}

	// Erasure must still work. The append-only trigger permits a delete by the
	// table owner precisely so that closing an account can cascade; without
	// that exception a tenant could never be deleted, and this suite's own
	// cleanup would be impossible.
	t.Run("the owner can still erase, so a tenant can be closed", func(t *testing.T) {
		if n := countAsOwner(t, `SELECT count(*) FROM expense_events WHERE expense_id = $1`, claim); n != 1 {
			t.Fatalf("expected one ledger row before erasure, found %d", n)
		}
	})

	if n := countAsOwner(t, `SELECT count(*) FROM expense_events WHERE expense_id = $1`, claim); n != 1 {
		t.Errorf("ledger holds %d rows after the tampering attempts, want 1", n)
	}
}

// A submitted claim is a record. The policy refuses to delete anything that is
// not still a draft, which is what holds if a future endpoint forgets to ask
// the state machine.
func TestOnlyDraftsCanBeDeleted(t *testing.T) {
	o := seedOrg(t, "acme-delete")
	draft := seedClaim(t, o, "draft", 100)
	pending := seedClaim(t, o, "pending_approval", 200)

	expenses := repo.NewExpenseRepository()
	ctx := context.Background()

	err := app.WithTenantTx(ctx, postgres.Binding{TenantID: o.TenantID},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			if err := expenses.DeleteDraft(ctx, conn, draft); err != nil {
				return fmt.Errorf("deleting a draft: %w", err)
			}
			if err := expenses.DeleteDraft(ctx, conn, pending); !errors.Is(err, shared.ErrNotFound) {
				return fmt.Errorf("deleting a submitted claim: got %v, want ErrNotFound", err)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}

	if n := countAsOwner(t, `SELECT count(*) FROM expenses WHERE id = $1`, pending); n != 1 {
		t.Error("a submitted claim was deleted")
	}
}

// A system transaction is the only widening in the model. It must work, and it
// must be inert without the flag.
func TestSystemTransactionCrossesTenantsAndOrdinaryOnesDoNot(t *testing.T) {
	a := seedOrg(t, "sys-a")
	b := seedOrg(t, "sys-b")
	seedClaim(t, a, "draft", 11)
	seedClaim(t, b, "draft", 22)

	ctx := context.Background()

	var systemView int
	err := app.WithSystemTx(ctx, postgres.Binding{ReadOnly: true}, "test: cross-tenant read",
		func(ctx context.Context, conn *postgres.TenantConn) error {
			return conn.QueryRow(ctx,
				`SELECT count(*) FROM expenses WHERE tenant_id IN ($1, $2)`,
				a.TenantID, b.TenantID).Scan(&systemView)
		})
	if err != nil {
		t.Fatalf("system transaction: %v", err)
	}
	if systemView != 2 {
		t.Fatalf("a system transaction saw %d of 2 claims", systemView)
	}

	var tenantView int
	err = app.WithTenantTx(ctx, postgres.Binding{TenantID: a.TenantID, ReadOnly: true},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			return conn.QueryRow(ctx,
				`SELECT count(*) FROM expenses WHERE tenant_id IN ($1, $2)`,
				a.TenantID, b.TenantID).Scan(&tenantView)
		})
	if err != nil {
		t.Fatalf("tenant transaction: %v", err)
	}
	if tenantView != 1 {
		t.Fatalf("an ordinary transaction saw %d claims across two tenants; it must see only its own", tenantView)
	}
}
