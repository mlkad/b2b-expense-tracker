//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/expense"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/shared"
	"github.com/mlkad/b2b-expense-tracker/internal/domain/tenant"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
)

func actorFor(o orgFixture, membershipID string, role tenant.Role, dept bool) tenant.Actor {
	a := tenant.Actor{
		TenantID:     o.TenantID,
		Role:         role,
		Status:       tenant.MembershipActive,
		TenantStatus: tenant.StatusActive,
	}
	switch membershipID {
	case "submitter":
		a.MembershipID = o.Submitter
	case "manager":
		a.MembershipID = o.Manager
	case "finance":
		a.MembershipID = o.Finance
	}
	if dept {
		d := o.Department
		a.DepartmentID = &d
	}
	return a
}

// The full lifecycle, persisted. The state machine is unit tested in the
// domain package; what this checks is that every state it produces is one the
// database accepts - in particular that expenses_status_timestamps_chk agrees
// with what commit() writes.
func TestLifecyclePersists(t *testing.T) {
	o := seedOrg(t, "flow-happy")
	expenses := repo.NewExpenseRepository()
	ctx := context.Background()

	submitter := actorFor(o, "submitter", tenant.RoleMember, false)
	manager := actorFor(o, "manager", tenant.RoleManager, true)
	finance := actorFor(o, "finance", tenant.RoleFinance, false)

	var claimID string

	// Create.
	err := app.WithTenantTx(ctx, postgres.Binding{TenantID: o.TenantID, ActorID: o.Submitter},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			dept := o.Department
			claim, event, err := expense.New(o.TenantID, o.Submitter, expense.Draft{
				DepartmentID: &dept,
				Category:     expense.CategorySoftware,
				Amount:       shared.Money{Minor: 12_500, Currency: "USD"},
				Merchant:     "Figma",
				SpentAt:      time.Now().UTC().AddDate(0, 0, -2),
			}, time.Now().UTC())
			if err != nil {
				return err
			}
			if err := expenses.Create(ctx, conn, claim, event); err != nil {
				return err
			}
			claimID = claim.ID.String()
			return nil
		})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	steps := []struct {
		action     expense.Action
		actor      tenant.Actor
		reason     *string
		paymentRef *string
		want       expense.Status
	}{
		{expense.ActionSubmit, submitter, nil, nil, expense.StatusPendingApproval},
		{expense.ActionApprove, manager, nil, nil, expense.StatusApproved},
		{expense.ActionPay, finance, nil, strptr("BACS-0001"), expense.StatusPaid},
	}

	parsed := uuid.MustParse(claimID)

	for _, step := range steps {
		err := app.WithTenantTx(ctx, postgres.Binding{TenantID: o.TenantID, ActorID: step.actor.MembershipID},
			func(ctx context.Context, conn *postgres.TenantConn) error {
				claim, err := expenses.GetForUpdate(ctx, conn, parsed)
				if err != nil {
					return err
				}
				expected := claim.Version

				event, err := claim.Apply(expense.Command{
					Action: step.action, Actor: step.actor,
					Reason: step.reason, PaymentRef: step.paymentRef,
				}, time.Now().UTC())
				if err != nil {
					return err
				}
				if err := expenses.Save(ctx, conn, claim, event, expected); err != nil {
					return err
				}
				if claim.Status != step.want {
					return fmt.Errorf("after %s the claim is %s, want %s", step.action, claim.Status, step.want)
				}
				return nil
			})
		if err != nil {
			t.Fatalf("%s: %v", step.action, err)
		}
	}

	// Four ledger rows: created, submitted, approved, paid. The ledger is
	// written in the same transaction as each update, so a count that
	// disagrees means one of them was not atomic.
	if n := countAsOwner(t, `SELECT count(*) FROM expense_events WHERE expense_id = $1`, claimID); n != 4 {
		t.Errorf("ledger holds %d rows, want 4", n)
	}
}

