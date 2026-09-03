//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mlkad/b2b-expense-tracker/internal/domain/billing"
	"github.com/mlkad/b2b-expense-tracker/internal/gateway"
	"github.com/mlkad/b2b-expense-tracker/internal/platform/postgres"
	repo "github.com/mlkad/b2b-expense-tracker/internal/repository/postgres"
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

// The recurring sweep must materialise one claim per charge and no more,
// however many times it runs.
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

	budgets := repo.NewBudgetRepository()

	// Insert the same charge twice, as two sweeps would if the advance were
	// ever lost. The partial unique index has to refuse the second.
	for attempt := 0; attempt < 2; attempt++ {
		err := app.WithSystemTx(ctx, postgres.Binding{TenantID: o.TenantID}, "test: recurring sweep",
			func(ctx context.Context, conn *postgres.TenantConn) error {
				var subID uuid.UUID
				if err := conn.QueryRow(ctx,
					`SELECT id FROM vendor_subscriptions WHERE tenant_id = $1`, o.TenantID).Scan(&subID); err != nil {
					return err
				}
				_, err := conn.Exec(ctx, `
					INSERT INTO expenses
					  (tenant_id, submitter_id, department_id, status, category, amount_minor,
					   currency, merchant, spent_at, source_subscription_id)
					VALUES ($1, $2, $3, 'draft', 'software', 4500, 'USD', 'Figma', $4, $5)`,
					o.TenantID, o.Submitter, o.Department, due, subID)
				return err
			})

		if attempt == 0 && err != nil {
			t.Fatalf("first materialisation: %v", err)
		}
		if attempt == 1 && err == nil {
			t.Fatal("a second claim was created for the same charge date")
		}
	}

	if n := countAsOwner(t,
		`SELECT count(*) FROM expenses WHERE tenant_id = $1 AND source_subscription_id IS NOT NULL`,
		o.TenantID); n != 1 {
		t.Fatalf("the sweep produced %d claims for one charge, want 1", n)
	}

	_ = budgets
}
