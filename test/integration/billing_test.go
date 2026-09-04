//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/billing"
	"github.com/mlkad/b2b-expense-tracker/internal/gateway"
	"github.com/mlkad/b2b-expense-tracker/internal/notify"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
	"github.com/mlkad/b2b-expense-tracker/internal/worker"
)

func linkBilling(t *testing.T, tenantID uuid.UUID, ref string) {
	t.Helper()
	err := app.WithSystemTx(context.Background(),
		postgres.Binding{TenantID: tenantID}, "test: link billing reference",
		func(ctx context.Context, conn *postgres.TenantConn) error {
			_, err := conn.Exec(ctx, `UPDATE tenants SET billing_customer_ref = $2 WHERE id = $1`, tenantID, ref)
			return err
		})
	if err != nil {
		t.Fatalf("link billing ref: %v", err)
	}
}

// A tenant must not be able to write its own subscription row. If it could, it
// could grant itself the enterprise plan.
func TestTenantCannotGrantItselfAPlan(t *testing.T) {
	o := seedOrg(t, "billing-selfgrant")

	err := app.WithTenantTx(context.Background(), postgres.Binding{TenantID: o.TenantID},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			_, err := conn.Exec(ctx, `
				INSERT INTO tenant_subscriptions
				  (tenant_id, gateway_subscription_id, gateway_customer_ref, plan_code, status,
				   current_period_start, current_period_end)
				VALUES ($1, 'sub_forged', 'cus_forged', 'enterprise', 'active', now(), now() + interval '30 days')`,
				o.TenantID)
			return err
		})
	if err == nil {
		t.Fatal("a tenant wrote its own subscription row")
	}

	if n := countAsOwner(t, `SELECT count(*) FROM tenant_subscriptions WHERE tenant_id = $1`, o.TenantID); n != 0 {
		t.Fatalf("the forged subscription row was written anyway (%d rows)", n)
	}
}

// The projection is readable by the tenant it belongs to, and by nobody else.
func TestEntitlementIsReadableAndIsolated(t *testing.T) {
	a := seedOrg(t, "billing-read-a")
	b := seedOrg(t, "billing-read-b")

	billingRepo := repo.NewBillingRepository()
	ctx := context.Background()

	// A tenant with no subscription is on free, not an error.
	var free billing.Entitlement
	err := app.WithTenantTx(ctx, postgres.Binding{TenantID: a.TenantID, ReadOnly: true},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			var err error
			free, err = billingRepo.GetEntitlement(ctx, conn)
			return err
		})
	if err != nil {
		t.Fatalf("read entitlement: %v", err)
	}
	if free.Known || free.EffectivePlan() != billing.PlanFree {
		t.Fatalf("a tenant with no subscription resolved to %+v", free)
	}

	// Give A a growth subscription through the system path.
	now := time.Now().UTC()
	if err := billingRepo.ApplySubscription(ctx, app, a.TenantID, repo.SubscriptionState{
		GatewaySubscriptionID: "sub_" + a.Slug,
		GatewayCustomerRef:    a.TenantID.String(),
		PlanCode:              string(billing.PlanGrowth),
		Status:                string(billing.StatusActive),
		Seats:                 25,
		CurrentPeriodStart:    now,
		CurrentPeriodEnd:      now.Add(30 * 24 * time.Hour),
		EventID:               "evt_1",
		EventAt:               now,
	}); err != nil {
		t.Fatalf("apply subscription: %v", err)
	}

	var got billing.Entitlement
	err = app.WithTenantTx(ctx, postgres.Binding{TenantID: a.TenantID, ReadOnly: true},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			var err error
			got, err = billingRepo.GetEntitlement(ctx, conn)
			return err
		})
	if err != nil {
		t.Fatalf("read entitlement: %v", err)
	}
	if got.EffectivePlan() != billing.PlanGrowth || got.Limits().Seats != 25 {
		t.Fatalf("entitlement = %+v, limits %+v", got, got.Limits())
	}

	// B must not see A's plan.
	var neighbour billing.Entitlement
	err = app.WithTenantTx(ctx, postgres.Binding{TenantID: b.TenantID, ReadOnly: true},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			var err error
			neighbour, err = billingRepo.GetEntitlement(ctx, conn)
			return err
		})
	if err != nil {
		t.Fatalf("read neighbour entitlement: %v", err)
	}
	if neighbour.Known {
		t.Fatal("one tenant can read another's subscription")
	}
}