// Two approvers clicking at the same moment must produce one approval, not
// two ledger rows and not a lost update.
//
// The guarantee comes from two mechanisms and this test would pass with either
// one removed - which is why the assertion is on the ledger as well as on the
// error: FOR UPDATE NOWAIT serialises the read-decide-write, and the
// compare-and-swap on version catches anything that gets past it.
func TestConcurrentApprovalsProduceOneDecision(t *testing.T) {
	const rounds = 8

	for round := 0; round < rounds; round++ {
		o := seedOrg(t, fmt.Sprintf("race-%d", round))
		claimID := seedClaim(t, o, "pending_approval", 5_000)

		expenses := repo.NewExpenseRepository()
		approvers := []tenant.Actor{
			actorFor(o, "manager", tenant.RoleManager, true),
			actorFor(o, "finance", tenant.RoleFinance, false),
		}
		// Finance cannot approve, so give the second racer approval authority
		// without making it the same membership as the first.
		approvers[1].Role = tenant.RoleAdmin

		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make(chan error, len(approvers))

		for _, actor := range approvers {
			wg.Add(1)
			go func(a tenant.Actor) {
				defer wg.Done()
				<-start

				results <- app.WithTenantTx(context.Background(),
					postgres.Binding{TenantID: o.TenantID, ActorID: a.MembershipID},
					func(ctx context.Context, conn *postgres.TenantConn) error {
						claim, err := expenses.GetForUpdate(ctx, conn, claimID)
						if err != nil {
							return err
						}
						expected := claim.Version

						event, err := claim.Apply(expense.Command{Action: expense.ActionApprove, Actor: a},
							time.Now().UTC())
						if err != nil {
							return err
						}
						return expenses.Save(ctx, conn, claim, event, expected)
					})
			}(actor)
		}

		close(start)
		wg.Wait()
		close(results)

		var succeeded, refused int
		for err := range results {
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, shared.ErrConflict), errors.Is(err, shared.ErrStaleWrite):
				refused++
			default:
				t.Fatalf("round %d: unexpected error: %v", round, err)
			}
		}

		if succeeded != 1 {
			t.Fatalf("round %d: %d approvals succeeded, want exactly 1", round, succeeded)
		}
		if refused != 1 {
			t.Fatalf("round %d: %d approvals were refused, want exactly 1", round, refused)
		}

		if n := countAsOwner(t,
			`SELECT count(*) FROM expense_events WHERE expense_id = $1 AND action = 'approved'`, claimID); n != 1 {
			t.Fatalf("round %d: ledger holds %d approvals, want 1", round, n)
		}
	}
}

// The database must refuse a row whose timestamps contradict its status, even
// if a bug in the state machine produced one.
func TestStatusTimestampConstraintHolds(t *testing.T) {
	o := seedOrg(t, "constraint-check")
	claimID := seedClaim(t, o, "draft", 900)

	err := app.WithTenantTx(context.Background(), postgres.Binding{TenantID: o.TenantID},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			// Approved with no decision recorded: exactly the row a state
			// machine bug would write.
			_, err := conn.Exec(ctx,
				`UPDATE expenses SET status = 'approved' WHERE id = $1`, claimID)
			return err
		})
	if err == nil {
		t.Fatal("the database accepted an approved claim with no decided_at or decided_by")
	}

	err = app.WithTenantTx(context.Background(), postgres.Binding{TenantID: o.TenantID},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			_, err := conn.Exec(ctx,
				`UPDATE expenses SET status = 'paid', submitted_at = now(), decided_at = now(),
				        decided_by = $2, paid_at = NULL WHERE id = $1`,
				claimID, o.Manager)
			return err
		})
	if err == nil {
		t.Fatal("the database accepted a paid claim with no paid_at")
	}
}

// The export query is the one statement sqlc does not generate, so nothing
// else would notice a column renamed in a migration.
func TestExportQueryMatchesSchema(t *testing.T) {
	o := seedOrg(t, "export-schema")
	seedClaim(t, o, "approved", 3_300)
	seedClaim(t, o, "paid", 4_400)

	expenses := repo.NewExpenseRepository()

	var rows []repo.ExportRow
	err := app.WithTenantTx(context.Background(),
		postgres.Binding{TenantID: o.TenantID, ReadOnly: true},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			return expenses.StreamForExport(ctx, conn, repo.Filter{}, func(row repo.ExportRow) error {
				rows = append(rows, row)
				return nil
			})
		})
	if err != nil {
		t.Fatalf("stream export: %v", err)
	}

	if len(rows) != 2 {
		t.Fatalf("streamed %d rows, want 2", len(rows))
	}
	for _, row := range rows {
		if row.Merchant == "" || row.Amount.Currency == "" {
			t.Errorf("a scanned column came back empty: %+v", row)
		}
		if row.DepartmentName == nil || *row.DepartmentName != "Engineering" {
			t.Errorf("the department join did not resolve: %+v", row.DepartmentName)
		}
		if row.SubmitterEmail == nil {
			t.Error("the submitter join did not resolve")
		}
	}
}

// Returning an error from the yield stops the walk. That is how a client
// disconnecting mid-download stops the scan rather than streaming a hundred
// thousand rows into a closed socket.
func TestExportStopsWhenTheConsumerStops(t *testing.T) {
	o := seedOrg(t, "export-abort")
	for i := 0; i < 5; i++ {
		seedClaim(t, o, "approved", int64(100+i))
	}

	sentinel := errors.New("consumer went away")
	seen := 0

	err := app.WithTenantTx(context.Background(),
		postgres.Binding{TenantID: o.TenantID, ReadOnly: true},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			return repo.NewExpenseRepository().StreamForExport(ctx, conn, repo.Filter{},
				func(repo.ExportRow) error {
					seen++
					if seen == 2 {
						return sentinel
					}
					return nil
				})
		})

	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want the consumer's own error", err)
	}
	if seen != 2 {
		t.Fatalf("the walk continued past the consumer's refusal: %d rows", seen)
	}
}

func strptr(s string) *string { return &s }
