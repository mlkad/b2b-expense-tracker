//go:build integration

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
)

// org is a seeded organisation with one member of each role that matters.
type org struct {
	TenantID   uuid.UUID
	Slug       string
	Department uuid.UUID

	Submitter uuid.UUID // membership ids, not user ids
	Manager   uuid.UUID
	Finance   uuid.UUID
}

// seedOrg creates an organisation as the owner - that is, with RLS bypassed.
//
// Fixtures are written past the policies on purpose. Setting up two tenants'
// data through the policies would require the very isolation the tests are
// about to check, so a failure in the policies would show up as a failure to
// build the fixture rather than as a failed assertion.
func seedOrg(t *testing.T, slug string) org {
	t.Helper()

	db, err := sql.Open("pgx", ownerDSN)
	if err != nil {
		t.Fatalf("open owner connection: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	o := org{Slug: slug}

	if err := db.QueryRowContext(ctx,
		`INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id`,
		slug, slug+" Ltd",
	).Scan(&o.TenantID); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	if err := db.QueryRowContext(ctx,
		`INSERT INTO departments (tenant_id, name) VALUES ($1, 'Engineering') RETURNING id`,
		o.TenantID,
	).Scan(&o.Department); err != nil {
		t.Fatalf("seed department: %v", err)
	}

	o.Submitter = seedMember(t, db, o, "member", tenant.RoleMember, nil)
	o.Manager = seedMember(t, db, o, "manager", tenant.RoleManager, &o.Department)
	o.Finance = seedMember(t, db, o, "finance", tenant.RoleFinance, nil)

	t.Cleanup(func() { deleteOrg(t, o.TenantID) })
	return o
}

func seedMember(t *testing.T, db *sql.DB, o org, label string, role tenant.Role, dept *uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()

	var userID uuid.UUID
	email := fmt.Sprintf("%s-%s@example.test", label, o.Slug)
	if err := db.QueryRowContext(ctx,
		`INSERT INTO users (email, full_name) VALUES ($1, $2) RETURNING id`,
		email, label,
	).Scan(&userID); err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}

	var membershipID uuid.UUID
	if err := db.QueryRowContext(ctx,
		`INSERT INTO memberships (tenant_id, user_id, role, status, department_id)
		 VALUES ($1, $2, $3, 'active', $4) RETURNING id`,
		o.TenantID, userID, string(role), dept,
	).Scan(&membershipID); err != nil {
		t.Fatalf("seed membership %s: %v", label, err)
	}
	return membershipID
}

// seedClaim inserts an expense directly, for tests that need a starting state
// the state machine would take several steps to reach.
func seedClaim(t *testing.T, o org, status string, minor int64) uuid.UUID {
	t.Helper()

	db, err := sql.Open("pgx", ownerDSN)
	if err != nil {
		t.Fatalf("open owner connection: %v", err)
	}
	defer db.Close()

	var (
		submitted, decided *time.Time
		decidedBy          *uuid.UUID
		paid               *time.Time
		now                = time.Now().UTC()
	)
	switch status {
	case "pending_approval":
		submitted = &now
	case "approved", "rejected":
		submitted, decided, decidedBy = &now, &now, &o.Manager
	case "paid":
		submitted, decided, decidedBy, paid = &now, &now, &o.Manager, &now
	}

	var paymentRef *string
	if status == "paid" {
		ref := "seed-ref"
		paymentRef = &ref
	}

	// status is cast explicitly. Without the cast PostgreSQL has to deduce the
	// parameter's type from its uses, and a parameter used both as an enum
	// column value and in a text comparison deduces inconsistently (42P08).
	var id uuid.UUID
	err = db.QueryRow(
		`INSERT INTO expenses
		   (tenant_id, submitter_id, department_id, status, amount_minor, currency,
		    merchant, spent_at, submitted_at, decided_at, decided_by, paid_at, payment_ref)
		 VALUES ($1, $2, $3, $4::expense_status, $5, 'USD', 'Figma', CURRENT_DATE,
		         $6, $7, $8, $9, $10)
		 RETURNING id`,
		o.TenantID, o.Submitter, o.Department, status, minor,
		submitted, decided, decidedBy, paid, paymentRef,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed claim (%s): %v", status, err)
	}
	return id
}

// deleteOrg removes a tenant and everything cascading from it, so tests do not
// see each other's rows. Truncating the whole schema between tests would be
// simpler and would forbid running them in parallel.
func deleteOrg(t *testing.T, tenantID uuid.UUID) {
	t.Helper()

	db, err := sql.Open("pgx", ownerDSN)
	if err != nil {
		t.Fatalf("open owner connection: %v", err)
	}
	defer db.Close()

	// The tenant goes first. Its memberships, departments and claims cascade
	// from it, and expenses_submitter_fk is ON DELETE RESTRICT - so deleting
	// the users first would be refused by the very constraint that stops a
	// hard user delete from destroying billing history in production.
	var userIDs []uuid.UUID
	rows, err := db.Query(`SELECT user_id FROM memberships WHERE tenant_id = $1`, tenantID)
	if err != nil {
		t.Fatalf("cleanup: list users: %v", err)
	}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatalf("cleanup: scan user: %v", err)
		}
		userIDs = append(userIDs, id)
	}
	rows.Close()

	if _, err := db.Exec(`DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
		t.Fatalf("cleanup tenant: %v", err)
	}
	for _, id := range userIDs {
		if _, err := db.Exec(`DELETE FROM users WHERE id = $1`, id); err != nil {
			t.Fatalf("cleanup user: %v", err)
		}
	}
}

// countAsOwner reads past every policy, to prove a row does or does not exist
// regardless of what the application role can see.
func countAsOwner(t *testing.T, query string, args ...any) int {
	t.Helper()

	db, err := sql.Open("pgx", ownerDSN)
	if err != nil {
		t.Fatalf("open owner connection: %v", err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count as owner: %v", err)
	}
	return n
}