// The gateway forwards Stripe's stream, which is unordered and at-least-once.
// A redelivered older event must not overwrite newer state.
func TestOutOfOrderEventsDoNotRegress(t *testing.T) {
	o := seedOrg(t, "billing-ordering")
	billingRepo := repo.NewBillingRepository()
	ctx := context.Background()

	base := time.Now().UTC()
	apply := func(status string, at time.Time, eventID string) error {
		return billingRepo.ApplySubscription(ctx, app, o.TenantID, repo.SubscriptionState{
			GatewaySubscriptionID: "sub_ordering",
			GatewayCustomerRef:    o.TenantID.String(),
			PlanCode:              "growth",
			Status:                status,
			Seats:                 5,
			CurrentPeriodStart:    base,
			CurrentPeriodEnd:      base.Add(30 * 24 * time.Hour),
			EventID:               eventID,
			EventAt:               at,
		})
	}

	if err := apply("active", base, "evt_active"); err != nil {
		t.Fatalf("first event: %v", err)
	}
	if err := apply("canceled", base.Add(time.Hour), "evt_cancel"); err != nil {
		t.Fatalf("second event: %v", err)
	}

	// The redelivery: an hour older than what has been applied.
	err := apply("active", base, "evt_active")
	if !errors.Is(err, repo.ErrStaleEvent) {
		t.Fatalf("a stale redelivery returned %v, want ErrStaleEvent", err)
	}

	var status string
	if err := app.WithTenantTx(ctx, postgres.Binding{TenantID: o.TenantID, ReadOnly: true},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			return conn.QueryRow(ctx,
				`SELECT status FROM tenant_subscriptions WHERE tenant_id = $1`, o.TenantID).Scan(&status)
		}); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "canceled" {
		t.Fatalf("status is %q; a redelivered old event resurrected stale state", status)
	}
}

// The delivery ledger must accept an event id exactly once.
func TestRelayDeliveryIsClaimedOnce(t *testing.T) {
	billingRepo := repo.NewBillingRepository()
	ctx := context.Background()
	eventID := "evt_" + uuid.NewString()
	payload := []byte(`{"id":"x"}`)

	if err := billingRepo.ClaimDelivery(ctx, app, eventID, "subscription.updated", payload); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	err := billingRepo.ClaimDelivery(ctx, app, eventID, "subscription.updated", payload)
	if !errors.Is(err, repo.ErrDuplicateDelivery) {
		t.Fatalf("second claim returned %v, want ErrDuplicateDelivery", err)
	}
}

// The relay's signature scheme, end to end. Sign is what the gateway would do;
// Verify is what this service does.
func TestRelaySignatureRoundTrip(t *testing.T) {
	secret := "an-integration-test-secret-of-sufficient-length"
	relay, err := gateway.NewRelay(secret, gateway.DefaultTolerance)
	if err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(gateway.Event{
		ID:        "evt_roundtrip",
		Type:      gateway.EventSubscriptionUpdated,
		CreatedAt: time.Now().UTC(),
		TenantRef: uuid.NewString(),
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	header := relay.Sign(body, now)

	if _, err := relay.Verify(header, body, now); err != nil {
		t.Fatalf("a genuine delivery was rejected: %v", err)
	}

	t.Run("a tampered body is rejected", func(t *testing.T) {
		tampered := append([]byte(nil), body...)
		tampered[len(tampered)-2] ^= 0xFF
		if _, err := relay.Verify(header, tampered, now); !errors.Is(err, gateway.ErrSignatureInvalid) {
			t.Fatalf("got %v, want ErrSignatureInvalid", err)
		}
	})

	t.Run("a replayed delivery is rejected once it is old enough", func(t *testing.T) {
		// The timestamp is inside the signed payload, so an old delivery
		// cannot be re-dated without breaking the signature.
		if _, err := relay.Verify(header, body, now.Add(gateway.DefaultTolerance+time.Minute)); !errors.Is(err, gateway.ErrSignatureExpired) {
			t.Fatalf("got %v, want ErrSignatureExpired", err)
		}
	})

	t.Run("a signature from another secret is rejected", func(t *testing.T) {
		other, err := gateway.NewRelay("a-different-secret-of-sufficient-length!!", gateway.DefaultTolerance)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := relay.Verify(other.Sign(body, now), body, now); !errors.Is(err, gateway.ErrSignatureInvalid) {
			t.Fatalf("got %v, want ErrSignatureInvalid", err)
		}
	})
}

// The sweep sends nothing; a notifier is only required to construct the
// handlers.
type silentNotifier struct{}

func (silentNotifier) ExpenseTransition(context.Context, notify.ExpenseEvent) error { return nil }
func (silentNotifier) BudgetThreshold(context.Context, notify.BudgetEvent) error    { return nil }

// The recurring sweep must materialise one claim per charge and no more,
// however many times it runs - and a repeat run must still advance the
// subscription's charge date.
//
// This drives the worker handler. An earlier version of this test inserted the
// rows itself with raw SQL, so it proved the partial unique index worked and
// nothing about the code that was supposed to rely on it. The handler was in
// fact broken twice over the whole time this test was green: it ran in a
// transaction bound to no tenant, so every insert failed a foreign key check,
// and the duplicate path aborted the transaction so the date never moved.
func TestRecurringSweepIsIdempotent(t *testing.T) {
	o := seedOrg(t, "recurring-sweep")
	ctx := context.Background()

	due := time.Now().UTC().Truncate(24 * time.Hour)

	err := app.WithTenantTx(ctx, postgres.Binding{TenantID: o.TenantID},
		func(ctx context.Context, conn *postgres.TenantConn) error {
			_, err := conn.Exec(ctx, `
				INSERT INTO vendor_subscriptions
				  (tenant_id, vendor, department_id, owner_id, amount_minor, currency,
				   cadence, next_charge_on, auto_create_expense)
				VALUES ($1, 'Figma', $2, $3, 4500, 'USD', 'monthly', $4, TRUE)`,
				o.TenantID, o.Department, o.Submitter, due)
			return err
		})
	if err != nil {
		t.Fatalf("seed vendor subscription: %v", err)
	}

	// A nil queue keeps the sweep on its direct path rather than fanning out
	// into jobs no worker is running here.
	handlers := worker.NewHandlers(
		app, repo.NewExpenseRepository(), repo.NewBudgetRepository(),
		repo.NewTenancyRepository(), repo.NewOrgRepository(), nil, nil,
		silentNotifier{}, slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	chargeDate := func() time.Time {
		t.Helper()
		var next time.Time
		if err := app.WithSystemTx(ctx, postgres.Binding{TenantID: o.TenantID}, "test: read charge date",
			func(ctx context.Context, conn *postgres.TenantConn) error {
				return conn.QueryRow(ctx,
					`SELECT next_charge_on FROM vendor_subscriptions WHERE tenant_id = $1`,
					o.TenantID).Scan(&next)
			}); err != nil {
			t.Fatalf("read charge date: %v", err)
		}
		return next
	}

	if err := handlers.HandleRecurringSweep(ctx, nil); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	afterFirst := chargeDate()
	if !afterFirst.After(due) {
		t.Fatalf("the first sweep left the charge date at %s", afterFirst.Format(time.DateOnly))
	}

	// Put the subscription back on today's charge, as a sweep would see it if
	// the advance had been lost to a crash between the insert and the update.
	// The second run must recognise its own earlier work and move on, not fail.
	if err := app.WithSystemTx(ctx, postgres.Binding{TenantID: o.TenantID}, "test: rewind charge date",
		func(ctx context.Context, conn *postgres.TenantConn) error {
			_, err := conn.Exec(ctx,
				`UPDATE vendor_subscriptions SET next_charge_on = $2, last_generated_on = NULL
				 WHERE tenant_id = $1`, o.TenantID, due)
			return err
		}); err != nil {
		t.Fatalf("rewind: %v", err)
	}

	if err := handlers.HandleRecurringSweep(ctx, nil); err != nil {
		t.Fatalf("the repeat sweep failed instead of skipping the duplicate: %v", err)
	}

	if n := countAsOwner(t,
		`SELECT count(*) FROM expenses WHERE tenant_id = $1 AND source_subscription_id IS NOT NULL`,
		o.TenantID); n != 1 {
		t.Fatalf("the sweep produced %d claims for one charge, want 1", n)
	}

	// The point of the second assertion: the duplicate must not leave the
	// subscription stuck on a date it has already charged, or it is retried
	// every day for ever.
	if after := chargeDate(); !after.After(due) {
		t.Fatalf("the repeat sweep left the charge date at %s, so the subscription is stuck",
			after.Format(time.DateOnly))
	}
}
